package config

// Boot configuration (DESIGN2.md §1): the daemon boots from env vars only.

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"apothecary/internal/secrets"
)

// Boot is the deployment-plane configuration (§1 exact set).
type Boot struct {
	Listen       string // APOTHECARY_LISTEN, default 127.0.0.1:8080
	PostgresDSN  string // APOTHECARY_POSTGRES_DSN, required
	MasterKeyRaw string // APOTHECARY_MASTER_KEY, required for serve
	RunsDir      string // APOTHECARY_RUNS_DIR, default /var/lib/apothecary/runs
	CPUIDir      string // APOTHECARY_CPUI_DIR, default "" (API only)
	OtelEndpoint string // APOTHECARY_OTEL_ENDPOINT, default "" (disabled)
	// EgressProxy toggles the hostname-filtering egress proxy (M18).
	// APOTHECARY_EGRESS_PROXY: "off" disables (falls back to the derived IP
	// allowlist firewall); "on"/unset enables on the default port; a port
	// number sets a fixed port. Off falls back to the derived IP allowlist.
	EgressProxy bool
	// EgressProxyPort is the loopback port the proxy binds — STABLE across
	// restarts (a fixed port, not ephemeral) so every workspace's proxy.env
	// stays valid after the engine restarts. Default 8079.
	EgressProxyPort int
	// ArtifactMaxBytes caps each uploaded artifact (documents/images
	// attached to a goal or ping answer). APOTHECARY_ARTIFACT_MAX_BYTES,
	// default 25 MiB.
	ArtifactMaxBytes int64
	// ArtifactMimeAllow is the MIME allowlist for uploads (exact types or
	// "text/*" wildcards). APOTHECARY_ARTIFACT_MIME_ALLOW (comma-separated),
	// default DefaultArtifactMimeAllow.
	ArtifactMimeAllow []string
	// PortAutoExpose auto-exposes runtime-discovered listening ports instead
	// of only listing them for click-to-expose. APOTHECARY_PORT_AUTOEXPOSE
	// ("on" enables); default off.
	PortAutoExpose bool
}

// DefaultEgressProxyPort is the proxy's loopback port when unspecified.
const DefaultEgressProxyPort = 8079

// DefaultArtifactMaxBytes caps an uploaded artifact when unset (25 MiB).
const DefaultArtifactMaxBytes = 25 << 20

// DefaultArtifactMimeAllow is the upload MIME allowlist: text (covers
// markdown/csv/plain via the wildcard), common images, PDF, JSON, MS Office.
var DefaultArtifactMimeAllow = []string{
	"text/*",
	"image/png", "image/jpeg", "image/gif", "image/webp",
	"application/pdf", "application/json",
	"application/msword",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation",
}

// FromEnv reads the §1 variables. Only the DSN is unconditionally required;
// the master key is validated by Key() where the caller needs it (serve),
// so key-less commands like rebuild-projections still work.
func FromEnv() (*Boot, error) {
	b := &Boot{
		Listen:       getenvDefault("APOTHECARY_LISTEN", "127.0.0.1:8080"),
		PostgresDSN:  os.Getenv("APOTHECARY_POSTGRES_DSN"),
		MasterKeyRaw: os.Getenv("APOTHECARY_MASTER_KEY"),
		RunsDir:      getenvDefault("APOTHECARY_RUNS_DIR", "/var/lib/apothecary/runs"),
		CPUIDir:      os.Getenv("APOTHECARY_CPUI_DIR"),
		OtelEndpoint: os.Getenv("APOTHECARY_OTEL_ENDPOINT"),
	}
	// Egress proxy: "off" disables; "on"/unset uses the default port; a bare
	// port number sets a fixed port. Anything unparseable falls back to the
	// default (loopback is always the host).
	raw := os.Getenv("APOTHECARY_EGRESS_PROXY")
	b.EgressProxy = raw != "off"
	b.EgressProxyPort = DefaultEgressProxyPort
	if raw != "" && raw != "on" && raw != "off" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			b.EgressProxyPort = p
		}
	}
	b.ArtifactMaxBytes = DefaultArtifactMaxBytes
	if raw := os.Getenv("APOTHECARY_ARTIFACT_MAX_BYTES"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			b.ArtifactMaxBytes = n
		}
	}
	b.ArtifactMimeAllow = DefaultArtifactMimeAllow
	if raw := os.Getenv("APOTHECARY_ARTIFACT_MIME_ALLOW"); raw != "" {
		if list := splitComma(raw); len(list) > 0 {
			b.ArtifactMimeAllow = list
		}
	}
	b.PortAutoExpose = os.Getenv("APOTHECARY_PORT_AUTOEXPOSE") == "on"
	if b.PostgresDSN == "" {
		return nil, fmt.Errorf("config: APOTHECARY_POSTGRES_DSN is required")
	}
	return b, nil
}

func splitComma(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Key parses APOTHECARY_MASTER_KEY, refusing boot with a precise message.
func (b *Boot) Key() (secrets.Key, error) {
	if b.MasterKeyRaw == "" {
		return secrets.Key{}, fmt.Errorf("config: APOTHECARY_MASTER_KEY is required (generate one with `apothecaryd keygen`)")
	}
	return secrets.ParseKey(b.MasterKeyRaw)
}

func getenvDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
