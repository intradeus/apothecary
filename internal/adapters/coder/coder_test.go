package coder

import (
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"factory/internal/ports"
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

func (s *scriptRunner) Run(_ context.Context, args []string, stdin []byte) ([]byte, []byte, int, error) {
	s.calls = append(s.calls, call{args: args, stdin: stdin})
	if len(s.responses) == 0 {
		return nil, nil, 0, nil
	}
	r := s.responses[0]
	s.responses = s.responses[1:]
	return []byte(r.stdout), []byte(r.stderr), r.code, r.err
}

func newTestAdapter(r CmdRunner) *Adapter {
	a := NewWithRunner(Config{
		URL: "http://localhost:7080", Token: "tok", Template: "factory", Owner: "lu",
	}, r)
	a.retryDelay = time.Millisecond
	// Never spawn a real `coder ssh` tunnel in unit tests.
	a.tunnelCmd = func(ctx context.Context, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "300")
	}
	return a
}

const listRunning = `[{"name":"factory-run1","latest_build":{"status":"running"}}]`

func TestProvisionCreatesThenWaits(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: `[]`},        // status: absent
		{stdout: ``, code: 0}, // create
		{stdout: listRunning}, // status poll: running
		{stdout: ``, code: 0}, // ssh -- true probe
	}}
	a := newTestAdapter(r)
	id, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "RUN1", RepoURL: "https://g.local/r.git", Branch: "factory/x"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "factory-run1" {
		t.Errorf("env id = %s", id)
	}
	create := r.calls[1]
	joined := strings.Join(create.args, " ")
	for _, want := range []string{"create factory-run1", "--template factory",
		"run_id=RUN1", "repo_url=https://g.local/r.git", "branch=factory/x", "--yes"} {
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
	if id != "factory-run1" {
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
	res, err := a.Exec(context.Background(), "factory-run1", ports.Command{Script: "make test", Dir: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || res.Output != "ok\n" {
		t.Errorf("result = %+v", res)
	}
	c := r.calls[0]
	joined := strings.Join(c.args, " ")
	if !strings.HasPrefix(joined, "ssh factory-run1 -- bash -eo pipefail -s") {
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

func TestTransportFailureRetriesThreeTimes(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{code: 255, stderr: "connection reset"},
		{code: 255, stderr: "connection reset"},
		{stdout: "recovered", code: 0},
	}}
	a := newTestAdapter(r)
	res, err := a.Exec(context.Background(), "ws", ports.Command{Script: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "recovered" || len(r.calls) != 3 {
		t.Errorf("result = %+v after %d calls", res, len(r.calls))
	}

	// All three failing → error.
	r2 := &scriptRunner{responses: []response{
		{err: errors.New("no coder binary")}, {err: errors.New("no coder binary")}, {err: errors.New("no coder binary")},
	}}
	a2 := newTestAdapter(r2)
	if _, err := a2.Exec(context.Background(), "ws", ports.Command{Script: "true"}); err == nil {
		t.Error("expected error after 3 transport failures")
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
	if !strings.Contains(string(r.calls[0].stdin), "$HOME/repo/artifacts/plan.md") {
		t.Errorf("relative path not repo-resolved: %q", r.calls[0].stdin)
	}

	// Absolute path stays absolute.
	r.responses = []response{{stdout: "", code: 0}}
	if _, err := a.ReadFile(context.Background(), "ws", "/factory/run.env"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(r.calls[1].stdin), " /factory/run.env") {
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
	if err := a.WriteFile(context.Background(), "ws", "/factory/run.env", []byte("SECRET=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin := string(r.calls[0].stdin)
	lines := strings.SplitN(stdin, "\n", 2)
	if !strings.Contains(lines[0], "base64 -d > /factory/run.env") || !strings.Contains(lines[0], "chmod 600") {
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
	_, ok := a.tunnels["factory-run1"]
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
