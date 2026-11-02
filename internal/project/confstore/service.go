package confstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"apothecary/internal/flow"
	"apothecary/internal/flow/builtin"
	"apothecary/internal/platform/secrets"
)

// Service seals/opens secrets, stamps ids/timestamps and validates writes.
// It is the only path to the DB for the API, the resolver, import-config
// and dev seeding.
type Service struct {
	db  DB
	key secrets.Key

	// KindValid reports whether (port, kind) is registered; nil accepts any
	// (unit tests). Wired to the kinds registry at serve time.
	KindValid func(port, kind string) bool
	// Notices hears statuses and types a tracker added; nil = silent.
	Notices TrackerListNotices

	now   func() time.Time
	newID func() string
}

func New(db DB, key secrets.Key) *Service {
	return &Service{
		db:    db,
		key:   key,
		now:   time.Now,
		newID: func() string { return ulid.Make().String() },
	}
}

// DB exposes the raw store (resolver needs reads without re-validation).
func (s *Service) DB() DB { return s.db }

// invalidError is-matches ErrInvalid without polluting the message.
type invalidError struct{ msg string }

func (e invalidError) Error() string         { return e.msg }
func (invalidError) Is(target error) bool    { return target == ErrInvalid }
func invalidf(format string, a ...any) error { return invalidError{fmt.Sprintf(format, a...)} }

// maskPrefix matches secrets.Mask output. Masked placeholders round-tripped
// from a GET must never be accepted as secret values — they would silently
// replace the real credential.
const maskPrefix = "••••"

// --- integrations -----------------------------------------------------------

func (s *Service) CreateIntegration(ctx context.Context, in Integration) (Integration, error) {
	if in.Kind == KindInternal {
		return Integration{}, invalidf("integration: the internal tracker is built in — every user has exactly one, made for them")
	}
	if err := s.validateIntegration(ctx, &in); err != nil {
		return Integration{}, err
	}
	in.ID = s.newID()
	in.CreatedAt = s.now().UTC()
	in.UpdatedAt = in.CreatedAt
	sealed, err := secrets.Seal(s.key, in.Secrets)
	if err != nil {
		return Integration{}, err
	}
	if err := s.db.UpsertIntegration(ctx, StoredIntegration{
		ID: in.ID, UserID: in.UserID, Port: in.Port, Kind: in.Kind, Name: in.Name,
		Config: in.Config, Sealed: sealed,
		CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt,
	}); err != nil {
		return Integration{}, err
	}
	return in, nil
}

// UpdateIntegration applies the §3 write semantics: config replaces whole;
// secretPatch merges — keys present replace, "" deletes, absent keep. Port
// and kind are immutable.
func (s *Service) UpdateIntegration(ctx context.Context, id, name string, config map[string]any, secretPatch map[string]string) (Integration, error) {
	cur, err := s.GetIntegration(ctx, id)
	if err != nil {
		return Integration{}, err
	}
	if name != "" {
		cur.Name = name
	}
	if config != nil {
		cur.Config = config
	}
	for k, v := range secretPatch {
		if v == "" {
			delete(cur.Secrets, k)
		} else {
			cur.Secrets[k] = v
		}
	}
	if err := s.validateIntegration(ctx, &cur); err != nil {
		return Integration{}, err
	}
	cur.UpdatedAt = s.now().UTC()
	sealed, err := secrets.Seal(s.key, cur.Secrets)
	if err != nil {
		return Integration{}, err
	}
	if err := s.db.UpsertIntegration(ctx, StoredIntegration{
		ID: cur.ID, UserID: cur.UserID, Port: cur.Port, Kind: cur.Kind, Name: cur.Name,
		Config: cur.Config, Sealed: sealed,
		CreatedAt: cur.CreatedAt, UpdatedAt: cur.UpdatedAt,
	}); err != nil {
		return Integration{}, err
	}
	return cur, nil
}

