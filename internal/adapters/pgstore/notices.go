package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"apothecary/internal/notices"
)

const noticeCols = `id, user_id, kind, key, title, body, link, count, created_at, last_seen_at, cleared_at, dismissed_at`

func scanNotice(row pgx.Row, extra ...any) (notices.Notice, error) {
	var n notices.Notice
	var cleared, dismissed *time.Time
	dst := append([]any{&n.ID, &n.UserID, &n.Kind, &n.Key, &n.Title, &n.Body, &n.Link, &n.Count,
		&n.CreatedAt, &n.LastSeenAt, &cleared, &dismissed}, extra...)
	if err := row.Scan(dst...); err != nil {
		return notices.Notice{}, err
	}
	if cleared != nil {
		n.ClearedAt = *cleared
	}
	if dismissed != nil {
		n.DismissedAt = *dismissed
	}
	return n, nil
}

// RaiseNotice is one statement so concurrent repeats of a key still land on one row.
func (s *Store) RaiseNotice(ctx context.Context, n notices.Notice) (notices.Notice, bool, error) {
	var inserted bool
	got, err := scanNotice(s.pool.QueryRow(ctx, `
		INSERT INTO notices (id, user_id, kind, key, title, body, link, count, created_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8, $8)
		ON CONFLICT (user_id, key) WHERE key <> '' AND cleared_at IS NULL DO UPDATE SET
			title=EXCLUDED.title, body=EXCLUDED.body, link=EXCLUDED.link,
			count=notices.count+1, last_seen_at=EXCLUDED.last_seen_at
		RETURNING `+noticeCols+`, (xmax = 0)`,
		n.ID, n.UserID, n.Kind, n.Key, n.Title, n.Body, n.Link, n.LastSeenAt), &inserted)
	if err != nil {
		return notices.Notice{}, false, fmt.Errorf("raise notice: %w", err)
	}
	return got, inserted, nil
}

func (s *Store) ClearNotices(ctx context.Context, key string, at time.Time) error {
	if key == "" {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `UPDATE notices SET cleared_at=$2 WHERE key=$1 AND cleared_at IS NULL`, key, at); err != nil {
		return fmt.Errorf("clear notices: %w", err)
	}
	return nil
}

func (s *Store) ListNotices(ctx context.Context, userID string) ([]notices.Notice, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+noticeCols+` FROM notices
		WHERE user_id=$1 AND dismissed_at IS NULL ORDER BY last_seen_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list notices: %w", err)
	}
	defer rows.Close()
	var out []notices.Notice
	for rows.Next() {
		n, err := scanNotice(rows)
		if err != nil {
			return nil, fmt.Errorf("list notices: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list notices: %w", err)
	}
	return out, nil
}

func (s *Store) GetNotice(ctx context.Context, id string) (notices.Notice, error) {
	n, err := scanNotice(s.pool.QueryRow(ctx, `SELECT `+noticeCols+` FROM notices WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return notices.Notice{}, fmt.Errorf("notice %s: %w", id, notices.ErrNotFound)
	}
	if err != nil {
		return notices.Notice{}, fmt.Errorf("get notice: %w", err)
	}
	return n, nil
}

func (s *Store) DismissNotice(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE notices SET dismissed_at=COALESCE(dismissed_at, $2) WHERE id=$1`, id, at)
	if err != nil {
		return fmt.Errorf("dismiss notice: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("notice %s: %w", id, notices.ErrNotFound)
	}
	return nil
}

func (s *Store) DismissAllNotices(ctx context.Context, userID string, at time.Time) error {
	if _, err := s.pool.Exec(ctx, `UPDATE notices SET dismissed_at=$2 WHERE user_id=$1 AND dismissed_at IS NULL`, userID, at); err != nil {
		return fmt.Errorf("dismiss notices: %w", err)
	}
	return nil
}

var _ notices.Store = (*Store)(nil)
