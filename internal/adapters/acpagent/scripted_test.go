package acpagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/workspace/sidecar"
)

// sideFake is the sidecar's HTTP API with nothing behind it: where the tests
// beside runner_test.go drive a real shim and a scripted ACP agent, this one
// scripts the sidecar's answers, to reach what a healthy pair never says.
type sideFake struct {
	mu       sync.Mutex
	srv      *httptest.Server
	boot     string
	seq      int64
	init     string
	fail     map[string]int // path → status to answer with
	status   sidecar.Status
	session  sidecar.SessionResponse
	buffered []sidecar.Event
	sessions []sidecar.SessionRequest
	starts   []sidecar.StartRequest
	prompts  []sidecar.PromptRequest
	cancels  int
	stops    int
	afters   []string // the ?after= of each follow
	// log is every event pushed; each follow streams those after its
	// ?after=, as the sidecar does, so a follow whose client has gone (but
	// the server hasn't noticed yet) can't swallow the next follow's events.
	log      []sidecar.Event
	logged   chan struct{} // closed and replaced on each push
	drop     chan struct{}
	onPrompt func(sidecar.PromptRequest)
	onCancel func()
}

func newSideFake(t *testing.T) *sideFake {
	t.Helper()
	f := &sideFake{boot: "b1", init: "{}", fail: map[string]int{}, logged: make(chan struct{}), drop: make(chan struct{}, 1),
		session: sidecar.SessionResponse{SessionID: "s1"}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(func() {
		f.srv.CloseClientConnections()
		f.srv.Close()
	})
	return f
}

func (f *sideFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	code := f.fail[r.URL.Path]
	f.mu.Unlock()
	if code != 0 {
		http.Error(w, `{"error":"scripted failure"}`, code)
		return
	}
	switch r.URL.Path {
	case "/agent/start":
		var req sidecar.StartRequest
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.starts = append(f.starts, req)
		f.mu.Unlock()
		fmt.Fprintf(w, `{"boot":%q,"init":%s,"proc":"p1"}`, f.boot, f.init)
	case "/agent/session":
		var req sidecar.SessionRequest
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.sessions = append(f.sessions, req)
		resp := f.session
		f.mu.Unlock()
		json.NewEncoder(w).Encode(resp)
	case "/agent/prompt":
		var req sidecar.PromptRequest
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.prompts = append(f.prompts, req)
		fn := f.onPrompt
		f.mu.Unlock()
		if fn != nil {
			fn(req)
		}
		w.Write([]byte("{}"))
	case "/agent/cancel":
		f.mu.Lock()
		f.cancels++
		fn := f.onCancel
		f.mu.Unlock()
		if fn != nil {
			fn()
		}
		w.Write([]byte("{}"))
	case "/agent/stop":
		f.mu.Lock()
		f.stops++
		f.mu.Unlock()
		w.Write([]byte("{}"))
	case "/agent/status":
		f.mu.Lock()
		st := f.status
		if st.LastSeq == 0 {
			st.LastSeq = f.seq
		}
		f.mu.Unlock()
		if st.Boot == "" {
			st.Boot = f.boot
		}
		json.NewEncoder(w).Encode(st)
	case "/agent/events":
		f.events(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *sideFake) events(w http.ResponseWriter, r *http.Request) {
	enc := json.NewEncoder(w)
	if r.URL.Query().Get("follow") == "0" {
		f.mu.Lock()
		evs := f.buffered
		f.mu.Unlock()
		for _, ev := range evs {
			enc.Encode(ev)
		}
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	f.mu.Lock()
	f.afters = append(f.afters, r.URL.Query().Get("after"))
	f.mu.Unlock()
	w.(http.Flusher).Flush()
	for next := 0; ; {
		f.mu.Lock()
		evs := f.log[next:]
		next = len(f.log)
		logged := f.logged
		f.mu.Unlock()
		for _, ev := range evs {
			if ev.Seq > after {
				enc.Encode(ev)
			}
		}
		w.(http.Flusher).Flush()
		select {
		case <-logged:
		case <-f.drop:
			return
		case <-r.Context().Done():
			return
		}
	}
}

// push queues events on the stream, numbering them.
func (f *sideFake) push(evs ...sidecar.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range evs {
		f.seq++
		if ev.Seq == 0 {
			ev.Seq = f.seq
		}
		if ev.Boot == "" {
			ev.Boot = f.boot
		}
		f.log = append(f.log, ev)
	}
	if len(evs) > 0 {
		close(f.logged)
		f.logged = make(chan struct{})
	}
}

// turn scripts one prompt: its events, then a clean end.
func (f *sideFake) endTurn(evs ...sidecar.Event) {
	f.onPrompt = func(p sidecar.PromptRequest) {
		for i := range evs {
			evs[i].Turn = p.Turn
		}
		f.push(evs...)
		f.push(sidecar.Event{Session: p.SessionID, Turn: p.Turn, Type: "turn_end", StopReason: "end_turn"})
	}
}

type tcpDial string

func (d tcpDial) Dial(ctx context.Context) (net.Conn, error) {
	var nd net.Dialer
	return nd.DialContext(ctx, "tcp", string(d))
}
func (d tcpDial) Close() error { return nil }

type sideEnv struct {
	envport.EnvProvisioner
	conn envport.PortConn
	err  error
}

func (e sideEnv) Sidecar(context.Context, envport.EnvID) (envport.PortConn, error) {
	return e.conn, e.err
}

func scripted(f *sideFake, p Profile) *Runner {
	if p.Harness == "" {
		p.Harness = "fake"
	}
	if p.Command == "" {
		p.Command = "agent"
	}
	r := New(sideEnv{conn: tcpDial(strings.TrimPrefix(f.srv.URL, "http://"))}, p)
	r.cancelGrace = time.Second
	return r
}

var bg = context.Background()

func TestCanReadOnly(t *testing.T) {
	if New(nil, Profile{}).CanReadOnly() {
		t.Error("no promise made")
	}
	if !New(nil, Profile{ReadOnlyBy: ReadOnlyByAgent}).CanReadOnly() {
		t.Error("promise ignored")
	}
}

func TestNoSidecarIsAnError(t *testing.T) {
	r := New(fake.NewEnvProvisioner(), Profile{Harness: "h"})
	if _, err := r.Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{}); err == nil || !strings.Contains(err.Error(), "cannot host the agent sidecar") {
		t.Errorf("Run = %v", err)
	}
	if _, err := r.Resume(bg, "e", "s", agentport.AgentInput{}); err == nil {
		t.Error("Resume went ahead")
	}
	if _, err := r.AgentModels(bg, "e"); err == nil {
		t.Error("AgentModels went ahead")
	}
	if err := r.StopAgent(bg, "e"); err == nil {
		t.Error("StopAgent went ahead")
	}
	if r.Reattachable(bg, "e", "s", "") {
		t.Error("Reattachable with no sidecar")
	}
	if _, ok, err := r.Reattach(bg, "e", "s", "", agentport.AgentInput{}); ok || err != nil {
		t.Errorf("Reattach = %v, %v (false: the engine then restarts the stage)", ok, err)
	}
	boom := errors.New("workspace gone")
	r = New(sideEnv{err: boom}, Profile{Harness: "h"})
	if _, err := r.Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{}); !errors.Is(err, boom) || !strings.Contains(err.Error(), "start workspace sidecar") {
		t.Errorf("sidecar failure = %v", err)
	}
}

func TestStartFailures(t *testing.T) {
	f := newSideFake(t)
	f.fail["/agent/start"] = 500
	r := scripted(f, Profile{})
	if _, err := r.Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{}); err == nil || !strings.Contains(err.Error(), "start agent") {
		t.Errorf("Run = %v", err)
	}
	if _, err := r.Resume(bg, "e", "s", agentport.AgentInput{}); err == nil {
		t.Error("Resume went ahead")
	}
	if _, err := r.AgentModels(bg, "e"); err == nil {
		t.Error("AgentModels went ahead")
	}
	f.status.Turns = []sidecar.TurnStatus{{ID: "t", Session: "s"}}
	if _, ok, err := r.Reattach(bg, "e", "s", "", agentport.AgentInput{}); ok || err != nil {
		t.Errorf("Reattach = %v, %v", ok, err)
	}
}

func TestMCPServerShapes(t *testing.T) {
	bad := []agentport.MCPServer{{Name: "empty"}}
	f := newSideFake(t)
	r := scripted(f, Profile{})
	if _, err := r.Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{MCPServers: bad}); err == nil || !strings.Contains(err.Error(), "neither a command nor a url") {
		t.Errorf("Run = %v", err)
	}
	if res, err := r.Resume(bg, "e", "s9", agentport.AgentInput{MCPServers: bad}); err == nil || res.Session != "s9" {
		t.Errorf("Resume = %+v, %v (keeps the session)", res, err)
	}
	if len(f.sessions) != 0 {
		t.Errorf("a session opened with a bad server: %+v", f.sessions)
	}
	// Name clash with the harness's own server.
	r = scripted(f, Profile{MCPServers: []agentport.MCPServer{{Name: "ask_human", Command: []string{"x"}}}})
	_, err := r.Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{MCPServers: []agentport.MCPServer{{Name: "ask_human", Command: []string{"y"}}}})
	if err == nil || !strings.Contains(err.Error(), "is the harness's own") {
		t.Errorf("clash = %v", err)
	}
}

