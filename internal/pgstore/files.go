package pgstore

// The filestore persistence (DESIGN-FILES §3.2): attachments (human uploads,
// structural ownership) and snapshots (engine record, a projection of
// snapshot_captured). One column-list const per table — the old artifacts
// SQL carried four copy-pasted SELECT lists.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"apothecary/internal/filestore"
)

// --- attachments ------------------------------------------------------------

const attachmentCols = `id, ticket_id, run_id, ping_id, filename, mime, size, sha256, created_at, user_id`

// nullStr maps Go's zero-string convention to the schema's NULLs: the CHECKs
// reason about NULL, not ”.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func scanAttachment(row pgx.Row) (filestore.Attachment, error) {
	var a filestore.Attachment
	var ticket, run, ping sql.NullString
	err := row.Scan(&a.ID, &ticket, &run, &ping, &a.Filename, &a.Mime, &a.Size, &a.SHA256, &a.CreatedAt, &a.UserID)
	if err != nil {
		return a, err
	}
	a.TicketID, a.RunID, a.PingID = ticket.String, run.String, ping.String
	return a, nil
}

func (s *Store) queryAttachments(ctx context.Context, q string, args ...any) ([]filestore.Attachment, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []filestore.Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) InsertAttachment(ctx context.Context, a filestore.Attachment) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO attachments (`+attachmentCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		a.ID, nullStr(a.TicketID), nullStr(a.RunID), nullStr(a.PingID),
		a.Filename, a.Mime, a.Size, a.SHA256, a.CreatedAt, a.UserID)
	return err
}

func (s *Store) GetAttachment(ctx context.Context, id string) (filestore.Attachment, error) {
	a, err := scanAttachment(s.pool.QueryRow(ctx,
		`SELECT `+attachmentCols+` FROM attachments WHERE id=$1`, id))
	if err == pgx.ErrNoRows {
		return a, fmt.Errorf("attachment %s: %w", id, filestore.ErrNotFound)
	}
	return a, err
}

// ClaimAttachments is one-shot by construction: the WHERE clause only matches
// still-unclaimed rows, so re-claims and claims of someone else's attachment
// are silent no-ops (the caller re-sends full id lists on update).
func (s *Store) ClaimAttachments(ctx context.Context, ids []string, ticketID, runID, pingID string) error {
	if (ticketID == "") == (runID == "") {
		return fmt.Errorf("claim: exactly one of ticket/run must own the attachment")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE attachments SET ticket_id=$1, run_id=$2, ping_id=$3
		WHERE id = ANY($4) AND ticket_id IS NULL AND run_id IS NULL`,
		nullStr(ticketID), nullStr(runID), nullStr(pingID), ids)
	return err
}

func (s *Store) AttachmentsForTicket(ctx context.Context, ticketID string) ([]filestore.Attachment, error) {
	return s.queryAttachments(ctx,
		`SELECT `+attachmentCols+` FROM attachments WHERE ticket_id=$1 ORDER BY created_at, id`, ticketID)
}

func (s *Store) AttachmentsForRun(ctx context.Context, runID string) ([]filestore.Attachment, error) {
	return s.queryAttachments(ctx,
		`SELECT `+attachmentCols+` FROM attachments WHERE run_id=$1 ORDER BY created_at, id`, runID)
}

func (s *Store) AttachmentsForPing(ctx context.Context, pingID string) ([]filestore.Attachment, error) {
	return s.queryAttachments(ctx,
		`SELECT `+attachmentCols+` FROM attachments WHERE ping_id=$1 ORDER BY created_at, id`, pingID)
}

func (s *Store) DeleteAttachmentsForRun(ctx context.Context, runID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM attachments WHERE run_id=$1`, runID)
	return err
}

func (s *Store) DeleteAttachmentsForTicket(ctx context.Context, ticketID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM attachments WHERE ticket_id=$1`, ticketID)
	return err
}

func (s *Store) UnclaimedAttachmentIDsBefore(ctx context.Context, userID string, cutoff time.Time) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM attachments
		WHERE ticket_id IS NULL AND run_id IS NULL AND created_at < $1 AND ($2 = '' OR user_id = $2)`, cutoff, userID)
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

func (s *Store) DeleteAttachments(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM attachments WHERE id = ANY($1)`, ids)
	return err
}

