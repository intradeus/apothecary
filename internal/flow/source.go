package flow

import (
	"context"
	"errors"
	"fmt"
)

// ErrNotFound distinguishes a genuinely missing flow (terminal for a run)
// from transient source errors (DB blips — the run stays recoverable).
var ErrNotFound = errors.New("flow not found")

// Source resolves flow names at run creation (DESIGN2 §7): DB-backed in
// production (confstore), MapSource in tests. Get returns the CURRENT
// document for a name — what a new run pins itself to.
//
// A flow is addressed by (project, name), never by name alone: its stages name
// this project's commands, its agents know this repository. The built-ins are
// the one global layer, and they are templates — resolved for any project that
// has no row of its own, and copied into the project the moment it saves one.
type Source interface {
	Get(ctx context.Context, projectID, name string) (*Flow, error)
}

// PinnedSource resolves the exact document a run started with, by content
// hash. A run records its flow's hash at creation, so every later stage —
// and every recovery — executes the text the run began with rather than
// whatever the name resolves to now (PLAN5 M19). Since v3 the document
// carries its agents, so this pins the prompts too.
//
// Optional: a Source that does not implement it (MapSource in tests) simply
// keeps the by-name behavior. ErrNotFound means "that version is gone" and
// the caller falls back to the current document.
type PinnedSource interface {
	GetVersion(ctx context.Context, projectID, name, hash string) (*Flow, error)
}

// MapSource is the in-memory Source used by tests and `apothecaryd validate`:
// one document per name, shared by every project. Project ownership is a
// property of the STORE, not of the flow language, so nothing here needs to
// know which project is asking.
type MapSource map[string]*Flow

func (m MapSource) Get(_ context.Context, _, name string) (*Flow, error) {
	f, ok := m[name]
	if !ok {
		return nil, fmt.Errorf("flow %q: %w", name, ErrNotFound)
	}
	return f, nil
}

// GetVersion serves the mapped document when its hash matches; a map holds
// one version per name, so anything else is genuinely absent.
func (m MapSource) GetVersion(_ context.Context, _, name, hash string) (*Flow, error) {
	f, ok := m[name]
	if !ok || f.Hash != hash {
		return nil, fmt.Errorf("flow %q version %s: %w", name, hash, ErrNotFound)
	}
	return f, nil
}
