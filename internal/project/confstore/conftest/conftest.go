// Package conftest is the confstore.DB contract, run against every
// implementation (Mem in confstore's own tests, Postgres in pgstore's). It
// asserts what Postgres does: production runs nothing else, and the unit tier
// stands on Mem, so a Mem that differs proves the wrong thing.
//
// Two things it deliberately can't pin, so fixtures stay clear of them: names
// are lowercase letters and digits, the only ones Postgres's collation and
// Go's byte order sort alike; and jsonb rewrites the text it stores, so a value
// compares exactly only when the fixture is already in jsonb's form.
package conftest

import (
	"apothecary/internal/ports/envport"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/providers"
	"apothecary/internal/flow/builtin"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

// RunDBContract exercises one DB implementation. open must return a fresh,
// empty store.
func RunDBContract(t *testing.T, open func(t *testing.T) confstore.DB) {
	t.Helper()
	t.Run("integrations", func(t *testing.T) { integrations(t, open) })
	t.Run("projects", func(t *testing.T) { projects(t, open) })
	t.Run("project deletion", func(t *testing.T) { projectDeletion(t, open) })
	t.Run("members", func(t *testing.T) { members(t, open) })
	t.Run("tickets", func(t *testing.T) { tickets(t, open) })
	t.Run("ticket versions", func(t *testing.T) { ticketVersions(t, open) })
	t.Run("trackers", func(t *testing.T) { trackers(t, open) })
	t.Run("flows", func(t *testing.T) { flows(t, open) })
	t.Run("flow versions", func(t *testing.T) { flowVersions(t, open) })
	t.Run("flow bindings", func(t *testing.T) { bindings(t, open) })
	t.Run("project secrets", func(t *testing.T) { secrets(t, open) })
	t.Run("automations", func(t *testing.T) { automations(t, open) })
	t.Run("settings", func(t *testing.T) { settings(t, open) })
	t.Run("credentials", func(t *testing.T) { credentials(t, open) })
	t.Run("timestamps", func(t *testing.T) { timestamps(t, open) })
	t.Run("no shared memory", func(t *testing.T) { sharedMemory(t, open) })
}

// SeedProject lands p for a test that only needs a project to exist, with what
// no project exists without: a repository account and a provisioner of its
// owner's, its owner's internal tracker as its ticket source, and a template
// to default to.
func SeedProject(t *testing.T, svc *confstore.Service, p confstore.Project) {
	t.Helper()
	for _, ref := range []struct {
		id   *string
		port string
	}{{&p.RepoAccountID, confstore.PortRepository}, {&p.ProvisionerID, confstore.PortProvisioner}} {
		if *ref.id != "" {
			continue
		}
		in, err := svc.CreateIntegration(ctx, confstore.Integration{UserID: p.UserID, Port: ref.port, Kind: "fake", Name: ref.port + " of " + p.ID})
		ok(t, err)
		*ref.id = in.ID
	}
	if p.TicketSourceID == "" {
		in, err := svc.InternalTracker(ctx, p.UserID)
		ok(t, err)
		p.TicketSourceID = in.ID
	}
	if p.DefaultFlow == "" {
		p.DefaultFlow = template()
	}
	ok(t, svc.DB().UpsertProject(ctx, p))
}

type opener = func(t *testing.T) confstore.DB

var ctx = context.Background()

var epoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// at is n minutes after epoch: fixtures name their order, never the clock.
func at(n int) time.Time { return epoch.Add(time.Duration(n) * time.Minute) }

// template is a built-in flow: a project's default_flow may name one with no
// flows row behind it.
func template() string { return builtin.FlowNames()[0] }

// store reads rows the test needs to exist.
type store struct {
	confstore.DB
	t *testing.T
}

func (s store) integration(id string) confstore.StoredIntegration {
	s.t.Helper()
	r, err := s.GetIntegration(ctx, id)
	ok(s.t, err)
	return r
}

func (s store) project(id string) confstore.Project {
	s.t.Helper()
	p, err := s.GetProject(ctx, id)
	ok(s.t, err)
	return p
}

func (s store) ticket(id string) confstore.Ticket {
	s.t.Helper()
	r, err := s.GetTicket(ctx, id)
	ok(s.t, err)
	return r
}

func (s store) flow(projectID, name string) confstore.FlowRow {
	s.t.Helper()
	r, err := s.GetFlow(ctx, projectID, name)
	ok(s.t, err)
	return r
}

func (s store) automation(id string) confstore.Automation {
	s.t.Helper()
	a, err := s.GetAutomation(ctx, id)
	ok(s.t, err)
	return a
}

func (s store) credential(id string) confstore.ProviderCredential {
	s.t.Helper()
	c, err := s.GetCredential(ctx, id)
	ok(s.t, err)
	return c
}

// seeded is a store holding integration i1 and project p1 ("web"), both u1's.
func seeded(t *testing.T, open opener) store {
	t.Helper()
	s := store{open(t), t}
	ok(t, s.UpsertIntegration(ctx, repoAccount("i1", "u1", "main", 0)))
	ok(t, s.UpsertProject(ctx, plainProject("p1", "u1", "web", 0)))
	return s
}

func repoAccount(id, user, name string, n int) confstore.StoredIntegration {
	return confstore.StoredIntegration{ID: id, UserID: user, Port: confstore.PortRepository, Kind: "fake", Name: name,
		Config: map[string]any{}, Sealed: []byte{1}, CreatedAt: at(n), UpdatedAt: at(n)}
}

func fullIntegration() confstore.StoredIntegration {
	return confstore.StoredIntegration{ID: "i1", UserID: "u1", Port: confstore.PortTicketSource, Kind: "fake", Name: "main",
		Config: map[string]any{"url": "https://example.test", "n": 1.5, "on": true, "none": nil,
			"list": []any{"a", 2.0}, "sub": map[string]any{"k": "v"}},
		Sealed: []byte{1, 2, 3}, CreatedAt: at(1), UpdatedAt: at(2)}
}

// plainProject references i1 (its ticket source too: the store checks only
// that the row exists) and writes the slices and maps as they read back.
func plainProject(id, user, name string, n int) confstore.Project {
	return confstore.Project{ID: id, UserID: user, Name: name, RepoAccountID: "i1", ProvisionerID: "i1", TicketSourceID: "i1",
		HarnessIDs: []string{}, Repos: []confstore.ExtraRepo{}, Commands: map[string]string{},
		DefaultFlow: template(), CreatedAt: at(n), UpdatedAt: at(n)}
}

func fullProject() confstore.Project {
	return confstore.Project{ID: "p1", UserID: "u1", Name: "web", RepoAccountID: "i1", ProvisionerID: "i2", TicketSourceID: "i2",
		HarnessIDs: []string{"i3", "i2"},
		Repo: confstore.RepoConfig{URL: "https://example.test/g/web.git", Locator: "42", DefaultBranch: "trunk", AgentsMDPath: "docs/AGENTS.md",
			BranchFormat: "{ticket_number}/{ticket_title}"},
		Repos:    []confstore.ExtraRepo{{Name: "api", URL: "https://example.test/g/api.git", Locator: "43", DefaultBranch: "main"}},
		Commands: map[string]string{"check": "make check"},
		Workspace: confstore.WorkspaceConfig{
			ExtraDomains: []string{"pypi.org"}, EgressMode: "deny", DeniedDomains: []string{"denied.test"}, BlockedDomains: []string{"blocked.test"},
			EgressAllowedCIDRs: []string{"10.0.0.0/8"}, EgressAllowedPorts: []int{22}, CodeServerExtensions: []string{"acme.ext"},
			Editor: confstore.EditorConfig{Theme: "dark", FontSize: 13, TabSize: 2, FormatOnSave: true, WordWrap: true, TrustWorkspace: true},
			Services: []confstore.ServiceConfig{{Name: "default", Description: "the app", Start: "make up", Stop: "make down",
				Health: confstore.ServiceHealth{HTTP: "http://localhost:3000", TCP: 3000, Command: "true", Timeout: "1m", Interval: "5s"},
				Ports:  []confstore.ServicePort{{Port: 3000, Label: "web", Path: "/app", Protocol: "http"}}}},
			Browser:     confstore.BrowserConfig{Enabled: true},
			Docker:      true,
			Size:        envport.SandboxSize{MilliCPU: 2500, MemoryMiB: 6144},
			Runtime:     envport.RuntimeSysbox,
			IdleMinutes: 45,
			Env:         map[string]string{"JEST_MAX_WORKERS": "2", "NX_PARALLEL": "${APO_CPUS}"},
		},
		DefaultFlow: template(), CreatedAt: at(1), UpdatedAt: at(2)}
}

// ticket is an internal ticket of tracker sourceID: its remote id is its own.
func ticket(id, sourceID, user string, n int) confstore.Ticket {
	return confstore.Ticket{ID: id, UserID: user, Title: "t", SourceID: sourceID, SourceKind: confstore.KindInternal,
		RemoteID: id, CreatedAt: at(n), UpdatedAt: at(n)}
}

// fullTicket records issue 10001 of tracker i9 (trackerLists), in its status
// st1 and type ty1 (the store holds a status of any tracker; that only the
// internal one has them is the Service's).
func fullTicket() confstore.Ticket {
	moved := at(5)
	return confstore.Ticket{ID: "k1", UserID: "u1", Title: "Title", Goal: "Goal", BaseBranch: "fix/one",
		ChangeType: trackerport.ChangeFix, StatusID: "st1", TypeID: "ty1", Labels: []string{"backend", "Urgent"},
		SourceID: "i9", SourceKind: "jira", SourceKey: "PROJ-1", SourceURL: "https://example.test/1",
		SourceType: "Bug", RemoteID: "10001", StatusChangedAt: &moved, StatusChangedBy: "run:r1/main-1/kick",
		AutomationID: "a1", CreatedAt: at(1), UpdatedAt: at(2)}
}

// trackerLists stores tracker i9 (u1's Jira) with statuses st1 (To Do) and
// st2 (Done) and type ty1 (Bug, fix): what fullTicket points at.
func trackerLists(t *testing.T, s store) {
	t.Helper()
	ok(t, s.UpsertIntegration(ctx, confstore.StoredIntegration{ID: "i9", UserID: "u1", Port: confstore.PortTicketSource,
		Kind: "jira", Name: "jira", Config: map[string]any{}, Sealed: []byte{1}, CreatedAt: at(0), UpdatedAt: at(0)}))
	ok(t, s.ReplaceTrackerStatuses(ctx, "i9", []confstore.TrackerStatus{
		{ID: "st1", IntegrationID: "i9", RemoteID: "1", Name: "To Do"},
		{ID: "st2", IntegrationID: "i9", RemoteID: "2", Name: "Done", Position: 1, IsDone: true},
	}, nil))
	ok(t, s.ReplaceTrackerTypes(ctx, "i9", []confstore.TrackerType{
		{ID: "ty1", IntegrationID: "i9", RemoteID: "10", Name: "Bug", ChangeType: trackerport.ChangeFix},
	}, nil))
}

func flowRow(projectID, name, hash string, n int) confstore.FlowRow {
	return confstore.FlowRow{ProjectID: projectID, Name: name, YAML: "yaml of " + hash, Hash: hash, UpdatedAt: at(n)}
}

// canonicalLayout is already in jsonb's form, so it reads back byte for byte.
const canonicalLayout = `{"a": 1, "b": [1, 2]}`

func automation(id, projectID string, n int) confstore.Automation {
	return confstore.Automation{ID: id, ProjectID: projectID, Name: "n", Kind: confstore.AutomationCron, Flow: "f", CreatedAt: at(n), UpdatedAt: at(n)}
}

func fullAutomation() confstore.Automation {
	polled, fired := at(3), at(4)
	return confstore.Automation{ID: "a1", ProjectID: "p1", Name: "nightly", Kind: confstore.AutomationMergeRequest,
		Query: "q", Schedule: "0 3 * * *", TicketID: "k9", Repo: "api", Flow: "f",
		Params: map[string]string{"gate": "false"}, Enabled: true, LastPolledAt: &polled, LastFiredAt: &fired, LastError: "boom",
		CreatedAt: at(1), UpdatedAt: at(2)}
}

func credential(id, user string, n int) confstore.ProviderCredential {
	return confstore.ProviderCredential{ID: id, UserID: user, Provider: "acme", Kind: providers.KindAPIKey, Name: "n",
		Meta: confstore.CredentialMeta{Status: confstore.CredentialOK}, Sealed: []byte{1}, CreatedAt: at(n), UpdatedAt: at(n)}
}

func fullCredential() confstore.ProviderCredential {
	return confstore.ProviderCredential{ID: "c1", UserID: "u1", Provider: "acme", Kind: providers.KindOAuth, Name: "Acme",
		Meta:   confstore.CredentialMeta{Account: "me@example.test", ExpiresAt: at(9), LastRefresh: at(8), Status: confstore.CredentialDisabled, Error: "expired"},
		Sealed: []byte("sealed"), CreatedAt: at(1), UpdatedAt: at(2)}
}

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func is(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("%s = %v, want %v", what, err, want)
	}
}

