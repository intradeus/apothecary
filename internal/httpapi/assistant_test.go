package httpapi

// The assistant's routes: a person's sessions and theirs alone, the
// workshop's read checked on open, the stream, and the fill — through the
// MCP face, by the session's own token alone, bound to its session and
// shaped as its page's fill.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/assistant"
	"apothecary/internal/engine"
	"apothecary/internal/ports/assistantport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/confstore/conftest"
	"apothecary/internal/skills"
	"apothecary/internal/skills/skillstore"
)

type apiHarnesses struct{}

func (apiHarnesses) Integrations(context.Context, string) ([]assistant.Integration, error) {
	return []assistant.Integration{{ID: "int-1", Name: "mine", Kind: "claude-code"}}, nil
}

// Open waits for the question to be stopped: a turn held in flight
// (asking), there being no harness in these tests.
func (apiHarnesses) Open(ctx context.Context, _, _ string, _ envport.EnvProvisioner) (assistant.Harness, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// homePods is a pod as far as a turn reads one before its harness: the
// session's home.
type homePods struct{ assistantport.Pods }

func (homePods) Home(id string) string { return "/state/" + id }

// asking holds a question open on a session, the turn its fill needs, until
// the test ends.
func (x *env) asking(t *testing.T, id string) {
	t.Helper()
	svc := x.srv.Assistant
	x.expect(t, "a choice", nil, "PUT", "/api/assistant/choice", AssistantChoiceRequest{Integration: "int-1", Model: "acme/one"}, 200)
	svc.Pods = homePods{}
	x.expect(t, "a question", nil, "POST", "/api/assistant/sessions/"+id+"/messages", AssistantMessageRequest{Text: "hi"}, http.StatusAccepted)
	t.Cleanup(func() {
		if err := svc.Cancel(context.Background(), testUser, id); err != nil {
			t.Error(err)
		}
		for deadline := time.Now().Add(5 * time.Second); svc.Busy(id); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the question never stopped")
			}
		}
	})
}

// apiWorkshops lets a person read a workshop they own.
type apiWorkshops struct{ x *env }

func (w apiWorkshops) Repos(ctx context.Context, person, ws string) ([]assistantport.FetchItem, error) {
	p, err := w.x.srv.Conf.GetProject(ctx, ws)
	if err != nil || p.UserID != person {
		return nil, errors.New("workshop not found")
	}
	return nil, nil
}

type apiTokens struct{ a *auth.Service }

func (t apiTokens) Mint(ctx context.Context, person, name string, actions, ws []string, ttl time.Duration) (string, string, error) {
	until := time.Now().Add(ttl)
	secret, tok, err := t.a.NewToken(ctx, person, name, &until, auth.Grant{Actions: actions, ProjectIDs: ws})
	return tok.ID, secret, err
}
func (t apiTokens) Revoke(ctx context.Context, person, id string) error {
	return t.a.DeleteToken(ctx, person, id)
}

type apiChecks struct{}

func (apiChecks) TicketTypes(context.Context, string, string) ([]string, error) {
	return []string{"bug"}, nil
}

func (apiChecks) DashboardPanels(context.Context, string, json.RawMessage) error { return nil }

func newAssistantEnv(t *testing.T) *env {
	t.Helper()
	x := newEnv(t, engine.Config{})
	var face []string
	for _, a := range MCPActions() {
		face = append(face, string(a))
	}
	x.srv.Assistant = &assistant.Service{Store: assistant.NewMemStore(), Harnesses: apiHarnesses{},
		Workshops: apiWorkshops{x: x}, Tokens: apiTokens{a: x.srv.Auth}, Checks: apiChecks{}, FaceActions: face}
	x.reserve(t)
	return x
}

