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
	flows        map[string]FlowRow
	versions     map[string]FlowVersion
	agents       map[string]Agent // key: projectID+"\x00"+name
	agentVs      map[string]AgentVersion
	settings     map[string]string

	// BlockingRuns lists non-archived runs of a project (blocks project
	// deletion); nil = none.
	BlockingRuns func(projectID string) []string
	// FlowPinnedBy lists non-terminal runs pinning a flow name; nil = none.
	FlowPinnedBy func(flowName string) []string
}

func NewMem() *Mem {
	return &Mem{
		integrations: map[string]StoredIntegration{},
		projects:     map[string]Project{},
		flows:        map[string]FlowRow{},
		versions:     map[string]FlowVersion{},
		agents:       map[string]Agent{},
		agentVs:      map[string]AgentVersion{},
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
		if p.ForgeID == id || p.ProvisionerID == id || inSet {
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
	return nil
}

func (m *Mem) UpsertFlow(_ context.Context, row FlowRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// nil layout keeps the stored one (DESIGN3 §2.2; parity with pg's
	// COALESCE) — a YAML-only save must not wipe node positions.
	if row.Layout == nil {
		if old, ok := m.flows[row.Name]; ok {
			row.Layout = old.Layout
		}
	}
	m.flows[row.Name] = row
	if _, ok := m.versions[row.Hash]; !ok {
		m.versions[row.Hash] = FlowVersion{Hash: row.Hash, Name: row.Name, YAML: row.YAML, CreatedAt: row.UpdatedAt}
	}
	return nil
}

func (m *Mem) GetFlow(_ context.Context, name string) (FlowRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.flows[name]
	if !ok {
		return FlowRow{}, notFound("flow", name)
	}
	return r, nil
}

func (m *Mem) ListFlows(_ context.Context) ([]FlowRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]FlowRow, 0, len(m.flows))
	for _, r := range m.flows {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Mem) DeleteFlow(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.flows[name]; !ok {
		return notFound("flow", name)
	}
	var refs []string
	for _, p := range m.projects {
		if p.DefaultFlow == name {
			refs = append(refs, "project "+p.Name)
		}
	}
	if m.FlowPinnedBy != nil {
		for _, r := range m.FlowPinnedBy(name) {
			refs = append(refs, "run "+r)
		}
	}
	if len(refs) > 0 {
		sort.Strings(refs)
		return fmt.Errorf("flow %s is referenced by %s: %w", name, strings.Join(refs, ", "), ErrReferenced)
	}
	delete(m.flows, name) // versions stay: immutable forever (§7)
	return nil
}

func (m *Mem) GetFlowVersion(_ context.Context, hash string) (FlowVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.versions[hash]
	if !ok {
		return FlowVersion{}, notFound("flow version", hash)
	}
	return v, nil
}

func (m *Mem) ListFlowVersions(_ context.Context, name string) ([]FlowVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []FlowVersion
	for _, v := range m.versions {
		if v.Name == name {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func agentKey(projectID, name string) string { return projectID + "\x00" + name }

func (m *Mem) UpsertAgent(_ context.Context, a Agent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents[agentKey(a.ProjectID, a.Name)] = a
	if _, ok := m.agentVs[a.Hash]; !ok {
		m.agentVs[a.Hash] = AgentVersionRow(a, a.UpdatedAt)
	}
	return nil
}

func (m *Mem) GetAgent(_ context.Context, projectID, name string) (Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[agentKey(projectID, name)]
	if !ok {
		return Agent{}, notFound("agent", name)
	}
	return a, nil
}

func (m *Mem) ListAgents(_ context.Context, projectID string) ([]Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Agent
	for _, a := range m.agents {
		if a.ProjectID == projectID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Mem) DeleteAgent(_ context.Context, projectID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.agents[agentKey(projectID, name)]; !ok {
		return notFound("agent", name)
	}
	delete(m.agents, agentKey(projectID, name)) // versions stay: immutable
	return nil
}

func (m *Mem) GetAgentVersion(_ context.Context, hash string) (AgentVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.agentVs[hash]
	if !ok {
		return AgentVersion{}, notFound("agent version", hash)
	}
	return v, nil
}

func (m *Mem) ListAgentVersions(_ context.Context, name string) ([]AgentVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []AgentVersion
	for _, v := range m.agentVs {
		if v.Name == name {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

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