// refused is for refusals Postgres reports as a bare database error: which
// error is not part of the contract, only that the write did not land.
func refused(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: want a refusal", what)
	}
}

func names(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s = %v, want it to name %q", what, err, want)
	}
}

// same is reflect.DeepEqual, so nil and empty differ, and time zones don't:
// Postgres hands an instant back in Local.
func same[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	inUTC(reflect.ValueOf(&got))
	inUTC(reflect.ValueOf(&want))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %#v\nwant %#v", what, got, want)
	}
}

func inUTC(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			inUTC(v.Elem())
		}
	case reflect.Slice:
		for i := range v.Len() {
			inUTC(v.Index(i))
		}
	case reflect.Struct:
		if ts, isTime := v.Interface().(time.Time); isTime {
			v.Set(reflect.ValueOf(ts.UTC()))
			return
		}
		for i := range v.NumField() {
			inUTC(v.Field(i))
		}
	}
}

// allSet fails on a fixture field left zero, so a field added to a row can't
// stay outside the roundtrips.
func allSet(t *testing.T, v any) {
	t.Helper()
	var walk func(path string, v reflect.Value)
	walk = func(path string, v reflect.Value) {
		if v.IsZero() {
			t.Errorf("fixture leaves %s zero", path)
			return
		}
		switch v.Kind() {
		case reflect.Pointer:
			walk(path, v.Elem())
		case reflect.Slice:
			for i := range v.Len() {
				walk(fmt.Sprintf("%s[%d]", path, i), v.Index(i))
			}
		case reflect.Struct:
			if _, isTime := v.Interface().(time.Time); isTime {
				return
			}
			for i := range v.NumField() {
				walk(path+"."+v.Type().Field(i).Name, v.Field(i))
			}
		}
	}
	walk(reflect.TypeOf(v).Name(), reflect.ValueOf(v))
}

// sameJSON compares by meaning: jsonb keeps neither spacing nor key order.
func sameJSON(t *testing.T, what, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("%s: got %q: %v", what, got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: want %q: %v", what, want, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

func ids[T any](rows []T, id func(T) string) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, id(r))
	}
	return out
}

func integrationIDs(s store, user string) []string {
	s.t.Helper()
	rows, err := s.ListIntegrations(ctx, user)
	ok(s.t, err)
	return ids(rows, func(r confstore.StoredIntegration) string { return r.ID })
}

func projectIDs(s store, user string) []string {
	s.t.Helper()
	rows, err := s.ListProjects(ctx, user)
	ok(s.t, err)
	return ids(rows, func(r confstore.Project) string { return r.ID })
}

func flowNames(s store, projectID string) []string {
	s.t.Helper()
	rows, err := s.ListFlows(ctx, projectID)
	ok(s.t, err)
	return ids(rows, func(r confstore.FlowRow) string { return r.ProjectID + "/" + r.Name })
}

func versionHashes(s store, projectID, name string) []string {
	s.t.Helper()
	rows, err := s.ListFlowVersions(ctx, projectID, name)
	ok(s.t, err)
	return ids(rows, func(r confstore.FlowVersion) string { return r.Hash })
}

func bindingNames(s store, projectID string) []string {
	s.t.Helper()
	rows, err := s.ListFlowBindings(ctx, projectID)
	ok(s.t, err)
	return ids(rows, func(r confstore.FlowBinding) string { return r.FlowName })
}

func secretNames(s store, projectID string) []string {
	s.t.Helper()
	rows, err := s.ListProjectSecrets(ctx, projectID)
	ok(s.t, err)
	return ids(rows, func(r confstore.StoredProjectSecret) string { return r.Name })
}

func automationIDs(s store, projectID string) []string {
	s.t.Helper()
	rows, err := s.ListAutomations(ctx, projectID)
	ok(s.t, err)
	return ids(rows, func(r confstore.Automation) string { return r.ID })
}

func credentialIDs(s store, user string) []string {
	s.t.Helper()
	rows, err := s.ListCredentials(ctx, user)
	ok(s.t, err)
	return ids(rows, func(r confstore.ProviderCredential) string { return r.ID })
}

func integrations(t *testing.T, open opener) {
	t.Run("roundtrip keeps every field", func(t *testing.T) {
		s := store{open(t), t}
		in := fullIntegration()
		allSet(t, in)
		ok(t, s.UpsertIntegration(ctx, in))
		same(t, "get", s.integration(in.ID), in)
		list, err := s.ListIntegrations(ctx, in.UserID)
		ok(t, err)
		same(t, "list", list, []confstore.StoredIntegration{in})
	})

	// What CredentialRefIDs and every config reader type-assert on.
	t.Run("config reads back as JSON", func(t *testing.T) {
		s := store{open(t), t}
		in := repoAccount("i1", "u1", "main", 0)
		in.Config = map[string]any{"n": 1, "ids": []string{"a"}, "sub": map[string]int{"x": 1}}
		ok(t, s.UpsertIntegration(ctx, in))
		same(t, "config", s.integration("i1").Config, map[string]any{"n": 1.0, "ids": []any{"a"}, "sub": map[string]any{"x": 1.0}})
	})

	t.Run("nil and empty", func(t *testing.T) {
		s := store{open(t), t}
		in := repoAccount("i1", "u1", "main", 0)
		in.Config, in.Sealed = nil, nil
		ok(t, s.UpsertIntegration(ctx, in))
		got := s.integration("i1")
		same(t, "nil config", got.Config, nil)
		same(t, "nil secrets", got.Sealed, []byte{})

		in = repoAccount("i2", "u1", "other", 0)
		in.Sealed = []byte{}
		ok(t, s.UpsertIntegration(ctx, in))
		got = s.integration("i2")
		same(t, "empty config", got.Config, map[string]any{})
		same(t, "empty secrets", got.Sealed, []byte{})
	})

	t.Run("missing", func(t *testing.T) {
		s := store{open(t), t}
		_, err := s.GetIntegration(ctx, "nope")
		is(t, "get", err, confstore.ErrNotFound)
		is(t, "delete", s.DeleteIntegration(ctx, "nope"), confstore.ErrNotFound)
	})

	t.Run("names are unique per owner and port", func(t *testing.T) {
		s := store{open(t), t}
		ok(t, s.UpsertIntegration(ctx, repoAccount("i1", "u1", "main", 0)))
		is(t, "duplicate", s.UpsertIntegration(ctx, repoAccount("i2", "u1", "main", 1)), confstore.ErrConflict)
		if _, err := s.GetIntegration(ctx, "i2"); !errors.Is(err, confstore.ErrNotFound) {
			t.Errorf("a refused insert landed: %v", err)
		}
		ok(t, s.UpsertIntegration(ctx, repoAccount("i3", "u2", "main", 2)))
		otherPort := repoAccount("i4", "u1", "main", 3)
		otherPort.Port = confstore.PortProvisioner
		ok(t, s.UpsertIntegration(ctx, otherPort))

		spare := repoAccount("i5", "u1", "spare", 4)
		ok(t, s.UpsertIntegration(ctx, spare))
		spare.Name = "main"
		is(t, "rename onto a taken name", s.UpsertIntegration(ctx, spare), confstore.ErrConflict)
		same(t, "after the refused rename", s.integration("i5").Name, "spare")
		ok(t, s.UpsertIntegration(ctx, repoAccount("i1", "u1", "main", 0))) // its own name is not a conflict
	})

	t.Run("an update sets name, config, secrets and updated_at, nothing else", func(t *testing.T) {
		s := store{open(t), t}
		in := fullIntegration()
		ok(t, s.UpsertIntegration(ctx, in))
		upd := in
		upd.UserID, upd.Port, upd.Kind, upd.Name = "u2", confstore.PortProvisioner, "other", "renamed"
		upd.Config, upd.Sealed = map[string]any{"k": "v"}, []byte{9}
		upd.CreatedAt, upd.UpdatedAt = at(50), at(60)
		ok(t, s.UpsertIntegration(ctx, upd))
		want := in
		want.Name, want.Config, want.Sealed, want.UpdatedAt = upd.Name, upd.Config, upd.Sealed, upd.UpdatedAt
		same(t, "after update", s.integration("i1"), want)
		same(t, "owner's list", integrationIDs(s, "u1"), []string{"i1"})
		same(t, "payload owner's list", integrationIDs(s, "u2"), []string{})
	})

	// The owner and port on an update are ignored, so they can neither
	// cause a conflict nor dodge one.
	t.Run("a rename conflicts on the stored owner and port", func(t *testing.T) {
		s := store{open(t), t}
		ok(t, s.UpsertIntegration(ctx, repoAccount("i1", "u1", "taken", 0)))
		ok(t, s.UpsertIntegration(ctx, repoAccount("i2", "u2", "theirs", 1)))
		mine := repoAccount("i3", "u1", "mine", 2)
		ok(t, s.UpsertIntegration(ctx, mine))

		dodge := mine
		dodge.UserID, dodge.Name = "u2", "taken"
		is(t, "stored owner has it", s.UpsertIntegration(ctx, dodge), confstore.ErrConflict)
		otherPort := mine
		otherPort.Port, otherPort.Name = confstore.PortProvisioner, "taken"
		is(t, "stored port has it", s.UpsertIntegration(ctx, otherPort), confstore.ErrConflict)

		free := mine
		free.UserID, free.Name = "u2", "theirs"
		ok(t, s.UpsertIntegration(ctx, free))
		got := s.integration("i3")
		same(t, "name", got.Name, "theirs")
		same(t, "owner", got.UserID, "u1")
	})

	t.Run("lists by created_at, one owner or everyone", func(t *testing.T) {
		s := store{open(t), t}
		ok(t, s.UpsertIntegration(ctx, repoAccount("i1", "u1", "c", 3)))
		ok(t, s.UpsertIntegration(ctx, repoAccount("i2", "u2", "b", 2)))
		ok(t, s.UpsertIntegration(ctx, repoAccount("i3", "u1", "a", 1)))
		same(t, "u1", integrationIDs(s, "u1"), []string{"i3", "i1"})
		same(t, "everyone", integrationIDs(s, ""), []string{"i3", "i2", "i1"})
		none, err := s.ListIntegrations(ctx, "nobody")
		ok(t, err)
		same(t, "nobody", none, nil)
	})

	t.Run("delete is refused while a project uses it, naming them", func(t *testing.T) {
		s := store{open(t), t}
		ok(t, s.UpsertIntegration(ctx, repoAccount("i1", "u1", "repo", 0)))
		ok(t, s.UpsertIntegration(ctx, repoAccount("i2", "u1", "prov", 0)))
		ok(t, s.UpsertIntegration(ctx, repoAccount("i3", "u1", "harn", 0)))
		ok(t, s.UpsertIntegration(ctx, repoAccount("i4", "u1", "free", 0)))
		ok(t, s.UpsertIntegration(ctx, repoAccount("i5", "u1", "tracker", 0)))
		a := plainProject("p1", "u1", "alpha", 1)
		a.ProvisionerID, a.HarnessIDs, a.TicketSourceID = "i2", []string{"i3"}, "i5"
		b := plainProject("p2", "u1", "beta", 2)
		b.ProvisionerID = "i2"
		ok(t, s.UpsertProject(ctx, b)) // beta first: the message sorts, the insert order doesn't
		ok(t, s.UpsertProject(ctx, a))

		for _, tc := range []struct{ id, want string }{{"i1", "alpha, beta"}, {"i2", "alpha, beta"}, {"i3", "alpha"}, {"i5", "alpha"}} {
			err := s.DeleteIntegration(ctx, tc.id)
			is(t, tc.id, err, confstore.ErrReferenced)
			names(t, tc.id, err, tc.want)
			s.integration(tc.id)
		}
		ok(t, s.DeleteIntegration(ctx, "i4"))
		is(t, "second delete", s.DeleteIntegration(ctx, "i4"), confstore.ErrNotFound)
	})

	// tickets.source_id has no FK: a record outlives its tracker, and only
	// the type it pointed at (the tracker's list goes with it) is let go.
	t.Run("a ticket source no project uses deletes, its tickets stay", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		k := fullTicket()
		k.StatusID = ""
		ok(t, s.UpsertTicket(ctx, k, confstore.Change{Kind: confstore.AuthorUser}))
		p := s.project("p1")
		p.TicketSourceID = "i9"
		ok(t, s.UpsertProject(ctx, p))
		err := s.DeleteIntegration(ctx, "i9")
		is(t, "in use", err, confstore.ErrReferenced)
		names(t, "in use", err, "web")
		p.TicketSourceID = "i1"
		ok(t, s.UpsertProject(ctx, p))
		ok(t, s.DeleteIntegration(ctx, "i9"))
		want := k
		want.TypeID = ""
		same(t, "the record", s.ticket("k1"), want)
		types, err := s.ListTrackerTypes(ctx, "i9")
		ok(t, err)
		same(t, "its types", len(types), 0)
	})

	// The harness set has no FK behind it: the scan is all there is, and it
	// runs before anything looks for the row.
	t.Run("a reference outranks a missing row", func(t *testing.T) {
		s := seeded(t, open)
		p := plainProject("p2", "u1", "api", 1)
		p.HarnessIDs = []string{"ghost"}
		ok(t, s.UpsertProject(ctx, p))
		is(t, "delete", s.DeleteIntegration(ctx, "ghost"), confstore.ErrReferenced)
	})
}

