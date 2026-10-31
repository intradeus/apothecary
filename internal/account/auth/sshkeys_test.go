package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testKey(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return pk, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
}

func newKeyService(t *testing.T) (*Service, User, User) {
	t.Helper()
	s := New(NewMem())
	ctx := context.Background()
	pw := "correct horse battery staple"
	alice, err := s.Setup(ctx, NewUser{Handle: "alice", Email: "alice@example.com", Name: "Alice", Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.CreateUser(ctx, NewUser{Handle: "bob", Email: "bob@example.com", Name: "Bob", Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	return s, alice, bob
}

// A key is registered from its .pub line, named from its comment when no
// name is given, listed with its fingerprint, and refused twice.
func TestAddSSHKey(t *testing.T) {
	s, alice, bob := newKeyService(t)
	ctx := context.Background()
	pk, line := testKey(t)
	k, err := s.AddSSHKey(ctx, alice.ID, "", line+" alice@laptop")
	if err != nil {
		t.Fatal(err)
	}
	if k.Name != "alice@laptop" || k.Type != "ssh-ed25519" || k.PublicKey != line || k.Fingerprint != ssh.FingerprintSHA256(pk) || k.UserID != alice.ID {
		t.Errorf("key = %+v", k)
	}
	if _, err := s.AddSSHKey(ctx, bob.ID, "same key", line); !errors.Is(err, ErrConflict) {
		t.Errorf("a key registered twice: %v", err)
	}
	keys, _ := s.ListSSHKeys(ctx, alice.ID)
	if len(keys) != 1 || keys[0].ID != k.ID {
		t.Errorf("alice's keys = %+v", keys)
	}
	if keys, _ := s.ListSSHKeys(ctx, bob.ID); len(keys) != 0 {
		t.Errorf("bob's keys = %+v", keys)
	}
	// Bob cannot remove Alice's key; Alice can.
	if err := s.DeleteSSHKey(ctx, bob.ID, k.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign delete: %v", err)
	}
	if err := s.DeleteSSHKey(ctx, alice.ID, k.ID); err != nil {
		t.Error(err)
	}
}

func TestAddSSHKeyRefusals(t *testing.T) {
	s, alice, _ := newKeyService(t)
	ctx := context.Background()
	_, line := testKey(t)
	short, _ := rsa.GenerateKey(rand.Reader, 1024)
	shortPK, _ := ssh.NewPublicKey(&short.PublicKey)
	for _, c := range []struct{ name, key, want string }{
		{"empty", "   ", "a public key is required"},
		{"two lines", line + "\n" + line, "one key at a time"},
		{"garbage", "ssh-ed25519 notbase64", "not an OpenSSH public key"},
		{"private key", "-----BEGIN OPENSSH PRIVATE KEY-----", "not an OpenSSH public key"},
		{"short rsa", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(shortPK))), "shorter than 2048"},
		{"long name", line, "longer than"},
	} {
		name := ""
		if c.name == "long name" {
			name = strings.Repeat("x", sshKeyNameMax+1)
		}
		_, err := s.AddSSHKey(ctx, alice.ID, name, c.key)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want ErrInvalid %q", c.name, err, c.want)
		}
	}
}

// The door's lookup: the key's owner, unless the key is unknown or the
// account is disabled.
func TestAuthenticateSSHKey(t *testing.T) {
	s, alice, _ := newKeyService(t)
	ctx := context.Background()
	pk, line := testKey(t)
	k, err := s.AddSSHKey(ctx, alice.ID, "laptop", line)
	if err != nil {
		t.Fatal(err)
	}
	u, got, err := s.AuthenticateSSHKey(ctx, pk)
	if err != nil || u.ID != alice.ID || got.ID != k.ID {
		t.Fatalf("authenticate = %+v %+v %v", u, got, err)
	}
	other, _ := testKey(t)
	if _, _, err := s.AuthenticateSSHKey(ctx, other); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("unknown key: %v", err)
	}
	// Alice is the only admin: a second one lets her be disabled.
	if _, err := s.CreateUser(ctx, NewUser{Handle: "root", Email: "root@example.com", Name: "Root", Password: "correct horse battery staple", Admin: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDisabled(ctx, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthenticateSSHKey(ctx, pk); !errors.Is(err, ErrDisabled) {
		t.Errorf("disabled owner: %v", err)
	}
	if _, err := s.SetDisabled(ctx, alice.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthenticateSSHKey(ctx, pk); err != nil {
		t.Errorf("re-enabled owner: %v", err)
	}
}