func TestAssistantSessionsArePersonal(t *testing.T) {
	x := newAssistantEnv(t)
	bob := x.addUser(t, "bob", false)
	conftest.SeedProject(t, x.srv.Conf, confstore.Project{ID: "prj-bob", UserID: "bob", Name: "bobs"})

	// No pods here: the status says so, and a question is refused.
	st := decode[assistant.Status](t, x.expect(t, "me", nil, "GET", "/api/assistant", nil, 200))
	if st.Enabled || st.Reason == "" || len(st.Pages) != len(assistant.Kinds) {
		t.Errorf("status = %+v", st)
	}
	x.expect(t, "a choice that is mine", nil, "PUT", "/api/assistant/choice", AssistantChoiceRequest{Integration: "int-1", Model: "acme/one"}, 200)
	x.expect(t, "a choice that is not", nil, "PUT", "/api/assistant/choice", AssistantChoiceRequest{Integration: "int-9"}, 400)

	view := decode[AssistantSessionView](t, x.expect(t, "open", nil, "POST", "/api/assistant/sessions",
		AssistantOpenRequest{Kind: "flow", Workshop: testProject, Subject: "builder"}, 200))
	if view.Session.Kind != assistant.KindFlow || view.Session.Integration != "int-1" || view.Messages == nil {
		t.Fatalf("view = %+v", view)
	}
	again := decode[AssistantSessionView](t, x.expect(t, "open again", nil, "POST", "/api/assistant/sessions",
		AssistantOpenRequest{Kind: "flow", Workshop: testProject, Subject: "builder"}, 200))
	if again.Session.ID != view.Session.ID {
		t.Error("the same subject opened two sessions")
	}
	fresh := decode[AssistantSessionView](t, x.expect(t, "open fresh", nil, "POST", "/api/assistant/sessions",
		AssistantOpenRequest{Kind: "flow", Workshop: testProject, Subject: "builder", Fresh: true}, 200))
	if fresh.Session.ID == view.Session.ID || len(fresh.Messages) != 0 {
		t.Errorf("a fresh open = %+v", fresh.Session)
	}
	// By its id: the session held, not the subject's latest.
	byID := decode[AssistantSessionView](t, x.expect(t, "the first session by its id", nil, "GET", "/api/assistant/sessions/"+view.Session.ID, nil, 200))
	if byID.Session.ID != view.Session.ID || byID.Messages == nil || byID.Busy {
		t.Errorf("the session by its id = %+v", byID)
	}
	x.expect(t, "a session not there", nil, "GET", "/api/assistant/sessions/nope", nil, 404)
	// Another's workshop is not there; a bad kind is refused.
	x.expect(t, "another's workshop", nil, "POST", "/api/assistant/sessions", AssistantOpenRequest{Kind: "flow", Workshop: "prj-bob", Subject: "x"}, 404)
	x.expect(t, "a bad kind", nil, "POST", "/api/assistant/sessions", AssistantOpenRequest{Kind: "widget", Workshop: testProject, Subject: "x"}, 400)

	id := view.Session.ID
	x.expect(t, "messages", nil, "GET", "/api/assistant/sessions/"+id+"/messages", nil, 200)
	x.expect(t, "a question with no pod", nil, "POST", "/api/assistant/sessions/"+id+"/messages", AssistantMessageRequest{Text: "hi"}, 503)
	x.expect(t, "the model", nil, "PUT", "/api/assistant/sessions/"+id+"/model", AssistantModelRequest{Model: "acme/two"}, 200)
	x.expect(t, "the subject route is gone", nil, "PUT", "/api/assistant/sessions/"+id+"/subject", map[string]string{"subject": "reviewer"}, 404)
	x.expect(t, "cancel", nil, "POST", "/api/assistant/sessions/"+id+"/cancel", nil, 204)
	x.expect(t, "a refresh with no pod", nil, "POST", "/api/assistant/repos/refresh", AssistantRefreshRequest{Workshop: testProject}, 503)
	x.expect(t, "a refresh of another's", nil, "POST", "/api/assistant/repos/refresh", AssistantRefreshRequest{Workshop: "prj-bob"}, 404)

	// Bob reaches none of my session: not there at all.
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/assistant/sessions/" + id},
		{"GET", "/api/assistant/sessions/" + id + "/messages"},
		{"POST", "/api/assistant/sessions/" + id + "/messages"},
		{"PUT", "/api/assistant/sessions/" + id + "/model"},
		{"POST", "/api/assistant/sessions/" + id + "/fill"},
	} {
		x.expect(t, "bob "+c.method+" "+c.path, bob, c.method, c.path, map[string]any{"text": "x", "model": "x", "fill": map[string]any{}}, 404)
	}
	x.expect(t, "a session that is not", nil, "GET", "/api/assistant/sessions/nope/messages", nil, 404)
}

