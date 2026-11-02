package pgstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"apothecary/internal/account/auth"
	"apothecary/internal/adapters/pgstore/pgtest"
	"apothecary/internal/analytics"
	"apothecary/internal/engine/ask"
	"apothecary/internal/notices"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/egressport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/skills/skillstore"
)

// A database that is down is an error, never an answer: no read may turn it
// into "not found" (which the API would serve as a 404 and the engine would
// act on), and no write may report success.
func TestDeadDatabaseIsAnErrorEverywhere(t *testing.T) {
	s := open(t)
	s.Close()
	ctx := context.Background()
	now := testNow
	calls := map[string]func() error{
		"Ping":               func() error { return s.Ping(ctx) },
		"Append":             func() error { _, err := s.Append(ctx, "r", &event.RunCreated{}); return err },
		"Events":             func() error { _, err := s.Events(ctx, "r", 0); return err },
		"RunIDs":             func() error { _, err := s.RunIDs(ctx); return err },
		"RunView":            func() error { _, _, err := s.RunView(ctx, "r"); return err },
		"RunByEnv":           func() error { _, _, err := s.RunByEnv(ctx, "e"); return err },
		"RunViews":           func() error { _, err := s.RunViews(ctx); return err },
		"ListRunViews":       func() error { _, err := s.ListRunViews(ctx, run.ListFilter{UserID: "u"}); return err },
		"Rebuild":            func() error { _, err := s.RebuildProjections(ctx); return err },
		"SaveChannel":        func() error { return s.SaveChannel(ctx, "c", "", "k", nil) },
		"DeleteChannel":      func() error { return s.DeleteChannel(ctx, "c") },
		"LoadChannels":       func() error { _, err := s.LoadChannels(ctx); return err },
		"SaveExposedPort":    func() error { return s.SaveExposedPort(ctx, "r", 1, 2) },
		"DeleteExposedPorts": func() error { return s.DeleteExposedPorts(ctx, "r") },
		"LoadExposedPorts":   func() error { _, err := s.LoadExposedPorts(ctx); return err },
		"ListPings":          func() error { _, err := s.ListPings(ctx, "", "", 0); return err },
		"GetPing":            func() error { _, err := s.GetPing(ctx, "p"); return err },
		"LoadLimits":         func() error { _, _, _, err := s.LoadLimits(ctx); return err },
		"SaveLimits":         func() error { return s.SaveLimits(ctx, 1, 1) },
		"AskThread":          func() error { _, _, err := s.AskThread(ctx, "r"); return err },
		"SaveAskThread":      func() error { return s.SaveAskThread(ctx, ask.Thread{RunID: "r"}) },
		"AppendAskMessage":   func() error { _, err := s.AppendAskMessage(ctx, ask.Message{RunID: "r"}); return err },
		"AskMessages":        func() error { _, err := s.AskMessages(ctx, "r", 0); return err },
		"UpsertEgressAudit": func() error {
			return s.UpsertEgressAudit(ctx, []egressport.EgressAuditRow{{Run: "r", Host: "h", First: now, Last: now}})
		},
		"QueryEgressAudit":       func() error { _, err := s.QueryEgressAudit(ctx, egressport.EgressAuditFilter{}); return err },
		"UpsertIntegration":      func() error { return s.UpsertIntegration(ctx, confstore.StoredIntegration{ID: "i"}) },
		"GetIntegration":         func() error { _, err := s.GetIntegration(ctx, "i"); return err },
		"ListIntegrations":       func() error { _, err := s.ListIntegrations(ctx, "u"); return err },
		"DeleteIntegration":      func() error { return s.DeleteIntegration(ctx, "i") },
		"UpsertProject":          func() error { return s.UpsertProject(ctx, confstore.Project{ID: "p"}) },
		"GetProject":             func() error { _, err := s.GetProject(ctx, "p"); return err },
		"GetProjectByName":       func() error { _, err := s.GetProjectByName(ctx, "u", "p"); return err },
		"ListProjects":           func() error { _, err := s.ListProjects(ctx, "u"); return err },
		"DeleteProject":          func() error { return s.DeleteProject(ctx, "p") },
		"PutProjectSecret":       func() error { return s.PutProjectSecret(ctx, "p", confstore.StoredProjectSecret{}) },
		"ListProjectSecrets":     func() error { _, err := s.ListProjectSecrets(ctx, "p"); return err },
		"DeleteProjectSecret":    func() error { return s.DeleteProjectSecret(ctx, "p", "n") },
		"UpsertTicket":           func() error { return s.UpsertTicket(ctx, confstore.Ticket{ID: "t"}) },
		"GetTicket":              func() error { _, err := s.GetTicket(ctx, "t"); return err },
		"ListTickets":            func() error { _, err := s.ListTickets(ctx, "u"); return err },
		"DeleteTicket":           func() error { return s.DeleteTicket(ctx, "t") },
		"UpsertFlow":             func() error { return s.UpsertFlow(ctx, confstore.FlowRow{}) },
		"GetFlow":                func() error { _, err := s.GetFlow(ctx, "p", "f"); return err },
		"ListFlows":              func() error { _, err := s.ListFlows(ctx, "p"); return err },
		"DeleteFlow":             func() error { return s.DeleteFlow(ctx, "p", "f") },
		"PutFlowBinding":         func() error { return s.PutFlowBinding(ctx, confstore.FlowBinding{}) },
		"GetFlowBinding":         func() error { _, err := s.GetFlowBinding(ctx, "p", "f"); return err },
		"ListFlowBindings":       func() error { _, err := s.ListFlowBindings(ctx, "p"); return err },
		"DeleteFlowBinding":      func() error { return s.DeleteFlowBinding(ctx, "p", "f") },
		"UpsertAutomation":       func() error { return s.UpsertAutomation(ctx, confstore.Automation{ID: "a"}) },
		"GetAutomation":          func() error { _, err := s.GetAutomation(ctx, "a"); return err },
		"ListAutomations":        func() error { _, err := s.ListAutomations(ctx, "p"); return err },
		"DeleteAutomation":       func() error { return s.DeleteAutomation(ctx, "a") },
		"GetFlowVersion":         func() error { _, err := s.GetFlowVersion(ctx, "p", "h"); return err },
		"ListFlowVersions":       func() error { _, err := s.ListFlowVersions(ctx, "p", "f"); return err },
		"GetSettings":            func() error { _, err := s.GetSettings(ctx, "u"); return err },
		"PutSetting":             func() error { return s.PutSetting(ctx, "u", "k", "1") },
		"SettingsOwners":         func() error { _, err := s.SettingsOwners(ctx); return err },
		"UpsertCredential":       func() error { return s.UpsertCredential(ctx, confstore.ProviderCredential{ID: "c"}) },
		"GetCredential":          func() error { _, err := s.GetCredential(ctx, "c"); return err },
		"ListCredentials":        func() error { _, err := s.ListCredentials(ctx, "u"); return err },
		"DeleteCredential":       func() error { return s.DeleteCredential(ctx, "c") },
		"InsertAttachment":       func() error { return s.InsertAttachment(ctx, filestore.Attachment{ID: "a"}) },
		"GetAttachment":          func() error { _, err := s.GetAttachment(ctx, "a"); return err },
		"ClaimAttachments":       func() error { return s.ClaimAttachments(ctx, "", []string{"a"}, "", "r", "") },
		"AdoptRemoteAttachment":  func() error { _, err := s.AdoptRemoteAttachment(ctx, "t", "a", "r"); return err },
		"AttachmentsForRun":      func() error { _, err := s.AttachmentsForRun(ctx, "r"); return err },
		"DeleteForRun":           func() error { return s.DeleteAttachmentsForRun(ctx, "r") },
		"DeleteForTicket":        func() error { return s.DeleteAttachmentsForTicket(ctx, "t") },
		"Unclaimed":              func() error { _, err := s.UnclaimedAttachmentIDsBefore(ctx, "", now); return err },
		"DeleteAttachments":      func() error { return s.DeleteAttachments(ctx, []string{"a"}) },
		"InsertSnapshot":         func() error { return s.InsertSnapshot(ctx, filestore.Snapshot{ID: "s"}) },
		"GetSnapshot":            func() error { _, err := s.GetSnapshot(ctx, "s"); return err },
		"SnapshotsForRun":        func() error { _, err := s.SnapshotsForRun(ctx, "r"); return err },
		"LiveBlobSHAs":           func() error { _, err := s.LiveBlobSHAs(ctx, nil); return err },
		"UpsertSkill":            func() error { return s.UpsertSkill(ctx, skillstore.Skill{ID: "s"}) },
		"GetSkill":               func() error { _, err := s.GetSkill(ctx, "s"); return err },
		"GetSkillByName":         func() error { _, err := s.GetSkillByName(ctx, "u", "n"); return err },
		"GetSkillBySource":       func() error { _, err := s.GetSkillBySource(ctx, "u", "x"); return err },
		"ListSkills":             func() error { _, err := s.ListSkills(ctx, "u"); return err },
		"DeleteSkill":            func() error { return s.DeleteSkill(ctx, "s") },
		"UpsertSkillset":         func() error { return s.UpsertSkillset(ctx, skillstore.Skillset{ID: "s"}) },
		"GetSkillset":            func() error { _, err := s.GetSkillset(ctx, "s"); return err },
		"ListSkillsets":          func() error { _, err := s.ListSkillsets(ctx, "u"); return err },
		"DeleteSkillset":         func() error { return s.DeleteSkillset(ctx, "s") },
		"InsertVersion":          func() error { return s.InsertVersion(ctx, skillstore.Version{SkillsetID: "s"}) },
		"GetVersion":             func() error { _, err := s.GetVersion(ctx, "s", 1); return err },
		"ListVersions":           func() error { _, err := s.ListVersions(ctx, "s"); return err },
		"CountUsers":             func() error { _, err := s.CountUsers(ctx); return err },
		"CreateUser":             func() error { return s.CreateUser(ctx, auth.User{ID: "u"}) },
		"UpdateUser":             func() error { return s.UpdateUser(ctx, auth.User{ID: "u"}) },
		"GetUser":                func() error { _, err := s.GetUser(ctx, "u"); return err },
		"GetUserByLogin":         func() error { _, err := s.GetUserByLogin(ctx, "u"); return err },
		"ListUsers":              func() error { _, err := s.ListUsers(ctx); return err },
		"TouchUser":              func() error { return s.TouchUser(ctx, "u", now) },
		"PutMethod":              func() error { return s.PutMethod(ctx, auth.Method{ID: "m"}) },
		"GetMethod":              func() error { _, err := s.GetMethod(ctx, "u", "k"); return err },
		"FindMethodBySubject":    func() error { _, err := s.FindMethodBySubject(ctx, "k", "s"); return err },
		"ListMethods":            func() error { _, err := s.ListMethods(ctx, "u"); return err },
		"DeleteMethod":           func() error { return s.DeleteMethod(ctx, "m") },
		"CreateSession":          func() error { return s.CreateSession(ctx, auth.Session{ID: "s"}) },
		"GetSession":             func() error { _, err := s.GetSession(ctx, "s"); return err },
		"TouchSession":           func() error { return s.TouchSession(ctx, "s", now, now) },
		"DeleteSession":          func() error { return s.DeleteSession(ctx, "s") },
		"DeleteSessionsOf":       func() error { return s.DeleteSessionsOf(ctx, "u") },
		"CreateToken":            func() error { return s.CreateToken(ctx, auth.Token{ID: "t"}) },
		"GetTokenByHash":         func() error { _, err := s.GetTokenByHash(ctx, "h"); return err },
		"ListTokens":             func() error { _, err := s.ListTokens(ctx, "u"); return err },
		"DeleteToken":            func() error { return s.DeleteToken(ctx, "u", "t") },
		"TouchToken":             func() error { return s.TouchToken(ctx, "t", now) },
		"DeleteTokensOf":         func() error { return s.DeleteTokensOf(ctx, "u") },
		"CreateSSHKey":           func() error { return s.CreateSSHKey(ctx, auth.SSHKey{ID: "k"}) },
		"GetSSHKeyByFingerprint": func() error { _, err := s.GetSSHKeyByFingerprint(ctx, "f"); return err },
		"ListSSHKeys":            func() error { _, err := s.ListSSHKeys(ctx, "u"); return err },
		"DeleteSSHKey":           func() error { return s.DeleteSSHKey(ctx, "u", "k") },
		"TouchSSHKey":            func() error { return s.TouchSSHKey(ctx, "k", now) },
		"RunQuery": func() error {
			_, err := s.RunQuery(ctx, analytics.Compiled{SQL: "SELECT count(*)"})
			return err
		},
		"AnalyticsDrill":    func() error { _, err := s.Drill(ctx, analytics.Compiled{SQL: "SELECT 1"}); return err },
		"AnalyticsValues":   func() error { _, err := s.Values(ctx, analytics.Compiled{SQL: "SELECT ''"}); return err },
		"FlowVersionLabels": func() error { _, err := s.FlowVersionLabels(ctx, "u", []string{"h"}); return err },
		"ProjectNames":      func() error { _, err := s.ProjectNames(ctx, "u", []string{"p"}); return err },
		"ListDashboards":    func() error { _, err := s.ListDashboards(ctx, "u"); return err },
		"GetDashboard":      func() error { _, err := s.GetDashboard(ctx, "d"); return err },
		"PutDashboard":      func() error { return s.PutDashboard(ctx, analytics.Dashboard{ID: "d"}) },
		"DeleteDashboard":   func() error { return s.DeleteDashboard(ctx, "d") },
		"ListScorecards":    func() error { _, err := s.ListScorecards(ctx, "u"); return err },
		"GetScorecard":      func() error { _, err := s.GetScorecard(ctx, "s"); return err },
		"PutScorecard":      func() error { return s.PutScorecard(ctx, analytics.Scorecard{ID: "s"}) },
		"DeleteScorecard":   func() error { return s.DeleteScorecard(ctx, "s") },
		"RaiseNotice":       func() error { _, _, err := s.RaiseNotice(ctx, notices.Notice{ID: "n"}); return err },
		"ClearNotices":      func() error { return s.ClearNotices(ctx, "k", testNow) },
		"ListNotices":       func() error { _, err := s.ListNotices(ctx, "u"); return err },
		"GetNotice":         func() error { _, err := s.GetNotice(ctx, "n"); return err },
		"DismissNotice":     func() error { return s.DismissNotice(ctx, "n", testNow) },
		"DismissAllNotices": func() error { return s.DismissAllNotices(ctx, "u", testNow) },
	}
	for name, call := range calls {
		err := call()
		if err == nil {
			t.Errorf("%s: no error from a closed pool", name)
			continue
		}
		for _, miss := range []error{confstore.ErrNotFound, auth.ErrNotFound, filestore.ErrNotFound, ErrPingNotFound, analytics.ErrNotFound, notices.ErrNotFound} {
			if errors.Is(err, miss) {
				t.Errorf("%s: a dead database read as %v", name, err)
			}
		}
	}
	if _, err := s.Watch(ctx, "r", 0); err == nil {
		t.Error("Watch: no error from a closed pool")
	}
}

