package resolve

import (
	"context"

	"apothecary/internal/confstore"
	"apothecary/internal/engine"
)

// Settings bridges the confstore settings rows to engine.SettingsSource
// (DESIGN2 §6.4: read on every admission decision, no cache).
type Settings struct{ Conf *confstore.Service }

func (s Settings) Limits(ctx context.Context) (engine.Limits, error) {
	set, err := s.Conf.Settings(ctx)
	if err != nil {
		return engine.Limits{}, err
	}
	return engine.Limits{
		MaxConcurrentRuns: set.MaxConcurrentRuns,
		MaxEnvironments:   set.MaxEnvironments,
	}, nil
}

var _ engine.SettingsSource = Settings{}