func projects(t *testing.T, open opener) {
	full := func(t *testing.T) store {
		t.Helper()
		s := store{open(t), t}
		ok(t, s.UpsertIntegration(ctx, repoAccount("i1", "u1", "one", 0)))
		ok(t, s.UpsertIntegration(ctx, repoAccount("i2", "u1", "two", 0)))
		return s
	}

	t.Run("roundtrip keeps every field", func(t *testing.T) {
		s := full(t)
		p := fullProject()
		allSet(t, p)
		ok(t, s.UpsertProject(ctx, p))
		same(t, "get", s.project(p.ID), p)
		byName, err := s.GetProjectByName(ctx, p.UserID, p.Name)
		ok(t, err)
		same(t, "by name", byName, p)
		list, err := s.ListProjects(ctx, p.UserID)
		ok(t, err)
		same(t, "list", list, []confstore.Project{p})
	})

	t.Run("nil and empty", func(t *testing.T) {
		s := seeded(t, open)
		p := plainProject("p2", "u1", "api", 1)
		p.HarnessIDs, p.Repos, p.Commands = nil, nil, nil
		ok(t, s.UpsertProject(ctx, p))
		got := s.project("p2")
		same(t, "nil harness set", got.HarnessIDs, []string{})
		same(t, "nil repos", got.Repos, []confstore.ExtraRepo{})
		same(t, "nil commands", got.Commands, nil)

		same(t, "empty harness set", s.project("p1").HarnessIDs, []string{})
		same(t, "empty repos", s.project("p1").Repos, []confstore.ExtraRepo{})
		same(t, "empty commands", s.project("p1").Commands, map[string]string{})
	})

	// workspace is jsonb through omitempty tags.
	t.Run("empty workspace lists read back nil", func(t *testing.T) {
		s := seeded(t, open)
		p := plainProject("p2", "u1", "api", 1)
		p.Workspace = confstore.WorkspaceConfig{ExtraDomains: []string{},
			Services: []confstore.ServiceConfig{{Name: "default", Start: "up", Ports: []confstore.ServicePort{}}}}
		ok(t, s.UpsertProject(ctx, p))
		same(t, "workspace", s.project("p2").Workspace,
			confstore.WorkspaceConfig{Services: []confstore.ServiceConfig{{Name: "default", Start: "up"}}})
	})

	t.Run("missing", func(t *testing.T) {
		s := store{open(t), t}
		_, err := s.GetProject(ctx, "nope")
		is(t, "get", err, confstore.ErrNotFound)
		_, err = s.GetProjectByName(ctx, "", "nope")
		is(t, "by name", err, confstore.ErrNotFound)
		is(t, "delete", s.DeleteProject(ctx, "nope"), confstore.ErrNotFound)
	})

	t.Run("default_flow is the project's own flow or a template", func(t *testing.T) {
		s := seeded(t, open)
		p := plainProject("p2", "u1", "api", 1)
		p.DefaultFlow = "nope"
		is(t, "unknown", s.UpsertProject(ctx, p), confstore.ErrInvalid)
		p.DefaultFlow = ""
		is(t, "empty", s.UpsertProject(ctx, p), confstore.ErrInvalid)
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "mine", "h1", 0)))
		p.DefaultFlow = "mine"
		is(t, "another project's flow", s.UpsertProject(ctx, p), confstore.ErrInvalid)
		if _, err := s.GetProject(ctx, "p2"); !errors.Is(err, confstore.ErrNotFound) {
			t.Errorf("a refused insert landed: %v", err)
		}

		p.DefaultFlow = template()
		ok(t, s.UpsertProject(ctx, p))
		ok(t, s.UpsertFlow(ctx, flowRow("p2", "own", "h2", 0)))
		p.DefaultFlow = "own"
		ok(t, s.UpsertProject(ctx, p))

		upd := p
		upd.Name, upd.DefaultFlow = "renamed", "nope"
		is(t, "on update", s.UpsertProject(ctx, upd), confstore.ErrInvalid)
		same(t, "after the refused update", s.project("p2"), p)
	})

	t.Run("repo account, provisioner and ticket source must exist, the harness set need not", func(t *testing.T) {
		s := seeded(t, open)
		p := plainProject("p2", "u1", "api", 1)
		p.RepoAccountID = "ghost"
		is(t, "repo account", s.UpsertProject(ctx, p), confstore.ErrInvalid)
		p.RepoAccountID, p.ProvisionerID = "i1", "ghost"
		is(t, "provisioner", s.UpsertProject(ctx, p), confstore.ErrInvalid)
		p.ProvisionerID, p.TicketSourceID = "i1", "ghost"
		is(t, "ticket source", s.UpsertProject(ctx, p), confstore.ErrInvalid)
		p.TicketSourceID = "i1"
		if _, err := s.GetProject(ctx, "p2"); !errors.Is(err, confstore.ErrNotFound) {
			t.Errorf("a refused insert landed: %v", err)
		}

		before := s.project("p1")
		upd := before
		upd.RepoAccountID = "ghost"
		is(t, "on update", s.UpsertProject(ctx, upd), confstore.ErrInvalid)
		same(t, "after the refused update", s.project("p1"), before)

		p.HarnessIDs = []string{"ghost"}
		ok(t, s.UpsertProject(ctx, p))
		same(t, "harness set", s.project("p2").HarnessIDs, []string{"ghost"})
	})

	// Which refusal wins: the flow gate says whether a row is written at all,
	// the unique index fires on the write, the FK after it.
	t.Run("the flow gate outranks a conflict, a conflict outranks an FK", func(t *testing.T) {
		s := seeded(t, open)
		dup := plainProject("p2", "u1", "web", 1)
		dup.DefaultFlow = "nope"
		is(t, "taken name, missing flow", s.UpsertProject(ctx, dup), confstore.ErrInvalid)
		dup.DefaultFlow = template()
		dup.RepoAccountID = "ghost"
		is(t, "taken name, missing integration", s.UpsertProject(ctx, dup), confstore.ErrConflict)
	})

	t.Run("names are unique per owner", func(t *testing.T) {
		s := seeded(t, open)
		is(t, "duplicate", s.UpsertProject(ctx, plainProject("p2", "u1", "web", 1)), confstore.ErrConflict)
		ok(t, s.UpsertProject(ctx, plainProject("p3", "u2", "web", 2)))
		spare := plainProject("p4", "u1", "spare", 3)
		ok(t, s.UpsertProject(ctx, spare))
		spare.Name = "web"
		is(t, "rename onto a taken name", s.UpsertProject(ctx, spare), confstore.ErrConflict)
		same(t, "after the refused rename", s.project("p4").Name, "spare")
		ok(t, s.UpsertProject(ctx, plainProject("p1", "u1", "web", 0))) // its own name is not a conflict
	})

	t.Run("a rename conflicts on the stored owner", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u2", "theirs", 1)))
		mine := plainProject("p3", "u1", "mine", 2)
		ok(t, s.UpsertProject(ctx, mine))

		dodge := mine
		dodge.UserID, dodge.Name = "u2", "web"
		is(t, "stored owner has it", s.UpsertProject(ctx, dodge), confstore.ErrConflict)

		free := mine
		free.UserID, free.Name = "u2", "theirs"
		ok(t, s.UpsertProject(ctx, free))
		got := s.project("p3")
		same(t, "name", got.Name, "theirs")
		same(t, "owner", got.UserID, "u1")
	})

	t.Run("an update replaces all but the owner and created_at", func(t *testing.T) {
		s := full(t)
		in := fullProject()
		ok(t, s.UpsertProject(ctx, in))
		upd := plainProject("p1", "u2", "renamed", 70)
		upd.RepoAccountID, upd.ProvisionerID = "i2", "i2"
		ok(t, s.UpsertProject(ctx, upd))
		want := upd
		want.UserID, want.CreatedAt = in.UserID, in.CreatedAt
		same(t, "after update", s.project("p1"), want)
		same(t, "payload owner's list", projectIDs(s, "u2"), []string{})
	})

	t.Run("by name: one owner's, or the oldest of anyone's", func(t *testing.T) {
		s := store{open(t), t}
		ok(t, s.UpsertIntegration(ctx, repoAccount("i1", "u1", "main", 0)))
		ok(t, s.UpsertProject(ctx, plainProject("p1", "u1", "web", 5)))
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u2", "web", 1)))
		ok(t, s.UpsertProject(ctx, plainProject("p3", "u3", "web", 9)))
		for _, tc := range []struct{ user, want string }{{"u1", "p1"}, {"u3", "p3"}, {"", "p2"}} {
			got, err := s.GetProjectByName(ctx, tc.user, "web")
			ok(t, err)
			same(t, "owner "+tc.user, got.ID, tc.want)
		}
		_, err := s.GetProjectByName(ctx, "u4", "web")
		is(t, "another owner's", err, confstore.ErrNotFound)
	})

	t.Run("lists by created_at, one owner or everyone", func(t *testing.T) {
		s := store{open(t), t}
		ok(t, s.UpsertIntegration(ctx, repoAccount("i1", "u1", "main", 0)))
		ok(t, s.UpsertProject(ctx, plainProject("p1", "u1", "c", 3)))
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u2", "b", 2)))
		ok(t, s.UpsertProject(ctx, plainProject("p3", "u1", "a", 1)))
		same(t, "u1", projectIDs(s, "u1"), []string{"p3", "p1"})
		same(t, "everyone", projectIDs(s, ""), []string{"p3", "p2", "p1"})
		none, err := s.ListProjects(ctx, "nobody")
		ok(t, err)
		same(t, "nobody", none, nil)
	})
}

