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
	t.Setenv("APOTHECARY_BASE_IMAGE", "ghcr.io/x/base:v1")
	b, _ = FromEnv()
	if b.Listen != "127.0.0.1:9999" || b.BaseImage != "ghcr.io/x/base:v1" {
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
