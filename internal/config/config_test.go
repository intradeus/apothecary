package config

import (
	"strings"
	"testing"

	"apothecary/internal/secrets"
)

func TestFromEnvRequiresDSN(t *testing.T) {
	t.Setenv("APOTHECARY_POSTGRES_DSN", "")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "APOTHECARY_POSTGRES_DSN") {
		t.Errorf("err = %v, want named missing var", err)
	}
}

func TestFromEnvDefaultsAndOverrides(t *testing.T) {
	t.Setenv("APOTHECARY_POSTGRES_DSN", "postgres://x@localhost/db")
	t.Setenv("APOTHECARY_LISTEN", "")
	b, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if b.Listen != "127.0.0.1:8080" || b.RunsDir != "/var/lib/apothecary/runs" {
		t.Errorf("defaults = %+v", b)
	}
	t.Setenv("APOTHECARY_LISTEN", "127.0.0.1:9999")
	b, _ = FromEnv()
	if b.Listen != "127.0.0.1:9999" {
		t.Errorf("overrides = %+v", b)
	}
}

func TestKeyValidation(t *testing.T) {
	b := &Boot{}
	if _, err := b.Key(); err == nil || !strings.Contains(err.Error(), "keygen") {
		t.Errorf("missing key err = %v", err)
	}
	b.MasterKeyRaw = "not-base64!!"
	if _, err := b.Key(); err == nil {
		t.Error("junk key accepted")
	}
	b.MasterKeyRaw = secrets.GenerateKey()
	if _, err := b.Key(); err != nil {
		t.Errorf("valid key refused: %v", err)
	}
}

// The SSH door's advertised address: explicit, else the listen host when it
// is one people can reach, else the public URL's host — and an error when
// the door is on with nothing to tell people.
func TestSSHAdvertiseAddr(t *testing.T) {
	for _, c := range []struct {
		name, listen, advertise, public, want string
		wantErr                               bool
	}{
		{name: "off", want: ""},
		{name: "explicit", listen: "0.0.0.0:2222", advertise: "door.tail.ts.net:2222", want: "door.tail.ts.net:2222"},
		{name: "bad explicit", listen: "0.0.0.0:2222", advertise: "door.tail.ts.net", wantErr: true},
		{name: "listen host", listen: "100.64.0.9:2222", want: "100.64.0.9:2222"},
		{name: "wildcard with public url", listen: "0.0.0.0:2222", public: "https://apothecary.example.com", want: "apothecary.example.com:2222"},
		{name: "wildcard alone", listen: ":2222", wantErr: true},
	} {
		b := &Boot{SSHListen: c.listen, SSHAdvertise: c.advertise, PublicURL: c.public}
		got, err := b.SSHAdvertiseAddr()
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%s: got %q, %v; want %q (err %v)", c.name, got, err, c.want, c.wantErr)
		}
	}
}