// --- snapshots --------------------------------------------------------------

const snapshotCols = `id, run_id, kind, stage_id, iteration, ping_id, name, path, filename, mime, size, sha256, truncated, created_at`

func scanSnapshot(row pgx.Row) (filestore.Snapshot, error) {
	var sn filestore.Snapshot
	var kind string
	var ping, name, path sql.NullString
	err := row.Scan(&sn.ID, &sn.RunID, &kind, &sn.StageID, &sn.Iteration,
		&ping, &name, &path, &sn.Filename, &sn.Mime, &sn.Size, &sn.SHA256,
		&sn.Truncated, &sn.CreatedAt)
	if err != nil {
		return sn, err
	}
	sn.Kind = filestore.SnapshotKind(kind)
	sn.PingID, sn.Name, sn.Path = ping.String, name.String, path.String
	return sn, nil
}

func (s *Store) querySnapshots(ctx context.Context, q string, args ...any) ([]filestore.Snapshot, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []filestore.Snapshot
	for rows.Next() {
		sn, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}

func (s *Store) InsertSnapshot(ctx context.Context, sn filestore.Snapshot) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO snapshots (`+snapshotCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		sn.ID, sn.RunID, string(sn.Kind), sn.StageID, sn.Iteration,
		nullStr(sn.PingID), nullStr(sn.Name), nullStr(sn.Path),
		sn.Filename, sn.Mime, sn.Size, sn.SHA256, sn.Truncated, sn.CreatedAt)
	return err
}

func (s *Store) GetSnapshot(ctx context.Context, id string) (filestore.Snapshot, error) {
	sn, err := scanSnapshot(s.pool.QueryRow(ctx,
		`SELECT `+snapshotCols+` FROM snapshots WHERE id=$1`, id))
	if err == pgx.ErrNoRows {
		return sn, fmt.Errorf("snapshot %s: %w", id, filestore.ErrNotFound)
	}
	return sn, err
}

func (s *Store) SnapshotsForRun(ctx context.Context, runID string) ([]filestore.Snapshot, error) {
	return s.querySnapshots(ctx,
		`SELECT `+snapshotCols+` FROM snapshots WHERE run_id=$1 ORDER BY stage_id, iteration, created_at, id`, runID)
}

func (s *Store) SnapshotsForPing(ctx context.Context, pingID string) ([]filestore.Snapshot, error) {
	return s.querySnapshots(ctx,
		`SELECT `+snapshotCols+` FROM snapshots WHERE ping_id=$1 ORDER BY created_at, id`, pingID)
}

// LiveBlobSHAs is the blob sweep's mark set: every sha a row still
// references. keepSnapshotsAfter narrows the snapshot side for content
// retention (DESIGN-FILES §6), per owner: a snapshot ROW older than its
// owner's window keeps its metadata but stops pinning bytes. An owner
// absent from the map keeps everything (retention "never").
func (s *Store) LiveBlobSHAs(ctx context.Context, keepSnapshotsAfter map[string]time.Time) (map[string]bool, error) {
	owners := make([]string, 0, len(keepSnapshotsAfter))
	cutoffs := make([]time.Time, 0, len(keepSnapshotsAfter))
	for owner, at := range keepSnapshotsAfter {
		owners = append(owners, owner)
		cutoffs = append(cutoffs, at)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT sha256 FROM attachments
		UNION
		SELECT sn.sha256 FROM snapshots sn
		  LEFT JOIN runs r ON r.run_id = sn.run_id
		  LEFT JOIN unnest($1::text[], $2::timestamptz[]) AS c(user_id, keep_after)
		         ON c.user_id = COALESCE(r.user_id, '')
		 WHERE c.keep_after IS NULL OR sn.created_at >= c.keep_after
		UNION
		SELECT f->>'sha256' FROM skills sk, jsonb_array_elements(sk.files) f
		UNION
		SELECT f->>'sha256' FROM skillset_versions sv, jsonb_array_elements(sv.manifest) m, jsonb_array_elements(m->'files') f`, owners, cutoffs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	live := map[string]bool{}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		live[sha] = true
	}
	return live, rows.Err()
}

var (
	_ filestore.AttachmentDB = (*Store)(nil)
	_ filestore.SnapshotDB   = (*Store)(nil)
)
