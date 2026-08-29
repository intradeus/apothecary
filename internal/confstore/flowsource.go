package confstore

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"apothecary/internal/flow"
)

// FlowSource adapts the flows table to flow.Source (DESIGN2 §7): documents
// parse at read time, cached by content hash (saves re-validate, so a
// cached hash is always parseable).
func (s *Service) FlowSource() flow.Source {
	return &flowSource{svc: s, cache: map[string]*flow.Flow{}}
}

type flowSource struct {
	svc   *Service
	mu    sync.Mutex
	cache map[string]*flow.Flow
}

func (fs *flowSource) Get(ctx context.Context, projectID, name string) (*flow.Flow, error) {
	row, err := fs.svc.db.GetFlow(ctx, projectID, name)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("flow %q: %w", name, flow.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return fs.parse(name, row.Hash, row.YAML)
}

// GetVersion serves the immutable content-addressed version, so a run keeps
// executing the document it started with however the name is later edited
// (flow.PinnedSource, PLAN5 M19).
func (fs *flowSource) GetVersion(ctx context.Context, projectID, name, hash string) (*flow.Flow, error) {
	v, err := fs.svc.db.GetFlowVersion(ctx, projectID, hash)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("flow %q version %s: %w", name, hash, flow.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return fs.parse(name, v.Hash, v.YAML)
}

// parse returns the cached document for a content hash, parsing on miss. The
// cache is keyed by hash, so a cached entry is always parseable and current
// and pinned reads share it.
func (fs *flowSource) parse(name, hash, yaml string) (*flow.Flow, error) {
	fs.mu.Lock()
	cached, ok := fs.cache[hash]
	fs.mu.Unlock()
	if ok {
		return cached, nil
	}
	f, err := flow.Parse(name, []byte(yaml))
	if err != nil {
		return nil, err
	}
	fs.mu.Lock()
	fs.cache[hash] = f
	fs.mu.Unlock()
	return f, nil
}