func projectDeletion(t *testing.T, open opener) {
	t.Run("takes everything under it and nothing of another project's", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u1", "api", 1)))
		for _, id := range []string{"p1", "p2"} {
			ok(t, s.UpsertFlow(ctx, flowRow(id, "main", "h"+id, 0)))
			ok(t, s.PutFlowBinding(ctx, confstore.FlowBinding{ProjectID: id, FlowName: "main", Params: map[string]string{"a": "b"}, UpdatedAt: at(0)}))
			ok(t, s.PutProjectSecret(ctx, id, confstore.StoredProjectSecret{Name: "token", Sealed: []byte{1}, UpdatedAt: at(0)}))
			ok(t, s.UpsertAutomation(ctx, automation("a"+id, id, 0)))
		}
		// A default_flow pointing at its own row doesn't hold the delete back.
		p := s.project("p1")
		p.DefaultFlow = "main"
		ok(t, s.UpsertProject(ctx, p))

		ok(t, s.DeleteProject(ctx, "p1"))
		_, err := s.GetProject(ctx, "p1")
		is(t, "project", err, confstore.ErrNotFound)
		_, err = s.GetFlow(ctx, "p1", "main")
		is(t, "flow", err, confstore.ErrNotFound)
		_, err = s.GetFlowVersion(ctx, "p1", "hp1")
		is(t, "flow version", err, confstore.ErrNotFound)
		same(t, "flow versions", versionHashes(s, "p1", "main"), []string{})
		_, err = s.GetFlowBinding(ctx, "p1", "main")
		is(t, "binding", err, confstore.ErrNotFound)
		same(t, "secrets", secretNames(s, "p1"), []string{})
		_, err = s.GetAutomation(ctx, "ap1")
		is(t, "automation", err, confstore.ErrNotFound)

		same(t, "other project's flows", flowNames(s, ""), []string{"p2/main"})
		same(t, "other project's versions", versionHashes(s, "p2", "main"), []string{"hp2"})
		same(t, "other project's bindings", bindingNames(s, "p2"), []string{"main"})
		same(t, "other project's secrets", secretNames(s, "p2"), []string{"token"})
		same(t, "other project's automations", automationIDs(s, ""), []string{"ap2"})
	})

	t.Run("frees its integrations and its name", func(t *testing.T) {
		s := seeded(t, open)
		is(t, "in use", s.DeleteIntegration(ctx, "i1"), confstore.ErrReferenced)
		ok(t, s.DeleteProject(ctx, "p1"))
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u1", "web", 1)))
		ok(t, s.DeleteProject(ctx, "p2"))
		ok(t, s.DeleteIntegration(ctx, "i1"))
	})
}

// withUsers makes the accounts a member row names, where the store keeps
// accounts (Postgres's project_members.user_id is a foreign key; Mem holds
// no accounts to check).
func withUsers(t *testing.T, s store, ids ...string) {
	t.Helper()
	us, ok := s.DB.(interface {
		CreateUser(context.Context, auth.User) error
	})
	if !ok {
		return
	}
	for _, id := range ids {
		if err := us.CreateUser(ctx, auth.User{ID: id, Handle: id, Email: id + "@x", Name: id, CreatedAt: at(0), UpdatedAt: at(0)}); err != nil {
			t.Fatal(err)
		}
	}
}

func member(project, user, role string, n int) confstore.Member {
	return confstore.Member{ProjectID: project, UserID: user, Role: role, CreatedAt: at(n)}
}

func memberRoles(s store, project string) []string {
	s.t.Helper()
	ms, err := s.ListMembers(ctx, project)
	ok(s.t, err)
	out := []string{}
	for _, m := range ms {
		out = append(out, m.UserID+"="+m.Role)
	}
	return out
}

func members(t *testing.T, open opener) {
	t.Run("put, read, list, upsert, delete", func(t *testing.T) {
		s := seeded(t, open)
		withUsers(t, s, "u2", "u3")
		same(t, "none yet", memberRoles(s, "p1"), []string{})
		ok(t, s.PutMember(ctx, member("p1", "u3", confstore.MemberViewer, 2), ""))
		ok(t, s.PutMember(ctx, member("p1", "u2", confstore.MemberOperator, 1), ""))
		same(t, "by created_at", memberRoles(s, "p1"), []string{"u2=operator", "u3=viewer"})
		got, err := s.GetMember(ctx, "p1", "u2")
		ok(t, err)
		same(t, "roundtrip", got, member("p1", "u2", confstore.MemberOperator, 1))

		// A second put is the role changing: one row, the role new, created_at kept.
		ok(t, s.PutMember(ctx, member("p1", "u2", confstore.MemberMaintainer, 9), confstore.MemberOperator))
		same(t, "changed", memberRoles(s, "p1"), []string{"u2=maintainer", "u3=viewer"})
		got, err = s.GetMember(ctx, "p1", "u2")
		ok(t, err)
		same(t, "created_at kept", got.CreatedAt, at(1))

		ok(t, s.DeleteMember(ctx, "p1", "u3", confstore.MemberViewer))
		same(t, "after delete", memberRoles(s, "p1"), []string{"u2=maintainer"})
		is(t, "delete again", s.DeleteMember(ctx, "p1", "u3", confstore.MemberViewer), confstore.ErrNotFound)
		_, err = s.GetMember(ctx, "p1", "u3")
		is(t, "gone", err, confstore.ErrNotFound)
	})

	t.Run("a role outside the three and a missing project are refused", func(t *testing.T) {
		s := seeded(t, open)
		withUsers(t, s, "u2")
		is(t, "owner role", s.PutMember(ctx, member("p1", "u2", "owner", 0), ""), confstore.ErrInvalid)
		is(t, "no project", s.PutMember(ctx, member("nope", "u2", confstore.MemberViewer, 0), ""), confstore.ErrNotFound)
		is(t, "no project, a change", s.PutMember(ctx, member("nope", "u2", confstore.MemberViewer, 0), confstore.MemberOperator), confstore.ErrNotFound)
		same(t, "nothing written", memberRoles(s, "p1"), []string{})
	})

	// Every write names the role it read: one that changed meanwhile wins
	// over the stale request, which writes nothing. The owner-only rule on
	// maintainers rests on it — a maintainer that read an operator must not
	// demote or remove the maintainer the owner made of it in between.
	t.Run("a write from a stale role writes nothing", func(t *testing.T) {
		s := seeded(t, open)
		withUsers(t, s, "u2")
		ok(t, s.PutMember(ctx, member("p1", "u2", confstore.MemberOperator, 0), ""))
		// The maintainer reads operator; the owner promotes; the maintainer writes.
		ok(t, s.PutMember(ctx, member("p1", "u2", confstore.MemberMaintainer, 0), confstore.MemberOperator))
		is(t, "stale demotion", s.PutMember(ctx, member("p1", "u2", confstore.MemberViewer, 0), confstore.MemberOperator), confstore.ErrConflict)
		is(t, "stale removal", s.DeleteMember(ctx, "p1", "u2", confstore.MemberOperator), confstore.ErrConflict)
		is(t, "stale add", s.PutMember(ctx, member("p1", "u2", confstore.MemberViewer, 0), ""), confstore.ErrConflict)
		same(t, "still the owner's maintainer", memberRoles(s, "p1"), []string{"u2=maintainer"})
		// Gone meanwhile: a change from its old role finds nothing to change.
		ok(t, s.DeleteMember(ctx, "p1", "u2", confstore.MemberMaintainer))
		is(t, "a change after removal", s.PutMember(ctx, member("p1", "u2", confstore.MemberOperator, 0), confstore.MemberMaintainer), confstore.ErrConflict)
		same(t, "not re-added", memberRoles(s, "p1"), []string{})
	})

	t.Run("two writes from the same role: one wins, the other changes nothing", func(t *testing.T) {
		s := seeded(t, open)
		withUsers(t, s, "u2")
		for round := range 20 {
			ok(t, s.PutMember(ctx, member("p1", "u2", confstore.MemberOperator, 0), ""))
			// The owner promotes while a maintainer demotes (even rounds) or
			// removes (odd), both having read operator.
			errs := make([]error, 2)
			var wg sync.WaitGroup
			start := make(chan struct{})
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				errs[0] = s.PutMember(ctx, member("p1", "u2", confstore.MemberMaintainer, 0), confstore.MemberOperator)
			}()
			go func() {
				defer wg.Done()
				<-start
				if round%2 == 0 {
					errs[1] = s.PutMember(ctx, member("p1", "u2", confstore.MemberViewer, 0), confstore.MemberOperator)
				} else {
					errs[1] = s.DeleteMember(ctx, "p1", "u2", confstore.MemberOperator)
				}
			}()
			close(start)
			wg.Wait()
			promoted, other := errs[0] == nil, errs[1] == nil
			if promoted == other {
				t.Fatalf("round %d: promote %v, the other %v; want exactly one to win", round, errs[0], errs[1])
			}
			loser := errs[0]
			if promoted {
				loser = errs[1]
			}
			// Whichever lost found the row changed (or removed) under it.
			if !errors.Is(loser, confstore.ErrConflict) {
				t.Fatalf("round %d: the loser = %v, want ErrConflict", round, loser)
			}
			want := []string{"u2=maintainer"}
			switch {
			case !promoted && round%2 == 0:
				want = []string{"u2=viewer"}
			case !promoted:
				want = []string{}
			}
			got := memberRoles(s, "p1")
			same(t, fmt.Sprintf("round %d", round), got, want)
			if len(got) == 1 {
				ok(t, s.DeleteMember(ctx, "p1", "u2", strings.TrimPrefix(got[0], "u2=")))
			}
		}
	})

	t.Run("access and reach: owned, shared, neither", func(t *testing.T) {
		s := seeded(t, open)
		withUsers(t, s, "u1", "u2", "u3")
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u2", "api", 1)))
		ok(t, s.UpsertProject(ctx, plainProject("p3", "u3", "ops", 2)))
		ok(t, s.PutMember(ctx, member("p2", "u1", confstore.MemberOperator, 0), ""))
		for _, tc := range []struct{ project, user, owner, role string }{
			{"p1", "u1", "u1", confstore.RoleOwner},
			{"p2", "u1", "u2", confstore.MemberOperator},
			{"p3", "u1", "u3", ""},
		} {
			owner, role, err := s.ProjectAccess(ctx, tc.project, tc.user)
			ok(t, err)
			same(t, tc.project+" owner", owner, tc.owner)
			same(t, tc.project+" role", role, tc.role)
		}
		_, _, err := s.ProjectAccess(ctx, "nope", "u1")
		is(t, "missing project", err, confstore.ErrNotFound)

		reach, err := s.ProjectsOf(ctx, "u1")
		ok(t, err)
		same(t, "reach", reach, []confstore.Reach{
			{ProjectID: "p1", OwnerID: "u1", Role: confstore.RoleOwner},
			{ProjectID: "p2", OwnerID: "u2", Role: confstore.MemberOperator},
		})
		none, err := s.ProjectsOf(ctx, "nobody")
		ok(t, err)
		same(t, "nobody's reach", len(none), 0)

		vis, err := s.ListVisibleProjects(ctx, "u1", []string{"p2", "nope"})
		ok(t, err)
		ids := []string{}
		for _, p := range vis {
			ids = append(ids, p.ID)
		}
		same(t, "own and listed", ids, []string{"p1", "p2"})
		vis, err = s.ListVisibleProjects(ctx, "", []string{"p3"})
		ok(t, err)
		same(t, "no owner matches nothing by owner", len(vis), 1)
		vis, err = s.ListVisibleProjects(ctx, "", nil)
		ok(t, err)
		same(t, "nothing", len(vis), 0)
	})

	t.Run("go with their project", func(t *testing.T) {
		s := seeded(t, open)
		withUsers(t, s, "u2")
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u1", "api", 1)))
		ok(t, s.PutMember(ctx, member("p1", "u2", confstore.MemberViewer, 0), ""))
		ok(t, s.PutMember(ctx, member("p2", "u2", confstore.MemberViewer, 0), ""))
		ok(t, s.DeleteProject(ctx, "p1"))
		same(t, "deleted project's", memberRoles(s, "p1"), []string{})
		same(t, "other project's", memberRoles(s, "p2"), []string{"u2=viewer"})
	})
}

