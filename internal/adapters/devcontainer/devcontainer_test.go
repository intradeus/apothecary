package devcontainer

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"apothecary/internal/ports"
)

type call struct {
	bin   string
	args  []string
	stdin []byte
}

// scriptRunner replays canned responses and records calls.
type scriptRunner struct {
	calls     []call
	responses []response
}

type response struct {
	stdout string
	stderr string
	code   int
	err    error
}

func (s *scriptRunner) Run(_ context.Context, bin string, args []string, stdin []byte, tee io.Writer) ([]byte, []byte, int, error) {
	s.calls = append(s.calls, call{bin: bin, args: args, stdin: stdin})
	if len(s.responses) == 0 {
		return nil, nil, 0, nil
	}
	r := s.responses[0]
	s.responses = s.responses[1:]
	if tee != nil && r.stdout != "" {
		// Emulate live streaming: the tee sees output before Run returns.
		_, _ = tee.Write([]byte(r.stdout))
	}
	return []byte(r.stdout), []byte(r.stderr), r.code, r.err
}

func newTestAdapter(t *testing.T, r CmdRunner) *Adapter {
	t.Helper()
	a := NewWithRunner(Config{RunsDir: t.TempDir()}, r)
	a.retryDelay = time.Millisecond
	// Tests never bind real gateway IPs.
	a.listen = func(network, addr string) (net.Listener, error) {
		return net.Listen("tcp", "127.0.0.1:0")
	}
	return a
}

const upOK = `{"outcome":"success","containerId":"cid1","remoteUser":"vscode","remoteWorkspaceFolder":"/workspaces/src"}`

// find returns the first recorded call whose bin and leading arg match.
func find(t *testing.T, calls []call, bin, firstArg string) call {
	t.Helper()
	for _, c := range calls {
		if c.bin == bin && len(c.args) > 0 && c.args[0] == firstArg {
			return c
		}
	}
	t.Fatalf("no %s %s call in %v", bin, firstArg, calls)
	return call{}
}

func TestProvisionClonesUpsAndInits(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{code: 0},                 // git clone
		{stdout: upOK},            // devcontainer up
		{code: 0},                 // docker exec root-init
		{stdout: "/usr/bin/node"}, // node probe
		{stdout: "cid1\n"},        // docker ps -q (relay)
		{code: 0},                 // docker exec -d relay
		{code: 0},                 // ready/relay probe
	}}
	a := newTestAdapter(t, r)
	a.cfg.ProxyPort = 9090
	id, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "RUN1", RepoURL: "https://g.local/grp/repo.git",
		GitUser: "oauth2", GitPass: "sekret", Image: "apothecary/ws:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "apothecary-run1" {
		t.Errorf("env id = %s", id)
	}

	clone := find(t, r.calls, "git", "-c")
	joined := strings.Join(clone.args, " ")
	if strings.Contains(joined, "sekret") {
		t.Fatalf("credential leaked into argv: %v", clone.args)
	}
	if !strings.Contains(joined, "credential.helper=store --file=") {
		t.Errorf("clone must use a credential store file: %v", clone.args)
	}
	if _, err := os.Stat(filepath.Join(a.folderFor("apothecary-run1"), "git-credentials")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("credential file must be removed after clone")
	}

	up := find(t, r.calls, "devcontainer", "up")
	joined = strings.Join(up.args, " ")
	for _, want := range []string{
		"--workspace-folder " + filepath.Join(a.folderFor("apothecary-run1"), "src"),
		"--id-label apothecary.env=apothecary-run1",
		"target=/opt/apothecary",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("up args missing %q: %v", want, up.args)
		}
	}
	// Repo has no devcontainer config in this test: the pipeline-image
	// fallback must ride --override-config.
	if !strings.Contains(joined, "--override-config") {
		t.Errorf("fallback config expected for repo without devcontainer: %v", up.args)
	}
	data, err := os.ReadFile(filepath.Join(a.folderFor("apothecary-run1"), "override.json"))
	if err != nil || !strings.Contains(string(data), "apothecary/ws:abc") {
		t.Errorf("override.json = %s, %v", data, err)
	}
	for _, f := range []string{"ask-human.mjs", "relay.mjs"} {
		data, err := os.ReadFile(filepath.Join(a.folderFor("apothecary-run1"), "shim", f))
		if err != nil || len(data) == 0 {
			t.Errorf("%s not materialized: %v", f, err)
		}
	}

	init := find(t, r.calls, "docker", "exec")
	joined = strings.Join(init.args, " ")
	for _, want := range []string{"-u root", "cid1", "mkdir -p /apothecary", "ln -sfn '/workspaces/src'"} {
		if !strings.Contains(joined, want) {
			t.Errorf("root-init missing %q: %v", want, init.args)
		}
	}

	// The engine bridge came up for both sockets (proxy mode on).
	if n := len(a.bridges["apothecary-run1"]); n != 2 {
		t.Errorf("bridge listeners = %d, want 2 (engine + proxy)", n)
	}
	a.Close()
}

