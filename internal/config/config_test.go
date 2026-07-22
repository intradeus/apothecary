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

func TestEgressProxyConfig(t *testing.T) {
	t.Setenv("APOTHECARY_POSTGRES_DSN", "postgres://x@localhost/db")
	cases := []struct {
		raw      string
		wantOn   bool
		wantPort int
	}{
		{"", true, DefaultEgressProxyPort},   // unset ⇒ on, default port
		{"on", true, DefaultEgressProxyPort}, // explicit on
		{"off", false, DefaultEgressProxyPort},
		{"8078", true, 8078},                      // fixed port
		{"garbage", true, DefaultEgressProxyPort}, // unparseable ⇒ default, still on
	}
	for _, c := range cases {
		// Empty string is the unset-equivalent (t.Setenv can't truly unset).
		t.Setenv("APOTHECARY_EGRESS_PROXY", c.raw)
		b, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if b.EgressProxy != c.wantOn || b.EgressProxyPort != c.wantPort {
			t.Errorf("APOTHECARY_EGRESS_PROXY=%q -> on=%v port=%d, want on=%v port=%d",
				c.raw, b.EgressProxy, b.EgressProxyPort, c.wantOn, c.wantPort)
		}
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