func TestRemoteMCPServerReachesASessionThatTakesThem(t *testing.T) {
	f := newSideFake(t)
	f.init = `{"agentCapabilities":{"mcpCapabilities":{"http":true}}}`
	f.endTurn()
	r := scripted(f, Profile{})
	in := agentport.AgentInput{MCPServers: []agentport.MCPServer{
		{Name: "r", URL: "https://mcp.test", Headers: map[string]string{"b": "2", "a": "1"}},
		{Name: "l", Command: []string{"cmd", "arg"}, Env: map[string]string{"K": "V"}},
	}}
	if _, err := r.Run(bg, "e", agentport.AgentSpec{}, in); err != nil {
		t.Fatal(err)
	}
	got := f.sessions[0].MCPServers
	if len(got) != 2 || got[0].URL != "https://mcp.test" || got[0].Headers[0].Name != "a" || got[0].Headers[1].Value != "2" {
		t.Errorf("remote = %+v", got)
	}
	if got[1].Command != "cmd" || got[1].Args[0] != "arg" || got[1].Env[0].Name != "K" {
		t.Errorf("local = %+v", got[1])
	}
}

func TestRunSessionAndPreamble(t *testing.T) {
	f := newSideFake(t)
	f.endTurn()
	p := Profile{Preamble: func(_ context.Context, _ envport.EnvID, s agentport.AgentSpec) (string, error) {
		return "ROLE " + s.Agent + "\n", nil
	}, Mode: func(s agentport.AgentSpec) string { return "mode-" + s.Agent }}
	r := scripted(f, p)
	res, err := r.Run(bg, "e", agentport.AgentSpec{Agent: "planner", Model: "fake/one"}, agentport.AgentInput{Prompt: "do it"})
	if err != nil || res.Session != "s1" {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if f.prompts[0].Text != "ROLE planner\ndo it" {
		t.Errorf("prompt = %q", f.prompts[0].Text)
	}
	if s := f.sessions[0]; s.Mode != "mode-planner" || s.Model != "fake/one" || s.Proc != "p1" {
		t.Errorf("session = %+v", s)
	}
	if !strings.Contains(f.starts[0].Argv[2], "exec agent") || !strings.Contains(f.starts[0].Argv[2], "provider.env") {
		t.Errorf("start = %v", f.starts[0].Argv)
	}

	p.Preamble = func(context.Context, envport.EnvID, agentport.AgentSpec) (string, error) {
		return "", errors.New("no role")
	}
	r = scripted(f, p)
	res, err = r.Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{})
	if err == nil || res.Session != "s1" {
		t.Errorf("preamble failure = %+v, %v", res, err)
	}

	f.fail["/agent/session"] = 500
	if _, err := r.Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{}); err == nil || !strings.Contains(err.Error(), "fake:") {
		t.Errorf("session failure = %v", err)
	}
}