func TestProvisionHonorsRepoDevcontainer(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: upOK},            // up (no clone: src pre-seeded)
		{code: 0},                 // root-init
		{stdout: "/usr/bin/node"}, // node probe
		{stdout: "cid1\n"},        // docker ps -q (relay)
		{code: 0},                 // docker exec -d relay
		{code: 0},                 // ready/relay probe
	}}
	a := newTestAdapter(t, r)
	src := filepath.Join(a.folderFor("apothecary-run1"), "src")
	if err := os.MkdirAll(filepath.Join(src, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(src, ".git"), filepath.Join(src, ".devcontainer", "devcontainer.json")} {
		if err := os.WriteFile(f, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "run1", RepoURL: "https://g.local/r.git"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		if c.bin == "git" {
			t.Error("must not re-clone an existing checkout")
		}
	}
	up := find(t, r.calls, "devcontainer", "up")
	if strings.Contains(strings.Join(up.args, " "), "--override-config") {
		t.Errorf("repo devcontainer must be honored, not overridden: %v", up.args)
	}
}

func TestProvisionWithoutConfigOrImageFails(t *testing.T) {
	r := &scriptRunner{responses: []response{{code: 0}}} // clone
	a := newTestAdapter(t, r)
	_, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "run1", RepoURL: "https://g.local/r.git"})
	if err == nil || !strings.Contains(err.Error(), "no .devcontainer config") {
		t.Fatalf("want missing-config error, got %v", err)
	}
}

func TestProvisionSurfacesUpFailure(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{code: 0}, // clone
		{stdout: `{"outcome":"error","message":"Dockerfile failed","description":"exit 1"}`, stderr: "build log tail", code: 1},
	}}
	a := newTestAdapter(t, r)
	_, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "run1", RepoURL: "u", Image: "img"})
	if err == nil || !strings.Contains(err.Error(), "Dockerfile failed") {
		t.Fatalf("want up failure with message, got %v", err)
	}
}

func TestExecPropagatesExitAndCombinesOutput(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "out\n", stderr: "err\n", code: 7},
	}}
	a := newTestAdapter(t, r)
	res, err := a.Exec(context.Background(), "apothecary-run1", ports.Command{Script: "make test", Dir: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 {
		t.Errorf("exit = %d", res.ExitCode)
	}
	if res.Output != "out\nerr\n" {
		t.Errorf("output = %q", res.Output)
	}
	c := r.calls[0]
	if c.bin != "devcontainer" || c.args[0] != "exec" {
		t.Fatalf("exec transport = %v", c.args)
	}
	joined := strings.Join(c.args, " ")
	if !strings.Contains(joined, "--id-label apothecary.env=apothecary-run1") {
		t.Errorf("exec args missing label: %v", c.args)
	}
	stdin := string(c.stdin)
	if !strings.Contains(stdin, `cd "$HOME/repo"`) || !strings.Contains(stdin, "make test") || !strings.Contains(stdin, "proxy.env") {
		t.Errorf("stdin = %q", stdin)
	}
}

func TestExecStreamsToTailableLog(t *testing.T) {
	r := &scriptRunner{responses: []response{{stdout: "live output\n"}}}
	a := newTestAdapter(t, r)
	if _, err := a.Exec(context.Background(), "ws1", ports.Command{Script: "echo live output"}); err != nil {
		t.Fatal(err)
	}
	got, err := a.TailExecLog(context.Background(), "ws1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "live output") {
		t.Errorf("exec.log = %q", got)
	}
}

func TestReadFile(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("hello devcontainer"))
	r := &scriptRunner{responses: []response{
		{stdout: payload + "\n"},
		{code: 44},
	}}
	a := newTestAdapter(t, r)
	data, err := a.ReadFile(context.Background(), "ws", "artifacts/plan.md")
	if err != nil || string(data) != "hello devcontainer" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if _, err := a.ReadFile(context.Background(), "ws", "missing.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want fs.ErrNotExist, got %v", err)
	}
}