func tickets(t *testing.T, open opener) {
	t.Run("roundtrip keeps every field", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		k := fullTicket()
		allSet(t, k)
		ok(t, s.UpsertTicket(ctx, k, confstore.Change{Kind: confstore.AuthorUser}))
		same(t, "get", s.ticket(k.ID), k)
		byRemote, err := s.GetTicketByRemote(ctx, "i9", "10001")
		ok(t, err)
		same(t, "by remote", byRemote, k)
		list, err := s.ListTicketsByRemote(ctx, "i9", []string{"10001"})
		ok(t, err)
		same(t, "list by remote", list, []confstore.Ticket{k})
	})

	t.Run("by last-seen key: a real issue's record first, then the newest", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		k := fullTicket()
		ok(t, s.UpsertTicket(ctx, k, confstore.Change{Kind: confstore.AuthorUser}))
		dup := fullTicket()
		dup.ID, dup.RemoteID, dup.CreatedAt, dup.UpdatedAt = "k2", "dup:k2", at(3), at(3)
		ok(t, s.UpsertTicket(ctx, dup, confstore.Change{Kind: confstore.AuthorUser}))
		got, err := s.GetTicketBySourceKey(ctx, "i9", "PROJ-1")
		ok(t, err)
		same(t, "over a placeholder", got.ID, "k1")
		newer := fullTicket()
		newer.ID, newer.RemoteID, newer.CreatedAt, newer.UpdatedAt = "k3", "10002", at(4), at(4)
		ok(t, s.UpsertTicket(ctx, newer, confstore.Change{Kind: confstore.AuthorUser}))
		got, err = s.GetTicketBySourceKey(ctx, "i9", "PROJ-1")
		ok(t, err)
		same(t, "newest", got.ID, "k3")
		_, err = s.GetTicketBySourceKey(ctx, "i9", "proj-1")
		is(t, "another case", err, confstore.ErrNotFound)
		_, err = s.GetTicketBySourceKey(ctx, "i1", "PROJ-1")
		is(t, "another source", err, confstore.ErrNotFound)
	})

	// The column default never applies: the write always names the column.
	// feat for "" is the Service's.
	t.Run("an empty change type stays empty", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertTicket(ctx, ticket("k1", "i1", "u1", 0), confstore.Change{Kind: confstore.AuthorUser}))
		same(t, "change type", s.ticket("k1").ChangeType, "")
	})

	t.Run("missing", func(t *testing.T) {
		s := store{open(t), t}
		_, err := s.GetTicket(ctx, "nope")
		is(t, "get", err, confstore.ErrNotFound)
		_, err = s.GetTicketByRemote(ctx, "i9", "nope")
		is(t, "by remote", err, confstore.ErrNotFound)
		is(t, "delete", s.DeleteTicket(ctx, "nope"), confstore.ErrNotFound)
		is(t, "seen", s.SetTicketSeen(ctx, "nope", confstore.TicketSeen{Title: "t"}, "", trackerport.ChangeFeat, at(1), confstore.Change{Kind: confstore.AuthorSync}), confstore.ErrNotFound)
	})

	t.Run("its status and type must exist", func(t *testing.T) {
		s := seeded(t, open)
		k := ticket("k1", "i1", "u1", 0)
		k.StatusID = "ghost"
		is(t, "status", s.UpsertTicket(ctx, k, confstore.Change{Kind: confstore.AuthorUser}), confstore.ErrInvalid)
		k.StatusID, k.TypeID = "", "ghost"
		is(t, "type", s.UpsertTicket(ctx, k, confstore.Change{Kind: confstore.AuthorUser}), confstore.ErrInvalid)
		if _, err := s.GetTicket(ctx, "k1"); !errors.Is(err, confstore.ErrNotFound) {
			t.Errorf("a refused insert landed: %v", err)
		}
	})

	// The owner is whatever the caller wrote: it is the Service that picks
	// it, not the store.
	t.Run("the owner is stored as given, on insert and on update", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertTicket(ctx, ticket("k1", "i1", "someone", 0), confstore.Change{Kind: confstore.AuthorUser}))
		same(t, "insert", s.ticket("k1").UserID, "someone")
		ok(t, s.UpsertTicket(ctx, ticket("k1", "i1", "else", 1), confstore.Change{Kind: confstore.AuthorUser}))
		same(t, "update", s.ticket("k1").UserID, "else")
	})

	t.Run("an update replaces all but created_at", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		in := fullTicket()
		ok(t, s.UpsertTicket(ctx, in, confstore.Change{Kind: confstore.AuthorUser}))
		upd := ticket("k1", "i8", "u2", 70)
		ok(t, s.UpsertTicket(ctx, upd, confstore.Change{Kind: confstore.AuthorUser}))
		want := upd
		want.CreatedAt = in.CreatedAt
		same(t, "after update", s.ticket("k1"), want)
	})

	// tickets_source_remote_key: one record of an issue per tracker, whoever
	// holds it; another tracker's issue of the same id is another record.
	t.Run("an issue is one record per tracker, across owners", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		a, b := fullTicket(), fullTicket()
		ok(t, s.UpsertTicket(ctx, a, confstore.Change{Kind: confstore.AuthorUser}))
		b.ID, b.UserID, b.SourceKey = "k2", "u2", "OTHER-1"
		is(t, "another owner", s.UpsertTicket(ctx, b, confstore.Change{Kind: confstore.AuthorUser}), confstore.ErrConflict)
		if _, err := s.GetTicket(ctx, "k2"); !errors.Is(err, confstore.ErrNotFound) {
			t.Errorf("a refused insert landed: %v", err)
		}
		c := ticket("k3", "i1", "u1", 1)
		ok(t, s.UpsertTicket(ctx, c, confstore.Change{Kind: confstore.AuthorUser}))
		c.RemoteID = "10001"
		ok(t, s.UpsertTicket(ctx, c, confstore.Change{Kind: confstore.AuthorUser})) // the same id on another tracker
		c.SourceID = "i9"
		is(t, "an update onto a held issue", s.UpsertTicket(ctx, c, confstore.Change{Kind: confstore.AuthorUser}), confstore.ErrConflict)
		same(t, "after the refused update", s.ticket("k3").SourceID, "i1")
	})

	t.Run("listed by remote id, of one tracker", func(t *testing.T) {
		s := seeded(t, open)
		for i, id := range []string{"r1", "r2", "r3"} {
			k := ticket("k"+id, "i9", "u1", i)
			k.RemoteID = id
			ok(t, s.UpsertTicket(ctx, k, confstore.Change{Kind: confstore.AuthorUser}))
		}
		other := ticket("kx", "i8", "u1", 0)
		other.RemoteID = "r1"
		ok(t, s.UpsertTicket(ctx, other, confstore.Change{Kind: confstore.AuthorUser}))
		list, err := s.ListTicketsByRemote(ctx, "i9", []string{"r3", "r1", "nope"})
		ok(t, err)
		got := ids(list, func(r confstore.Ticket) string { return r.ID })
		sort.Strings(got)
		same(t, "found", got, []string{"kr1", "kr3"})
		none, err := s.ListTicketsByRemote(ctx, "i9", nil)
		ok(t, err)
		same(t, "none asked", len(none), 0)
	})

	t.Run("an internal tracker's list pages newest first, by done and text", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertIntegration(ctx, confstore.StoredIntegration{ID: "i8", UserID: "u1", Port: confstore.PortTicketSource,
			Kind: confstore.KindInternal, Name: "internal", Config: map[string]any{}, Sealed: []byte{1}, CreatedAt: at(0), UpdatedAt: at(0)}))
		ok(t, s.ReplaceTrackerStatuses(ctx, "i8", []confstore.TrackerStatus{
			{ID: "open", IntegrationID: "i8", RemoteID: "open", Name: "To do"},
			{ID: "done", IntegrationID: "i8", RemoteID: "done", Name: "Done", Position: 1, IsDone: true},
		}, nil))
		add := func(id, status, title, goal string, n int) {
			k := ticket(id, "i8", "u1", n)
			k.StatusID, k.Title, k.Goal = status, title, goal
			ok(t, s.UpsertTicket(ctx, k, confstore.Change{Kind: confstore.AuthorUser}))
		}
		add("a1", "open", "Login page", "", 1)
		add("a2", "done", "Logout", "", 2)
		add("a3", "", "Search", "find the LOGIN form", 3)
		add("a5", "open", "Billing", "", 4)
		add("a4", "done", "Export", "", 4) // same created_at as a5: id breaks the tie
		ok(t, s.UpsertTicket(ctx, ticket("b1", "i9", "u1", 5), confstore.Change{Kind: confstore.AuthorUser}))
		list := func(q confstore.InternalTicketQuery) []string {
			t.Helper()
			rows, err := s.ListInternalTickets(ctx, "i8", q)
			ok(t, err)
			return ids(rows, func(r confstore.Ticket) string { return r.ID })
		}
		same(t, "all", list(confstore.InternalTicketQuery{}), []string{"a5", "a4", "a3", "a2", "a1"})
		same(t, "open", list(confstore.InternalTicketQuery{Done: trackerport.DoneOpen}), []string{"a5", "a3", "a1"})
		same(t, "done", list(confstore.InternalTicketQuery{Done: trackerport.DoneOnly}), []string{"a4", "a2"})
		same(t, "text", list(confstore.InternalTicketQuery{Text: "login"}), []string{"a3", "a1"})
		same(t, "text in the id", list(confstore.InternalTicketQuery{Text: "A4"}), []string{"a4"})
		same(t, "first page", list(confstore.InternalTicketQuery{Limit: 2}), []string{"a5", "a4"})
		same(t, "next page", list(confstore.InternalTicketQuery{Limit: 2, After: &confstore.TicketCursor{CreatedAt: at(4), ID: "a4"}}),
			[]string{"a3", "a2"})
		same(t, "inside a tie", list(confstore.InternalTicketQuery{After: &confstore.TicketCursor{CreatedAt: at(4), ID: "a5"}}),
			[]string{"a4", "a3", "a2", "a1"})
		same(t, "open after", list(confstore.InternalTicketQuery{Done: trackerport.DoneOpen, After: &confstore.TicketCursor{CreatedAt: at(3), ID: "a3"}}),
			[]string{"a1"})
		for i := range 55 {
			ok(t, s.UpsertTicket(ctx, ticket(fmt.Sprintf("m%02d", i), "i8", "u1", 10), confstore.Change{Kind: confstore.AuthorUser}))
		}
		same(t, "default limit", len(list(confstore.InternalTicketQuery{})), 50)
	})

	t.Run("what was seen is written, the remote id kept", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		k := fullTicket()
		ok(t, s.UpsertTicket(ctx, k, confstore.Change{Kind: confstore.AuthorUser}))
		seen := confstore.TicketSeen{RemoteID: "ignored", Key: "NEW-7", URL: "https://example.test/7", Title: "Moved", Goal: "New goal",
			Type: "Story", TypeRemoteID: "11", Labels: []string{"x"}}
		ok(t, s.SetTicketSeen(ctx, "k1", seen, "", trackerport.ChangeChore, at(9), confstore.Change{Kind: confstore.AuthorSync}))
		want := k
		want.Title, want.Goal, want.SourceKey, want.SourceURL, want.SourceType = "Moved", "New goal", "NEW-7", "https://example.test/7", "Story"
		want.TypeID, want.ChangeType, want.Labels, want.UpdatedAt = "", trackerport.ChangeChore, []string{"x"}, at(9)
		same(t, "seen", s.ticket("k1"), want)
		ok(t, s.SetTicketSeen(ctx, "k1", seen, "ty1", trackerport.ChangeFix, at(10), confstore.Change{Kind: confstore.AuthorSync}))
		same(t, "type", s.ticket("k1").TypeID, "ty1")
		is(t, "missing type", s.SetTicketSeen(ctx, "k1", seen, "ghost", trackerport.ChangeFix, at(11), confstore.Change{Kind: confstore.AuthorSync}), confstore.ErrInvalid)
	})

	t.Run("delete goes once", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertTicket(ctx, ticket("k1", "i1", "u1", 0), confstore.Change{Kind: confstore.AuthorUser}))
		ok(t, s.DeleteTicket(ctx, "k1"))
		is(t, "second delete", s.DeleteTicket(ctx, "k1"), confstore.ErrNotFound)
	})
}

