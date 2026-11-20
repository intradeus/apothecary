package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"apothecary/internal/account/providers"
	"apothecary/internal/flow/builtin"
	"apothecary/internal/project/confstore"
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
	// The owner is set at insert and never by an update: an edit cannot
	// hand a credential to someone else.
	_, err = s.pool.Exec(ctx, `
		INSERT INTO integrations (id, port, kind, name, config, secrets, created_at, updated_at, user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET
			name=$4, config=$5, secrets=$6, updated_at=$8`,
		r.ID, r.Port, r.Kind, r.Name, cfg, sealed, r.CreatedAt, r.UpdatedAt, r.UserID)
	if isUnique(err) {
		return fmt.Errorf("integration %s/%s: %w", r.Port, r.Name, confstore.ErrConflict)
	}
	return err
}

const integrationCols = `id, port, kind, name, config, secrets, created_at, updated_at, user_id`

func (s *Store) GetIntegration(ctx context.Context, id string) (confstore.StoredIntegration, error) {
	var r confstore.StoredIntegration
	var cfg []byte
	err := s.pool.QueryRow(ctx, `SELECT `+integrationCols+` FROM integrations WHERE id=$1`, id).
		Scan(&r.ID, &r.Port, &r.Kind, &r.Name, &cfg, &r.Sealed, &r.CreatedAt, &r.UpdatedAt, &r.UserID)
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

// ownerClause scopes a list to one owner; "" lists every row (the daemon's
// own passes). $n is the placeholder number the user id takes.
func ownerClause(userID string, n int) (string, []any) {
	if userID == "" {
		return "", nil
	}
	return fmt.Sprintf(` WHERE user_id=$%d`, n), []any{userID}
}

func (s *Store) ListIntegrations(ctx context.Context, userID string) ([]confstore.StoredIntegration, error) {
	where, args := ownerClause(userID, 1)
	rows, err := s.pool.Query(ctx, `SELECT `+integrationCols+` FROM integrations`+where+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confstore.StoredIntegration
	for rows.Next() {
		var r confstore.StoredIntegration
		var cfg []byte
		if err := rows.Scan(&r.ID, &r.Port, &r.Kind, &r.Name, &cfg, &r.Sealed, &r.CreatedAt, &r.UpdatedAt, &r.UserID); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(cfg, &r.Config); err != nil {
			return nil, fmt.Errorf("integration %s: config: %w", r.ID, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteIntegration refuses while a project uses the integration. A ticket
// source no workshop uses goes with its lists; its tickets stay (runs point
// at them, and tickets.source_id has no FK), losing only their type, in the
// same transaction.
func (s *Store) DeleteIntegration(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var refs []string
	rows, err := tx.Query(ctx, `
		SELECT name FROM projects
		WHERE repo_account_id=$1 OR provisioner_id=$1 OR ticket_source_id=$1 OR harnesses ? $1
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
	if _, err := tx.Exec(ctx, `
		UPDATE tickets SET type_id=NULL
		 WHERE type_id IN (SELECT y.id FROM tracker_types y WHERE y.integration_id=$1)`, id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM integrations WHERE id=$1`, id)
	if isFK(err) {
		// A project took it meanwhile, or a ticket still holds one of its
		// statuses (they cascade).
		return fmt.Errorf("integration %s is still referenced by a project or a ticket's status: %w", id, confstore.ErrReferenced)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("integration %s: %w", id, confstore.ErrNotFound)
	}
	return tx.Commit(ctx)
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
	// The owner (user_id) is set at insert and never by an update.
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO projects (id, name, repo_account_id, provisioner_id,
		                      repo, commands, workspace, default_flow, created_at, updated_at, harnesses, repos, user_id,
		                      ticket_source_id)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$13,$14,$15
		WHERE EXISTS (SELECT 1 FROM flows WHERE project_id=$1 AND name=$8) OR $8 = ANY($12)
		ON CONFLICT (id) DO UPDATE SET
			name=$2, repo_account_id=$3, provisioner_id=$4,
			repo=$5, commands=$6, workspace=$7, default_flow=$8, updated_at=$10, harnesses=$11, repos=$13,
			ticket_source_id=$15`,
		p.ID, p.Name, p.RepoAccountID, p.ProvisionerID,
		repo, cmds, ws, p.DefaultFlow, p.CreatedAt, p.UpdatedAt, harnesses,
		builtin.FlowNames(), repos, p.UserID, p.TicketSourceID)
	if isUnique(err) {
		return fmt.Errorf("project %s: %w", p.Name, confstore.ErrConflict)
	}
	if isFK(err) {
		// FK backstop: a referenced integration (repository host,
		// provisioner, ticket source) vanished between the service-layer
		// validation and this write.
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
	err := row.Scan(&p.ID, &p.Name, &p.RepoAccountID, &p.ProvisionerID,
		&repo, &cmds, &ws, &p.DefaultFlow, &p.CreatedAt, &p.UpdatedAt, &harnesses, &repos, &p.UserID, &p.TicketSourceID)
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

const projectCols = `id, name, repo_account_id, provisioner_id,
	repo, commands, workspace, default_flow, created_at, updated_at, harnesses, repos, user_id, ticket_source_id`

func (s *Store) GetProject(ctx context.Context, id string) (confstore.Project, error) {
	p, err := scanProject(s.pool.QueryRow(ctx,
		`SELECT `+projectCols+` FROM projects WHERE id=$1`, id))
	if err == pgx.ErrNoRows {
		return p, fmt.Errorf("project %s: %w", id, confstore.ErrNotFound)
	}
	return p, err
}

func (s *Store) GetProjectByName(ctx context.Context, userID, name string) (confstore.Project, error) {
	p, err := scanProject(s.pool.QueryRow(ctx,
		`SELECT `+projectCols+` FROM projects WHERE name=$1 AND ($2 = '' OR user_id=$2) ORDER BY created_at LIMIT 1`, name, userID))
	if err == pgx.ErrNoRows {
		return p, fmt.Errorf("project %s: %w", name, confstore.ErrNotFound)
	}
	return p, err
}

func (s *Store) ListProjects(ctx context.Context, userID string) ([]confstore.Project, error) {
	where, args := ownerClause(userID, 1)
	rows, err := s.pool.Query(ctx, `SELECT `+projectCols+` FROM projects`+where+` ORDER BY created_at`, args...)
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
		`SELECT run_id FROM runs WHERE project_id=$1 AND archived_at IS NULL ORDER BY run_id`, id)
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

// --- project secrets --------------------------------------------------------

func (s *Store) PutProjectSecret(ctx context.Context, projectID string, sec confstore.StoredProjectSecret) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO project_secrets (project_id, name, value, updated_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (project_id, name) DO UPDATE SET value=$3, updated_at=$4`,
		projectID, sec.Name, sec.Sealed, sec.UpdatedAt)
	if isFK(err) {
		return fmt.Errorf("secret %s references a missing project: %w", sec.Name, confstore.ErrInvalid)
	}
	return err
}

func (s *Store) ListProjectSecrets(ctx context.Context, projectID string) ([]confstore.StoredProjectSecret, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, value, updated_at FROM project_secrets
		WHERE project_id=$1 ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confstore.StoredProjectSecret
	for rows.Next() {
		var r confstore.StoredProjectSecret
		if err := rows.Scan(&r.Name, &r.Sealed, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteProjectSecret(ctx context.Context, projectID, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM project_secrets WHERE project_id=$1 AND name=$2`, projectID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("secret %s: %w", name, confstore.ErrNotFound)
	}
	return nil
}

// --- tickets ----------------------------------------------------------------

// ticketCols reads a tickets row aliased t (the lists join tracker_statuses).
const ticketCols = `t.id, t.title, t.goal, t.base_branch, ` +
	`t.source_id, t.source_kind, t.source_key, t.source_url, t.automation_id, t.created_at, t.updated_at, t.user_id, ` +
	`t.change_type, t.source_type, COALESCE(t.status_id, ''), COALESCE(t.type_id, ''), t.labels, t.remote_id, ` +
	`t.status_changed_at, t.status_changed_by`

func scanTicket(row pgx.Row) (confstore.Ticket, error) {
	var t confstore.Ticket
	err := row.Scan(&t.ID, &t.Title, &t.Goal, &t.BaseBranch,
		&t.SourceID, &t.SourceKind, &t.SourceKey, &t.SourceURL, &t.AutomationID,
		&t.CreatedAt, &t.UpdatedAt, &t.UserID, &t.ChangeType, &t.SourceType,
		&t.StatusID, &t.TypeID, &t.Labels, &t.RemoteID,
		&t.StatusChangedAt, &t.StatusChangedBy)
	if len(t.Labels) == 0 {
		t.Labels = nil
	}
	return t, err
}

// ticketWriteErr maps a ticket write's constraint failures: the unique
// (source_id, remote_id) is a second record of one issue, an FK a status or
// type that vanished between the service-layer validation and this write.
func ticketWriteErr(id string, err error) error {
	if isUnique(err) {
		return fmt.Errorf("ticket %s: that issue already has a record: %w", id, confstore.ErrConflict)
	}
	if isFK(err) {
		return fmt.Errorf("ticket %s references a missing status or type: %w", id, confstore.ErrInvalid)
	}
	return err
}

func (s *Store) UpsertTicket(ctx context.Context, t confstore.Ticket, c confstore.Change) error {
	labels := t.Labels
	if labels == nil {
		labels = []string{}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
		INSERT INTO tickets (id, title, goal, base_branch,
			source_id, source_kind, source_key, source_url, automation_id, created_at, updated_at, user_id,
			change_type, source_type, status_id, type_id, labels, remote_id,
			status_changed_at, status_changed_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,NULLIF($15,''),NULLIF($16,''),$17,$18,$19,$20)
		ON CONFLICT (id) DO UPDATE SET
			title=$2, goal=$3, base_branch=$4,
			source_id=$5, source_kind=$6, source_key=$7, source_url=$8, automation_id=$9, updated_at=$11, user_id=$12,
			change_type=$13, source_type=$14, status_id=NULLIF($15,''), type_id=NULLIF($16,''), labels=$17, remote_id=$18,
			status_changed_at=$19, status_changed_by=$20`,
		t.ID, t.Title, t.Goal, t.BaseBranch,
		t.SourceID, t.SourceKind, t.SourceKey, t.SourceURL, t.AutomationID, t.CreatedAt, t.UpdatedAt, t.UserID,
		t.ChangeType, t.SourceType, t.StatusID, t.TypeID, labels, t.RemoteID,
		t.StatusChangedAt, t.StatusChangedBy)
	if err != nil {
		return ticketWriteErr(t.ID, err)
	}
	if err := appendTicketVersion(ctx, tx, t.ID, c, t.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) GetTicket(ctx context.Context, id string) (confstore.Ticket, error) {
	t, err := scanTicket(s.pool.QueryRow(ctx,
		`SELECT `+ticketCols+` FROM tickets t WHERE t.id=$1`, id))
	if err == pgx.ErrNoRows {
		return t, fmt.Errorf("ticket %s: %w", id, confstore.ErrNotFound)
	}
	return t, err
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

// --- ticket versions --------------------------------------------------------

// appendTicketVersionSQL appends a version of ticket $1's stored content,
// made by ($2, $3, restored from $4) at $5, when it differs from the latest
// version. The row write before it holds the ticket's row lock, so two
// writers take their seqs one after the other; the primary key is the last
// line of defence.
const appendTicketVersionSQL = `
	INSERT INTO ticket_versions (ticket_id, seq, title, goal, base_branch, change_type, type_id, labels,
		author_kind, author_id, restored_from, created_at)
	SELECT t.id, COALESCE(l.seq, 0) + 1, t.title, t.goal, t.base_branch, t.change_type, COALESCE(t.type_id, ''), t.labels,
		$2, $3, NULLIF($4::integer, 0), $5
	  FROM tickets t
	  LEFT JOIN LATERAL (SELECT * FROM ticket_versions v WHERE v.ticket_id = t.id ORDER BY v.seq DESC LIMIT 1) l ON true
	 WHERE t.id = $1
	   AND (l.seq IS NULL OR (l.title, l.goal, l.base_branch, l.change_type, l.type_id, l.labels)
	        IS DISTINCT FROM (t.title, t.goal, t.base_branch, t.change_type, COALESCE(t.type_id, ''), t.labels))`

// appendTicketVersion runs appendTicketVersionSQL in tx, then keeps the
// newest confstore.MaxTicketVersions.
func appendTicketVersion(ctx context.Context, tx pgx.Tx, id string, c confstore.Change, at time.Time) error {
	tag, err := tx.Exec(ctx, appendTicketVersionSQL, id, string(c.Kind), c.AuthorID, c.RestoredFrom, at)
	var pg *pgconn.PgError
	switch {
	case isUnique(err):
		return fmt.Errorf("ticket %s changed meanwhile; reload and try again: %w", id, confstore.ErrConflict)
	case errors.As(err, &pg) && pg.Code == "23514":
		return fmt.Errorf("ticket %s: author kind %q: %w", id, c.Kind, confstore.ErrInvalid)
	case err != nil:
		return err
	case tag.RowsAffected() == 0:
		return nil
	}
	_, err = tx.Exec(ctx, `DELETE FROM ticket_versions WHERE ticket_id = $1
		AND seq <= (SELECT max(seq) FROM ticket_versions WHERE ticket_id = $1) - $2`, id, confstore.MaxTicketVersions)
	return err
}

const ticketVersionCols = `ticket_id, seq, title, goal, base_branch, change_type, type_id, labels, ` +
	`author_kind, author_id, COALESCE(restored_from, 0), created_at`

func scanTicketVersion(row pgx.Row) (confstore.TicketVersion, error) {
	var v confstore.TicketVersion
	err := row.Scan(&v.TicketID, &v.Seq, &v.Title, &v.Goal, &v.BaseBranch, &v.ChangeType, &v.TypeID, &v.Labels,
		&v.Kind, &v.AuthorID, &v.RestoredFrom, &v.CreatedAt)
	if len(v.Labels) == 0 {
		v.Labels = nil
	}
	return v, err
}

func (s *Store) ListTicketVersions(ctx context.Context, ticketID string) ([]confstore.TicketVersion, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+ticketVersionCols+` FROM ticket_versions
		WHERE ticket_id=$1 ORDER BY seq DESC`, ticketID)
	out, err := collect(rows, err, scanTicketVersion)
	if err != nil || len(out) > 0 {
		return out, err
	}
	return nil, s.ticketExists(ctx, ticketID)
}

func (s *Store) GetTicketVersion(ctx context.Context, ticketID string, seq int) (confstore.TicketVersion, error) {
	v, err := scanTicketVersion(s.pool.QueryRow(ctx, `SELECT `+ticketVersionCols+` FROM ticket_versions
		WHERE ticket_id=$1 AND seq=$2`, ticketID, seq))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, fmt.Errorf("ticket version %s/%d: %w", ticketID, seq, confstore.ErrNotFound)
	}
	return v, err
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
// live run pinned to it. A default a built-in template answers to points at
// nothing the delete takes away: that delete is the reset to the template.
// Another project's flow of the same name is a different document and never
// blocks.
func (s *Store) DeleteFlow(ctx context.Context, projectID, name string) error {
	var refs []string
	prows, err := s.pool.Query(ctx,
		`SELECT name FROM projects WHERE id=$1 AND default_flow=$2 AND NOT default_flow = ANY($3) ORDER BY name`,
		projectID, name, builtin.FlowNames())
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

const automationCols = `id, project_id, name, kind, query, schedule, ticket_id, flow, params, ` +
	`enabled, last_polled_at, last_fired_at, last_error, created_at, updated_at, repo`

func scanAutomation(row pgx.Row) (confstore.Automation, error) {
	var a confstore.Automation
	var params []byte
	err := row.Scan(&a.ID, &a.ProjectID, &a.Name, &a.Kind, &a.Query, &a.Schedule, &a.TicketID,
		&a.Flow, &params, &a.Enabled, &a.LastPolledAt, &a.LastFiredAt, &a.LastError, &a.CreatedAt, &a.UpdatedAt, &a.Repo)
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
		INSERT INTO automations (id, project_id, name, kind, query, schedule, ticket_id, flow, params,
			enabled, last_polled_at, last_fired_at, last_error, created_at, updated_at, repo)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (id) DO UPDATE SET
			name=$3, kind=$4, query=$5, schedule=$6, ticket_id=$7, flow=$8, params=$9,
			enabled=$10, last_polled_at=$11, last_fired_at=$12, last_error=$13, updated_at=$15, repo=$16`,
		a.ID, a.ProjectID, a.Name, a.Kind, a.Query, a.Schedule, a.TicketID, a.Flow, params,
		a.Enabled, a.LastPolledAt, a.LastFiredAt, a.LastError, a.CreatedAt, a.UpdatedAt, a.Repo)
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

func (s *Store) GetSettings(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM settings WHERE user_id=$1`, userID)
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

func (s *Store) PutSetting(ctx context.Context, userID, key, value string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO settings (user_id, key, value) VALUES ($1,$2,$3)
		ON CONFLICT (user_id, key) DO UPDATE SET value=$3`, userID, key, []byte(value))
	return err
}

// SettingsOwners lists every user with settings rows plus every owner of a
// run or an upload — the people a reaper pass reads settings for.
func (s *Store) SettingsOwners(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT user_id FROM settings
		UNION SELECT user_id FROM runs
		UNION SELECT user_id FROM attachments`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// --- agents (DESIGN3 §1) ----------------------------------------------------

const agentCols = `project_id, name, description, read_only, allow_bash, prompt, inputs, outputs, hash, updated_at`

var _ confstore.DB = (*Store)(nil)

// --- provider credentials ---------------------------------------------------

const credentialCols = `id, user_id, provider, kind, name, secrets, meta, created_at, updated_at`

func (s *Store) UpsertCredential(ctx context.Context, c confstore.ProviderCredential) error {
	meta, err := json.Marshal(c.Meta)
	if err != nil {
		return err
	}
	sealed := c.Sealed
	if sealed == nil {
		sealed = []byte{}
	}
	// Owner, provider and kind are set at insert and never by an update.
	_, err = s.pool.Exec(ctx, `
		INSERT INTO provider_credentials (`+credentialCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET name=$5, secrets=$6, meta=$7, updated_at=$9`,
		c.ID, c.UserID, c.Provider, string(c.Kind), c.Name, sealed, meta, c.CreatedAt, c.UpdatedAt)
	return err
}

func scanCredential(row interface{ Scan(...any) error }) (confstore.ProviderCredential, error) {
	var c confstore.ProviderCredential
	var kind string
	var meta []byte
	if err := row.Scan(&c.ID, &c.UserID, &c.Provider, &kind, &c.Name, &c.Sealed, &meta, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return c, err
	}
	c.Kind = providers.Kind(kind)
	if err := json.Unmarshal(meta, &c.Meta); err != nil {
		return c, fmt.Errorf("credential %s: meta: %w", c.ID, err)
	}
	return c, nil
}

func (s *Store) GetCredential(ctx context.Context, id string) (confstore.ProviderCredential, error) {
	c, err := scanCredential(s.pool.QueryRow(ctx, `SELECT `+credentialCols+` FROM provider_credentials WHERE id=$1`, id))
	if err == pgx.ErrNoRows {
		return c, fmt.Errorf("credential %s: %w", id, confstore.ErrNotFound)
	}
	return c, err
}

func (s *Store) ListCredentials(ctx context.Context, userID string) ([]confstore.ProviderCredential, error) {
	where, args := ownerClause(userID, 1)
	rows, err := s.pool.Query(ctx, `SELECT `+credentialCols+` FROM provider_credentials`+where+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confstore.ProviderCredential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCredential(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM provider_credentials WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("credential %s: %w", id, confstore.ErrNotFound)
	}
	return nil
}

// --- members ------------------------------------------------------------------

func (s *Store) ListVisibleProjects(ctx context.Context, userID string, projectIDs []string) ([]confstore.Project, error) {
	if projectIDs == nil {
		projectIDs = []string{}
	}
	rows, err := s.pool.Query(ctx, `SELECT `+projectCols+` FROM projects
		WHERE ($1 <> '' AND user_id=$1) OR id = ANY($2) ORDER BY created_at`, userID, projectIDs)
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

func (s *Store) PutMember(ctx context.Context, m confstore.Member, from string) error {
	// One statement each way: the role is compared and written atomically.
	var tag pgconn.CommandTag
	var err error
	if from == "" {
		tag, err = s.pool.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, created_at)
			VALUES ($1,$2,$3,$4) ON CONFLICT (project_id, user_id) DO NOTHING`,
			m.ProjectID, m.UserID, m.Role, m.CreatedAt)
	} else {
		tag, err = s.pool.Exec(ctx, `UPDATE project_members SET role=$3
			WHERE project_id=$1 AND user_id=$2 AND role=$4`,
			m.ProjectID, m.UserID, m.Role, from)
	}
	var pg *pgconn.PgError
	switch {
	case errors.As(err, &pg) && pg.Code == "23514":
		return fmt.Errorf("member role %q: %w", m.Role, confstore.ErrInvalid)
	case isFK(err) && strings.Contains(err.Error(), "project_id"):
		return fmt.Errorf("project %s: %w", m.ProjectID, confstore.ErrNotFound)
	case isFK(err):
		return fmt.Errorf("user %s: %w", m.UserID, confstore.ErrNotFound)
	case err != nil:
		return err
	case tag.RowsAffected() == 1:
		return nil
	}
	if _, err := s.GetProject(ctx, m.ProjectID); err != nil {
		return err
	}
	return memberChanged(m.UserID, from)
}

// memberChanged is a compare-and-swap that lost: the role is no longer from.
func memberChanged(userID, from string) error {
	if from == "" {
		from = "none"
	}
	return fmt.Errorf("member %s: the role is no longer %s; reload and try again: %w", userID, from, confstore.ErrConflict)
}

const memberCols = `project_id, user_id, role, created_at`

func (s *Store) GetMember(ctx context.Context, projectID, userID string) (confstore.Member, error) {
	var m confstore.Member
	err := s.pool.QueryRow(ctx, `SELECT `+memberCols+` FROM project_members WHERE project_id=$1 AND user_id=$2`,
		projectID, userID).Scan(&m.ProjectID, &m.UserID, &m.Role, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, fmt.Errorf("member %s: %w", userID, confstore.ErrNotFound)
	}
	return m, err
}

func (s *Store) ListMembers(ctx context.Context, projectID string) ([]confstore.Member, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+memberCols+` FROM project_members WHERE project_id=$1 ORDER BY created_at, user_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confstore.Member
	for rows.Next() {
		var m confstore.Member
		if err := rows.Scan(&m.ProjectID, &m.UserID, &m.Role, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) DeleteMember(ctx context.Context, projectID, userID, from string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2 AND role=$3`, projectID, userID, from)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	if _, err := s.GetMember(ctx, projectID, userID); err != nil {
		return err
	}
	return memberChanged(userID, from)
}

func (s *Store) ProjectAccess(ctx context.Context, projectID, userID string) (string, string, error) {
	var owner string
	var role *string
	err := s.pool.QueryRow(ctx, `SELECT p.user_id, m.role FROM projects p
		LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = $2
		WHERE p.id = $1`, projectID, userID).Scan(&owner, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", fmt.Errorf("project %s: %w", projectID, confstore.ErrNotFound)
	}
	if err != nil {
		return "", "", err
	}
	switch {
	case owner == userID:
		return owner, confstore.RoleOwner, nil
	case role != nil:
		return owner, *role, nil
	}
	return owner, "", nil
}

func (s *Store) ProjectsOf(ctx context.Context, userID string) ([]confstore.Reach, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, 'owner' FROM projects WHERE user_id = $1
		UNION ALL
		SELECT p.id, p.user_id, m.role FROM project_members m JOIN projects p ON p.id = m.project_id
		WHERE m.user_id = $1 AND p.user_id <> $1
		ORDER BY 1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confstore.Reach
	for rows.Next() {
		var r confstore.Reach
		if err := rows.Scan(&r.ProjectID, &r.OwnerID, &r.Role); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
