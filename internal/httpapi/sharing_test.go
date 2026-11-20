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
	"apothecary/internal/account/authz"
	"apothecary/internal/engine"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/confstore/conftest"
	"apothecary/internal/run/event"
	"apothecary/internal/skills"
	"apothecary/internal/skills/skillstore"
	"apothecary/internal/workspace/sidecar"
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

// share puts an account on a project at a role, whatever it held before.
func (x *env) share(t *testing.T, project, user, role string) {
	t.Helper()
	from := ""
	if m, err := x.srv.Conf.GetMember(context.Background(), project, user); err == nil {
		from = m.Role
	}
	if _, err := x.srv.Conf.PutMember(context.Background(), confstore.Member{ProjectID: project, UserID: user, Role: role}, from); err != nil {
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
			!slices.Equal(ps[0].Access.Actions, []string{"project:read", "project:leave", "run:read", "ticket:read"}) {
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
			{"POST", pingPath + "/answer", "run:answer"},
			{"PUT", projPath + "/secrets/token", "project:secrets"},
			{"POST", projPath + "/tickets", "ticket:write"},
			{"GET", runPath + "/terminal", "run:shell"},
			{"POST", projPath + "/members", "project:members"},
			{"POST", "/api/flow-templates/builder/copy", "project:author"},
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

// Past its checkouts, a workspace is the terminal's reach: the owner's
// injected credentials live there. A viewer reads the run's checkouts and
// nothing else; a maintainer (run:shell) reaches what the terminal does.
func TestWorkspaceBeyondTheCheckoutsIsTheShells(t *testing.T) {
	x := newEnv(t, engine.Config{})
	var seen []string
	side := fakeSidecar(t, &seen)
	x.res.Envp = &workspaceEnv{EnvProvisioner: x.envp, sidecarAddr: strings.TrimPrefix(side.URL, "http://")}
	x.seedLiveRun(t, "r1")
	viewer := x.account(t, "viewer", auth.RoleMember)
	maintainer := x.account(t, "maintainer", auth.RoleMember)
	x.share(t, testProject, "viewer", confstore.MemberViewer)
	x.share(t, testProject, "maintainer", confstore.MemberMaintainer)
	base := "/api/runs/r1/workspace"
	const secret = "/home/owner/.git-credentials"
	stat := func(who string, hdr map[string]string, items ...sidecar.StatItem) []sidecar.StatResult {
		t.Helper()
		return decode[WorkspaceStatResponse](t, x.expect(t, who, hdr, "POST", base+"/stat", WorkspaceStatRequest{Items: items}, 200)).Items
	}

	t.Run("a viewer", func(t *testing.T) {
		x.expect(t, "viewer", viewer, "GET", base+"/file?path=main.go", nil, 200)
		if got := decode[sidecar.Resolved](t, x.expect(t, "viewer", viewer, "GET", base+"/resolve?path=/work/repo/main.go", nil, 200)); got != (sidecar.Resolved{Path: "main.go"}) {
			t.Errorf("an absolute path in the checkout = %+v", got)
		}
		// Refused alike, there or not.
		for _, path := range []string{"/path/file?path=" + secret, "/path/tree?path=~", "/resolve?path=" + secret, "/resolve?path=/gone"} {
			if data := x.expect(t, "viewer", viewer, "GET", base+path, nil, 403); !strings.Contains(string(data), "run:shell") {
				t.Errorf("%s refused without naming run:shell: %s", path, data)
			}
		}
		got := stat("viewer", viewer, sidecar.StatItem{Path: "a.go"}, sidecar.StatItem{Abs: secret})
		if len(got) != 2 || !got[0].Exists || got[1].Exists || !strings.Contains(got[1].Error, "run:shell") {
			t.Errorf("stat = %+v", got)
		}
		before := len(seen)
		if got := stat("viewer", viewer, sidecar.StatItem{Abs: secret}); len(got) != 1 || !strings.Contains(got[0].Error, "run:shell") {
			t.Errorf("an absolute batch = %+v", got)
		}
		if len(seen) != before {
			t.Errorf("a batch of refused items reached the sidecar: %v", seen[before:])
		}
		for _, s := range seen {
			if strings.HasPrefix(s, "/fs/abs/") || strings.HasPrefix(s, "/fs/stat") && strings.Contains(s, "git-credentials") {
				t.Errorf("the sidecar read past the checkouts for a viewer: %s", s)
			}
		}
	})

	t.Run("a maintainer", func(t *testing.T) {
		if f := decode[sidecar.File](t, x.expect(t, "maintainer", maintainer, "GET", base+"/path/file?path="+secret, nil, 200)); f.Path != secret {
			t.Errorf("file = %+v", f)
		}
		x.expect(t, "maintainer", maintainer, "GET", base+"/path/tree?path=~", nil, 200)
		if got := decode[sidecar.Resolved](t, x.expect(t, "maintainer", maintainer, "GET", base+"/resolve?path="+secret, nil, 200)); got.Abs != secret {
			t.Errorf("resolve = %+v", got)
		}
		x.expect(t, "maintainer", maintainer, "GET", base+"/resolve?path=/gone", nil, 404)
		stat("maintainer", maintainer, sidecar.StatItem{Path: "a.go"}, sidecar.StatItem{Abs: secret})
		if last := seen[len(seen)-1]; !strings.Contains(last, `{"abs":"`+secret+`"}`) {
			t.Errorf("the sidecar was asked %s", last)
		}
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
	if data := x.expect(t, "owner", nil, "DELETE", path+"/me", nil, 403); !strings.Contains(string(data), "project:leave") {
		t.Errorf("the owner leaving refused without naming project:leave: %s", data)
	}
	if got := ids(t, x.expect(t, "owner", nil, "GET", path, nil, 200), "user_id"); !slices.Equal(got, []string{"maint"}) {
		t.Errorf("members at the end = %v", got)
	}
}

// staleMember is a store whose GetMember, once armed, lets a write land
// right after its read: the owner's change between a handler's read of a
// member and its write.
type staleMember struct {
	confstore.DB
	meanwhile func()
}

func (s *staleMember) GetMember(ctx context.Context, projectID, userID string) (confstore.Member, error) {
	m, err := s.DB.GetMember(ctx, projectID, userID)
	if f := s.meanwhile; f != nil {
		s.meanwhile = nil
		f()
	}
	return m, err
}

// A maintainer's request read an operator, and the owner promoted it
// before the write: the stale request changes nothing (409), and the
// maintainer the owner made stays one.
func TestMemberChangesFromAStaleRole(t *testing.T) {
	x := newEnv(t, engine.Config{})
	ctx := context.Background()
	maint := x.account(t, "maint", auth.RoleMember)
	x.account(t, "op", auth.RoleMember)
	x.share(t, testProject, "maint", confstore.MemberMaintainer)
	db := &staleMember{DB: x.srv.Conf.DB()}
	x.srv.Conf = confstore.New(db, secretsKey(t))
	az, err := authz.New(x.srv.Conf)
	if err != nil {
		t.Fatal(err)
	}
	x.srv.Authz = az
	promote := func() {
		if _, err := x.srv.Conf.PutMember(ctx, confstore.Member{ProjectID: testProject, UserID: "op", Role: confstore.MemberMaintainer}, confstore.MemberOperator); err != nil {
			t.Error(err)
		}
	}
	path := "/api/projects/" + testProject + "/members"
	for _, c := range []struct {
		name, method, path string
		body               any
	}{
		{"demote", "PUT", path + "/op", MemberRoleRequest{Role: "viewer"}},
		{"remove", "DELETE", path + "/op", nil},
		{"share again", "POST", path, MemberRequest{Login: "op", Role: "viewer"}},
	} {
		x.share(t, testProject, "op", confstore.MemberOperator)
		db.meanwhile = promote
		x.expect(t, c.name, maint, c.method, c.path, c.body, http.StatusConflict)
		if db.meanwhile != nil {
			t.Errorf("%s never read the member", c.name)
		}
		if m, err := x.srv.Conf.GetMember(ctx, testProject, "op"); err != nil || m.Role != confstore.MemberMaintainer {
			t.Errorf("%s: after the stale request = %+v, %v", c.name, m, err)
		}
	}
	// Unraced, the same request goes through.
	x.share(t, testProject, "op", confstore.MemberOperator)
	x.expect(t, "demote", maint, "PUT", path+"/op", MemberRoleRequest{Role: "viewer"}, 200)
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

// An account demoted to guest keeps its rows but none of their powers: the
// workshop it made as a member, the runs in it, and changes to its own
// configuration are a member's. Promoted back, all of it is there again.
func TestDemotedOwnerKeepsNothing(t *testing.T) {
	x := newEnv(t, engine.Config{})
	ctx := context.Background()
	erin := x.account(t, "erin", auth.RoleMember)
	prj := "prj-erin"
	conftest.SeedProject(t, x.srv.Conf, confstore.Project{ID: prj, UserID: "erin", Name: "erin's"})
	integ := decode[map[string]any](t, x.expect(t, "member", erin, "POST", "/api/integrations",
		map[string]any{"port": "repository", "kind": "fake", "name": "mine"}, 201))["id"].(string)
	if _, err := x.store.Append(ctx, "r-e", &event.RunCreated{ProjectID: prj, UserID: "erin", FlowName: "loop"}); err != nil {
		t.Fatal(err)
	}
	projPath, runPath, integPath := "/api/projects/"+prj, "/api/runs/r-e", "/api/integrations/"+integ
	x.expect(t, "member", erin, "GET", projPath, nil, 200)
	x.expect(t, "member", erin, "GET", runPath, nil, 200)

	if _, err := x.srv.Auth.SetRole(ctx, "erin", auth.RoleGuest); err != nil {
		t.Fatal(err)
	}
	for _, list := range []struct{ path, field string }{{"/api/projects", "id"}, {"/api/runs", "run_id"}} {
		if got := ids(t, x.expect(t, "guest", erin, "GET", list.path, nil, 200), list.field); len(got) != 0 {
			t.Errorf("a demoted owner's %s = %v", list.path, got)
		}
	}
	sameOrigin := merged(erin, map[string]string{"Origin": x.ts.URL})
	for _, c := range []struct{ method, path string }{
		{"GET", projPath},
		{"PUT", projPath},
		{"PUT", projPath + "/secrets/token"},
		{"DELETE", projPath},
		{"GET", runPath},
		{"POST", runPath + "/abort"},
		{"GET", runPath + "/terminal"},
	} {
		x.expect(t, "guest", sameOrigin, c.method, c.path, map[string]any{"name": "x", "value": "x"}, 404)
	}
	// Its configuration stays its own to see, frozen.
	x.expect(t, "guest", erin, "GET", integPath, nil, 200)
	for _, method := range []string{"PUT", "DELETE"} {
		if data := x.expect(t, "guest", erin, method, integPath, map[string]any{"name": "changed"}, 403); !strings.Contains(string(data), "integration:write") {
			t.Errorf("%s refused without naming integration:write: %s", method, data)
		}
	}

	if _, err := x.srv.Auth.SetRole(ctx, "erin", auth.RoleMember); err != nil {
		t.Fatal(err)
	}
	x.expect(t, "member again", erin, "GET", projPath, nil, 200)
	x.expect(t, "member again", erin, "GET", runPath, nil, 200)
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
		"project:leave":       {Name: "x", Actions: []string{"project:read", "project:leave"}},
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
	if len(perms) < 20 || !grantable["run:read"] || grantable["account:manage"] || grantable["project:leave"] {
		t.Errorf("permissions = %+v", perms)
	}

	// A member's token that sees its workshops cannot take the account off
	// one: leaving is the account's, never a limited token's.
	x.account(t, "member", auth.RoleMember)
	x.share(t, testProject, "member", confstore.MemberViewer)
	secret, _, err := x.srv.Auth.NewToken(ctx, "member", "sees", nil, auth.Grant{Actions: []string{"project:read"}})
	if err != nil {
		t.Fatal(err)
	}
	sees := bearer(secret)
	x.expect(t, "member's token", sees, "GET", "/api/projects/"+testProject, nil, 200)
	if data := x.expect(t, "member's token", sees, "DELETE", "/api/projects/"+testProject+"/members/me", nil, 403); !strings.Contains(string(data), "project:leave") {
		t.Errorf("leaving refused without naming project:leave: %s", data)
	}
	if m, err := x.srv.Conf.GetMember(ctx, testProject, "member"); err != nil || m.Role != confstore.MemberViewer {
		t.Errorf("after the token's leave = %+v, %v", m, err)
	}

	// A ticket record of its own is reached through a workshop on its
	// source: inside the token's workshops it is there (prj-q shares the
	// owner's internal tracker), outside them it is not.
	ticket := "/api/tickets/" + x.newTicket(t, "in P", "")
	x.expect(t, "token", tok, "GET", ticket, nil, 403)
	x.expect(t, "token in Q", limitedTo(t, x, "prj-q"), "GET", ticket, nil, 200)
	x.expect(t, "token elsewhere", limitedTo(t, x, "prj-elsewhere"), "GET", ticket, nil, 404)

	// A member's token limited to the later of two workshops on the
	// ticket's source reaches it there, whichever workshop came first.
	x.share(t, "prj-q", "member", confstore.MemberViewer)
	secret, _, err = x.srv.Auth.NewToken(ctx, "member", "in Q", nil,
		auth.Grant{Actions: []string{"ticket:read"}, ProjectIDs: []string{"prj-q"}})
	if err != nil {
		t.Fatal(err)
	}
	x.expect(t, "member's token in Q", bearer(secret), "GET", ticket, nil, 200)
}

// What a list holds is exactly what its guard lets the caller read: for
// every principal — owner, member at each role, stranger, admin, a member
// demoted to guest after making a workshop, a token limited to some
// workshops — and every listable kind, the list equals the set of resources
// whose GET answers 200. Lists are SQL (or their Mem twin) and the guard is
// Cedar; this holds them together.
func TestListsEqualTheGuard(t *testing.T) {
	x := newEnv(t, engine.Config{})
	ctx := context.Background()
	hdr := map[string]map[string]string{"tester": nil}
	for _, u := range []string{"alice", "bob", "carol", "dave", "erin"} {
		hdr[u] = x.account(t, u, auth.RoleMember)
	}
	hdr["root"] = x.account(t, "root", auth.RoleAdmin)
	hdr["guest"] = x.account(t, "guest", auth.RoleGuest)
	for _, p := range []struct{ id, owner string }{{"prj-a", "alice"}, {"prj-b", "bob"}, {"prj-e", "erin"}} {
		conftest.SeedProject(t, x.srv.Conf, confstore.Project{ID: p.id, UserID: p.owner, Name: p.id})
	}
	x.share(t, "prj-a", "carol", confstore.MemberViewer)
	x.share(t, "prj-a", "dave", confstore.MemberMaintainer)
	x.share(t, "prj-b", "dave", confstore.MemberOperator)
	x.share(t, testProject, "guest", confstore.MemberViewer)
	x.share(t, "prj-b", "alice", confstore.MemberViewer)
	x.share(t, "prj-b", "erin", confstore.MemberViewer)
	runs := map[string]string{"r-t": testProject, "r-a1": "prj-a", "r-a2": "prj-a", "r-b": "prj-b", "r-e": "prj-e"}
	owners := map[string]string{testProject: testUser, "prj-a": "alice", "prj-b": "bob", "prj-e": "erin"}
	for id, project := range runs {
		if _, err := x.store.Append(ctx, id,
			&event.RunCreated{ProjectID: project, UserID: owners[project], FlowName: "loop"},
			&event.PingRaised{PingID: "ping-" + id, Type: "clarification", Question: "?"}); err != nil {
			t.Fatal(err)
		}
	}
	// Erin made prj-e as a member; demoted to guest, she keeps prj-b only.
	if _, err := x.srv.Auth.SetRole(ctx, "erin", auth.RoleGuest); err != nil {
		t.Fatal(err)
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

	projects := []string{testProject, "prj-a", "prj-b", "prj-e"}
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
		"alice, nowhere she reaches": {}, "guest": {"r-t"}, "alice": {"r-a1", "r-a2", "r-b"}, "erin": {"r-b"},
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

// An operator on someone else's workshop installs their own skillsets,
// never the owner's — not by naming one, and not by rerunning the owner's
// run: its sets stay at the version the owner's run installed.
func TestOperatorsInstallTheirOwnSkillsets(t *testing.T) {
	skl := newTestSkills(t, nil)
	x := newEnvWith(t, engine.Config{}, skl.deps)
	skl.serve(t, x)
	op := x.account(t, "op", auth.RoleMember)
	x.share(t, testProject, "op", "operator")
	setOf := func(who string, hdr map[string]string) skillstore.Skillset {
		t.Helper()
		skill := decode[skillstore.Skill](t, x.expect(t, who, hdr, "POST", "/api/skills", skillBody(who, "d"), http.StatusCreated)).ID
		data := x.expect(t, who, hdr, "POST", "/api/skillsets", SkillsetRequest{Name: who, SkillIDs: []string{skill}}, http.StatusCreated)
		return decode[skillstore.Skillset](t, data)
	}
	owners, ops := setOf("owner", nil), setOf("operator", op)
	runs := ticketPath(testProject, x.newTicket(t, "T", "")) + "/runs"
	start := func(who string, hdr map[string]string, path string, body any) string {
		t.Helper()
		id := decode[RunRef](t, x.expect(t, who, hdr, "POST", path, body, http.StatusCreated)).RunID
		x.e.Wait(id)
		return id
	}
	pins := func(runID string) []event.SkillsetPin {
		t.Helper()
		return decode[RunDetail](t, x.expect(t, "owner", nil, "GET", "/api/runs/"+runID, nil, http.StatusOK)).Run.Skillsets
	}
	with := func(sets ...string) map[string]any {
		refs := []skills.Ref{}
		for _, id := range sets {
			refs = append(refs, skills.Ref{ID: id})
		}
		return map[string]any{"flow": "loop", "skillsets": refs}
	}

	x.expect(t, "operator", op, "POST", runs, with(owners.ID), http.StatusBadRequest)
	if p := pins(start("operator", op, runs, with(ops.ID))); len(p) != 1 || p[0].ID != ops.ID {
		t.Errorf("operator's run pins = %+v, want their own set", p)
	}
	theirs := start("owner", nil, runs, with(owners.ID))
	// The owner's set moves on to v2.
	more := decode[skillstore.Skill](t, x.expect(t, "owner", nil, "POST", "/api/skills", skillBody("more", "d"), http.StatusCreated)).ID
	x.expect(t, "owner", nil, "PUT", "/api/skillsets/"+owners.ID, SkillsetRequest{Name: "owner", SkillIDs: append(owners.SkillIDs, more)}, http.StatusOK)

	rerun := "/api/runs/" + theirs + "/rerun"
	if data := x.expect(t, "operator", op, "POST", rerun, map[string]any{"skillsets": []skills.Ref{{ID: owners.ID}}}, http.StatusNotFound); !strings.Contains(string(data), "skillset "+owners.ID) {
		t.Errorf("operator's rerun naming the owner's set = %s", data)
	}
	if p := pins(start("operator", op, rerun, nil)); len(p) != 1 || p[0].ID != owners.ID || p[0].Seq != 1 {
		t.Errorf("operator's rerun of the owner's run = %+v, want the owner's set still at v1", p)
	}
	if p := pins(start("owner", nil, rerun, nil)); len(p) != 1 || p[0].ID != owners.ID || p[0].Seq != 2 {
		t.Errorf("owner's rerun = %+v, want their set at v2", p)
	}
}

// Whoever a workshop is shared with learns where its tickets are written —
// its owner's ticket source's kind, name and address — and nothing of that
// integration's configuration or credentials, which stay its owner's.
func TestSharedWorkshopShowsItsTicketSource(t *testing.T) {
	x := newConfEnv(t)
	ctx := context.Background()
	project := x.makeProject(t, "mine", nil)
	op := x.account(t, "op", auth.RoleMember)
	x.share(t, project, "op", "operator")
	source := func(data []byte) *TicketSourceView {
		t.Helper()
		return decode[ProjectView](t, data).TicketSource
	}
	path := "/api/projects/" + project
	if src := source(x.expect(t, "operator", op, "GET", path, nil, http.StatusOK)); src == nil || src.Kind != confstore.KindInternal || src.Home != "" {
		t.Errorf("internal source = %+v", src)
	}

	// A remote tracker kind the test registry lacks, planted as the owner's.
	valid := x.conf.KindValid
	x.conf.KindValid = nil
	gh, err := x.conf.CreateIntegration(ctx, confstore.Integration{UserID: testUser, Port: confstore.PortTicketSource, Kind: "github", Name: "Issues",
		Config: map[string]any{"repos": "acme/app", "labels": "planted-config"}, Secrets: map[string]string{"token": "ghp-PLANTED"}})
	x.conf.KindValid = valid
	if err != nil {
		t.Fatal(err)
	}
	full := decode[map[string]any](t, x.expect(t, "owner", nil, "GET", path, nil, http.StatusOK))
	full["ticket_source_id"] = gh.ID
	x.expect(t, "owner", nil, "PUT", path, full, http.StatusOK)

	for _, data := range [][]byte{
		x.expect(t, "operator", op, "GET", path, nil, http.StatusOK),
		x.expect(t, "operator", op, "GET", "/api/projects", nil, http.StatusOK),
	} {
		if strings.Contains(string(data), "PLANTED") || strings.Contains(string(data), "planted-config") {
			t.Errorf("the owner's integration leaked: %s", data)
		}
	}
	want := TicketSourceView{Kind: "github", Name: "Issues", Home: "https://github.com/acme/app/issues"}
	if src := source(x.expect(t, "operator", op, "GET", path, nil, http.StatusOK)); src == nil || *src != want {
		t.Errorf("remote source = %+v, want %+v", src, want)
	}
	if rows := decode[[]ProjectView](t, x.expect(t, "operator", op, "GET", "/api/projects", nil, http.StatusOK)); len(rows) != 1 || rows[0].TicketSource == nil || *rows[0].TicketSource != want {
		t.Errorf("listed = %+v", rows)
	}

	// A tracker address carrying credentials reaches its readers as the
	// bare browse address.
	for _, c := range []struct{ kind, baseURL, home string }{
		{"jira", "https://alice:SECRET-USERINFO@jira.example/?token=SECRET-QUERY#SECRET-FRAGMENT", "https://jira.example"},
		{"github", "https://bob:SECRET-USERINFO@gh.acme/api/v3?access_token=SECRET-QUERY#SECRET-FRAGMENT", "https://gh.acme/acme/app/issues"},
	} {
		x.conf.KindValid = nil
		in, err := x.conf.CreateIntegration(ctx, confstore.Integration{UserID: testUser, Port: confstore.PortTicketSource, Kind: c.kind, Name: c.kind,
			Config: map[string]any{"base_url": c.baseURL, "repos": "acme/app"}})
		x.conf.KindValid = valid
		if err != nil {
			t.Fatal(err)
		}
		full["ticket_source_id"] = in.ID
		x.expect(t, "owner", nil, "PUT", path, full, http.StatusOK)
		for _, data := range [][]byte{
			x.expect(t, "operator", op, "GET", path, nil, http.StatusOK),
			x.expect(t, "operator", op, "GET", "/api/projects", nil, http.StatusOK),
		} {
			if strings.Contains(string(data), "SECRET") {
				t.Errorf("%s: a credential in the tracker address leaked: %s", c.kind, data)
			}
		}
		if src := source(x.expect(t, "operator", op, "GET", path, nil, http.StatusOK)); src == nil || src.Home != c.home {
			t.Errorf("%s: source = %+v, want home %q", c.kind, src, c.home)
		}
	}
}

// A shared operator moves a ticket through the statuses of the owner's
// ticket source and reads its types through the workshop: the integration
// itself stays the owner's (404 to them), and a stranger reads neither.
func TestSharedOperatorMovesTicketsThroughTheOwnersStatuses(t *testing.T) {
	x := newEnv(t, engine.Config{})
	key := x.newTicket(t, "shared", "")
	op := x.account(t, "op", auth.RoleMember)
	x.share(t, testProject, "op", "operator")
	stranger := x.account(t, "stranger", auth.RoleMember)
	workshop := "/api/projects/" + testProject
	source := decode[map[string]any](t, x.expect(t, "owner", nil, "GET", workshop, nil, http.StatusOK))["ticket_source_id"].(string)

	for _, list := range []string{"/statuses", "/types"} {
		x.expect(t, "operator", op, "GET", "/api/integrations/"+source+list, nil, http.StatusNotFound)
	}
	for _, list := range []string{"/ticket-statuses", "/ticket-types"} {
		x.expect(t, "stranger", stranger, "GET", workshop+list, nil, http.StatusNotFound)
	}

	types := x.expect(t, "operator", op, "GET", workshop+"/ticket-types", nil, http.StatusOK)
	if want := x.expect(t, "owner", nil, "GET", "/api/integrations/"+source+"/types", nil, http.StatusOK); string(types) != string(want) {
		t.Errorf("types = %s, want the source's %s", types, want)
	}
	statuses := decode[TrackerStatusList](t, x.expect(t, "operator", op, "GET", workshop+"/ticket-statuses", nil, http.StatusOK))
	current := decode[TicketDetail](t, x.expect(t, "operator", op, "GET", ticketPath(testProject, key), nil, http.StatusOK)).Status
	i := slices.IndexFunc(statuses.Statuses, func(st confstore.TrackerStatus) bool { return st.Name != current })
	if i < 0 {
		t.Fatalf("statuses = %+v: none but %q to move to", statuses, current)
	}
	next := statuses.Statuses[i].Name
	moved := decode[TicketDetail](t, x.expect(t, "operator", op, "POST", ticketPath(testProject, key)+"/status", TicketStatusRequest{Status: next}, http.StatusOK))
	if moved.Status != next {
		t.Errorf("moved to %q, want %q", moved.Status, next)
	}
}
