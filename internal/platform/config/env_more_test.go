package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// clearEnv blanks every variable FromEnv reads, so a test states only the ones
// it is about, whatever the machine running it exports.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"APOTHECARY_LISTEN", "APOTHECARY_MASTER_KEY", "APOTHECARY_RUNS_DIR", "APOTHECARY_CPUI_DIR",
		"APOTHECARY_LOG_FORMAT", "APOTHECARY_LOG_LEVEL", "APOTHECARY_DEBUG_ADDR", "APOTHECARY_GRAFANA_URL",
		"APOTHECARY_ARTIFACT_MAX_BYTES", "APOTHECARY_UPLOAD_MAX_BYTES",
		"APOTHECARY_ARTIFACT_MIME_ALLOW", "APOTHECARY_UPLOAD_MIME_ALLOW", "APOTHECARY_SNAPSHOT_MAX_BYTES",
		"APOTHECARY_PORT_AUTOEXPOSE", "APOTHECARY_K8S_RUNTIME_CLASS", "APOTHECARY_K8S_PRIVILEGED", "APOTHECARY_WORKSPACE_DOCKER", "APOTHECARY_K8S_NAMESPACE",
		"APOTHECARY_K8S_ENGINE_NAMESPACE", "APOTHECARY_K8S_ENGINE_URL", "APOTHECARY_K8S_KUBECONFIG",
		"APOTHECARY_K8S_TRANSPORT", "APOTHECARY_K8S_PROTECT_CIDRS", "APOTHECARY_SANDBOX_IMAGE",
		"APOTHECARY_SHIM_IMAGE", "APOTHECARY_TOOLS_IMAGE", "APOTHECARY_GATEWAY_IMAGE", "APOTHECARY_BROWSER_IMAGE",
		"APOTHECARY_K8S_IMAGE_PULL_POLICY", "APOTHECARY_K8S_BROWSER_PROXY_LISTEN", "APOTHECARY_K8S_BROWSER_PROXY_ADDR",
		"APOTHECARY_APPS_DOMAIN", "APOTHECARY_APPS_SCHEME", "APOTHECARY_PUBLIC_URL", "APOTHECARY_SSH_LISTEN",
		"APOTHECARY_SSH_ADVERTISE", "APOTHECARY_CLI_TOKEN_IDLE",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("APOTHECARY_POSTGRES_DSN", "postgres://x@localhost/db")
}

// Logging and the debug server default quiet and safe; a bad value refuses
// to boot naming the variable, and pprof never binds beyond loopback.
func TestObservabilityKnobs(t *testing.T) {
	clearEnv(t)
	b, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if b.LogFormat != "text" || b.LogLevel != "info" || b.DebugAddr != "" || b.GrafanaURL != "" {
		t.Errorf("defaults = %q %q %q %q", b.LogFormat, b.LogLevel, b.DebugAddr, b.GrafanaURL)
	}
	for _, c := range []struct{ name, val, err string }{
		{"APOTHECARY_LOG_FORMAT", "json", ""},
		{"APOTHECARY_LOG_FORMAT", "xml", `config: APOTHECARY_LOG_FORMAT must be text or json, got "xml"`},
		{"APOTHECARY_LOG_LEVEL", "debug", ""},
		{"APOTHECARY_LOG_LEVEL", "loud", `config: APOTHECARY_LOG_LEVEL must be debug, info, warn or error, got "loud"`},
		{"APOTHECARY_DEBUG_ADDR", "127.0.0.1:6060", ""},
		{"APOTHECARY_DEBUG_ADDR", "localhost:6060", ""},
		{"APOTHECARY_DEBUG_ADDR", "[::1]:6060", ""},
		{"APOTHECARY_DEBUG_ADDR", "0.0.0.0:6060", `config: APOTHECARY_DEBUG_ADDR must be a loopback host:port, got "0.0.0.0:6060"`},
		{"APOTHECARY_DEBUG_ADDR", ":6060", `config: APOTHECARY_DEBUG_ADDR must be a loopback host:port, got ":6060"`},
		{"APOTHECARY_DEBUG_ADDR", "127.0.0.1", `config: APOTHECARY_DEBUG_ADDR must be a loopback host:port, got "127.0.0.1"`},
		{"APOTHECARY_GRAFANA_URL", "https://grafana.example", ""},
	} {
		t.Run(c.name+"="+c.val, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(c.name, c.val)
			_, err := FromEnv()
			if c.err == "" && err != nil {
				t.Fatal(err)
			}
			if c.err != "" && (err == nil || err.Error() != c.err) {
				t.Fatalf("err = %v, want %s", err, c.err)
			}
		})
	}
}

