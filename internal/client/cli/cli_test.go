package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/platform/wstunnel"
)

// fakeEngine answers what the commands ask, as the engine would.
type fakeEngine struct {
	mu      sync.Mutex
	ports   []int  // what the run listens on now
	portsAt int    // 404: the run is gone; 502: listing fails; 0: fine
	version string // what /api/auth/status says
	revoked []string
	polls   int  // device-login polls so far
	approve int  // the poll that is approved (0: never)
	deny    bool // the sign-in is denied
	status  int  // device-login status calls
	srv     *httptest.Server
}

func newFakeEngine(t *testing.T) *fakeEngine {
	t.Helper()
	e := &fakeEngine{version: "v1.0.0", approve: 2}
	mux := http.NewServeMux()
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "token expired"})
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/auth/status", func(w http.ResponseWriter, _ *http.Request) {
		e.mu.Lock()
		e.status++
		v := e.version
		e.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"setup_required": false, "version": v})
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			_, _ = io.WriteString(w, `{"user":{"id":"u1","handle":"ada"}}`)
		}
	})
	mux.HandleFunc("POST /api/cli/logins", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"device_code":"dc","user_code":"ABCD-EFGH","verification_url":"`+e.srv.URL+`/cli-login?code=ABCD-EFGH","interval":1,"expires_in":50}`)
	})
	mux.HandleFunc("POST /api/cli/logins/token", func(w http.ResponseWriter, _ *http.Request) {
		e.mu.Lock()
		e.polls++
		n, approve, deny := e.polls, e.approve, e.deny
		e.mu.Unlock()
		switch {
		case deny:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"the sign-in was denied"}`)
		case approve == 0 || n < approve:
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"status":"pending"}`)
		default:
			_, _ = io.WriteString(w, `{"status":"approved","token":"good","token_id":"t-cli","handle":"ada"}`)
		}
	})
	mux.HandleFunc("DELETE /api/me/tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			e.mu.Lock()
			e.revoked = append(e.revoked, r.PathValue("id"))
			e.mu.Unlock()
			w.WriteHeader(204)
		}
	})
	runJSON := func(id string) string {
		return `{"run_id":"` + id + `","state":"running","env_id":"apo-` + id + `","ticket_key":"PROJ-1","ticket_title":"Fix ` + id + `"}`
	}
	mux.HandleFunc("GET /api/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		if id := r.PathValue("id"); strings.HasPrefix(id, "r") {
			_, _ = io.WriteString(w, `{"run":`+runJSON(id)+`,"events":[]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"not found"}`)
	})
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			_, _ = io.WriteString(w, `[`+runJSON("r1")+`,`+runJSON("r2")+`]`)
		}
	})
	mux.HandleFunc("GET /api/runs/{id}/ports", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		e.mu.Lock()
		at := e.portsAt
		e.mu.Unlock()
		if r.PathValue("id") == "rgone" || at == http.StatusNotFound {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"no such run"}`)
			return
		}
		if at != 0 {
			w.WriteHeader(at)
			return
		}
		e.mu.Lock()
		var obs []map[string]int
		for _, p := range e.ports {
			obs = append(obs, map[string]int{"port": p})
		}
		e.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ports": []any{}, "observed": obs})
	})
	// Each tunnel answers who it reached, then echoes.
	echo := func(w http.ResponseWriter, r *http.Request, what string) {
		if !authed(w, r) {
			return
		}
		c, err := wstunnel.Accept(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = c.Write([]byte(what + "\n"))
		_, _ = io.Copy(c, c)
	}
	mux.HandleFunc("GET /api/runs/{id}/ports/{port}/tunnel", func(w http.ResponseWriter, r *http.Request) {
		echo(w, r, "port "+r.PathValue("port"))
	})
	mux.HandleFunc("GET /api/ssh/tunnel", func(w http.ResponseWriter, r *http.Request) {
		echo(w, r, "ssh "+r.URL.Query().Get("workspace"))
	})
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *fakeEngine) listen(ports ...int) {
	e.mu.Lock()
	e.ports = ports
	e.mu.Unlock()
}

type rig struct {
	env      Env
	out, err *lockedBuffer
}

// shortDir is a directory whose paths fit a unix socket's 104 bytes (macOS):
// t.TempDir() embeds the test's name.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "apo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{out: &lockedBuffer{}, err: &lockedBuffer{}}
	r.env = Env{
		Stdin: strings.NewReader(""), Stdout: r.out, Stderr: r.err,
		ConfigDir: shortDir(t), Home: t.TempDir(), Getenv: func(string) string { return "" },
		Hostname: "laptop", Executable: "/usr/local/bin/apothecary", Version: "dev",
	}
	return r
}

