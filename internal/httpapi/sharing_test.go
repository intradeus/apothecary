package httpapi

// A workshop shared with other accounts, guests, and limited tokens: the
// authorizer's model (internal/account/authz) seen from the HTTP side —
// lists, reads, actions and their 404s and 403s.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/engine"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/confstore/conftest"
	"apothecary/internal/run/event"
)

// account makes an account of an instance role with a personal token and
// returns its bearer header.
func (x *env) account(t *testing.T, id string, role auth.Role) map[string]string {
	t.Helper()
	now := time.Now().UTC()
	if err := x.users.CreateUser(context.Background(), auth.User{
		ID: id, Handle: id, Email: id + "@example.test", Name: id, Role: role, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	secret, _, err := x.srv.Auth.NewToken(context.Background(), id, "t", nil, auth.Grant{})
	if err != nil {
		t.Fatal(err)
	}
	return bearer(secret)
}

func bearer(secret string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + secret}
}

// share puts an account on testProject at a role.
func (x *env) share(t *testing.T, project, user, role string) {
	t.Helper()
	if _, err := x.srv.Conf.PutMember(context.Background(), confstore.Member{ProjectID: project, UserID: user, Role: role}); err != nil {
		t.Fatal(err)
	}
}

// expect asserts a request's status.
func (x *env) expect(t *testing.T, who string, hdr map[string]string, method, path string, body any, want int) []byte {
	t.Helper()
	res, data := x.do(t, method, path, body, hdr)
	if res.StatusCode != want {
		t.Errorf("%s: %s %s = %d %s, want %d", who, method, path, res.StatusCode, data, want)
	}
	return data
}

// ids is the ids a list answered, by the JSON field that names them.
func ids(t *testing.T, data []byte, field string) []string {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("not a list: %s", data)
	}
	out := []string{}
	for _, r := range rows {
		out = append(out, r[field].(string))
	}
	slices.Sort(out)
	return out
}

