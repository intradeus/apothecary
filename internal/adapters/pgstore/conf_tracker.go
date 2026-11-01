package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

// Trackers (migration 0021, docs/ticket-sync-plan.md): a ticketsource
// integration's status and type lists, a ticket's tracker state, its
// comments and links. Every write a run or a person makes to a ticket's state
// is ONE statement, so two writers at once are last-write-wins and never a
// torn row.

const statusCols = `id, integration_id, remote_id, name, remote_category, position, is_done, COALESCE(flow_step, '')`

func scanStatus(row pgx.Row) (confstore.TrackerStatus, error) {
	var s confstore.TrackerStatus
	err := row.Scan(&s.ID, &s.IntegrationID, &s.RemoteID, &s.Name, &s.RemoteCategory, &s.Position, &s.IsDone, &s.FlowStep)
	return s, err
}

const typeCols = `id, integration_id, remote_id, name, change_type, position`

func scanType(row pgx.Row) (confstore.TrackerType, error) {
	var t confstore.TrackerType
	err := row.Scan(&t.ID, &t.IntegrationID, &t.RemoteID, &t.Name, &t.ChangeType, &t.Position)
	return t, err
}

// collect scans every row of a query.
func collect[T any](rows pgx.Rows, err error, scan func(pgx.Row) (T, error)) ([]T, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ListTrackerStatuses(ctx context.Context, integrationID string) ([]confstore.TrackerStatus, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+statusCols+` FROM tracker_statuses WHERE integration_id=$1 ORDER BY position, id`, integrationID)
	return collect(rows, err, scanStatus)
}

func (s *Store) ListTrackerTypes(ctx context.Context, integrationID string) ([]confstore.TrackerType, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+typeCols+` FROM tracker_types WHERE integration_id=$1 ORDER BY position, id`, integrationID)
	return collect(rows, err, scanType)
}

// listErr maps a list write's constraint failures.
func listErr(what, integrationID string, err error) error {
	switch {
	case err == nil:
		return nil
	case isUnique(err):
		return fmt.Errorf("%s of integration %s: a flow step or remote id is held twice: %w", what, integrationID, confstore.ErrConflict)
	case isFK(err):
		return fmt.Errorf("%s of integration %s: one being deleted is still used by a ticket: %w", what, integrationID, confstore.ErrReferenced)
	}
	return err
}

func (s *Store) integrationExists(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string) error {
	var one int
	err := q.QueryRow(ctx, `SELECT 1 FROM integrations WHERE id=$1`, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("integration %s: %w", id, confstore.ErrInvalid)
	}
	return err
}