func flows(t *testing.T, open opener) {
	t.Run("roundtrip keeps every field", func(t *testing.T) {
		s := seeded(t, open)
		r := flowRow("p1", "main", "h1", 1)
		r.Layout = json.RawMessage(canonicalLayout)
		allSet(t, r)
		ok(t, s.UpsertFlow(ctx, r))
		same(t, "get", s.flow("p1", "main"), r)
		list, err := s.ListFlows(ctx, "p1")
		ok(t, err)
		same(t, "list", list, []confstore.FlowRow{r})
	})

	t.Run("missing", func(t *testing.T) {
		s := seeded(t, open)
		_, err := s.GetFlow(ctx, "p1", "nope")
		is(t, "get", err, confstore.ErrNotFound)
		is(t, "delete", s.DeleteFlow(ctx, "p1", "nope"), confstore.ErrNotFound)
	})

	t.Run("its project must exist", func(t *testing.T) {
		s := seeded(t, open)
		refused(t, "upsert", s.UpsertFlow(ctx, flowRow("ghost", "main", "h1", 0)))
		_, err := s.GetFlow(ctx, "ghost", "main")
		is(t, "row", err, confstore.ErrNotFound)
		_, err = s.GetFlowVersion(ctx, "ghost", "h1")
		is(t, "version", err, confstore.ErrNotFound)
	})

	t.Run("no layout stays nil", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "main", "h1", 0)))
		same(t, "nil", s.flow("p1", "main").Layout, nil)
		empty := flowRow("p1", "other", "h2", 0)
		empty.Layout = json.RawMessage{}
		ok(t, s.UpsertFlow(ctx, empty))
		same(t, "empty", s.flow("p1", "other").Layout, nil)
	})

	// A YAML-only save must not wipe node positions.
	t.Run("a save without a layout keeps the stored one", func(t *testing.T) {
		s := seeded(t, open)
		r := flowRow("p1", "main", "h1", 0)
		r.Layout = json.RawMessage(canonicalLayout)
		ok(t, s.UpsertFlow(ctx, r))
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "main", "h2", 1)))
		same(t, "nil", string(s.flow("p1", "main").Layout), canonicalLayout)
		empty := flowRow("p1", "main", "h3", 2)
		empty.Layout = json.RawMessage{}
		ok(t, s.UpsertFlow(ctx, empty))
		got := s.flow("p1", "main")
		same(t, "empty", string(got.Layout), canonicalLayout)
		same(t, "the rest moved on", got.Hash, "h3")
	})

	t.Run("a save with a layout replaces it", func(t *testing.T) {
		s := seeded(t, open)
		r := flowRow("p1", "main", "h1", 0)
		r.Layout = json.RawMessage(canonicalLayout)
		ok(t, s.UpsertFlow(ctx, r))
		r.Layout = json.RawMessage(`{"z": 1}`)
		ok(t, s.UpsertFlow(ctx, r))
		same(t, "replaced", string(s.flow("p1", "main").Layout), `{"z": 1}`)
		r.Layout = json.RawMessage(`null`) // a JSON null is a value, not "none"
		ok(t, s.UpsertFlow(ctx, r))
		same(t, "json null", string(s.flow("p1", "main").Layout), `null`)
	})

	t.Run("a layout is JSON", func(t *testing.T) {
		s := seeded(t, open)
		r := flowRow("p1", "main", "h1", 0)
		r.Layout = json.RawMessage(`{"b":2,  "a":[1,2], "b":3}`)
		ok(t, s.UpsertFlow(ctx, r))
		sameJSON(t, "stored", string(s.flow("p1", "main").Layout), `{"a":[1,2],"b":3}`)

		bad := flowRow("p1", "bad", "h2", 0)
		bad.Layout = json.RawMessage(`nope`)
		refused(t, "not JSON", s.UpsertFlow(ctx, bad))
		_, err := s.GetFlow(ctx, "p1", "bad")
		is(t, "row", err, confstore.ErrNotFound)
		_, err = s.GetFlowVersion(ctx, "p1", "h2")
		is(t, "version", err, confstore.ErrNotFound)
	})

	t.Run("a name means one document per project", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u1", "api", 1)))
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "main", "h1", 0)))
		ok(t, s.UpsertFlow(ctx, flowRow("p2", "main", "h2", 1)))
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "main", "h3", 2)))
		same(t, "p1", s.flow("p1", "main"), flowRow("p1", "main", "h3", 2))
		same(t, "p2", s.flow("p2", "main"), flowRow("p2", "main", "h2", 1))
	})

	t.Run("lists by name; everyone's by project, then name", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u1", "api", 1)))
		ok(t, s.UpsertFlow(ctx, flowRow("p2", "beta", "h1", 0)))
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "gamma", "h2", 1)))
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "alpha", "h3", 2)))
		ok(t, s.UpsertFlow(ctx, flowRow("p2", "alpha", "h4", 3)))
		same(t, "p1", flowNames(s, "p1"), []string{"p1/alpha", "p1/gamma"})
		same(t, "everyone", flowNames(s, ""), []string{"p1/alpha", "p1/gamma", "p2/alpha", "p2/beta"})
		none, err := s.ListFlows(ctx, "nobody")
		ok(t, err)
		same(t, "nobody", none, nil)
	})

	t.Run("delete is refused while its project defaults to it", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "main", "h1", 0)))
		p := s.project("p1")
		p.DefaultFlow = "main"
		ok(t, s.UpsertProject(ctx, p))
		err := s.DeleteFlow(ctx, "p1", "main")
		is(t, "delete", err, confstore.ErrReferenced)
		names(t, "delete", err, "web")
		s.flow("p1", "main")
	})

	t.Run("a default a template answers to does not hold back the reset", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertFlow(ctx, flowRow("p1", template(), "h1", 0)))
		p := s.project("p1")
		p.DefaultFlow = template()
		ok(t, s.UpsertProject(ctx, p))
		ok(t, s.DeleteFlow(ctx, "p1", template()))
		is(t, "the copy is gone", s.DeleteFlow(ctx, "p1", template()), confstore.ErrNotFound)
		if got := s.project("p1").DefaultFlow; got != template() {
			t.Errorf("default = %q", got)
		}
	})

	t.Run("another project's default of that name is another document", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "main", "h1", 0)))
		p := s.project("p1")
		p.DefaultFlow = "main"
		ok(t, s.UpsertProject(ctx, p))
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u1", "api", 1)))
		ok(t, s.UpsertFlow(ctx, flowRow("p2", "main", "h2", 2)))
		ok(t, s.DeleteFlow(ctx, "p2", "main"))
	})

	t.Run("delete goes once and leaves its bindings", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "main", "h1", 0)))
		ok(t, s.PutFlowBinding(ctx, confstore.FlowBinding{ProjectID: "p1", FlowName: "main", UpdatedAt: at(0)}))
		ok(t, s.DeleteFlow(ctx, "p1", "main"))
		is(t, "second delete", s.DeleteFlow(ctx, "p1", "main"), confstore.ErrNotFound)
		same(t, "bindings", bindingNames(s, "p1"), []string{"main"})
	})
}

func flowVersions(t *testing.T, open opener) {
	t.Run("a save records the version", func(t *testing.T) {
		s := seeded(t, open)
		r := flowRow("p1", "main", "h1", 3)
		ok(t, s.UpsertFlow(ctx, r))
		want := confstore.FlowVersion{ProjectID: "p1", Hash: "h1", Name: "main", YAML: r.YAML, CreatedAt: r.UpdatedAt}
		allSet(t, want)
		got, err := s.GetFlowVersion(ctx, "p1", "h1")
		ok(t, err)
		same(t, "get", got, want)
		list, err := s.ListFlowVersions(ctx, "p1", "main")
		ok(t, err)
		same(t, "list", list, []confstore.FlowVersion{want})
	})

	t.Run("missing", func(t *testing.T) {
		s := seeded(t, open)
		_, err := s.GetFlowVersion(ctx, "p1", "nope")
		is(t, "get", err, confstore.ErrNotFound)
		none, err := s.ListFlowVersions(ctx, "p1", "nope")
		ok(t, err)
		same(t, "list", none, nil)
	})

	t.Run("a hash is saved once, as it first was", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "main", "h1", 1)))
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "main", "h2", 3)))
		again := flowRow("p1", "main", "h1", 5)
		again.YAML = "edited"
		ok(t, s.UpsertFlow(ctx, again))
		same(t, "current", s.flow("p1", "main"), again)
		v, err := s.GetFlowVersion(ctx, "p1", "h1")
		ok(t, err)
		same(t, "yaml", v.YAML, flowRow("p1", "main", "h1", 1).YAML)
		same(t, "created_at", v.CreatedAt, at(1))
	})

	t.Run("lists newest first", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "main", "h1", 2)))
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "main", "h2", 9)))
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "main", "h3", 1)))
		same(t, "versions", versionHashes(s, "p1", "main"), []string{"h2", "h1", "h3"})
	})

	t.Run("a version belongs to the name that first saved it", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "first", "h1", 0)))
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "second", "h1", 1)))
		same(t, "first", versionHashes(s, "p1", "first"), []string{"h1"})
		same(t, "second", versionHashes(s, "p1", "second"), []string{})
	})

	t.Run("the same hash in two projects is two histories", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u1", "api", 1)))
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "main", "h1", 1)))
		ok(t, s.UpsertFlow(ctx, flowRow("p2", "main", "h1", 2)))
		v, err := s.GetFlowVersion(ctx, "p2", "h1")
		ok(t, err)
		same(t, "own created_at", v.CreatedAt, at(2))
	})

	t.Run("deleting the flow keeps them", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertFlow(ctx, flowRow("p1", "main", "h1", 1)))
		ok(t, s.DeleteFlow(ctx, "p1", "main"))
		same(t, "versions", versionHashes(s, "p1", "main"), []string{"h1"})
	})
}

func bindings(t *testing.T, open opener) {
	t.Run("roundtrip keeps every field", func(t *testing.T) {
		s := seeded(t, open)
		b := confstore.FlowBinding{ProjectID: "p1", FlowName: "main", Params: map[string]string{"a": "b"}, UpdatedAt: at(1)}
		allSet(t, b)
		ok(t, s.PutFlowBinding(ctx, b))
		got, err := s.GetFlowBinding(ctx, "p1", "main")
		ok(t, err)
		same(t, "get", got, b)
		list, err := s.ListFlowBindings(ctx, "p1")
		ok(t, err)
		same(t, "list", list, []confstore.FlowBinding{b})
	})

	t.Run("nil and empty params", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.PutFlowBinding(ctx, confstore.FlowBinding{ProjectID: "p1", FlowName: "none", UpdatedAt: at(0)}))
		got, err := s.GetFlowBinding(ctx, "p1", "none")
		ok(t, err)
		same(t, "nil", got.Params, nil)
		ok(t, s.PutFlowBinding(ctx, confstore.FlowBinding{ProjectID: "p1", FlowName: "empty", Params: map[string]string{}, UpdatedAt: at(0)}))
		got, err = s.GetFlowBinding(ctx, "p1", "empty")
		ok(t, err)
		same(t, "empty", got.Params, map[string]string{})
	})

	t.Run("a put replaces", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.PutFlowBinding(ctx, confstore.FlowBinding{ProjectID: "p1", FlowName: "main", Params: map[string]string{"a": "b"}, UpdatedAt: at(0)}))
		b := confstore.FlowBinding{ProjectID: "p1", FlowName: "main", Params: map[string]string{"c": "d"}, UpdatedAt: at(1)}
		ok(t, s.PutFlowBinding(ctx, b))
		got, err := s.GetFlowBinding(ctx, "p1", "main")
		ok(t, err)
		same(t, "get", got, b)
		same(t, "one row", bindingNames(s, "p1"), []string{"main"})
	})

	t.Run("its project must exist, its flow need not", func(t *testing.T) {
		s := seeded(t, open)
		refused(t, "ghost project", s.PutFlowBinding(ctx, confstore.FlowBinding{ProjectID: "ghost", FlowName: "main", UpdatedAt: at(0)}))
		_, err := s.GetFlowBinding(ctx, "ghost", "main")
		is(t, "row", err, confstore.ErrNotFound)
		ok(t, s.PutFlowBinding(ctx, confstore.FlowBinding{ProjectID: "p1", FlowName: "unsaved", UpdatedAt: at(0)}))
	})

	t.Run("missing", func(t *testing.T) {
		s := seeded(t, open)
		_, err := s.GetFlowBinding(ctx, "p1", "nope")
		is(t, "get", err, confstore.ErrNotFound)
		is(t, "delete", s.DeleteFlowBinding(ctx, "p1", "nope"), confstore.ErrNotFound)
		none, err := s.ListFlowBindings(ctx, "p1")
		ok(t, err)
		same(t, "list", none, nil)
	})

	t.Run("lists by flow name, per project", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u1", "api", 1)))
		for _, b := range [][2]string{{"p1", "gamma"}, {"p2", "beta"}, {"p1", "alpha"}} {
			ok(t, s.PutFlowBinding(ctx, confstore.FlowBinding{ProjectID: b[0], FlowName: b[1], UpdatedAt: at(0)}))
		}
		same(t, "p1", bindingNames(s, "p1"), []string{"alpha", "gamma"})
		same(t, "p2", bindingNames(s, "p2"), []string{"beta"})
		ok(t, s.DeleteFlowBinding(ctx, "p1", "alpha"))
		is(t, "second delete", s.DeleteFlowBinding(ctx, "p1", "alpha"), confstore.ErrNotFound)
		same(t, "after delete", bindingNames(s, "p1"), []string{"gamma"})
	})
}

