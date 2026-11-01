package confstore

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"apothecary/internal/flow/builtin"
)

// Mem is the in-memory DB used by unit tests and httpapi tests, held to
// pgstore's behaviour by the conftest contract. Delete rules that depend on
// runs are injectable hooks (the SQL implementation queries the runs
// projection instead).
type Mem struct {
	mu           sync.Mutex
	integrations map[string]StoredIntegration
	projects     map[string]Project
	tickets      map[string]Ticket
	flows        map[string]FlowRow
	statuses     map[string]TrackerStatus
	types        map[string]TrackerType
	comments     map[string]TicketComment
	links        map[string]TicketLink // ticketID \x00 url
	versions     map[string]FlowVersion
	bindings     map[string]FlowBinding
	automations  map[string]Automation
	secrets      map[string]StoredProjectSecret // flowKey(project, name)
	settings     map[string]string
	credentials  map[string]ProviderCredential

	// BlockingRuns lists non-archived runs of a project (blocks project
	// deletion); nil = none.
	BlockingRuns func(projectID string) []string
	// FlowPinnedBy lists non-terminal runs of a project pinning a flow name;
	// nil = none.
	FlowPinnedBy func(projectID, flowName string) []string
}

func NewMem() *Mem {
	return &Mem{
		integrations: map[string]StoredIntegration{},
		projects:     map[string]Project{},
		tickets:      map[string]Ticket{},
		flows:        map[string]FlowRow{},
		statuses:     map[string]TrackerStatus{},
		types:        map[string]TrackerType{},
		comments:     map[string]TicketComment{},
		links:        map[string]TicketLink{},
		versions:     map[string]FlowVersion{},
		bindings:     map[string]FlowBinding{},
		automations:  map[string]Automation{},
		secrets:      map[string]StoredProjectSecret{},
		settings:     map[string]string{},
		credentials:  map[string]ProviderCredential{},
	}
}

// A row as its table holds it and hands it back. stored* runs on the way in
// and on the way out, so a caller sees what Postgres would show it and shares
// no memory with the store.

// viaJSON is v as a jsonb column reads back: numbers are float64, a []string
// is a []any, an empty omitempty slice is gone.
func viaJSON[T any](v T) (T, error) {
	var out T
	raw, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(raw, &out)
}

// pgTime is t as a timestamptz keeps it: pgx truncates to the microsecond.
func pgTime(t time.Time) time.Time { return t.Truncate(time.Microsecond) }

func pgTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := pgTime(*t)
	return &v
}

func storedIntegration(r StoredIntegration) (StoredIntegration, error) {
	cfg, err := viaJSON(r.Config)
	if err != nil {
		return r, err
	}
	r.Config = cfg
	r.Sealed = append([]byte{}, r.Sealed...)
	r.CreatedAt, r.UpdatedAt = pgTime(r.CreatedAt), pgTime(r.UpdatedAt)
	return r, nil
}

// readIntegration can't fail: the row went through storedIntegration to get in.
func readIntegration(r StoredIntegration) StoredIntegration {
	r, _ = storedIntegration(r)
	return r
}

func storedProject(p Project) Project {
	p.HarnessIDs = append([]string{}, p.HarnessIDs...)
	p.Repos = append([]ExtraRepo{}, p.Repos...)
	p.Commands = maps.Clone(p.Commands)
	p.Workspace, _ = viaJSON(p.Workspace) // plain data: can't fail
	p.CreatedAt, p.UpdatedAt = pgTime(p.CreatedAt), pgTime(p.UpdatedAt)
	return p
}

func storedTicket(t Ticket) Ticket {
	// Parity with the labels text[] column: empty reads back as none.
	if len(t.Labels) == 0 {
		t.Labels = nil
	}
	t.Labels = slices.Clone(t.Labels)
	t.RemoteUpdatedAt, t.SyncedAt = pgTimePtr(t.RemoteUpdatedAt), pgTimePtr(t.SyncedAt)
	t.StatusChangedAt = pgTimePtr(t.StatusChangedAt)
	t.CreatedAt, t.UpdatedAt = pgTime(t.CreatedAt), pgTime(t.UpdatedAt)
	return t
}

func storedFlow(r FlowRow) FlowRow {
	if len(r.Layout) == 0 {
		r.Layout = nil
	}
	r.Layout = slices.Clone(r.Layout)
	r.UpdatedAt = pgTime(r.UpdatedAt)
	return r
}

