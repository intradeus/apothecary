package acpagent

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/sidecar"
)

// These tests run the REAL sidecar (the shim binary, built from source) with
// a scripted ACP agent behind it, and drive it through the runner exactly as
// the engine does: the whole engine↔workspace path minus docker.

type harness struct {
	sock    string
	fakeACP string
}

var (
	buildOnce sync.Once
	buildDir  string
	buildErr  error
)

func build(t *testing.T) (shim, fake string) {
	t.Helper()
	buildOnce.Do(func() {
		buildDir, buildErr = os.MkdirTemp("", "acpagent-test-*")
		if buildErr != nil {
			return
		}
		for _, b := range [][2]string{
			{"shim", "apothecary/workspace/shim"},
			{"fakeacp", "../../sidecar/testdata/fakeacp"},
		} {
			out, err := exec.Command("go", "build", "-o", filepath.Join(buildDir, b[0]), b[1]).CombinedOutput()
			if err != nil {
				buildErr = errors.New(string(out))
				return
			}
		}
	})
	if buildErr != nil {
		t.Fatalf("build: %v", buildErr)
	}
	return filepath.Join(buildDir, "shim"), filepath.Join(buildDir, "fakeacp")
}

func startSidecar(t *testing.T) *harness {
	t.Helper()
	shim, fake := build(t)
	// Unix socket paths are short-limited; t.TempDir() can exceed it on macOS.
	dir, err := os.MkdirTemp("", "sc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	cmd := exec.Command(shim, "sidecar", "--listen", sock, "--repo", dir)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sidecar.New(unixConn(sock)).Stop(context.Background())
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	for range 500 {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			return &harness{sock: sock, fakeACP: fake}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("sidecar never served")
	return nil
}

type unixConn string

func (u unixConn) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", string(u))
}
func (u unixConn) Close() error { return nil }

// env is a provisioner with nothing but a sidecar.
type env struct {
	envport.EnvProvisioner
	sock string
}

func (e env) Sidecar(context.Context, envport.EnvID) (envport.PortConn, error) {
	return unixConn(e.sock), nil
}

func (h *harness) runner() *Runner {
	r := New(env{sock: h.sock}, Profile{Harness: "fake", Command: h.fakeACP,
		Mode: func(s agentport.AgentSpec) string { return s.Agent }})
	r.cancelGrace = 5 * time.Second
	return r
}

// recorder collects events and serves as the inbox.
type recorder struct {
	mu     sync.Mutex
	events []agentport.AgentEvent
	queue  []agentport.UserMessage
	held   bool
	wake   chan struct{}
	onText func(string)
}

func newRecorder() *recorder { return &recorder{wake: make(chan struct{}, 1)} }

func (r *recorder) on(ev agentport.AgentEvent) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	f := r.onText
	r.mu.Unlock()
	if f != nil && ev.Kind == agentport.AgentEvMessage {
		f(ev.Text)
	}
}

func (r *recorder) push(m agentport.UserMessage) {
	r.mu.Lock()
	r.queue = append(r.queue, m)
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *recorder) Wake() <-chan struct{} { return r.wake }
func (r *recorder) Take() []agentport.UserMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.queue
	r.queue = nil
	return q
}
func (r *recorder) Interrupting() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.queue {
		if m.Interrupt {
			return true
		}
	}
	return false
}
func (r *recorder) Held() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.held
}

