package config

// Boot configuration (DESIGN2.md §1): the daemon boots from env vars only.

import (
	"fmt"
	"net"
	"net/url"
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

	// Kubernetes: the deployment-wide facts a Kubernetes provisioner and the
	// project browsers need. A provisioner integration may override most of
	// them per integration; the browser has no integration, so these are its
	// only source.
	K8s K8s

	// AppsDomain turns exposed workspace ports into host-routed URLs
	// (<port>-<run>.<domain>) served by the engine itself instead of one
	// loopback listener per port — what makes them reachable when the engine
	// sits behind a Service and an Ingress. APOTHECARY_APPS_DOMAIN, default ""
	// (loopback listeners); APOTHECARY_APPS_SCHEME, default "http".
	AppsDomain string
	AppsScheme string
	// PublicURL is the UI's own origin as browsers reach it
	// (APOTHECARY_PUBLIC_URL), where an app host sends a browser to sign in.
	PublicURL string
	// SSHListen is where the SSH door listens (APOTHECARY_SSH_LISTEN, e.g.
	// 0.0.0.0:2222 or a VPN address); default "" = no door. SSHAdvertise is
	// the host:port people connect to (APOTHECARY_SSH_ADVERTISE), what the
	// run page prints and links to; derived when unset (SSHAdvertiseAddr).
	SSHListen    string
	SSHAdvertise string
}

// SSHAdvertiseAddr is the address people are told to ssh to: the explicit
// one, else the listen address when it names a host, else the public URL's
// host with the listen port. An error when the door is on and no host can
// be derived — "which address is this door at" must be readable off one
// line of the deployment, not guessed.
func (b *Boot) SSHAdvertiseAddr() (string, error) {
	if b.SSHListen == "" {
		return "", nil
	}
	if b.SSHAdvertise != "" {
		if _, _, err := net.SplitHostPort(b.SSHAdvertise); err != nil {
			return "", fmt.Errorf("config: APOTHECARY_SSH_ADVERTISE must be host:port, got %q", b.SSHAdvertise)
		}
		return b.SSHAdvertise, nil
	}
	host, port, err := net.SplitHostPort(b.SSHListen)
	if err != nil {
		return "", fmt.Errorf("config: APOTHECARY_SSH_LISTEN must be host:port, got %q", b.SSHListen)
	}
	if host != "" && host != "0.0.0.0" && host != "::" && host != "[::]" {
		return net.JoinHostPort(host, port), nil
	}
	if b.PublicURL != "" {
		if u, err := url.Parse(b.PublicURL); err == nil && u.Hostname() != "" {
			return net.JoinHostPort(u.Hostname(), port), nil
		}
	}
	return "", fmt.Errorf("config: APOTHECARY_SSH_LISTEN %q names no host people can reach — set APOTHECARY_SSH_ADVERTISE to the host:port they connect to", b.SSHListen)
}

// K8s is the Kubernetes part of the boot configuration.
type K8s struct {
	// Namespace is where sandboxes, gateways and browsers are created.
	// APOTHECARY_K8S_NAMESPACE; set ⇒ the project browsers run on
	// Kubernetes too.
	Namespace string
	// EngineNamespace is the engine's own namespace (the network policies
	// that let workspaces reach it select its pods there).
	// APOTHECARY_K8S_ENGINE_NAMESPACE, usually from the downward API.
	EngineNamespace string
	// EngineURL is how a workspace pod reaches the engine (its Service).
	// APOTHECARY_K8S_ENGINE_URL, e.g. http://apothecaryd.apothecary.svc.cluster.local:8080.
	EngineURL string
	// Kubeconfig is a kubeconfig path ("" = in-cluster, or kubectl's
	// default). APOTHECARY_K8S_KUBECONFIG.
	Kubeconfig string
	// ProtectCIDRs are destinations no workspace may reach through its
	// gateway: the cluster's pod and service ranges and link-local metadata.
	// APOTHECARY_K8S_PROTECT_CIDRS, default the k3s ranges + 169.254.0.0/16.
	ProtectCIDRs []string
	// Transport is how the engine dials a sandbox pod's sockets: "direct"
	// (TCP to the pod, in-cluster) or "portforward" (kubectl port-forward,
	// an engine outside the cluster). APOTHECARY_K8S_TRANSPORT; default:
	// direct when running in a pod, portforward otherwise.
	Transport string
	// Images apothecary runs on the cluster (it cannot build there).
	// APOTHECARY_SANDBOX_IMAGE, _SHIM_IMAGE, _TOOLS_IMAGE, _GATEWAY_IMAGE,
	// _BROWSER_IMAGE; defaults are the engine's own release (see DefaultImage).
	SandboxImage, ShimImage, ToolsImage, GatewayImage, BrowserImage string
	// PullPolicy for those images (APOTHECARY_K8S_IMAGE_PULL_POLICY): ""
	// = the cluster default; Always for a reused tag like :local.
	PullPolicy string
	// BrowserProxyListen is where the engine serves browser sessions' routing
	// proxy (APOTHECARY_K8S_BROWSER_PROXY_LISTEN, default :7071) and
	// BrowserProxyAddr how a browser pod reaches it
	// (APOTHECARY_K8S_BROWSER_PROXY_ADDR, default the engine URL's host:7071).
	BrowserProxyListen, BrowserProxyAddr string
}