// The fill: through the MCP face, the session's own token sees one fill
// tool bound to its session and shaped as its page's fill; my full token
// and the session's token for another session fill nothing.
func TestAssistantFillIsTheSessionsTokens(t *testing.T) {
	x := newAssistantEnv(t)
	ctx := context.Background()
	svc := x.srv.Assistant
	ticket := decode[AssistantSessionView](t, x.expect(t, "open", nil, "POST", "/api/assistant/sessions",
		AssistantOpenRequest{Kind: "ticket", Workshop: testProject, Subject: "draft-1"}, 200)).Session
	other := decode[AssistantSessionView](t, x.expect(t, "open", nil, "POST", "/api/assistant/sessions",
		AssistantOpenRequest{Kind: "ticket", Workshop: testProject, Subject: "draft-2"}, 200)).Session
	secret, err := svc.Token(ctx, testUser, ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	session := bearer(secret)
	x.asking(t, ticket.ID)

	// My full token lists no fill; the session's lists it, bound.
	if names := x.mcpToolNames(t, nil); strings.Contains(strings.Join(names, " "), "fill") {
		t.Errorf("a full token lists fill: %v", names)
	}
	status, r := x.mcpCall(t, session, "tools/list", nil)
	var list struct {
		Tools []mcpToolDef `json:"tools"`
	}
	if status != 200 || json.Unmarshal(r.Result, &list) != nil {
		t.Fatalf("session tools/list = %d %+v", status, r.Error)
	}
	var fill *mcpToolDef
	for i, tl := range list.Tools {
		if tl.Name == "fill" {
			fill = &list.Tools[i]
		}
		if tl.Name == "create_ticket" || strings.HasPrefix(tl.Name, "start_") {
			t.Errorf("the session's token lists a write: %s", tl.Name)
		}
	}
	if fill == nil {
		t.Fatalf("the session's token lists no fill: %+v", list.Tools)
	}
	props := fill.InputSchema["properties"].(map[string]any)
	if _, ok := props["title"]; !ok || props["id"] != nil {
		t.Errorf("the fill tool's input = %v, want the ticket's fill with the session bound", props)
	}

	frames, gap, live, stop := svc.Watch(testUser, 0)
	defer stop()
	_ = frames
	_ = gap
	// A fill the page refuses is the agent's to correct, and reaches no page.
	res := x.mcpTool(t, session, "fill", map[string]any{"change_type": "rewrite"})
	if res.IsError || !strings.Contains(res.Content[0].Text, `"ok":false`) || !strings.Contains(res.Content[0].Text, "change_type") {
		t.Errorf("a refused fill = %+v", res)
	}
	res = x.mcpTool(t, session, "fill", map[string]any{"title": "Fix the login page", "type_id": "bug"})
	if res.IsError || !strings.Contains(res.Content[0].Text, `"ok":true`) {
		t.Errorf("a good fill = %+v", res)
	}
	select {
	case f := <-live:
		if f.Kind != "fill" || f.Session != ticket.ID || !strings.Contains(string(f.Fill), "Fix the login page") {
			t.Errorf("frame = %+v", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no fill frame")
	}
	select {
	case f := <-live:
		t.Errorf("a second frame: %+v", f)
	default:
	}
	// The route itself: my own token fills nothing; the session's token
	// fills only its own session.
	x.expect(t, "my token", nil, "POST", "/api/assistant/sessions/"+ticket.ID+"/fill", AssistantFillRequest{Fill: json.RawMessage(`{"title":"x"}`)}, 403)
	x.expect(t, "another session", session, "POST", "/api/assistant/sessions/"+other.ID+"/fill", AssistantFillRequest{Fill: json.RawMessage(`{"title":"x"}`)}, 403)
	// The session's token writes nothing else.
	x.expect(t, "a write", session, "POST", "/api/projects/"+testProject+"/tickets", map[string]string{"title": "x"}, 403)
	x.expect(t, "a question", session, "POST", "/api/assistant/sessions/"+ticket.ID+"/messages", AssistantMessageRequest{Text: "x"}, 403)
}

func TestAssistantStream(t *testing.T) {
	x := newAssistantEnv(t)
	// A question in flight, asked before the stream opens.
	view := decode[AssistantSessionView](t, x.expect(t, "open", nil, "POST", "/api/assistant/sessions",
		AssistantOpenRequest{Kind: "skill", Subject: "skill-1"}, 200))
	x.asking(t, view.Session.ID)
	req, err := http.NewRequest("GET", x.ts.URL+"/api/assistant/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := http.DefaultClient.Do(x.authed(req.WithContext(ctx)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q", res.Header.Get("Content-Type"))
	}
	secret, err := x.srv.Assistant.Token(context.Background(), testUser, view.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	md := "---\nname: notes\ndescription: Write notes.\n---\n\nWrite them.\n"
	x.mcpTool(t, bearer(secret), "fill", map[string]any{"name": "notes", "files": []map[string]string{{"path": "SKILL.md", "content": md}}})
	// The stream opens at its head (nothing kept is replayed), then live.
	sc := bufio.NewScanner(res.Body)
	var events, datas []string
	var event string
	for len(events) < 2 && sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			event = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			events, datas = append(events, event), append(datas, v)
		}
	}
	if len(events) != 2 || events[0] != "head" || events[1] != "fill" || !strings.Contains(datas[1], `"page":"skill"`) {
		t.Errorf("stream = %v %v", events, datas)
	}
	// A reconnect from before a restart is told it missed frames.
	req2, _ := http.NewRequest("GET", x.ts.URL+"/api/assistant/stream?after=9999", nil)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	res2, err := http.DefaultClient.Do(x.authed(req2.WithContext(ctx2)))
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	sc2 := bufio.NewScanner(res2.Body)
	var seen []string
	for len(seen) < 2 && sc2.Scan() {
		if v, ok := strings.CutPrefix(sc2.Text(), "event: "); ok {
			seen = append(seen, v)
		}
	}
	if !slices.Equal(seen, []string{"head", "gap"}) {
		t.Errorf("a reconnect from before a restart = %v", seen)
	}
	x.expect(t, "a negative after", nil, "GET", "/api/assistant/stream?after=-1", nil, 400)
	x.expect(t, "a bad after", nil, "GET", "/api/assistant/stream?after=x", nil, 400)
	x.expect(t, "messages after junk", nil, "GET", "/api/assistant/sessions/"+view.Session.ID+"/messages?after=x", nil, 400)
}

func TestAssistantStatusCodes(t *testing.T) {
	for err, want := range map[error]int{
		assistant.ErrNotFound: 404, assistant.ErrInvalid: 400, assistant.ErrBusy: 409, assistant.ErrStale: 409, assistant.ErrNoChoice: 409,
		assistant.ErrNotYours: 403, assistant.ErrNotEnabled: 503, errors.New("x"): 500,
	} {
		if got := assistantStatus(err); got != want {
			t.Errorf("%v = %d, want %d", err, got, want)
		}
	}
}

// A page's session token lists fill and no request; the general session's
// (the assistant button's) lists request and no fill, with its session
// bound; each act must be a write tool fitting its input, and every write
// tool the face serves may be one.
func TestAssistantRequestTool(t *testing.T) {
	x := newAssistantEnv(t)
	ctx := context.Background()
	svc := x.srv.Assistant
	open := func(kind, ws, subject string) assistant.Session {
		return decode[AssistantSessionView](t, x.expect(t, "open", nil, "POST", "/api/assistant/sessions",
			AssistantOpenRequest{Kind: kind, Workshop: ws, Subject: subject}, 200)).Session
	}
	tools := func(s assistant.Session) (map[string]string, map[string]string) {
		secret, err := svc.Token(ctx, testUser, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		hdr := bearer(secret)
		_, r := x.mcpCall(t, hdr, "tools/list", nil)
		var list struct {
			Tools []mcpToolDef `json:"tools"`
		}
		if json.Unmarshal(r.Result, &list) != nil {
			t.Fatal("tools/list")
		}
		out := map[string]string{}
		for _, tl := range list.Tools {
			raw, _ := json.Marshal(tl.InputSchema)
			out[tl.Name] = string(raw)
		}
		return out, hdr
	}
	got, _ := tools(open("ticket", testProject, "T-1"))
	if got["fill"] == "" || got["request"] != "" {
		t.Errorf("a ticket session's bound tools: fill %q, request %q", got["fill"], got["request"])
	}
	general := open("general", testProject, "palette")
	palette, hdr := tools(general)
	if palette["fill"] != "" || palette["request"] == "" || strings.Contains(palette["request"], `"id"`) {
		t.Errorf("the general session's bound tools: %v", slices.Sorted(maps.Keys(palette)))
	}
	for name := range palette {
		if strings.HasPrefix(name, "create_") || strings.HasPrefix(name, "start_") || strings.HasPrefix(name, "update_") {
			t.Errorf("the session's token lists a write: %s", name)
		}
	}

	call := func(acts []map[string]any) string {
		res := x.mcpTool(t, hdr, "request", map[string]any{"why": "Start a run on it.", "acts": acts})
		return res.Content[0].Text
	}
	for name, c := range map[string]struct {
		acts []map[string]any
		want string
	}{
		"a read":           {[]map[string]any{{"tool": "list_tickets", "arguments": map[string]any{"id": testProject}}}, "not a write tool"},
		"an unknown tool":  {[]map[string]any{{"tool": "delete_everything"}}, "not a write tool"},
		"its own":          {[]map[string]any{{"tool": "fill", "arguments": map[string]any{}}}, "not a write tool"},
		"a missing arg":    {[]map[string]any{{"tool": "start_run", "arguments": map[string]any{"id": testProject}}}, "start_run's arguments"},
		"a wrong arg":      {[]map[string]any{{"tool": "pause_run", "arguments": map[string]any{"id": testProject, "speed": 3}}}, "pause_run's arguments"},
		"no question open": {[]map[string]any{{"tool": "start_run", "arguments": map[string]any{"id": testProject, "key": "T-1"}}}, "no question is being answered"},
	} {
		if out := call(c.acts); !strings.Contains(out, c.want) || !strings.Contains(out, `"ok":false`) {
			t.Errorf("%s = %s", name, out)
		}
	}
	// Every write tool the face serves may be an act: the never-a-tool list
	// (no route of it is a tool) is the only bound, beside the arguments.
	all, err := mcpCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var writes []string
	for _, tl := range all {
		if boundRoute(tl.route.ID) || readRoute(tl.route) || (tl.route.Enabled != nil && !tl.route.Enabled(x.srv)) {
			continue
		}
		writes = append(writes, tl.def.Name)
		_, errs := x.srv.checkActs(AssistantActsRequest{Why: "x", Acts: []AssistantActIn{{Tool: tl.def.Name}}})
		for _, e := range errs {
			if !strings.Contains(e, tl.def.Name+"'s arguments") {
				t.Errorf("%s is refused as an act: %s", tl.def.Name, e)
			}
		}
	}
	for _, want := range []string{"create_ticket", "start_run", "pause_run", "resume_run", "retry_run", "abort_run", "create_flow"} {
		if !slices.Contains(writes, want) {
			t.Errorf("%s is not among the write tools served: %v", want, writes)
		}
	}
	// The agent never confirms its own request: its token cannot reach the
	// person's decision.
	m, err := svc.Store.AppendAssistantMessage(ctx, assistant.Message{SessionID: general.ID, Role: assistant.RoleAssistant,
		Request: &assistant.Request{Why: "x", State: assistant.RequestPending, Acts: []assistant.Act{{Tool: "start_run", Arguments: json.RawMessage(`{}`)}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"confirm", "decline"} {
		x.expect(t, "the session's own "+verb, hdr, "POST", fmt.Sprintf("/api/assistant/sessions/%s/messages/%d/%s", general.ID, m.Seq, verb), nil, 403)
	}
	// The route itself: only the session's own token.
	x.expect(t, "my token", nil, "POST", "/api/assistant/sessions/"+general.ID+"/request",
		AssistantActsRequest{Why: "x", Acts: []AssistantActIn{{Tool: "start_run", Arguments: map[string]any{"id": testProject, "key": "T-1"}}}}, 403)
}

// The person's click runs each act through its route as the person: here,
// from the assistant button on a workshop's page, "start a run on this
// ticket" starts it with one confirmation. A token without the act's
// action gets the refusal as the outcome; a declined request runs nothing.
func TestAssistantRequestConfirmed(t *testing.T) {
	x := newAssistantEnv(t)
	ctx := context.Background()
	svc := x.srv.Assistant
	key := x.newTicket(t, "Add a health endpoint", "")
	s := decode[AssistantSessionView](t, x.expect(t, "open", nil, "POST", "/api/assistant/sessions",
		AssistantOpenRequest{Kind: "general", Workshop: testProject, Subject: "palette"}, 200)).Session
	// The act passes the request tool's checks: only no question in flight
	// stops it here (the service's tests hold a request made mid-answer).
	if _, errs := x.srv.checkActs(AssistantActsRequest{Why: "Start a run on it.", Acts: []AssistantActIn{{Tool: "start_run",
		Arguments: map[string]any{"id": testProject, "key": key}}}}); len(errs) > 0 {
		t.Fatalf("start_run as an act: %v", errs)
	}
	ask := func(acts ...assistant.Act) assistant.Message {
		m, err := svc.Store.AppendAssistantMessage(ctx, assistant.Message{SessionID: s.ID, Role: assistant.RoleAssistant, Text: "Shall I?",
			Request: &assistant.Request{Why: "Start a run on it.", State: assistant.RequestPending, Acts: acts}})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	start := assistant.Act{Tool: "start_run", Arguments: json.RawMessage(`{"id":"` + testProject + `","key":"` + key + `"}`)}
	m := ask(start)
	path := fmt.Sprintf("/api/assistant/sessions/%s/messages/%d", s.ID, m.Seq)
	done := decode[assistant.Message](t, x.expect(t, "confirm", nil, "POST", path+"/confirm", nil, 200))
	if r := done.Request; r.State != assistant.RequestDone || r.Acts[0].Status != http.StatusCreated || !strings.Contains(r.Acts[0].Result, "run_id") {
		t.Fatalf("confirmed = %+v", r)
	}
	runs := decode[[]map[string]any](t, x.expect(t, "runs", nil, "GET", "/api/runs?project="+testProject, nil, 200))
	if len(runs) != 1 {
		t.Errorf("runs after the confirmation = %d", len(runs))
	}
	x.expect(t, "twice", nil, "POST", path+"/confirm", nil, 409)

	// As a token that may talk to its assistant but not file tickets.
	secret, _, err := x.srv.Auth.NewToken(ctx, testUser, "panel only", nil, auth.Grant{Actions: []string{"assistant:read", "assistant:write"}})
	if err != nil {
		t.Fatal(err)
	}
	m = ask(assistant.Act{Tool: "create_ticket", Arguments: json.RawMessage(`{"id":"` + testProject + `","title":"T","goal":"G"}`)})
	path = fmt.Sprintf("/api/assistant/sessions/%s/messages/%d", s.ID, m.Seq)
	refused := decode[assistant.Message](t, x.expect(t, "confirm as a narrow token", bearer(secret), "POST", path+"/confirm", nil, 200))
	if a := refused.Request.Acts[0]; a.Status != http.StatusForbidden || !strings.Contains(a.Result, "ticket:write") {
		t.Errorf("an act beyond the caller = %+v", a)
	}

	m = ask(start)
	path = fmt.Sprintf("/api/assistant/sessions/%s/messages/%d", s.ID, m.Seq)
	declined := decode[assistant.Message](t, x.expect(t, "decline", nil, "POST", path+"/decline", nil, 200))
	if declined.Request.State != assistant.RequestDeclined || declined.Request.Acts[0].Status != 0 {
		t.Errorf("declined = %+v", declined.Request)
	}
	if runs := decode[[]map[string]any](t, x.expect(t, "runs", nil, "GET", "/api/runs?project="+testProject, nil, 200)); len(runs) != 1 {
		t.Errorf("runs after the decline = %d", len(runs))
	}
	x.expect(t, "a bad seq", nil, "POST", "/api/assistant/sessions/"+s.ID+"/messages/x/confirm", nil, 400)
	x.expect(t, "no request there", nil, "POST", "/api/assistant/sessions/"+s.ID+"/messages/999/confirm", nil, 404)
}

// The session's own routes answer for what they change, and refuse what
// they cannot read.
func TestAssistantSessionRoutes(t *testing.T) {
	x := newAssistantEnv(t)
	s := decode[AssistantSessionView](t, x.expect(t, "open", nil, "POST", "/api/assistant/sessions",
		AssistantOpenRequest{Kind: "ticket", Workshop: testProject, Subject: "draft-1"}, 200)).Session
	base := "/api/assistant/sessions/" + s.ID
	x.expect(t, "cancel with nothing running", nil, "POST", base+"/cancel", nil, 204)
	if got := decode[assistant.Session](t, x.expect(t, "model", nil, "PUT", base+"/model", AssistantModelRequest{Model: "acme/two"}, 200)); got.Model != "acme/two" {
		t.Errorf("model = %+v", got)
	}
	for _, c := range []struct{ method, path string }{
		{"PUT", base + "/model"}, {"POST", base + "/request"}, {"POST", "/api/assistant/repos/refresh"},
		{"PUT", "/api/assistant/choice"}, {"POST", "/api/assistant/sessions"}, {"POST", base + "/messages"},
	} {
		res, _ := x.do(t, c.method, c.path, "not an object", nil)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s %s with a bad body = %d", c.method, c.path, res.StatusCode)
		}
	}
	// No pods on this server: a refresh says so.
	x.expect(t, "refresh", nil, "POST", "/api/assistant/repos/refresh", AssistantRefreshRequest{Workshop: testProject}, 503)
	x.expect(t, "refresh elsewhere", nil, "POST", "/api/assistant/repos/refresh", AssistantRefreshRequest{Workshop: "nope"}, 404)
}

// apiPods is a pod that only starts: what a warm reaches.
type apiPods struct {
	assistantport.Pods
	ensured chan []string
}

func (apiPods) Status(context.Context, string) (assistantport.Status, error) {
	return assistantport.Status{State: assistantport.PodRunning}, nil
}

func (p apiPods) Ensure(_ context.Context, _ string, hosts []string) error {
	p.ensured <- hosts
	return nil
}

// apiWarmHarnesses open a harness that only names its hosts.
type apiWarmHarnesses struct{ apiHarnesses }

type apiHarness struct{ assistant.Harness }

func (apiHarness) EgressDomains() []string { return []string{"api.anthropic.com"} }

func (apiWarmHarnesses) Open(context.Context, string, string, envport.EnvProvisioner) (assistant.Harness, error) {
	return apiHarness{}, nil
}

// A warm starts one's pod with the harness's hosts and answers at once;
// it is no route on a server with no assistant.
func TestAssistantWarm(t *testing.T) {
	x := newAssistantEnv(t)
	x.expect(t, "no pods", nil, "POST", "/api/assistant/warm", nil, 503)
	pods := apiPods{ensured: make(chan []string, 1)}
	x.srv.Assistant.Pods = pods
	x.srv.Assistant.Harnesses = apiWarmHarnesses{}
	x.expect(t, "no harness chosen", nil, "POST", "/api/assistant/warm", nil, 409)
	x.expect(t, "choose", nil, "PUT", "/api/assistant/choice", AssistantChoiceRequest{Integration: "int-1"}, 200)
	x.expect(t, "warm", nil, "POST", "/api/assistant/warm", nil, 202)
	select {
	case hosts := <-pods.ensured:
		if !slices.Equal(hosts, []string{"api.anthropic.com"}) {
			t.Errorf("ensured with %v", hosts)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the pod was not started")
	}
	plain := newEnv(t, engine.Config{})
	plain.expect(t, "no assistant", nil, "POST", "/api/assistant/warm", nil, 404)
}

// apiNoHarness opens no harness, at once: a status lists no models.
type apiNoHarness struct{ apiHarnesses }

func (apiNoHarness) Open(context.Context, string, string, envport.EnvProvisioner) (assistant.Harness, error) {
	return nil, errors.New("no harness in these tests")
}

// A choice names one's own skillsets: they are saved and the status shows
// them; another's is refused.
func TestAssistantChoiceSkillsets(t *testing.T) {
	x := newAssistantEnv(t)
	skl := newTestSkills(t, nil)
	skl.serve(t, x)
	x.srv.Assistant.Skills = skl.lib
	x.srv.Assistant.Pods = apiPods{}
	x.srv.Assistant.Harnesses = apiNoHarness{}
	bob := x.addUser(t, "bob", false)
	setOf := func(who string, hdr map[string]string) string {
		t.Helper()
		skill := decode[skillstore.Skill](t, x.expect(t, who, hdr, "POST", "/api/skills", skillBody(who, "d"), http.StatusCreated)).ID
		data := x.expect(t, who, hdr, "POST", "/api/skillsets", SkillsetRequest{Name: who, SkillIDs: []string{skill}}, http.StatusCreated)
		return decode[skillstore.Skillset](t, data).ID
	}
	mine, bobs := setOf("tickets", nil), setOf("bobs", bob)
	x.expect(t, "mine", nil, "PUT", "/api/assistant/choice", AssistantChoiceRequest{Integration: "int-1", Skillsets: []skills.Ref{{ID: mine}}}, 200)
	st := decode[assistant.Status](t, x.expect(t, "status", nil, "GET", "/api/assistant", nil, 200))
	if !slices.Equal(st.Choice.Skillsets, []skills.Ref{{ID: mine}}) {
		t.Errorf("the status's skillsets = %+v", st.Choice.Skillsets)
	}
	x.expect(t, "bob's", nil, "PUT", "/api/assistant/choice", AssistantChoiceRequest{Integration: "int-1", Skillsets: []skills.Ref{{ID: bobs}}}, 400)
}
