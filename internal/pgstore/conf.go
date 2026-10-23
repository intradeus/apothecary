package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"apothecary/internal/builtin"
	"apothecary/internal/confstore"
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
		WHERE forge_id=$1 OR provisioner_id=$1 OR harnesses ? $1
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
	harnessIDs := p.HarnessIDs
	if harnessIDs == nil {
		harnessIDs = []string{}
	}
	harnesses, err := json.Marshal(harnessIDs)
	if err != nil {
		return err
	}
	extra := p.Repos
	if extra == nil {
		extra = []confstore.ExtraRepo{}
	}
	repos, err := json.Marshal(extra)
	if err != nil {
		return err
	}
	// The SELECT gate is the atomic replacement for the 0004 FK, made
	// builtin-aware: built-in flows are compiled into the binary, never rows,
	// so a plain FK rejected them. The flow must be THIS project's own or a
	// built-in template — another project's flow of the same name is a
	// different document. When the gate yields no row (the flow vanished
	// between the service-layer validation and this write, and isn't a
	// built-in) nothing is inserted, no conflict fires, and RowsAffected
	// reads 0.
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO projects (id, name, forge_id, provisioner_id,
		                      repo, commands, workspace, default_flow, created_at, updated_at, harnesses, repos)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$13
		WHERE EXISTS (SELECT 1 FROM flows WHERE project_id=$1 AND name=$8) OR $8 = ANY($12)
		ON CONFLICT (id) DO UPDATE SET
			name=$2, forge_id=$3, provisioner_id=$4,
			repo=$5, commands=$6, workspace=$7, default_flow=$8, updated_at=$10, harnesses=$11, repos=$13`,
		p.ID, p.Name, p.ForgeID, p.ProvisionerID,
		repo, cmds, ws, p.DefaultFlow, p.CreatedAt, p.UpdatedAt, harnesses,
		builtin.FlowNames(), repos)
	if isUnique(err) {
		return fmt.Errorf("project %s: %w", p.Name, confstore.ErrConflict)
	}
	if isFK(err) {
		// FK backstop: a referenced integration (forge/provisioner) vanished
		// between the service-layer validation and this write.
		return fmt.Errorf("project %s references a missing flow or integration: %w", p.Name, confstore.ErrInvalid)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("project %s references a missing flow or integration: %w", p.Name, confstore.ErrInvalid)
	}
	return nil
}

func scanProject(row pgx.Row) (confstore.Project, error) {
	var p confstore.Project
	var repo, cmds, ws, harnesses, repos []byte
	err := row.Scan(&p.ID, &p.Name, &p.ForgeID, &p.ProvisionerID,
		&repo, &cmds, &ws, &p.DefaultFlow, &p.CreatedAt, &p.UpdatedAt, &harnesses, &repos)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(repo, &p.Repo); err != nil {
		return p, err
	}
	if len(repos) > 0 {
		if err := json.Unmarshal(repos, &p.Repos); err != nil {
			return p, err
		}
	}
	if err := json.Unmarshal(cmds, &p.Commands); err != nil {
		return p, err
	}
	if err := json.Unmarshal(ws, &p.Workspace); err != nil {
		return p, err
	}
	if len(harnesses) > 0 {
		if err := json.Unmarshal(harnesses, &p.HarnessIDs); err != nil {
			return p, err
		}
	}
	return p, nil
}

const projectCols = `id, name, forge_id, provisioner_id,
	repo, commands, workspace, default_flow, created_at, updated_at, harnesses, repos`

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

// --- tickets ----------------------------------------------------------------

const ticketCols = `id, project_id, title, goal, base_branch, ` +
	`source_id, source_kind, source_key, source_url, automation_id, created_at, updated_at`

func scanTicket(row pgx.Row) (confstore.Ticket, error) {
	var t confstore.Ticket
	err := row.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Goal, &t.BaseBranch,
		&t.SourceID, &t.SourceKind, &t.SourceKey, &t.SourceURL, &t.AutomationID,
		&t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (s *Store) UpsertTicket(ctx context.Context, t confstore.Ticket) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tickets (id, project_id, title, goal, base_branch,
			source_id, source_kind, source_key, source_url, automation_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO UPDATE SET
			project_id=$2, title=$3, goal=$4, base_branch=$5,
			source_id=$6, source_kind=$7, source_key=$8, source_url=$9, automation_id=$10, updated_at=$12`,
		t.ID, t.ProjectID, t.Title, t.Goal, t.BaseBranch,
		t.SourceID, t.SourceKind, t.SourceKey, t.SourceURL, t.AutomationID, t.CreatedAt, t.UpdatedAt)
	if isFK(err) {
		// FK backstop: the referenced project vanished between service-layer
		// validation and this write.
		return fmt.Errorf("ticket %s references a missing project: %w", t.ID, confstore.ErrInvalid)
	}
	return err
}

