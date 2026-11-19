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
)

type apiHarnesses struct{}

func (apiHarnesses) Integrations(context.Context, string) ([]assistant.Integration, error) {
	return []assistant.Integration{{ID: "int-1", Name: "mine", Kind: "claude-code"}}, nil
}
func (apiHarnesses) Open(context.Context, string, string, envport.EnvProvisioner) (assistant.Harness, error) {
	return nil, errors.New("no harness in these tests")
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
	if st.Enabled || st.Reason == "" || len(st.Pages) != 3 {
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
	// Another's workshop is not there; a bad kind is refused.
	x.expect(t, "another's workshop", nil, "POST", "/api/assistant/sessions", AssistantOpenRequest{Kind: "flow", Workshop: "prj-bob", Subject: "x"}, 404)
	x.expect(t, "a bad kind", nil, "POST", "/api/assistant/sessions", AssistantOpenRequest{Kind: "dashboard", Workshop: testProject, Subject: "x"}, 400)

	id := view.Session.ID
	x.expect(t, "messages", nil, "GET", "/api/assistant/sessions/"+id+"/messages", nil, 200)
	x.expect(t, "a question with no pod", nil, "POST", "/api/assistant/sessions/"+id+"/messages", AssistantMessageRequest{Text: "hi"}, 503)
	x.expect(t, "the model", nil, "PUT", "/api/assistant/sessions/"+id+"/model", AssistantModelRequest{Model: "acme/two"}, 200)
	x.expect(t, "the subject", nil, "PUT", "/api/assistant/sessions/"+id+"/subject", AssistantSubjectRequest{Subject: "reviewer"}, 200)
	x.expect(t, "cancel", nil, "POST", "/api/assistant/sessions/"+id+"/cancel", nil, 204)
	x.expect(t, "a refresh with no pod", nil, "POST", "/api/assistant/repos/refresh", AssistantRefreshRequest{Workshop: testProject}, 503)
	x.expect(t, "a refresh of another's", nil, "POST", "/api/assistant/repos/refresh", AssistantRefreshRequest{Workshop: "prj-bob"}, 404)

	// Bob reaches none of my session: not there at all.
	for _, c := range []struct{ method, path string }{
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
	// A refresh with nothing to fetch still reports, on the stream.
	view := decode[AssistantSessionView](t, x.expect(t, "open", nil, "POST", "/api/assistant/sessions",
		AssistantOpenRequest{Kind: "skill", Subject: "skill-1"}, 200))
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
		assistant.ErrNotFound: 404, assistant.ErrInvalid: 400, assistant.ErrBusy: 409, assistant.ErrNoChoice: 409,
		assistant.ErrNotYours: 403, assistant.ErrNotEnabled: 503, errors.New("x"): 500,
	} {
		if got := assistantStatus(err); got != want {
			t.Errorf("%v = %d, want %d", err, got, want)
		}
	}
}