// hold flips the take-over flag; letting go wakes the runner.
func (r *recorder) hold(held bool) {
	r.mu.Lock()
	r.held = held
	r.mu.Unlock()
	if !held {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

func (r *recorder) kinds() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ks []string
	for _, e := range r.events {
		ks = append(ks, e.Kind)
	}
	return strings.Join(ks, " ")
}

func (r *recorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, e := range r.events {
		if e.Kind == agentport.AgentEvMessage {
			b.WriteString(e.Text)
			b.WriteString("|")
		}
	}
	return b.String()
}

func TestRunStreamsTheTranscript(t *testing.T) {
	h := startSidecar(t)
	rec := newRecorder()
	res, err := h.runner().Run(context.Background(), "e", agentport.AgentSpec{Agent: "planner", Model: "m/x"},
		agentport.AgentInput{Prompt: "hello", Events: rec.on, Inbox: rec})
	if err != nil {
		t.Fatal(err)
	}
	if want := "session turn_start message tool tool message plan turn_end"; rec.kinds() != want {
		t.Errorf("events = %s\nwant     %s", rec.kinds(), want)
	}
	if rec.text() != "echo: hello| done|" {
		t.Errorf("text = %q", rec.text())
	}
	if !strings.HasPrefix(string(res.Session), "s1-") || res.Usage.CostUSD < 0.0999 || res.Usage.CostUSD > 0.1001 ||
		res.Usage.InputTokens != 7 || res.Usage.OutputTokens != 3 || res.TurnsUsed != 2 {
		t.Errorf("result = %+v", res)
	}
	if res.Output != "echo: hello\n\ndone" {
		t.Errorf("output = %q", res.Output)
	}
	for _, e := range rec.events {
		if e.Cursor == "" && e.Kind != agentport.AgentEvSession {
			t.Errorf("event %s has no cursor", e.Kind)
		}
	}
}

// A message sent while the agent works is delivered into the same session
// when the turn ends — no restart, no new process.
func TestQueuedMessageBecomesTheNextTurn(t *testing.T) {
	h := startSidecar(t)
	rec := newRecorder()
	var once sync.Once
	rec.onText = func(s string) {
		if s == "working slowly" {
			once.Do(func() { rec.push(agentport.UserMessage{ID: "m1", Text: "also add tests"}) })
		}
	}
	res, err := h.runner().Run(context.Background(), "e", agentport.AgentSpec{Agent: "builder"},
		agentport.AgentInput{Prompt: "SLOW", Events: rec.on, Inbox: rec})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.text(), "echo: Message from the human operator:\n\nalso add tests") {
		t.Errorf("the queued message was not the next prompt: %q", rec.text())
	}
	var delivered []agentport.UserMessage
	for _, e := range rec.events {
		if e.Kind == agentport.AgentEvUserMessage {
			delivered = append(delivered, e.Messages...)
		}
	}
	if len(delivered) != 1 || delivered[0].ID != "m1" {
		t.Errorf("delivered = %+v", delivered)
	}
	if res.Usage.CostUSD < 0.1999 || res.Usage.CostUSD > 0.2001 {
		t.Errorf("cost over two turns = %v", res.Usage.CostUSD)
	}
}