func TestReadOnlyByMode(t *testing.T) {
	f := newSideFake(t)
	f.endTurn()
	r := scripted(f, Profile{ReadOnlyBy: ReadOnlyByMode, ReadOnlyMode: "read-only",
		Mode: func(agentport.AgentSpec) string { return "full" }})
	if _, err := r.Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resume(bg, "e", "s1", agentport.AgentInput{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	for i, s := range f.sessions {
		if s.Mode != "read-only" || !s.ReadOnly {
			t.Errorf("session %d = %+v", i, s)
		}
	}
}

// A stage's harness options choose its process on every path: the session
// it runs in, the reply that resumes it and the reattach that follows it
// all start the same command line. A read-only session never takes them.
func TestHarnessOptionsReachEveryStart(t *testing.T) {
	opts := map[string]string{"advisor": "acme/adv"}
	calls := 0
	profile := Profile{OptionArgs: func(_ context.Context, _ envport.EnvID, got map[string]string) (string, error) {
		calls++
		if got["advisor"] != "acme/adv" {
			t.Errorf("options = %v", got)
		}
		return "--opt", nil
	}}
	if !scripted(newSideFake(t), profile).TakesHarnessOptions() {
		t.Error("a profile with OptionArgs takes options")
	}
	// Each path on its own sidecar, so one's stream position cannot stall
	// the next; the command line it started is all that is asserted.
	started := func(name string, call func(r *Runner, f *sideFake) error) string {
		f := newSideFake(t)
		f.endTurn()
		if err := call(scripted(f, profile), f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(f.starts) != 1 {
			t.Fatalf("%s: starts = %+v, want one", name, f.starts)
		}
		return f.starts[0].Argv[2]
	}
	for name, call := range map[string]func(r *Runner, f *sideFake) error{
		"run": func(r *Runner, _ *sideFake) error {
			_, err := r.Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{HarnessOptions: opts})
			return err
		},
		"reply": func(r *Runner, _ *sideFake) error {
			_, err := r.Resume(bg, "e", "s1", agentport.AgentInput{HarnessOptions: opts, Prompt: "reply"})
			return err
		},
		"reattach": func(r *Runner, f *sideFake) error {
			f.status = sidecar.Status{LastSeq: 40, Turns: []sidecar.TurnStatus{{ID: "t9", Session: "s1"}}, Sessions: map[string]float64{"s1": 1}}
			f.push(sidecar.Event{Session: "s1", Turn: "t9", Type: "turn_end", StopReason: "end_turn"})
			if _, ok, err := r.Reattach(bg, "e", "s1", "", agentport.AgentInput{HarnessOptions: opts}); !ok || err != nil {
				return fmt.Errorf("reattach = %v, %v", ok, err)
			}
			return nil
		},
	} {
		if argv := started(name, call); !strings.HasSuffix(argv, "exec agent --opt") {
			t.Errorf("%s started %q, want the options on the command line", name, argv)
		}
	}
	if calls != 3 {
		t.Errorf("OptionArgs ran %d times, want 3", calls)
	}
	readOnly := started("read-only", func(r *Runner, _ *sideFake) error {
		_, err := r.Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{HarnessOptions: opts, ReadOnly: true})
		return err
	})
	if calls != 3 || strings.Contains(readOnly, "--opt") {
		t.Errorf("a read-only session took the options: %d calls, %q", calls, readOnly)
	}

	none := scripted(newSideFake(t), Profile{})
	if none.TakesHarnessOptions() {
		t.Error("a profile without OptionArgs takes no options")
	}
	if _, err := none.Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{HarnessOptions: opts}); err == nil || !strings.Contains(err.Error(), "takes no harness options") {
		t.Errorf("options on a harness that takes none = %v", err)
	}
}

