package coder

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/ports"
)

type call struct {
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

func (s *scriptRunner) Run(_ context.Context, args []string, stdin []byte, tee io.Writer) ([]byte, []byte, int, error) {
	s.calls = append(s.calls, call{args: args, stdin: stdin})
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

func newTestAdapter(r CmdRunner) *Adapter {
	a := NewWithRunner(Config{
		URL: "http://localhost:7080", Token: "tok", Template: "custom-tmpl", Owner: "lu",
	}, r)
	a.retryDelay = time.Millisecond
	// Never spawn a real `coder ssh` tunnel in unit tests.
	a.tunnelCmd = func(ctx context.Context, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "300")
	}
	return a
}

const listRunning = `[{"name":"apothecary-run1","latest_build":{"status":"running"}}]`

func TestExposePortSupervisesPortForward(t *testing.T) {
	a := newTestAdapter(&scriptRunner{})
	type pfCall struct {
		ws            string
		local, remote int
	}
	calls := make(chan pfCall, 8)
	a.pfCmd = func(ctx context.Context, ws string, local, remote int) *exec.Cmd {
		calls <- pfCall{ws, local, remote}
		return exec.CommandContext(ctx, "sleep", "300")
	}
	pc, err := a.ExposePort(context.Background(), "apothecary-run1", 3000)
	if err != nil {
		t.Fatal(err)
	}
	c := <-calls
	if c.ws != "apothecary-run1" || c.remote != 3000 || c.local == 0 {
		t.Errorf("port-forward args = %+v", c)
	}
	a.tunnelMu.Lock()
	registered := len(a.forwards)
	a.tunnelMu.Unlock()
	if registered != 1 {
		t.Errorf("forwards registered = %d", registered)
	}
	if err := pc.Close(); err != nil {
		t.Fatal(err)
	}
	a.tunnelMu.Lock()
	registered = len(a.forwards)
	a.tunnelMu.Unlock()
	if registered != 0 {
		t.Errorf("forwards after close = %d", registered)
	}
}

func TestDefaultPfCmdArgs(t *testing.T) {
	a := newTestAdapter(&scriptRunner{})
	cmd := a.defaultPfCmd(context.Background(), "apothecary-run1", 41234, 3000)
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "port-forward apothecary-run1 --tcp 41234:3000") {
		t.Errorf("pf cmd = %v", cmd.Args)
	}
}

func TestProvisionCreatesThenWaits(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: `[]`},        // status: absent
		{stdout: ``, code: 0}, // create
		{stdout: listRunning}, // status poll: running
		{stdout: ``, code: 0}, // ssh -- true probe
	}}
	a := newTestAdapter(r)
	id, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "RUN1", RepoURL: "https://g.local/r.git", Branch: "apothecary/x"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "apothecary-run1" {
		t.Errorf("env id = %s", id)
	}
	create := r.calls[1]
	joined := strings.Join(create.args, " ")
	for _, want := range []string{"create apothecary-run1", "--template custom-tmpl",
		"run_id=RUN1", "repo_url=https://g.local/r.git", "branch=apothecary/x", "--yes"} {
		if !strings.Contains(joined, want) {
			t.Errorf("create args missing %q: %v", want, create.args)
		}
	}
}

func TestProvisionReusesExistingWorkspace(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: listRunning}, // status: running already (crash recovery)
		{stdout: listRunning}, // waitReady status
		{stdout: ``, code: 0}, // ssh probe
	}}
	a := newTestAdapter(r)
	id, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "run1"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "apothecary-run1" {
		t.Errorf("id = %s", id)
	}
	for _, c := range r.calls {
		if c.args[0] == "create" {
			t.Error("must not create over an existing workspace")
		}
	}
}

func TestExecSendsScriptOnStdinWithDir(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "ok\n", code: 0},
	}}
	a := newTestAdapter(r)
	res, err := a.Exec(context.Background(), "apothecary-run1", ports.Command{Script: "make test", Dir: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || res.Output != "ok\n" {
		t.Errorf("result = %+v", res)
	}
	c := r.calls[0]
	joined := strings.Join(c.args, " ")
	if !strings.HasPrefix(joined, "ssh apothecary-run1 -- bash -eo pipefail -s") {
		t.Errorf("args = %v", c.args)
	}
	stdin := string(c.stdin)
	if !strings.Contains(stdin, `cd "$HOME/repo"`) || !strings.Contains(stdin, "make test") {
		t.Errorf("stdin = %q", stdin)
	}
}

func TestExecNonzeroExitIsResultNotError(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "FAIL", stderr: "boom", code: 2},
	}}
	a := newTestAdapter(r)
	res, err := a.Exec(context.Background(), "ws", ports.Command{Script: "make test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 2 || !strings.Contains(res.Output, "FAIL") || !strings.Contains(res.Output, "boom") {
		t.Errorf("result = %+v", res)
	}
	if len(r.calls) != 1 {
		t.Errorf("nonzero exit must not retry, got %d calls", len(r.calls))
	}
}