func TestUploadLimits(t *testing.T) {
	for _, c := range []struct {
		name          string
		env           map[string]string
		upload, snaps int64
	}{
		{"defaults", nil, DefaultUploadMaxBytes, DefaultUploadMaxBytes},
		{"set", map[string]string{"APOTHECARY_UPLOAD_MAX_BYTES": "1024"}, 1024, 1024},
		{"legacy alias", map[string]string{"APOTHECARY_ARTIFACT_MAX_BYTES": "2048"}, 2048, 2048},
		{"the new name beats the alias", map[string]string{"APOTHECARY_ARTIFACT_MAX_BYTES": "2048", "APOTHECARY_UPLOAD_MAX_BYTES": "1024"}, 1024, 1024},
		{"junk keeps the default", map[string]string{"APOTHECARY_UPLOAD_MAX_BYTES": "lots"}, DefaultUploadMaxBytes, DefaultUploadMaxBytes},
		{"zero keeps the default", map[string]string{"APOTHECARY_UPLOAD_MAX_BYTES": "0"}, DefaultUploadMaxBytes, DefaultUploadMaxBytes},
		{"negative keeps the default", map[string]string{"APOTHECARY_UPLOAD_MAX_BYTES": "-5"}, DefaultUploadMaxBytes, DefaultUploadMaxBytes},
		{"snapshots on their own", map[string]string{"APOTHECARY_UPLOAD_MAX_BYTES": "1024", "APOTHECARY_SNAPSHOT_MAX_BYTES": "4096"}, 1024, 4096},
		{"junk snapshot cap follows uploads", map[string]string{"APOTHECARY_UPLOAD_MAX_BYTES": "1024", "APOTHECARY_SNAPSHOT_MAX_BYTES": "x"}, 1024, 1024},
	} {
		t.Run(c.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			b, err := FromEnv()
			if err != nil {
				t.Fatal(err)
			}
			if b.UploadMaxBytes != c.upload || b.SnapshotMaxBytes != c.snaps {
				t.Errorf("upload %d snapshot %d, want %d %d", b.UploadMaxBytes, b.SnapshotMaxBytes, c.upload, c.snaps)
			}
		})
	}
}

func TestUploadAllowlist(t *testing.T) {
	clearEnv(t)
	b, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(b.UploadMimeAllow, DefaultUploadMimeAllow) {
		t.Errorf("default = %v", b.UploadMimeAllow)
	}
	t.Setenv("APOTHECARY_ARTIFACT_MIME_ALLOW", "image/png")
	if b, _ = FromEnv(); !slices.Equal(b.UploadMimeAllow, []string{"image/png"}) {
		t.Errorf("legacy alias = %v", b.UploadMimeAllow)
	}
	t.Setenv("APOTHECARY_UPLOAD_MIME_ALLOW", " text/* ,, image/png ,")
	if b, _ = FromEnv(); !slices.Equal(b.UploadMimeAllow, []string{"text/*", "image/png"}) {
		t.Errorf("list = %v", b.UploadMimeAllow)
	}
	// A list of nothing is no list: it must not lock every upload out.
	t.Setenv("APOTHECARY_ARTIFACT_MIME_ALLOW", "")
	t.Setenv("APOTHECARY_UPLOAD_MIME_ALLOW", " , ,")
	if b, _ = FromEnv(); !slices.Equal(b.UploadMimeAllow, DefaultUploadMimeAllow) {
		t.Errorf("blank list = %v", b.UploadMimeAllow)
	}
}

func TestSmallSwitches(t *testing.T) {
	clearEnv(t)
	t.Setenv("APOTHECARY_PORT_AUTOEXPOSE", "yes")
	t.Setenv("APOTHECARY_K8S_RUNTIME_CLASS", "  sysbox-runc ")
	t.Setenv("APOTHECARY_PUBLIC_URL", " https://ui.example/ ")
	b, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if b.PortAutoExpose {
		t.Error(`only "on" turns auto-expose on`)
	}
	if b.K8s.RuntimeClass != "sysbox-runc" || b.PublicURL != "https://ui.example" {
		t.Errorf("trimmed = %q %q", b.K8s.RuntimeClass, b.PublicURL)
	}
	t.Setenv("APOTHECARY_PORT_AUTOEXPOSE", "on")
	if b, _ = FromEnv(); !b.PortAutoExpose {
		t.Error("on did not turn auto-expose on")
	}
}

