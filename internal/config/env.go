package config

// Boot configuration (DESIGN2.md §1): the daemon boots from env vars only.
// Everything that was user config in factory.yaml lives in the DB; the yaml
// loader in this package survives solely for `factoryd import-config`.

import (
	"fmt"
	"os"

	"factory/internal/secrets"
)

// Boot is the deployment-plane configuration (§1 exact set).
type Boot struct {
	Listen       string // FACTORY_LISTEN, default 127.0.0.1:8080
	PostgresDSN  string // FACTORY_POSTGRES_DSN, required
	MasterKeyRaw string // FACTORY_MASTER_KEY, required for serve
	RunsDir      string // FACTORY_RUNS_DIR, default /var/lib/factory/runs
	CPUIDir      string // FACTORY_CPUI_DIR, default "" (API only)
	OtelEndpoint string // FACTORY_OTEL_ENDPOINT, default "" (disabled)
	BaseImage    string // FACTORY_BASE_IMAGE, default "" (build embedded, M9)
}

// FromEnv reads the §1 variables. Only the DSN is unconditionally required;
// the master key is validated by Key() where the caller needs it (serve),
// so key-less commands like rebuild-projections still work.
func FromEnv() (*Boot, error) {
	b := &Boot{
		Listen:       getenvDefault("FACTORY_LISTEN", "127.0.0.1:8080"),
		PostgresDSN:  os.Getenv("FACTORY_POSTGRES_DSN"),
		MasterKeyRaw: os.Getenv("FACTORY_MASTER_KEY"),
		RunsDir:      getenvDefault("FACTORY_RUNS_DIR", "/var/lib/factory/runs"),
		CPUIDir:      os.Getenv("FACTORY_CPUI_DIR"),
		OtelEndpoint: os.Getenv("FACTORY_OTEL_ENDPOINT"),
		BaseImage:    os.Getenv("FACTORY_BASE_IMAGE"),
	}
	if b.PostgresDSN == "" {
		return nil, fmt.Errorf("config: FACTORY_POSTGRES_DSN is required")
	}
	return b, nil
}

// Key parses FACTORY_MASTER_KEY, refusing boot with a precise message.
func (b *Boot) Key() (secrets.Key, error) {
	if b.MasterKeyRaw == "" {
		return secrets.Key{}, fmt.Errorf("config: FACTORY_MASTER_KEY is required (generate one with `factoryd keygen`)")
	}
	return secrets.ParseKey(b.MasterKeyRaw)
}

func getenvDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
