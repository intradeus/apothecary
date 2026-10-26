package confstore

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
)

// Mem is the in-memory DB used by unit tests and httpapi tests. Delete
// rules that depend on runs are injectable hooks (the SQL implementation
// queries the runs projection instead).
type Mem struct {
	mu           sync.Mutex
	integrations map[string]StoredIntegration
	projects     map[string]Project
	tickets      map[string]Ticket
	flows        map[string]FlowRow
	versions     map[string]FlowVersion
	bindings     map[string]FlowBinding
	automations  map[string]Automation
	secrets      map[string]StoredProjectSecret // flowKey(project, name)
	settings     map[string]string

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
		versions:     map[string]FlowVersion{},
		bindings:     map[string]FlowBinding{},
		automations:  map[string]Automation{},
		secrets:      map[string]StoredProjectSecret{},
		settings:     map[string]string{},
	}
}

func (m *Mem) UpsertIntegration(_ context.Context, r StoredIntegration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.integrations {
		if x.Port == r.Port && x.Name == r.Name && x.ID != r.ID {
			return fmt.Errorf("integration %s/%s: %w", r.Port, r.Name, ErrConflict)
		}
	}
	// Parity with the pg upsert: port and kind are immutable on update.
	if old, ok := m.integrations[r.ID]; ok {
		r.Port, r.Kind = old.Port, old.Kind
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
	return r, nil
}

func (m *Mem) ListIntegrations(_ context.Context) ([]StoredIntegration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]StoredIntegration, 0, len(m.integrations))
	for _, r := range m.integrations {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Mem) DeleteIntegration(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.integrations[id]; !ok {
		return notFound("integration", id)
	}
	var refs []string
	for _, p := range m.projects {
		inSet := false
		for _, h := range p.HarnessIDs {
			if h == id {
				inSet = true
				break
			}
		}
		if p.RepoAccountID == id || p.ProvisionerID == id || inSet {
			refs = append(refs, p.Name)
		}
	}
	if len(refs) > 0 {
		sort.Strings(refs)
		return fmt.Errorf("integration %s is used by project(s) %s: %w", id, strings.Join(refs, ", "), ErrReferenced)
	}
	delete(m.integrations, id)
	return nil
}

func (m *Mem) UpsertProject(_ context.Context, p Project) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.projects {
		if x.Name == p.Name && x.ID != p.ID {
			return fmt.Errorf("project %s: %w", p.Name, ErrConflict)
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
	return p, nil
}

func (m *Mem) GetProjectByName(_ context.Context, name string) (Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.projects {
		if p.Name == name {
			return p, nil
		}
	}
	return Project{}, notFound("project", name)
}

func (m *Mem) ListProjects(_ context.Context) ([]Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Project, 0, len(m.projects))
	for _, p := range m.projects {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Mem) DeleteProject(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[id]; !ok {
		return notFound("project", id)
	}
	if m.BlockingRuns != nil {
		if runs := m.BlockingRuns(id); len(runs) > 0 {
			return fmt.Errorf("project %s has non-archived run(s) %s: %w", id, strings.Join(runs, ", "), ErrReferenced)
		}
	}
	delete(m.projects, id)
	// Bindings and automations go with the project (pg: ON DELETE CASCADE).
	for key, b := range m.bindings {
		if b.ProjectID == id {
			delete(m.bindings, key)
		}
	}
	for key, a := range m.automations {
		if a.ProjectID == id {
			delete(m.automations, key)
		}
	}
	for key := range m.secrets {
		if strings.HasPrefix(key, id+"\x00") {
			delete(m.secrets, key)
		}
	}
	return nil
}

func (m *Mem) PutProjectSecret(_ context.Context, projectID string, sec StoredProjectSecret) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[projectID]; !ok {
		return fmt.Errorf("secret %s references a missing project: %w", sec.Name, ErrInvalid)
	}
	sec.Sealed = append([]byte(nil), sec.Sealed...)
	m.secrets[flowKey(projectID, sec.Name)] = sec
	return nil
}

func (m *Mem) ListProjectSecrets(_ context.Context, projectID string) ([]StoredProjectSecret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []StoredProjectSecret
	for key, sec := range m.secrets {
		if strings.HasPrefix(key, projectID+"\x00") {
			out = append(out, sec)
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[t.ProjectID]; !ok {
		return fmt.Errorf("ticket %s references a missing project: %w", t.ID, ErrInvalid)
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
	return t, nil
}

func (m *Mem) ListTickets(_ context.Context) ([]Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Ticket, 0, len(m.tickets))
	for _, t := range m.tickets {
		out = append(out, t)
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
	return nil
}

func (m *Mem) UpsertFlow(_ context.Context, row FlowRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	return r, nil
}

// ListFlows returns one project's flows, or every project's when projectID is
// empty — the cross-project overview the copy action starts from.
func (m *Mem) ListFlows(_ context.Context, projectID string) ([]FlowRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]FlowRow, 0, len(m.flows))
	for _, r := range m.flows {
		if projectID == "" || r.ProjectID == projectID {
			out = append(out, r)
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
	if _, ok := m.flows[flowKey(projectID, name)]; !ok {
		return notFound("flow", name)
	}
	// Only THIS project's references matter now: another project's default
	// flow of the same name is a different document.
	var refs []string
	for _, p := range m.projects {
		if p.ID == projectID && p.DefaultFlow == name {
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
	b.Params = maps.Clone(b.Params)
	m.bindings[flowKey(b.ProjectID, b.FlowName)] = b
	return nil
}

func (m *Mem) GetFlowBinding(_ context.Context, projectID, flowName string) (FlowBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bindings[flowKey(projectID, flowName)]
	if !ok {
		return FlowBinding{}, notFound("flow binding", flowName)
	}
	b.Params = maps.Clone(b.Params)
	return b, nil
}

func (m *Mem) ListFlowBindings(_ context.Context, projectID string) ([]FlowBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []FlowBinding
	for _, b := range m.bindings {
		if b.ProjectID == projectID {
			b.Params = maps.Clone(b.Params)
			out = append(out, b)
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[a.ProjectID]; !ok {
		return fmt.Errorf("automation %s references a missing project: %w", a.ID, ErrInvalid)
	}
	a.Params = maps.Clone(a.Params)
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
	a.Params = maps.Clone(a.Params)
	return a, nil
}

func (m *Mem) ListAutomations(_ context.Context, projectID string) ([]Automation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Automation
	for _, a := range m.automations {
		if projectID == "" || a.ProjectID == projectID {
			a.Params = maps.Clone(a.Params)
			out = append(out, a)
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

func (m *Mem) GetSettings(_ context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	maps.Copy(out, m.settings)
	return out, nil
}

func (m *Mem) PutSetting(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings[key] = value
	return nil
}

var _ DB = (*Mem)(nil)