func TestKubernetesSettings(t *testing.T) {
	clearEnv(t)
	b, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if want := "portforward"; !InCluster() && b.K8s.Transport != want {
		t.Errorf("transport outside a cluster = %q", b.K8s.Transport)
	}
	if b.K8s.BrowserProxyListen != ":7071" || !slices.Equal(b.K8s.ProtectCIDRs, DefaultProtectCIDRs) {
		t.Errorf("defaults = %+v", b.K8s)
	}

	t.Setenv("APOTHECARY_K8S_NAMESPACE", " sandboxes ")
	t.Setenv("APOTHECARY_K8S_TRANSPORT", "direct")
	t.Setenv("APOTHECARY_K8S_PROTECT_CIDRS", "10.0.0.0/8, 192.168.0.0/16")
	t.Setenv("APOTHECARY_K8S_BROWSER_PROXY_LISTEN", ":9000")
	t.Setenv("APOTHECARY_SANDBOX_IMAGE", "img:1")
	t.Setenv("APOTHECARY_K8S_RUNTIME_CLASS", "sysbox-runc")
	t.Setenv("APOTHECARY_K8S_PRIVILEGED", "off")
	if b, err = FromEnv(); err != nil {
		t.Fatal(err)
	}
	k := b.K8s
	if k.Namespace != "sandboxes" || k.Transport != "direct" || k.BrowserProxyListen != ":9000" || k.SandboxImage != "img:1" ||
		k.RuntimeClass != "sysbox-runc" || k.Privileged != "off" ||
		!slices.Equal(k.ProtectCIDRs, []string{"10.0.0.0/8", "192.168.0.0/16"}) {
		t.Errorf("set = %+v", k)
	}

	t.Setenv("APOTHECARY_K8S_TRANSPORT", "carrier-pigeon")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "APOTHECARY_K8S_TRANSPORT") {
		t.Errorf("bad transport = %v", err)
	}
}

// A misconfiguration stops the boot and names the variable.
func TestBootRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"apps host needs a UI address", map[string]string{"APOTHECARY_APPS_DOMAIN": "apps.example"}, "APOTHECARY_PUBLIC_URL"},
		{"ssh door with a bad address", map[string]string{"APOTHECARY_SSH_LISTEN": "nonsense"}, "APOTHECARY_SSH_LISTEN"},
		{"privileged neither on nor off", map[string]string{"APOTHECARY_K8S_PRIVILEGED": "maybe"}, "APOTHECARY_K8S_PRIVILEGED"},
		{"unprivileged sandboxes need a runtime class", map[string]string{"APOTHECARY_K8S_PRIVILEGED": "off"}, "APOTHECARY_K8S_RUNTIME_CLASS"},
	} {
		t.Run(c.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to name %s", err, c.want)
			}
		})
	}
}

// The CLI token's inactivity window: a week by default, any positive Go
// duration, and anything else refuses to boot naming the variable.
func TestCLITokenIdle(t *testing.T) {
	clearEnv(t)
	b, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if b.CLITokenIdle != 168*time.Hour {
		t.Errorf("default = %v", b.CLITokenIdle)
	}
	t.Setenv("APOTHECARY_CLI_TOKEN_IDLE", " 72h ")
	if b, err := FromEnv(); err != nil || b.CLITokenIdle != 72*time.Hour {
		t.Errorf("72h = %v, %v", b, err)
	}
	for _, bad := range []string{"0", "-1h", "a week", "7"} {
		t.Setenv("APOTHECARY_CLI_TOKEN_IDLE", bad)
		if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "APOTHECARY_CLI_TOKEN_IDLE") {
			t.Errorf("%q = %v, want a refusal naming the variable", bad, err)
		}
	}
}

func TestAppsSettings(t *testing.T) {
	clearEnv(t)
	t.Setenv("APOTHECARY_APPS_DOMAIN", " .apps.example. ")
	t.Setenv("APOTHECARY_PUBLIC_URL", "https://ui.example")
	t.Setenv("APOTHECARY_APPS_SCHEME", "https")
	b, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if b.AppsDomain != "apps.example" || b.AppsScheme != "https" {
		t.Errorf("apps = %q %q", b.AppsDomain, b.AppsScheme)
	}
}

func TestDefaultImage(t *testing.T) {
	for version, want := range map[string]string{
		"":       ImageRepo + "/shim:local",
		"dev":    ImageRepo + "/shim:local",
		"v1.2.3": ImageRepo + "/shim:v1.2.3",
	} {
		if got := DefaultImage("shim", version); got != want {
			t.Errorf("DefaultImage(%q) = %q, want %q", version, got, want)
		}
	}
}

func TestSSHAdvertiseKeepsIPv6Listen(t *testing.T) {
	b := &Boot{SSHListen: "[fd7a::1]:2222"}
	if got, err := b.SSHAdvertiseAddr(); err != nil || got != "[fd7a::1]:2222" {
		t.Errorf("got %q, %v", got, err)
	}
	for _, listen := range []string{"[::]:2222", "0.0.0.0:2222"} {
		b := &Boot{SSHListen: listen, PublicURL: "%%%"}
		if _, err := b.SSHAdvertiseAddr(); err == nil {
			t.Errorf("%s with an unusable public url was accepted", listen)
		}
	}
}
