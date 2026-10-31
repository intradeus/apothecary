package confstore

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"apothecary/internal/platform/secrets"
)

// Project secrets are the values a flow reads where it says
// ${secrets.<name>} — an MCP server's token, an API key in a header — so none
// is ever written in the flow (FLOWLANG §1 mcp_servers). They belong to the
// project, are sealed at rest like an integration's, and are never read back
// through the API: a name and when it was last set is all anyone sees. The
// engine opens them when a run is created (every name the flow reads must
// exist) and again when an agent stage starts, so a rotated value reaches the
// next stage without a new run.

// ProjectSecret is one secret as the API shows it: never its value.
type ProjectSecret struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

// StoredProjectSecret is one sealed secret as the DB holds it.
type StoredProjectSecret struct {
	Name      string
	Sealed    []byte
	UpdatedAt time.Time
}

// secretNameRe is the shape of a secret's name — the flow's slug, since
// ${secrets.<name>} is checked against the same rule.
var secretNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// ListProjectSecrets lists a project's secrets by name.
func (s *Service) ListProjectSecrets(ctx context.Context, projectID string) ([]ProjectSecret, error) {
	if _, err := s.db.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.ListProjectSecrets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectSecret, 0, len(rows))
	for _, r := range rows {
		out = append(out, ProjectSecret{Name: r.Name, UpdatedAt: r.UpdatedAt})
	}
	return out, nil
}

// SetProjectSecret creates or replaces one secret.
func (s *Service) SetProjectSecret(ctx context.Context, projectID, name, value string) (ProjectSecret, error) {
	if _, err := s.db.GetProject(ctx, projectID); err != nil {
		return ProjectSecret{}, err
	}
	if !secretNameRe.MatchString(name) {
		return ProjectSecret{}, invalidf("secret %q: a name is a lowercase slug (letters, digits, - and _), as ${secrets.<name>} spells it", name)
	}
	if value == "" {
		return ProjectSecret{}, invalidf("secret %q: a value is required (delete the secret to remove it)", name)
	}
	if strings.HasPrefix(value, maskPrefix) {
		return ProjectSecret{}, invalidf("secret %q: the value is a masked placeholder — send the real value", name)
	}
	sealed, err := secrets.Seal(s.key, map[string]string{name: value})
	if err != nil {
		return ProjectSecret{}, err
	}
	at := s.now().UTC()
	if err := s.db.PutProjectSecret(ctx, projectID, StoredProjectSecret{Name: name, Sealed: sealed, UpdatedAt: at}); err != nil {
		return ProjectSecret{}, err
	}
	return ProjectSecret{Name: name, UpdatedAt: at}, nil
}

// DeleteProjectSecret removes one secret. A flow that reads it is refused at
// the next run's creation.
func (s *Service) DeleteProjectSecret(ctx context.Context, projectID, name string) error {
	return s.db.DeleteProjectSecret(ctx, projectID, name)
}

// ProjectSecrets opens every secret of a project: what the resolver hands a
// run, and what the engine re-reads when an agent stage starts.
func (s *Service) ProjectSecrets(ctx context.Context, projectID string) (map[string]string, error) {
	rows, err := s.db.ListProjectSecrets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		m, err := secrets.Open(s.key, r.Sealed)
		if err != nil {
			return nil, fmt.Errorf("project %s: secret %s: %w", projectID, r.Name, err)
		}
		out[r.Name] = m[r.Name]
	}
	return out, nil
}