func TestResumePaths(t *testing.T) {
	r := scripted(newSideFake(t), Profile{})
	if _, err := r.Resume(bg, "e", "", agentport.AgentInput{}); err == nil || !strings.Contains(err.Error(), "no session recorded") {
		t.Errorf("no session = %v", err)
	}
	f := newSideFake(t)
	f.endTurn()
	r = scripted(f, Profile{Mode: func(s agentport.AgentSpec) string { return "m-" + s.Agent }})
	attempt := agentport.AgentSpec{Agent: "fixer", Model: "fake/two"}
	if _, err := r.Resume(bg, "e", "s1", agentport.AgentInput{Attempt: &attempt, Prompt: "again"}); err != nil {
		t.Fatal(err)
	}
	if s := f.sessions[0]; s.SessionID != "s1" || s.Mode != "m-fixer" || s.Model != "fake/two" {
		t.Errorf("session = %+v", s)
	}
	if _, err := r.Resume(bg, "e", "s1", agentport.AgentInput{Model: "fake/three"}); err != nil {
		t.Fatal(err)
	}
	if f.sessions[1].Model != "fake/three" {
		t.Errorf("model switch = %+v", f.sessions[1])
	}
	f.fail["/agent/session"] = 404
	res, err := r.Resume(bg, "e", "s1", agentport.AgentInput{})
	if !errors.Is(err, agentport.ErrSessionGone) || res.Session != "s1" {
		t.Errorf("gone = %+v, %v", res, err)
	}
}

func TestAgentModelsAndStop(t *testing.T) {
	f := newSideFake(t)
	f.session.Models = []string{"fake/a", "fake/b"}
	r := scripted(f, Profile{})
	got, err := r.AgentModels(bg, "e")
	if err != nil || len(got) != 2 {
		t.Errorf("models = %v, %v", got, err)
	}
	f.fail["/agent/session"] = 500
	if _, err := r.AgentModels(bg, "e"); err == nil {
		t.Error("session failure swallowed")
	}
	if err := r.StopAgent(bg, "e"); err != nil || f.stops != 1 {
		t.Errorf("stop = %v, %d", err, f.stops)
	}
}

