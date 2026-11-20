package pgstore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/run/event"
)

func seedIntegration(t *testing.T, s *Store, id, user string) {
	t.Helper()
	if err := s.UpsertIntegration(context.Background(), confstore.StoredIntegration{
		ID: id, UserID: user, Port: "repository", Kind: "gitlab", Name: "n-" + id,
		Config: map[string]any{"k": id}, Sealed: []byte{1}, CreatedAt: testNow, UpdatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
}

func mkProject(id, name, user, integ string) confstore.Project {
	return confstore.Project{ID: id, UserID: user, Name: name, RepoAccountID: integ, ProvisionerID: integ,
		TicketSourceID: integ, DefaultFlow: "builder", CreatedAt: testNow, UpdatedAt: testNow}
}

func seedProject(t *testing.T, s *Store, p confstore.Project) {
	t.Helper()
	if err := s.UpsertProject(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationsScopeAndOwnership(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	if _, err := s.GetIntegration(ctx, "nope"); !errors.Is(err, confstore.ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	seedIntegration(t, s, "i1", "u1")
	seedIntegration(t, s, "i2", "u2")
	mine, err := s.ListIntegrations(ctx, "u1")
	if err != nil || len(mine) != 1 || mine[0].ID != "i1" || mine[0].Config["k"] != "i1" || mine[0].Sealed[0] != 1 {
		t.Fatalf("u1 lists = %+v, %v", mine, err)
	}
	if all, _ := s.ListIntegrations(ctx, ""); len(all) != 2 {
		t.Errorf("daemon pass lists %d, want 2", len(all))
	}
	if none, err := s.ListIntegrations(ctx, "u3"); err != nil || len(none) != 0 {
		t.Errorf("stranger lists %+v, %v", none, err)
	}

	// An edit cannot hand a credential to someone else, nor retype it.
	in, _ := s.GetIntegration(ctx, "i1")
	in.UserID, in.Port, in.Kind, in.Name, in.Sealed = "u2", "provisioner", "kubernetes", "renamed", nil
	if err := s.UpsertIntegration(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetIntegration(ctx, "i1")
	if got.UserID != "u1" || got.Port != "repository" || got.Kind != "gitlab" || got.Name != "renamed" || len(got.Sealed) != 0 {
		t.Errorf("after edit = %+v", got)
	}

	// (owner, port, name) is the unique key: another owner may reuse a name.
	dup := in
	dup.ID, dup.UserID, dup.Port, dup.Name = "i3", "u1", "repository", "renamed"
	if err := s.UpsertIntegration(ctx, dup); !errors.Is(err, confstore.ErrConflict) {
		t.Errorf("same owner duplicate = %v", err)
	}
	dup.UserID = "u9"
	if err := s.UpsertIntegration(ctx, dup); err != nil {
		t.Errorf("other owner, same name: %v", err)
	}

	bad := dup
	bad.ID, bad.Name, bad.Config = "i4", "bad", map[string]any{"x": make(chan int)}
	if err := s.UpsertIntegration(ctx, bad); err == nil {
		t.Error("unmarshalable config stored")
	}
}

func TestDeleteIntegrationRefusesEveryKindOfReference(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	if err := s.DeleteIntegration(ctx, "ghost"); !errors.Is(err, confstore.ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	for _, id := range []string{"repo", "prov", "harn", "src", "free"} {
		seedIntegration(t, s, id, "u1")
	}
	p := mkProject("p1", "web", "u1", "repo")
	p.ProvisionerID, p.HarnessIDs, p.TicketSourceID = "prov", []string{"harn"}, "src"
	seedProject(t, s, p)
	for _, id := range []string{"repo", "prov", "harn", "src"} {
		if err := s.DeleteIntegration(ctx, id); !errors.Is(err, confstore.ErrReferenced) || !strings.Contains(err.Error(), "web") {
			t.Errorf("delete %s = %v, want ErrReferenced naming the project", id, err)
		}
		if _, err := s.GetIntegration(ctx, id); err != nil {
			t.Errorf("%s was deleted anyway: %v", id, err)
		}
	}
	if err := s.DeleteIntegration(ctx, "free"); err != nil {
		t.Errorf("unreferenced delete = %v", err)
	}
}

func TestProjectsScopeAndConflicts(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	seedIntegration(t, s, "i1", "u1")
	seedIntegration(t, s, "i2", "u2")

	if _, err := s.GetProject(ctx, "nope"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("GetProject missing = %v", err)
	}
	if _, err := s.GetProjectByName(ctx, "", "nope"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("GetProjectByName missing = %v", err)
	}

	a := mkProject("p1", "web", "u1", "i1")
	a.Repos = []confstore.ExtraRepo{{Name: "api"}}
	seedProject(t, s, a)
	b := mkProject("p2", "web", "u2", "i2") // two people may each have a "web"
	b.CreatedAt = testNow.Add(time.Minute)
	seedProject(t, s, b)
	// The same owner may not.
	if err := s.UpsertProject(ctx, mkProject("p3", "web", "u1", "i1")); !errors.Is(err, confstore.ErrConflict) {
		t.Errorf("same-owner duplicate = %v", err)
	}
	// A project on an integration that vanished is invalid, not a crash.
	if err := s.UpsertProject(ctx, mkProject("p4", "ghost", "u1", "gone")); !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("missing integration = %v", err)
	}

	for user, want := range map[string]string{"u1": "p1", "u2": "p2", "": "p1"} {
		got, err := s.GetProjectByName(ctx, user, "web")
		if err != nil || got.ID != want {
			t.Errorf("by name as %q = %s, %v; want %s", user, got.ID, err, want)
		}
	}
	if _, err := s.GetProjectByName(ctx, "u3", "web"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("stranger by name = %v", err)
	}
	mine, err := s.ListProjects(ctx, "u2")
	if err != nil || len(mine) != 1 || mine[0].ID != "p2" {
		t.Errorf("ListProjects u2 = %+v, %v", mine, err)
	}
	all, err := s.ListProjects(ctx, "")
	if err != nil || len(all) != 2 || all[0].ID != "p1" {
		t.Errorf("ListProjects all = %+v, %v", all, err)
	}
	if len(all[0].Repos) != 1 || all[0].Repos[0].Name != "api" || all[1].Repos != nil && len(all[1].Repos) != 0 {
		t.Errorf("extra repos = %+v / %+v", all[0].Repos, all[1].Repos)
	}

	// The owner is set at insert, never by an update.
	a.UserID, a.Name = "u2", "web2"
	seedProject(t, s, a)
	if got, _ := s.GetProject(ctx, "p1"); got.UserID != "u1" || got.Name != "web2" {
		t.Errorf("after update = %+v", got)
	}

	if err := s.DeleteProject(ctx, "nope"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("delete missing = %v", err)
	}
}

// A project's default flow must be its own or a built-in: another project's
// flow of the same name is a different document.
func TestProjectCannotBorrowAnotherProjectsFlow(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	seedIntegration(t, s, "i1", "u1")
	seedProject(t, s, mkProject("p1", "a", "u1", "i1"))
	seedProject(t, s, mkProject("p2", "b", "u1", "i1"))
	if err := s.UpsertFlow(ctx, confstore.FlowRow{ProjectID: "p1", Name: "mine", YAML: "y", Hash: "h", UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	p := mkProject("p2", "b", "u1", "i1")
	p.DefaultFlow = "mine"
	if err := s.UpsertProject(ctx, p); !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("borrowed flow = %v, want ErrInvalid", err)
	}
	if got, _ := s.GetProject(ctx, "p2"); got.DefaultFlow != "builder" {
		t.Errorf("refused update wrote: %+v", got)
	}
}

// A ticket belongs to its source, not to a workshop: one record per
// (source, remote id) across owners, found by remote id, its last-seen
// columns rewritten in place.
func TestTicketsScopeAndLifecycle(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	seedIntegration(t, s, "i1", "u1")
	seedIntegration(t, s, "i2", "u2")

	if _, err := s.GetTicket(ctx, "nope"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
	mk := func(id, user, remote string, at time.Time) confstore.Ticket {
		return confstore.Ticket{ID: id, UserID: user, Title: "T " + id, Goal: "g", BaseBranch: "b",
			SourceID: "src", SourceKind: "jira", SourceKey: "K-" + id, SourceURL: "https://t.local/K-" + id, RemoteID: remote,
			CreatedAt: at, UpdatedAt: at}
	}
	for _, tk := range []confstore.Ticket{mk("t1", "u1", "100", testNow), mk("t2", "u1", "200", testNow.Add(time.Hour)), mk("t3", "u2", "300", testNow)} {
		if err := s.UpsertTicket(ctx, tk, confstore.Change{Kind: confstore.AuthorUser}); err != nil {
			t.Fatal(err)
		}
	}
	// The same issue is one record, whoever records it second.
	if err := s.UpsertTicket(ctx, mk("t4", "u2", "100", testNow), confstore.Change{Kind: confstore.AuthorUser}); !errors.Is(err, confstore.ErrConflict) {
		t.Errorf("second record of an issue = %v, want ErrConflict", err)
	}
	bad := mk("t5", "u1", "500", testNow)
	bad.StatusID = "ghost"
	if err := s.UpsertTicket(ctx, bad, confstore.Change{Kind: confstore.AuthorUser}); !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("ticket on a missing status = %v, want ErrInvalid", err)
	}
	got, err := s.GetTicket(ctx, "t1")
	if err != nil || got.SourceKey != "K-t1" || got.SourceKind != "jira" || got.BaseBranch != "b" || got.Goal != "g" || got.RemoteID != "100" {
		t.Fatalf("roundtrip = %+v, %v", got, err)
	}
	if got, err := s.GetTicketByRemote(ctx, "src", "200"); err != nil || got.ID != "t2" {
		t.Errorf("by remote = %+v, %v", got, err)
	}
	if _, err := s.GetTicketByRemote(ctx, "other", "200"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("by remote on another source = %v", err)
	}
	page, err := s.ListTicketsByRemote(ctx, "src", []string{"100", "300", "999"})
	ids := []string{}
	for _, tk := range page {
		ids = append(ids, tk.ID)
	}
	slices.Sort(ids)
	if err != nil || !slices.Equal(ids, []string{"t1", "t3"}) {
		t.Errorf("by remote ids = %v, %v", ids, err)
	}
	if none, err := s.ListTicketsByRemote(ctx, "src", nil); err != nil || len(none) != 0 {
		t.Errorf("no remote ids = %+v, %v", none, err)
	}

	// What was last seen of the issue: the key moved, the type went.
	at := testNow.Add(2 * time.Hour)
	seen := confstore.TicketSeen{RemoteID: "100", Key: "NEW-1", URL: "https://t.local/NEW-1", Title: "Renamed", Goal: "g2",
		Type: "Bug", Labels: []string{"x"}}
	if err := s.SetTicketSeen(ctx, "t1", seen, "", trackerport.ChangeFix, at, confstore.Change{Kind: confstore.AuthorSync}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTicket(ctx, "t1")
	if got.SourceKey != "NEW-1" || got.SourceURL != seen.URL || got.Title != "Renamed" || got.Goal != "g2" || got.SourceType != "Bug" ||
		got.TypeID != "" || got.ChangeType != trackerport.ChangeFix || !slices.Equal(got.Labels, []string{"x"}) ||
		!got.UpdatedAt.Equal(at) || got.RemoteID != "100" || got.BaseBranch != "b" {
		t.Errorf("after seen = %+v", got)
	}
	if err := s.SetTicketSeen(ctx, "ghost", seen, "", trackerport.ChangeFix, at, confstore.Change{Kind: confstore.AuthorSync}); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("seen of a missing ticket = %v", err)
	}
	if err := s.SetTicketSeen(ctx, "t1", seen, "ghost", trackerport.ChangeFix, at, confstore.Change{Kind: confstore.AuthorSync}); !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("seen with a missing type = %v", err)
	}

	if err := s.DeleteTicket(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTicket(ctx, "t1"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("second delete = %v", err)
	}
}

// The internal tracker's list is read from Postgres: newest first, a stable
// (created_at, id) cursor, a case-insensitive substring of title, goal or id
// (LIKE's wildcards taken literally), and done by the status's is_done (no
// status is open).
func TestListInternalTickets(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	if err := s.UpsertIntegration(ctx, confstore.StoredIntegration{ID: "in1", UserID: "u1", Port: confstore.PortTicketSource,
		Kind: confstore.KindInternal, Name: "Apothecary tickets", Config: map[string]any{}, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	for _, st := range []confstore.TrackerStatus{
		{ID: "in1-todo", IntegrationID: "in1", RemoteID: "in1-todo", Name: "To do"},
		{ID: "in1-done", IntegrationID: "in1", RemoteID: "in1-done", Name: "Done", Position: 1, IsDone: true},
	} {
		if _, err := s.EnsureTrackerStatus(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(id, title, status string, minute int) confstore.Ticket {
		at := testNow.Add(time.Duration(minute) * time.Minute)
		return confstore.Ticket{ID: id, UserID: "u1", Title: title, SourceID: "in1", SourceKind: confstore.KindInternal, RemoteID: id,
			StatusID: status, CreatedAt: at, UpdatedAt: at}
	}
	for _, tk := range []confstore.Ticket{
		mk("a", "Fix login", "in1-todo", 0),
		mk("b", "100% LOGIN", "in1-done", 1),
		mk("c", "Docs", "", 2),
		mk("d", "Logout", "in1-todo", 2), // same instant as c: id breaks the tie
		{ID: "r", UserID: "u1", Title: "remote login", SourceID: "jira", SourceKind: "jira", RemoteID: "1", CreatedAt: testNow, UpdatedAt: testNow},
	} {
		if err := s.UpsertTicket(ctx, tk, confstore.Change{Kind: confstore.AuthorUser}); err != nil {
			t.Fatal(err)
		}
	}
	list := func(q confstore.InternalTicketQuery) []string {
		t.Helper()
		got, err := s.ListInternalTickets(ctx, "in1", q)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, tk := range got {
			ids = append(ids, tk.ID)
		}
		return ids
	}
	for _, c := range []struct {
		name string
		q    confstore.InternalTicketQuery
		want []string
	}{
		{"all, newest first", confstore.InternalTicketQuery{}, []string{"d", "c", "b", "a"}},
		{"open", confstore.InternalTicketQuery{Done: trackerport.DoneOpen}, []string{"d", "c", "a"}},
		{"done", confstore.InternalTicketQuery{Done: trackerport.DoneOnly}, []string{"b"}},
		{"text, any case", confstore.InternalTicketQuery{Text: "login"}, []string{"b", "a"}},
		{"wildcards are literal", confstore.InternalTicketQuery{Text: "0%"}, []string{"b"}},
		{"underscore is literal", confstore.InternalTicketQuery{Text: "_"}, []string{}},
		{"by id", confstore.InternalTicketQuery{Text: "D"}, []string{"d", "c"}},
		{"page", confstore.InternalTicketQuery{Limit: 2}, []string{"d", "c"}},
		{"after the page", confstore.InternalTicketQuery{Limit: 2,
			After: &confstore.TicketCursor{CreatedAt: testNow.Add(2 * time.Minute), ID: "c"}}, []string{"b", "a"}},
		{"after the tie", confstore.InternalTicketQuery{
			After: &confstore.TicketCursor{CreatedAt: testNow.Add(2 * time.Minute), ID: "d"}}, []string{"c", "b", "a"}},
	} {
		if got := list(c.q); !slices.Equal(got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFlowsLayoutAndHistory(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	seedIntegration(t, s, "i1", "u1")
	seedProject(t, s, mkProject("p1", "a", "u1", "i1"))
	seedProject(t, s, mkProject("p2", "b", "u1", "i1"))

	if _, err := s.GetFlow(ctx, "p1", "nope"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("missing flow = %v", err)
	}
	layout := []byte(`{"nodes": []}`)
	if err := s.UpsertFlow(ctx, confstore.FlowRow{ProjectID: "p1", Name: "f", YAML: "one", Hash: "h1", Layout: layout, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	// A YAML-tab edit carries no layout and must not wipe the node positions.
	if err := s.UpsertFlow(ctx, confstore.FlowRow{ProjectID: "p1", Name: "f", YAML: "two", Hash: "h2", UpdatedAt: testNow.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFlow(ctx, "p1", "f")
	if err != nil || got.YAML != "two" || got.Hash != "h2" || !strings.Contains(string(got.Layout), "nodes") {
		t.Fatalf("after layoutless save = %+v (%s), %v", got, got.Layout, err)
	}
	// A new layout replaces the old one.
	if err := s.UpsertFlow(ctx, confstore.FlowRow{ProjectID: "p1", Name: "f", YAML: "two", Hash: "h2", Layout: []byte(`{"moved": true}`), UpdatedAt: testNow.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetFlow(ctx, "p1", "f"); !strings.Contains(string(got.Layout), "moved") {
		t.Errorf("layout = %s", got.Layout)
	}

	// History is keyed by content, once per project: resaving a hash is a no-op,
	// and it keeps the first save's time.
	versions, err := s.ListFlowVersions(ctx, "p1", "f")
	if err != nil || len(versions) != 2 || versions[0].Hash != "h2" || versions[1].Hash != "h1" {
		t.Fatalf("versions = %+v, %v (newest first)", versions, err)
	}
	if !versions[0].CreatedAt.Equal(testNow.Add(time.Minute)) {
		t.Errorf("resave moved the version: %v", versions[0].CreatedAt)
	}
	if v, err := s.GetFlowVersion(ctx, "p1", "h1"); err != nil || v.YAML != "one" {
		t.Errorf("version h1 = %+v, %v", v, err)
	}
	if _, err := s.GetFlowVersion(ctx, "p2", "h1"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("another project's version = %v", err)
	}

	if err := s.UpsertFlow(ctx, confstore.FlowRow{ProjectID: "p2", Name: "g", YAML: "x", Hash: "h9", UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertFlow(ctx, confstore.FlowRow{ProjectID: "p2", Name: "a", YAML: "x", Hash: "h8", UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertFlow(ctx, confstore.FlowRow{ProjectID: "ghost", Name: "a", YAML: "x", Hash: "h7", UpdatedAt: testNow}); err == nil {
		t.Error("flow of a missing project stored")
	}
	two, err := s.ListFlows(ctx, "p2")
	if err != nil || len(two) != 2 || two[0].Name != "a" || two[1].Name != "g" {
		t.Errorf("ListFlows p2 = %+v, %v", two, err)
	}
	all, err := s.ListFlows(ctx, "")
	if err != nil || len(all) != 3 || all[0].ProjectID != "p1" || len(all[0].Layout) == 0 || len(all[1].Layout) != 0 {
		t.Errorf("ListFlows all = %+v, %v", all, err)
	}
}

// DeleteFlow's reference check: this project's default, this project's live
// runs, and nothing else.
func TestDeleteFlowReferences(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	seedIntegration(t, s, "i1", "u1")
	seedProject(t, s, mkProject("p1", "a", "u1", "i1"))
	seedProject(t, s, mkProject("p2", "b", "u1", "i1"))
	for _, pid := range []string{"p1", "p2"} {
		if err := s.UpsertFlow(ctx, confstore.FlowRow{ProjectID: pid, Name: "f", YAML: "y", Hash: "h-" + pid, UpdatedAt: testNow}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteFlow(ctx, "p1", "nope"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("missing flow = %v", err)
	}
	// p1 has a live run on f: refused, naming it.
	if _, err := s.Append(ctx, "r1", &event.RunCreated{TicketTitle: "t", FlowName: "f", ProjectID: "p1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFlow(ctx, "p1", "f"); !errors.Is(err, confstore.ErrReferenced) || !strings.Contains(err.Error(), "run r1") {
		t.Fatalf("live run = %v", err)
	}
	// p2's flow of the same name is a different document.
	if err := s.DeleteFlow(ctx, "p2", "f"); err != nil {
		t.Errorf("another project's run blocked the delete: %v", err)
	}
	// A finished run no longer pins it.
	if _, err := s.Append(ctx, "r1", &event.RunFailed{Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFlow(ctx, "p1", "f"); err != nil {
		t.Errorf("finished run blocked the delete: %v", err)
	}
}

func TestSettingsPerUser(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	if got, err := s.GetSettings(ctx, "u1"); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty settings = %v, %v (want an empty map)", got, err)
	}
	if err := s.PutSetting(ctx, "u1", "k", `"a"`); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSetting(ctx, "u1", "k", `"b"`); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSetting(ctx, "u2", "k", `"c"`); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSetting(ctx, "u1", "bad", `not json`); err == nil {
		t.Error("a non-JSON value stored in a jsonb column")
	}
	if got, _ := s.GetSettings(ctx, "u1"); len(got) != 1 || got["k"] != `"b"` {
		t.Errorf("u1 = %v", got)
	}
	if got, _ := s.GetSettings(ctx, "u2"); got["k"] != `"c"` {
		t.Errorf("u2 = %v", got)
	}
}

// The reaper reads settings for everyone who has them AND everyone who owns
// a run or an upload, once each.
func TestSettingsOwnersCoversRunsAndUploads(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	if err := s.PutSetting(ctx, "by-setting", "k", `1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "r1", &event.RunCreated{UserID: "by-run"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "r2", &event.RunCreated{UserID: "by-setting"}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertAttachment(ctx, mkAttachment("a1", "by-upload", testNow)); err != nil {
		t.Fatal(err)
	}
	got, err := s.SettingsOwners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, id := range got {
		seen[id]++
	}
	if len(seen) != 3 || seen["by-setting"] != 1 || seen["by-run"] != 1 || seen["by-upload"] != 1 {
		t.Errorf("owners = %v", got)
	}
}

func TestCredentialsListAndMisses(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	if _, err := s.GetCredential(ctx, "nope"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
	if err := s.DeleteCredential(ctx, "nope"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("delete missing = %v", err)
	}
	mk := func(id, user string, at time.Time) confstore.ProviderCredential {
		return confstore.ProviderCredential{ID: id, UserID: user, Provider: "acme", Kind: "api_key", Name: id, CreatedAt: at, UpdatedAt: at}
	}
	for _, c := range []confstore.ProviderCredential{mk("c1", "u1", testNow.Add(time.Second)), mk("c2", "u2", testNow)} {
		if err := s.UpsertCredential(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ListCredentials(ctx, "")
	if err != nil || len(all) != 2 || all[0].ID != "c2" || all[1].ID != "c1" {
		t.Errorf("all = %+v, %v", all, err)
	}
	// nil sealed stores as empty, not NULL: the column is NOT NULL.
	if got, err := s.GetCredential(ctx, "c1"); err != nil || len(got.Sealed) != 0 {
		t.Errorf("c1 = %+v, %v", got, err)
	}
	// One owner cannot read another's rows through the list.
	if mine, _ := s.ListCredentials(ctx, "u1"); len(mine) != 1 || mine[0].ID != "c1" {
		t.Errorf("u1 = %+v", mine)
	}
}

func TestFlowBindingAndAutomationEdges(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	if err := s.PutFlowBinding(ctx, confstore.FlowBinding{ProjectID: "ghost", FlowName: "f", Params: map[string]string{}, UpdatedAt: testNow}); err == nil {
		t.Error("binding of a missing project stored")
	}
	if err := s.UpsertAutomation(ctx, confstore.Automation{ID: "a1", ProjectID: "ghost", Name: "n", Kind: confstore.AutomationCron, CreatedAt: testNow, UpdatedAt: testNow}); !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("automation of a missing project = %v", err)
	}
	if _, err := s.GetAutomation(ctx, "a1"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("refused automation = %v", err)
	}
	if list, err := s.ListFlowBindings(ctx, "ghost"); err != nil || len(list) != 0 {
		t.Errorf("bindings of nothing = %+v, %v", list, err)
	}
}

func errNotFound() error   { return confstore.ErrNotFound }
func errInvalid() error    { return confstore.ErrInvalid }
func errReferenced() error { return confstore.ErrReferenced }