func secrets(t *testing.T, open opener) {
	t.Run("roundtrip keeps every field", func(t *testing.T) {
		s := seeded(t, open)
		sec := confstore.StoredProjectSecret{Name: "token", Sealed: []byte{1, 2}, UpdatedAt: at(1)}
		allSet(t, sec)
		ok(t, s.PutProjectSecret(ctx, "p1", sec))
		list, err := s.ListProjectSecrets(ctx, "p1")
		ok(t, err)
		same(t, "list", list, []confstore.StoredProjectSecret{sec})
	})

	t.Run("a put replaces", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.PutProjectSecret(ctx, "p1", confstore.StoredProjectSecret{Name: "token", Sealed: []byte{1}, UpdatedAt: at(0)}))
		sec := confstore.StoredProjectSecret{Name: "token", Sealed: []byte{2}, UpdatedAt: at(1)}
		ok(t, s.PutProjectSecret(ctx, "p1", sec))
		list, err := s.ListProjectSecrets(ctx, "p1")
		ok(t, err)
		same(t, "list", list, []confstore.StoredProjectSecret{sec})
	})

	t.Run("a value is required, an empty one will do", func(t *testing.T) {
		s := seeded(t, open)
		refused(t, "nil", s.PutProjectSecret(ctx, "p1", confstore.StoredProjectSecret{Name: "token", UpdatedAt: at(0)}))
		same(t, "after the refused insert", secretNames(s, "p1"), []string{})

		ok(t, s.PutProjectSecret(ctx, "p1", confstore.StoredProjectSecret{Name: "token", Sealed: []byte{}, UpdatedAt: at(0)}))
		list, err := s.ListProjectSecrets(ctx, "p1")
		ok(t, err)
		same(t, "empty", list[0].Sealed, []byte{})

		refused(t, "nil on update", s.PutProjectSecret(ctx, "p1", confstore.StoredProjectSecret{Name: "token", UpdatedAt: at(1)}))
	})

	t.Run("its project must exist", func(t *testing.T) {
		s := seeded(t, open)
		is(t, "put", s.PutProjectSecret(ctx, "ghost", confstore.StoredProjectSecret{Name: "token", Sealed: []byte{1}, UpdatedAt: at(0)}), confstore.ErrInvalid)
		same(t, "row", secretNames(s, "ghost"), []string{})
	})

	t.Run("missing", func(t *testing.T) {
		s := seeded(t, open)
		is(t, "delete", s.DeleteProjectSecret(ctx, "p1", "nope"), confstore.ErrNotFound)
		none, err := s.ListProjectSecrets(ctx, "p1")
		ok(t, err)
		same(t, "list", none, nil)
	})

	t.Run("lists by name, per project", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u1", "api", 1)))
		for _, sec := range [][2]string{{"p1", "gamma"}, {"p2", "beta"}, {"p1", "alpha"}} {
			ok(t, s.PutProjectSecret(ctx, sec[0], confstore.StoredProjectSecret{Name: sec[1], Sealed: []byte{1}, UpdatedAt: at(0)}))
		}
		same(t, "p1", secretNames(s, "p1"), []string{"alpha", "gamma"})
		same(t, "p2", secretNames(s, "p2"), []string{"beta"})
		ok(t, s.DeleteProjectSecret(ctx, "p1", "alpha"))
		is(t, "second delete", s.DeleteProjectSecret(ctx, "p1", "alpha"), confstore.ErrNotFound)
		is(t, "another project's", s.DeleteProjectSecret(ctx, "p1", "beta"), confstore.ErrNotFound)
		same(t, "after delete", secretNames(s, "p1"), []string{"gamma"})
	})
}

func automations(t *testing.T, open opener) {
	t.Run("roundtrip keeps every field", func(t *testing.T) {
		s := seeded(t, open)
		a := fullAutomation()
		allSet(t, a)
		ok(t, s.UpsertAutomation(ctx, a))
		same(t, "get", s.automation(a.ID), a)
		list, err := s.ListAutomations(ctx, "p1")
		ok(t, err)
		same(t, "list", list, []confstore.Automation{a})
	})

	t.Run("no marks and no params read back nil", func(t *testing.T) {
		s := seeded(t, open)
		a := automation("a1", "p1", 0)
		ok(t, s.UpsertAutomation(ctx, a))
		same(t, "nil params", s.automation("a1"), a)
		e := automation("a2", "p1", 0)
		e.Params = map[string]string{}
		ok(t, s.UpsertAutomation(ctx, e))
		same(t, "empty params", s.automation("a2").Params, nil)
	})

	t.Run("missing", func(t *testing.T) {
		s := store{open(t), t}
		_, err := s.GetAutomation(ctx, "nope")
		is(t, "get", err, confstore.ErrNotFound)
		is(t, "delete", s.DeleteAutomation(ctx, "nope"), confstore.ErrNotFound)
	})

	t.Run("its project must exist", func(t *testing.T) {
		s := seeded(t, open)
		is(t, "insert", s.UpsertAutomation(ctx, automation("a1", "ghost", 0)), confstore.ErrInvalid)
		_, err := s.GetAutomation(ctx, "a1")
		is(t, "row", err, confstore.ErrNotFound)
	})

	t.Run("the kind is one the table knows", func(t *testing.T) {
		s := seeded(t, open)
		for i, kind := range confstore.AutomationKinds {
			a := automation(fmt.Sprintf("a%d", i), "p1", 0)
			a.Kind = kind
			ok(t, s.UpsertAutomation(ctx, a))
		}
		bad := automation("bad", "p1", 0)
		bad.Kind = "nope"
		refused(t, "unknown", s.UpsertAutomation(ctx, bad))
		bad.Kind = ""
		refused(t, "empty", s.UpsertAutomation(ctx, bad))
		_, err := s.GetAutomation(ctx, "bad")
		is(t, "row", err, confstore.ErrNotFound)

		upd := automation("a0", "p1", 1)
		upd.Kind = "nope"
		refused(t, "on update", s.UpsertAutomation(ctx, upd))
		same(t, "after the refused update", s.automation("a0").Kind, confstore.AutomationKinds[0])
	})

	t.Run("what it names need not exist", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertTicket(ctx, ticket("k1", "i1", "u1", 0), confstore.Change{Kind: confstore.AuthorUser}))
		a := fullAutomation()
		a.TicketID, a.Flow = "k1", "ghost"
		ok(t, s.UpsertAutomation(ctx, a))
		ok(t, s.DeleteTicket(ctx, "k1"))
		same(t, "ticket id", s.automation("a1").TicketID, "k1")
	})

	t.Run("an update replaces all but the project and created_at", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u1", "api", 1)))
		in := fullAutomation()
		ok(t, s.UpsertAutomation(ctx, in))
		upd := automation("a1", "p2", 70)
		ok(t, s.UpsertAutomation(ctx, upd))
		want := upd
		want.ProjectID, want.CreatedAt = in.ProjectID, in.CreatedAt
		same(t, "after update", s.automation("a1"), want)
		same(t, "payload project's list", automationIDs(s, "p2"), []string{})
	})

	// Only an insert writes the project, so only an insert checks it.
	t.Run("an update ignores a project that doesn't exist", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertAutomation(ctx, automation("a1", "p1", 0)))
		ok(t, s.UpsertAutomation(ctx, automation("a1", "ghost", 1)))
		same(t, "project", s.automation("a1").ProjectID, "p1")
	})

	t.Run("lists by created_at then id, per project or everyone's", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertProject(ctx, plainProject("p2", "u1", "api", 1)))
		ok(t, s.UpsertAutomation(ctx, automation("a4", "p1", 3)))
		ok(t, s.UpsertAutomation(ctx, automation("a3", "p2", 2)))
		ok(t, s.UpsertAutomation(ctx, automation("a2", "p1", 1)))
		ok(t, s.UpsertAutomation(ctx, automation("a1", "p1", 1)))
		same(t, "p1", automationIDs(s, "p1"), []string{"a1", "a2", "a4"})
		same(t, "everyone", automationIDs(s, ""), []string{"a1", "a2", "a3", "a4"})
		none, err := s.ListAutomations(ctx, "nobody")
		ok(t, err)
		same(t, "nobody", none, nil)
	})

	t.Run("delete goes once", func(t *testing.T) {
		s := seeded(t, open)
		ok(t, s.UpsertAutomation(ctx, automation("a1", "p1", 0)))
		ok(t, s.DeleteAutomation(ctx, "a1"))
		is(t, "second delete", s.DeleteAutomation(ctx, "a1"), confstore.ErrNotFound)
	})
}

func settings(t *testing.T, open opener) {
	t.Run("a user with none reads an empty map", func(t *testing.T) {
		s := store{open(t), t}
		got, err := s.GetSettings(ctx, "nobody")
		ok(t, err)
		same(t, "settings", got, map[string]string{})
	})

	t.Run("per user, last write wins", func(t *testing.T) {
		s := store{open(t), t}
		ok(t, s.PutSetting(ctx, "u1", "k", `"one"`))
		ok(t, s.PutSetting(ctx, "u1", "j", `true`))
		ok(t, s.PutSetting(ctx, "u2", "k", `"two"`))
		ok(t, s.PutSetting(ctx, "u1", "k", `"three"`))
		got, err := s.GetSettings(ctx, "u1")
		ok(t, err)
		same(t, "u1", got, map[string]string{"k": `"three"`, "j": `true`})
		got, err = s.GetSettings(ctx, "u2")
		ok(t, err)
		same(t, "u2", got, map[string]string{"k": `"two"`})
	})

	// Unlike every list here, "" is a key of its own: the instance's row.
	t.Run("the empty user is one user, not everyone", func(t *testing.T) {
		s := store{open(t), t}
		ok(t, s.PutSetting(ctx, "u1", "k", `1`))
		ok(t, s.PutSetting(ctx, "", "instance", `2`))
		got, err := s.GetSettings(ctx, "")
		ok(t, err)
		same(t, "instance", got, map[string]string{"instance": `2`})
	})

	t.Run("a value is JSON", func(t *testing.T) {
		s := store{open(t), t}
		ok(t, s.PutSetting(ctx, "u1", "k", `{"b":2,  "a":[1,2], "b":3}`))
		refused(t, "not JSON", s.PutSetting(ctx, "u1", "k", `nope`))
		refused(t, "empty", s.PutSetting(ctx, "u1", "k", ``))
		got, err := s.GetSettings(ctx, "u1")
		ok(t, err)
		sameJSON(t, "kept", got["k"], `{"a":[1,2],"b":3}`)
	})
}

