package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/ports/assistantport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
	"apothecary/internal/skills"
)

// fakeAgentHost is the shim sidecar's agent API in memory: start, session,
// prompt, stop, and an event stream. A session already live in its process
// is answered as it is, not loaded, and keeps the MCP servers it was opened
// with, as the shim's; stopping the process forgets its sessions. A prompt
// answers "answered: <its text's first line>" after onPrompt runs (a test
// fills from there, as the agent would through the MCP face).
type fakeAgentHost struct {
	srv *httptest.Server

	mu       sync.Mutex
	starts   []map[string]any
	sessions []map[string]any
	// live are the sessions open in each process, by proc then session
	// id: the MCP servers each was opened with.
	live     map[string]map[string]any
	stops    []string
	prompts  []string
	events   []map[string]any
	subs     []chan map[string]any
	onPrompt func(text string)
	loadFail bool
	said     string
}

func newFakeAgentHost(t *testing.T) *fakeAgentHost {
	f := &fakeAgentHost{said: "I filled it with sk-secret-key."}
	mux := http.NewServeMux()
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(assistantport.AgentHostTokenHeader) != "t0k" {
				http.Error(w, "no token", http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("POST /agent/start", guard(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.starts = append(f.starts, req)
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"boot": "b1", "proc": "p1"})
	}))
	mux.HandleFunc("POST /agent/session", guard(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		proc, _ := req["proc"].(string)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.sessions = append(f.sessions, req)
		if f.live == nil {
			f.live = map[string]map[string]any{}
		}
		if f.live[proc] == nil {
			f.live[proc] = map[string]any{}
		}
		if id, _ := req["session_id"].(string); id != "" {
			if _, ok := f.live[proc][id]; ok {
				json.NewEncoder(w).Encode(map[string]any{"session_id": id, "loaded": false})
				return
			}
			if f.loadFail {
				http.Error(w, "no such session", http.StatusBadGateway)
				return
			}
			f.live[proc][id] = req["mcp_servers"]
			json.NewEncoder(w).Encode(map[string]any{"session_id": id, "loaded": true})
			return
		}
		f.live[proc]["h-1"] = req["mcp_servers"]
		json.NewEncoder(w).Encode(map[string]any{"session_id": "h-1", "model": "acme/one"})
	}))
	mux.HandleFunc("POST /agent/stop", guard(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Proc string `json:"proc"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		if req.Proc == "" {
			http.Error(w, "the test stops one process", http.StatusBadRequest)
			return
		}
		f.stops = append(f.stops, req.Proc)
		delete(f.live, req.Proc)
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /agent/prompt", guard(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SessionID string `json:"session_id"`
			Turn      string `json:"turn"`
			Text      string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.prompts = append(f.prompts, req.Text)
		hook, said := f.onPrompt, f.said
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		go func() {
			if hook != nil {
				hook(req.Text)
			}
			f.emit(map[string]any{"type": "tool_call", "turn": req.Turn, "tool": map[string]any{"title": "Read README", "status": "pending"}})
			f.emit(map[string]any{"type": "message", "turn": req.Turn, "text": said})
			cost := 0.5
			f.emit(map[string]any{"type": "turn_end", "turn": req.Turn, "cost_usd": cost})
		}()
	}))
	mux.HandleFunc("POST /agent/cancel", guard(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	mux.HandleFunc("GET /agent/events", guard(func(w http.ResponseWriter, r *http.Request) {
		ch := make(chan map[string]any, 64)
		f.mu.Lock()
		f.subs = append(f.subs, ch)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		fl.Flush()
		enc := json.NewEncoder(w)
		for {
			select {
			case ev := <-ch:
				enc.Encode(ev)
				fl.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}))
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAgentHost) emit(ev map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	for _, ch := range f.subs {
		ch <- ev
	}
}

type tcpDialer string

func (d tcpDialer) Dial(ctx context.Context) (net.Conn, error) {
	var nd net.Dialer
	return nd.DialContext(ctx, "tcp", string(d))
}

// fakePods is a person's pod: files in memory, an agent host, fetches.
type fakePods struct {
	host    *fakeAgentHost
	mu      sync.Mutex
	files   map[string]string
	ensured [][]string
	fetches int
	state   assistantport.Status
	ensureE error
	// hold, when set, keeps Ensure waiting until it closes.
	hold chan struct{}
	// writeFail refuses a write to a path holding it.
	writeFail string
}

func (p *fakePods) Ensure(_ context.Context, _ string, hosts []string) error {
	p.mu.Lock()
	p.ensured = append(p.ensured, hosts)
	hold := p.hold
	p.mu.Unlock()
	if hold != nil {
		<-hold
	}
	return p.ensureE
}
func (p *fakePods) Status(context.Context, string) (assistantport.Status, error) { return p.state, nil }
func (p *fakePods) AgentHost(context.Context, string) (assistantport.AgentHost, error) {
	return assistantport.AgentHost{Dialer: tcpDialer(strings.TrimPrefix(p.host.srv.URL, "http://")), Token: "t0k"}, nil
}
func (p *fakePods) StartRequest(_ string, s assistantport.Session) (assistantport.Start, error) {
	var read []string
	for _, r := range s.Repos {
		read = append(read, p.Folder(r))
	}
	return assistantport.Start{Argv: s.Argv, Env: s.Env, Cwd: "/workshops/w", Confine: &assistantport.Rules{Read: read, Write: []string{p.Home(s.ID)}}}, nil
}
func (p *fakePods) Home(id string) string { return "/state/" + id }
func (p *fakePods) Folder(r assistantport.Repo) string {
	return "/workshops/" + r.Workshop + "/" + r.Name
}
func (p *fakePods) Fetch(_ context.Context, _ string, items []assistantport.FetchItem) ([]assistantport.RepoState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fetches++
	var out []assistantport.RepoState
	for _, it := range items {
		out = append(out, assistantport.RepoState{Workshop: it.Repo.Workshop, Name: it.Repo.Name, OK: it.Repo.Name != "broken", Head: "abc", At: time.Now()})
	}
	return out, nil
}
func (p *fakePods) WriteFile(_ context.Context, _, path string, data []byte, _ uint32) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.writeFail != "" && strings.Contains(path, p.writeFail) {
		return errors.New("disk full")
	}
	p.files[path] = string(data)
	return nil
}
func (p *fakePods) ReadFile(_ context.Context, _, path string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.files[path]
	if !ok {
		return nil, errors.New("no file")
	}
	return []byte(c), nil
}

type fakeHarnesses struct{ kind string }

func (h fakeHarnesses) Integrations(context.Context, string) ([]Integration, error) {
	return []Integration{{ID: "int-1", Name: "mine", Kind: h.kind}}, nil
}
func (h fakeHarnesses) Open(_ context.Context, _, id string, env envport.EnvProvisioner) (Harness, error) {
	if id != "int-1" {
		return nil, errors.New("not yours")
	}
	return &fakeHarness{kind: h.kind, env: env}, nil
}

type fakeHarness struct {
	kind string
	env  envport.EnvProvisioner
}

func (h *fakeHarness) Kind() string { return h.kind }
func (h *fakeHarness) Models(context.Context) ([]string, error) {
	return []string{"acme/one", "acme/two"}, nil
}
func (h *fakeHarness) EnvVars() map[string]string {
	return map[string]string{"ANTHROPIC_API_KEY": "sk-secret-key"}
}
func (h *fakeHarness) EgressDomains() []string { return []string{"api.anthropic.com"} }
func (h *fakeHarness) SecretValues() []string  { return []string{"login-secret-xyz"} }
func (h *fakeHarness) SeedAuth(ctx context.Context, env envport.EnvID, register func(string)) error {
	register("login-secret-xyz")
	return h.env.WriteFile(ctx, env, ".claude/.credentials.json", []byte(`{"token":"login-secret-xyz"}`), 0o600)
}

type fakeWorkshops struct{}

func (fakeWorkshops) Repos(_ context.Context, person, ws string) ([]assistantport.FetchItem, error) {
	if ws == "w-denied" || person == "stranger" {
		return nil, errors.New("workshop not found")
	}
	return []assistantport.FetchItem{
		{Repo: assistantport.Repo{Workshop: ws, WorkshopName: "acme", Name: "api", URL: "https://github.com/acme/api.git"}, Cred: assistantport.Credential{Pass: "repo-pass"}},
		{Repo: assistantport.Repo{Workshop: ws, WorkshopName: "acme", Name: "broken", URL: "https://github.com/acme/broken.git"}},
	}, nil
}

type fakeTokens struct {
	mu      sync.Mutex
	n       int
	minted  []string
	revoked []string
	grants  [][]string
	scopes  [][]string
}

func (f *fakeTokens) Mint(_ context.Context, _, _ string, actions, ws []string, _ time.Duration) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	id := fmt.Sprintf("tok-%d", f.n)
	f.minted = append(f.minted, id)
	f.grants = append(f.grants, actions)
	f.scopes = append(f.scopes, ws)
	return id, fmt.Sprintf("apo_secret-%d", f.n), nil
}
func (f *fakeTokens) Revoke(_ context.Context, _, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, id)
	return nil
}

type fakeChecks struct{}

func (fakeChecks) TicketTypes(context.Context, string, string) ([]string, error) {
	return []string{"bug", "feature"}, nil
}

func (fakeChecks) DashboardPanels(_ context.Context, _ string, panels json.RawMessage) error {
	if strings.Contains(string(panels), "nope") {
		return errors.New("measure nope is not in the catalog")
	}
	return nil
}

type world struct {
	svc    *Service
	store  *MemStore
	pods   *fakePods
	host   *fakeAgentHost
	tokens *fakeTokens
}

func newWorld(t *testing.T, kind string) *world {
	host := newFakeAgentHost(t)
	w := &world{store: NewMemStore(), host: host, tokens: &fakeTokens{},
		pods: &fakePods{host: host, files: map[string]string{}, state: assistantport.Status{State: assistantport.PodRunning}}}
	w.svc = &Service{Store: w.store, Pods: w.pods, Harnesses: fakeHarnesses{kind: kind}, Workshops: fakeWorkshops{},
		Tokens: w.tokens, Checks: fakeChecks{}, FaceActions: []string{"project:read", "ticket:read"},
		FaceURL: func(context.Context) (string, error) { return "http://engine:8080/mcp", nil }}
	if err := w.store.SaveAssistantChoice(context.Background(), Choice{UserID: "u1", Integration: "int-1", Model: "acme/two"}); err != nil {
		t.Fatal(err)
	}
	return w
}

// frames collects a person's stream until a frame matches until.
func frames(t *testing.T, svc *Service, person string, until func(Frame) bool) []Frame {
	t.Helper()
	backlog, _, live, stop := svc.Watch(person, 0)
	defer stop()
	out := backlog
	for _, f := range backlog {
		if until(f) {
			return out
		}
	}
	timeout := time.After(10 * time.Second)
	for {
		select {
		case f := <-live:
			out = append(out, f)
			if until(f) {
				return out
			}
		case <-timeout:
			t.Fatalf("no frame came; saw %+v", out)
		}
	}
}

func idle(f Frame) bool { return f.Kind == "state" && !f.Busy }

// answered waits for a session to hold n messages, its last an answer.
func answered(t *testing.T, svc *Service, id string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		msgs, _ := svc.Store.AssistantMessages(context.Background(), id, 0)
		svc.mu.Lock()
		busy := svc.turns[id] != nil
		svc.mu.Unlock()
		if len(msgs) >= n && !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session %s never reached %d messages", id, n)
}

// A turn end to end: the pod, the repositories, the harness confined with
// its files and logins, the face by URL with the session's token, the git
// tool, the prompt with the page; the fill reaches the page once; the
// answer is recorded with its fill and no secret.
func TestATurn(t *testing.T) {
	w := newWorld(t, "claude-code")
	ctx := context.Background()
	s, msgs, err := w.svc.Open(ctx, "u1", KindTicket, "w1", "draft-1", false)
	if err != nil || len(msgs) != 0 || s.Integration != "int-1" || s.Model != "acme/two" {
		t.Fatalf("open = %+v, %v, %v", s, msgs, err)
	}
	var fillErrs []string
	w.host.onPrompt = func(string) {
		live, _, _ := w.svc.Store.AssistantSession(ctx, s.ID)
		// A fill the page refuses comes back to the agent, and reaches no page.
		errs, err := w.svc.Fill(ctx, live.TokenID, "u1", s.ID, json.RawMessage(`{"change_type":"rewrite"}`))
		if err != nil || len(errs) == 0 {
			t.Errorf("a bad fill = %v, %v", errs, err)
		}
		fillErrs = errs
		if _, err := w.svc.Fill(ctx, "tok-other", "u1", s.ID, json.RawMessage(`{"title":"x"}`)); !errors.Is(err, ErrNotYours) {
			t.Errorf("another token's fill = %v", err)
		}
		// A fill holding the turn's secrets reaches no page, however it came
		// by them (a prompt injection asking for the harness's key).
		for _, secret := range []string{"sk-secret-key", "login-secret-xyz"} {
			errs, err := w.svc.Fill(ctx, live.TokenID, "u1", s.ID, json.RawMessage(`{"title":"x","goal":"key: `+secret+`"}`))
			if err != nil || len(errs) == 0 || !strings.Contains(errs[0], "credential") {
				t.Errorf("a fill with %s = %v, %v", secret, errs, err)
			}
		}
		if errs, err := w.svc.Fill(ctx, live.TokenID, "u1", s.ID, json.RawMessage(`{"title":"Fix the login page","type_id":"bug"}`)); err != nil || len(errs) > 0 {
			t.Errorf("a good fill = %v, %v", errs, err)
		}
		// The person picks a model while it answers: the turn's end keeps it.
		if _, err := w.svc.SetModel(ctx, "u1", s.ID, "acme/one"); err != nil {
			t.Errorf("model mid-turn = %v", err)
		}
	}
	q, err := w.svc.Send(ctx, "u1", s.ID, "the login page fails on Safari", json.RawMessage(`{"title":"","goal":"notes"}`))
	if err != nil || q.Seq != 1 || q.Role != RolePerson {
		t.Fatalf("send = %+v, %v", q, err)
	}
	if _, err := w.svc.Send(ctx, "u1", s.ID, "again", nil); !errors.Is(err, ErrBusy) {
		t.Errorf("a second question mid-turn = %v", err)
	}
	got := frames(t, w.svc, "u1", idle)
	var fills, deltas int
	for _, f := range got {
		switch f.Kind {
		case "fill":
			fills++
			if !strings.Contains(string(f.Fill), "Fix the login page") || f.Page != KindTicket || f.Subject != "draft-1" {
				t.Errorf("fill frame = %+v", f)
			}
		case "delta":
			deltas++
			if strings.Contains(f.Text, "sk-secret-key") {
				t.Error("a delta shows the API key")
			}
		}
	}
	if fills != 1 {
		t.Errorf("%d fill frames, want 1 (the refused one reaches no page): %+v", fills, got)
	}
	if len(fillErrs) == 0 || !strings.Contains(fillErrs[0], "change_type") {
		t.Errorf("refused fill errors = %v", fillErrs)
	}
	all, _ := w.svc.Messages(ctx, "u1", s.ID, 0)
	if len(all) != 2 || all[1].Role != RoleAssistant || all[1].CostUSD != 0.5 || !strings.Contains(string(all[1].Fill), "Fix the login page") {
		t.Fatalf("messages = %+v", all)
	}
	if strings.Contains(all[1].Text, "sk-secret-key") || !strings.Contains(all[1].Text, "***") {
		t.Errorf("the answer = %q, want the key scrubbed", all[1].Text)
	}
	// What the harness was started with.
	start := w.host.starts[0]
	if start["confine"] == nil || !strings.HasSuffix(start["argv"].([]any)[0].(string), "libexec/claude-agent-acp") {
		t.Errorf("start = %v", start)
	}
	env := start["env"].(map[string]any)
	if env["ANTHROPIC_API_KEY"] != "sk-secret-key" || env["CLAUDE_CODE_EXECUTABLE"] == nil {
		t.Errorf("env = %v", env)
	}
	read := fmt.Sprint(start["confine"].(map[string]any)["read"])
	if !strings.Contains(read, "/workshops/w1/api") || strings.Contains(read, "broken") {
		t.Errorf("rules read %s: the fetched repositories only", read)
	}
	sess := w.host.sessions[0]
	if sess["mode"] != "bypassPermissions" || sess["model"] != "acme/two" {
		t.Errorf("session = %v", sess)
	}
	servers := fmt.Sprint(sess["mcp_servers"])
	if !strings.Contains(servers, "http://engine:8080/mcp") || !strings.Contains(servers, "Bearer apo_secret-1") || !strings.Contains(servers, "git-mcp") {
		t.Errorf("servers = %s", servers)
	}
	// The session's files: its settings, its logins.
	if !strings.Contains(w.pods.files["/state/"+s.ID+"/.claude/settings.json"], `"Bash"`) ||
		w.pods.files["/state/"+s.ID+"/.claude/.credentials.json"] == "" {
		t.Errorf("files = %v", w.pods.files)
	}
	// The prompt: the instructions (first turn), the repositories, the page.
	p := w.host.prompts[0]
	if !strings.Contains(p, "You are the Apothecary assistant") || !strings.Contains(p, "/workshops/w1/api [api]") ||
		!strings.Contains(p, `"goal": "notes"`) || !strings.HasSuffix(p, "the login page fails on Safari") {
		t.Errorf("first prompt = %q", p)
	}
	// The token: the face's reads in its workshop, and its fill.
	if !slices.Equal(w.tokens.grants[0], []string{"project:read", "ticket:read", FillAction}) || !slices.Equal(w.tokens.scopes[0], []string{"w1"}) {
		t.Errorf("token = %v in %v", w.tokens.grants[0], w.tokens.scopes[0])
	}
	if hosts := w.pods.ensured[0]; !slices.Equal(hosts, []string{"api.anthropic.com"}) {
		t.Errorf("ensure hosts = %v", hosts)
	}

	if after, _, _ := w.svc.Store.AssistantSession(ctx, s.ID); after.Model != "acme/one" || after.HarnessSession != "h-1" {
		t.Errorf("after the turn = %+v: the mid-turn changes are lost", after)
	}
	// Between turns the token stays with the harness session, whose process
	// holds it: still bound, but the page takes no fill until a question.
	held := func() string {
		w.host.mu.Lock()
		defer w.host.mu.Unlock()
		return fmt.Sprint(w.host.live["p1"]["h-1"])
	}
	w.svc.mu.Lock()
	liveSecret := w.svc.tokens[s.ID].secret
	w.svc.mu.Unlock()
	if h := held(); liveSecret != "apo_secret-1" || !strings.Contains(h, "Bearer "+liveSecret) {
		t.Errorf("the harness session holds %s, the live token is %s", h, liveSecret)
	}
	if sid, _, _, ok := w.svc.FillTool(ctx, "tok-1"); !ok || sid != s.ID {
		t.Errorf("the fill tool between turns = %s %v", sid, ok)
	}
	if errs, err := w.svc.Fill(ctx, "tok-1", "u1", s.ID, json.RawMessage(`{"title":"late"}`)); err != nil || len(errs) == 0 || !strings.Contains(errs[0], "no question is being answered") {
		t.Errorf("a fill between turns = %v, %v", errs, err)
	}
	// A second turn: no instructions again, the live session as it is, the
	// same token, the fetch reused.
	w.host.onPrompt = nil
	if _, err := w.svc.Send(ctx, "u1", s.ID, "thanks", nil); err != nil {
		t.Fatal(err)
	}
	answered(t, w.svc, s.ID, 4)
	if p := w.host.prompts[1]; strings.Contains(p, "You are the Apothecary assistant") {
		t.Errorf("the second prompt repeats the instructions: %q", p)
	}
	if w.host.sessions[1]["session_id"] != "h-1" || w.pods.fetches != 1 {
		t.Errorf("second turn: session %v, fetches %d", w.host.sessions[1], w.pods.fetches)
	}
	w.tokens.mu.Lock()
	minted, revoked := slices.Clone(w.tokens.minted), slices.Clone(w.tokens.revoked)
	w.tokens.mu.Unlock()
	if len(minted) != 1 || len(revoked) != 0 || len(w.host.stops) != 0 || !strings.Contains(held(), "Bearer apo_secret-1") {
		t.Errorf("second turn: minted %v, revoked %v, stops %v, held %s", minted, revoked, w.host.stops, held())
	}
	// The session the harness lost (the pod restarted): a new one,
	// instructions again.
	w.host.mu.Lock()
	w.host.live, w.host.loadFail = nil, true
	w.host.mu.Unlock()
	if _, err := w.svc.Send(ctx, "u1", s.ID, "once more", nil); err != nil {
		t.Fatal(err)
	}
	answered(t, w.svc, s.ID, 6)
	if p := w.host.prompts[2]; !strings.Contains(p, "You are the Apothecary assistant") {
		t.Errorf("a new harness session lacks the instructions: %q", p)
	}
}

// A token replaced while its harness session is live (half its life gone,
// or the engine restarted and holds no secret): the session's process
// starts again and loads the session with the new token, its context kept.
func TestTokenRenewedRestartsTheLiveHarness(t *testing.T) {
	w := newWorld(t, "claude-code")
	ctx := context.Background()
	s, _, err := w.svc.Open(ctx, "u1", KindTicket, "w1", "draft-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Send(ctx, "u1", s.ID, "hello", nil); err != nil {
		t.Fatal(err)
	}
	answered(t, w.svc, s.ID, 2)
	for i, renew := range []struct {
		name string
		age  func()
	}{
		{"half its life gone", func() {
			lt := w.svc.tokens[s.ID]
			lt.until = time.Now().Add(time.Minute)
			w.svc.tokens[s.ID] = lt
		}},
		{"an engine restart", func() { delete(w.svc.tokens, s.ID) }},
	} {
		n := i + 2 // the token this turn mints
		w.svc.mu.Lock()
		renew.age()
		w.svc.mu.Unlock()
		if _, err := w.svc.Send(ctx, "u1", s.ID, "and then", nil); err != nil {
			t.Fatal(err)
		}
		answered(t, w.svc, s.ID, 2*n)
		w.host.mu.Lock()
		stops, sessions, prompt := slices.Clone(w.host.stops), slices.Clone(w.host.sessions), w.host.prompts[n-1]
		held := fmt.Sprint(w.host.live["p1"]["h-1"])
		w.host.mu.Unlock()
		w.tokens.mu.Lock()
		revoked := slices.Clone(w.tokens.revoked)
		w.tokens.mu.Unlock()
		bearer := fmt.Sprintf("Bearer apo_secret-%d", n)
		if len(stops) != n-1 || stops[n-2] != "p1" {
			t.Errorf("%s: stops = %v, want the session's process once more", renew.name, stops)
		}
		reload := sessions[len(sessions)-1]
		if reload["session_id"] != "h-1" || !strings.Contains(fmt.Sprint(reload["mcp_servers"]), bearer) || !strings.Contains(held, bearer) {
			t.Errorf("%s: the session opened again = %v, held %s, want %s", renew.name, reload, held, bearer)
		}
		if want := fmt.Sprintf("tok-%d", n-1); !slices.Equal(revoked[n-2:], []string{want}) {
			t.Errorf("%s: revoked %v, want %s", renew.name, revoked, want)
		}
		if strings.Contains(prompt, "You are the Apothecary assistant") {
			t.Errorf("%s: the context is lost, the instructions given again: %q", renew.name, prompt)
		}
	}
}

// A turn that fails after minting the replacement (here the face's URL)
// leaves the live harness session holding the revoked token: the next
// question still restarts its process and loads it with the replacement
// before prompting it, and the one after leaves it be.
func TestATokenNotInstalledIsInstalledNextTurn(t *testing.T) {
	w := newWorld(t, "claude-code")
	ctx := context.Background()
	s, _, err := w.svc.Open(ctx, "u1", KindTicket, "w1", "draft-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Send(ctx, "u1", s.ID, "hello", nil); err != nil {
		t.Fatal(err)
	}
	answered(t, w.svc, s.ID, 2)
	var failed atomic.Bool
	w.svc.FaceURL = func(context.Context) (string, error) {
		if failed.CompareAndSwap(false, true) {
			return "", errors.New("no face")
		}
		return "http://engine:8080/mcp", nil
	}
	w.svc.mu.Lock()
	lt := w.svc.tokens[s.ID]
	lt.until = time.Now().Add(time.Minute)
	w.svc.tokens[s.ID] = lt
	w.svc.mu.Unlock()
	if _, err := w.svc.Send(ctx, "u1", s.ID, "and then", nil); err != nil {
		t.Fatal(err)
	}
	answered(t, w.svc, s.ID, 4)
	if all, _ := w.svc.Messages(ctx, "u1", s.ID, 0); all[3].Error == "" {
		t.Fatalf("the failed turn = %+v", all[3])
	}
	w.tokens.mu.Lock()
	revoked := slices.Clone(w.tokens.revoked)
	w.tokens.mu.Unlock()
	if !slices.Equal(revoked, []string{"tok-1"}) {
		t.Fatalf("revoked %v, want tok-1 replaced", revoked)
	}
	if _, err := w.svc.Send(ctx, "u1", s.ID, "again", nil); err != nil {
		t.Fatal(err)
	}
	answered(t, w.svc, s.ID, 6)
	w.host.mu.Lock()
	stops, prompts := slices.Clone(w.host.stops), len(w.host.prompts)
	held, prompt := fmt.Sprint(w.host.live["p1"]["h-1"]), w.host.prompts[len(w.host.prompts)-1]
	w.host.mu.Unlock()
	if !slices.Equal(stops, []string{"p1"}) || !strings.Contains(held, "Bearer apo_secret-2") || prompts != 2 {
		t.Errorf("the next question: stops %v, held %s, %d prompts", stops, held, prompts)
	}
	if strings.Contains(prompt, "You are the Apothecary assistant") {
		t.Errorf("the context is lost, the instructions given again: %q", prompt)
	}
	if _, err := w.svc.Send(ctx, "u1", s.ID, "and more", nil); err != nil {
		t.Fatal(err)
	}
	answered(t, w.svc, s.ID, 8)
	w.host.mu.Lock()
	stops = slices.Clone(w.host.stops)
	w.host.mu.Unlock()
	if len(stops) != 1 {
		t.Errorf("an installed token restarts the harness again: stops %v", stops)
	}
}

func TestOpenAndOwnership(t *testing.T) {
	w := newWorld(t, "opencode")
	ctx := context.Background()
	a, _, err := w.svc.Open(ctx, "u1", KindFlow, "w1", "builder", false)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := w.svc.Open(ctx, "u1", KindFlow, "w1", "builder", false)
	if a.ID != b.ID {
		t.Error("the same subject opened two sessions")
	}
	// Fresh: a new session each time, which a plain open then finds.
	c, _, _ := w.svc.Open(ctx, "u1", KindFlow, "w1", "builder", true)
	d, msgs, _ := w.svc.Open(ctx, "u1", KindFlow, "w1", "builder", true)
	if c.ID == a.ID || d.ID == c.ID || len(msgs) != 0 {
		t.Errorf("fresh opens = %s, %s after %s", c.ID, d.ID, a.ID)
	}
	if latest, _, _ := w.svc.Open(ctx, "u1", KindFlow, "w1", "builder", false); latest.ID != d.ID {
		t.Errorf("a plain open after fresh ones = %s, want %s", latest.ID, d.ID)
	}
	// Reopen is the session asked for, not the subject's latest.
	if old, _, err := w.svc.Reopen(ctx, "u1", a.ID); err != nil || old.ID != a.ID {
		t.Errorf("reopen the first session = %s, %v", old.ID, err)
	}
	for name, args := range map[string][3]string{
		"a bad kind":              {"widget", "w1", "x"},
		"no subject":              {"flow", "w1", " "},
		"a flow with no workshop": {"flow", "", "x"},
		"a workshop out of reach": {"ticket", "w-denied", "x"},
	} {
		if _, _, err := w.svc.Open(ctx, "u1", Kind(args[0]), args[1], args[2], false); err == nil {
			t.Errorf("%s: opened", name)
		}
	}
	if _, _, err := w.svc.Open(ctx, "u1", KindSkill, "", "skill-1", false); err != nil {
		t.Errorf("a skill with no workshop: %v", err)
	}
	// Another person reaches none of it.
	for name, err := range map[string]error{
		"messages": func() error { _, err := w.svc.Messages(ctx, "u2", a.ID, 0); return err }(),
		"reopen":   func() error { _, _, err := w.svc.Reopen(ctx, "u2", a.ID); return err }(),
		"send":     func() error { _, err := w.svc.Send(ctx, "u2", a.ID, "hi", nil); return err }(),
		"model":    func() error { _, err := w.svc.SetModel(ctx, "u2", a.ID, "x"); return err }(),
		"cancel":   w.svc.Cancel(ctx, "u2", a.ID),
		"fill":     func() error { _, err := w.svc.Fill(ctx, "tok-1", "u2", a.ID, json.RawMessage(`{}`)); return err }(),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s by another = %v", name, err)
		}
	}
	if s, err := w.svc.SetModel(ctx, "u1", a.ID, " acme/one "); err != nil || s.Model != "acme/one" {
		t.Errorf("set model = %+v, %v", s, err)
	}
	if s, _, err := w.svc.Open(ctx, "u1", KindFlow, "w-denied", "x", true); err == nil {
		t.Errorf("a fresh session out of reach = %+v", s)
	}
	if err := w.svc.Cancel(ctx, "u1", a.ID); err != nil {
		t.Errorf("cancel with nothing running = %v", err)
	}
}

func TestSendNeedsAChoice(t *testing.T) {
	w := newWorld(t, "opencode")
	ctx := context.Background()
	if err := w.store.SaveAssistantChoice(ctx, Choice{UserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	s, _, _ := w.svc.Open(ctx, "u1", KindSkill, "", "skill-1", false)
	if _, err := w.svc.Send(ctx, "u1", s.ID, "hi", nil); !errors.Is(err, ErrNoChoice) {
		t.Errorf("no harness chosen = %v", err)
	}
	if _, err := w.svc.Send(ctx, "u1", s.ID, "  ", nil); err == nil {
		t.Error("an empty question was sent")
	}
	if err := w.svc.SetChoice(ctx, "u1", Choice{Integration: "someone-elses"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("another's integration = %v", err)
	}
	if err := w.svc.SetChoice(ctx, "u1", Choice{Integration: "int-1", Model: "acme/one"}); err != nil {
		t.Fatal(err)
	}
	none := &Service{Store: w.store}
	if _, err := none.Send(ctx, "u1", s.ID, "hi", nil); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("no pods = %v", err)
	}
	if _, err := none.Refresh(ctx, "u1", "w1"); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("no pods refresh = %v", err)
	}
}

// A turn that fails says why, in the answer and on the stream.
func TestATurnThatFails(t *testing.T) {
	w := newWorld(t, "claude-code")
	ctx := context.Background()
	w.pods.ensureE = errors.New("the node cannot confine a session")
	s, _, _ := w.svc.Open(ctx, "u1", KindFlow, "w1", "builder", false)
	if _, err := w.svc.Send(ctx, "u1", s.ID, "hi", nil); err != nil {
		t.Fatal(err)
	}
	frames(t, w.svc, "u1", idle)
	all, _ := w.svc.Messages(ctx, "u1", s.ID, 0)
	if len(all) != 2 || !strings.Contains(all[1].Error, "confine") {
		t.Errorf("messages = %+v", all)
	}
	// A harness the assistant does not run.
	w2 := newWorld(t, "codex")
	s2, _, _ := w2.svc.Open(ctx, "u1", KindFlow, "w1", "builder", false)
	w2.svc.Send(ctx, "u1", s2.ID, "hi", nil)
	frames(t, w2.svc, "u1", idle)
	if all, _ := w2.svc.Messages(ctx, "u1", s2.ID, 0); !strings.Contains(all[1].Error, "does not run on codex") {
		t.Errorf("codex = %+v", all)
	}
}

func TestStatus(t *testing.T) {
	w := newWorld(t, "claude-code")
	ctx := context.Background()
	st, err := w.svc.Status(ctx, "u1", "w1")
	if err != nil || !st.Enabled || st.Choice.Integration != "int-1" || len(st.Models) != 2 || len(st.Integrations) != 1 || len(st.Pages[KindFlow].Presets) == 0 {
		t.Errorf("status = %+v, %v", st, err)
	}
	w.store.SaveAssistantChoice(ctx, Choice{UserID: "u2"})
	if st, _ := w.svc.Status(ctx, "u2", ""); st.Enabled || st.Reason != ErrNoChoice.Error() {
		t.Errorf("no choice = %+v", st)
	}
	w.pods.state = assistantport.Status{State: assistantport.PodRunning, Error: "Landlock is off"}
	if st, _ := w.svc.Status(ctx, "u1", ""); st.Enabled || st.Reason != "Landlock is off" {
		t.Errorf("an unconfined node = %+v", st)
	}
	if st, _ := (&Service{}).Status(ctx, "u1", ""); st.Enabled || st.Reason == "" {
		t.Errorf("no pods = %+v", st)
	}
	views, err := w.svc.Refresh(ctx, "u1", "w1")
	if err != nil || len(views) != 2 || views[1].OK {
		t.Errorf("refresh = %+v, %v", views, err)
	}
	if st, _ := w.svc.Status(ctx, "u1", "w1"); len(st.Repos) != 2 {
		t.Errorf("repos after a refresh = %+v", st.Repos)
	}
}

// The stream resumes from a sequence, and says when frames are gone.
func TestWatchResumes(t *testing.T) {
	svc := &Service{}
	for i := range 3 {
		svc.hub.publish("u1", Frame{Kind: "status", Text: fmt.Sprint(i)})
	}
	svc.hub.publish("u2", Frame{Kind: "status", Text: "other"})
	backlog, gap, _, stop := svc.Watch("u1", 1)
	stop()
	if gap || len(backlog) != 2 || backlog[0].Text != "1" {
		t.Errorf("backlog = %+v, gap %v", backlog, gap)
	}
	for range ringSize + 5 {
		svc.hub.publish("u1", Frame{Kind: "status"})
	}
	if _, gap, _, stop := svc.Watch("u1", 2); !gap {
		t.Error("frames past the ring are not reported gone")
	} else {
		stop()
	}
	// From now: nothing kept is replayed.
	if backlog, gap, _, stop := svc.Watch("u1", -1); gap || len(backlog) != 0 {
		t.Errorf("from now = %d frames, gap %v", len(backlog), gap)
	} else {
		stop()
	}
	// A sequence from before a restart: a gap, and every frame since.
	fresh := &Service{}
	fresh.hub.publish("u1", Frame{Kind: "status", Text: "new"})
	if backlog, gap, _, stop := fresh.Watch("u1", 400); !gap || len(backlog) != 1 || fresh.Head() != 1 {
		t.Errorf("after a restart = %+v, gap %v, head %d", backlog, gap, fresh.Head())
	} else {
		stop()
	}
}

func TestTokenIsRemintedWhenOld(t *testing.T) {
	w := newWorld(t, "claude-code")
	ctx := context.Background()
	s, _, _ := w.svc.Open(ctx, "u1", KindSkill, "", "skill-1", false)
	tok, fresh, err := w.svc.token(ctx, &s)
	if err != nil || !fresh || tok != "apo_secret-1" || !slices.Equal(w.tokens.grants[0], []string{"skill:read", FillAction}) || w.tokens.scopes[0] != nil {
		t.Fatalf("skill token = %s %v %v %v %v", tok, fresh, err, w.tokens.grants, w.tokens.scopes)
	}
	if again, fresh, _ := w.svc.token(ctx, &s); again != tok || !fresh {
		t.Errorf("a live token no harness session took = %s, fresh %v", again, fresh)
	}
	w.svc.installed(s.ID, s.TokenID)
	if again, fresh, _ := w.svc.token(ctx, &s); again != tok || fresh {
		t.Errorf("a live token installed = %s, fresh %v", again, fresh)
	}
	sid, schema, acts, ok := w.svc.FillTool(ctx, s.TokenID)
	if !ok || sid != s.ID || schema["required"] == nil || acts {
		t.Errorf("fill tool = %s %v %v %v", sid, schema, acts, ok)
	}
	if _, _, _, ok := w.svc.FillTool(ctx, "tok-unknown"); ok {
		t.Error("an unknown token has a fill tool")
	}
	w.svc.mu.Lock()
	lt := w.svc.tokens[s.ID]
	lt.until = time.Now().Add(time.Minute)
	w.svc.tokens[s.ID] = lt
	w.svc.mu.Unlock()
	if tok, fresh, _ := w.svc.token(ctx, &s); tok != "apo_secret-2" || !fresh || !slices.Equal(w.tokens.revoked, []string{"tok-1"}) {
		t.Errorf("an old token: %s, fresh %v, revoked %v", tok, fresh, w.tokens.revoked)
	}
}

func TestSessionEnvIsAHomeAndNoShell(t *testing.T) {
	pods := &fakePods{files: map[string]string{}}
	env := &sessionEnv{pods: pods, person: "u1", home: "/state/s1"}
	ctx := context.Background()
	if res, err := env.Exec(ctx, "s1", envport.Command{Script: `printf %s "$HOME"`}); err != nil || res.Output != "/state/s1" {
		t.Errorf("home probe = %+v, %v", res, err)
	}
	if res, err := env.Exec(ctx, "s1", envport.Command{Script: "rm -rf /"}); err == nil || res.ExitCode == 0 {
		t.Error("a script ran")
	}
	if err := env.WriteFile(ctx, "s1", ".config/x", []byte("a"), 0o600); err != nil || pods.files["/state/s1/.config/x"] != "a" {
		t.Errorf("write = %v %v", err, pods.files)
	}
	if b, err := env.ReadFile(ctx, "s1", "/state/s1/.config/x"); err != nil || string(b) != "a" {
		t.Errorf("read = %q %v", b, err)
	}
	if _, err := env.Provision(ctx, envport.EnvSpec{}); err == nil {
		t.Error("provisioned")
	}
	if env.WriteTree(ctx, "s1", "/x", nil) == nil {
		t.Error("a tree was written")
	}
	if _, err := env.ListFiles(ctx, "s1", "/"); err == nil {
		t.Error("files were listed")
	}
	if _, err := env.Status(ctx, "s1"); err == nil {
		t.Error("a status")
	}
	if env.Destroy(ctx, "s1") != nil {
		t.Error("destroy")
	}
}

// A skill reads a workshop's repositories only when one is attached.
func TestASkillReadsAnAttachedWorkshop(t *testing.T) {
	for _, tc := range []struct {
		workshop string
		fetches  int
		read     string
	}{{"", 0, ""}, {"w1", 1, "/workshops/w1/api"}} {
		w := newWorld(t, "claude-code")
		ctx := context.Background()
		s, _, err := w.svc.Open(ctx, "u1", KindSkill, tc.workshop, "skill-1", false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.svc.Send(ctx, "u1", s.ID, "write it", nil); err != nil {
			t.Fatal(err)
		}
		answered(t, w.svc, s.ID, 2)
		w.host.mu.Lock()
		start := fmt.Sprint(w.host.starts[0])
		w.host.mu.Unlock()
		w.pods.mu.Lock()
		fetches := w.pods.fetches
		w.pods.mu.Unlock()
		if fetches != tc.fetches || (tc.read != "") != strings.Contains(start, "/workshops/w1/") ||
			(tc.read != "" && !strings.Contains(start, tc.read)) {
			t.Errorf("workshop %q: fetches %d, start %s", tc.workshop, fetches, start)
		}
	}
}

// A request (phase 5): proposed in a turn by the session's own token,
// kept with the answer, confirmed (its acts run in order until one fails)
// or declined, and what came of it is in the next prompt, once.
func TestARequestIsConfirmedAndReported(t *testing.T) {
	w := newWorld(t, "claude-code")
	ctx := context.Background()
	s, _, err := w.svc.Open(ctx, "u1", KindGeneral, "w1", "palette", false)
	if err != nil {
		t.Fatal(err)
	}
	acts := []Act{
		{Tool: "start_run", Title: "Start one attempt", Arguments: json.RawMessage(`{"id":"w1","key":"T-1"}`)},
		{Tool: "pause_run", Arguments: json.RawMessage(`{"id":"r1"}`)},
		{Tool: "abort_run", Arguments: json.RawMessage(`{"id":"r1"}`)},
	}
	// Outside a turn, and with another token: nothing.
	if errs, err := w.svc.Propose(ctx, "tok-1", "u1", s.ID, Request{Why: "x", Acts: acts}); err == nil && len(errs) == 0 {
		t.Error("a request outside a turn was taken")
	}
	w.host.onPrompt = func(string) {
		live, _, _ := w.svc.Store.AssistantSession(ctx, s.ID)
		if _, err := w.svc.Propose(ctx, "tok-other", "u1", s.ID, Request{Why: "x", Acts: acts}); !errors.Is(err, ErrNotYours) {
			t.Errorf("another token's request = %v", err)
		}
		for name, r := range map[string]Request{
			"no why":    {Acts: acts},
			"no acts":   {Why: "x"},
			"a secret":  {Why: "x", Acts: []Act{{Tool: "create_ticket", Arguments: json.RawMessage(`{"goal":"sk-secret-key"}`)}}},
			"too many":  {Why: "x", Acts: slices.Repeat(acts[:1], maxActs+1)},
			"a bad act": {Why: "x", Acts: []Act{{Tool: "", Arguments: json.RawMessage(`{}`)}}},
		} {
			if errs, err := w.svc.Propose(ctx, live.TokenID, "u1", s.ID, r); err != nil || len(errs) == 0 {
				t.Errorf("%s = %v, %v", name, errs, err)
			}
		}
		// A first request, then one that replaces it.
		if errs, err := w.svc.Propose(ctx, live.TokenID, "u1", s.ID, Request{Why: "first", Acts: acts[:1]}); err != nil || len(errs) > 0 {
			t.Errorf("a good request = %v, %v", errs, err)
		}
		if errs, err := w.svc.Propose(ctx, live.TokenID, "u1", s.ID, Request{Why: "Start it, then hold it.", Acts: acts,
			State: RequestDone}); err != nil || len(errs) > 0 {
			t.Errorf("the second request = %v, %v", errs, err)
		}
	}
	if _, err := w.svc.Send(ctx, "u1", s.ID, "start a run on this", nil); err != nil {
		t.Fatal(err)
	}
	got := frames(t, w.svc, "u1", idle)
	var requests int
	for _, f := range got {
		if f.Kind == "request" {
			requests++
		}
	}
	if requests != 2 {
		t.Errorf("request frames = %d, want 2", requests)
	}
	answered(t, w.svc, s.ID, 2)
	msgs, _ := w.svc.Store.AssistantMessages(ctx, s.ID, 0)
	r := msgs[1].Request
	if r == nil || r.Why != "Start it, then hold it." || r.State != RequestPending || len(r.Acts) != 3 {
		t.Fatalf("the answer's request = %+v", r)
	}

	// Confirmed: run in order; the second fails, the third is not run.
	var ran []string
	run := func(_ context.Context, a Act) (int, string) {
		ran = append(ran, a.Tool)
		if a.Tool == "pause_run" {
			return 409, strings.Repeat("x", maxResult+10)
		}
		return 201, `{"run_id":"r1"}`
	}
	if _, err := w.svc.Decide(ctx, "stranger", s.ID, msgs[1].Seq, true, run); !errors.Is(err, ErrNotFound) {
		t.Errorf("another person's decision = %v", err)
	}
	m, err := w.svc.Decide(ctx, "u1", s.ID, msgs[1].Seq, true, run)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ran, []string{"start_run", "pause_run"}) || m.Request.State != RequestDone ||
		m.Request.Acts[0].Status != 201 || m.Request.Acts[1].Status != 409 || len(m.Request.Acts[1].Result) > maxResult+10 ||
		m.Request.Acts[2].Status != 0 {
		t.Errorf("confirmed = %v %+v", ran, m.Request)
	}
	if _, err := w.svc.Decide(ctx, "u1", s.ID, msgs[1].Seq, false, run); !errors.Is(err, ErrStale) {
		t.Errorf("deciding twice = %v", err)
	}
	if _, err := w.svc.Decide(ctx, "u1", s.ID, msgs[0].Seq, true, run); !errors.Is(err, ErrNotFound) {
		t.Errorf("a message with no request = %v", err)
	}

	// The next prompt says what came of it, and the one after does not.
	w.host.onPrompt = nil
	if _, err := w.svc.Send(ctx, "u1", s.ID, "and now?", nil); err != nil {
		t.Fatal(err)
	}
	answered(t, w.svc, s.ID, 4)
	w.host.mu.Lock()
	p := w.host.prompts[1]
	w.host.mu.Unlock()
	if !strings.Contains(p, `"Start it, then hold it.": confirmed.`) || !strings.Contains(p, "start_run: done, 201") ||
		!strings.Contains(p, "pause_run: failed, 409") || !strings.Contains(p, "abort_run: not run") {
		t.Errorf("the next prompt = %q", p)
	}
	if _, err := w.svc.Send(ctx, "u1", s.ID, "again", nil); err != nil {
		t.Fatal(err)
	}
	answered(t, w.svc, s.ID, 6)
	w.host.mu.Lock()
	p = w.host.prompts[2]
	w.host.mu.Unlock()
	if strings.Contains(p, "What came of your requests") {
		t.Errorf("an outcome told twice: %q", p)
	}
}

// A declined request runs nothing; a pending one is said to be waiting.
func TestARequestDeclinedOrWaiting(t *testing.T) {
	w := newWorld(t, "claude-code")
	ctx := context.Background()
	s, _, _ := w.svc.Open(ctx, "u1", KindGeneral, "", "palette", false)
	add := func(why string) Message {
		m, err := w.store.AppendAssistantMessage(ctx, Message{SessionID: s.ID, Role: RoleAssistant, Text: "?",
			Request: &Request{Why: why, State: RequestPending, Acts: []Act{{Tool: "create_ticket", Arguments: json.RawMessage(`{}`)}}}})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	declined, waiting := add("File it."), add("Run it.")
	m, err := w.svc.Decide(ctx, "u1", s.ID, declined.Seq, false, func(context.Context, Act) (int, string) {
		t.Error("a declined act ran")
		return 0, ""
	})
	if err != nil || m.Request.State != RequestDeclined {
		t.Fatalf("declined = %+v, %v", m.Request, err)
	}
	text, told := w.svc.outcomes(ctx, s.ID)
	if !strings.Contains(text, `"File it.": declined`) || !strings.Contains(text, `"Run it.": not decided yet`) || len(told) != 1 || told[0].Seq != declined.Seq {
		t.Errorf("outcomes = %q, told %d", text, len(told))
	}
	w.svc.reported(ctx, s.ID, told)
	text, told = w.svc.outcomes(ctx, s.ID)
	if strings.Contains(text, "File it.") || !strings.Contains(text, "Run it.") || len(told) != 0 {
		t.Errorf("after reporting = %q", text)
	}
	_ = waiting
	// A request left running (the engine stopped mid-way) is never run
	// again, and the assistant is told its outcome is unknown.
	stuck := add("Ship it.")
	r := *stuck.Request
	r.State = RequestRunning
	if err := w.store.SetAssistantRequest(ctx, s.ID, stuck.Seq, RequestPending, r); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Decide(ctx, "u1", s.ID, stuck.Seq, true, func(context.Context, Act) (int, string) {
		t.Error("an interrupted request ran again")
		return 0, ""
	}); !errors.Is(err, ErrStale) {
		t.Errorf("confirming an interrupted request = %v", err)
	}
	if text, _ := w.svc.outcomes(ctx, s.ID); !strings.Contains(text, `"Ship it.": confirmed, but its acts were interrupted`) {
		t.Errorf("outcomes = %q", text)
	}
	// Two clicks at once: the second waits its turn, then finds it decided.
	block := make(chan struct{})
	go func() {
		_, _ = w.svc.Decide(ctx, "u1", s.ID, waiting.Seq, true, func(context.Context, Act) (int, string) {
			<-block
			return 201, "ok"
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		w.svc.mu.Lock()
		busy := len(w.svc.deciding) > 0
		w.svc.mu.Unlock()
		if busy || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := w.svc.Decide(ctx, "u1", s.ID, waiting.Seq, true, nil); !errors.Is(err, ErrStale) {
		t.Errorf("a second click = %v", err)
	}
	close(block)
}

// The run and dashboard pages' fills, and the general page's none.
func TestNewPagesFill(t *testing.T) {
	ctx := context.Background()
	flowDoc := "name: ship\nversion: 5\nparameters: { harness: fake, model: fake/model }\nagents:\n  - name: b\n    prompt: build\nmain:\n  - { id: build, type: agent, harness: $harness, model: $model, agent: b }\n"
	fill, _ := json.Marshal(map[string]string{"flow": "ship", "yaml": flowDoc})
	if out, errs := pages[KindRun].Check(ctx, fakeChecks{}, "u1", "w1", fill); len(errs) > 0 || !strings.Contains(string(out), `"flow":"ship"`) {
		t.Errorf("a run's flow fill = %s %v", out, errs)
	}
	for name, raw := range map[string]string{
		"no flow":  `{"yaml":"x"}`,
		"bad yaml": `{"flow":"ship","yaml":"stages: ["}`,
		"extra":    `{"flow":"ship","yaml":"x","more":1}`,
	} {
		if _, errs := pages[KindRun].Check(ctx, fakeChecks{}, "u1", "w1", json.RawMessage(raw)); len(errs) == 0 {
			t.Errorf("run %s: accepted", name)
		}
	}
	if _, errs := pages[KindDashboard].Check(ctx, fakeChecks{}, "u1", "", json.RawMessage(`{"panels":[{"title":"a"}]}`)); len(errs) > 0 {
		t.Errorf("a dashboard fill = %v", errs)
	}
	for name, raw := range map[string]string{
		"not a list":    `{"panels":{}}`,
		"the catalog's": `{"panels":[{"measure":"nope"}]}`,
		"nothing":       `{}`,
	} {
		if _, errs := pages[KindDashboard].Check(ctx, fakeChecks{}, "u1", "", json.RawMessage(raw)); len(errs) == 0 {
			t.Errorf("dashboard %s: accepted", name)
		}
	}
	if _, errs := pages[KindGeneral].Check(ctx, fakeChecks{}, "u1", "", json.RawMessage(`{}`)); len(errs) == 0 || !strings.Contains(errs[0], "nothing to fill") {
		t.Errorf("the general page's fill = %v", errs)
	}
	// A run's session names its workshop; a palette's needs none, and has no
	// fill tool.
	w := newWorld(t, "claude-code")
	if _, _, err := w.svc.Open(ctx, "u1", KindRun, "", "r1", false); !errors.Is(err, ErrInvalid) {
		t.Errorf("a run with no workshop = %v", err)
	}
	s, _, err := w.svc.Open(ctx, "u1", KindGeneral, "", "palette", false)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := w.svc.Token(ctx, "u1", s.ID)
	if err != nil || tok == "" {
		t.Fatal(err)
	}
	live, _, _ := w.store.AssistantSession(ctx, s.ID)
	if sid, schema, acts, ok := w.svc.FillTool(ctx, live.TokenID); !ok || sid != s.ID || schema != nil || !acts {
		t.Errorf("the palette's bound tools = %s %v %v %v", sid, schema, acts, ok)
	}
}

// A page's session fills: a request from it is refused, and nothing of it
// is kept or shown.
func TestAPageRequestsNothing(t *testing.T) {
	ctx := context.Background()
	for _, k := range Kinds {
		if pages[k].Acts {
			continue
		}
		t.Run(string(k), func(t *testing.T) {
			w := newWorld(t, "claude-code")
			s, _, err := w.svc.Open(ctx, "u1", k, "w1", "s-1", false)
			if err != nil {
				t.Fatal(err)
			}
			w.host.onPrompt = func(string) {
				live, _, _ := w.svc.Store.AssistantSession(ctx, s.ID)
				errs, err := w.svc.Propose(ctx, live.TokenID, "u1", s.ID, Request{Why: "Start it.",
					Acts: []Act{{Tool: "start_run", Arguments: json.RawMessage(`{"id":"w1","key":"T-1"}`)}}})
				if err != nil || len(errs) == 0 || !strings.Contains(errs[0], "global assistant") {
					t.Errorf("a %s session's request = %v, %v", k, errs, err)
				}
			}
			if _, err := w.svc.Send(ctx, "u1", s.ID, "start a run", nil); err != nil {
				t.Fatal(err)
			}
			for _, f := range frames(t, w.svc, "u1", idle) {
				if f.Kind == "request" {
					t.Errorf("a request frame from a %s session", k)
				}
			}
			answered(t, w.svc, s.ID, 2)
			if msgs, _ := w.svc.Store.AssistantMessages(ctx, s.ID, 0); msgs[1].Request != nil {
				t.Errorf("a %s session kept a request: %+v", k, msgs[1].Request)
			}
		})
	}
}

// A warm starts the pod once, with the harness's hosts, and returns at
// once; a second while it runs starts nothing.
func TestWarm(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "claude-code")
	w.pods.hold = make(chan struct{})
	ensured := func() [][]string {
		w.pods.mu.Lock()
		defer w.pods.mu.Unlock()
		return slices.Clone(w.pods.ensured)
	}
	if err := w.svc.Warm(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(ensured()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := w.svc.Warm(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if got := ensured(); len(got) != 1 || !slices.Equal(got[0], []string{"api.anthropic.com"}) {
		t.Errorf("ensured while warming = %v", got)
	}
	close(w.pods.hold)
	for time.Now().Before(deadline) {
		w.svc.mu.Lock()
		warming := w.svc.warming["u1"]
		w.svc.mu.Unlock()
		if !warming {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Done: the next warm starts one again.
	if err := w.svc.Warm(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	for len(ensured()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := ensured(); len(got) != 2 {
		t.Errorf("ensured after the first warm ended = %v", got)
	}

	if err := (&Service{Store: NewMemStore()}).Warm(ctx, "u1"); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("no pods = %v", err)
	}
	if err := w.svc.Warm(ctx, "u2"); !errors.Is(err, ErrNoChoice) {
		t.Errorf("no choice = %v", err)
	}
}

// fakeSkills is a library: a ref resolves to v3 unless it is gone or
// another's, or the library fails (resolveE); every pin holds one skill,
// "tickets", with two files.
type fakeSkills struct {
	mu       sync.Mutex
	trees    int
	treesE   error
	resolveE error
}

func (f *fakeSkills) ResolveSkillsets(_ context.Context, _ string, refs []skills.Ref) ([]event.SkillsetPin, error) {
	if f.resolveE != nil {
		return nil, f.resolveE
	}
	var out []event.SkillsetPin
	for _, r := range refs {
		if r.ID == "set-gone" || strings.HasPrefix(r.ID, "theirs") {
			return nil, fmt.Errorf("skillset %s: %w", r.ID, ErrSkillsetGone)
		}
		seq := r.Seq
		if seq == 0 {
			seq = 3
		}
		out = append(out, event.SkillsetPin{ID: r.ID, Name: "name-" + r.ID, Seq: seq, Hash: "h-" + r.ID})
	}
	return out, nil
}

func (f *fakeSkills) SkillTrees(_ context.Context, pins []event.SkillsetPin) ([]skills.Tree, error) {
	f.mu.Lock()
	f.trees++
	f.mu.Unlock()
	if f.treesE != nil {
		return nil, f.treesE
	}
	var out []skills.Tree
	if len(pins) > 0 {
		out = append(out, skills.Tree{Name: "tickets", Files: []envport.TreeFile{
			{Path: "SKILL.md", Data: []byte("---\nname: tickets\ndescription: How we write tickets.\n---\n")},
			{Path: "refs/style.md", Data: []byte("Short titles.")},
		}})
	}
	return out, nil
}

// A choice names the person's own skillsets, on a server that keeps them;
// a new session pins them as they are then, one gone since left out, and
// keeps its pins whatever the choice becomes.
func TestSkillsetsArePinnedAtOpen(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "claude-code")
	if err := w.svc.SetChoice(ctx, "u1", Choice{Integration: "int-1", Skillsets: []skills.Ref{{ID: "set-1"}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("skillsets on a server with none = %v", err)
	}
	w.svc.Skills = &fakeSkills{}
	if err := w.svc.SetChoice(ctx, "u1", Choice{Integration: "int-1", Skillsets: []skills.Ref{{ID: "theirs-1"}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("another's skillset = %v", err)
	}
	if err := w.svc.SetChoice(ctx, "u1", Choice{Integration: "int-1", Skillsets: []skills.Ref{{ID: "set-1"}, {ID: "set-1", Seq: 2}, {ID: "set-2", Seq: 1}}}); err != nil {
		t.Fatal(err)
	}
	if c, _ := w.store.AssistantChoice(ctx, "u1"); !slices.Equal(c.Skillsets, []skills.Ref{{ID: "set-1"}, {ID: "set-2", Seq: 1}}) {
		t.Errorf("the choice's skillsets = %+v", c.Skillsets)
	}
	// One deleted since it was chosen: left out, the rest pinned.
	if err := w.store.SaveAssistantChoice(ctx, Choice{UserID: "u1", Integration: "int-1",
		Skillsets: []skills.Ref{{ID: "set-1"}, {ID: "set-gone"}, {ID: "set-2", Seq: 1}}}); err != nil {
		t.Fatal(err)
	}
	s, _, err := w.svc.Open(ctx, "u1", KindTicket, "w1", "draft-1", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []event.SkillsetPin{{ID: "set-1", Name: "name-set-1", Seq: 3, Hash: "h-set-1"}, {ID: "set-2", Name: "name-set-2", Seq: 1, Hash: "h-set-2"}}
	if !slices.Equal(s.Skillsets, want) {
		t.Errorf("pins = %+v", s.Skillsets)
	}
	if err := w.svc.SetChoice(ctx, "u1", Choice{Integration: "int-1"}); err != nil {
		t.Fatal(err)
	}
	if again, _, _ := w.svc.Open(ctx, "u1", KindTicket, "w1", "draft-1", false); again.ID != s.ID || !slices.Equal(again.Skillsets, want) {
		t.Errorf("an open session after the choice changed = %+v", again.Skillsets)
	}
	if fresh, _, _ := w.svc.Open(ctx, "u1", KindTicket, "w1", "draft-1", true); fresh.Skillsets != nil {
		t.Errorf("a new session after the choice changed = %+v", fresh.Skillsets)
	}
}

// The library failing to answer is not a skillset gone: the session is not
// opened (a session without the pin would never install it), and one opened
// once the library is back pins the set.
func TestSkillsetsLibraryFailureOpensNothing(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "claude-code")
	lib := &fakeSkills{resolveE: errors.New("connection reset")}
	w.svc.Skills = lib
	if err := w.store.SaveAssistantChoice(ctx, Choice{UserID: "u1", Integration: "int-1", Skillsets: []skills.Ref{{ID: "set-1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.svc.Open(ctx, "u1", KindGeneral, "", "palette", false); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("open while the library fails = %v", err)
	}
	if list, _ := w.store.AssistantSessions(ctx, "u1"); len(list) != 0 {
		t.Fatalf("sessions saved while the library failed = %+v", list)
	}
	lib.resolveE = nil
	s, _, err := w.svc.Open(ctx, "u1", KindGeneral, "", "palette", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []event.SkillsetPin{{ID: "set-1", Name: "name-set-1", Seq: 3, Hash: "h-set-1"}}; !slices.Equal(s.Skillsets, want) {
		t.Errorf("pins once the library is back = %+v", s.Skillsets)
	}
}

// A turn installs the session's pinned skills into its harness's skills
// directory in the session's home, once; a session with none installs
// nothing.
func TestATurnInstallsItsSkills(t *testing.T) {
	ctx := context.Background()
	for kind, dir := range map[string]string{"claude-code": ".claude/skills", "opencode": ".config/opencode/skills"} {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t, kind)
			lib := &fakeSkills{}
			w.svc.Skills = lib
			bare, _, _ := w.svc.Open(ctx, "u1", KindTicket, "w1", "bare", false)
			if err := w.store.SaveAssistantChoice(ctx, Choice{UserID: "u1", Integration: "int-1", Skillsets: []skills.Ref{{ID: "set-1"}}}); err != nil {
				t.Fatal(err)
			}
			s, _, _ := w.svc.Open(ctx, "u1", KindTicket, "w1", "draft-1", false)
			if _, err := w.svc.Send(ctx, "u1", s.ID, "write the ticket", nil); err != nil {
				t.Fatal(err)
			}
			answered(t, w.svc, s.ID, 2)
			root := "/state/" + s.ID + "/" + dir + "/"
			file := func(p string) (string, bool) {
				w.pods.mu.Lock()
				defer w.pods.mu.Unlock()
				c, ok := w.pods.files[p]
				return c, ok
			}
			if c, ok := file(root + "tickets/SKILL.md"); !ok || !strings.Contains(c, "name: tickets") {
				t.Errorf("SKILL.md = %q, %v (files %v)", c, ok, slices.Sorted(maps.Keys(w.pods.files)))
			}
			if c, _ := file(root + "tickets/refs/style.md"); c != "Short titles." {
				t.Errorf("a support file = %q", c)
			}
			if c, _ := file(root + skillsMarker); !strings.Contains(c, "set-1 v3 h-set-1") {
				t.Errorf("the marker = %q", c)
			}
			// The second turn finds the marker: nothing is written again.
			w.pods.mu.Lock()
			delete(w.pods.files, root+"tickets/SKILL.md")
			w.pods.mu.Unlock()
			if _, err := w.svc.Send(ctx, "u1", s.ID, "again", nil); err != nil {
				t.Fatal(err)
			}
			answered(t, w.svc, s.ID, 4)
			if _, ok := file(root + "tickets/SKILL.md"); ok || lib.trees != 1 {
				t.Errorf("the second turn installed again (%d loads)", lib.trees)
			}
			// No pins: no skills.
			if _, err := w.svc.Send(ctx, "u1", bare.ID, "hi", nil); err != nil {
				t.Fatal(err)
			}
			answered(t, w.svc, bare.ID, 2)
			w.pods.mu.Lock()
			defer w.pods.mu.Unlock()
			for p := range w.pods.files {
				if strings.HasPrefix(p, "/state/"+bare.ID+"/"+dir) {
					t.Errorf("a session with no skillsets wrote %s", p)
				}
			}
		})
	}
}

// Skills that cannot be loaded or written fail the turn, saying so; a
// warm that cannot start the pod or open the harness says so too.
func TestSkillsAndWarmsThatFail(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		lib   *fakeSkills
		write string
		want  string
	}{
		"the library": {lib: &fakeSkills{treesE: errors.New("blob gone")}, want: "your skills: blob gone"},
		"the pod":     {lib: &fakeSkills{}, write: "/tickets/", want: "install skill tickets: disk full"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, "claude-code")
			w.svc.Skills = c.lib
			w.pods.writeFail = c.write
			if err := w.store.SaveAssistantChoice(ctx, Choice{UserID: "u1", Integration: "int-1", Skillsets: []skills.Ref{{ID: "set-1"}}}); err != nil {
				t.Fatal(err)
			}
			s, _, _ := w.svc.Open(ctx, "u1", KindTicket, "w1", "draft-1", true)
			if _, err := w.svc.Send(ctx, "u1", s.ID, "write it", nil); err != nil {
				t.Fatal(err)
			}
			answered(t, w.svc, s.ID, 2)
			if msgs, _ := w.svc.Messages(ctx, "u1", s.ID, 0); !strings.Contains(msgs[1].Error, c.want) {
				t.Errorf("the answer's error = %q", msgs[1].Error)
			}
		})
	}

	w := newWorld(t, "claude-code")
	if err := w.store.SaveAssistantChoice(ctx, Choice{UserID: "u2", Integration: "int-gone"}); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Warm(ctx, "u2"); err == nil || !strings.Contains(err.Error(), "your harness integration") {
		t.Errorf("a warm on a harness gone = %v", err)
	}
	w.pods.ensureE = errors.New("no node")
	if err := w.svc.Warm(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w.svc.mu.Lock()
		warming := w.svc.warming["u1"]
		w.svc.mu.Unlock()
		if !warming {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := w.svc.Warm(ctx, "u1"); err != nil {
		t.Errorf("a warm after one that failed = %v", err)
	}
}

// flakyStore fails one of its reads or writes.
type flakyStore struct {
	*MemStore
	fail string
}

var errStore = errors.New("the database is away")

func (s flakyStore) FindAssistantSession(ctx context.Context, u string, k Kind, w, sub string) (Session, bool, error) {
	if s.fail == "find" {
		return Session{}, false, errStore
	}
	return s.MemStore.FindAssistantSession(ctx, u, k, w, sub)
}

func (s flakyStore) AssistantChoice(ctx context.Context, u string) (Choice, error) {
	if s.fail == "choice" {
		return Choice{}, errStore
	}
	return s.MemStore.AssistantChoice(ctx, u)
}

func (s flakyStore) SaveAssistantSession(ctx context.Context, v Session) error {
	if s.fail == "save" {
		return errStore
	}
	return s.MemStore.SaveAssistantSession(ctx, v)
}

func (s flakyStore) AssistantSession(ctx context.Context, id string) (Session, bool, error) {
	if s.fail == "get" {
		return Session{}, false, errStore
	}
	return s.MemStore.AssistantSession(ctx, id)
}

// A store that fails is the caller's error: no session half-opened, no
// warm, no status made up.
func TestAStoreThatFails(t *testing.T) {
	ctx := context.Background()
	for _, fail := range []string{"find", "choice", "save", "get"} {
		w := newWorld(t, "claude-code")
		w.svc.Store = flakyStore{MemStore: w.store, fail: fail}
		if s, _, err := w.svc.Open(ctx, "u1", KindTicket, "w1", "T-1", false); !errors.Is(err, errStore) {
			t.Errorf("open with %s failing = %+v, %v", fail, s, err)
		}
	}
	w := newWorld(t, "claude-code")
	w.svc.Store = flakyStore{MemStore: w.store, fail: "choice"}
	if err := w.svc.Warm(ctx, "u1"); !errors.Is(err, errStore) {
		t.Errorf("a warm = %v", err)
	}
	if _, err := w.svc.Status(ctx, "u1", ""); !errors.Is(err, errStore) {
		t.Errorf("the status = %v", err)
	}
	// A token whose session is gone binds no tool.
	s, _, _ := w.svc.Open(ctx, "u1", KindGeneral, "", "palette", true)
	if s.ID != "" {
		t.Fatalf("opened %+v with no choice to read", s)
	}
	w.svc.Store = w.store
	s, _, _ = w.svc.Open(ctx, "u1", KindGeneral, "", "palette", true)
	if _, err := w.svc.Token(ctx, "u1", s.ID); err != nil {
		t.Fatal(err)
	}
	live, _, _ := w.store.AssistantSession(ctx, s.ID)
	w.svc.Store = flakyStore{MemStore: w.store, fail: "get"}
	if _, _, _, ok := w.svc.FillTool(ctx, live.TokenID); ok {
		t.Error("a token whose session cannot be read binds a tool")
	}
}
