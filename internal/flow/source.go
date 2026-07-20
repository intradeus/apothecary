package flow

import (
	"context"
	"fmt"
)

// Source resolves flow names at run creation and recovery (DESIGN2 §7):
// DB-backed in production (confstore), MapSource in tests. Current-version
// semantics are unchanged from v1's map — recovery uses the current
// document and warns on hash drift.
type Source interface {
	Get(ctx context.Context, name string) (*Flow, error)
}

// MapSource is the in-memory Source used by tests and `apothecaryd validate`.
type MapSource map[string]*Flow

func (m MapSource) Get(_ context.Context, name string) (*Flow, error) {
	f, ok := m[name]
	if !ok {
		return nil, fmt.Errorf("flow %q not found", name)
	}
	return f, nil
}