func TestWriteFileSendsScriptAndPayload(t *testing.T) {
	r := &scriptRunner{responses: []response{{code: 0}}}
	a := newTestAdapter(t, r)
	if err := a.WriteFile(context.Background(), "ws", "/apothecary/run.env", []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin := string(r.calls[0].stdin)
	if !strings.Contains(stdin, "base64 -d > '/apothecary/run.env'") ||
		!strings.Contains(stdin, base64.StdEncoding.EncodeToString([]byte("A=1\n"))) ||
		!strings.Contains(stdin, "chmod 600") {
		t.Errorf("stdin = %q", stdin)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		psOut string
		want  ports.EnvStatus
	}{
		{"running\n", ports.EnvRunning},
		{"created\n", ports.EnvStarting},
		{"restarting\n", ports.EnvStarting},
		{"exited\n", ports.EnvStopped},
		{"paused\n", ports.EnvStopped},
		{"", ports.EnvAbsent},
	}
	for _, c := range cases {
		a := newTestAdapter(t, &scriptRunner{responses: []response{{stdout: c.psOut}}})
		got, err := a.Status(context.Background(), "apothecary-run1")
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("state %q -> %s, want %s", strings.TrimSpace(c.psOut), got, c.want)
		}
	}
}

func TestDestroyRemovesContainersAndFolder(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "cid1\n"}, // docker ps -aq
		{code: 0},          // docker rm -f
	}}
	a := newTestAdapter(t, r)
	if err := os.MkdirAll(a.folderFor("apothecary-run1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Destroy(context.Background(), "apothecary-run1"); err != nil {
		t.Fatal(err)
	}
	rm := find(t, r.calls, "docker", "rm")
	if !strings.Contains(strings.Join(rm.args, " "), "-f cid1") {
		t.Errorf("rm args = %v", rm.args)
	}
	if _, err := os.Stat(a.folderFor("apothecary-run1")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("env folder not cleaned up")
	}
}

func TestDestroyAbsentIsNoop(t *testing.T) {
	r := &scriptRunner{responses: []response{{stdout: ""}}}
	a := newTestAdapter(t, r)
	if err := a.Destroy(context.Background(), "apothecary-run1"); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 {
		t.Errorf("absent destroy should only list: %v", r.calls)
	}
}

func TestLinksExposeDeclaredPortsOnContainerIP(t *testing.T) {
	rc := `{"configuration":{"appPort":9000},"mergedConfiguration":{"forwardPorts":[3000,"5432","db:5432"]}}`
	r := &scriptRunner{responses: []response{
		{stdout: "eager_apothecary\n"}, // ps names
		{stdout: "cid1\n"},             // ps -q (containerIP)
		{stdout: "172.17.0.5 \n"},      // inspect IP
		{stdout: rc + "\n"},            // read-configuration
	}}
	a := newTestAdapter(t, r)
	links, err := a.Links(context.Background(), "apothecary-run1")
	if err != nil {
		t.Fatal(err)
	}
	var urls, cmds []string
	for _, l := range links {
		urls = append(urls, l.URL)
		cmds = append(cmds, l.Command)
	}
	if !strings.Contains(strings.Join(cmds, " "), "docker exec -it eager_apothecary bash") {
		t.Errorf("terminal link missing: %v", cmds)
	}
	joined := strings.Join(urls, " ")
	for _, want := range []string{"http://172.17.0.5:3000/", "http://172.17.0.5:5432/", "http://172.17.0.5:9000/"} {
		if !strings.Contains(joined, want) {
			t.Errorf("links missing %q: %v", want, urls)
		}
	}
	if strings.Contains(joined, "db:5432") {
		t.Errorf("service-qualified port must be skipped: %v", urls)
	}
}

