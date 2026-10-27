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
	// UploadMaxBytes caps each uploaded attachment (documents/images given to
	// a ticket, run goal or ping answer). APOTHECARY_UPLOAD_MAX_BYTES,
	// default 25 MiB. (APOTHECARY_ARTIFACT_MAX_BYTES is read as a legacy
	// alias.)
	UploadMaxBytes int64
	// UploadMimeAllow is the MIME allowlist for uploads (exact types or
	// "text/*" wildcards). APOTHECARY_UPLOAD_MIME_ALLOW (comma-separated),
	// default DefaultUploadMimeAllow. (APOTHECARY_ARTIFACT_MIME_ALLOW is a
	// legacy alias.)
	UploadMimeAllow []string
	// SnapshotMaxBytes is the tail-truncation cap for engine captures (stage
	// outputs, logs, ping files). APOTHECARY_SNAPSHOT_MAX_BYTES; defaults to
	// UploadMaxBytes.
	SnapshotMaxBytes int64
	// PortAutoExpose auto-exposes runtime-discovered listening ports instead
	// of only listing them for click-to-expose. APOTHECARY_PORT_AUTOEXPOSE
	// ("on" enables); default off.
	PortAutoExpose bool
	// MaxConcurrentRuns / MaxEnvironments are the host's capacity: runs
	// beyond the first queue as pending, FIFO; at the second, new runs are
	// refused until a workspace is archived or reaped. Deployment-wide, the
	// same for every user — they protect the machine, not a person's
	// preference. APOTHECARY_MAX_CONCURRENT_RUNS (default 3) and
	// APOTHECARY_MAX_ENVIRONMENTS (default 6); -1 = no cap.
	MaxConcurrentRuns int
	MaxEnvironments   int
	// SandboxRuntime is the OCI runtime each run's Docker-in-Docker sandbox is
	// created with. APOTHECARY_SANDBOX_RUNTIME; default "" = the daemon's
	// default runtime, which needs `--privileged` to host a nested dockerd —
	// so sandbox root is host root. Set it to "sysbox-runc" (sysbox installed
	// on the HOST) and the sandbox runs unprivileged in a user namespace
	// instead, containing a compromised run to its own workspace.
	//
	// Opt-in, not auto-detected: which isolation a run got must be a property
	// of the deployment you can read off one line, not of whatever happened to
	// be installed on the machine.
	SandboxRuntime string
}

// DefaultEgressProxyPort is the proxy's loopback port when unspecified.
const DefaultEgressProxyPort = 8079

// DefaultUploadMaxBytes caps an uploaded attachment when unset (25 MiB).
const DefaultUploadMaxBytes = 25 << 20

// DefaultUploadMimeAllow is the upload MIME allowlist: text (covers
// markdown/csv/plain via the wildcard), common images, PDF, JSON, MS Office.
var DefaultUploadMimeAllow = []string{
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
	b.UploadMaxBytes = DefaultUploadMaxBytes
	for _, key := range []string{"APOTHECARY_ARTIFACT_MAX_BYTES", "APOTHECARY_UPLOAD_MAX_BYTES"} {
		if raw := os.Getenv(key); raw != "" {
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
				b.UploadMaxBytes = n
			}
		}
	}
	b.UploadMimeAllow = DefaultUploadMimeAllow
	for _, key := range []string{"APOTHECARY_ARTIFACT_MIME_ALLOW", "APOTHECARY_UPLOAD_MIME_ALLOW"} {
		if raw := os.Getenv(key); raw != "" {
			if list := splitComma(raw); len(list) > 0 {
				b.UploadMimeAllow = list
			}
		}
	}
	b.SnapshotMaxBytes = b.UploadMaxBytes
	if raw := os.Getenv("APOTHECARY_SNAPSHOT_MAX_BYTES"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			b.SnapshotMaxBytes = n
		}
	}
	b.PortAutoExpose = os.Getenv("APOTHECARY_PORT_AUTOEXPOSE") == "on"
	b.SandboxRuntime = strings.TrimSpace(os.Getenv("APOTHECARY_SANDBOX_RUNTIME"))
	var err error
	if b.MaxConcurrentRuns, err = limitEnv("APOTHECARY_MAX_CONCURRENT_RUNS", DefaultMaxConcurrentRuns); err != nil {
		return nil, err
	}
	if b.MaxEnvironments, err = limitEnv("APOTHECARY_MAX_ENVIRONMENTS", DefaultMaxEnvironments); err != nil {
		return nil, err
	}
	if b.PostgresDSN == "" {
		return nil, fmt.Errorf("config: APOTHECARY_POSTGRES_DSN is required")
	}
	return b, nil
}

// Default capacity caps (the v1 config defaults).
const (
	DefaultMaxConcurrentRuns = 3
	DefaultMaxEnvironments   = 6
)

// limitEnv reads a capacity cap: a positive count, or -1 for no cap. Zero
// and other negatives are refused rather than read as "nothing may run".
func limitEnv(name string, def int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n == 0 || n < -1 {
		return 0, fmt.Errorf("config: %s must be a positive count or -1 (no cap), got %q", name, raw)
	}
	return n, nil
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
