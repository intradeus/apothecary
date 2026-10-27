package pgstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// LoadLimits reads the instance's capacity limits; ok=false when an admin
// never set them (the engine then uses its defaults).
func (s *Store) LoadLimits(ctx context.Context) (int, int, bool, error) {
	var runs, envs int
	err := s.pool.QueryRow(ctx,
		`SELECT max_concurrent_runs, max_environments FROM instance_limits WHERE id`).Scan(&runs, &envs)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	return runs, envs, true, nil
}

// SaveLimits stores them (the engine validated them first).
func (s *Store) SaveLimits(ctx context.Context, runs, envs int) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO instance_limits (id, max_concurrent_runs, max_environments, updated_at)
		VALUES (true, $1, $2, now())
		ON CONFLICT (id) DO UPDATE SET
		  max_concurrent_runs = EXCLUDED.max_concurrent_runs,
		  max_environments    = EXCLUDED.max_environments,
		  updated_at          = now()`, runs, envs)
	return err
}
