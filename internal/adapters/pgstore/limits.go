package pgstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"apothecary/internal/ports/envport"
)

// LoadLimits reads the instance's capacity limits and sandbox policy;
// ok=false when an admin never set them (the engine then uses its defaults).
func (s *Store) LoadLimits(ctx context.Context) (int, int, envport.SandboxPolicy, bool, error) {
	var runs, envs int
	var sz envport.SandboxPolicy
	err := s.pool.QueryRow(ctx, `
		SELECT max_concurrent_runs, max_environments,
		       sandbox_default_milli_cpu, sandbox_default_memory_mib, sandbox_max_milli_cpu, sandbox_max_memory_mib,
		       sandbox_idle_minutes
		FROM instance_limits WHERE id`).Scan(&runs, &envs,
		&sz.Default.MilliCPU, &sz.Default.MemoryMiB, &sz.Ceiling.MilliCPU, &sz.Ceiling.MemoryMiB, &sz.IdleMinutes)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, envport.SandboxPolicy{}, false, nil
	}
	if err != nil {
		return 0, 0, envport.SandboxPolicy{}, false, err
	}
	return runs, envs, sz, true, nil
}

// SaveLimits stores them (the engine validated them first).
func (s *Store) SaveLimits(ctx context.Context, runs, envs int, sz envport.SandboxPolicy) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO instance_limits (id, max_concurrent_runs, max_environments,
		  sandbox_default_milli_cpu, sandbox_default_memory_mib, sandbox_max_milli_cpu, sandbox_max_memory_mib,
		  sandbox_idle_minutes, updated_at)
		VALUES (true, $1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (id) DO UPDATE SET
		  max_concurrent_runs        = EXCLUDED.max_concurrent_runs,
		  max_environments           = EXCLUDED.max_environments,
		  sandbox_default_milli_cpu  = EXCLUDED.sandbox_default_milli_cpu,
		  sandbox_default_memory_mib = EXCLUDED.sandbox_default_memory_mib,
		  sandbox_max_milli_cpu      = EXCLUDED.sandbox_max_milli_cpu,
		  sandbox_max_memory_mib     = EXCLUDED.sandbox_max_memory_mib,
		  sandbox_idle_minutes       = EXCLUDED.sandbox_idle_minutes,
		  updated_at                 = now()`, runs, envs,
		sz.Default.MilliCPU, sz.Default.MemoryMiB, sz.Ceiling.MilliCPU, sz.Ceiling.MemoryMiB, sz.IdleMinutes)
	return err
}