func TestReattachability(t *testing.T) {
	r := func(f *sideFake) *Runner { return scripted(f, Profile{}) }
	if r(newSideFake(t)).Reattachable(bg, "e", "", "") {
		t.Error("no session is not reattachable")
	}
	if _, ok, _ := r(newSideFake(t)).Reattach(bg, "e", "", "", agentport.AgentInput{}); ok {
		t.Error("no session reattached")
	}
	t.Run("status failure", func(t *testing.T) {
		f := newSideFake(t)
		f.fail["/agent/status"] = 500
		if r(f).Reattachable(bg, "e", "s1", "") {
			t.Error("reattachable on no information")
		}
	})
	t.Run("turn in flight", func(t *testing.T) {
		f := newSideFake(t)
		f.status.Turns = []sidecar.TurnStatus{{ID: "t1", Session: "s1"}}
		if !r(f).Reattachable(bg, "e", "s1", "") {
			t.Error("a live turn must be reattachable")
		}
	})
	t.Run("the sidecar restarted", func(t *testing.T) {
		f := newSideFake(t)
		f.buffered = []sidecar.Event{{Session: "s1", Turn: "t1", Type: "turn_end"}}
		if r(f).Reattachable(bg, "e", "s1", "old-boot:3") {
			t.Error("a turn from another boot does not survive")
		}
	})
	t.Run("no cursor reads the whole buffer", func(t *testing.T) {
		f := newSideFake(t)
		f.buffered = []sidecar.Event{{Session: "other", Turn: "x", Type: "turn_end"}, {Session: "s1", Turn: "t1", Type: "turn_start"}, {Session: "s1", Type: "message"}}
		if !r(f).Reattachable(bg, "e", "s1", "") {
			t.Error("a buffered turn must be reattachable")
		}
		f.buffered = f.buffered[:1]
		if r(f).Reattachable(bg, "e", "s1", "") {
			t.Error("another session's turn matched")
		}
	})
	t.Run("look-back refused", func(t *testing.T) {
		f := newSideFake(t)
		f.fail["/agent/events"] = 500
		if r(f).Reattachable(bg, "e", "s1", "b1:0") {
			t.Error("reattachable on a refused look-back")
		}
	})
	t.Run("nothing to follow", func(t *testing.T) {
		f := newSideFake(t)
		res, ok, err := r(f).Reattach(bg, "e", "s1", "", agentport.AgentInput{})
		if ok || err != nil || res.Session != "s1" {
			t.Errorf("= %+v, %v, %v", res, ok, err)
		}
	})
}

// With no recorded position the whole buffer replays for the session.
func TestReattachWithoutACursorReplays(t *testing.T) {
	f := newSideFake(t)
	f.status = sidecar.Status{LastSeq: 40, Turns: []sidecar.TurnStatus{{ID: "t1", Session: "s1"}}, Sessions: map[string]float64{"s1": 1.5}}
	f.push(sidecar.Event{Session: "s1", Turn: "t1", Type: "message", Text: "still here"},
		sidecar.Event{Session: "s1", Turn: "t1", Type: "turn_end", StopReason: "end_turn"})
	res, ok, err := scripted(f, Profile{}).Reattach(bg, "e", "s1", "", agentport.AgentInput{})
	if !ok || err != nil || res.Output != "still here" {
		t.Fatalf("= %+v, %v, %v", res, ok, err)
	}
	if f.afters[0] != "0" {
		t.Errorf("followed from %q, want the buffer's start", f.afters[0])
	}
	if len(f.prompts) != 0 {
		t.Errorf("a reattach prompted: %+v", f.prompts)
	}
}

func TestDriveStatusFailure(t *testing.T) {
	f := newSideFake(t)
	f.fail["/agent/status"] = 500
	res, err := scripted(f, Profile{}).Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{})
	if err == nil || !strings.Contains(err.Error(), "sidecar status") || res.Session != "s1" {
		t.Errorf("= %+v, %v", res, err)
	}
}

func TestDrivePromptFailure(t *testing.T) {
	f := newSideFake(t)
	f.fail["/agent/prompt"] = 500
	res, err := scripted(f, Profile{}).Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{})
	if err == nil || !strings.Contains(err.Error(), "prompt") || !strings.Contains(res.Output, "prompt") {
		t.Errorf("= %+v, %v (the error is the stage's output when nothing was said)", res, err)
	}
}

