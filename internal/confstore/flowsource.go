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

func (fs *flowSource) Get(ctx context.Context, name string) (*flow.Flow, error) {
	row, err := fs.svc.db.GetFlow(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("flow %q: %w", name, flow.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	fs.mu.Lock()
	cached, ok := fs.cache[row.Hash]
	fs.mu.Unlock()
	if ok {
		return cached, nil
	}
	f, err := flow.Parse(name, []byte(row.YAML))
	if err != nil {
		return nil, err
	}
	fs.mu.Lock()
	fs.cache[row.Hash] = f
	fs.mu.Unlock()
	return f, nil
}
