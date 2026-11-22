package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

// Trackers: a ticketsource integration's type list (and the internal
// tracker's status list: a remote tracker's statuses are read from it), a
// ticket's tracker state, its comments and links. A remote issue's tickets
// row is a record of what Apothecary last saw of it, unique by (source_id,
// remote_id). Every write a run or a person makes to a ticket's state is ONE
// statement, so two writers at once are last-write-wins and never a torn
// row.

const statusCols = `id, integration_id, remote_id, name, position, is_done`

func scanStatus(row pgx.Row) (confstore.TrackerStatus, error) {
	var s confstore.TrackerStatus
	err := row.Scan(&s.ID, &s.IntegrationID, &s.RemoteID, &s.Name, &s.Position, &s.IsDone)
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
		return fmt.Errorf("%s of integration %s: a remote id is held twice: %w", what, integrationID, confstore.ErrConflict)
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
	for _, r := range rows {
		tag, err := tx.Exec(ctx, `
			INSERT INTO tracker_statuses (id, integration_id, remote_id, name, position, is_done)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (id) DO UPDATE SET remote_id=$3, name=$4, position=$5, is_done=$6
			WHERE tracker_statuses.integration_id=$2`,
			r.ID, integrationID, r.RemoteID, r.Name, r.Position, r.IsDone)
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
	// A listed remote id takes the new name and keeps every user-set column.
	out, err := scanStatus(s.pool.QueryRow(ctx, `
		INSERT INTO tracker_statuses (id, integration_id, remote_id, name, position, is_done)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (integration_id, remote_id) DO UPDATE SET name=EXCLUDED.name
		RETURNING `+statusCols,
		st.ID, st.IntegrationID, st.RemoteID, st.Name, st.Position, st.IsDone))
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

func (s *Store) GetTicketByRemote(ctx context.Context, sourceID, remoteID string) (confstore.Ticket, error) {
	t, err := scanTicket(s.pool.QueryRow(ctx, `SELECT `+ticketCols+` FROM tickets t
		WHERE t.source_id=$1 AND t.remote_id=$2`, sourceID, remoteID))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, fmt.Errorf("ticket %s/%s: %w", sourceID, remoteID, confstore.ErrNotFound)
	}
	return t, err
}

func (s *Store) GetTicketBySourceKey(ctx context.Context, sourceID, key string) (confstore.Ticket, error) {
	// A placeholder remote id (the live-tickets migration's dup:/legacy:)
	// stands for no issue: a real record of the key wins over one.
	t, err := scanTicket(s.pool.QueryRow(ctx, `SELECT `+ticketCols+` FROM tickets t
		WHERE t.source_id=$1 AND t.source_key=$2 AND $2 <> ''
		ORDER BY (t.remote_id LIKE 'dup:%' OR t.remote_id LIKE 'legacy:%'), t.created_at DESC, t.id DESC
		LIMIT 1`, sourceID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, fmt.Errorf("ticket %s/%s: %w", sourceID, key, confstore.ErrNotFound)
	}
	return t, err
}

func (s *Store) ListTicketsByRemote(ctx context.Context, sourceID string, remoteIDs []string) ([]confstore.Ticket, error) {
	if len(remoteIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+ticketCols+` FROM tickets t
		WHERE t.source_id=$1 AND t.remote_id = ANY($2::text[])`, sourceID, remoteIDs)
	return collect(rows, err, scanTicket)
}

// likeEscape makes s a literal inside an ILIKE pattern.
var likeEscape = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (s *Store) ListInternalTickets(ctx context.Context, sourceID string, q confstore.InternalTicketQuery) ([]confstore.Ticket, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	var text string
	if q.Text != "" {
		text = "%" + likeEscape.Replace(q.Text) + "%"
	}
	var afterAt *time.Time
	var afterID string
	if q.After != nil {
		afterAt, afterID = &q.After.CreatedAt, q.After.ID
	}
	// Newest first; the cursor is the last row's (created_at, id), so a page
	// stays put while tickets are added above it. A ticket with no status is
	// open.
	rows, err := s.pool.Query(ctx, `SELECT `+ticketCols+` FROM tickets t
		LEFT JOIN tracker_statuses s ON s.id = t.status_id
		WHERE t.source_id=$1
		  AND ($2 = '' OR t.title ILIKE $2 OR t.goal ILIKE $2 OR t.id ILIKE $2)
		  AND CASE $3 WHEN 'open' THEN NOT COALESCE(s.is_done, false)
		              WHEN 'done' THEN COALESCE(s.is_done, false)
		              ELSE true END
		  AND ($4::timestamptz IS NULL OR (t.created_at, t.id) < ($4, $5))
		ORDER BY t.created_at DESC, t.id DESC
		LIMIT $6`, sourceID, text, string(q.Done), afterAt, afterID, limit)
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

func (s *Store) SetTicketLabels(ctx context.Context, id string, labels []string, union bool, at time.Time, c confstore.Change) ([]string, error) {
	if labels == nil {
		labels = []string{}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var out []string
	err = tx.QueryRow(ctx, `
		UPDATE tickets SET updated_at=$4, labels = CASE WHEN $3 THEN
				ARRAY(SELECT l FROM unnest(labels || $2::text[]) WITH ORDINALITY AS u(l, n) GROUP BY l ORDER BY min(n))
			ELSE $2::text[] END
		 WHERE id=$1 RETURNING labels`, id, labels, union, at).Scan(&out)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("ticket %s: %w", id, confstore.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	if err := appendTicketVersion(ctx, tx, id, c, at); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		out = nil
	}
	return out, nil
}

func (s *Store) SetTicketSeen(ctx context.Context, id string, seen confstore.TicketSeen, typeID string, changeType trackerport.ChangeType, at time.Time, c confstore.Change) error {
	labels := seen.Labels
	if labels == nil {
		labels = []string{}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE tickets SET title=$2, goal=$3, source_key=$4, source_url=$5, source_type=$6,
			type_id=NULLIF($7,''), change_type=$8, labels=$9, updated_at=$10
		 WHERE id=$1`,
		id, seen.Title, seen.Goal, seen.Key, seen.URL, seen.Type, typeID, changeType, labels, at)
	if err != nil {
		return ticketWriteErr(id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("ticket %s: %w", id, confstore.ErrNotFound)
	}
	if err := appendTicketVersion(ctx, tx, id, c, at); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- comments and links ------------------------------------------------------

const commentCols = `id, ticket_id, author, body_md, created_at, updated_at, origin, run_id, stage_id`

func scanComment(row pgx.Row) (confstore.TicketComment, error) {
	var c confstore.TicketComment
	err := row.Scan(&c.ID, &c.TicketID, &c.Author, &c.Body, &c.CreatedAt, &c.UpdatedAt, &c.Origin, &c.RunID, &c.StageID)
	return c, err
}

func (s *Store) ListTicketComments(ctx context.Context, ticketID string) ([]confstore.TicketComment, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+commentCols+` FROM ticket_comments WHERE ticket_id=$1 ORDER BY created_at, id`, ticketID)
	return collect(rows, err, scanComment)
}

func (s *Store) InsertTicketComment(ctx context.Context, c confstore.TicketComment) (bool, error) {
	// Any unique key already held — (run, stage), the id — makes this a
	// no-op: the comment is there.
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO ticket_comments (`+commentCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT DO NOTHING`,
		c.ID, c.TicketID, c.Author, c.Body, c.CreatedAt, c.UpdatedAt, c.Origin, c.RunID, c.StageID)
	if isFK(err) {
		return false, fmt.Errorf("ticket %s: %w", c.TicketID, confstore.ErrNotFound)
	}
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
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