// Everything the sidecar reports lands as its event, for this session only.
func TestDriveMapsEvents(t *testing.T) {
	f := newSideFake(t)
	cost := 0.75
	f.onPrompt = func(p sidecar.PromptRequest) {
		ev := func(e sidecar.Event) sidecar.Event { e.Session, e.Turn = p.SessionID, p.Turn; return e }
		f.push(
			ev(sidecar.Event{Type: "turn_start"}),
			sidecar.Event{Session: "other", Type: "message", Text: "not mine"},
			ev(sidecar.Event{Type: "thought", Text: "hmm"}),
			ev(sidecar.Event{Type: "message", Text: "first "}),
			ev(sidecar.Event{Type: "message", Text: "part"}),
			ev(sidecar.Event{Type: "tool_call", Tool: &sidecar.Tool{ID: "t1", Title: "edit", Diffs: []sidecar.Diff{{Path: "a", OldText: "x", NewText: "y"}}}}),
			ev(sidecar.Event{Type: "tool_update"}),
			ev(sidecar.Event{Type: "permission", Text: "may I", Tool: &sidecar.Tool{ID: "t2"}}),
			ev(sidecar.Event{Type: "plan", Plan: []sidecar.Plan{{Content: "step", Status: "pending", Priority: "high"}}}),
			ev(sidecar.Event{Type: "message", Text: "second"}),
			ev(sidecar.Event{Type: "usage", CostUSD: &cost}),
			sidecar.Event{Type: "agent_exit", Proc: "other-proc", Error: "not this one"},
			sidecar.Event{Type: "agent_exit", Proc: "p1", Error: " died ", Text: "stderr tail"},
			ev(sidecar.Event{Type: "turn_start", Turn: "someone-else"}),
			ev(sidecar.Event{Type: "turn_end", StopReason: "end_turn", Usage: &sidecar.TurnUsage{InputTokens: 10, CachedWriteTokens: 5, OutputTokens: 3, ThoughtTokens: 2}}),
		)
	}
	rec := newRecorder()
	f.session.CostUSD = 0.25
	res, err := scripted(f, Profile{}).Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{Events: rec.on})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "first part\n\nsecond" {
		t.Errorf("output = %q", res.Output)
	}
	if res.TurnsUsed != 2 {
		t.Errorf("turns = %d: a message after tools is a new model step", res.TurnsUsed)
	}
	if res.Usage.CostUSD != 0.5 || res.Usage.InputTokens != 15 || res.Usage.OutputTokens != 5 {
		t.Errorf("usage = %+v", res.Usage)
	}
	var notices, toolEvents, perms, plans, thoughts int
	var endUsage *agentport.Usage
	for _, ev := range rec.events {
		switch ev.Kind {
		case agentport.AgentEvNotice:
			notices++
			if !strings.Contains(ev.Text, "died") || !strings.Contains(ev.Text, "stderr tail") {
				t.Errorf("notice = %q", ev.Text)
			}
		case agentport.AgentEvTool:
			toolEvents++
		case agentport.AgentEvPermission:
			perms++
			if ev.Text != "may I" || ev.Tool.ID != "t2" {
				t.Errorf("permission = %+v", ev)
			}
		case agentport.AgentEvPlan:
			plans++
			if ev.Plan[0] != (agentport.AgentPlanEntry{Content: "step", Status: "pending", Priority: "high"}) {
				t.Errorf("plan = %+v", ev.Plan)
			}
		case agentport.AgentEvThought:
			thoughts++
		case agentport.AgentEvTurnEnd:
			endUsage = ev.Usage
		case agentport.AgentEvMessage:
			if ev.Text == "not mine" {
				t.Error("another session's message leaked in")
			}
		case agentport.AgentEvTurnStart:
			if ev.Turn == "someone-else" {
				t.Error("another turn's start leaked in")
			}
		}
	}
	if notices != 1 || toolEvents != 2 || perms != 1 || plans != 1 || thoughts != 1 {
		t.Errorf("notices=%d tools=%d perms=%d plans=%d thoughts=%d", notices, toolEvents, perms, plans, thoughts)
	}
	if endUsage == nil || endUsage.CostUSD != 0.5 || endUsage.InputTokens != 15 || endUsage.OutputTokens != 5 {
		t.Errorf("turn usage = %+v", endUsage)
	}
}

func TestDriveTurnErrorIsTheResult(t *testing.T) {
	f := newSideFake(t)
	f.onPrompt = func(p sidecar.PromptRequest) {
		f.push(sidecar.Event{Session: p.SessionID, Turn: p.Turn, Type: "message", Text: "partial"},
			sidecar.Event{Session: p.SessionID, Turn: p.Turn, Type: "turn_end", StopReason: "error", Error: "rate limited"})
	}
	res, err := scripted(f, Profile{}).Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{})
	if err == nil || !strings.Contains(err.Error(), "rate limited") || res.Output != "partial" {
		t.Errorf("= %+v, %v", res, err)
	}
}

// A new boot on the stream means the agent and its turn are gone.
func TestSidecarRestartMidTurnFails(t *testing.T) {
	f := newSideFake(t)
	f.onPrompt = func(sidecar.PromptRequest) {
		f.push(sidecar.Event{Type: "gap", Boot: "b2"})
	}
	rec := newRecorder()
	_, err := scripted(f, Profile{}).Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{Events: rec.on})
	if !errors.Is(err, errSidecarRestarted) {
		t.Fatalf("err = %v", err)
	}
	if !hasNotice(rec, "sidecar restarted") {
		t.Errorf("no notice: %+v", rec.events)
	}
}

func hasNotice(r *recorder, sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		if ev.Kind == agentport.AgentEvNotice && strings.Contains(ev.Text, sub) {
			return true
		}
	}
	return false
}