func (s *Store) GetTicket(ctx context.Context, id string) (confstore.Ticket, error) {
	t, err := scanTicket(s.pool.QueryRow(ctx,
		`SELECT `+ticketCols+` FROM tickets WHERE id=$1`, id))
	if err == pgx.ErrNoRows {
		return t, fmt.Errorf("ticket %s: %w", id, confstore.ErrNotFound)
	}
	return t, err
}

func (s *Store) ListTickets(ctx context.Context) ([]confstore.Ticket, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+ticketCols+` FROM tickets ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confstore.Ticket
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteTicket(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tickets WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("ticket %s: %w", id, confstore.ErrNotFound)
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
	// COALESCE keeps the stored layout when the save carries none
	// (DESIGN3 §2.2: a YAML-tab edit must not wipe node positions).
	var layout []byte
	if len(row.Layout) > 0 {
		layout = row.Layout
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO flows (project_id, name, yaml, hash, layout, updated_at) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (project_id, name) DO UPDATE SET yaml=$3, hash=$4,
			layout=COALESCE($5, flows.layout), updated_at=$6`,
		row.ProjectID, row.Name, row.YAML, row.Hash, layout, row.UpdatedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO flow_versions (project_id, hash, name, yaml, created_at) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (project_id, hash) DO NOTHING`,
		row.ProjectID, row.Hash, row.Name, row.YAML, row.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) GetFlow(ctx context.Context, projectID, name string) (confstore.FlowRow, error) {
	var r confstore.FlowRow
	var layout []byte
	err := s.pool.QueryRow(ctx,
		`SELECT project_id, name, yaml, hash, layout, updated_at FROM flows WHERE project_id=$1 AND name=$2`,
		projectID, name).
		Scan(&r.ProjectID, &r.Name, &r.YAML, &r.Hash, &layout, &r.UpdatedAt)
	r.Layout = layout
	if err == pgx.ErrNoRows {
		return r, fmt.Errorf("flow %s: %w", name, confstore.ErrNotFound)
	}
	return r, err
}

// ListFlows returns one project's flows, or every project's when projectID is
// empty — what the cross-project overview lists, and where copying starts.
func (s *Store) ListFlows(ctx context.Context, projectID string) ([]confstore.FlowRow, error) {
	const q = `SELECT project_id, name, yaml, hash, layout, updated_at FROM flows`
	var rows pgx.Rows
	var err error
	if projectID == "" {
		rows, err = s.pool.Query(ctx, q+` ORDER BY project_id, name`)
	} else {
		rows, err = s.pool.Query(ctx, q+` WHERE project_id=$1 ORDER BY name`, projectID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confstore.FlowRow
	for rows.Next() {
		var r confstore.FlowRow
		var layout []byte
		if err := rows.Scan(&r.ProjectID, &r.Name, &r.YAML, &r.Hash, &layout, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Layout = layout
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteFlow refuses while THIS project still points at the flow or has a
// live run pinned to it. Another project's flow of the same name is a
// different document and never blocks.
func (s *Store) DeleteFlow(ctx context.Context, projectID, name string) error {
	var refs []string
	prows, err := s.pool.Query(ctx,
		`SELECT name FROM projects WHERE id=$1 AND default_flow=$2 ORDER BY name`, projectID, name)
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
		WHERE project_id=$1 AND flow_name=$2
		  AND state IN ('pending','provisioning','running','waiting_on_human','in_review')
		ORDER BY run_id`, projectID, name)
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
	tag, err := s.pool.Exec(ctx, `DELETE FROM flows WHERE project_id=$1 AND name=$2`, projectID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("flow %s: %w", name, confstore.ErrNotFound)
	}
	return nil // versions stay: immutable forever (§7)
}

// --- flow bindings ----------------------------------------------------------

func (s *Store) PutFlowBinding(ctx context.Context, b confstore.FlowBinding) error {
	params, err := json.Marshal(b.Params)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO flow_bindings (project_id, flow_name, params, updated_at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (project_id, flow_name) DO UPDATE SET params=$3, updated_at=$4`,
		b.ProjectID, b.FlowName, params, b.UpdatedAt)
	return err
}

func (s *Store) GetFlowBinding(ctx context.Context, projectID, flowName string) (confstore.FlowBinding, error) {
	var b confstore.FlowBinding
	var params []byte
	err := s.pool.QueryRow(ctx,
		`SELECT project_id, flow_name, params, updated_at FROM flow_bindings WHERE project_id=$1 AND flow_name=$2`,
		projectID, flowName).
		Scan(&b.ProjectID, &b.FlowName, &params, &b.UpdatedAt)
	if err == pgx.ErrNoRows {
		return b, fmt.Errorf("flow binding %s: %w", flowName, confstore.ErrNotFound)
	}
	if err != nil {
		return b, err
	}
	err = json.Unmarshal(params, &b.Params)
	return b, err
}

func (s *Store) ListFlowBindings(ctx context.Context, projectID string) ([]confstore.FlowBinding, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT project_id, flow_name, params, updated_at FROM flow_bindings WHERE project_id=$1 ORDER BY flow_name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confstore.FlowBinding
	for rows.Next() {
		var b confstore.FlowBinding
		var params []byte
		if err := rows.Scan(&b.ProjectID, &b.FlowName, &params, &b.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(params, &b.Params); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) DeleteFlowBinding(ctx context.Context, projectID, flowName string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM flow_bindings WHERE project_id=$1 AND flow_name=$2`, projectID, flowName)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("flow binding %s: %w", flowName, confstore.ErrNotFound)
	}
	return nil
}

