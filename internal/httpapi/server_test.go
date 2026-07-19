package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"factory/internal/adapters/fake"
	"factory/internal/channels"
	"factory/internal/engine"
	"factory/internal/event"
	"factory/internal/flow"
	"factory/internal/ports"
	"factory/internal/run"
)

const loopFlowDoc = `name: loop
defaults: { model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: build, type: agent, agent: builder }
  - { id: test,  type: command, run_ref: verify, on_fail: { to: build, max: 5, feed: output } }
`

type env struct {
	e      *engine.Engine
	store  *event.MemStore
	envp   *fake.EnvProvisioner
	runner *fake.AgentRunner
	forge  *fake.Forge
	reg    *channels.Registry
	fakeCh *fake.Channel
	srv    *Server
	ts     *httptest.Server
}

func newEnv(t *testing.T, cfg engine.Config, docs ...string) *env {
	t.Helper()
	x := &env{
		store:  event.NewMemStore(),
		envp:   fake.NewEnvProvisioner(),
		runner: &fake.AgentRunner{},
		forge:  &fake.Forge{},
		reg:    channels.NewRegistry(),
		fakeCh: &fake.Channel{},
	}
	x.reg.Register("test", func(map[string]any) (ports.NotificationChannel, error) {
		return x.fakeCh, nil
	})
	flows := map[string]*flow.Flow{}
	if len(docs) == 0 {
		docs = []string{loopFlowDoc}
	}
	for _, d := range docs {
		f, err := flow.Parse("t.yaml", []byte(d))
		if err != nil {
			t.Fatal(err)
		}
		flows[f.Name] = f
	}
	if cfg.RepoURL == "" {
		cfg.RepoURL = "https://gitlab.local/g/p.git"
		cfg.DefaultBranch = "main"
	}
	if cfg.Commands == nil {
		cfg.Commands = map[string]string{"setup": "./setup.sh", "verify": "make lint && make test"}
	}
	if cfg.MRPollInterval == 0 {
		cfg.MRPollInterval = 5 * time.Millisecond
	}
	x.e = engine.New(cfg, x.store, x.envp, x.runner, x.forge, []ports.NotificationChannel{x.reg}, flows)
	t.Cleanup(x.e.Shutdown)
	x.srv = New(x.e, x.store, x.reg, cfg.RunsDir)
	x.ts = httptest.NewServer(x.srv.Handler())
	t.Cleanup(x.ts.Close)
	return x
}

