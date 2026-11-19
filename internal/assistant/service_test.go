package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/ports/assistantport"
	"apothecary/internal/ports/envport"
)

// fakeAgentHost is the shim sidecar's agent API in memory: start, session,
// prompt, and an event stream. A prompt answers "answered: <its text's
// first line>" after onPrompt runs (a test fills from there, as the agent
// would through the MCP face).
type fakeAgentHost struct {
	srv *httptest.Server

	mu       sync.Mutex
	starts   []map[string]any
	sessions []map[string]any
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
		f.mu.Lock()
		f.sessions = append(f.sessions, req)
		fail := f.loadFail
		f.mu.Unlock()
		if id, _ := req["session_id"].(string); id != "" {
			if fail {
				http.Error(w, "no such session", http.StatusBadGateway)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"session_id": id, "loaded": true})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"session_id": "h-1", "model": "acme/one"})
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
}

func (p *fakePods) Ensure(_ context.Context, _ string, hosts []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensured = append(p.ensured, hosts)
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
	s, msgs, err := w.svc.Open(ctx, "u1", KindTicket, "w1", "draft-1")
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
		// The person saves the draft as a ticket and picks a model while it
		// answers: the turn's end keeps both.
		if _, err := w.svc.Rebind(ctx, "u1", s.ID, "APO-12"); err != nil {
			t.Errorf("rebind mid-turn = %v", err)
		}
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

	if after, _, _ := w.svc.Store.AssistantSession(ctx, s.ID); after.Subject != "APO-12" || after.Model != "acme/one" || after.HarnessSession != "h-1" {
		t.Errorf("after the turn = %+v: the mid-turn changes are lost", after)
	}
	// The turn's token went with it: a process left running fills nothing.
	w.tokens.mu.Lock()
	revoked := slices.Clone(w.tokens.revoked)
	w.tokens.mu.Unlock()
	if !slices.Contains(revoked, "tok-1") {
		t.Errorf("revoked after the turn = %v", revoked)
	}
	if _, err := w.svc.Fill(ctx, "tok-1", "u1", s.ID, json.RawMessage(`{"title":"late"}`)); !errors.Is(err, ErrNotYours) {
		t.Errorf("a fill after the turn = %v", err)
	}
	if sid, _, ok := w.svc.FillTool(ctx, "tok-1"); ok {
		t.Errorf("the fill tool after the turn: bound to %s", sid)
	}
	// A second turn: no instructions again, the session loaded, a token of
	// its own, the fetch reused.
	w.host.onPrompt = nil
	if _, err := w.svc.Send(ctx, "u1", s.ID, "thanks", nil); err != nil {
		t.Fatal(err)
	}
	answered(t, w.svc, s.ID, 4)
	if p := w.host.prompts[1]; strings.Contains(p, "You are the Apothecary assistant") {
		t.Errorf("the second prompt repeats the instructions: %q", p)
	}
	if w.host.sessions[1]["session_id"] != "h-1" || len(w.tokens.minted) != 2 || w.pods.fetches != 1 {
		t.Errorf("second turn: session %v, tokens %v, fetches %d", w.host.sessions[1], w.tokens.minted, w.pods.fetches)
	}
	// The session the harness lost: a new one, instructions again.
	w.host.loadFail = true
	if _, err := w.svc.Send(ctx, "u1", s.ID, "once more", nil); err != nil {
		t.Fatal(err)
	}
	answered(t, w.svc, s.ID, 6)
	if p := w.host.prompts[2]; !strings.Contains(p, "You are the Apothecary assistant") {
		t.Errorf("a new harness session lacks the instructions: %q", p)
	}
}

func TestOpenAndOwnership(t *testing.T) {
	w := newWorld(t, "opencode")
	ctx := context.Background()
	a, _, err := w.svc.Open(ctx, "u1", KindFlow, "w1", "builder")
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := w.svc.Open(ctx, "u1", KindFlow, "w1", "builder")
	if a.ID != b.ID {
		t.Error("the same subject opened two sessions")
	}
	for name, args := range map[string][3]string{
		"a bad kind":              {"dashboard", "w1", "x"},
		"no subject":              {"flow", "w1", " "},
		"a flow with no workshop": {"flow", "", "x"},
		"a workshop out of reach": {"ticket", "w-denied", "x"},
	} {
		if _, _, err := w.svc.Open(ctx, "u1", Kind(args[0]), args[1], args[2]); err == nil {
			t.Errorf("%s: opened", name)
		}
	}
	if _, _, err := w.svc.Open(ctx, "u1", KindSkill, "", "skill-1"); err != nil {
		t.Errorf("a skill with no workshop: %v", err)
	}
	// Another person reaches none of it.
	for name, err := range map[string]error{
		"messages": func() error { _, err := w.svc.Messages(ctx, "u2", a.ID, 0); return err }(),
		"send":     func() error { _, err := w.svc.Send(ctx, "u2", a.ID, "hi", nil); return err }(),
		"model":    func() error { _, err := w.svc.SetModel(ctx, "u2", a.ID, "x"); return err }(),
		"rebind":   func() error { _, err := w.svc.Rebind(ctx, "u2", a.ID, "y"); return err }(),
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
	draft, _, _ := w.svc.Open(ctx, "u1", KindTicket, "w1", "draft-9")
	if s, err := w.svc.Rebind(ctx, "u1", draft.ID, "APO-12"); err != nil || s.Subject != "APO-12" {
		t.Errorf("rebind = %+v, %v", s, err)
	}
	if found, _, _ := w.svc.Open(ctx, "u1", KindTicket, "w1", "APO-12"); found.ID != draft.ID {
		t.Error("the saved ticket does not find its draft's conversation")
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
	s, _, _ := w.svc.Open(ctx, "u1", KindSkill, "", "skill-1")
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
	s, _, _ := w.svc.Open(ctx, "u1", KindFlow, "w1", "builder")
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
	s2, _, _ := w2.svc.Open(ctx, "u1", KindFlow, "w1", "builder")
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
	s, _, _ := w.svc.Open(ctx, "u1", KindSkill, "", "skill-1")
	tok, err := w.svc.token(ctx, &s)
	if err != nil || tok != "apo_secret-1" || !slices.Equal(w.tokens.grants[0], []string{"skill:read", FillAction}) || w.tokens.scopes[0] != nil {
		t.Fatalf("skill token = %s %v %v %v", tok, err, w.tokens.grants, w.tokens.scopes)
	}
	sid, schema, ok := w.svc.FillTool(ctx, s.TokenID)
	if !ok || sid != s.ID || schema["required"] == nil {
		t.Errorf("fill tool = %s %v %v", sid, schema, ok)
	}
	if _, _, ok := w.svc.FillTool(ctx, "tok-unknown"); ok {
		t.Error("an unknown token has a fill tool")
	}
	w.svc.mu.Lock()
	lt := w.svc.tokens[s.ID]
	lt.until = time.Now().Add(time.Minute)
	w.svc.tokens[s.ID] = lt
	w.svc.mu.Unlock()
	if tok, _ := w.svc.token(ctx, &s); tok != "apo_secret-2" || !slices.Equal(w.tokens.revoked, []string{"tok-1"}) {
		t.Errorf("an old token: %s, revoked %v", tok, w.tokens.revoked)
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
		s, _, err := w.svc.Open(ctx, "u1", KindSkill, tc.workshop, "skill-1")
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
