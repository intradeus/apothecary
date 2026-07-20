// Package confstore is the config plane's storage service (DESIGN2.md §2,
// §3): integrations, projects, flows and settings, CRUD'd only through the
// API/CPUI and persisted in Postgres. The Service seals/opens integration
// secrets with the master key and owns validation; the DB interface is
// implemented by pgstore (SQL) and Mem (tests, seeding).
package confstore

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Ports an integration can implement (DESIGN2 §2).
const (
	PortForge       = "forge"
	PortProvisioner = "provisioner"
	PortHarness     = "harness"
	PortProvider    = "provider"
)

var ValidPorts = []string{PortForge, PortProvisioner, PortHarness, PortProvider}

var (
	ErrNotFound = errors.New("not found")
	// ErrReferenced refuses deletes of rows something still points at; the
	// message names the referrers (§2: API maps to 409).
	ErrReferenced = errors.New("still referenced")
	// ErrConflict signals a unique-name collision (409).
	ErrConflict = errors.New("name already in use")
)

// Integration is a credentialed connection (§2) with decrypted secrets —
// service-layer only; at rest and over the API secrets are sealed/masked.
type Integration struct {
	ID        string            `json:"id"`
	Port      string            `json:"port"`
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Config    map[string]any    `json:"config"`
	Secrets   map[string]string `json:"-"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// StoredIntegration is the at-rest shape: secrets sealed (§3).
type StoredIntegration struct {
	ID, Port, Kind, Name string
	Config               map[string]any
	Sealed               []byte
	CreatedAt, UpdatedAt time.Time
}

// RepoConfig is projects.repo (§2).
type RepoConfig struct {
	URL           string `json:"url"`
	Locator       string `json:"locator"`
	DefaultBranch string `json:"default_branch"`
	AgentsMDPath  string `json:"agents_md_path"`
}

// WorkspaceConfig is projects.workspace (§2, §8).
type WorkspaceConfig struct {
	DockerfileFragment string   `json:"dockerfile_fragment,omitempty"`
	ExtraDomains       []string `json:"extra_domains,omitempty"`
	// Template names a user-managed provisioner template (§8.4 escape
	// hatch + import-config compatibility path): the engine skips the image
	// pipeline and template pushes entirely.
	Template string `json:"template,omitempty"`
}

// Project picks one integration per port and adds repo/commands/workspace
// specifics (§2).
type Project struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	ForgeID       string            `json:"forge_id"`
	ProvisionerID string            `json:"provisioner_id"`
	HarnessID     string            `json:"harness_id"`
	ProviderIDs   []string          `json:"provider_ids"`
	Repo          RepoConfig        `json:"repo"`
	Commands      map[string]string `json:"commands"`
	Workspace     WorkspaceConfig   `json:"workspace"`
	DefaultFlow   string            `json:"default_flow"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// FlowRow is the current version of a stored flow (§7).
type FlowRow struct {
	Name      string    `json:"name"`
	YAML      string    `json:"yaml"`
	Hash      string    `json:"hash"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FlowVersion is one immutable content-addressed version (§7).
type FlowVersion struct {
	Hash      string    `json:"hash"`
	Name      string    `json:"name"`
	YAML      string    `json:"yaml"`
	CreatedAt time.Time `json:"created_at"`
}

// DB is the persistence boundary. Implementations enforce the §2 delete
// rules that need adjacent tables (project→runs, integration→projects,
// flow→projects/runs) and return ErrNotFound/ErrReferenced/ErrConflict.
type DB interface {
	UpsertIntegration(ctx Ctx, r StoredIntegration) error
	GetIntegration(ctx Ctx, id string) (StoredIntegration, error)
	ListIntegrations(ctx Ctx) ([]StoredIntegration, error)
	DeleteIntegration(ctx Ctx, id string) error

	UpsertProject(ctx Ctx, p Project) error
	GetProject(ctx Ctx, id string) (Project, error)
	GetProjectByName(ctx Ctx, name string) (Project, error)
	ListProjects(ctx Ctx) ([]Project, error)
	DeleteProject(ctx Ctx, id string) error

	UpsertFlow(ctx Ctx, row FlowRow) error // also inserts the immutable version (ON CONFLICT DO NOTHING)
	GetFlow(ctx Ctx, name string) (FlowRow, error)
	ListFlows(ctx Ctx) ([]FlowRow, error)
	DeleteFlow(ctx Ctx, name string) error
	GetFlowVersion(ctx Ctx, hash string) (FlowVersion, error)
	ListFlowVersions(ctx Ctx, name string) ([]FlowVersion, error)

	GetSettings(ctx Ctx) (map[string]string, error) // key -> raw JSON value
	PutSetting(ctx Ctx, key, value string) error
}

// Ctx aliases context.Context (brevity in the wide DB interface).
type Ctx = context.Context

func notFound(what, id string) error {
	return fmt.Errorf("%s %s: %w", what, id, ErrNotFound)
}