func storedBinding(b FlowBinding) FlowBinding {
	b.Params = maps.Clone(b.Params)
	b.UpdatedAt = pgTime(b.UpdatedAt)
	return b
}

func storedSecret(sec StoredProjectSecret) StoredProjectSecret {
	sec.Sealed = slices.Clone(sec.Sealed)
	sec.UpdatedAt = pgTime(sec.UpdatedAt)
	return sec
}

func storedAutomation(a Automation) Automation {
	if len(a.Params) == 0 {
		a.Params = nil
	}
	a.Params = maps.Clone(a.Params)
	a.LastPolledAt, a.LastFiredAt = pgTimePtr(a.LastPolledAt), pgTimePtr(a.LastFiredAt)
	a.CreatedAt, a.UpdatedAt = pgTime(a.CreatedAt), pgTime(a.UpdatedAt)
	return a
}

func storedCredential(c ProviderCredential) ProviderCredential {
	c.Sealed = append([]byte{}, c.Sealed...)
	c.CreatedAt, c.UpdatedAt = pgTime(c.CreatedAt), pgTime(c.UpdatedAt)
	return c
}

func (m *Mem) UpsertIntegration(_ context.Context, r StoredIntegration) error {
	r, err := storedIntegration(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Parity with the pg upsert: owner, port, kind and created_at are set at
	// insert, so the stored ones are what a rename conflicts on.
	if old, ok := m.integrations[r.ID]; ok {
		r.UserID, r.Port, r.Kind, r.CreatedAt = old.UserID, old.Port, old.Kind, old.CreatedAt
	}
	for _, x := range m.integrations {
		if x.UserID == r.UserID && x.Port == r.Port && x.Name == r.Name && x.ID != r.ID {
			return fmt.Errorf("integration %s/%s: %w", r.Port, r.Name, ErrConflict)
		}
	}
	m.integrations[r.ID] = r
	return nil
}

func (m *Mem) GetIntegration(_ context.Context, id string) (StoredIntegration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.integrations[id]
	if !ok {
		return StoredIntegration{}, notFound("integration", id)
	}
	return readIntegration(r), nil
}

func (m *Mem) ListIntegrations(_ context.Context, userID string) ([]StoredIntegration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []StoredIntegration
	for _, r := range m.integrations {
		if userID == "" || r.UserID == userID {
			out = append(out, readIntegration(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Mem) DeleteIntegration(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deleteIntegration(id)
}

// deleteIntegration is DeleteIntegration under m.mu.
func (m *Mem) deleteIntegration(id string) error {
	// Parity with pg: references first, so a harness id nothing stands behind
	// still reads as referenced.
	var refs []string
	for _, p := range m.projects {
		if p.RepoAccountID == id || p.ProvisionerID == id || slices.Contains(p.HarnessIDs, id) {
			refs = append(refs, p.Name)
		}
	}
	if len(refs) > 0 {
		sort.Strings(refs)
		return fmt.Errorf("integration %s is used by project(s) %s: %w", id, strings.Join(refs, ", "), ErrReferenced)
	}
	if _, ok := m.integrations[id]; !ok {
		return notFound("integration", id)
	}
	// Parity with the lists' cascade meeting the tickets' FKs.
	for _, t := range m.tickets {
		if m.statuses[t.StatusID].IntegrationID == id || m.types[t.TypeID].IntegrationID == id {
			return fmt.Errorf("integration %s: %w", id, ErrReferenced)
		}
	}
	delete(m.integrations, id)
	maps.DeleteFunc(m.statuses, func(_ string, s TrackerStatus) bool { return s.IntegrationID == id })
	maps.DeleteFunc(m.types, func(_ string, t TrackerType) bool { return t.IntegrationID == id })
	return nil
}

func (m *Mem) UpsertProject(_ context.Context, p Project) error {
	p = storedProject(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	// Parity with pg, in its order: the flow gate says whether a row is written
	// at all, the unique index fires on the write, the FKs after it.
	if _, own := m.flows[flowKey(p.ID, p.DefaultFlow)]; !own && !slices.Contains(builtin.FlowNames(), p.DefaultFlow) {
		return fmt.Errorf("project %s references a missing flow or integration: %w", p.Name, ErrInvalid)
	}
	// The owner and created_at are set at insert, so the stored owner is what
	// a rename conflicts on.
	if old, ok := m.projects[p.ID]; ok {
		p.UserID, p.CreatedAt = old.UserID, old.CreatedAt
	}
	for _, x := range m.projects {
		if x.UserID == p.UserID && x.Name == p.Name && x.ID != p.ID {
			return fmt.Errorf("project %s: %w", p.Name, ErrConflict)
		}
	}
	for _, id := range []string{p.RepoAccountID, p.ProvisionerID} {
		if _, ok := m.integrations[id]; !ok {
			return fmt.Errorf("project %s references a missing flow or integration: %w", p.Name, ErrInvalid)
		}
	}
	m.projects[p.ID] = p
	return nil
}

func (m *Mem) GetProject(_ context.Context, id string) (Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok {
		return Project{}, notFound("project", id)
	}
	return storedProject(p), nil
}

func (m *Mem) GetProjectByName(_ context.Context, userID, name string) (Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Parity with pg's ORDER BY created_at LIMIT 1: "" can match several owners.
	var found *Project
	for _, p := range m.projects {
		if p.Name != name || (userID != "" && p.UserID != userID) {
			continue
		}
		if found == nil || p.CreatedAt.Before(found.CreatedAt) ||
			(p.CreatedAt.Equal(found.CreatedAt) && p.ID < found.ID) {
			found = &p
		}
	}
	if found == nil {
		return Project{}, notFound("project", name)
	}
	return storedProject(*found), nil
}

func (m *Mem) ListProjects(_ context.Context, userID string) ([]Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Project
	for _, p := range m.projects {
		if userID == "" || p.UserID == userID {
			out = append(out, storedProject(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Mem) DeleteProject(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.BlockingRuns != nil {
		if runs := m.BlockingRuns(id); len(runs) > 0 {
			return fmt.Errorf("project %s has non-archived run(s) %s: %w", id, strings.Join(runs, ", "), ErrReferenced)
		}
	}
	if _, ok := m.projects[id]; !ok {
		return notFound("project", id)
	}
	delete(m.projects, id)
	// Everything under the project goes with it (pg: ON DELETE CASCADE).
	maps.DeleteFunc(m.tickets, func(_ string, t Ticket) bool { return t.ProjectID == id })
	m.dropOrphans()
	maps.DeleteFunc(m.flows, func(_ string, r FlowRow) bool { return r.ProjectID == id })
	maps.DeleteFunc(m.versions, func(_ string, v FlowVersion) bool { return v.ProjectID == id })
	maps.DeleteFunc(m.bindings, func(_ string, b FlowBinding) bool { return b.ProjectID == id })
	maps.DeleteFunc(m.automations, func(_ string, a Automation) bool { return a.ProjectID == id })
	maps.DeleteFunc(m.secrets, func(key string, _ StoredProjectSecret) bool { return strings.HasPrefix(key, id+"\x00") })
	return nil
}

func (m *Mem) PutProjectSecret(_ context.Context, projectID string, sec StoredProjectSecret) error {
	// Parity with the value column's NOT NULL: nil is no value, empty is one.
	if sec.Sealed == nil {
		return fmt.Errorf("secret %s: value is required", sec.Name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[projectID]; !ok {
		return fmt.Errorf("secret %s references a missing project: %w", sec.Name, ErrInvalid)
	}
	m.secrets[flowKey(projectID, sec.Name)] = storedSecret(sec)
	return nil
}

func (m *Mem) ListProjectSecrets(_ context.Context, projectID string) ([]StoredProjectSecret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []StoredProjectSecret
	for key, sec := range m.secrets {
		if strings.HasPrefix(key, projectID+"\x00") {
			out = append(out, storedSecret(sec))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Mem) DeleteProjectSecret(_ context.Context, projectID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.secrets[flowKey(projectID, name)]; !ok {
		return notFound("secret", name)
	}
	delete(m.secrets, flowKey(projectID, name))
	return nil
}

func (m *Mem) UpsertTicket(_ context.Context, t Ticket) error {
	t = storedTicket(t)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[t.ProjectID]; !ok {
		return fmt.Errorf("ticket %s references a missing project, status or type: %w", t.ID, ErrInvalid)
	}
	if _, ok := m.statuses[t.StatusID]; t.StatusID != "" && !ok {
		return fmt.Errorf("ticket %s references a missing project, status or type: %w", t.ID, ErrInvalid)
	}
	if _, ok := m.types[t.TypeID]; t.TypeID != "" && !ok {
		return fmt.Errorf("ticket %s references a missing project, status or type: %w", t.ID, ErrInvalid)
	}
	// Parity with tickets_source_key_idx.
	if t.SourceID != "" {
		for _, x := range m.tickets {
			if x.ID != t.ID && x.UserID == t.UserID && x.SourceID == t.SourceID && strings.EqualFold(x.SourceKey, t.SourceKey) {
				return fmt.Errorf("ticket %s: %s is already ticket %s: %w", t.ID, t.SourceKey, x.ID, ErrConflict)
			}
		}
	}
	// Parity with the pg upsert: only created_at survives an update. The owner
	// is written as given; copying the project's is the Service's job.
	if old, ok := m.tickets[t.ID]; ok {
		t.CreatedAt = old.CreatedAt
	}
	m.tickets[t.ID] = t
	return nil
}

func (m *Mem) GetTicket(_ context.Context, id string) (Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[id]
	if !ok {
		return Ticket{}, notFound("ticket", id)
	}
	return storedTicket(t), nil
}

func (m *Mem) ListTickets(_ context.Context, userID string) ([]Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Ticket
	for _, t := range m.tickets {
		if userID == "" || t.UserID == userID {
			out = append(out, storedTicket(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *Mem) DeleteTicket(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tickets[id]; !ok {
		return notFound("ticket", id)
	}
	delete(m.tickets, id)
	m.dropOrphans()
	return nil
}

func (m *Mem) UpsertFlow(_ context.Context, row FlowRow) error {
	row = storedFlow(row)
	// Parity with the layout column's jsonb.
	if row.Layout != nil && !json.Valid(row.Layout) {
		return fmt.Errorf("flow %s: layout is not JSON", row.Name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[row.ProjectID]; !ok {
		return fmt.Errorf("flow %s references a missing project: %w", row.Name, ErrInvalid)
	}
	key := flowKey(row.ProjectID, row.Name)
	// nil layout keeps the stored one (DESIGN3 §2.2; parity with pg's
	// COALESCE) — a YAML-only save must not wipe node positions.
	if row.Layout == nil {
		if old, ok := m.flows[key]; ok {
			row.Layout = old.Layout
		}
	}
	m.flows[key] = row
	vkey := flowKey(row.ProjectID, row.Hash)
	if _, ok := m.versions[vkey]; !ok {
		m.versions[vkey] = FlowVersion{ProjectID: row.ProjectID, Hash: row.Hash,
			Name: row.Name, YAML: row.YAML, CreatedAt: row.UpdatedAt}
	}
	return nil
}

func (m *Mem) GetFlow(_ context.Context, projectID, name string) (FlowRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.flows[flowKey(projectID, name)]
	if !ok {
		return FlowRow{}, notFound("flow", name)
	}
	return storedFlow(r), nil
}

// ListFlows returns one project's flows, or every project's when projectID is
// empty — the cross-project overview the copy action starts from.
func (m *Mem) ListFlows(_ context.Context, projectID string) ([]FlowRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []FlowRow
	for _, r := range m.flows {
		if projectID == "" || r.ProjectID == projectID {
			out = append(out, storedFlow(r))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectID != out[j].ProjectID {
			return out[i].ProjectID < out[j].ProjectID
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (m *Mem) DeleteFlow(_ context.Context, projectID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Only THIS project's references matter now: another project's default
	// flow of the same name is a different document. They come first (parity
	// with pg): a project on a template holds a name with no row to find.
	var refs []string
	for _, p := range m.projects {
		if p.ID == projectID && p.DefaultFlow == name && !slices.Contains(builtin.FlowNames(), name) {
			refs = append(refs, "project "+p.Name)
		}
	}
	if m.FlowPinnedBy != nil {
		for _, r := range m.FlowPinnedBy(projectID, name) {
			refs = append(refs, "run "+r)
		}
	}
	if len(refs) > 0 {
		sort.Strings(refs)
		return fmt.Errorf("flow %s is referenced by %s: %w", name, strings.Join(refs, ", "), ErrReferenced)
	}
	if _, ok := m.flows[flowKey(projectID, name)]; !ok {
		return notFound("flow", name)
	}
	delete(m.flows, flowKey(projectID, name)) // versions stay: immutable forever (§7)
	return nil
}

func (m *Mem) GetFlowVersion(_ context.Context, projectID, hash string) (FlowVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.versions[flowKey(projectID, hash)]
	if !ok {
		return FlowVersion{}, notFound("flow version", hash)
	}
	return v, nil
}

func (m *Mem) ListFlowVersions(_ context.Context, projectID, name string) ([]FlowVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []FlowVersion
	for _, v := range m.versions {
		if v.ProjectID == projectID && v.Name == name {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *Mem) PutFlowBinding(_ context.Context, b FlowBinding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[b.ProjectID]; !ok {
		return fmt.Errorf("binding of flow %s references a missing project: %w", b.FlowName, ErrInvalid)
	}
	m.bindings[flowKey(b.ProjectID, b.FlowName)] = storedBinding(b)
	return nil
}

func (m *Mem) GetFlowBinding(_ context.Context, projectID, flowName string) (FlowBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bindings[flowKey(projectID, flowName)]
	if !ok {
		return FlowBinding{}, notFound("flow binding", flowName)
	}
	return storedBinding(b), nil
}

func (m *Mem) ListFlowBindings(_ context.Context, projectID string) ([]FlowBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []FlowBinding
	for _, b := range m.bindings {
		if b.ProjectID == projectID {
			out = append(out, storedBinding(b))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FlowName < out[j].FlowName })
	return out, nil
}

func (m *Mem) DeleteFlowBinding(_ context.Context, projectID, flowName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.bindings[flowKey(projectID, flowName)]; !ok {
		return notFound("flow binding", flowName)
	}
	delete(m.bindings, flowKey(projectID, flowName))
	return nil
}

func (m *Mem) UpsertAutomation(_ context.Context, a Automation) error {
	// Parity with automations_kind_check.
	if !slices.Contains(AutomationKinds, a.Kind) {
		return fmt.Errorf("automation %s: kind %q is not one the table knows", a.ID, a.Kind)
	}
	a = storedAutomation(a)
	m.mu.Lock()
	defer m.mu.Unlock()
	// Parity with the pg upsert: the project and created_at are set at insert,
	// so only an insert needs the project to exist.
	if old, ok := m.automations[a.ID]; ok {
		a.ProjectID, a.CreatedAt = old.ProjectID, old.CreatedAt
	} else if _, ok := m.projects[a.ProjectID]; !ok {
		return fmt.Errorf("automation %s references a missing project: %w", a.ID, ErrInvalid)
	}
	m.automations[a.ID] = a
	return nil
}

func (m *Mem) GetAutomation(_ context.Context, id string) (Automation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.automations[id]
	if !ok {
		return Automation{}, notFound("automation", id)
	}
	return storedAutomation(a), nil
}

func (m *Mem) ListAutomations(_ context.Context, projectID string) ([]Automation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Automation
	for _, a := range m.automations {
		if projectID == "" || a.ProjectID == projectID {
			out = append(out, storedAutomation(a))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *Mem) DeleteAutomation(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.automations[id]; !ok {
		return notFound("automation", id)
	}
	delete(m.automations, id)
	return nil
}

// flowKey scopes a flow (and a version) to its project.
func flowKey(projectID, name string) string { return projectID + "\x00" + name }

func agentKey(projectID, name string) string { return projectID + "\x00" + name }

func (m *Mem) GetSettings(_ context.Context, userID string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for k, v := range m.settings {
		if strings.HasPrefix(k, userID+"\x00") {
			out[strings.TrimPrefix(k, userID+"\x00")] = v
		}
	}
	return out, nil
}

func (m *Mem) PutSetting(_ context.Context, userID, key, value string) error {
	// Parity with the settings table's jsonb column.
	if !json.Valid([]byte(value)) {
		return fmt.Errorf("setting %s: value is not JSON", key)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings[userID+"\x00"+key] = value
	return nil
}

func (m *Mem) UpsertCredential(_ context.Context, c ProviderCredential) error {
	c = storedCredential(c)
	m.mu.Lock()
	defer m.mu.Unlock()
	// Parity with the pg upsert: owner, provider and kind never change.
	if old, ok := m.credentials[c.ID]; ok {
		c.UserID, c.Provider, c.Kind, c.CreatedAt = old.UserID, old.Provider, old.Kind, old.CreatedAt
	}
	m.credentials[c.ID] = c
	return nil
}

func (m *Mem) GetCredential(_ context.Context, id string) (ProviderCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.credentials[id]
	if !ok {
		return ProviderCredential{}, notFound("credential", id)
	}
	return storedCredential(c), nil
}

func (m *Mem) ListCredentials(_ context.Context, userID string) ([]ProviderCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ProviderCredential
	for _, c := range m.credentials {
		if userID == "" || c.UserID == userID {
			out = append(out, storedCredential(c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Mem) DeleteCredential(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.credentials[id]; !ok {
		return notFound("credential", id)
	}
	delete(m.credentials, id)
	return nil
}

var _ DB = (*Mem)(nil)
