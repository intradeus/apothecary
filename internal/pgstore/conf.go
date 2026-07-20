package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"factory/internal/confstore"
)

// Config-plane persistence (DESIGN2 §2): pgstore implements confstore.DB.
// Delete rules that need adjacent tables are enforced here with explicit
// queries; the schema's FK RESTRICT is the belt to these braces.

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

func isFK(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23503"
}

func (s *Store) UpsertIntegration(ctx context.Context, r confstore.StoredIntegration) error {
	cfg, err := json.Marshal(r.Config)
	if err != nil {
		return err
	}
	sealed := r.Sealed
	if sealed == nil {
		sealed = []byte{}
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO integrations (id, port, kind, name, config, secrets, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO UPDATE SET
			name=$4, config=$5, secrets=$6, updated_at=$8`,
		r.ID, r.Port, r.Kind, r.Name, cfg, sealed, r.CreatedAt, r.UpdatedAt)
	if isUnique(err) {
		return fmt.Errorf("integration %s/%s: %w", r.Port, r.Name, confstore.ErrConflict)
	}
	return err
}

func (s *Store) GetIntegration(ctx context.Context, id string) (confstore.StoredIntegration, error) {
	var r confstore.StoredIntegration
	var cfg []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, port, kind, name, config, secrets, created_at, updated_at
		FROM integrations WHERE id=$1`, id).
		Scan(&r.ID, &r.Port, &r.Kind, &r.Name, &cfg, &r.Sealed, &r.CreatedAt, &r.UpdatedAt)
	if err == pgx.ErrNoRows {
		return r, fmt.Errorf("integration %s: %w", id, confstore.ErrNotFound)
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(cfg, &r.Config); err != nil {
		return r, fmt.Errorf("integration %s: config: %w", id, err)
	}
	return r, nil
}

func (s *Store) ListIntegrations(ctx context.Context) ([]confstore.StoredIntegration, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, port, kind, name, config, secrets, created_at, updated_at
		FROM integrations ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confstore.StoredIntegration
	for rows.Next() {
		var r confstore.StoredIntegration
		var cfg []byte
		if err := rows.Scan(&r.ID, &r.Port, &r.Kind, &r.Name, &cfg, &r.Sealed, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(cfg, &r.Config); err != nil {
			return nil, fmt.Errorf("integration %s: config: %w", r.ID, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteIntegration(ctx context.Context, id string) error {
	var refs []string
	rows, err := s.pool.Query(ctx, `
		SELECT name FROM projects
		WHERE forge_id=$1 OR provisioner_id=$1 OR harness_id=$1
		ORDER BY name`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		refs = append(refs, n)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(refs) > 0 {
		return fmt.Errorf("integration %s is used by project(s) %s: %w", id, strings.Join(refs, ", "), confstore.ErrReferenced)
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM integrations WHERE id=$1`, id)
	if isFK(err) {
		return fmt.Errorf("integration %s: %w", id, confstore.ErrReferenced)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("integration %s: %w", id, confstore.ErrNotFound)
	}
	return nil
}

func (s *Store) UpsertProject(ctx context.Context, p confstore.Project) error {
	repo, err := json.Marshal(p.Repo)
	if err != nil {
		return err
	}
	cmds, err := json.Marshal(p.Commands)
	if err != nil {
		return err
	}
	ws, err := json.Marshal(p.Workspace)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO projects (id, name, forge_id, provisioner_id, harness_id,
		                      repo, commands, workspace, default_flow, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (id) DO UPDATE SET
			name=$2, forge_id=$3, provisioner_id=$4, harness_id=$5,
			repo=$6, commands=$7, workspace=$8, default_flow=$9, updated_at=$11`,
		p.ID, p.Name, p.ForgeID, p.ProvisionerID, p.HarnessID,
		repo, cmds, ws, p.DefaultFlow, p.CreatedAt, p.UpdatedAt)
	if isUnique(err) {
		return fmt.Errorf("project %s: %w", p.Name, confstore.ErrConflict)
	}
	return err
}

func scanProject(row pgx.Row) (confstore.Project, error) {
	var p confstore.Project
	var repo, cmds, ws []byte
	err := row.Scan(&p.ID, &p.Name, &p.ForgeID, &p.ProvisionerID, &p.HarnessID,
		&repo, &cmds, &ws, &p.DefaultFlow, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(repo, &p.Repo); err != nil {
		return p, err
	}
	if err := json.Unmarshal(cmds, &p.Commands); err != nil {
		return p, err
	}
	if err := json.Unmarshal(ws, &p.Workspace); err != nil {
		return p, err
	}
	return p, nil
}

const projectCols = `id, name, forge_id, provisioner_id, harness_id,
	repo, commands, workspace, default_flow, created_at, updated_at`

func (s *Store) GetProject(ctx context.Context, id string) (confstore.Project, error) {
	p, err := scanProject(s.pool.QueryRow(ctx,
		`SELECT `+projectCols+` FROM projects WHERE id=$1`, id))
	if err == pgx.ErrNoRows {
		return p, fmt.Errorf("project %s: %w", id, confstore.ErrNotFound)
	}
	return p, err
}

func (s *Store) GetProjectByName(ctx context.Context, name string) (confstore.Project, error) {
	p, err := scanProject(s.pool.QueryRow(ctx,
		`SELECT `+projectCols+` FROM projects WHERE name=$1`, name))
	if err == pgx.ErrNoRows {
		return p, fmt.Errorf("project %s: %w", name, confstore.ErrNotFound)
	}
	return p, err
}

func (s *Store) ListProjects(ctx context.Context) ([]confstore.Project, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+projectCols+` FROM projects ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confstore.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteProject(ctx context.Context, id string) error {
	var blocking []string
	rows, err := s.pool.Query(ctx,
		`SELECT run_id FROM runs WHERE project_id=$1 AND state <> 'archived' ORDER BY run_id`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return err
		}
		blocking = append(blocking, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(blocking) > 0 {
		return fmt.Errorf("project %s has non-archived run(s) %s: %w", id, strings.Join(blocking, ", "), confstore.ErrReferenced)
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM projects WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("project %s: %w", id, confstore.ErrNotFound)
	}
	return nil
}

// --- flows ------------------------------------------------------------------

func (s *Store) UpsertFlow(ctx context.Context, row confstore.FlowRow) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO flows (name, yaml, hash, updated_at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (name) DO UPDATE SET yaml=$2, hash=$3, updated_at=$4`,
		row.Name, row.YAML, row.Hash, row.UpdatedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO flow_versions (hash, name, yaml, created_at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (hash) DO NOTHING`,
		row.Hash, row.Name, row.YAML, row.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) GetFlow(ctx context.Context, name string) (confstore.FlowRow, error) {
	var r confstore.FlowRow
	err := s.pool.QueryRow(ctx,
		`SELECT name, yaml, hash, updated_at FROM flows WHERE name=$1`, name).
		Scan(&r.Name, &r.YAML, &r.Hash, &r.UpdatedAt)
	if err == pgx.ErrNoRows {
		return r, fmt.Errorf("flow %s: %w", name, confstore.ErrNotFound)
	}
	return r, err
}

func (s *Store) ListFlows(ctx context.Context) ([]confstore.FlowRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT name, yaml, hash, updated_at FROM flows ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confstore.FlowRow
	for rows.Next() {
		var r confstore.FlowRow
		if err := rows.Scan(&r.Name, &r.YAML, &r.Hash, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteFlow(ctx context.Context, name string) error {
	var refs []string
	prows, err := s.pool.Query(ctx, `SELECT name FROM projects WHERE default_flow=$1 ORDER BY name`, name)
	if err != nil {
		return err
	}
	defer prows.Close()
	for prows.Next() {
		var n string
		if err := prows.Scan(&n); err != nil {
			return err
		}
		refs = append(refs, "project "+n)
	}
	if err := prows.Err(); err != nil {
		return err
	}
	rrows, err := s.pool.Query(ctx, `
		SELECT run_id FROM runs
		WHERE flow_name=$1 AND state IN ('pending','provisioning','running','waiting_on_human')
		ORDER BY run_id`, name)
	if err != nil {
		return err
	}
	defer rrows.Close()
	for rrows.Next() {
		var r string
		if err := rrows.Scan(&r); err != nil {
			return err
		}
		refs = append(refs, "run "+r)
	}
	if err := rrows.Err(); err != nil {
		return err
	}
	if len(refs) > 0 {
		return fmt.Errorf("flow %s is referenced by %s: %w", name, strings.Join(refs, ", "), confstore.ErrReferenced)
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM flows WHERE name=$1`, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("flow %s: %w", name, confstore.ErrNotFound)
	}
	return nil // versions stay: immutable forever (§7)
}

func (s *Store) GetFlowVersion(ctx context.Context, hash string) (confstore.FlowVersion, error) {
	var v confstore.FlowVersion
	err := s.pool.QueryRow(ctx,
		`SELECT hash, name, yaml, created_at FROM flow_versions WHERE hash=$1`, hash).
		Scan(&v.Hash, &v.Name, &v.YAML, &v.CreatedAt)
	if err == pgx.ErrNoRows {
		return v, fmt.Errorf("flow version %s: %w", hash, confstore.ErrNotFound)
	}
	return v, err
}

func (s *Store) ListFlowVersions(ctx context.Context, name string) ([]confstore.FlowVersion, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT hash, name, yaml, created_at FROM flow_versions WHERE name=$1 ORDER BY created_at DESC`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confstore.FlowVersion
	for rows.Next() {
		var v confstore.FlowVersion
		if err := rows.Scan(&v.Hash, &v.Name, &v.YAML, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// --- settings ---------------------------------------------------------------

func (s *Store) GetSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = string(v)
	}
	return out, rows.Err()
}

func (s *Store) PutSetting(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO settings (key, value) VALUES ($1,$2)
		ON CONFLICT (key) DO UPDATE SET value=$2`, key, []byte(value))
	return err
}

var _ confstore.DB = (*Store)(nil)