func TestExecDoesNotRetryAmbiguous255(t *testing.T) {
	// Exit 255 is indistinguishable from an ssh transport drop; exec'd
	// commands can have side effects (full agent invocations), so Exec must
	// surface it as a result after ONE attempt, never blind-retry.
	r := &scriptRunner{responses: []response{
		{code: 255, stderr: "connection reset"},
	}}
	a := newTestAdapter(r)
	res, err := a.Exec(context.Background(), "ws", ports.Command{Script: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 255 || len(r.calls) != 1 {
		t.Errorf("result = %+v after %d calls", res, len(r.calls))
	}

	// Spawn failures (nothing ran remotely) still retry, then error.
	r2 := &scriptRunner{responses: []response{
		{err: errors.New("no coder binary")}, {err: errors.New("no coder binary")}, {err: errors.New("no coder binary")},
	}}
	a2 := newTestAdapter(r2)
	if _, err := a2.Exec(context.Background(), "ws", ports.Command{Script: "true"}); err == nil {
		t.Error("expected error after 3 spawn failures")
	}
	if len(r2.calls) != 3 {
		t.Errorf("spawn failures retried %d times, want 3", len(r2.calls))
	}

	// Non-exec CLI calls (probes, status) keep the 255 retry.
	r3 := &scriptRunner{responses: []response{
		{code: 255, stderr: "connection reset"},
		{code: 255, stderr: "connection reset"},
		{stdout: "recovered", code: 0},
	}}
	a3 := newTestAdapter(r3)
	stdout, _, code, err := a3.run(context.Background(), []string{"ssh", "ws", "--", "true"}, nil)
	if err != nil || code != 0 || string(stdout) != "recovered" || len(r3.calls) != 3 {
		t.Errorf("run() 255 retry broken: %q code=%d err=%v calls=%d", stdout, code, err, len(r3.calls))
	}
}

func TestReadFilePathResolutionAndDecode(t *testing.T) {
	content := "# Plan\nsteps"
	r := &scriptRunner{responses: []response{
		{stdout: base64.StdEncoding.EncodeToString([]byte(content)) + "\n", code: 0},
	}}
	a := newTestAdapter(r)
	data, err := a.ReadFile(context.Background(), "ws", "artifacts/plan.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("content = %q", data)
	}
	if !strings.Contains(string(r.calls[0].stdin), `"$HOME"/repo/'artifacts/plan.md'`) {
		t.Errorf("relative path not repo-resolved: %q", r.calls[0].stdin)
	}

	// Absolute path stays absolute.
	r.responses = []response{{stdout: "", code: 0}}
	if _, err := a.ReadFile(context.Background(), "ws", "/apothecary/run.env"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(r.calls[1].stdin), ` '/apothecary/run.env'`) {
		t.Errorf("absolute path mangled: %q", r.calls[1].stdin)
	}
}

func TestReadFileMissingIsErrNotExist(t *testing.T) {
	r := &scriptRunner{responses: []response{{stderr: "MISSING", code: 44}}}
	a := newTestAdapter(r)
	_, err := a.ReadFile(context.Background(), "ws", "artifacts/nope.md")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestWriteFileBase64Payload(t *testing.T) {
	r := &scriptRunner{responses: []response{{code: 0}}}
	a := newTestAdapter(r)
	if err := a.WriteFile(context.Background(), "ws", "/apothecary/run.env", []byte("SECRET=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin := string(r.calls[0].stdin)
	lines := strings.SplitN(stdin, "\n", 2)
	if !strings.Contains(lines[0], `base64 -d > '/apothecary/run.env'`) || !strings.Contains(lines[0], "chmod 600") {
		t.Errorf("script line = %q", lines[0])
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || string(decoded) != "SECRET=x\n" {
		t.Errorf("payload = %q (%v)", lines[1], err)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		json string
		want ports.EnvStatus
	}{
		{`[{"name":"ws","latest_build":{"status":"running"}}]`, ports.EnvRunning},
		{`[{"name":"ws","latest_build":{"status":"starting"}}]`, ports.EnvStarting},
		{`[{"name":"ws","latest_build":{"status":"stopped"}}]`, ports.EnvStopped},
		{`[{"name":"ws","latest_build":{"status":"failed"}}]`, ports.EnvStopped},
		{`[]`, ports.EnvAbsent},
		{`[{"name":"other","latest_build":{"status":"running"}}]`, ports.EnvAbsent},
	}
	for _, tc := range cases {
		r := &scriptRunner{responses: []response{{stdout: tc.json, code: 0}}}
		a := newTestAdapter(r)
		st, err := a.Status(context.Background(), "ws")
		if err != nil {
			t.Fatal(err)
		}
		if st != tc.want {
			t.Errorf("%s -> %s, want %s", tc.json, st, tc.want)
		}
	}
}

func TestTunnelSupervisorRestartsAndStops(t *testing.T) {
	a := newTestAdapter(&scriptRunner{})
	var mu sync.Mutex
	starts := 0
	a.tunnelCmd = func(ctx context.Context, ws string) *exec.Cmd {
		mu.Lock()
		starts++
		mu.Unlock()
		return exec.CommandContext(ctx, "true") // exits immediately -> restart path
	}
	a.ensureTunnel("ws1")
	a.ensureTunnel("ws1") // idempotent: one supervisor

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := starts
		mu.Unlock()
		if n >= 2 { // restarted at least once after exit
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("supervisor never restarted the tunnel (starts=%d)", n)
		}
		time.Sleep(5 * time.Millisecond)
	}

	a.stopTunnel("ws1") // returns only after the supervisor goroutine ends
	mu.Lock()
	after := starts
	mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	if starts != after {
		t.Errorf("tunnel restarted after stop (%d -> %d)", after, starts)
	}
	mu.Unlock()
}

func TestProvisionEstablishesTunnel(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: `[]`}, {code: 0}, {stdout: listRunning}, {code: 0},
	}}
	a := newTestAdapter(r)
	t.Cleanup(a.Close)
	if _, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "run1"}); err != nil {
		t.Fatal(err)
	}
	a.tunnelMu.Lock()
	_, ok := a.tunnels["apothecary-run1"]
	a.tunnelMu.Unlock()
	if !ok {
		t.Error("no tunnel registered after Provision")
	}
}