func (r *rig) run(args ...string) int {
	r.out.Reset()
	r.err.Reset()
	return Run(context.Background(), args, r.env)
}

func (r *rig) signIn(t *testing.T, e *fakeEngine) {
	t.Helper()
	if code := r.run("login", e.srv.URL, "--token", "good"); code != 0 {
		t.Fatalf("login = %d: %s", code, r.err)
	}
}

// lockedBuffer is a buffer goroutines may share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *lockedBuffer) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b.Reset()
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDispatch(t *testing.T) {
	r := newRig(t)
	for _, args := range [][]string{nil, {"help"}, {"--help"}} {
		if r.run(args...) != 0 || !strings.Contains(r.out.String(), "usage: apothecary <command>") {
			t.Errorf("%v = %s", args, r.out)
		}
	}
	r.env.Version = "v9"
	if r.run("version") != 0 || r.out.String() != "apothecary v9\n" {
		t.Errorf("version = %q", r.out)
	}
	if r.run("dance") != 2 || !strings.Contains(r.err.String(), `unknown command "dance"`) {
		t.Errorf("unknown = %q", r.err)
	}
	for _, args := range [][]string{
		{"forward"}, {"login"}, {"ssh-proxy"}, {"ssh-proxy", ".apothecary"}, {"forward", "r1", "x"},
		{"forward", "r1", "--bogus"}, {"forward", "--same"}, {"forward", "stop"}, {"forward", "stop", "--all", "r1"},
		{"forward", "stop", "r1", "x"}, {"forward", "stop", "--bogus"}, {"update", "--bogus"},
	} {
		if code := r.run(args...); code != 2 || !strings.Contains(r.err.String(), "apothecary: ") {
			t.Errorf("%v = %d %q", args, code, r.err)
		}
	}
	// Nothing signed in: the commands that need the engine say how.
	for _, args := range [][]string{{"forward", "r1"}, {"ssh-config"}, {"ssh-proxy", "w.apothecary"}, {"logout"}, {"update"}} {
		if code := r.run(args...); code != 1 || !strings.Contains(r.err.String(), "not signed in: apothecary login <url>") {
			t.Errorf("%v = %d %q", args, code, r.err)
		}
	}
}