// IntegrationConfig is an integration's stored config alone — its secrets
// are not opened.
func (s *Service) IntegrationConfig(ctx context.Context, id string) (map[string]any, error) {
	r, err := s.db.GetIntegration(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.Config, nil
}

// DropIntegrationConfig removes config keys from an integration, its sealed
// secrets kept exactly as stored (never opened and resealed) — a one-off
// cleanup such as Jira's legacy status_map / type_map once the tracker lists
// have taken them over. Keys it does not have are no change.
func (s *Service) DropIntegrationConfig(ctx context.Context, id string, keys ...string) error {
	r, err := s.db.GetIntegration(ctx, id)
	if err != nil {
		return err
	}
	cfg := make(map[string]any, len(r.Config))
	for k, v := range r.Config {
		cfg[k] = v
	}
	changed := false
	for _, k := range keys {
		if _, ok := cfg[k]; ok {
			delete(cfg, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	r.Config, r.UpdatedAt = cfg, s.now().UTC()
	return s.db.UpsertIntegration(ctx, r)
}

func (s *Service) GetIntegration(ctx context.Context, id string) (Integration, error) {
	r, err := s.db.GetIntegration(ctx, id)
	if err != nil {
		return Integration{}, err
	}
	return s.open(r)
}

// ListIntegrations is one user's integrations ("" = everyone's, for the
// daemon's own passes).
func (s *Service) ListIntegrations(ctx context.Context, userID string) ([]Integration, error) {
	rows, err := s.db.ListIntegrations(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Integration, 0, len(rows))
	for _, r := range rows {
		in, err := s.open(r)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, nil
}

// DeleteIntegration removes an integration nothing references. The internal
// tracker is never deleted: every ticket without a source lives on it.
func (s *Service) DeleteIntegration(ctx context.Context, id string) error {
	r, err := s.db.GetIntegration(ctx, id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if err == nil && r.Kind == KindInternal {
		return invalidf("integration %s: the internal tracker is built in and cannot be deleted", r.Name)
	}
	return s.db.DeleteIntegration(ctx, id)
}

func (s *Service) open(r StoredIntegration) (Integration, error) {
	sec, err := secrets.Open(s.key, r.Sealed)
	if err != nil {
		return Integration{}, fmt.Errorf("integration %s (%s/%s): %w", r.ID, r.Port, r.Kind, err)
	}
	cfg := r.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	return Integration{
		ID: r.ID, UserID: r.UserID, Port: r.Port, Kind: r.Kind, Name: r.Name,
		Config: cfg, Secrets: sec,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

func (s *Service) validateIntegration(ctx context.Context, in *Integration) error {
	if strings.TrimSpace(in.Name) == "" {
		return invalidf("integration: name is required")
	}
	ok := false
	for _, p := range ValidPorts {
		if in.Port == p {
			ok = true
		}
	}
	if !ok {
		return invalidf("integration: port must be one of %v, got %q", ValidPorts, in.Port)
	}
	if in.Kind == "" {
		return invalidf("integration: kind is required")
	}
	// The internal tracker is no registered kind: nothing builds an adapter
	// for it, and it holds no credential.
	if in.Kind == KindInternal && len(in.Secrets) > 0 {
		return invalidf("integration: the internal tracker holds no secrets")
	}
	if in.Kind != KindInternal && s.KindValid != nil && !s.KindValid(in.Port, in.Kind) {
		return invalidf("integration: unknown kind %q for port %s", in.Kind, in.Port)
	}
	if in.Config == nil {
		in.Config = map[string]any{}
	}
	if in.Secrets == nil {
		in.Secrets = map[string]string{}
	}
	for k, v := range in.Secrets {
		if strings.HasPrefix(v, maskPrefix) {
			return invalidf("integration: secret %q is a masked placeholder — send the real value, or omit the key to keep the stored one", k)
		}
	}
	if in.Port == PortHarness {
		// A harness holds no credential of its own: it references the
		// owner's provider credentials (docs/model-providers-plan.md).
		if len(in.Secrets) > 0 {
			return invalidf("integration: a harness holds no secrets — reference provider credentials (credential_ids) instead")
		}
		return s.validateCredentialRefs(ctx, in)
	}
	return nil
}

// --- projects ---------------------------------------------------------------

func (s *Service) CreateProject(ctx context.Context, p Project) (Project, error) {
	if err := s.validateProject(ctx, &p); err != nil {
		return Project{}, err
	}
	p.ID = s.newID()
	p.CreatedAt = s.now().UTC()
	p.UpdatedAt = p.CreatedAt
	if err := s.db.UpsertProject(ctx, p); err != nil {
		return Project{}, err
	}
	return p, nil
}

func (s *Service) UpdateProject(ctx context.Context, id string, upd Project) (Project, error) {
	cur, err := s.db.GetProject(ctx, id)
	if err != nil {
		return Project{}, err
	}
	upd.ID = cur.ID
	upd.UserID = cur.UserID // ownership never changes through an edit
	upd.CreatedAt = cur.CreatedAt
	if err := s.validateProject(ctx, &upd); err != nil {
		return Project{}, err
	}
	upd.UpdatedAt = s.now().UTC()
	if err := s.db.UpsertProject(ctx, upd); err != nil {
		return Project{}, err
	}
	return upd, nil
}

func (s *Service) GetProject(ctx context.Context, id string) (Project, error) {
	return s.db.GetProject(ctx, id)
}

// GetProjectByName finds a user's project by name; userID "" matches any
// owner (the CLI, dev seeding).
func (s *Service) GetProjectByName(ctx context.Context, userID, name string) (Project, error) {
	return s.db.GetProjectByName(ctx, userID, name)
}

// ListProjects is one user's projects ("" = everyone's, for the daemon's
// own passes).
func (s *Service) ListProjects(ctx context.Context, userID string) ([]Project, error) {
	return s.db.ListProjects(ctx, userID)
}

func (s *Service) DeleteProject(ctx context.Context, id string) error {
	return s.db.DeleteProject(ctx, id)
}

// sameOwner reports whether a referenced row belongs to the referrer's
// owner (an ownerless pair — dev seeding, tests — counts as one person's).
func sameOwner(have, want string) bool { return have == want }

// repoNameRe is the shape of an extra repo's name: it is a directory name
// under ~/repos and a token in flows and prompts.
var repoNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// RepoNameFromURL derives a checkout name from a repo URL: the last path
// segment, without .git, lowercased, with anything unusual folded to '-'.
func RepoNameFromURL(u string) string {
	u = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(u), "/"), ".git")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(u) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// RepoURLHasPassword reports whether a repository URL embeds a password
// (https://user:token@host/…). An ssh-style git@host:path has a user, no
// password, and is fine.
func RepoURLHasPassword(raw string) bool {
	if u, err := url.Parse(raw); err == nil {
		if u.User == nil {
			return false
		}
		_, ok := u.User.Password()
		return ok
	}
	_, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return false
	}
	authority, _, _ := strings.Cut(rest, "/")
	at := strings.LastIndexByte(authority, '@')
	return at >= 0 && strings.Contains(authority[:at], ":")
}

func (s *Service) validateProject(ctx context.Context, p *Project) error {
	if strings.TrimSpace(p.Name) == "" {
		return invalidf("project: name is required")
	}
	if p.Repo.URL == "" {
		return invalidf("project: repo.url is required")
	}
	if RepoURLHasPassword(p.Repo.URL) {
		return invalidf("project: repo.url must not embed credentials — the repository integration supplies the login")
	}
	if p.Repo.DefaultBranch == "" {
		p.Repo.DefaultBranch = "main"
	}
	if p.Repo.AgentsMDPath == "" {
		p.Repo.AgentsMDPath = "AGENTS.md"
	}
	seen := map[string]bool{"repo": true} // the primary's own checkout dir
	for i := range p.Repos {
		r := &p.Repos[i]
		r.URL = strings.TrimSpace(r.URL)
		if r.URL == "" {
			return invalidf("project: repos[%d]: url is required", i)
		}
		if RepoURLHasPassword(r.URL) {
			return invalidf("project: repos[%d]: url must not embed credentials — the repository integration supplies the login", i)
		}
		if r.Name == "" {
			r.Name = RepoNameFromURL(r.URL)
		}
		if !repoNameRe.MatchString(r.Name) {
			return invalidf("project: repos[%d]: name %q must be a slug (lowercase letters, digits, - and _)", i, r.Name)
		}
		if seen[r.Name] {
			return invalidf("project: repos[%d]: name %q is already used by another repository", i, r.Name)
		}
		seen[r.Name] = true
		if r.DefaultBranch == "" {
			r.DefaultBranch = "main"
		}
		r.Locator = strings.TrimSpace(r.Locator)
	}
	if p.Commands == nil {
		p.Commands = map[string]string{}
	}
	if err := validateServices(p.Workspace.Services); err != nil {
		return err
	}
	refs := []struct{ id, port, field string }{
		{p.RepoAccountID, PortRepository, "repo_account_id"},
		{p.ProvisionerID, PortProvisioner, "provisioner_id"},
	}
	for _, r := range refs {
		if r.id == "" {
			return invalidf("project: %s is required", r.field)
		}
		in, err := s.db.GetIntegration(ctx, r.id)
		if errors.Is(err, ErrNotFound) || (err == nil && !sameOwner(in.UserID, p.UserID)) {
			// A dangling reference on a write is a validation failure (400),
			// not a 404 on the project route. Someone else's integration is
			// dangling too: a person's credentials are theirs alone.
			return invalidf("project: %s: integration %s: %v", r.field, r.id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("project: %s: %w", r.field, err)
		}
		if in.Port != r.port {
			return invalidf("project: %s references %s integration %q, need port %s", r.field, in.Port, in.Name, r.port)
		}
	}
	// The project owns a SET of available harness integrations, no default:
	// the flow names the harness kind. At least one is required (a project
	// with no harness can run no agent stage); at most one per kind, so a
	// flow's `harness: <kind>` maps to exactly one integration.
	seenID := map[string]bool{}
	seenKind := map[string]string{} // kind -> integration name, for the error
	norm := make([]string, 0, len(p.HarnessIDs))
	for _, id := range p.HarnessIDs {
		if id == "" || seenID[id] {
			continue
		}
		seenID[id] = true
		in, err := s.db.GetIntegration(ctx, id)
		if errors.Is(err, ErrNotFound) || (err == nil && !sameOwner(in.UserID, p.UserID)) {
			return invalidf("project: harness_ids: integration %s: %v", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("project: harness_ids: %w", err)
		}
		if in.Port != PortHarness {
			return invalidf("project: harness_ids references %s integration %q, need port %s", in.Port, in.Name, PortHarness)
		}
		if other, dup := seenKind[in.Kind]; dup {
			return invalidf("project: harness_ids has two %q harnesses (%q and %q); the set allows one per kind", in.Kind, other, in.Name)
		}
		seenKind[in.Kind] = in.Name
		norm = append(norm, id)
	}
	if len(norm) == 0 {
		return invalidf("project: harness_ids is required (at least one harness integration)")
	}
	p.HarnessIDs = norm
	// default_flow is the project's own choice — the UI offers the flows it
	// can run, templates included — never a name the code knows. It resolves
	// against the DB first, then the built-ins (same layering the engine's
	// flow source uses).
	if p.DefaultFlow == "" {
		return invalidf("project: default_flow is required")
	}
	if _, err := s.db.GetFlow(ctx, p.ID, p.DefaultFlow); errors.Is(err, ErrNotFound) {
		if builtin.Flow(p.DefaultFlow) == nil {
			return invalidf("project: default_flow %q: %v", p.DefaultFlow, err)
		}
	} else if err != nil {
		return fmt.Errorf("project: default_flow %q: %w", p.DefaultFlow, err)
	}
	return nil
}

// --- flows ------------------------------------------------------------------

// SaveFlow validates the YAML through the flow parser (line-precise errors
// travel to the API verbatim, §7) and upserts current + immutable version.
// The document's name must match the addressed name — names are ids.
// layout is the graph editor's display metadata (DESIGN3 §2.2): nil keeps
// the stored one (a YAML-tab edit must not wipe node positions), non-nil
// replaces it; it never influences the hash or versions.
func (s *Service) SaveFlow(ctx context.Context, projectID, name, yamlSrc string, layout json.RawMessage) (FlowRow, error) {
	if projectID == "" {
		return FlowRow{}, invalidf("flow: a project is required (flows belong to one project)")
	}
	if _, err := s.db.GetProject(ctx, projectID); err != nil {
		return FlowRow{}, fmt.Errorf("flow: project %s: %w", projectID, err)
	}
	f, err := flow.Parse(name, []byte(yamlSrc))
	if err != nil {
		return FlowRow{}, invalidf("%v", err)
	}
	if f.Name != name {
		return FlowRow{}, invalidf("flow document is named %q but addressed as %q — names are immutable ids (create a new flow to rename)", f.Name, name)
	}
	if len(layout) > 0 && !json.Valid(layout) {
		return FlowRow{}, invalidf("flow %s: layout is not valid JSON", name)
	}
	row := FlowRow{ProjectID: projectID, Name: name, YAML: yamlSrc, Hash: f.Hash,
		Layout: layout, UpdatedAt: s.now().UTC()}
	if err := s.db.UpsertFlow(ctx, row); err != nil {
		return FlowRow{}, err
	}
	if len(layout) == 0 {
		// Reflect the kept layout back to the caller.
		if cur, err := s.db.GetFlow(ctx, projectID, name); err == nil {
			row.Layout = cur.Layout
		}
	}
	return row, nil
}

// CopyFlow clones one project's flow into another. This is how a good flow
// spreads now that flows are owned rather than shared: the copy is a NEW
// document in the target project, with its own history from this moment —
// editing it never reaches back, which is the whole point of ownership.
//
// The source may be a built-in template (no project row of that name), which
// is how a project adopts one for editing.
func (s *Service) CopyFlow(ctx context.Context, fromProject, fromName, toProject, toName string) (FlowRow, error) {
	if toName == "" {
		toName = fromName
	}
	if fromProject == toProject && fromName == toName {
		return FlowRow{}, invalidf("flow %q: copying onto itself needs a different name", fromName)
	}
	yamlSrc, err := s.flowText(ctx, fromProject, fromName)
	if err != nil {
		return FlowRow{}, err
	}
	return s.saveCopy(ctx, yamlSrc, fromName, toProject, toName)
}

// CopyTemplate adopts a compiled-in template into a project, optionally under
// a new name. A template belongs to no project, so unlike CopyFlow there is no
// source project to address — and no "copy onto itself" to refuse: adopting
// the template under its own name is exactly what shadows it for that project.
func (s *Service) CopyTemplate(ctx context.Context, name, toProject, toName string) (FlowRow, error) {
	if toName == "" {
		toName = name
	}
	yamlSrc, ok := builtin.FlowYAML(name)
	if !ok {
		return FlowRow{}, notFound("flow template", name)
	}
	return s.saveCopy(ctx, yamlSrc, name, toProject, toName)
}

// saveCopy lands a document under a new owner. The document names itself, and
// the name is its id: a copy under a new name must say so in the YAML, or the
// save would refuse it. Copying onto a name the target already has refuses
// rather than silently replacing someone else's flow.
func (s *Service) saveCopy(ctx context.Context, yamlSrc, fromName, toProject, toName string) (FlowRow, error) {
	if toName != fromName {
		var err error
		yamlSrc, err = flow.Rename(yamlSrc, toName)
		if err != nil {
			return FlowRow{}, invalidf("%v", err)
		}
	}
	if _, err := s.db.GetFlow(ctx, toProject, toName); err == nil {
		return FlowRow{}, fmt.Errorf("flow %q already exists in the target project: %w", toName, ErrConflict)
	} else if !errors.Is(err, ErrNotFound) {
		return FlowRow{}, err
	}
	return s.SaveFlow(ctx, toProject, toName, yamlSrc, nil)
}

// flowText reads a flow's YAML from a project, falling back to the built-in
// template of that name — the same layering the engine resolves through.
func (s *Service) flowText(ctx context.Context, projectID, name string) (string, error) {
	row, err := s.db.GetFlow(ctx, projectID, name)
	if err == nil {
		return row.YAML, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	if src, ok := builtin.FlowYAML(name); ok {
		return src, nil
	}
	return "", err
}

func (s *Service) GetFlow(ctx context.Context, projectID, name string) (FlowRow, error) {
	return s.db.GetFlow(ctx, projectID, name)
}

// ListFlows returns one project's flows, or every project's when projectID is
// empty (the cross-project overview).
func (s *Service) ListFlows(ctx context.Context, projectID string) ([]FlowRow, error) {
	return s.db.ListFlows(ctx, projectID)
}

// DeleteFlow removes the project's document. Its binding goes with it —
// unless a built-in template of that name is underneath: then the delete
// is a reset to the template, which the project keeps configured.
func (s *Service) DeleteFlow(ctx context.Context, projectID, name string) error {
	if err := s.db.DeleteFlow(ctx, projectID, name); err != nil {
		return err
	}
	if _, template := builtin.FlowYAML(name); template {
		return nil
	}
	if err := s.db.DeleteFlowBinding(ctx, projectID, name); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

func (s *Service) FlowVersions(ctx context.Context, projectID, name string) ([]FlowVersion, error) {
	return s.db.ListFlowVersions(ctx, projectID, name)
}

func (s *Service) FlowVersion(ctx context.Context, projectID, hash string) (FlowVersion, error) {
	return s.db.GetFlowVersion(ctx, projectID, hash)
}