func (x *env) do(t *testing.T, method, path string, body any, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, x.ts.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func (x *env) createTicket(t *testing.T, title string) string {
	t.Helper()
	resp, data := x.do(t, "POST", "/api/tickets", map[string]string{"title": title, "goal": "g", "flow": "loop"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/tickets = %d: %s", resp.StatusCode, data)
	}
	var out struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.RunID == "" {
		t.Fatalf("bad ticket response: %s", data)
	}
	return out.RunID
}

func TestTicketRunsAndDetail(t *testing.T) {
	x := newEnv(t, engine.Config{})
	runID := x.createTicket(t, "Add search")
	x.e.Wait(runID)

	resp, data := x.do(t, "GET", "/api/runs", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/runs = %d", resp.StatusCode)
	}
	var views []run.View
	if err := json.Unmarshal(data, &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].RunID != runID || views[0].State != run.Completed {
		t.Errorf("runs = %+v", views)
	}

	resp, data = x.do(t, "GET", "/api/runs/"+runID, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET run = %d", resp.StatusCode)
	}
	var detail struct {
		Run    run.View          `json:"run"`
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(data, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Run.TicketTitle != "Add search" || len(detail.Events) < 5 {
		t.Errorf("detail = %+v (%d events)", detail.Run, len(detail.Events))
	}

	resp, _ = x.do(t, "GET", "/api/runs/nope", nil, nil)
	if resp.StatusCode != 404 {
		t.Errorf("unknown run = %d, want 404", resp.StatusCode)
	}
}

func TestSSEReplayAndLive(t *testing.T) {
	x := newEnv(t, engine.Config{})
	block := make(chan struct{})
	x.runner.RunFunc = func(_ context.Context, call fake.RunCall) (ports.AgentResult, error) {
		<-block
		return ports.AgentResult{TurnsUsed: 1}, nil
	}
	runID := x.createTicket(t, "T")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", x.ts.URL+"/api/runs/"+runID+"/events?after_seq=1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}

	sc := bufio.NewScanner(resp.Body)
	var types []string
	var firstSeq int64
	readEvent := func() event.Event {
		t.Helper()
		var ev event.Event
		for sc.Scan() {
			line := sc.Text()
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					t.Fatalf("bad SSE data %q: %v", data, err)
				}
				return ev
			}
		}
		t.Fatal("stream ended early")
		return ev
	}
	ev := readEvent() // replay starts after seq 1
	firstSeq = ev.Seq
	types = append(types, string(ev.Type))
	if firstSeq != 2 {
		t.Errorf("first streamed seq = %d, want 2 (after_seq=1)", firstSeq)
	}

	close(block) // run continues; live events must arrive on the same stream
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ev := readEvent()
		types = append(types, string(ev.Type))
		if ev.Type == event.TypeRunCompleted {
			break
		}
	}
	if types[len(types)-1] != string(event.TypeRunCompleted) {
		t.Errorf("stream types = %v, want trailing run_completed", types)
	}
}

// TestShimProtocol drives the full §10 contract over HTTP: POST with token
// → 204 + X-Ping-ID (hold expired) → GET re-poll → 200 with the answer.
func TestShimProtocol(t *testing.T) {
	x := newEnv(t, engine.Config{}, `name: ping
defaults: { model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: build, type: agent, agent: builder }
`)
	x.srv.HoldTimeout = 5 * time.Millisecond

	// shimCall avoids t.Fatal (this runs on a non-test goroutine).
	shimCall := func(method, path string, body any, token string) (*http.Response, []byte, error) {
		var rd io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return nil, nil, err
			}
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, x.ts.URL+path, rd)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, nil, err
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, data, nil
	}
	runIDCh := make(chan string, 1)
	gotAnswer := make(chan string, 1)
	fail := func(format string, args ...any) (ports.AgentResult, error) {
		err := fmt.Errorf(format, args...)
		gotAnswer <- "SHIM ERROR: " + err.Error()
		return ports.AgentResult{}, err
	}
	x.runner.RunFunc = func(_ context.Context, call fake.RunCall) (ports.AgentResult, error) {
		runID := <-runIDCh
		token := x.e.RunToken(runID)

		resp, data, err := shimCall("POST", "/api/runs/"+runID+"/pings",
			map[string]string{"type": "blocker", "question": "migration destroys data — proceed?"}, token)
		if err != nil {
			return fail("POST ping: %v", err)
		}
		if resp.StatusCode != http.StatusNoContent {
			// The answer arrived within the hold window (fast path);
			// the test delays its answer to make this practically impossible.
			return fail("POST ping = %d (%s), want 204", resp.StatusCode, data)
		}
		pid := resp.Header.Get("X-Ping-ID")
		if pid == "" {
			return fail("no X-Ping-ID header")
		}
		for { // shim re-poll loop
			resp, data, err := shimCall("GET", "/api/runs/"+runID+"/pings/"+pid, nil, token)
			if err != nil {
				return fail("re-poll: %v", err)
			}
			if resp.StatusCode == http.StatusNoContent {
				continue
			}
			if resp.StatusCode != http.StatusOK {
				return fail("re-poll = %d (%s)", resp.StatusCode, data)
			}
			var out struct {
				Answer string `json:"answer"`
			}
			if err := json.Unmarshal(data, &out); err != nil {
				return fail("re-poll body: %v", err)
			}
			gotAnswer <- out.Answer
			return ports.AgentResult{TurnsUsed: 1}, nil
		}
	}

	resp, data := x.do(t, "POST", "/api/tickets", map[string]string{"title": "T", "flow": "ping"}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("ticket = %d: %s", resp.StatusCode, data)
	}
	var created struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(data, &created)
	runIDCh <- created.RunID

	// Inbox shows the pending ping; answer it via the CPUI endpoint.
	var pid string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && pid == "" {
		_, data := x.do(t, "GET", "/api/pings?status=pending", nil, nil)
		var pings []struct {
			PingID string `json:"ping_id"`
			Type   string `json:"type"`
		}
		_ = json.Unmarshal(data, &pings)
		if len(pings) > 0 {
			if pings[0].Type != "blocker" {
				t.Fatalf("inbox ping = %+v", pings[0])
			}
			pid = pings[0].PingID
		}
		time.Sleep(2 * time.Millisecond)
	}
	if pid == "" {
		t.Fatal("ping never reached the inbox")
	}
	// Let the first long-poll hold (5 ms) expire so the 204 + re-poll path
	// is what the shim actually exercises.
	time.Sleep(100 * time.Millisecond)
	resp, data = x.do(t, "POST", "/api/pings/"+pid+"/answer", map[string]string{"answer": "yes, staging data only"}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("answer = %d: %s", resp.StatusCode, data)
	}
	if got := <-gotAnswer; got != "yes, staging data only" {
		t.Errorf("shim received %q", got)
	}
	x.e.Wait(created.RunID)

	// Double answer → 409.
	resp, _ = x.do(t, "POST", "/api/pings/"+pid+"/answer", map[string]string{"answer": "again"}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("double answer = %d, want 409", resp.StatusCode)
	}
}