func TestDestroyToleratesAlreadyGone(t *testing.T) {
	r := &scriptRunner{responses: []response{{stderr: "workspace not found", code: 1}}}
	a := newTestAdapter(r)
	if err := a.Destroy(context.Background(), "ws"); err != nil {
		t.Errorf("destroy of missing workspace should be nil, got %v", err)
	}
}

// TestExecStreamsLiveLog: exec output tees into <RunsDir>/<env>/exec.log
// as it arrives, and TailExecLog serves the tail (ports.ExecLogTailer).
func TestExecStreamsLiveLog(t *testing.T) {
	dir := t.TempDir()
	r := &scriptRunner{responses: []response{
		{stdout: "{\"type\":\"step_start\"}\nworking...\n", code: 0},
	}}
	a := NewWithRunner(Config{
		URL: "http://localhost:7080", Token: "tok", Template: "t", RunsDir: dir,
	}, r)
	a.retryDelay = time.Millisecond
	a.tunnelCmd = func(ctx context.Context, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "300")
	}

	if _, err := a.Exec(context.Background(), "apothecary-run1", ports.Command{Script: "opencode run", Dir: "repo"}); err != nil {
		t.Fatal(err)
	}
	data, err := a.TailExecLog(context.Background(), "apothecary-run1", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"[dir=repo]", "opencode run", "step_start", "working..."} {
		if !strings.Contains(got, want) {
			t.Errorf("exec.log missing %q:\n%s", want, got)
		}
	}
	// Tail cap: ask for a small n and get only the end.
	tailOnly, err := a.TailExecLog(context.Background(), "apothecary-run1", 12)
	if err != nil || len(tailOnly) != 12 || !strings.HasSuffix(got, string(tailOnly)) {
		t.Errorf("tail(12) = %q, %v", tailOnly, err)
	}
	// Unknown env: error, not empty success.
	if _, err := a.TailExecLog(context.Background(), "nope", 0); err == nil {
		t.Error("expected error for unknown env")
	}
}

func TestLinksIncludeSSHAndVSCodeDesktop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/users/lu/workspace/apothecary-run1" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"latest_build":{"resources":[{"agents":[{"name":"main","apps":[{"slug":"code-server"}]}]}]}}`)
	}))
	defer srv.Close()
	a := NewWithRunner(Config{URL: srv.URL, Token: "tok", Owner: "lu"}, &scriptRunner{})

	links, err := a.Links(context.Background(), "apothecary-run1")
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string]ports.AccessLink{}
	for _, l := range links {
		byKind[l.Kind] = l
	}
	if got := byKind["ssh"].Command; got != "coder ssh lu/apothecary-run1" {
		t.Errorf("ssh command = %q", got)
	}
	if byKind["ssh"].URL != "" {
		t.Errorf("ssh link should be command-only, got URL %q", byKind["ssh"].URL)
	}
	vs := byKind["vscode"].URL
	if !strings.HasPrefix(vs, "vscode://coder.coder-remote/open?") {
		t.Errorf("vscode link = %q", vs)
	}
	for _, want := range []string{"owner=lu", "workspace=apothecary-run1", "agent=main", "folder=%2Fhome%2Fapothecary%2Frepo"} {
		if !strings.Contains(vs, want) {
			t.Errorf("vscode link %q missing %q", vs, want)
		}
	}
	if byKind["ide"].URL == "" || byKind["terminal"].URL == "" {
		t.Errorf("web links missing: %+v", links)
	}
}
