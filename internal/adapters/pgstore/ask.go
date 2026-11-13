package pgstore

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"apothecary/internal/engine/ask"
)

// The Ask pane's rows (migrations 0001, 0003): any number of threads per
// run, and the run's messages in order across them. Appends are serialized
// per run by the engine, across all of the run's threads, so the next seq is
// read in the same statement that takes it.

const askThreadCols = `id, session_id, model, created_at, updated_at`

func scanAskThread(row pgx.Row, runID string) (ask.Thread, error) {
	t := ask.Thread{RunID: runID}
	err := row.Scan(&t.ID, &t.SessionID, &t.Model, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (s *Store) AskThread(ctx context.Context, runID, threadID string) (ask.Thread, bool, error) {
	t, err := scanAskThread(s.pool.QueryRow(ctx,
		`SELECT `+askThreadCols+` FROM ask_threads WHERE run_id = $1 AND id = $2`, runID, threadID), runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ask.Thread{}, false, nil
	}
	if err != nil {
		return ask.Thread{}, false, err
	}
	return t, true, nil
}

func (s *Store) AskThreads(ctx context.Context, runID string) ([]ask.Thread, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+askThreadCols+` FROM ask_threads WHERE run_id = $1 ORDER BY created_at, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ask.Thread
	for rows.Next() {
		t, err := scanAskThread(rows, runID)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) SaveAskThread(ctx context.Context, t ask.Thread) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ask_threads (run_id, id, session_id, model, created_at, updated_at)
		VALUES ($1, $2, $3, $4, now(), now())
		ON CONFLICT (run_id, id) DO UPDATE SET
		  session_id = EXCLUDED.session_id,
		  model      = EXCLUDED.model,
		  updated_at = now()`, t.RunID, t.ID, t.SessionID, t.Model)
	return err
}

func (s *Store) AppendAskMessage(ctx context.Context, m ask.Message) (ask.Message, error) {
	if m.At.IsZero() {
		m.At = time.Now().UTC()
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ask_messages (run_id, seq, id, role, text, user_id, model, cost_usd, turns, error, at, thread_id)
		VALUES ($1, COALESCE((SELECT MAX(seq) FROM ask_messages WHERE run_id = $1), 0) + 1,
		        $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING seq`,
		m.RunID, m.ID, m.Role, m.Text, m.UserID, m.Model, m.CostUSD, m.Turns, m.Error, m.At, m.ThreadID).Scan(&m.Seq)
	return m, err
}

func (s *Store) AskMessages(ctx context.Context, runID string, afterSeq int64) ([]ask.Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT seq, thread_id, id, role, text, user_id, model, cost_usd, turns, error, at
		FROM ask_messages WHERE run_id = $1 AND seq > $2 ORDER BY seq`, runID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ask.Message
	for rows.Next() {
		m := ask.Message{RunID: runID}
		if err := rows.Scan(&m.Seq, &m.ThreadID, &m.ID, &m.Role, &m.Text, &m.UserID, &m.Model, &m.CostUSD, &m.Turns, &m.Error, &m.At); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

var _ ask.Store = (*Store)(nil)