// DefaultProtectCIDRs are k3s's default pod and service ranges plus
// link-local (cloud metadata). Set APOTHECARY_K8S_PROTECT_CIDRS on clusters
// with other ranges.
var DefaultProtectCIDRs = []string{"10.42.0.0/16", "10.43.0.0/16", "169.254.0.0/16"}

// ImageRepo is where release images are published.
const ImageRepo = "ghcr.io/intradeus/apothecary"

// DefaultImage is the published image <name> of an engine release: the
// version's own tag, or :local for a development build ("dev").
func DefaultImage(name, version string) string {
	tag := version
	if tag == "" || tag == "dev" {
		tag = "local"
	}
	return ImageRepo + "/" + name + ":" + tag
}

// InCluster reports whether this process runs in a Kubernetes pod.
func InCluster() bool {
	_, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token")
	return err == nil
}

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
	b.K8s = K8s{
		Namespace:          strings.TrimSpace(os.Getenv("APOTHECARY_K8S_NAMESPACE")),
		EngineNamespace:    strings.TrimSpace(os.Getenv("APOTHECARY_K8S_ENGINE_NAMESPACE")),
		EngineURL:          strings.TrimSpace(os.Getenv("APOTHECARY_K8S_ENGINE_URL")),
		Kubeconfig:         strings.TrimSpace(os.Getenv("APOTHECARY_K8S_KUBECONFIG")),
		ProtectCIDRs:       DefaultProtectCIDRs,
		Transport:          strings.TrimSpace(os.Getenv("APOTHECARY_K8S_TRANSPORT")),
		SandboxImage:       strings.TrimSpace(os.Getenv("APOTHECARY_SANDBOX_IMAGE")),
		ShimImage:          strings.TrimSpace(os.Getenv("APOTHECARY_SHIM_IMAGE")),
		ToolsImage:         strings.TrimSpace(os.Getenv("APOTHECARY_TOOLS_IMAGE")),
		GatewayImage:       strings.TrimSpace(os.Getenv("APOTHECARY_GATEWAY_IMAGE")),
		BrowserImage:       strings.TrimSpace(os.Getenv("APOTHECARY_BROWSER_IMAGE")),
		PullPolicy:         strings.TrimSpace(os.Getenv("APOTHECARY_K8S_IMAGE_PULL_POLICY")),
		BrowserProxyListen: getenvDefault("APOTHECARY_K8S_BROWSER_PROXY_LISTEN", ":7071"),
		BrowserProxyAddr:   strings.TrimSpace(os.Getenv("APOTHECARY_K8S_BROWSER_PROXY_ADDR")),
	}
	if raw := os.Getenv("APOTHECARY_K8S_PROTECT_CIDRS"); raw != "" {
		b.K8s.ProtectCIDRs = splitComma(raw)
	}
	switch b.K8s.Transport {
	case "":
		b.K8s.Transport = "portforward"
		if InCluster() {
			b.K8s.Transport = "direct"
		}
	case "direct", "portforward":
	default:
		return nil, fmt.Errorf("config: APOTHECARY_K8S_TRANSPORT must be direct or portforward, got %q", b.K8s.Transport)
	}
	b.AppsDomain = strings.Trim(strings.TrimSpace(os.Getenv("APOTHECARY_APPS_DOMAIN")), ".")
	b.AppsScheme = getenvDefault("APOTHECARY_APPS_SCHEME", "http")
	b.PublicURL = strings.TrimSuffix(strings.TrimSpace(os.Getenv("APOTHECARY_PUBLIC_URL")), "/")
	b.SSHListen = strings.TrimSpace(os.Getenv("APOTHECARY_SSH_LISTEN"))
	b.SSHAdvertise = strings.TrimSpace(os.Getenv("APOTHECARY_SSH_ADVERTISE"))
	if _, err := b.SSHAdvertiseAddr(); err != nil {
		return nil, err
	}
	if b.AppsDomain != "" && b.PublicURL == "" {
		return nil, fmt.Errorf("config: APOTHECARY_APPS_DOMAIN needs APOTHECARY_PUBLIC_URL (the UI's address, where app hosts send browsers to sign in)")
	}
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
