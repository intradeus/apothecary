package pgstore

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"apothecary/internal/ask"
)

// The Ask pane's rows (migration 0015): one thread per run, its messages in
// order. Appends are serialized per run by the engine (one answer in flight),
// so the next seq is read in the same statement that takes it.

func (s *Store) AskThread(ctx context.Context, runID string) (ask.Thread, bool, error) {
	t := ask.Thread{RunID: runID}
	err := s.pool.QueryRow(ctx,
		`SELECT session_id, model, updated_at FROM ask_threads WHERE run_id = $1`, runID).
		Scan(&t.SessionID, &t.Model, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ask.Thread{}, false, nil
	}
	if err != nil {
		return ask.Thread{}, false, err
	}
	return t, true, nil
}

func (s *Store) SaveAskThread(ctx context.Context, t ask.Thread) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ask_threads (run_id, session_id, model, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (run_id) DO UPDATE SET
		  session_id = EXCLUDED.session_id,
		  model      = EXCLUDED.model,
		  updated_at = now()`, t.RunID, t.SessionID, t.Model)
	return err
}

func (s *Store) AppendAskMessage(ctx context.Context, m ask.Message) (ask.Message, error) {
	if m.At.IsZero() {
		m.At = time.Now().UTC()
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ask_messages (run_id, seq, id, role, text, user_id, model, cost_usd, turns, error, at)
		VALUES ($1, COALESCE((SELECT MAX(seq) FROM ask_messages WHERE run_id = $1), 0) + 1,
		        $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING seq`,
		m.RunID, m.ID, m.Role, m.Text, m.UserID, m.Model, m.CostUSD, m.Turns, m.Error, m.At).Scan(&m.Seq)
	return m, err
}

func (s *Store) AskMessages(ctx context.Context, runID string, afterSeq int64) ([]ask.Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT seq, id, role, text, user_id, model, cost_usd, turns, error, at
		FROM ask_messages WHERE run_id = $1 AND seq > $2 ORDER BY seq`, runID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ask.Message
	for rows.Next() {
		m := ask.Message{RunID: runID}
		if err := rows.Scan(&m.Seq, &m.ID, &m.Role, &m.Text, &m.UserID, &m.Model, &m.CostUSD, &m.Turns, &m.Error, &m.At); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

var _ ask.Store = (*Store)(nil)