func (s *Store) ReplaceTrackerStatuses(ctx context.Context, integrationID string, rows []confstore.TrackerStatus, reassign map[string]string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.integrationExists(ctx, tx, integrationID); err != nil {
		return err
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	for from, to := range reassign {
		if _, err := tx.Exec(ctx, `UPDATE tickets SET status_id=$2 WHERE status_id=$1
			AND EXISTS (SELECT 1 FROM tracker_statuses WHERE id=$1 AND integration_id=$3)
			AND $2 = ANY($4::text[])`, from, to, integrationID, ids); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM tracker_statuses WHERE integration_id=$1 AND NOT (id = ANY($2::text[]))`, integrationID, ids); err != nil {
		return listErr("statuses", integrationID, err)
	}
	// Steps are re-dealt below: clearing them first lets two statuses swap.
	if _, err := tx.Exec(ctx, `UPDATE tracker_statuses SET flow_step=NULL WHERE integration_id=$1`, integrationID); err != nil {
		return err
	}
	for _, r := range rows {
		tag, err := tx.Exec(ctx, `
			INSERT INTO tracker_statuses (id, integration_id, remote_id, name, remote_category, position, is_done, flow_step)
			VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''))
			ON CONFLICT (id) DO UPDATE SET remote_id=$3, name=$4, remote_category=$5, position=$6, is_done=$7, flow_step=NULLIF($8,'')
			WHERE tracker_statuses.integration_id=$2`,
			r.ID, integrationID, r.RemoteID, r.Name, r.RemoteCategory, r.Position, r.IsDone, string(r.FlowStep))
		if err != nil {
			return listErr("statuses", integrationID, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("status %s is another integration's: %w", r.ID, confstore.ErrConflict)
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ReplaceTrackerTypes(ctx context.Context, integrationID string, rows []confstore.TrackerType, reassign map[string]string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.integrationExists(ctx, tx, integrationID); err != nil {
		return err
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	for from, to := range reassign {
		if _, err := tx.Exec(ctx, `UPDATE tickets SET type_id=$2 WHERE type_id=$1
			AND EXISTS (SELECT 1 FROM tracker_types WHERE id=$1 AND integration_id=$3)
			AND $2 = ANY($4::text[])`, from, to, integrationID, ids); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM tracker_types WHERE integration_id=$1 AND NOT (id = ANY($2::text[]))`, integrationID, ids); err != nil {
		return listErr("types", integrationID, err)
	}
	for _, r := range rows {
		tag, err := tx.Exec(ctx, `
			INSERT INTO tracker_types (id, integration_id, remote_id, name, change_type, position)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (id) DO UPDATE SET remote_id=$3, name=$4, change_type=$5, position=$6
			WHERE tracker_types.integration_id=$2`,
			r.ID, integrationID, r.RemoteID, r.Name, r.ChangeType, r.Position)
		if err != nil {
			return listErr("types", integrationID, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("type %s is another integration's: %w", r.ID, confstore.ErrConflict)
		}
	}
	// The change type is derived from the type.
	if _, err := tx.Exec(ctx, `
		UPDATE tickets t SET change_type = y.change_type
		  FROM tracker_types y
		 WHERE t.type_id = y.id AND y.integration_id = $1 AND t.change_type <> y.change_type`, integrationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) EnsureTrackerStatus(ctx context.Context, st confstore.TrackerStatus) (confstore.TrackerStatus, error) {
	ensure := func(step string) (confstore.TrackerStatus, error) {
		// The step goes to the insert only while no status holds it; a
		// listed remote id keeps every user-set column.
		return scanStatus(s.pool.QueryRow(ctx, `
			INSERT INTO tracker_statuses (id, integration_id, remote_id, name, remote_category, position, is_done, flow_step)
			VALUES ($1,$2,$3,$4,$5,$6,$7,
				CASE WHEN $8 = '' OR EXISTS (SELECT 1 FROM tracker_statuses WHERE integration_id=$2 AND flow_step=$8)
				     THEN NULL ELSE $8 END)
			ON CONFLICT (integration_id, remote_id) DO UPDATE SET name=EXCLUDED.name,
				remote_category = CASE WHEN EXCLUDED.remote_category <> '' THEN EXCLUDED.remote_category
				                       ELSE tracker_statuses.remote_category END
			RETURNING `+statusCols,
			st.ID, st.IntegrationID, st.RemoteID, st.Name, st.RemoteCategory, st.Position, st.IsDone, step))
	}
	out, err := ensure(string(st.FlowStep))
	if isUnique(err) && st.FlowStep != "" {
		out, err = ensure("") // another writer took the step meanwhile
	}
	switch {
	case isFK(err):
		return out, fmt.Errorf("status %s references a missing integration: %w", st.Name, confstore.ErrInvalid)
	case isUnique(err):
		return out, fmt.Errorf("status %s: %w", st.ID, confstore.ErrConflict)
	}
	return out, err
}

func (s *Store) EnsureTrackerType(ctx context.Context, t confstore.TrackerType) (confstore.TrackerType, error) {
	out, err := scanType(s.pool.QueryRow(ctx, `
		INSERT INTO tracker_types (id, integration_id, remote_id, name, change_type, position)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (integration_id, remote_id) DO UPDATE SET name=EXCLUDED.name
		RETURNING `+typeCols,
		t.ID, t.IntegrationID, t.RemoteID, t.Name, t.ChangeType, t.Position))
	switch {
	case isFK(err):
		return out, fmt.Errorf("type %s references a missing integration: %w", t.Name, confstore.ErrInvalid)
	case isUnique(err):
		return out, fmt.Errorf("type %s: %w", t.ID, confstore.ErrConflict)
	}
	return out, err
}

// --- a ticket's tracker state ------------------------------------------------

func (s *Store) GetTicketBySource(ctx context.Context, userID, sourceID, key string) (confstore.Ticket, error) {
	t, err := scanTicket(s.pool.QueryRow(ctx, `SELECT `+ticketCols+` FROM tickets
		WHERE user_id=$1 AND source_id=$2 AND source_id <> '' AND lower(source_key)=lower($3)`, userID, sourceID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, fmt.Errorf("ticket %s: %w", key, confstore.ErrNotFound)
	}
	return t, err
}

func (s *Store) GetTicketByProjectKey(ctx context.Context, projectID, key string) (confstore.Ticket, error) {
	t, err := scanTicket(s.pool.QueryRow(ctx, `SELECT `+ticketCols+` FROM tickets
		WHERE project_id=$1 AND source_key <> '' AND lower(source_key)=lower($2)
		ORDER BY created_at, id LIMIT 1`, projectID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, fmt.Errorf("ticket %s: %w", key, confstore.ErrNotFound)
	}
	return t, err
}

func (s *Store) ListTicketsBySource(ctx context.Context, sourceID string, syncable bool) ([]confstore.Ticket, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+ticketCols+` FROM tickets t
		WHERE source_id=$1 AND source_id <> ''
		  AND (NOT $2 OR (sync_state <> 'gone' AND NOT EXISTS (
				SELECT 1 FROM tracker_statuses s WHERE s.id = t.status_id AND s.is_done)))
		ORDER BY created_at, id`, sourceID, syncable)
	return collect(rows, err, scanTicket)
}

// ticketExists tells a no-op write from a missing ticket.
func (s *Store) ticketExists(ctx context.Context, id string) error {
	var one int
	err := s.pool.QueryRow(ctx, `SELECT 1 FROM tickets WHERE id=$1`, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("ticket %s: %w", id, confstore.ErrNotFound)
	}
	return err
}

func (s *Store) SetTicketStatus(ctx context.Context, id, statusID, by string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE tickets SET status_id=$2, status_changed_at=$4, status_changed_by=$3, updated_at=$4
		 WHERE id=$1 AND status_id IS DISTINCT FROM $2`, id, statusID, by, at)
	if err != nil {
		return ticketWriteErr(id, err)
	}
	if tag.RowsAffected() == 0 {
		return s.ticketExists(ctx, id)
	}
	return nil
}

func (s *Store) SetTicketLabels(ctx context.Context, id string, labels []string, union bool, at time.Time) ([]string, error) {
	if labels == nil {
		labels = []string{}
	}
	var out []string
	err := s.pool.QueryRow(ctx, `
		UPDATE tickets SET updated_at=$4, labels = CASE WHEN $3 THEN
				ARRAY(SELECT l FROM unnest(labels || $2::text[]) WITH ORDINALITY AS u(l, n) GROUP BY l ORDER BY min(n))
			ELSE $2::text[] END
		 WHERE id=$1 RETURNING labels`, id, labels, union, at).Scan(&out)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("ticket %s: %w", id, confstore.ErrNotFound)
	}
	if len(out) == 0 {
		out = nil
	}
	return out, err
}

func (s *Store) MirrorTicket(ctx context.Context, id string, m confstore.TicketMirror, changeType trackerport.ChangeType, at time.Time) (bool, error) {
	labels := m.Labels
	if labels == nil {
		labels = []string{}
	}
	var updated *time.Time
	if !m.RemoteUpdated.IsZero() {
		updated = &m.RemoteUpdated
	}
	// A snapshot older than the one held is dropped: the guard is in the
	// WHERE, so two syncs racing cannot both win.
	tag, err := s.pool.Exec(ctx, `
		UPDATE tickets SET
			title=$2, goal=$3, source_key=$4, source_url=$5, source_type=$6, remote_id=$7,
			status_changed_at = CASE WHEN $8 <> '' AND status_id IS DISTINCT FROM $8 THEN $13 ELSE status_changed_at END,
			status_changed_by = CASE WHEN $8 <> '' AND status_id IS DISTINCT FROM $8 THEN 'sync' ELSE status_changed_by END,
			status_id = COALESCE(NULLIF($8,''), status_id),
			type_id = COALESCE(NULLIF($9,''), type_id),
			change_type=$10, labels=$11,
			remote_updated_at = COALESCE($12, remote_updated_at),
			synced_at=$13, sync_state='ok', sync_error='', updated_at=$13
		 WHERE id=$1 AND ($12::timestamptz IS NULL OR remote_updated_at IS NULL OR remote_updated_at <= $12)`,
		id, m.Title, m.Goal, m.SourceKey, m.SourceURL, m.SourceType, m.RemoteID,
		m.StatusID, m.TypeID, changeType, labels, updated, at)
	if err != nil {
		return false, ticketWriteErr(id, err)
	}
	if tag.RowsAffected() == 0 {
		return false, s.ticketExists(ctx, id)
	}
	return true, nil
}

func (s *Store) SetTicketSyncState(ctx context.Context, id, state, msg string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE tickets SET sync_state=$2, sync_error=$3 WHERE id=$1`, id, state, msg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("ticket %s: %w", id, confstore.ErrNotFound)
	}
	return nil
}

func (s *Store) DeleteTicketSource(ctx context.Context, integrationID string, moves []confstore.TicketUnlink, at time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ids := make([]string, len(moves))
	for i, mv := range moves {
		ids[i] = mv.TicketID
		if _, err := tx.Exec(ctx, `
			UPDATE tickets SET
				status_changed_at = CASE WHEN status_id IS DISTINCT FROM NULLIF($3,'') THEN $6 ELSE status_changed_at END,
				status_changed_by = CASE WHEN status_id IS DISTINCT FROM NULLIF($3,'') THEN 'user' ELSE status_changed_by END,
				status_id=NULLIF($3,''), type_id=NULLIF($4,''), change_type=$5,
				source_id='', source_kind='', source_key='', source_url='', source_type='',
				remote_id='', remote_updated_at=NULL, synced_at=NULL, sync_state='', sync_error='',
				updated_at=$6
			 WHERE id=$1 AND source_id=$2`,
			mv.TicketID, integrationID, mv.StatusID, mv.TypeID, mv.ChangeType, at); err != nil {
			return ticketWriteErr(mv.TicketID, err)
		}
	}
	var late string
	err = tx.QueryRow(ctx, `SELECT id FROM tickets WHERE source_id=$1 AND source_id <> '' AND NOT (id = ANY($2::text[])) LIMIT 1`,
		integrationID, ids).Scan(&late)
	if err == nil {
		return fmt.Errorf("integration %s: ticket %s was linked meanwhile: %w", integrationID, late, confstore.ErrConflict)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err := s.deleteIntegration(ctx, tx, integrationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- comments and links ------------------------------------------------------

const commentCols = `id, ticket_id, remote_id, author, body_md, created_at, updated_at, origin, run_id, stage_id`

func scanComment(row pgx.Row) (confstore.TicketComment, error) {
	var c confstore.TicketComment
	err := row.Scan(&c.ID, &c.TicketID, &c.RemoteID, &c.Author, &c.Body, &c.CreatedAt, &c.UpdatedAt, &c.Origin, &c.RunID, &c.StageID)
	return c, err
}

func (s *Store) ListTicketComments(ctx context.Context, ticketID string) ([]confstore.TicketComment, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+commentCols+` FROM ticket_comments WHERE ticket_id=$1 ORDER BY created_at, id`, ticketID)
	return collect(rows, err, scanComment)
}

func (s *Store) InsertTicketComment(ctx context.Context, c confstore.TicketComment) (bool, error) {
	// Any unique key already held — (run, stage), the remote id, the id —
	// makes this a no-op: the comment is there.
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO ticket_comments (`+commentCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT DO NOTHING`,
		c.ID, c.TicketID, c.RemoteID, c.Author, c.Body, c.CreatedAt, c.UpdatedAt, c.Origin, c.RunID, c.StageID)
	if isFK(err) {
		return false, fmt.Errorf("ticket %s: %w", c.TicketID, confstore.ErrNotFound)
	}
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) ReconcileRemoteComments(ctx context.Context, ticketID string, rows []confstore.TicketComment) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	remote := make([]string, 0, len(rows))
	for _, c := range rows {
		remote = append(remote, c.RemoteID)
		_, err := tx.Exec(ctx, `
			INSERT INTO ticket_comments (`+commentCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,'remote','','')
			ON CONFLICT (ticket_id, remote_id) WHERE remote_id <> '' DO UPDATE SET
				author=EXCLUDED.author, body_md=EXCLUDED.body_md, updated_at=EXCLUDED.updated_at`,
			c.ID, ticketID, c.RemoteID, c.Author, c.Body, c.CreatedAt, c.UpdatedAt)
		if isFK(err) {
			return fmt.Errorf("ticket %s: %w", ticketID, confstore.ErrNotFound)
		}
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ticket_comments WHERE ticket_id=$1 AND origin='remote' AND NOT (remote_id = ANY($2::text[]))`,
		ticketID, remote); err != nil {
		return err
	}
	if len(rows) == 0 {
		var one int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM tickets WHERE id=$1`, ticketID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("ticket %s: %w", ticketID, confstore.ErrNotFound)
		} else if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) UpsertTicketLink(ctx context.Context, l confstore.TicketLink) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ticket_links (ticket_id, url, title) VALUES ($1,$2,$3)
		ON CONFLICT (ticket_id, url) DO UPDATE SET title=$3`, l.TicketID, l.URL, l.Title)
	if isFK(err) {
		return fmt.Errorf("ticket %s: %w", l.TicketID, confstore.ErrNotFound)
	}
	return err
}

func (s *Store) ListTicketLinks(ctx context.Context, ticketID string) ([]confstore.TicketLink, error) {
	rows, err := s.pool.Query(ctx, `SELECT ticket_id, url, title FROM ticket_links WHERE ticket_id=$1 ORDER BY url`, ticketID)
	return collect(rows, err, func(r pgx.Row) (confstore.TicketLink, error) {
		var l confstore.TicketLink
		err := r.Scan(&l.TicketID, &l.URL, &l.Title)
		return l, err
	})
}
