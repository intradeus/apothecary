package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// sshKeyFile is the key ssh-config makes for the engine's SSH door, under
// ~/.ssh; its public half is beside it, with .pub.
const sshKeyFile = "apothecary_ed25519"

// machineKey is this machine's key for the door.
type machineKey struct {
	path, comment string
	pub           ssh.PublicKey
	// created: made by this run; tightened: it was readable by others
	// and is now its owner's only.
	created, tightened bool
}

// authorizedLine is the key as a .pub file holds it.
func authorizedLine(pub ssh.PublicKey, comment string) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " " + comment
}

// loadOrCreateKey is the key at dir/apothecary_ed25519: the one there,
// never overwritten, else a new Ed25519 key without a passphrase, readable
// by its owner only. Its .pub is (re)written when missing or not its own.
func loadOrCreateKey(dir, comment string) (machineKey, error) {
	k := machineKey{path: filepath.Join(dir, sshKeyFile), comment: comment}
	f, err := openPrivate(k.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		k.pub, err = createKey(k.path, comment)
		k.created = true
	case err != nil:
	default:
		k.pub, k.tightened, err = readKey(f)
		f.Close()
	}
	if err != nil {
		return machineKey{}, err
	}
	pubPath := k.path + ".pub"
	if old, err := os.ReadFile(pubPath); err == nil {
		if pk, _, _, _, err := ssh.ParseAuthorizedKey(old); err == nil && bytes.Equal(pk.Marshal(), k.pub.Marshal()) {
			return k, nil
		}
	}
	if err := os.WriteFile(pubPath, []byte(authorizedLine(k.pub, comment)+"\n"), 0o644); err != nil {
		return machineKey{}, err
	}
	return k, nil
}

// createKey writes a new key to path, which must not exist (a planted
// symlink is refused too), owner-only from its creation.
func createKey(path, comment string) (ssh.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, err
	}
	f, err := createPrivate(path)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(pem.EncodeToMemory(block))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	return ssh.NewPublicKey(pub)
}

// readKey is the public half of the key f holds, which it leaves owner-only.
// A key with a passphrase is read through the public key it carries.
func readKey(f *os.File) (ssh.PublicKey, bool, error) {
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil, false, err
	}
	var pub ssh.PublicKey
	raw, err := ssh.ParseRawPrivateKey(data)
	var locked *ssh.PassphraseMissingError
	switch {
	case errors.As(err, &locked) && locked.PublicKey != nil:
		pub = locked.PublicKey
	case err == nil:
		var signer ssh.Signer
		if signer, err = ssh.NewSignerFromKey(raw); err == nil {
			pub = signer.PublicKey()
		}
	}
	if pub == nil {
		return nil, false, fmt.Errorf("%s is not a private key this client can read (%v): move it aside and run apothecary ssh-config again", f.Name(), err)
	}
	looser, err := restrictToOwner(f)
	return pub, looser, err
}

// identityFileArg is path as ssh_config's IdentityFile reads it: forward
// slashes (Windows' OpenSSH takes them), % escaped (ssh expands its
// tokens there), quoted when it holds a space.
func identityFileArg(path string) string {
	return quoteArg(strings.ReplaceAll(filepath.ToSlash(path), "%", "%%"))
}