// An interrupting message cancels the turn in flight and is read at once.
func TestInterruptingMessage(t *testing.T) {
	h := startSidecar(t)
	rec := newRecorder()
	var once sync.Once
	rec.onText = func(s string) {
		if s == "waiting" {
			once.Do(func() { rec.push(agentport.UserMessage{ID: "m2", Text: "stop, use sqlite", Interrupt: true}) })
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := h.runner().Run(context.Background(), "e", agentport.AgentSpec{}, agentport.AgentInput{Prompt: "WAIT", Events: rec.on, Inbox: rec})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("interrupt never ended the waiting turn")
	}
	if !strings.Contains(rec.text(), "stop, use sqlite") {
		t.Errorf("interrupting message not delivered: %q", rec.text())
	}
	var stops []string
	for _, e := range rec.events {
		if e.Kind == agentport.AgentEvTurnEnd {
			stops = append(stops, e.StopReason)
		}
	}
	if strings.Join(stops, ",") != "cancelled,end_turn" {
		t.Errorf("turn ends = %v", stops)
	}
}

// A take-over: while the inbox is held, a turn ending with nothing queued is
// not the invocation's end — the runner waits for the holder's next message
// and runs it as the next turn in the same session; letting go ends it.
func TestHeldSessionWaitsForTheNextMessage(t *testing.T) {
	h := startSidecar(t)
	rec := newRecorder()
	rec.hold(true)
	done := make(chan error, 1)
	go func() {
		_, err := h.runner().Run(context.Background(), "e", agentport.AgentSpec{}, agentport.AgentInput{Prompt: "hello", Events: rec.on, Inbox: rec})
		done <- err
	}()
	turnEnds := func() int {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		n := 0
		for _, e := range rec.events {
			if e.Kind == agentport.AgentEvTurnEnd {
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(10 * time.Second)
	for turnEnds() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// The turn is over, nothing is queued, and the runner has not returned.
	select {
	case err := <-done:
		t.Fatalf("a held session returned at the end of its turn (err=%v)", err)
	case <-time.After(150 * time.Millisecond):
	}
	rec.push(agentport.UserMessage{ID: "m1", Text: "and again"})
	for turnEnds() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if turnEnds() != 2 {
		t.Fatalf("turn ends = %d, want 2", turnEnds())
	}
	// Letting go with the handback queued: it is the last turn, then the
	// runner returns.
	rec.push(agentport.UserMessage{Text: "FINISH", Verbatim: true})
	rec.hold(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the runner never returned after the hold ended")
	}
	if turnEnds() != 3 {
		t.Errorf("turn ends = %d, want 3", turnEnds())
	}
	if got := rec.text(); !strings.Contains(got, "echo: hello|") || !strings.Contains(got, "and again") || !strings.Contains(got, "echo: FINISH") {
		t.Errorf("text = %q", got)
	}
	// The handback went verbatim, not framed as the operator's words.
	for _, e := range rec.events {
		if e.Kind == agentport.AgentEvUserMessage && len(e.Messages) == 1 && e.Messages[0].Verbatim && e.Text != "FINISH" {
			t.Errorf("verbatim message rendered as %q", e.Text)
		}
	}
	for _, e := range rec.events {
		if e.Kind == agentport.AgentEvTurnEnd && e.Usage == nil {
			t.Errorf("turn_end without its usage: %+v", e)
		}
	}
}

// A profile that promises read-only by process: a read-only session runs in
// its own agent process, started with the read-only command line, and gets
// none of the profile's own MCP servers (it answers, it does not ask).
func TestReadOnlySessionRunsInItsOwnProcessWithoutProfileServers(t *testing.T) {
	h := startSidecar(t)
	r := New(env{sock: h.sock}, Profile{Harness: "fake", Command: h.fakeACP,
		ReadOnlyBy: ReadOnlyByProcess, ReadOnlyCommand: h.fakeACP + " --read-only",
		MCPServers: []agentport.MCPServer{{Name: "ask_human", Command: []string{"/bin/true"}}}})
	r.cancelGrace = 5 * time.Second
	ctx := context.Background()
	normal := newRecorder()
	if _, err := r.Run(ctx, "e", agentport.AgentSpec{}, agentport.AgentInput{Prompt: "MCP", Events: normal.on, Inbox: normal}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(normal.text(), "ask_human") {
		t.Errorf("a normal session has the profile's servers: %q", normal.text())
	}
	ro := newRecorder()
	res, err := r.Run(ctx, "e", agentport.AgentSpec{}, agentport.AgentInput{Prompt: "MCP", Events: ro.on, Inbox: ro, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ro.text(), "ask_human") {
		t.Errorf("a read-only session got the profile's servers: %q", ro.text())
	}
	c := sidecar.New(unixConn(h.sock))
	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Procs) != 2 || len(st.Sessions) != 2 {
		t.Errorf("status = %+v, want two processes and two sessions", st)
	}
	// Continuing the read-only session stays read-only and server-less.
	again := newRecorder()
	if _, err := r.Resume(ctx, "e", res.Session, agentport.AgentInput{Prompt: "MCP", Events: again.on, Inbox: again, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(again.text(), "ask_human") {
		t.Errorf("a resumed read-only session got the profile's servers: %q", again.text())
	}
	if st, _ := c.Status(ctx); len(st.Procs) != 2 {
		t.Errorf("resume started another process: %+v", st)
	}
}

// The engine cancelling (timeout, park, abort) winds the turn down and still
// reports the session and spend.
func TestContextCancelEndsTheTurn(t *testing.T) {
	h := startSidecar(t)
	ctx, cancel := context.WithCancel(context.Background())
	rec := newRecorder()
	rec.onText = func(s string) {
		if s == "waiting" {
			cancel()
		}
	}
	start := time.Now()
	res, err := h.runner().Run(ctx, "e", agentport.AgentSpec{}, agentport.AgentInput{Prompt: "WAIT", Events: rec.on})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.Session == "" || res.Usage.CostUSD < 0.0999 {
		t.Errorf("result after cancel = %+v", res)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("cancel took the whole grace period")
	}
}

func TestPromptErrorFailsTheInvocation(t *testing.T) {
	h := startSidecar(t)
	_, err := h.runner().Run(context.Background(), "e", agentport.AgentSpec{}, agentport.AgentInput{Prompt: "FAIL"})
	if err == nil || !strings.Contains(err.Error(), "model exploded") {
		t.Fatalf("err = %v", err)
	}
}

// After an engine restart the turn is still running in the sidecar: Reattach
// follows it to its end instead of starting the agent over.
func TestReattachFollowsTheTurnInFlight(t *testing.T) {
	h := startSidecar(t)
	r := h.runner()
	c := sidecar.New(unixConn(h.sock))
	ctx := context.Background()
	if _, err := r.start(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	sess, err := c.Session(ctx, sidecar.SessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// What the previous engine process started, and the last event it saw.
	if err := c.Prompt(ctx, sidecar.PromptRequest{SessionID: sess.SessionID, Turn: "old-turn", Text: "SLOW"}); err != nil {
		t.Fatal(err)
	}
	st, _ := c.Status(ctx)
	cursor := sidecar.Cursor(st.Boot, 0)

	rec := newRecorder()
	res, ok, err := r.Reattach(ctx, "e", agentport.SessionID(sess.SessionID), cursor, agentport.AgentInput{Events: rec.on})
	if err != nil || !ok {
		t.Fatalf("reattach = %v %v", ok, err)
	}
	if !strings.Contains(rec.text(), "working slowly") || !strings.Contains(rec.kinds(), "turn_end") {
		t.Errorf("reattached stream = %s / %q", rec.kinds(), rec.text())
	}
	if res.Session != agentport.SessionID(sess.SessionID) {
		t.Errorf("session = %q", res.Session)
	}
	// Nothing in flight, and nothing after the last event recorded: the
	// caller resumes normally.
	st, _ = c.Status(ctx)
	end := sidecar.Cursor(st.Boot, st.LastSeq)
	if r.Reattachable(ctx, "e", agentport.SessionID(sess.SessionID), end) {
		t.Error("reattachable with nothing new")
	}
	if _, ok, _ := r.Reattach(ctx, "e", agentport.SessionID(sess.SessionID), end, agentport.AgentInput{}); ok {
		t.Error("reattached with nothing to follow")
	}
}

// A turn that FINISHED while the engine was down is still followed: its
// events are in the sidecar's buffer, and the attempt continues from its
// result instead of re-prompting the agent.
func TestReattachReplaysATurnThatFinishedMeanwhile(t *testing.T) {
	h := startSidecar(t)
	r := h.runner()
	c := sidecar.New(unixConn(h.sock))
	ctx := context.Background()
	if _, err := r.start(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	sess, _ := c.Session(ctx, sidecar.SessionRequest{})
	st, _ := c.Status(ctx)
	before := sidecar.Cursor(st.Boot, st.LastSeq)
	if err := c.Prompt(ctx, sidecar.PromptRequest{SessionID: sess.SessionID, Turn: "done-turn", Text: "finished while away"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		st, _ = c.Status(ctx)
		if st.Turn(sess.SessionID) == nil {
			break
		}
		if i > 200 {
			t.Fatal("turn never ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !r.Reattachable(ctx, "e", agentport.SessionID(sess.SessionID), before) {
		t.Fatal("a turn that finished after the cursor is reattachable")
	}
	rec := newRecorder()
	res, ok, err := r.Reattach(ctx, "e", agentport.SessionID(sess.SessionID), before, agentport.AgentInput{Events: rec.on})
	if err != nil || !ok || !strings.Contains(res.Output, "echo: finished while away") {
		t.Fatalf("reattach = %+v %v %v", res, ok, err)
	}
}

func TestResumeContinuesTheSession(t *testing.T) {
	h := startSidecar(t)
	r := h.runner()
	first, err := r.Run(context.Background(), "e", agentport.AgentSpec{}, agentport.AgentInput{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	rec := newRecorder()
	again, err := r.Resume(context.Background(), "e", first.Session, agentport.AgentInput{Prompt: "two", Events: rec.on})
	if err != nil {
		t.Fatal(err)
	}
	if again.Session != first.Session || !strings.Contains(rec.text(), "echo: two") {
		t.Errorf("resume = %+v / %q", again, rec.text())
	}
	// Each invocation reports only its own spend.
	if again.Usage.CostUSD < 0.0999 || again.Usage.CostUSD > 0.1001 {
		t.Errorf("resume cost = %v", again.Usage.CostUSD)
	}
}

// A stage's MCP servers ride the session it opens, in ACP's shape: stdio
// untagged with every field, http tagged — and a resumed session that has to
// be reloaded gets them again.
func TestMCPServersRideTheSession(t *testing.T) {
	h := startSidecar(t)
	rec := newRecorder()
	servers := []agentport.MCPServer{
		{Name: "browser", Command: []string{"/opt/apothecary/shim", "browser-mcp"}},
		{Name: "figma", URL: "https://mcp.figma.test/mcp", Headers: map[string]string{"X-B": "2", "Authorization": "Bearer t"}},
		{Name: "db", Command: []string{"dbmcp"}, Env: map[string]string{"DSN": "pg://x"}},
	}
	res, err := h.runner().Run(context.Background(), "e", agentport.AgentSpec{Agent: "planner"},
		agentport.AgentInput{Prompt: "MCP", Events: rec.on, Inbox: rec, MCPServers: servers})
	if err != nil {
		t.Fatal(err)
	}
	want := `mcp: [{"name":"browser","command":"/opt/apothecary/shim","args":["browser-mcp"],"env":[]},` +
		`{"type":"http","name":"figma","url":"https://mcp.figma.test/mcp","headers":[{"name":"Authorization","value":"Bearer t"},{"name":"X-B","value":"2"}]},` +
		`{"name":"db","command":"dbmcp","args":[],"env":[{"name":"DSN","value":"pg://x"}]}]|`
	if rec.text() != want {
		t.Errorf("session saw\n%s\nwant\n%s", rec.text(), want)
	}
	// Resume: the session is live, so nothing is reloaded — and a session the
	// process lost is loaded with the servers again (fakeacp keeps no state
	// across processes, so restarting the agent stands in for that).
	if err := sidecar.New(unixConn(h.sock)).Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec2 := newRecorder()
	if _, err := h.runner().Resume(context.Background(), "e", res.Session,
		agentport.AgentInput{Prompt: "MCP", Events: rec2.on, Inbox: rec2, MCPServers: servers[:1]}); err != nil {
		t.Fatal(err)
	}
	if want := `mcp: [{"name":"browser","command":"/opt/apothecary/shim","args":["browser-mcp"],"env":[]}]|`; rec2.text() != want {
		t.Errorf("reloaded session saw %s, want %s", rec2.text(), want)
	}
}

// A harness's own MCP servers (omp reads no MCP config file in ACP mode) ride
// every session ahead of the stage's, reloads included; a stage server of
// the same name is refused rather than shadowed.
func TestProfileMCPServersRideEverySession(t *testing.T) {
	h := startSidecar(t)
	r := h.runner()
	r.p.MCPServers = []agentport.MCPServer{{Name: "ask_human", Command: []string{"/opt/apothecary/shim", "ask-human"}}}
	rec := newRecorder()
	res, err := r.Run(context.Background(), "e", agentport.AgentSpec{Agent: "planner"},
		agentport.AgentInput{Prompt: "MCP", Events: rec.on, Inbox: rec,
			MCPServers: []agentport.MCPServer{{Name: "db", Command: []string{"dbmcp"}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `mcp: [{"name":"ask_human","command":"/opt/apothecary/shim","args":["ask-human"],"env":[]},` +
		`{"name":"db","command":"dbmcp","args":[],"env":[]}]|`
	if rec.text() != want {
		t.Errorf("session saw\n%s\nwant\n%s", rec.text(), want)
	}
	if err := sidecar.New(unixConn(h.sock)).Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec2 := newRecorder()
	if _, err := r.Resume(context.Background(), "e", res.Session,
		agentport.AgentInput{Prompt: "MCP", Events: rec2.on, Inbox: rec2}); err != nil {
		t.Fatal(err)
	}
	if want := `mcp: [{"name":"ask_human","command":"/opt/apothecary/shim","args":["ask-human"],"env":[]}]|`; rec2.text() != want {
		t.Errorf("reloaded session saw %s, want %s", rec2.text(), want)
	}
	_, err = r.Run(context.Background(), "e", agentport.AgentSpec{},
		agentport.AgentInput{Prompt: "MCP", Events: rec.on, Inbox: rec,
			MCPServers: []agentport.MCPServer{{Name: "ask_human", Command: []string{"other"}}}})
	if err == nil || !strings.Contains(err.Error(), `"ask_human" is the harness's own`) {
		t.Errorf("err = %v, want the shadowing server refused", err)
	}
}

// A remote server on a harness that takes none is refused before any session
// is opened, naming the server.
func TestRemoteMCPServerRefusedWhereTheAgentTakesNone(t *testing.T) {
	h := startSidecar(t)
	r := New(env{sock: h.sock}, Profile{Harness: "fake", Command: "env FAKEACP_NO_HTTP_MCP=1 " + h.fakeACP})
	rec := newRecorder()
	_, err := r.Run(context.Background(), "e", agentport.AgentSpec{},
		agentport.AgentInput{Prompt: "MCP", Events: rec.on, Inbox: rec,
			MCPServers: []agentport.MCPServer{{Name: "figma", URL: "https://mcp.figma.test/mcp"}}})
	if err == nil || !strings.Contains(err.Error(), `mcp server "figma" is remote`) {
		t.Fatalf("err = %v, want the remote server refused", err)
	}
	if rec.kinds() != "" {
		t.Errorf("a session was opened anyway: %s", rec.kinds())
	}
}
