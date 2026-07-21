package confstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"apothecary/internal/flow"
	"apothecary/internal/secrets"
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
	if err := s.validateIntegration(&in); err != nil {
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
		ID: in.ID, Port: in.Port, Kind: in.Kind, Name: in.Name,
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
	if err := s.validateIntegration(&cur); err != nil {
		return Integration{}, err
	}
	cur.UpdatedAt = s.now().UTC()
	sealed, err := secrets.Seal(s.key, cur.Secrets)
	if err != nil {
		return Integration{}, err
	}
	if err := s.db.UpsertIntegration(ctx, StoredIntegration{
		ID: cur.ID, Port: cur.Port, Kind: cur.Kind, Name: cur.Name,
		Config: cur.Config, Sealed: sealed,
		CreatedAt: cur.CreatedAt, UpdatedAt: cur.UpdatedAt,
	}); err != nil {
		return Integration{}, err
	}
	return cur, nil
}

func (s *Service) GetIntegration(ctx context.Context, id string) (Integration, error) {
	r, err := s.db.GetIntegration(ctx, id)
	if err != nil {
		return Integration{}, err
	}
	return s.open(r)
}

func (s *Service) ListIntegrations(ctx context.Context) ([]Integration, error) {
	rows, err := s.db.ListIntegrations(ctx)
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

func (s *Service) DeleteIntegration(ctx context.Context, id string) error {
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
		ID: r.ID, Port: r.Port, Kind: r.Kind, Name: r.Name,
		Config: cfg, Secrets: sec,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

func (s *Service) validateIntegration(in *Integration) error {
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
	if s.KindValid != nil && !s.KindValid(in.Port, in.Kind) {
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

func (s *Service) GetProjectByName(ctx context.Context, name string) (Project, error) {
	return s.db.GetProjectByName(ctx, name)
}

func (s *Service) ListProjects(ctx context.Context) ([]Project, error) {
	return s.db.ListProjects(ctx)
}

func (s *Service) DeleteProject(ctx context.Context, id string) error {
	return s.db.DeleteProject(ctx, id)
}

func (s *Service) validateProject(ctx context.Context, p *Project) error {
	if strings.TrimSpace(p.Name) == "" {
		return invalidf("project: name is required")
	}
	if p.Repo.URL == "" {
		return invalidf("project: repo.url is required")
	}
	if p.Repo.DefaultBranch == "" {
		p.Repo.DefaultBranch = "main"
	}
	if p.Repo.AgentsMDPath == "" {
		p.Repo.AgentsMDPath = "AGENTS.md"
	}
	if p.Commands == nil {
		p.Commands = map[string]string{}
	}
	// commands.setup is OPTIONAL in v2 (DESIGN2 §8 decision): absent means
	// bootstrap step 6 is skipped.
	refs := []struct{ id, port, field string }{
		{p.ForgeID, PortForge, "forge_id"},
		{p.ProvisionerID, PortProvisioner, "provisioner_id"},
		{p.HarnessID, PortHarness, "harness_id"},
	}
	for _, r := range refs {
		if r.id == "" {
			return invalidf("project: %s is required", r.field)
		}
		in, err := s.db.GetIntegration(ctx, r.id)
		if errors.Is(err, ErrNotFound) {
			// A dangling reference on a write is a validation failure (400),
			// not a 404 on the project route.
			return invalidf("project: %s: %v", r.field, err)
		}
		if err != nil {
			return fmt.Errorf("project: %s: %w", r.field, err)
		}
		if in.Port != r.port {
			return invalidf("project: %s references %s integration %q, need port %s", r.field, in.Port, in.Name, r.port)
		}
	}
	// Per-step harness: normalize and validate the harness set. The default
	// (HarnessID, validated above) is always a member; the set holds at most
	// one integration per kind, so a stage's `harness: <kind>` maps to exactly
	// one integration.
	hasDefault := false
	for _, id := range p.HarnessIDs {
		if id == p.HarnessID {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		p.HarnessIDs = append([]string{p.HarnessID}, p.HarnessIDs...)
	}
	seenID := map[string]bool{}
	seenKind := map[string]string{} // kind -> integration name, for the error
	norm := make([]string, 0, len(p.HarnessIDs))
	for _, id := range p.HarnessIDs {
		if id == "" || seenID[id] {
			continue
		}
		seenID[id] = true
		in, err := s.db.GetIntegration(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return invalidf("project: harness_ids: %v", err)
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
	p.HarnessIDs = norm
	if err := ValidateFragment(p.Workspace.DockerfileFragment); err != nil {
		return err
	}
	// workspace.template is OPTIONAL since M9: empty = the apothecary-managed
	// template + built image (§8); set = the custom-template escape hatch.
	// Flows are user-defined — there is no built-in default to fall back to.
	if p.DefaultFlow == "" {
		return invalidf("project: default_flow is required (create a flow first)")
	}
	if _, err := s.db.GetFlow(ctx, p.DefaultFlow); errors.Is(err, ErrNotFound) {
		return invalidf("project: default_flow %q: %v", p.DefaultFlow, err)
	} else if err != nil {
		return fmt.Errorf("project: default_flow %q: %w", p.DefaultFlow, err)
	}
	return nil
}

// ValidateFragment enforces the §8.2 Dockerfile fragment rules: physical
// lines whose first token is FROM/COPY/ADD/USER/ENTRYPOINT/CMD (any case)
// are rejected — the build context has no files and the engine owns
// user/entry semantics.
func ValidateFragment(fragment string) error {
	forbidden := map[string]bool{
		"FROM": true, "COPY": true, "ADD": true,
		"USER": true, "ENTRYPOINT": true, "CMD": true,
		// SHELL would rewrite how the engine-owned harness RUN lines execute;
		// ONBUILD would smuggle any forbidden instruction past a first-token
		// check.
		"SHELL": true, "ONBUILD": true,
	}
	lines := strings.Split(fragment, "\n")
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if forbidden[strings.ToUpper(fields[0])] {
			return invalidf("workspace.dockerfile_fragment line %d: %s is not allowed in fragments (engine-owned)", i+1, strings.ToUpper(fields[0]))
		}
	}
	// A trailing line continuation would splice the fragment into the next
	// engine-owned line of the composed Dockerfile (Docker strips comment
	// lines before joining continuations, so scan back past those).
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasSuffix(t, "\\") {
			return invalidf("workspace.dockerfile_fragment line %d: must not end with a line continuation (engine-owned lines follow)", i+1)
		}
		break
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
func (s *Service) SaveFlow(ctx context.Context, name, yamlSrc string, layout json.RawMessage) (FlowRow, error) {
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
	row := FlowRow{Name: name, YAML: yamlSrc, Hash: f.Hash, Layout: layout, UpdatedAt: s.now().UTC()}
	if err := s.db.UpsertFlow(ctx, row); err != nil {
		return FlowRow{}, err
	}
	if len(layout) == 0 {
		// Reflect the kept layout back to the caller.
		if cur, err := s.db.GetFlow(ctx, name); err == nil {
			row.Layout = cur.Layout
		}
	}
	return row, nil
}

func (s *Service) GetFlow(ctx context.Context, name string) (FlowRow, error) {
	return s.db.GetFlow(ctx, name)
}

func (s *Service) ListFlows(ctx context.Context) ([]FlowRow, error) {
	return s.db.ListFlows(ctx)
}

func (s *Service) DeleteFlow(ctx context.Context, name string) error {
	return s.db.DeleteFlow(ctx, name)
}

func (s *Service) FlowVersions(ctx context.Context, name string) ([]FlowVersion, error) {
	return s.db.ListFlowVersions(ctx, name)
}

func (s *Service) FlowVersion(ctx context.Context, hash string) (FlowVersion, error) {
	return s.db.GetFlowVersion(ctx, hash)
}