func TestSharedWorkshop(t *testing.T) {
	x := newEnv(t, engine.Config{})
	run, _ := x.holdRun(t)
	ping, err := x.e.RaisePing(run, "clarification", "which db?", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	viewer := x.account(t, "viewer", auth.RoleMember)
	operator := x.account(t, "operator", auth.RoleGuest)
	maintainer := x.account(t, "maintainer", auth.RoleMember)
	stranger := x.account(t, "stranger", auth.RoleAdmin)
	x.account(t, "newbie", auth.RoleMember)
	x.share(t, testProject, "viewer", confstore.MemberViewer)
	x.share(t, testProject, "operator", confstore.MemberOperator)
	x.share(t, testProject, "maintainer", confstore.MemberMaintainer)
	runPath, pingPath, projPath := "/api/runs/"+run, "/api/pings/"+ping.ID, "/api/projects/"+testProject
	ticketPath := "/api/tickets/" + x.newTicket(t, "a shared ticket", "")
	sameOrigin := func(h map[string]string) map[string]string {
		return merged(h, map[string]string{"Origin": x.ts.URL})
	}

	t.Run("a viewer sees and does nothing", func(t *testing.T) {
		if got := ids(t, x.expect(t, "viewer", viewer, "GET", "/api/runs", nil, 200), "run_id"); !slices.Contains(got, run) {
			t.Errorf("runs = %v", got)
		}
		if got := ids(t, x.expect(t, "viewer", viewer, "GET", "/api/pings?status=all", nil, 200), "ping_id"); !slices.Equal(got, []string{ping.ID}) {
			t.Errorf("pings = %v", got)
		}
		data := x.expect(t, "viewer", viewer, "GET", "/api/projects", nil, 200)
		var ps []ProjectView
		_ = json.Unmarshal(data, &ps)
		if len(ps) != 1 || ps[0].ID != testProject || ps[0].OwnerHandle != "tester" || ps[0].Access.Role != "viewer" ||
			!slices.Equal(ps[0].Access.Actions, []string{"project:read", "run:read", "ticket:read"}) {
			t.Errorf("projects = %s", data)
		}
		x.expect(t, "viewer", viewer, "GET", runPath, nil, 200)
		x.expect(t, "viewer", viewer, "GET", pingPath, nil, 200)
		x.expect(t, "viewer", viewer, "GET", projPath+"/tickets", nil, 200)
		x.expect(t, "viewer", viewer, "GET", projPath+"/members", nil, 200)
		if rec := decode[TicketRecord](t, x.expect(t, "viewer", viewer, "GET", ticketPath, nil, 200)); len(rec.Workshops) != 1 || rec.Workshops[0].ID != testProject {
			t.Errorf("the ticket's workshops = %+v", rec.Workshops)
		}
		for _, c := range []struct{ method, path, needs string }{
			{"POST", runPath + "/abort", "run:operate"},
			{"POST", runPath + "/retry", "run:operate"},
			{"POST", pingPath + "/answer", "run:operate"},
			{"PUT", projPath + "/secrets/token", "project:write"},
			{"POST", projPath + "/tickets", "ticket:write"},
			{"GET", runPath + "/terminal", "run:shell"},
			{"POST", projPath + "/members", "project:members"},
			{"POST", "/api/flow-templates/builder/copy", "project:write"},
		} {
			if data := x.expect(t, "viewer", sameOrigin(viewer), c.method, c.path, map[string]any{"to_project_id": testProject, "name": "mine"}, 403); !strings.Contains(string(data), c.needs) {
				t.Errorf("viewer %s %s refused without naming %s: %s", c.method, c.path, c.needs, data)
			}
		}
	})

	t.Run("an operator runs, no shell, no settings", func(t *testing.T) {
		x.expect(t, "operator", sameOrigin(operator), "GET", runPath+"/terminal", nil, 403)
		x.expect(t, "operator", operator, "PUT", projPath+"/secrets/token", ProjectSecretRequest{Value: "x"}, 403)
		x.expect(t, "operator", operator, "POST", pingPath+"/answer", AnswerPingRequest{Answer: "accept", Content: map[string]any{"answer": "postgres"}}, 200)
	})

	t.Run("a maintainer shells, configures and shares, but the workshop stays the owner's", func(t *testing.T) {
		// Past the guard to the workspace, which the fake cannot host.
		x.expect(t, "maintainer", sameOrigin(maintainer), "GET", runPath+"/terminal", nil, http.StatusServiceUnavailable)
		x.expect(t, "maintainer", maintainer, "PUT", projPath+"/secrets/token", ProjectSecretRequest{Value: "x"}, 200)
		x.expect(t, "maintainer", maintainer, "POST", projPath+"/members", MemberRequest{Login: "newbie", Role: "viewer"}, 201)
		x.expect(t, "maintainer", maintainer, "DELETE", projPath, nil, 403)
		// The owner is no member to remove.
		x.expect(t, "maintainer", maintainer, "DELETE", projPath+"/members/"+testUser, nil, 404)
	})

	t.Run("someone it is not shared with finds nothing, admin or not", func(t *testing.T) {
		for _, path := range []string{runPath, pingPath, projPath, projPath + "/members", projPath + "/tickets", ticketPath} {
			x.expect(t, "stranger", stranger, "GET", path, nil, 404)
		}
		if got := ids(t, x.expect(t, "stranger", stranger, "GET", "/api/runs", nil, 200), "run_id"); len(got) != 0 {
			t.Errorf("a stranger's runs = %v", got)
		}
		x.expect(t, "stranger", stranger, "POST", runPath+"/abort", nil, 404)
	})

	t.Run("taken off the workshop, gone the next request", func(t *testing.T) {
		x.expect(t, "owner", nil, "DELETE", projPath+"/members/viewer", nil, 204)
		x.expect(t, "viewer", viewer, "GET", runPath, nil, 404)
		if got := ids(t, x.expect(t, "viewer", viewer, "GET", "/api/projects", nil, 200), "id"); len(got) != 0 {
			t.Errorf("projects after removal = %v", got)
		}
	})

	t.Run("an operator stops the run", func(t *testing.T) {
		x.expect(t, "operator", operator, "POST", runPath+"/abort", nil, http.StatusAccepted)
	})
}

func TestMembersRoutes(t *testing.T) {
	x := newEnv(t, engine.Config{})
	maint := x.account(t, "maint", auth.RoleMember)
	member := x.account(t, "member", auth.RoleMember)
	x.account(t, "carol", auth.RoleGuest)
	x.account(t, "dave", auth.RoleMember)
	path := "/api/projects/" + testProject + "/members"
	x.share(t, testProject, "maint", confstore.MemberMaintainer)

	// By handle and by email; an unknown account; the owner itself.
	if m := decode[Member](t, x.expect(t, "owner", nil, "POST", path, MemberRequest{Login: "carol", Role: "viewer"}, 201)); m.UserID != "carol" || m.Handle != "carol" || m.Role != "viewer" {
		t.Errorf("added = %+v", m)
	}
	x.expect(t, "owner", nil, "POST", path, MemberRequest{Login: "MEMBER@example.test", Role: "operator"}, 201)
	x.expect(t, "owner", nil, "POST", path, MemberRequest{Login: "nobody", Role: "viewer"}, 404)
	x.expect(t, "owner", nil, "POST", path, MemberRequest{Login: "tester", Role: "viewer"}, 400)
	x.expect(t, "owner", nil, "POST", path, MemberRequest{Login: " ", Role: "viewer"}, 400)
	x.expect(t, "owner", nil, "POST", path, MemberRequest{Login: "dave", Role: "owner"}, 400)
	if got := ids(t, x.expect(t, "owner", nil, "GET", path, nil, 200), "user_id"); !slices.Equal(got, []string{"carol", "maint", "member"}) {
		t.Errorf("members = %v", got)
	}

	// A maintainer manages viewers and operators, never maintainers.
	x.expect(t, "maint", maint, "PUT", path+"/carol", MemberRoleRequest{Role: "operator"}, 200)
	x.expect(t, "maint", maint, "PUT", path+"/carol", MemberRoleRequest{Role: "maintainer"}, 403)
	x.expect(t, "maint", maint, "POST", path, MemberRequest{Login: "dave", Role: "maintainer"}, 403)
	x.expect(t, "maint", maint, "DELETE", path+"/maint", nil, 403)
	x.expect(t, "maint", maint, "PUT", path+"/nobody", MemberRoleRequest{Role: "viewer"}, 404)
	x.expect(t, "maint", maint, "DELETE", path+"/nobody", nil, 404)
	x.expect(t, "maint", maint, "DELETE", path+"/carol", nil, 204)
	// The owner does.
	x.expect(t, "owner", nil, "PUT", path+"/member", MemberRoleRequest{Role: "maintainer"}, 200)
	x.expect(t, "owner", nil, "PUT", path+"/member", MemberRoleRequest{Role: "viewer"}, 200)

	// A member leaves; the owner cannot.
	x.expect(t, "member", member, "DELETE", path+"/me", nil, 204)
	x.expect(t, "member", member, "GET", path, nil, 404)
	x.expect(t, "owner", nil, "DELETE", path+"/me", nil, 400)
	if got := ids(t, x.expect(t, "owner", nil, "GET", path, nil, 200), "user_id"); !slices.Equal(got, []string{"maint"}) {
		t.Errorf("members at the end = %v", got)
	}
}

func TestGuestRole(t *testing.T) {
	x := newEnv(t, engine.Config{})
	guest := x.account(t, "guest", auth.RoleGuest)
	for _, c := range []struct {
		path string
		body any
	}{
		{"/api/projects", map[string]any{"name": "mine"}},
		{"/api/integrations", map[string]any{"port": "repository", "kind": "fake", "name": "x"}},
		{"/api/credentials", map[string]any{"provider": "openrouter", "key": "sk-or-x"}},
		{"/api/channels", map[string]any{"kind": "test"}},
	} {
		if data := x.expect(t, "guest", guest, "POST", c.path, c.body, 403); !strings.Contains(string(data), ":") {
			t.Errorf("%s refused without the action: %s", c.path, data)
		}
	}
	// One's own account, and what is shared, as its role allows.
	x.expect(t, "guest", guest, "GET", "/api/me", nil, 200)
	x.expect(t, "guest", guest, "POST", "/api/me/tokens", TokenRequest{Name: "mine", FullAccess: true}, 201)
	x.expect(t, "guest", guest, "GET", "/api/projects/"+testProject, nil, 404)
	x.share(t, testProject, "guest", confstore.MemberOperator)
	x.expect(t, "guest", guest, "GET", "/api/projects/"+testProject, nil, 200)
	key := decode[TicketDetail](t, x.expect(t, "guest", guest, "POST", "/api/projects/"+testProject+"/tickets", map[string]string{"title": "from a guest", "goal": "g"}, 201)).Key
	// Made in the owner's workshop, it is the owner's.
	if tk, err := x.srv.Conf.GetTicket(context.Background(), key); err != nil || tk.UserID != testUser {
		t.Errorf("the guest's ticket = %+v, %v", tk, err)
	}
	x.expect(t, "guest", guest, "PUT", "/api/projects/"+testProject+"/secrets/token", ProjectSecretRequest{Value: "x"}, 403)
}

// limitedTo is a token of the test user's holding ticket:read in one
// workshop.
func limitedTo(t *testing.T, x *env, project string) map[string]string {
	t.Helper()
	secret, _, err := x.srv.Auth.NewToken(context.Background(), testUser, "limited", nil,
		auth.Grant{Actions: []string{"ticket:read"}, ProjectIDs: []string{project}})
	if err != nil {
		t.Fatal(err)
	}
	return bearer(secret)
}

func TestScopedTokenLifecycle(t *testing.T) {
	x := newEnv(t, engine.Config{})
	ctx := context.Background()
	inP := x.createTicket(t, "in P")
	conftest.SeedProject(t, x.srv.Conf, confstore.Project{ID: "prj-q", UserID: testUser, Name: "q"})
	if _, err := x.store.Append(ctx, "r-q", &event.RunCreated{ProjectID: "prj-q", UserID: testUser, FlowName: "loop"}); err != nil {
		t.Fatal(err)
	}

	// Minting: the grant is checked against the vocabulary.
	for name, body := range map[string]TokenRequest{
		"neither":             {Name: "x"},
		"both":                {Name: "x", FullAccess: true, Actions: []string{"run:read"}},
		"unknown action":      {Name: "x", Actions: []string{"run:fly"}},
		"account:manage":      {Name: "x", Actions: []string{"account:manage"}},
		"full, but workshops": {Name: "x", FullAccess: true, ProjectIDs: []string{testProject}},
	} {
		x.expect(t, name, nil, "POST", "/api/me/tokens", body, 400)
	}
	created := decode[TokenCreated](t, x.expect(t, "owner", nil, "POST", "/api/me/tokens",
		TokenRequest{Name: "ci", Actions: []string{"run:read"}, ProjectIDs: []string{testProject}}, 201))
	if !slices.Equal(created.Token.Actions, []string{"run:read"}) || !slices.Equal(created.Token.ProjectIDs, []string{testProject}) {
		t.Errorf("minted = %+v", created.Token)
	}
	tok := bearer(created.Secret)

	if got := ids(t, x.expect(t, "token", tok, "GET", "/api/runs", nil, 200), "run_id"); !slices.Equal(got, []string{inP}) {
		t.Errorf("the token's runs = %v, want P's only", got)
	}
	x.expect(t, "token", tok, "GET", "/api/runs/"+inP, nil, 200)
	x.expect(t, "token", tok, "GET", "/api/runs/r-q", nil, 404)
	x.expect(t, "token", tok, "POST", "/api/runs/"+inP+"/abort", nil, 403)
	x.expect(t, "token", tok, "GET", "/api/projects", nil, 403)
	x.expect(t, "token", tok, "POST", "/api/me/tokens", TokenRequest{Name: "wider", FullAccess: true}, 403)
	x.expect(t, "token", tok, "PUT", "/api/me", map[string]any{"name": "x"}, 403)
	x.expect(t, "token", tok, "GET", "/api/me", nil, 403)

	// The list says what each token holds; the full one holds no list.
	var list []auth.Token
	_ = json.Unmarshal(x.expect(t, "owner", nil, "GET", "/api/me/tokens", nil, 200), &list)
	for _, k := range list {
		if (k.ID == created.Token.ID) != k.Scoped() {
			t.Errorf("token %s scoped = %v", k.Name, k.Scoped())
		}
	}
	// The vocabulary, for the form that mints them.
	var perms []Permission
	_ = json.Unmarshal(x.expect(t, "token", bearer(x.token), "GET", "/api/permissions", nil, 200), &perms)
	grantable := map[string]bool{}
	for _, p := range perms {
		grantable[p.Action] = p.Grantable
	}
	if len(perms) < 20 || !grantable["run:read"] || grantable["account:manage"] {
		t.Errorf("permissions = %+v", perms)
	}

	// A ticket record of its own is reached through a workshop on its
	// source: inside the token's workshops it is there (prj-q shares the
	// owner's internal tracker), outside them it is not.
	ticket := "/api/tickets/" + x.newTicket(t, "in P", "")
	x.expect(t, "token", tok, "GET", ticket, nil, 403)
	x.expect(t, "token in Q", limitedTo(t, x, "prj-q"), "GET", ticket, nil, 200)
	x.expect(t, "token elsewhere", limitedTo(t, x, "prj-elsewhere"), "GET", ticket, nil, 404)
}

// What a list holds is exactly what its guard lets the caller read: for
// every principal — owner, member at each role, stranger, admin, a token
// limited to some workshops — and every listable kind, the list equals the
// set of resources whose GET answers 200. Lists are SQL (or their Mem
// twin) and the guard is Cedar; this holds them together.
func TestListsEqualTheGuard(t *testing.T) {
	x := newEnv(t, engine.Config{})
	ctx := context.Background()
	hdr := map[string]map[string]string{"tester": nil}
	for _, u := range []string{"alice", "bob", "carol", "dave"} {
		hdr[u] = x.account(t, u, auth.RoleMember)
	}
	hdr["root"] = x.account(t, "root", auth.RoleAdmin)
	hdr["guest"] = x.account(t, "guest", auth.RoleGuest)
	for _, p := range []struct{ id, owner string }{{"prj-a", "alice"}, {"prj-b", "bob"}} {
		conftest.SeedProject(t, x.srv.Conf, confstore.Project{ID: p.id, UserID: p.owner, Name: p.id})
	}
	x.share(t, "prj-a", "carol", confstore.MemberViewer)
	x.share(t, "prj-a", "dave", confstore.MemberMaintainer)
	x.share(t, "prj-b", "dave", confstore.MemberOperator)
	x.share(t, testProject, "guest", confstore.MemberViewer)
	x.share(t, "prj-b", "alice", confstore.MemberViewer)
	runs := map[string]string{"r-t": testProject, "r-a1": "prj-a", "r-a2": "prj-a", "r-b": "prj-b"}
	owners := map[string]string{testProject: testUser, "prj-a": "alice", "prj-b": "bob"}
	for id, project := range runs {
		if _, err := x.store.Append(ctx, id,
			&event.RunCreated{ProjectID: project, UserID: owners[project], FlowName: "loop"},
			&event.PingRaised{PingID: "ping-" + id, Type: "clarification", Question: "?"}); err != nil {
			t.Fatal(err)
		}
	}
	limited := func(user string, projects []string) map[string]string {
		secret, _, err := x.srv.Auth.NewToken(ctx, user, "limited", nil,
			auth.Grant{Actions: []string{"run:read", "project:read"}, ProjectIDs: projects})
		if err != nil {
			t.Fatal(err)
		}
		return bearer(secret)
	}
	hdr["dave, prj-b only"] = limited("dave", []string{"prj-b"})
	hdr["alice, nowhere she reaches"] = limited("alice", []string{testProject})
	hdr["carol, any workshop"] = limited("carol", nil)

	projects := []string{testProject, "prj-a", "prj-b"}
	allRuns, allPings := []string{}, []string{}
	for id := range runs {
		allRuns = append(allRuns, id)
		allPings = append(allPings, "ping-"+id)
	}
	for _, kind := range []struct {
		list, field, item string
		all               []string
	}{
		{"/api/runs", "run_id", "/api/runs/", allRuns},
		{"/api/pings?status=all", "ping_id", "/api/pings/", allPings},
		{"/api/projects", "id", "/api/projects/", projects},
	} {
		for who, h := range hdr {
			listed := ids(t, x.expect(t, who, h, "GET", kind.list, nil, 200), kind.field)
			readable := []string{}
			for _, id := range kind.all {
				res, _ := x.do(t, "GET", kind.item+id, nil, h)
				switch res.StatusCode {
				case http.StatusOK:
					readable = append(readable, id)
				case http.StatusNotFound:
				default:
					t.Errorf("%s: GET %s%s = %d", who, kind.item, id, res.StatusCode)
				}
			}
			slices.Sort(readable)
			if !slices.Equal(listed, readable) {
				t.Errorf("%s: %s lists %v, the guard reads %v", who, kind.list, listed, readable)
			}
		}
	}
	// The model, spot-checked: the admin and the stranger see their own only;
	// a limited token sees inside its workshops only.
	for who, want := range map[string][]string{
		"root": {}, "carol": {"r-a1", "r-a2"}, "dave": {"r-a1", "r-a2", "r-b"}, "dave, prj-b only": {"r-b"},
		"alice, nowhere she reaches": {}, "guest": {"r-t"}, "alice": {"r-a1", "r-a2", "r-b"},
	} {
		if got := ids(t, x.expect(t, who, hdr[who], "GET", "/api/runs", nil, 200), "run_id"); !slices.Equal(got, want) {
			t.Errorf("%s's runs = %v, want %v", who, got, want)
		}
	}
}

// An operator writing tickets and starting runs on someone else's workshop
// attaches their own uploads: the ticket and the run stay the owner's, the
// files are the operator's — attached, and readable by the owner. An id of
// the owner's the operator sends claims nothing.
func TestOperatorsAttachTheirOwnUploads(t *testing.T) {
	files := newTestFiles(t, 1<<20, []string{"text/*"})
	x := newEnvWith(t, engine.Config{}, files.deps)
	files.serve(x)
	ctx := context.Background()
	op := x.account(t, "op", auth.RoleMember)
	x.share(t, testProject, "op", "operator")
	upload := func(name string) string {
		t.Helper()
		body, ct := multipartBody(name, "text/plain", []byte(name))
		req, err := http.NewRequest("POST", x.ts.URL+"/api/attachments", body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", ct)
		req.Header.Set("Authorization", op["Authorization"])
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var up Uploaded
		if err := json.NewDecoder(res.Body).Decode(&up); err != nil || res.StatusCode != http.StatusCreated || len(up.Attachments) != 1 {
			t.Fatalf("upload %s = %d, %+v, %v", name, res.StatusCode, up, err)
		}
		return up.Attachments[0].ID
	}
	created, edited, goal := upload("created.txt"), upload("edited.txt"), upload("goal.txt")
	owners, err := files.att.Save(ctx, testUser, "owners.txt", "text/plain", strings.NewReader("owner's"))
	if err != nil {
		t.Fatal(err)
	}

	data := x.expect(t, "operator", op, "POST", "/api/projects/"+testProject+"/tickets",
		map[string]any{"title": "T", "goal": "g", "attachment_ids": []string{created, owners.ID}}, http.StatusCreated)
	tk := decode[TicketDetail](t, data)
	if !slices.Equal(tk.AttachmentIDs, []string{created}) {
		t.Errorf("created ticket's files = %v, want [%s]", tk.AttachmentIDs, created)
	}
	data = x.expect(t, "operator", op, "PUT", ticketPath(testProject, tk.TicketID),
		map[string]any{"title": "T", "goal": "g", "attachment_ids": []string{created, edited}}, http.StatusOK)
	got := decode[TicketDetail](t, data).AttachmentIDs
	want := []string{created, edited}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("edited ticket's files = %v, want %v", got, want)
	}
	if rec, err := x.srv.Conf.GetTicket(ctx, tk.TicketID); err != nil || rec.UserID != testUser {
		t.Errorf("ticket = %+v, %v, want the workshop owner's", rec, err)
	}
	// The owner reads the operator's file on their ticket.
	x.expect(t, "owner", nil, "GET", "/api/attachments/"+edited, nil, http.StatusOK)

	data = x.expect(t, "operator", op, "POST", ticketPath(testProject, tk.TicketID)+"/runs",
		map[string]any{"flow": "loop", "attachment_ids": []string{goal, owners.ID}}, http.StatusCreated)
	var started RunRef
	if err := json.Unmarshal(data, &started); err != nil {
		t.Fatal(err)
	}
	defer x.e.Wait(started.RunID)
	if a, err := files.att.Get(ctx, goal); err != nil || a.RunID != started.RunID {
		t.Errorf("run's file = %+v, %v, want claimed for %s", a, err, started.RunID)
	}
	if owner, _, err := x.srv.runOwner(ctx, started.RunID); err != nil || owner != testUser {
		t.Errorf("run owner = %q, %v, want the workshop owner", owner, err)
	}
	if a, err := files.att.Get(ctx, owners.ID); err != nil || !a.Unclaimed() {
		t.Errorf("the owner's upload, sent by the operator = %+v, %v, want unclaimed", a, err)
	}
}
