package auth

// SSH keys (TODO 46): the public keys a person registers to reach their
// runs' workspaces through the engine's SSH door. One authorized_keys line
// each, normalised on the way in; the fingerprint is the identity the door
// looks an offered key up by, unique across the instance like a token's
// hash. Keys outlive a disabled account's sessions — the door checks the
// account live, so re-enabling restores access without re-registering.

import (
	"bytes"
	"context"
	"crypto/rsa"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

// sshKeyNameMax bounds a key's label.
const sshKeyNameMax = 64

// AddSSHKey registers a public key for a user: the contents of a .pub file
// (type, base64, optional comment). Certificates are not keys; an RSA key
// shorter than 2048 bits is refused. The name is the one given, else the
// key's comment, else its type.
func (s *Service) AddSSHKey(ctx context.Context, userID, name, authorized string) (SSHKey, error) {
	line := strings.TrimSpace(authorized)
	switch {
	case line == "":
		return SSHKey{}, invalidf("a public key is required: the contents of a .pub file, e.g. ssh-ed25519 AAAA… you@laptop")
	case strings.ContainsAny(line, "\r\n"):
		return SSHKey{}, invalidf("one key at a time: paste a single line")
	}
	pk, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return SSHKey{}, invalidf("not an OpenSSH public key (%v)", err)
	}
	if _, isCert := pk.(*ssh.Certificate); isCert {
		return SSHKey{}, invalidf("a certificate is not a key the door accepts: register the key it certifies")
	}
	if cpk, ok := pk.(ssh.CryptoPublicKey); ok {
		if rsaKey, ok := cpk.CryptoPublicKey().(*rsa.PublicKey); ok && rsaKey.N.BitLen() < 2048 {
			return SSHKey{}, invalidf("an RSA key shorter than 2048 bits is refused")
		}
	}
	if _, err := s.db.GetUser(ctx, userID); err != nil {
		return SSHKey{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(comment)
	}
	if name == "" {
		name = pk.Type()
	}
	if utf8.RuneCountInString(name) > sshKeyNameMax {
		return SSHKey{}, invalidf("the name is longer than %d characters", sshKeyNameMax)
	}
	k := SSHKey{
		ID: s.newID(), UserID: userID, Name: name, Type: pk.Type(),
		PublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))),
		Fingerprint: ssh.FingerprintSHA256(pk),
		CreatedAt:   s.now().UTC(),
	}
	if err := s.db.CreateSSHKey(ctx, k); err != nil {
		return SSHKey{}, err
	}
	return k, nil
}

func (s *Service) ListSSHKeys(ctx context.Context, userID string) ([]SSHKey, error) {
	return s.db.ListSSHKeys(ctx, userID)
}

func (s *Service) DeleteSSHKey(ctx context.Context, userID, id string) error {
	return s.db.DeleteSSHKey(ctx, userID, id)
}

// AuthenticateSSHKey resolves an offered key to its owner: a key the
// instance knows byte for byte, whose account is not disabled.
// ErrUnauthorized for anything else, ErrDisabled for a disabled account —
// the door tells neither apart to the client.
func (s *Service) AuthenticateSSHKey(ctx context.Context, key ssh.PublicKey) (User, SSHKey, error) {
	k, err := s.db.GetSSHKeyByFingerprint(ctx, ssh.FingerprintSHA256(key))
	if errors.Is(err, ErrNotFound) {
		return User{}, SSHKey{}, ErrUnauthorized
	}
	if err != nil {
		return User{}, SSHKey{}, err
	}
	stored, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.PublicKey))
	if err != nil || !bytes.Equal(stored.Marshal(), key.Marshal()) {
		return User{}, SSHKey{}, ErrUnauthorized
	}
	u, err := s.db.GetUser(ctx, k.UserID)
	if err != nil {
		return User{}, SSHKey{}, ErrUnauthorized
	}
	if u.Disabled() {
		return User{}, SSHKey{}, ErrDisabled
	}
	return u, k, nil
}

// TouchSSHKey records a use of the key.
func (s *Service) TouchSSHKey(ctx context.Context, id string, at time.Time) error {
	return s.db.TouchSSHKey(ctx, id, at)
}
