package confstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"apothecary/internal/agentspec"
	"apothecary/internal/flow"
)

// Agent is one per-project agent definition (DESIGN3 §1): the DB-backed
// successor of the target repo's .apothecary/agents/<name>.yaml.
type Agent struct {
	ProjectID   string    `json:"project_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	ReadOnly    bool      `json:"read_only"`
	AllowBash   bool      `json:"allow_bash"`
	Prompt      string    `json:"prompt"`
	Hash        string    `json:"hash"` // current content version
	UpdatedAt   time.Time `json:"updated_at"`
}

// AgentVersion is one immutable content-addressed version. project_id is
// deliberately absent from the content (and the hash): identical prompts
// share a version across projects, which is what copy-tracking analytics
// want.
type AgentVersion struct {
	Hash      string          `json:"hash"`
	Name      string          `json:"name"`
	Spec      json.RawMessage `json:"spec"`
	CreatedAt time.Time       `json:"created_at"`
}

// Spec converts to the harness-agnostic currency the adapters consume.
func (a Agent) Spec() *agentspec.Spec {
	return &agentspec.Spec{
		Name: a.Name, Description: a.Description,
		ReadOnly: a.ReadOnly, AllowBash: a.AllowBash, Prompt: a.Prompt,
	}
}

// agentContent is the canonical hashed/stored shape (stable field order via
// struct tags — json.Marshal emits fields in declaration order).
type agentContent struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only"`
	AllowBash   bool   `json:"allow_bash"`
	Prompt      string `json:"prompt"`
}

// AgentHash is the content address: sha256[:16] of the canonical JSON.
func AgentHash(a Agent) string {
	data, _ := json.Marshal(agentContent{
		Name: a.Name, Description: a.Description,
		ReadOnly: a.ReadOnly, AllowBash: a.AllowBash, Prompt: a.Prompt,
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

// SaveAgent validates and upserts, writing the immutable version row.
func (s *Service) SaveAgent(ctx context.Context, a Agent) (Agent, error) {
	if !agentspec.ValidName(a.Name) {
		return Agent{}, invalidf("agent: name must match [a-z0-9][a-z0-9._-]* (lowercase slug), got %q", a.Name)
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return Agent{}, invalidf("agent %s: prompt is required", a.Name)
	}
	if _, err := s.db.GetProject(ctx, a.ProjectID); err != nil {
		return Agent{}, fmt.Errorf("agent %s: project %s: %w", a.Name, a.ProjectID, err)
	}
	a.Hash = AgentHash(a)
	a.UpdatedAt = s.now().UTC()
	if err := s.db.UpsertAgent(ctx, a); err != nil {
		return Agent{}, err
	}
	return a, nil
}

func (s *Service) GetAgent(ctx context.Context, projectID, name string) (Agent, error) {
	return s.db.GetAgent(ctx, projectID, name)
}

func (s *Service) ListAgents(ctx context.Context, projectID string) ([]Agent, error) {
	return s.db.ListAgents(ctx, projectID)
}

// DeleteAgent refuses when the project's default flow references the agent
// (ErrReferenced); other flows are preflight's concern — flows are global,
// projects are not.
func (s *Service) DeleteAgent(ctx context.Context, projectID, name string) error {
	p, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	if row, err := s.db.GetFlow(ctx, p.DefaultFlow); err == nil {
		if f, err := flow.Parse(row.Name, []byte(row.YAML)); err == nil {
			for i := range f.Stages {
				if f.Stages[i].Type == flow.StageAgent && f.Stages[i].Agent == name {
					return fmt.Errorf("agent %s is used by stage %q of default flow %s: %w",
						name, f.Stages[i].ID, p.DefaultFlow, ErrReferenced)
				}
			}
		}
	}
	return s.db.DeleteAgent(ctx, projectID, name)
}

// CopyAgents copies agents from another project. Empty names = all of the
// source's agents. Any name collision in the destination refuses the whole
// copy (ErrConflict) — no silent overwrite.
func (s *Service) CopyAgents(ctx context.Context, dstProjectID, srcProjectID string, names []string) ([]Agent, error) {
	if dstProjectID == srcProjectID {
		return nil, invalidf("agent copy: source and destination are the same project")
	}
	if _, err := s.db.GetProject(ctx, dstProjectID); err != nil {
		return nil, err
	}
	src, err := s.db.ListAgents(ctx, srcProjectID)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.GetProject(ctx, srcProjectID); err != nil {
		return nil, err
	}
	byName := map[string]Agent{}
	for _, a := range src {
		byName[a.Name] = a
	}
	if len(names) == 0 {
		for n := range byName {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil, invalidf("agent copy: project %s has no agents", srcProjectID)
	}
	existing, err := s.db.ListAgents(ctx, dstProjectID)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, a := range existing {
		have[a.Name] = true
	}
	var toCopy []Agent
	for _, n := range names {
		a, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("agent %s in project %s: %w", n, srcProjectID, ErrNotFound)
		}
		if have[n] {
			return nil, fmt.Errorf("agent %s already exists in the destination (delete it first to replace): %w", n, ErrConflict)
		}
		toCopy = append(toCopy, a)
	}
	out := make([]Agent, 0, len(toCopy))
	for _, a := range toCopy {
		a.ProjectID = dstProjectID
		saved, err := s.SaveAgent(ctx, a)
		if err != nil {
			return nil, err
		}
		out = append(out, saved)
	}
	return out, nil
}

func (s *Service) AgentVersions(ctx context.Context, projectID, name string) ([]AgentVersion, error) {
	if _, err := s.db.GetAgent(ctx, projectID, name); err != nil {
		return nil, err
	}
	return s.db.ListAgentVersions(ctx, name)
}

func (s *Service) AgentVersion(ctx context.Context, hash string) (AgentVersion, error) {
	return s.db.GetAgentVersion(ctx, hash)
}

// AgentVersionRow builds the immutable row stores insert alongside an
// upsert (shared by pg + mem so the spec JSON is identical).
func AgentVersionRow(a Agent, at time.Time) AgentVersion {
	data, _ := json.Marshal(agentContent{
		Name: a.Name, Description: a.Description,
		ReadOnly: a.ReadOnly, AllowBash: a.AllowBash, Prompt: a.Prompt,
	})
	return AgentVersion{Hash: a.Hash, Name: a.Name, Spec: data, CreatedAt: at}
}