func TestConfigFromTheEnvironmentAndBroken(t *testing.T) {
	r := newRig(t)
	r.env.Getenv = func(k string) string {
		return map[string]string{"APOTHECARY_URL": "https://ci.example", "APOTHECARY_TOKEN": "tok"}[k]
	}
	cfg, err := loadConfig(r.env)
	if err != nil || cfg.URL != "https://ci.example" || cfg.Token != "tok" {
		t.Errorf("cfg = %+v %v", cfg, err)
	}
	path := filepath.Join(r.env.ConfigDir, "apothecary", "config.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte("{"), 0o600)
	if _, err := loadConfig(r.env); err == nil {
		t.Error("a broken config must be reported")
	}
	if _, err := loadConfig(Env{}); err != nil {
		t.Errorf("no config dir is no config: %v", err)
	}
	for _, err := range []error{saveConfig(Env{}, Config{}), removeConfig(Env{})} {
		if err == nil {
			t.Error("nowhere to keep a config")
		}
	}
	if _, err := forwardDir(Env{}); err == nil {
		t.Error("nowhere to keep the forwards")
	}
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	if err := saveConfig(Env{ConfigDir: file}, Config{}); err == nil {
		t.Error("a config dir under a file")
	}
	_ = os.Remove(path)
	_ = os.MkdirAll(filepath.Join(path, "x"), 0o700) // config.json is a non-empty directory
	if err := removeConfig(r.env); err == nil {
		t.Error("a config that cannot be removed")
	}
	if _, err := loadConfig(r.env); err == nil {
		t.Error("a config that cannot be read")
	}
}

func TestOSEnv(t *testing.T) {
	env := OSEnv("v1")
	if env.Version != "v1" || env.Stdout != os.Stdout || env.Getenv == nil || env.OpenBrowser == nil || env.SpawnDaemon == nil {
		t.Errorf("env = %+v", env)
	}
}

// ssh-config writes its block once, keeps everything else, and replaces
// only its own block when run again.
func TestSSHConfig(t *testing.T) {
	e := newFakeEngine(t)
	r := newRig(t)
	r.signIn(t, e)
	path := filepath.Join(r.env.Home, ".ssh", "config")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte("ServerAliveInterval 30\nHost work\n    User me"), 0o600)
	if code := r.run("ssh-config"); code != 0 {
		t.Fatalf("ssh-config = %d %q", code, r.err)
	}
	got, _ := os.ReadFile(path)
	host := strings.TrimPrefix(e.srv.URL, "http://")
	host = host[:strings.LastIndex(host, ":")]
	for _, want := range []string{
		"ServerAliveInterval 30\nHost work\n    User me\n\n# >>> apothecary >>>",
		"Host *.apothecary\n",
		"    ProxyCommand /usr/local/bin/apothecary ssh-proxy %h\n",
		"    HostKeyAlias apothecary-" + host + "\n",
		"    StrictHostKeyChecking accept-new\n",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("config lacks %q:\n%s", want, got)
		}
	}
	r.env.Executable = "/Applications/My Tools/apothecary"
	if code := r.run("ssh-config"); code != 0 {
		t.Fatal(r.err)
	}
	again, _ := os.ReadFile(path)
	if strings.Count(string(again), "# >>> apothecary >>>") != 1 || !strings.Contains(string(again), `ProxyCommand "/Applications/My Tools/apothecary" ssh-proxy %h`) {
		t.Errorf("rerun:\n%s", again)
	}
	if !strings.HasPrefix(string(again), "ServerAliveInterval 30\nHost work") {
		t.Errorf("what was there must stay:\n%s", again)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode())
	}
	if withBlock("", "B\n") != "B\n" {
		t.Error("empty config")
	}
	_ = os.Remove(path)
	_ = os.MkdirAll(path, 0o700)
	if code := r.run("ssh-config"); code != 1 {
		t.Errorf("ssh config is a dir = %d", code)
	}
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	r.env.Home = file // ~/.ssh cannot be made
	if code := r.run("ssh-config"); code != 1 {
		t.Errorf("no ~/.ssh = %d", code)
	}
	r.env.Home = ""
	if code := r.run("ssh-config"); code != 1 {
		t.Errorf("no home = %d", code)
	}
}

// ssh-proxy carries ssh's stdin and stdout to the door for the workspace
// the host names.
func TestSSHProxy(t *testing.T) {
	e := newFakeEngine(t)
	r := newRig(t)
	r.signIn(t, e)
	r.env.Stdin = strings.NewReader("hello")
	out := &lockedBuffer{}
	r.env.Stdout = out
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		// The fake door echoes until the client hangs up; stop it once the
		// echo has come back.
		waitFor(t, func() bool { return strings.Contains(out.String(), "hello") })
		cancel()
	}()
	if code := Run(ctx, []string{"ssh-proxy", "apo-w.apothecary"}, r.env); code != 0 {
		t.Fatalf("ssh-proxy = %d %q", code, r.err)
	}
	if !strings.HasPrefix(out.String(), "ssh apo-w\n") {
		t.Errorf("out = %q", out.String())
	}
	r.env.Getenv = func(k string) string { return map[string]string{"APOTHECARY_TOKEN": "bad"}[k] }
	if code := r.run("ssh-proxy", "apo-w.apothecary"); code != 1 || !strings.Contains(r.err.String(), "sign in again") {
		t.Errorf("bad token = %d %q", code, r.err)
	}
}

func TestOpenBrowser(t *testing.T) {
	old := browserFor
	t.Cleanup(func() { browserFor = old })
	var asked string
	browserFor = func(u string) *exec.Cmd { asked = u; return exec.Command("true") }
	if err := openBrowser("https://ui/x"); err != nil || asked != "https://ui/x" {
		t.Errorf("open = %v, %q", err, asked)
	}
	browserFor = func(string) *exec.Cmd { return exec.Command("/nonexistent/browser") }
	if err := openBrowser("u"); err == nil {
		t.Error("a browser that cannot start must say so")
	}
}

func TestBrowserCommand(t *testing.T) {
	for goos, want := range map[string]string{
		"darwin": "open u", "windows": "rundll32 url.dll,FileProtocolHandler u", "linux": "xdg-open u",
	} {
		cmd := browserCmd(goos, "u")
		if got := strings.Join(cmd.Args, " "); got != want {
			t.Errorf("%s = %q", goos, got)
		}
	}
}