// --- automations ------------------------------------------------------------

const automationCols = `id, project_id, name, kind, source_id, query, schedule, ticket_id, flow, phase, params, ` +
	`enabled, last_polled_at, last_fired_at, last_error, created_at, updated_at`

func scanAutomation(row pgx.Row) (confstore.Automation, error) {
	var a confstore.Automation
	var params []byte
	err := row.Scan(&a.ID, &a.ProjectID, &a.Name, &a.Kind, &a.SourceID, &a.Query, &a.Schedule, &a.TicketID,
		&a.Flow, &a.Phase, &params, &a.Enabled, &a.LastPolledAt, &a.LastFiredAt, &a.LastError, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return a, err
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &a.Params); err != nil {
			return a, err
		}
	}
	if len(a.Params) == 0 {
		a.Params = nil
	}
	return a, nil
}

func (s *Store) UpsertAutomation(ctx context.Context, a confstore.Automation) error {
	params, err := json.Marshal(a.Params)
	if err != nil {
		return err
	}
	if a.Params == nil {
		params = []byte("{}")
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO automations (id, project_id, name, kind, source_id, query, schedule, ticket_id, flow, phase, params,
			enabled, last_polled_at, last_fired_at, last_error, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (id) DO UPDATE SET
			name=$3, kind=$4, source_id=$5, query=$6, schedule=$7, ticket_id=$8, flow=$9, phase=$10, params=$11,
			enabled=$12, last_polled_at=$13, last_fired_at=$14, last_error=$15, updated_at=$17`,
		a.ID, a.ProjectID, a.Name, a.Kind, a.SourceID, a.Query, a.Schedule, a.TicketID, a.Flow, a.Phase, params,
		a.Enabled, a.LastPolledAt, a.LastFiredAt, a.LastError, a.CreatedAt, a.UpdatedAt)
	if isFK(err) {
		return fmt.Errorf("automation %s references a missing project: %w", a.ID, confstore.ErrInvalid)
	}
	return err
}

func (s *Store) GetAutomation(ctx context.Context, id string) (confstore.Automation, error) {
	a, err := scanAutomation(s.pool.QueryRow(ctx, `SELECT `+automationCols+` FROM automations WHERE id=$1`, id))
	if err == pgx.ErrNoRows {
		return a, fmt.Errorf("automation %s: %w", id, confstore.ErrNotFound)
	}
	return a, err
}

func (s *Store) ListAutomations(ctx context.Context, projectID string) ([]confstore.Automation, error) {
	const q = `SELECT ` + automationCols + ` FROM automations`
	var rows pgx.Rows
	var err error
	if projectID == "" {
		rows, err = s.pool.Query(ctx, q+` ORDER BY created_at, id`)
	} else {
		rows, err = s.pool.Query(ctx, q+` WHERE project_id=$1 ORDER BY created_at, id`, projectID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confstore.Automation
	for rows.Next() {
		a, err := scanAutomation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAutomation(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM automations WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("automation %s: %w", id, confstore.ErrNotFound)
	}
	return nil
}

func (s *Store) GetFlowVersion(ctx context.Context, projectID, hash string) (confstore.FlowVersion, error) {
	var v confstore.FlowVersion
	err := s.pool.QueryRow(ctx,
		`SELECT project_id, hash, name, yaml, created_at FROM flow_versions WHERE project_id=$1 AND hash=$2`,
		projectID, hash).
		Scan(&v.ProjectID, &v.Hash, &v.Name, &v.YAML, &v.CreatedAt)
	if err == pgx.ErrNoRows {
		return v, fmt.Errorf("flow version %s: %w", hash, confstore.ErrNotFound)
	}
	return v, err
}

func (s *Store) ListFlowVersions(ctx context.Context, projectID, name string) ([]confstore.FlowVersion, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT project_id, hash, name, yaml, created_at FROM flow_versions
		 WHERE project_id=$1 AND name=$2 ORDER BY created_at DESC`, projectID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confstore.FlowVersion
	for rows.Next() {
		var v confstore.FlowVersion
		if err := rows.Scan(&v.ProjectID, &v.Hash, &v.Name, &v.YAML, &v.CreatedAt); err != nil {
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

// --- agents (DESIGN3 §1) ----------------------------------------------------

const agentCols = `project_id, name, description, read_only, allow_bash, prompt, inputs, outputs, hash, updated_at`

var _ confstore.DB = (*Store)(nil)
