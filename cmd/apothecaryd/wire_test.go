package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/ssh"

	"apothecary/internal/adapters/pgstore"
	"apothecary/internal/adapters/pgstore/pgtest"
	"apothecary/internal/platform/config"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/kinds"
)

// noHostTools empties PATH, so nothing a boot starts can reach the docker or
// kubectl on the machine running the tests, and the default kube context
// stays out of reach.
func noHostTools(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "none"))
}

// runsDir is a data directory that may still be in use when the test ends
// (the image warm-up is a detached goroutine), so it is not a t.TempDir.
func runsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "apo-cmd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func testBoot(t *testing.T, dsn string) *config.Boot {
	t.Helper()
	return &config.Boot{
		Listen: "127.0.0.1:0", PostgresDSN: dsn, MasterKeyRaw: secrets.GenerateKey(), RunsDir: runsDir(t),
		UploadMaxBytes: 4096, SnapshotMaxBytes: 4096, UploadMimeAllow: []string{"text/*"},
	}
}

// booted builds the app and shuts it down at the end of the test; the context
// goes first, so nothing it started outlives the PATH override.
func booted(t *testing.T, boot *config.Boot, dev bool) (*app, func()) {
	t.Helper()
	noHostTools(t)
	ctx, cancel := context.WithCancel(context.Background())
	a, cleanup, err := buildApp(ctx, boot, dev, slog.New(slog.DiscardHandler))
	if err != nil {
		cancel()
		t.Fatalf("buildApp: %v", err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		a.e.Shutdown()
		cleanup()
	}
	t.Cleanup(stop)
	return a, stop
}

// client talks to a booted app as a browser: cookies kept, same-origin.
type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newClient(t *testing.T, a *app) *client {
	t.Helper()
	ts := httptest.NewServer(a.srv.Handler)
	t.Cleanup(ts.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &client{t: t, base: ts.URL, http: &http.Client{Jar: jar}}
}

func (c *client) do(method, path string, body any) (int, string) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		rd = strings.NewReader(string(data))
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(data)
}

func (c *client) setup() {
	c.t.Helper()
	if code, body := c.do("POST", "/api/auth/setup", map[string]string{"handle": "lu", "email": "lu@example.test", "password": "correct horse"}); code != http.StatusCreated {
		c.t.Fatalf("setup = %d %s", code, body)
	}
}

// A real boot on a real database: every service the API needs is wired to
// it, and an account made through the API is there on the next boot.
func TestBuildAppBootsOnPostgres(t *testing.T) {
	dsn := pgtest.Empty(t)
	boot := testBoot(t, dsn)
	a, stop := booted(t, boot, false)
	c := newClient(t, a)

	code, body := c.do("GET", "/api/auth/status", nil)
	if code != http.StatusOK || !strings.Contains(body, `"setup_required":true`) {
		t.Fatalf("status = %d %s", code, body)
	}
	c.setup()
	if code, body := c.do("GET", "/api/me", nil); code != http.StatusOK || !strings.Contains(body, `"admin":true`) {
		t.Fatalf("me = %d %s", code, body)
	}

	// The real kinds, and no fakes outside -dev.
	code, body = c.do("GET", "/api/kinds", nil)
	if code != http.StatusOK || !strings.Contains(body, `"gitlab"`) || !strings.Contains(body, `"docker"`) || strings.Contains(body, `"kind":"fake"`) {
		t.Errorf("kinds = %d %s", code, body)
	}
	// Each of these answers only when its field of the server was wired.
	for _, path := range []string{
		"/api/projects", "/api/tickets", "/api/integrations", "/api/channels", "/api/pings", "/api/flows", "/api/skills",
		"/api/skillsets", "/api/settings", "/api/credentials", "/api/runs", "/api/users", "/api/providers",
	} {
		if code, body := c.do("GET", path, nil); code != http.StatusOK {
			t.Errorf("%s = %d %s", path, code, body)
		}
	}
	// Boot configuration reaches the server: the upload policy.
	code, body = c.do("GET", "/api/attachments/limits", nil)
	if code != http.StatusOK || !strings.Contains(body, `"max_bytes":4096`) {
		t.Errorf("limits = %d %s", code, body)
	}
	// A notification channel comes back with the process.
	if code, body := c.do("POST", "/api/channels", map[string]string{"kind": "slack", "webhook_url": "http://127.0.0.1:1/hook"}); code != http.StatusCreated {
		t.Fatalf("channel = %d %s", code, body)
	}
	// The capacity caps are the stored setting: a write survives the restart.
	if code, body := c.do("PUT", "/api/limits", map[string]int{"max_concurrent_runs": 2, "max_environments": 5}); code != http.StatusOK {
		t.Fatalf("set limits = %d %s", code, body)
	}

	stop()

	// A second boot on the same database: no setup, same account, same caps.
	second := *boot
	a2, _ := booted(t, &second, false)
	c2 := newClient(t, a2)
	if code, body := c2.do("GET", "/api/auth/status", nil); code != http.StatusOK || !strings.Contains(body, `"setup_required":false`) {
		t.Errorf("status after restart = %d %s", code, body)
	}
	if code, _ := c2.do("POST", "/api/auth/login", map[string]string{"login": "lu", "proof": "correct horse"}); code != http.StatusOK {
		t.Errorf("login after restart = %d", code)
	}
	if code, body := c2.do("GET", "/api/limits", nil); !strings.Contains(body, `"max_environments":5`) {
		t.Errorf("limits after restart = %d %s", code, body)
	}
	if code, body := c2.do("GET", "/api/channels", nil); code != http.StatusOK || !strings.Contains(body, `"kind":"slack"`) {
		t.Errorf("channels after restart = %d %s", code, body)
	}
}

// -dev is the whole product over fakes: a project seeded on first boot, a
// ticket runs to its first gate, and a restart recovers the parked run.
func TestDevBootRunsAFlowAndRecoversIt(t *testing.T) {
	boot := testBoot(t, pgtest.Empty(t))
	a, stop := booted(t, boot, true)
	c := newClient(t, a)
	c.setup()

	code, body := c.do("GET", "/api/kinds", nil)
	if code != http.StatusOK || !strings.Contains(body, `"fake"`) {
		t.Fatalf("dev kinds = %d %s", code, body)
	}
	ctx := context.Background()
	p, err := a.conf.GetProjectByName(ctx, "", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if p.DefaultFlow != "dev" || len(p.HarnessIDs) != 1 {
		t.Fatalf("seeded project = %+v", p)
	}
	all, err := a.conf.ListIntegrations(ctx, "")
	if err != nil || len(all) != 4 {
		t.Fatalf("seeded integrations = %d, %v", len(all), err)
	}

	// The seed is ownerless; a dev hands it to the first account by hand.
	u, err := a.store.GetUserByLogin(ctx, "lu")
	if err != nil {
		t.Fatal(err)
	}
	db, err := pgx.Connect(ctx, boot.PostgresDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	for _, table := range []string{"projects", "integrations"} {
		if _, err := db.Exec(ctx, "UPDATE "+table+" SET user_id=$1", u.ID); err != nil {
			t.Fatal(err)
		}
	}
	code, body = c.do("POST", "/api/tickets", map[string]string{"title": "Add a thing", "project_id": p.ID})
	if code != http.StatusCreated {
		t.Fatalf("ticket = %d %s", code, body)
	}
	var tk struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &tk)
	code, body = c.do("POST", "/api/tickets/"+tk.ID+"/runs", nil)
	if code != http.StatusCreated {
		t.Fatalf("run = %d %s", code, body)
	}
	var r struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal([]byte(body), &r)

	var pingID string
	waitFor(t, 20*time.Second, "the plan gate", func() bool {
		_, body := c.do("GET", "/api/pings", nil)
		var pings []struct {
			PingID string `json:"ping_id"`
			RunID  string `json:"run_id"`
		}
		if json.Unmarshal([]byte(body), &pings) != nil || len(pings) == 0 {
			return false
		}
		pingID = pings[0].PingID
		return pings[0].RunID == r.RunID
	})

	stop()

	// A restart with the run parked at its gate: the question is still there.
	second := *boot
	a2, _ := booted(t, &second, true)
	c2 := newClient(t, a2)
	if code, body := c2.do("POST", "/api/auth/login", map[string]string{"login": "lu", "proof": "correct horse"}); code != http.StatusOK {
		t.Fatalf("login = %d %s", code, body)
	}
	if code, body := c2.do("GET", "/api/pings/"+pingID, nil); code != http.StatusOK || !strings.Contains(body, `"status":"pending"`) {
		t.Errorf("gate after restart = %d %s", code, body)
	}
	// The dev seed does not run twice.
	if all, err := a2.conf.ListIntegrations(ctx, ""); err != nil || len(all) != 4 {
		t.Errorf("integrations after restart = %d, %v", len(all), err)
	}
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// doorKey connects to the SSH door and returns the host key it offers.
func doorKey(t *testing.T, addr string) string {
	t.Helper()
	var got string
	cfg := &ssh.ClientConfig{User: "nobody", Timeout: 5 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got = ssh.FingerprintSHA256(key)
			return nil
		}}
	if c, err := ssh.Dial("tcp", addr, cfg); err == nil {
		c.Close()
		t.Fatal("the door let an unknown client in")
	}
	if got == "" {
		t.Fatal("the door offered no host key")
	}
	return got
}

// The SSH door listens where the boot says, and its host key is derived from
// an instance secret: one fingerprint for good, across restarts.
func TestSSHDoorKeepsItsIdentity(t *testing.T) {
	boot := testBoot(t, pgtest.Empty(t))
	boot.SSHListen = freePort(t)
	boot.SSHAdvertise = "door.example:2222"
	_, stop := booted(t, boot, false)
	first := doorKey(t, boot.SSHListen)
	stop()

	second := *boot
	second.SSHListen = freePort(t)
	booted(t, &second, false)
	if got := doorKey(t, second.SSHListen); got != first {
		t.Errorf("host key changed across a restart: %s then %s", first, got)
	}
}

func TestOptionalFacesOfTheBoot(t *testing.T) {
	cpui := t.TempDir()
	if err := os.WriteFile(filepath.Join(cpui, "index.html"), []byte("<the app>"), 0o644); err != nil {
		t.Fatal(err)
	}
	boot := testBoot(t, pgtest.Empty(t))
	boot.CPUIDir = cpui
	boot.AppsDomain, boot.AppsScheme, boot.PublicURL = "apps.test", "http", "http://ui.test"
	boot.OtelEndpoint = freePort(t) // a collector that is not there is not fatal
	a, _ := booted(t, boot, false)
	c := newClient(t, a)

	if code, body := c.do("GET", "/some/client/route", nil); code != http.StatusOK || body != "<the app>" {
		t.Errorf("UI = %d %q", code, body)
	}
	// An app host is routed to the relay, not the API.
	req, _ := http.NewRequest("GET", c.base+"/api/me", nil)
	req.Host = "3000-nothing.apps.test"
	res, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound || !strings.Contains(string(data), "nothing is exposed") {
		t.Errorf("app host = %d %s", res.StatusCode, data)
	}
}

// On Kubernetes the browser is the release image, on a session listener of the
// engine's. Nothing is applied at boot, and an empty PATH proves it: a boot
// that needed kubectl would fail here, and none is reachable.
func TestBootOnKubernetes(t *testing.T) {
	boot := testBoot(t, pgtest.Empty(t))
	boot.K8s = config.K8s{
		Namespace: "sandboxes", EngineNamespace: "apothecary", EngineURL: "http://engine.svc:8080",
		Kubeconfig: filepath.Join(t.TempDir(), "kubeconfig"), BrowserProxyListen: "127.0.0.1:0", Transport: "portforward",
	}
	a, _ := booted(t, boot, false)
	c := newClient(t, a)
	if code, body := c.do("GET", "/api/auth/status", nil); code != http.StatusOK {
		t.Errorf("status = %d %s", code, body)
	}
}

func TestK8sBrowserHostAdvertise(t *testing.T) {
	for name, c := range map[string]struct {
		k    config.K8s
		good bool
	}{
		"explicit address": {config.K8s{BrowserProxyAddr: "proxy.svc:7071", BrowserProxyListen: "127.0.0.1:0"}, true},
		"from the engine":  {config.K8s{EngineURL: "http://engine.svc:8080", BrowserProxyListen: "127.0.0.1:0"}, true},
		"neither":          {config.K8s{BrowserProxyListen: "127.0.0.1:0"}, true},
		"bad listener":     {config.K8s{BrowserProxyListen: "256.0.0.1:1"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			c.k.Kubeconfig = "/nonexistent/kubeconfig"
			host, closeMux, err := k8sBrowserHost(&config.Boot{K8s: c.k, RunsDir: t.TempDir()})
			if (err == nil) != c.good {
				t.Fatalf("err = %v", err)
			}
			if err == nil {
				if host == nil {
					t.Error("no host")
				}
				closeMux()
			}
		})
	}
}

// An inline kubeconfig is written under the data directory; a data directory
// that cannot hold it stops the boot with the reason.
func TestK8sBrowserHostBadKubeconfig(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := k8sBrowserHost(&config.Boot{RunsDir: file, K8s: config.K8s{Kubeconfig: "apiVersion: v1\n", BrowserProxyListen: "127.0.0.1:0"}})
	if err == nil || !strings.Contains(err.Error(), "browser kubeconfig") {
		t.Errorf("err = %v", err)
	}
}

func TestKubernetesDefaults(t *testing.T) {
	if got := kubernetesDefaults(nil); got.Namespace != "" || got.ShimImage != "" {
		t.Errorf("no boot = %+v", got)
	}
	got := kubernetesDefaults(&config.Boot{K8s: config.K8s{
		Namespace: "ns", EngineURL: "http://e", SandboxImage: "mine:1", PullPolicy: "Always", Transport: "direct",
	}})
	if got.Namespace != "ns" || got.EngineURL != "http://e" || got.PullPolicy != "Always" || got.Transport != "direct" {
		t.Errorf("passed through = %+v", got)
	}
	if got.SandboxImage != "mine:1" {
		t.Errorf("a set image was replaced: %q", got.SandboxImage)
	}
	if want := config.DefaultImage("shim", version); got.ShimImage != want {
		t.Errorf("default image = %q, want %q", got.ShimImage, want)
	}
	if want := config.DefaultImage("egress-gw", version); got.GatewayImage != want {
		t.Errorf("gateway image = %q, want %q", got.GatewayImage, want)
	}
}

func TestListenHelpers(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1:9000": "http://127.0.0.1:9000",
		":8081":          "http://127.0.0.1:8081",
		"no-port":        "http://127.0.0.1:8080",
	} {
		if got := engineURL(addr); got != want {
			t.Errorf("engineURL(%q) = %q, want %q", addr, got, want)
		}
	}
	if h, p, err := splitHostPort("[::1]:80"); err != nil || h != "[::1]" || p != "80" {
		t.Errorf("split = %q %q %v", h, p, err)
	}
	if _, _, err := splitHostPort("nope"); err == nil {
		t.Error("an address with no port was accepted")
	}
}

// The registry offers the real kinds always, and the fakes only with -dev.
func TestRegistry(t *testing.T) {
	has := func(r *kinds.Registry, kind string) bool {
		for _, d := range r.Descriptors() {
			if d.Kind == kind {
				return true
			}
		}
		return false
	}
	real := registry(false, &config.Boot{})
	for _, kind := range []string{"gitlab", "github", "docker", "kubernetes", "opencode", "codex", "omp", "jira"} {
		if !has(real, kind) {
			t.Errorf("%s is missing", kind)
		}
	}
	if has(real, "fake") {
		t.Error("fake kinds outside -dev")
	}
	if !has(registry(true, nil), "fake") {
		t.Error("no fake kinds in -dev")
	}
}

// The dev seed leaves a real configuration alone.
func TestSeedDevProject(t *testing.T) {
	store := openStore(t)
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	reg := registry(true, nil)
	conf := confstore.New(store, key)
	conf.KindValid = reg.Valid
	ctx := context.Background()
	if err := seedDevProject(ctx, conf); err != nil {
		t.Fatal(err)
	}
	p, err := conf.GetProjectByName(ctx, "", "dev")
	if err != nil || p.DefaultFlow != "dev" {
		t.Fatalf("seeded = %+v, %v", p, err)
	}
	// Seeded twice is seeded once.
	if err := seedDevProject(ctx, conf); err != nil {
		t.Fatal(err)
	}
	if all, _ := conf.ListProjects(ctx, ""); len(all) != 1 {
		t.Errorf("projects = %d", len(all))
	}
	// Renamed, it is a real configuration: no second dev project beside it.
	p.Name = "mine"
	if _, err := conf.UpdateProject(ctx, p.ID, p); err != nil {
		t.Fatal(err)
	}
	if err := seedDevProject(ctx, conf); err != nil {
		t.Fatal(err)
	}
	if all, _ := conf.ListProjects(ctx, ""); len(all) != 1 || all[0].Name != "mine" {
		t.Errorf("projects = %+v", all)
	}
}

func openStore(t *testing.T) *pgstore.Store {
	t.Helper()
	return openDSN(t, pgtest.Empty(t))
}

func openDSN(t *testing.T, dsn string) *pgstore.Store {
	t.Helper()
	store, err := pgstore.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

// --- main, as an operator runs it ---------------------------------------------------

// TestMain lets a test run the real main in a child process: the only way to
// see what an exit or a panic does to an operator's terminal.
func TestMain(m *testing.M) {
	if raw := os.Getenv("APOTHECARYD_TEST_ARGV"); raw != "" {
		var argv []string
		if err := json.Unmarshal([]byte(raw), &argv); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		os.Args = append([]string{"apothecaryd"}, argv...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type result struct {
	stdout, stderr string
	code           int
}

func runMain(t *testing.T, env map[string]string, stdin string, args ...string) result {
	t.Helper()
	argv, err := json.Marshal(append([]string{}, args...))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0])
	// GOCOVERDIR: a binary built with -cover warns on stderr without it.
	cmd.Env = []string{"APOTHECARYD_TEST_ARGV=" + string(argv), "HOME=" + t.TempDir(), "GOCOVERDIR=" + t.TempDir()}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	return result{out.String(), errb.String(), code}
}

func TestMainCommands(t *testing.T) {
	if r := runMain(t, nil, "", "version"); r.code != 0 || r.stdout != "apothecaryd dev\n" {
		t.Errorf("version = %+v", r)
	}
	r := runMain(t, nil, "", "keygen")
	if _, err := secrets.ParseKey(strings.TrimSpace(r.stdout)); r.code != 0 || err != nil {
		t.Errorf("keygen = %+v, %v", r, err)
	}
	for _, name := range []string{"shim-hash", "tools-hash"} {
		r := runMain(t, nil, "", name)
		if r.code != 0 || len(strings.TrimSpace(r.stdout)) < 16 {
			t.Errorf("%s = %+v", name, r)
		}
	}
	if a, b := runMain(t, nil, "", "shim-hash"), runMain(t, nil, "", "shim-hash"); a.stdout != b.stdout {
		t.Error("the shim hash moved between two runs of one binary")
	}

	// No command, or one it does not know: usage on stderr, exit 2.
	for _, args := range [][]string{{}, {"nonsense"}} {
		r := runMain(t, nil, "", args...)
		if r.code != 2 || !strings.Contains(r.stderr, "usage: apothecaryd") || r.stdout != "" {
			t.Errorf("%v = %+v", args, r)
		}
	}
	// A command that fails says why and exits 1.
	r = runMain(t, nil, "", "validate", "/no/such/flow.yaml")
	if r.code != 1 || r.stderr == "" {
		t.Errorf("validate = %+v", r)
	}
	r = runMain(t, nil, "", "rebuild-projections")
	if r.code != 1 || !strings.Contains(r.stderr, "APOTHECARY_POSTGRES_DSN") {
		t.Errorf("no config = %+v", r)
	}
	for _, args := range [][]string{{"user"}, {"envcheck"}} {
		if r := runMain(t, nil, "", args...); r.code != 1 || !strings.Contains(r.stderr, "usage") {
			t.Errorf("%v = %+v", args, r)
		}
	}
}

func TestValidateAFlowFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.yaml")
	if err := os.WriteFile(path, []byte(devFlowYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runMain(t, nil, "", "validate", path)
	if r.code != 0 || !strings.Contains(r.stdout, `flow "dev" valid`) || !strings.Contains(r.stdout, "stages") {
		t.Errorf("validate = %+v", r)
	}
	// In process too, for what it says on a bad call.
	if err := cmdValidate(nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("no file = %v", err)
	}
	if err := cmdValidate([]string{"a", "b"}); err == nil {
		t.Error("two files were accepted")
	}
	if err := os.WriteFile(path, []byte("name: broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdValidate([]string{path}); err == nil {
		t.Error("a broken flow validated")
	}
}

// buildApp's error must reach the operator: serve prints it and exits 1.
func TestServeReportsABootFailure(t *testing.T) {
	for name, c := range map[string]struct {
		env  map[string]string
		want string
	}{
		"no master key": {map[string]string{"APOTHECARY_POSTGRES_DSN": "postgres://x@127.0.0.1:1/x"}, "APOTHECARY_MASTER_KEY"},
		"database down": {map[string]string{"APOTHECARY_POSTGRES_DSN": "postgres://x@127.0.0.1:1/x?connect_timeout=1", "APOTHECARY_MASTER_KEY": secrets.GenerateKey()}, "failed to connect"},
	} {
		r := runMain(t, c.env, "", "serve")
		if r.code != 1 || strings.Contains(r.stderr, "panic") || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%s: %+v", name, r)
		}
	}
}