// A dropped stream is an engine-side hiccup: it reconnects from where it was.
func TestStreamReconnectsFromItsPosition(t *testing.T) {
	f := newSideFake(t)
	f.seq, f.status.LastSeq = 7, 7
	var turn sidecar.PromptRequest
	f.onPrompt = func(p sidecar.PromptRequest) {
		turn = p
		f.push(sidecar.Event{Session: p.SessionID, Turn: p.Turn, Type: "message", Text: "before"})
	}
	// Dropped only once the event was delivered, so the position has moved.
	events := func(ev agentport.AgentEvent) {
		if ev.Kind != agentport.AgentEvMessage {
			return
		}
		f.drop <- struct{}{}
		go func() {
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				f.mu.Lock()
				n := len(f.afters)
				f.mu.Unlock()
				if n >= 2 {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			f.push(sidecar.Event{Session: turn.SessionID, Turn: turn.Turn, Type: "turn_end", StopReason: "end_turn"})
		}()
	}
	res, err := scripted(f, Profile{}).Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{Events: events})
	if err != nil || res.Output != "before" {
		t.Fatalf("= %+v, %v", res, err)
	}
	if len(f.afters) != 2 || f.afters[0] != "7" || f.afters[1] != "8" {
		t.Errorf("afters = %v", f.afters)
	}
}

// Cancelled, the runner asks the agent to stop and follows the turn to its
// end: what it spent is real.
func TestCancelFollowsTheTurnToItsEnd(t *testing.T) {
	f := newSideFake(t)
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	cost := 2.0
	var turn sidecar.PromptRequest
	f.onPrompt = func(p sidecar.PromptRequest) {
		turn = p
		f.push(sidecar.Event{Session: p.SessionID, Turn: p.Turn, Type: "turn_start"})
	}
	f.onCancel = func() {
		f.push(sidecar.Event{Session: turn.SessionID, Turn: turn.Turn, Type: "usage", CostUSD: &cost},
			sidecar.Event{Session: turn.SessionID, Turn: turn.Turn, Type: "turn_end", StopReason: "cancelled"})
	}
	events := func(ev agentport.AgentEvent) {
		if ev.Kind == agentport.AgentEvTurnStart {
			cancel()
		}
	}
	res, err := scripted(f, Profile{}).Run(ctx, "e", agentport.AgentSpec{}, agentport.AgentInput{Events: events})
	if !errors.Is(err, context.Canceled) || res.Usage.CostUSD != 2 {
		t.Errorf("= %+v, %v", res, err)
	}
	if f.cancels != 1 {
		t.Errorf("cancels = %d", f.cancels)
	}
}

func TestCancelledTurnThatNeverEndsGivesUp(t *testing.T) {
	f := newSideFake(t)
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	f.onPrompt = func(p sidecar.PromptRequest) {
		f.push(sidecar.Event{Session: p.SessionID, Turn: p.Turn, Type: "turn_start"})
	}
	events := func(ev agentport.AgentEvent) {
		if ev.Kind == agentport.AgentEvTurnStart {
			cancel()
		}
	}
	r := scripted(f, Profile{})
	r.cancelGrace = 20 * time.Millisecond
	_, err := r.Run(ctx, "e", agentport.AgentSpec{}, agentport.AgentInput{Events: events})
	if err == nil || !strings.Contains(err.Error(), "did not end within") {
		t.Errorf("err = %v", err)
	}
}

// A release queues the handback and lets go in one breath: the runner's last
// look must catch it.
func TestReleaseRacingTheTurnEndStillPrompts(t *testing.T) {
	f := newSideFake(t)
	f.endTurn()
	rec := &lateInbox{recorder: newRecorder(), late: []agentport.UserMessage{{Text: "handback", Verbatim: true}}}
	res, err := scripted(f, Profile{}).Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{Inbox: rec})
	if err != nil {
		t.Fatal(err)
	}
	if res.TurnsUsed != 2 || len(f.prompts) != 2 || f.prompts[1].Text != "handback" {
		t.Errorf("turns = %d, prompts = %+v", res.TurnsUsed, f.prompts)
	}
}

// lateInbox answers its first Take empty and has the message by the second.
type lateInbox struct {
	*recorder
	takes int
	late  []agentport.UserMessage
}

func (l *lateInbox) Take() []agentport.UserMessage {
	l.takes++
	if l.takes == 2 {
		out := l.late
		l.late = nil
		return out
	}
	return nil
}

func TestHeldSessionEndsWithItsContext(t *testing.T) {
	f := newSideFake(t)
	f.endTurn()
	rec := newRecorder()
	rec.hold(true)
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := scripted(f, Profile{}).Run(ctx, "e", agentport.AgentSpec{}, agentport.AgentInput{Inbox: rec, Events: rec.on})
		done <- err
	}()
	waitTurnEnd(t, rec)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a held session outlived its context")
	}
}

func TestHeldSessionFailsWhenTheSidecarRestarts(t *testing.T) {
	f := newSideFake(t)
	f.endTurn()
	rec := newRecorder()
	rec.hold(true)
	done := make(chan error, 1)
	go func() {
		_, err := scripted(f, Profile{}).Run(bg, "e", agentport.AgentSpec{}, agentport.AgentInput{Inbox: rec, Events: rec.on})
		done <- err
	}()
	waitTurnEnd(t, rec)
	f.push(sidecar.Event{Type: "gap", Boot: "b2"})
	select {
	case err := <-done:
		if !errors.Is(err, errSidecarRestarted) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a held session outlived its sidecar")
	}
}