func credentials(t *testing.T, open opener) {
	t.Run("roundtrip keeps every field", func(t *testing.T) {
		s := store{open(t), t}
		c := fullCredential()
		allSet(t, c)
		ok(t, s.UpsertCredential(ctx, c))
		same(t, "get", s.credential(c.ID), c)
		list, err := s.ListCredentials(ctx, c.UserID)
		ok(t, err)
		same(t, "list", list, []confstore.ProviderCredential{c})
	})

	t.Run("nil and empty", func(t *testing.T) {
		s := store{open(t), t}
		c := credential("c1", "u1", 0)
		c.Sealed, c.Meta = nil, confstore.CredentialMeta{}
		ok(t, s.UpsertCredential(ctx, c))
		got := s.credential("c1")
		same(t, "nil secrets", got.Sealed, []byte{})
		same(t, "zero meta", got.Meta, confstore.CredentialMeta{})

		e := credential("c2", "u1", 0)
		e.Sealed = []byte{}
		ok(t, s.UpsertCredential(ctx, e))
		same(t, "empty secrets", s.credential("c2").Sealed, []byte{})
		list, err := s.ListCredentials(ctx, "u1")
		ok(t, err)
		same(t, "nil secrets, listed", list[0].Sealed, []byte{})
	})

	t.Run("missing", func(t *testing.T) {
		s := store{open(t), t}
		_, err := s.GetCredential(ctx, "nope")
		is(t, "get", err, confstore.ErrNotFound)
		is(t, "delete", s.DeleteCredential(ctx, "nope"), confstore.ErrNotFound)
	})

	t.Run("nothing makes a name unique", func(t *testing.T) {
		s := store{open(t), t}
		ok(t, s.UpsertCredential(ctx, credential("c1", "u1", 0)))
		ok(t, s.UpsertCredential(ctx, credential("c2", "u1", 1)))
	})

	t.Run("an update sets name, secrets, meta and updated_at, nothing else", func(t *testing.T) {
		s := store{open(t), t}
		in := fullCredential()
		ok(t, s.UpsertCredential(ctx, in))
		upd := in
		upd.UserID, upd.Provider, upd.Kind, upd.Name = "u2", "other", providers.KindAPIKey, "renamed"
		upd.Sealed, upd.Meta = []byte("new"), confstore.CredentialMeta{Status: confstore.CredentialOK}
		upd.CreatedAt, upd.UpdatedAt = at(50), at(60)
		ok(t, s.UpsertCredential(ctx, upd))
		want := in
		want.Name, want.Sealed, want.Meta, want.UpdatedAt = upd.Name, upd.Sealed, upd.Meta, upd.UpdatedAt
		same(t, "after update", s.credential("c1"), want)
		same(t, "payload owner's list", credentialIDs(s, "u2"), []string{})
	})

	t.Run("lists by created_at, one owner or everyone", func(t *testing.T) {
		s := store{open(t), t}
		ok(t, s.UpsertCredential(ctx, credential("c1", "u1", 3)))
		ok(t, s.UpsertCredential(ctx, credential("c2", "u2", 2)))
		ok(t, s.UpsertCredential(ctx, credential("c3", "u1", 1)))
		same(t, "u1", credentialIDs(s, "u1"), []string{"c3", "c1"})
		same(t, "everyone", credentialIDs(s, ""), []string{"c3", "c2", "c1"})
		none, err := s.ListCredentials(ctx, "nobody")
		ok(t, err)
		same(t, "nobody", none, nil)
	})

	t.Run("delete goes once", func(t *testing.T) {
		s := store{open(t), t}
		ok(t, s.UpsertCredential(ctx, credential("c1", "u1", 0)))
		ok(t, s.DeleteCredential(ctx, "c1"))
		is(t, "second delete", s.DeleteCredential(ctx, "c1"), confstore.ErrNotFound)
	})
}

func timestamps(t *testing.T, open opener) {
	// timestamptz keeps microseconds; pgx truncates what's finer, and the zone
	// is not stored.
	t.Run("every column keeps the instant, to the microsecond", func(t *testing.T) {
		s := seeded(t, open)
		finer := epoch.Add(123456789 * time.Nanosecond).In(time.FixedZone("east", 5*60*60))
		want := epoch.Add(123456 * time.Microsecond)
		check := func(what string, got time.Time) {
			t.Helper()
			if !got.Equal(want) {
				t.Errorf("%s = %v, want %v", what, got, want)
			}
		}

		in := repoAccount("i2", "u1", "other", 0)
		in.CreatedAt, in.UpdatedAt = finer, finer
		ok(t, s.UpsertIntegration(ctx, in))
		gi := s.integration("i2")
		check("integration created_at", gi.CreatedAt)
		check("integration updated_at", gi.UpdatedAt)

		p := plainProject("p2", "u1", "api", 0)
		p.CreatedAt, p.UpdatedAt = finer, finer
		ok(t, s.UpsertProject(ctx, p))
		gp := s.project("p2")
		check("project created_at", gp.CreatedAt)
		check("project updated_at", gp.UpdatedAt)

		k := ticket("k1", "i1", "u1", 0)
		k.CreatedAt, k.UpdatedAt = finer, finer
		ok(t, s.UpsertTicket(ctx, k, confstore.Change{Kind: confstore.AuthorUser}))
		gk := s.ticket("k1")
		check("ticket created_at", gk.CreatedAt)
		check("ticket updated_at", gk.UpdatedAt)

		f := flowRow("p1", "main", "h1", 0)
		f.UpdatedAt = finer
		ok(t, s.UpsertFlow(ctx, f))
		check("flow updated_at", s.flow("p1", "main").UpdatedAt)
		v, err := s.GetFlowVersion(ctx, "p1", "h1")
		ok(t, err)
		check("flow version created_at", v.CreatedAt)

		ok(t, s.PutFlowBinding(ctx, confstore.FlowBinding{ProjectID: "p1", FlowName: "main", UpdatedAt: finer}))
		b, err := s.GetFlowBinding(ctx, "p1", "main")
		ok(t, err)
		check("binding updated_at", b.UpdatedAt)

		ok(t, s.PutProjectSecret(ctx, "p1", confstore.StoredProjectSecret{Name: "token", Sealed: []byte{1}, UpdatedAt: finer}))
		list, err := s.ListProjectSecrets(ctx, "p1")
		ok(t, err)
		check("secret updated_at", list[0].UpdatedAt)

		a := automation("a1", "p1", 0)
		polled, fired := finer, finer
		a.CreatedAt, a.UpdatedAt, a.LastPolledAt, a.LastFiredAt = finer, finer, &polled, &fired
		ok(t, s.UpsertAutomation(ctx, a))
		ga := s.automation("a1")
		check("automation created_at", ga.CreatedAt)
		check("automation updated_at", ga.UpdatedAt)
		check("automation last_polled_at", *ga.LastPolledAt)
		check("automation last_fired_at", *ga.LastFiredAt)

		c := credential("c1", "u1", 0)
		c.CreatedAt, c.UpdatedAt = finer, finer
		ok(t, s.UpsertCredential(ctx, c))
		gc := s.credential("c1")
		check("credential created_at", gc.CreatedAt)
		check("credential updated_at", gc.UpdatedAt)
	})

	t.Run("a zero time stays zero", func(t *testing.T) {
		s := store{open(t), t}
		in := repoAccount("i1", "u1", "main", 0)
		in.CreatedAt, in.UpdatedAt = time.Time{}, time.Time{}
		ok(t, s.UpsertIntegration(ctx, in))
		if got := s.integration("i1"); !got.CreatedAt.IsZero() || !got.UpdatedAt.IsZero() {
			t.Errorf("zero time = %v, %v", got.CreatedAt, got.UpdatedAt)
		}
	})

	// Meta is jsonb, so its times are strings and keep every digit.
	t.Run("credential meta times keep their nanoseconds", func(t *testing.T) {
		s := store{open(t), t}
		finer := epoch.Add(123456789 * time.Nanosecond)
		c := credential("c1", "u1", 0)
		c.Meta.ExpiresAt, c.Meta.LastRefresh = finer, finer
		ok(t, s.UpsertCredential(ctx, c))
		same(t, "meta", s.credential("c1").Meta, c.Meta)
	})
}

// Postgres shares nothing with its callers by construction; Mem has to work
// at it, and a test that mutates a row it read must not rewrite the store.
func sharedMemory(t *testing.T, open opener) {
	t.Run("integration", func(t *testing.T) {
		s := store{open(t), t}
		in := fullIntegration()
		ok(t, s.UpsertIntegration(ctx, in))
		in.Sealed[0], in.Config["url"] = 9, "x"
		in.Config["sub"].(map[string]any)["k"] = "x"
		same(t, "after writing to the input", s.integration("i1"), fullIntegration())

		got := s.integration("i1")
		got.Sealed[0], got.Config["url"] = 9, "x"
		got.Config["sub"].(map[string]any)["k"] = "x"
		same(t, "after writing to a read", s.integration("i1"), fullIntegration())

		list, err := s.ListIntegrations(ctx, "")
		ok(t, err)
		list[0].Sealed[0], list[0].Config["url"] = 9, "x"
		same(t, "after writing to a list", s.integration("i1"), fullIntegration())
	})

	t.Run("project", func(t *testing.T) {
		s := store{open(t), t}
		ok(t, s.UpsertIntegration(ctx, repoAccount("i1", "u1", "one", 0)))
		ok(t, s.UpsertIntegration(ctx, repoAccount("i2", "u1", "two", 0)))
		spoil := func(p confstore.Project) {
			p.HarnessIDs[0], p.Commands["check"], p.Repos[0].Name = "x", "x", "x"
			p.Workspace.ExtraDomains[0], p.Workspace.Services[0].Ports[0].Label = "x", "x"
		}
		in := fullProject()
		ok(t, s.UpsertProject(ctx, in))
		spoil(in)
		same(t, "after writing to the input", s.project("p1"), fullProject())
		spoil(s.project("p1"))
		same(t, "after writing to a read", s.project("p1"), fullProject())
		byName, err := s.GetProjectByName(ctx, "u1", "web")
		ok(t, err)
		spoil(byName)
		same(t, "after writing to a read by name", s.project("p1"), fullProject())
		list, err := s.ListProjects(ctx, "")
		ok(t, err)
		spoil(list[0])
		same(t, "after writing to a list", s.project("p1"), fullProject())
	})

	t.Run("flow", func(t *testing.T) {
		s := seeded(t, open)
		r := flowRow("p1", "main", "h1", 0)
		r.Layout = json.RawMessage(canonicalLayout)
		ok(t, s.UpsertFlow(ctx, r))
		r.Layout[0] = 'x'
		same(t, "after writing to the input", string(s.flow("p1", "main").Layout), canonicalLayout)
		got := s.flow("p1", "main")
		got.Layout[0] = 'x'
		same(t, "after writing to a read", string(s.flow("p1", "main").Layout), canonicalLayout)
		list, err := s.ListFlows(ctx, "p1")
		ok(t, err)
		list[0].Layout[0] = 'x'
		same(t, "after writing to a list", string(s.flow("p1", "main").Layout), canonicalLayout)
	})

	t.Run("binding", func(t *testing.T) {
		s := seeded(t, open)
		want := map[string]string{"a": "b"}
		in := confstore.FlowBinding{ProjectID: "p1", FlowName: "main", Params: map[string]string{"a": "b"}, UpdatedAt: at(0)}
		ok(t, s.PutFlowBinding(ctx, in))
		in.Params["a"] = "x"
		got, err := s.GetFlowBinding(ctx, "p1", "main")
		ok(t, err)
		same(t, "after writing to the input", got.Params, want)
		got.Params["a"] = "x"
		got, err = s.GetFlowBinding(ctx, "p1", "main")
		ok(t, err)
		same(t, "after writing to a read", got.Params, want)
	})

	t.Run("secret", func(t *testing.T) {
		s := seeded(t, open)
		in := []byte{1}
		ok(t, s.PutProjectSecret(ctx, "p1", confstore.StoredProjectSecret{Name: "token", Sealed: in, UpdatedAt: at(0)}))
		in[0] = 9
		list, err := s.ListProjectSecrets(ctx, "p1")
		ok(t, err)
		same(t, "after writing to the input", list[0].Sealed, []byte{1})
		list[0].Sealed[0] = 9
		list, err = s.ListProjectSecrets(ctx, "p1")
		ok(t, err)
		same(t, "after writing to a list", list[0].Sealed, []byte{1})
	})

	t.Run("automation", func(t *testing.T) {
		s := seeded(t, open)
		spoil := func(a confstore.Automation) {
			a.Params["gate"] = "x"
			*a.LastFiredAt = at(99)
		}
		in := fullAutomation()
		ok(t, s.UpsertAutomation(ctx, in))
		spoil(in)
		same(t, "after writing to the input", s.automation("a1"), fullAutomation())
		spoil(s.automation("a1"))
		same(t, "after writing to a read", s.automation("a1"), fullAutomation())
		list, err := s.ListAutomations(ctx, "")
		ok(t, err)
		spoil(list[0])
		same(t, "after writing to a list", s.automation("a1"), fullAutomation())
	})

	t.Run("credential", func(t *testing.T) {
		s := store{open(t), t}
		in := fullCredential()
		ok(t, s.UpsertCredential(ctx, in))
		in.Sealed[0] = 'x'
		same(t, "after writing to the input", s.credential("c1"), fullCredential())
		got := s.credential("c1")
		got.Sealed[0] = 'x'
		same(t, "after writing to a read", s.credential("c1"), fullCredential())
		list, err := s.ListCredentials(ctx, "")
		ok(t, err)
		list[0].Sealed[0] = 'x'
		same(t, "after writing to a list", s.credential("c1"), fullCredential())
	})
}