// A row whose JSON does not fit its type is reported with the row's name
// (or at least as an error), never returned half-decoded.
func TestMalformedRowsAreErrors(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	seedIntegration(t, s, "i1", "u1")
	seedProject(t, s, mkProject("p1", "a", "u1", "i1"))
	if _, err := s.Append(ctx, "r1", &event.RunCreated{}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannel(ctx, "c1", "u1", "k", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFlowBinding(ctx, confstore.FlowBinding{ProjectID: "p1", FlowName: "f", Params: map[string]string{}, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAutomation(ctx, confstore.Automation{ID: "a1", ProjectID: "p1", Name: "n", Kind: confstore.AutomationCron, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCredential(ctx, confstore.ProviderCredential{ID: "c1", UserID: "u1", Provider: "acme", Kind: "api_key", Name: "n", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSkill(ctx, skillstore.Skill{ID: "s1", UserID: "u1", Name: "n", Hash: "h", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSkillset(ctx, skillstore.Skillset{ID: "set1", UserID: "u1", Name: "n", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertVersion(ctx, skillstore.Version{SkillsetID: "set1", Seq: 1, Hash: "h", CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDashboard(ctx, analytics.Dashboard{ID: "d1", UserID: "u1", Name: "n", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutScorecard(ctx, analytics.Scorecard{ID: "sc1", UserID: "u1", Name: "n", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}

	list := func(err error) error { return err }
	cases := []struct {
		name, breakSQL, fixSQL, want string
		calls                        []func() error
	}{
		{"integration config", `UPDATE integrations SET config='[]'`, `UPDATE integrations SET config='{}'`, "config", []func() error{
			func() error { _, err := s.GetIntegration(ctx, "i1"); return err },
			func() error { _, err := s.ListIntegrations(ctx, ""); return err },
		}},
		{"project repo", `UPDATE projects SET repo='[]'`, `UPDATE projects SET repo='{}'`, "", []func() error{
			func() error { _, err := s.GetProject(ctx, "p1"); return err },
			func() error { _, err := s.ListProjects(ctx, ""); return list(err) },
		}},
		{"project repos", `UPDATE projects SET repos='{}'`, `UPDATE projects SET repos='[]'`, "", []func() error{
			func() error { _, err := s.GetProject(ctx, "p1"); return err },
		}},
		{"project commands", `UPDATE projects SET commands='[]'`, `UPDATE projects SET commands='{}'`, "", []func() error{
			func() error { _, err := s.GetProject(ctx, "p1"); return err },
		}},
		{"project workspace", `UPDATE projects SET workspace='[]'`, `UPDATE projects SET workspace='{}'`, "", []func() error{
			func() error { _, err := s.GetProject(ctx, "p1"); return err },
		}},
		{"project harnesses", `UPDATE projects SET harnesses='{}'`, `UPDATE projects SET harnesses='[]'`, "", []func() error{
			func() error { _, err := s.GetProject(ctx, "p1"); return err },
		}},
		{"flow binding params", `UPDATE flow_bindings SET params='[]'`, `UPDATE flow_bindings SET params='{}'`, "", []func() error{
			func() error { _, err := s.GetFlowBinding(ctx, "p1", "f"); return err },
			func() error { _, err := s.ListFlowBindings(ctx, "p1"); return err },
		}},
		{"automation params", `UPDATE automations SET params='[]'`, `UPDATE automations SET params='{}'`, "", []func() error{
			func() error { _, err := s.GetAutomation(ctx, "a1"); return err },
			func() error { _, err := s.ListAutomations(ctx, "p1"); return err },
		}},
		{"credential meta", `UPDATE provider_credentials SET meta='[]'`, `UPDATE provider_credentials SET meta='{}'`, "meta", []func() error{
			func() error { _, err := s.GetCredential(ctx, "c1"); return err },
			func() error { _, err := s.ListCredentials(ctx, ""); return err },
		}},
		{"run view", `UPDATE runs SET view='[]'`, `UPDATE runs SET view='{}'`, "decode runs.view", []func() error{
			func() error { _, _, err := s.RunView(ctx, "r1"); return err },
			func() error { _, err := s.RunViews(ctx); return err },
			func() error { _, err := s.Append(ctx, "r1", &event.EnvRequested{}); return err },
		}},
		{"channel config", `UPDATE channels SET config='[]'`, `UPDATE channels SET config='{}'`, "", []func() error{
			func() error { _, err := s.LoadChannels(ctx); return err },
		}},
		{"event payload", `UPDATE events SET payload='[]'`, `UPDATE events SET payload='{}'`, "run r1 seq 1", []func() error{
			func() error { _, err := s.Events(ctx, "r1", 0); return err },
		}},
		{"skill files", `UPDATE skills SET files='{}'`, `UPDATE skills SET files='[]'`, "files", []func() error{
			func() error { _, err := s.GetSkill(ctx, "s1"); return err },
			func() error { _, err := s.ListSkills(ctx, ""); return err },
		}},
		{"skill source", `UPDATE skills SET source='[]'`, `UPDATE skills SET source=NULL`, "source", []func() error{
			func() error { _, err := s.GetSkill(ctx, "s1"); return err },
		}},
		{"skillset source", `UPDATE skillsets SET source='[]'`, `UPDATE skillsets SET source=NULL`, "source", []func() error{
			func() error { _, err := s.GetSkillset(ctx, "set1"); return err },
			func() error { _, err := s.ListSkillsets(ctx, ""); return err },
		}},
		{"version manifest", `UPDATE skillset_versions SET manifest='{}'`, `UPDATE skillset_versions SET manifest='[]'`, "manifest", []func() error{
			func() error { _, err := s.GetVersion(ctx, "set1", 1); return err },
			func() error { _, err := s.ListVersions(ctx, "set1"); return err },
		}},
		{"dashboard panels", `UPDATE analytics_dashboards SET panels='{}'`, `UPDATE analytics_dashboards SET panels='[]'`, "dashboard d1 panels", []func() error{
			func() error { _, err := s.GetDashboard(ctx, "d1"); return err },
			func() error { _, err := s.ListDashboards(ctx, "u1"); return err },
		}},
		{"dashboard filters", `UPDATE analytics_dashboards SET filters='{}'`, `UPDATE analytics_dashboards SET filters='[]'`, "dashboard d1 filters", []func() error{
			func() error { _, err := s.GetDashboard(ctx, "d1"); return err },
		}},
		{"scorecard terms", `UPDATE analytics_scorecards SET terms='{}'`, `UPDATE analytics_scorecards SET terms='[]'`, "scorecard sc1 terms", []func() error{
			func() error { _, err := s.GetScorecard(ctx, "sc1"); return err },
			func() error { _, err := s.ListScorecards(ctx, "u1"); return err },
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.pool.Exec(ctx, c.breakSQL); err != nil {
				t.Fatal(err)
			}
			for i, call := range c.calls {
				err := call()
				if err == nil {
					t.Errorf("call %d: no error", i)
				} else if !strings.Contains(err.Error(), c.want) {
					t.Errorf("call %d: %v, want it to mention %q", i, err, c.want)
				}
			}
			if _, err := s.pool.Exec(ctx, c.fixSQL); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// sabotage renames a table for one call, on a throwaway schema: the way to
// fail the second statement of a method whose first one succeeded.
func sabotage(t *testing.T, s *Store, table string, call func() error) error {
	t.Helper()
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `ALTER TABLE `+table+` RENAME TO `+table+`_gone`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.pool.Exec(ctx, `ALTER TABLE `+table+`_gone RENAME TO `+table); err != nil {
			t.Fatal(err)
		}
	}()
	return call()
}

// A projection failing fails the append: the event and its read model commit
// together or not at all.
func TestAppendRollsBackWithItsProjection(t *testing.T) {
	s := openDSN(t, pgtest.Empty(t))
	ctx := context.Background()
	if _, err := s.Append(ctx, "r1", &event.RunCreated{}, &event.PingRaised{PingID: "p0", Type: "blocker"},
		&event.StageStarted{StageID: "s", Iteration: 1}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		table   string
		payload event.Payload
	}{
		{"runs", &event.EnvRequested{}},
		{"pings", &event.PingRaised{PingID: "p1", Type: "blocker"}},
		{"pings", &event.PingAnswered{PingID: "p0", Answer: "x"}},
		{"pings", &event.RunFailed{Reason: "x"}},
		{"ping_review_comments", &event.PingAnswered{PingID: "p0", Answer: "x", Review: []event.ReviewComment{{Path: "a", Body: "b"}}}},
		{"snapshots", &event.SnapshotCaptured{SnapshotID: "s", Kind: "log", Filename: "f", Mime: "m"}},
		{"run_facts", &event.StageStarted{StageID: "t", Iteration: 1}},
		{"stage_attempts", &event.StageCompleted{StageID: "s"}},
		{"run_skillsets", &event.RunCreated{Skillsets: []event.SkillsetPin{{ID: "k", Name: "kit", Seq: 1, Hash: "h"}}}},
	}
	for _, c := range cases {
		if err := sabotage(t, s, c.table, func() error { _, err := s.Append(ctx, "r1", c.payload); return err }); err == nil {
			t.Errorf("%T with %s gone: append succeeded", c.payload, c.table)
		}
		if evs, _ := s.Events(ctx, "r1", 0); len(evs) != 3 {
			t.Fatalf("%T left %d events", c.payload, len(evs))
		}
	}
}

// Multi-statement writes are transactions; failing the later statement undoes
// the earlier one.
func TestLaterStatementFailuresRollBack(t *testing.T) {
	s := openDSN(t, pgtest.Empty(t))
	ctx := context.Background()
	seedIntegration(t, s, "i1", "u1")
	seedProject(t, s, mkProject("p1", "a", "u1", "i1"))
	if err := s.UpsertFlow(ctx, confstore.FlowRow{ProjectID: "p1", Name: "f", YAML: "one", Hash: "h1", UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}

	// A flow saved without its history row would run a version nothing records.
	err := sabotage(t, s, "flow_versions", func() error {
		return s.UpsertFlow(ctx, confstore.FlowRow{ProjectID: "p1", Name: "f", YAML: "two", Hash: "h2", UpdatedAt: testNow})
	})
	if err == nil {
		t.Fatal("UpsertFlow without its history table succeeded")
	}
	if got, _ := s.GetFlow(ctx, "p1", "f"); got.YAML != "one" {
		t.Errorf("flow row survived the failed history write: %+v", got)
	}

	// A skillset's membership swap is all or nothing.
	if err := s.UpsertSkill(ctx, skillstore.Skill{ID: "s1", UserID: "u1", Name: "n", Hash: "h", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	set := skillstore.Skillset{ID: "set1", UserID: "u1", Name: "set", SkillIDs: []string{"s1"}, CreatedAt: testNow, UpdatedAt: testNow}
	if err := s.UpsertSkillset(ctx, set); err != nil {
		t.Fatal(err)
	}
	edited := set
	edited.Description, edited.SkillIDs = "edited", nil
	if err := sabotage(t, s, "skillset_skills", func() error { return s.UpsertSkillset(ctx, edited) }); err == nil {
		t.Fatal("UpsertSkillset without its membership table succeeded")
	}
	if got, err := s.GetSkillset(ctx, "set1"); err != nil || got.Description != "" || len(got.SkillIDs) != 1 {
		t.Errorf("after a failed swap = %+v, %v", got, err)
	}
	// A member insert failing (not on the FK) is surfaced as itself.
	dup := set
	dup.SkillIDs = []string{"s1", "s1"}
	if err := s.UpsertSkillset(ctx, dup); err == nil || errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("duplicate member = %v", err)
	}
	if got, _ := s.GetSkillset(ctx, "set1"); len(got.SkillIDs) != 1 {
		t.Errorf("failed swap left %v", got.SkillIDs)
	}
}

// The reference checks that guard a delete must not read a failed query as
// "nothing references it": each of these would delete a row still in use.
func TestDeleteGuardsFailClosed(t *testing.T) {
	s := openDSN(t, pgtest.Empty(t))
	ctx := context.Background()
	seedIntegration(t, s, "i1", "u1")
	seedProject(t, s, mkProject("p1", "a", "u1", "i1"))
	if err := s.UpsertFlow(ctx, confstore.FlowRow{ProjectID: "p1", Name: "f", YAML: "y", Hash: "h", UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSkill(ctx, skillstore.Skill{ID: "s1", UserID: "u1", Name: "n", Hash: "h", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSkillset(ctx, skillstore.Skillset{ID: "set1", UserID: "u1", Name: "n", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, table string
		call        func() error
		check       func() error
	}{
		{"flow with runs gone", "runs",
			func() error { return s.DeleteFlow(ctx, "p1", "f") },
			func() error { _, err := s.GetFlow(ctx, "p1", "f"); return err }},
		{"project with runs gone", "runs",
			func() error { return s.DeleteProject(ctx, "p1") },
			func() error { _, err := s.GetProject(ctx, "p1"); return err }},
		{"skillset with runs gone", "runs",
			func() error { return s.DeleteSkillset(ctx, "set1") },
			func() error { _, err := s.GetSkillset(ctx, "set1"); return err }},
		{"integration with projects gone", "projects",
			func() error { return s.DeleteIntegration(ctx, "i1") },
			func() error { _, err := s.GetIntegration(ctx, "i1"); return err }},
		{"skill with memberships gone", "skillset_skills",
			func() error { return s.DeleteSkill(ctx, "s1") },
			func() error { _, err := s.GetSkill(ctx, "s1"); return err }},
	}
	for _, c := range cases {
		if err := sabotage(t, s, c.table, c.call); err == nil {
			t.Errorf("%s: the delete went through", c.name)
		}
		if err := c.check(); err != nil {
			t.Errorf("%s: the row is gone: %v", c.name, err)
		}
	}
}

// Reading a ping's comments failing fails the read: an answered review must
// not come back looking as if nobody said anything.
func TestPingReadFailsWhenItsCommentsDo(t *testing.T) {
	s := openDSN(t, pgtest.Empty(t))
	ctx := context.Background()
	if _, err := s.Append(ctx, "r1", &event.RunCreated{},
		&event.PingRaised{PingID: "p", Type: "gate"}, &event.PingAnswered{PingID: "p", Answer: "reject"}); err != nil {
		t.Fatal(err)
	}
	if err := sabotage(t, s, "ping_review_comments", func() error { _, err := s.GetPing(ctx, "p"); return err }); err == nil {
		t.Error("GetPing swallowed the failure")
	}
	if err := sabotage(t, s, "ping_review_comments", func() error { _, err := s.ListPings(ctx, "", "", 0); return err }); err == nil {
		t.Error("ListPings swallowed the failure")
	}
}

func TestRebuildFailsCleanlyWhenATableIsMissing(t *testing.T) {
	s := openDSN(t, pgtest.Empty(t))
	ctx := context.Background()
	if _, err := s.Append(ctx, "r1", &event.RunCreated{}); err != nil {
		t.Fatal(err)
	}
	if err := sabotage(t, s, "snapshots", func() error { _, err := s.RebuildProjections(ctx); return err }); err == nil {
		t.Error("rebuild without a projection table succeeded")
	}
	if views, _ := s.RunViews(ctx); len(views) != 1 {
		t.Errorf("failed rebuild emptied runs: %+v", views)
	}
}
