package engine

import (
	"context"
	"fmt"
)

// The instance's capacity limits are an admin's setting, stored in the
// database and edited in the UI (System → Limits). Nothing in the deployment
// environment sets them; an instance that never had them set runs on the
// defaults (Limits{}.withDefaults()).

// LimitsStore is where an instance's limits are kept. ok=false when they were
// never set.
type LimitsStore interface {
	LoadLimits(ctx context.Context) (maxConcurrentRuns, maxEnvironments int, ok bool, err error)
	SaveLimits(ctx context.Context, maxConcurrentRuns, maxEnvironments int) error
}

// StoredLimits serves the stored limits as the engine's SettingsSource —
// read on every admission, so an admin's change applies to the next run.
type StoredLimits struct{ Store LimitsStore }

func (s StoredLimits) Limits(ctx context.Context) (Limits, error) {
	runs, envs, ok, err := s.Store.LoadLimits(ctx)
	if err != nil {
		return Limits{}, err
	}
	if !ok {
		return Limits{}.withDefaults(), nil
	}
	return Limits{MaxConcurrentRuns: runs, MaxEnvironments: envs}.withDefaults(), nil
}

// Set validates and stores new limits.
func (s StoredLimits) Set(ctx context.Context, l Limits) error {
	if err := ValidateLimits(l); err != nil {
		return err
	}
	return s.Store.SaveLimits(ctx, l.MaxConcurrentRuns, l.MaxEnvironments)
}

// ValidateLimits: each cap is a positive count, or Unlimited (-1). Zero is
// refused rather than read as "nothing may run".
func ValidateLimits(l Limits) error {
	for name, v := range map[string]int{"max_concurrent_runs": l.MaxConcurrentRuns, "max_environments": l.MaxEnvironments} {
		if v == 0 || v < Unlimited {
			return fmt.Errorf("%s must be a positive count or -1 (no cap), got %d", name, v)
		}
	}
	return nil
}