func TestPingAuthRequired(t *testing.T) {
	x := newEnv(t, engine.Config{})
	block := make(chan struct{})
	x.runner.RunFunc = func(_ context.Context, call fake.RunCall) (ports.AgentResult, error) {
		<-block
		return ports.AgentResult{TurnsUsed: 1}, nil
	}
	runID := x.createTicket(t, "T")
	defer close(block)

	for _, hdr := range []map[string]string{nil, {"Authorization": "Bearer wrong"}} {
		resp, _ := x.do(t, "POST", "/api/runs/"+runID+"/pings",
			map[string]string{"type": "clarification", "question": "q"}, hdr)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("hdr %v: status = %d, want 401", hdr, resp.StatusCode)
		}
	}
}

func TestAbortAndArchiveAndCapacity(t *testing.T) {
	x := newEnv(t, engine.Config{Limits: engine.Limits{MaxEnvironments: 1}})
	if _, err := x.reg.Add("test", nil); err != nil {
		t.Fatal(err)
	}
	runID := x.createTicket(t, "first")
	x.e.Wait(runID) // completed; env kept until archive/reaper

	// Env cap reached → 409 naming the archivable run + blocker notification.
	resp, data := x.do(t, "POST", "/api/tickets", map[string]string{"title": "second", "flow": "loop"}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("at capacity = %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), runID) {
		t.Errorf("409 body should name archivable run: %s", data)
	}
	sent := x.fakeCh.Sent()
	if len(sent) != 1 || sent[0].Type != "blocker" || !strings.Contains(sent[0].Body, runID) {
		t.Errorf("blocker notification = %+v", sent)
	}

	// Archive frees the env; ticket now accepted.
	resp, data = x.do(t, "POST", "/api/runs/"+runID+"/archive", nil, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("archive = %d: %s", resp.StatusCode, data)
	}
	if len(x.envp.Destroyed) != 1 {
		t.Errorf("archive destroyed %d envs, want 1", len(x.envp.Destroyed))
	}
	id2 := x.createTicket(t, "second")
	x.e.Wait(id2)

	// Abort an in-flight run via the API.
	block := make(chan struct{})
	x.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (ports.AgentResult, error) {
		close(block)
		<-ctx.Done()
		return ports.AgentResult{}, ctx.Err()
	}
	// Second env still live → archive id2 first to stay under the cap.
	if resp, _ := x.do(t, "POST", "/api/runs/"+id2+"/archive", nil, nil); resp.StatusCode != 202 {
		t.Fatalf("archive id2 = %d", resp.StatusCode)
	}
	id3 := x.createTicket(t, "third")
	<-block
	if resp, _ := x.do(t, "POST", "/api/runs/"+id3+"/abort", nil, nil); resp.StatusCode != 202 {
		t.Fatalf("abort = %d", resp.StatusCode)
	}
	x.e.Wait(id3)
	_, data = x.do(t, "GET", "/api/runs/"+id3, nil, nil)
	if !strings.Contains(string(data), `"state":"failed"`) {
		t.Errorf("aborted run detail: %s", data)
	}

	// Archiving a non-terminal (never-started) run → 409; unknown → 404.
	if resp, _ := x.do(t, "POST", "/api/runs/nope/archive", nil, nil); resp.StatusCode != 404 {
		t.Errorf("archive unknown = %d, want 404", resp.StatusCode)
	}
}

func TestChannelsCRUD(t *testing.T) {
	x := newEnv(t, engine.Config{})

	resp, data := x.do(t, "POST", "/api/channels",
		map[string]any{"kind": "test", "room_id": "!ops:matrix.local"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add channel = %d: %s", resp.StatusCode, data)
	}
	var entry channels.Entry
	if err := json.Unmarshal(data, &entry); err != nil || entry.ID == "" || entry.Kind != "test" {
		t.Fatalf("entry = %s", data)
	}
	if entry.Config["room_id"] != "!ops:matrix.local" {
		t.Errorf("config = %+v", entry.Config)
	}

	_, data = x.do(t, "GET", "/api/channels", nil, nil)
	var list []channels.Entry
	if err := json.Unmarshal(data, &list); err != nil || len(list) != 1 {
		t.Fatalf("list = %s", data)
	}

	if resp, _ := x.do(t, "POST", "/api/channels", map[string]any{"kind": "carrier-pigeon"}, nil); resp.StatusCode != 400 {
		t.Errorf("unknown kind = %d, want 400", resp.StatusCode)
	}
	if resp, _ := x.do(t, "DELETE", "/api/channels/"+entry.ID, nil, nil); resp.StatusCode != 204 {
		t.Errorf("delete = %d, want 204", resp.StatusCode)
	}
	if resp, _ := x.do(t, "DELETE", "/api/channels/"+entry.ID, nil, nil); resp.StatusCode != 404 {
		t.Errorf("double delete = %d, want 404", resp.StatusCode)
	}
}

func TestLogsEndpoint(t *testing.T) {
	dir := t.TempDir()
	x := newEnv(t, engine.Config{RunsDir: dir})
	x.envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "make lint") {
			return ports.ExecResult{ExitCode: 0, Output: "=== RUN TestAll\n--- PASS: all 42 tests"}, nil
		}
		return ports.ExecResult{ExitCode: 0}, nil
	}
	runID := x.createTicket(t, "T")
	x.e.Wait(runID)

	resp, data := x.do(t, "GET", "/api/runs/"+runID+"/logs/test", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), "PASS: all 42 tests") {
		t.Errorf("log = %d %q", resp.StatusCode, data)
	}
	// Tail truncation via ?n=.
	_, data = x.do(t, "GET", "/api/runs/"+runID+"/logs/test?n=8", nil, nil)
	if string(data) != "42 tests" {
		t.Errorf("tail = %q, want last 8 bytes", data)
	}
	if resp, _ := x.do(t, "GET", "/api/runs/"+runID+"/logs/nosuch", nil, nil); resp.StatusCode != 404 {
		t.Errorf("unknown stage log = %d, want 404", resp.StatusCode)
	}
}