func waitTurnEnd(t *testing.T, r *recorder) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		for _, ev := range r.events {
			if ev.Kind == agentport.AgentEvTurnEnd {
				r.mu.Unlock()
				return
			}
		}
		r.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the turn never ended")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestFormatMessages(t *testing.T) {
	cases := []struct {
		name string
		in   []agentport.UserMessage
		want string
	}{
		{"none", nil, ""},
		{"one", []agentport.UserMessage{{Text: "hi"}}, "Message from the human operator:\n\nhi"},
		{"many", []agentport.UserMessage{{Text: "a"}, {Text: "b"}}, "Messages from the human operator:\n\n1. a\n\n2. b\n"},
		{"verbatim alone", []agentport.UserMessage{{Text: "v", Verbatim: true}}, "v"},
		{"verbatim follows", []agentport.UserMessage{{Text: "v", Verbatim: true}, {Text: "h"}, {Text: "w", Verbatim: true}},
			"Message from the human operator:\n\nh\n\nv\n\nw"},
	}
	for _, c := range cases {
		if got := FormatMessages(c.in); got != c.want {
			t.Errorf("%s = %q", c.name, got)
		}
	}
}

func TestToTool(t *testing.T) {
	if toTool(nil) != nil {
		t.Error("nil tool")
	}
	got := toTool(&sidecar.Tool{ID: "t", Diffs: []sidecar.Diff{{Path: "p", OldText: "o", NewText: "n"}}})
	if got.ID != "t" || got.Diffs[0] != (agentport.AgentDiff{Path: "p", OldText: "o", NewText: "n"}) {
		t.Errorf("tool = %+v", got)
	}
}

func TestPairsAreSorted(t *testing.T) {
	if pairs(nil) != nil {
		t.Error("nil map")
	}
	got := pairs(map[string]string{"b": "2", "a": "1", "c": "3"})
	if got[0].Name != "a" || got[2].Value != "3" {
		t.Errorf("pairs = %+v", got)
	}
}

func TestWriteRolesAndPreamble(t *testing.T) {
	envp := fake.NewEnvProvisioner()
	id, _ := envp.Provision(bg, envport.EnvSpec{RunID: "r"})
	specs := []*agentspec.Spec{{Name: "planner", Prompt: "  Plan.\n", ReadOnly: true}, {Name: "builder", Prompt: "Build.", AllowBash: true}}
	if err := WriteRoles(bg, envp, id, specs); err != nil {
		t.Fatal(err)
	}
	role := string(envp.Env(id).Files["/apothecary/agents/planner.role"])
	if !strings.HasPrefix(role, "# ROLE\nPlan.\n") || !strings.Contains(role, "READ-ONLY") || !strings.Contains(role, "Do not run shell commands") {
		t.Errorf("role = %q", role)
	}
	builder := string(envp.Env(id).Files["/apothecary/agents/builder.role"])
	if strings.Contains(builder, "READ-ONLY") || strings.Contains(builder, "Do not run shell commands") {
		t.Errorf("builder = %q", builder)
	}
	pre := RolePreamble(envp, "h")
	if got, err := pre(bg, id, agentport.AgentSpec{Agent: "planner"}); err != nil || got != role {
		t.Errorf("preamble = %q, %v", got, err)
	}
	if got, err := pre(bg, id, agentport.AgentSpec{}); err != nil || got != "" {
		t.Errorf("no agent = %q, %v", got, err)
	}
	if _, err := pre(bg, id, agentport.AgentSpec{Agent: "ghost"}); err == nil || !strings.Contains(err.Error(), `h: role for agent "ghost"`) {
		t.Errorf("ghost = %v", err)
	}
	if err := WriteRoles(bg, envp, "gone", specs); err == nil || !strings.Contains(err.Error(), "write role planner") {
		t.Errorf("write failure = %v", err)
	}
}

func TestInstallSkillsExecFailure(t *testing.T) {
	envp := fake.NewEnvProvisioner()
	if err := InstallSkills(bg, envp, "gone", "/x", []string{"a"}); err == nil || !strings.Contains(err.Error(), "install skills into /x") {
		t.Errorf("err = %v", err)
	}
	id, _ := envp.Provision(bg, envport.EnvSpec{RunID: "r"})
	envp.ExecFunc = func(*fake.Env, envport.Command) (envport.ExecResult, error) {
		return envport.ExecResult{ExitCode: 1, Output: strings.Repeat("x", 600) + "tail"}, nil
	}
	err := InstallSkills(bg, envp, id, "/x", []string{"a"})
	if err == nil || !strings.HasSuffix(err.Error(), "tail") || len(err.Error()) > 600 {
		t.Errorf("err (%d) = %v", len(fmt.Sprint(err)), err)
	}
}