func TestBridgePumpsBytesOverUnixSocket(t *testing.T) {
	// Real engine-side TCP listener the bridge forwards to.
	engine, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	go func() {
		for {
			c, err := engine.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err == nil {
					_, _ = c.Write([]byte("pong-" + string(buf)))
				}
			}(c)
		}
	}()
	_, portStr, _ := net.SplitHostPort(engine.Addr().String())
	port, _ := net.LookupPort("tcp", portStr)

	a := newTestAdapter(t, &scriptRunner{})
	a.listen = net.Listen
	a.cfg.EnginePort = port
	if err := a.ensureBridge("ws1"); err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	sock := filepath.Join(a.folderFor("ws1"), "shim", "engine.sock")
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "pong-ping" {
		t.Fatalf("round trip = %q, %v", got, err)
	}

	// Idempotent per env: a second ensure opens nothing new.
	if err := a.ensureBridge("ws1"); err != nil {
		t.Fatal(err)
	}
	if n := len(a.bridges["ws1"]); n != 1 {
		t.Errorf("listeners = %d, want 1", n)
	}
}

func TestBridgeOpensProxySocketWhenProxyModeOn(t *testing.T) {
	a := newTestAdapter(t, &scriptRunner{})
	a.cfg.ProxyPort = 9090
	var bound []string
	a.listen = func(network, addr string) (net.Listener, error) {
		bound = append(bound, network+"|"+filepath.Base(addr))
		return net.Listen("unix", addr)
	}
	if err := a.ensureBridge("ws1"); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	joined := strings.Join(bound, " ")
	for _, want := range []string{"unix|engine.sock", "unix|proxy.sock"} {
		if !strings.Contains(joined, want) {
			t.Errorf("bridge sockets missing %q: %v", want, bound)
		}
	}
}

func TestStartRelayResolvesNodeAndDetaches(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "/usr/local/share/nvm/current/bin/node\n"}, // command -v node
		{stdout: "cid1\n"}, // docker ps -q
		{code: 0},          // docker exec -d
	}}
	a := newTestAdapter(t, r)
	a.cfg.ProxyPort = 9090
	if err := a.startRelay(context.Background(), "apothecary-run1"); err != nil {
		t.Fatal(err)
	}
	relay := find(t, r.calls, "docker", "exec")
	joined := strings.Join(relay.args, " ")
	for _, want := range []string{
		"-d cid1 /usr/local/share/nvm/current/bin/node /opt/apothecary/relay.mjs",
		"8080:/opt/apothecary/engine.sock",
		"9090:/opt/apothecary/proxy.sock",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("relay start missing %q: %v", want, relay.args)
		}
	}
}

func TestStartRelayWithoutNodeExplains(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stderr: "bash: command -v: no node", code: 1},
	}}
	a := newTestAdapter(t, r)
	err := a.startRelay(context.Background(), "apothecary-run1")
	if err == nil || !strings.Contains(err.Error(), "ghcr.io/devcontainers/features/node") {
		t.Fatalf("want node guidance, got %v", err)
	}
}

func TestTestProbeReportsMissingCLI(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{err: errors.New("exec: not found")},
		{err: errors.New("exec: not found")},
		{err: errors.New("exec: not found")},
	}}
	a := newTestAdapter(t, r)
	err := a.Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "npm install -g @devcontainers/cli") {
		t.Fatalf("want install hint, got %v", err)
	}
}

func TestSpawnFailureRetries(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{err: errors.New("fork: retry")},
		{stdout: "ok\n"},
	}}
	a := newTestAdapter(t, r)
	res, err := a.Exec(context.Background(), "ws", ports.Command{Script: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "ok\n" || len(r.calls) != 2 {
		t.Errorf("output=%q calls=%d", res.Output, len(r.calls))
	}
}

func TestWorkspaceName(t *testing.T) {
	if got := WorkspaceName("01J8ULIDULIDULIDULIDULID26"); got != "apothecary-idulidulidulid26" {
		t.Errorf("name = %s", got)
	}
	if got := WorkspaceName("RUN1"); got != "apothecary-run1" {
		t.Errorf("name = %s", got)
	}
}

func TestCredentialLine(t *testing.T) {
	line, err := credentialLine("https://gitlab.local/grp/repo.git", "", "p@ss w")
	if err != nil {
		t.Fatal(err)
	}
	if line != "https://oauth2:p%40ss+w@gitlab.local" {
		t.Errorf("line = %q", line)
	}
}
