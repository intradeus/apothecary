package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"apothecary/internal/account/auth"
	"apothecary/internal/assistant"
	"apothecary/internal/engine"
	"apothecary/internal/project/confstore"
)

// A ticket's history: each save that changes it is a version by its author
// (an assistant's fill saved by the person says so), readable by whoever
// reads the ticket; a restore is a new version, made by whoever may edit it.
func TestTicketVersionRoutes(t *testing.T) {
	x := newEnv(t, engine.Config{})
	key := x.newTicket(t, "First", "")
	path := ticketPath(testProject, key) + "/versions"
	x.expect(t, "fill saved", nil, "PUT", ticketPath(testProject, key), map[string]any{"title": "Second", "goal": "g2", "assisted": true}, 200)
	x.expect(t, "same again", nil, "PUT", ticketPath(testProject, key), map[string]any{"title": "Second", "goal": "g2"}, 200)

	list := decode[[]TicketVersionSummary](t, x.expect(t, "list", nil, "GET", path, nil, 200))
	if len(list) != 2 {
		t.Fatalf("versions = %+v", list)
	}
	if v := list[0]; v.Seq != 2 || v.Title != "Second" || v.Kind != confstore.AuthorAssistant || v.AuthorID != testUser ||
		v.AuthorHandle != "tester" || !slices.Equal(v.Changed, []string{"title", "goal"}) || v.Oldest {
		t.Errorf("v2 = %+v", v)
	}
	if v := list[1]; v.Seq != 1 || v.Kind != confstore.AuthorUser || v.AuthorHandle != "tester" || !v.Oldest || v.Changed != nil {
		t.Errorf("v1 = %+v", v)
	}
	v1 := decode[TicketVersionView](t, x.expect(t, "get", nil, "GET", path+"/1", nil, 200))
	if v1.Title != "First" || v1.Goal != "g" || v1.AuthorHandle != "tester" {
		t.Errorf("v1 in full = %+v", v1)
	}
	x.expect(t, "not a number", nil, "GET", path+"/x", nil, 400)
	x.expect(t, "none such", nil, "GET", path+"/9", nil, 404)
	x.expect(t, "restore none such", nil, "POST", path+"/9/restore", nil, 404)

	got := decode[TicketDetail](t, x.expect(t, "restore", nil, "POST", path+"/1/restore", nil, 200))
	if got.Title != "First" {
		t.Errorf("restored = %+v", got.TicketRow)
	}
	list = decode[[]TicketVersionSummary](t, x.expect(t, "list after", nil, "GET", path, nil, 200))
	if len(list) != 3 || list[0].RestoredFrom != 1 || list[0].Kind != confstore.AuthorUser || list[0].Title != "First" {
		t.Errorf("after the restore = %+v", list)
	}

	viewer := x.account(t, "viewer", auth.RoleMember)
	x.share(t, testProject, "viewer", confstore.MemberViewer)
	x.expect(t, "viewer lists", viewer, "GET", path, nil, 200)
	x.expect(t, "viewer reads", viewer, "GET", path+"/1", nil, 200)
	if data := x.expect(t, "viewer restores", viewer, "POST", path+"/2/restore", nil, 403); !strings.Contains(string(data), "ticket:write") {
		t.Errorf("refusal = %s", data)
	}
	stranger := x.account(t, "stranger", auth.RoleMember)
	x.expect(t, "stranger lists", stranger, "GET", path, nil, 404)
	x.expect(t, "stranger restores", stranger, "POST", path+"/2/restore", nil, 404)
	if n := len(decode[[]TicketVersionSummary](t, x.expect(t, "list at the end", nil, "GET", path, nil, 200))); n != 3 {
		t.Errorf("a refused restore made a version: %d", n)
	}
}

// A remote issue's history is what its tracker showed and its base branch;
// its content is the tracker's, so a restore is refused and the next sync
// has nothing to undo.
func TestTicketVersionsOfARemoteIssue(t *testing.T) {
	x := newTrackerEnv(t)
	if got := decode[[]TicketVersionSummary](t, x.expect(t, "no record", nil, "GET", ticketPath(testProject, "PROJ-8")+"/versions", nil, 200)); len(got) != 0 {
		t.Errorf("an issue without a record = %+v", got)
	}
	x.expect(t, "no record, one version", nil, "GET", ticketPath(testProject, "PROJ-8")+"/versions/1", nil, 404)
	x.expect(t, "base branch", nil, "PUT", ticketPath(testProject, "PROJ-7"), map[string]any{"base_branch": "fix/seven"}, 200)
	path := ticketPath(testProject, "PROJ-7") + "/versions"
	list := decode[[]TicketVersionSummary](t, x.expect(t, "list", nil, "GET", path, nil, 200))
	if len(list) != 2 || list[0].Kind != confstore.AuthorUser || !slices.Equal(list[0].Changed, []string{"base_branch"}) ||
		list[1].Kind != confstore.AuthorSync || list[1].AuthorID != "int-1" || list[1].AuthorHandle != "" {
		t.Fatalf("versions = %+v", list)
	}
	data := x.expect(t, "restore", nil, "POST", path+"/1/restore", nil, 400)
	if !strings.Contains(string(data), "cannot be restored here") {
		t.Errorf("refusal = %s", data)
	}
	if got := x.detail(t, "PROJ-7"); got.BaseBranch != "fix/seven" {
		t.Errorf("after the refusal = %+v", got.TicketRow)
	}
	if n := len(decode[[]TicketVersionSummary](t, x.expect(t, "list after", nil, "GET", path, nil, 200))); n != 2 {
		t.Errorf("versions after the refusal = %d", n)
	}
}

// A ticket write an assistant proposed and the person confirmed is the
// assistant's, saved by them.
func TestAConfirmedAssistantActIsAuthoredAssistant(t *testing.T) {
	x := newAssistantEnv(t)
	ctx := context.Background()
	key := x.newTicket(t, "Add a health endpoint", "")
	s := decode[AssistantSessionView](t, x.expect(t, "open", nil, "POST", "/api/assistant/sessions",
		AssistantOpenRequest{Kind: "ticket", Workshop: testProject, Subject: key}, 200)).Session
	m, err := x.srv.Assistant.Store.AppendAssistantMessage(ctx, assistant.Message{SessionID: s.ID, Role: assistant.RoleAssistant, Text: "Shall I?",
		Request: &assistant.Request{Why: "Sharpen it.", State: assistant.RequestPending, Acts: []assistant.Act{{Tool: "update_ticket",
			Arguments: json.RawMessage(`{"id":"` + testProject + `","key":"` + key + `","title":"Add /healthz","goal":"g"}`)}}}})
	if err != nil {
		t.Fatal(err)
	}
	x.expect(t, "confirm", nil, "POST", fmt.Sprintf("/api/assistant/sessions/%s/messages/%d/confirm", s.ID, m.Seq), nil, 200)
	list, err := x.srv.Conf.TicketVersions(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Title != "Add /healthz" || list[0].Change != (confstore.Change{Kind: confstore.AuthorAssistant, AuthorID: testUser}) {
		t.Errorf("versions = %+v", list)
	}
}
