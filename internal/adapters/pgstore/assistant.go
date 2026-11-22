package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"apothecary/internal/assistant"
)

// The assistant's rows (migrations 0013, 0016, 0018): sessions, their
// messages, persons' choices. A session's turns are serialized by the
// service, so the next seq is read in the statement that takes it, as the
// Ask pane's are.

const assistantSessionCols = `id, user_id, kind, workshop_id, subject, harness_session, integration_id, model, token_id, skillsets, created_at, updated_at`

func scanAssistantSession(row pgx.Row) (assistant.Session, error) {
	var v assistant.Session
	var kind string
	var sets []byte
	err := row.Scan(&v.ID, &v.UserID, &kind, &v.Workshop, &v.Subject, &v.HarnessSession, &v.Integration, &v.Model, &v.TokenID, &sets, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	v.Kind = assistant.Kind(kind)
	v.CreatedAt, v.UpdatedAt = v.CreatedAt.UTC(), v.UpdatedAt.UTC()
	return v, readSkillsets(sets, &v.Skillsets)
}

// readSkillsets reads a skillsets column into out: none (the default) is nil.
func readSkillsets[T any](raw []byte, out *[]T) error {
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("skillsets: %w", err)
	}
	if len(*out) == 0 {
		*out = nil
	}
	return nil
}

// skillsetsJSON is a skillsets column's value: none is the empty list.
func skillsetsJSON[T any](v []T) (string, error) {
	if v == nil {
		v = []T{}
	}
	data, err := json.Marshal(v)
	return string(data), err
}

func (s *Store) AssistantSession(ctx context.Context, id string) (assistant.Session, bool, error) {
	v, err := scanAssistantSession(s.pool.QueryRow(ctx, `SELECT `+assistantSessionCols+` FROM assistant_sessions WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistant.Session{}, false, nil
	}
	return v, err == nil, err
}

func (s *Store) FindAssistantSession(ctx context.Context, userID string, kind assistant.Kind, workshop, subject string) (assistant.Session, bool, error) {
	v, err := scanAssistantSession(s.pool.QueryRow(ctx, `SELECT `+assistantSessionCols+` FROM assistant_sessions
		WHERE user_id = $1 AND kind = $2 AND workshop_id = $3 AND subject = $4
		ORDER BY updated_at DESC, id DESC LIMIT 1`, userID, string(kind), workshop, subject))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistant.Session{}, false, nil
	}
	return v, err == nil, err
}

func (s *Store) AssistantSessions(ctx context.Context, userID string) ([]assistant.Session, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+assistantSessionCols+` FROM assistant_sessions
		WHERE user_id = $1 ORDER BY updated_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []assistant.Session
	for rows.Next() {
		v, err := scanAssistantSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) SaveAssistantSession(ctx context.Context, v assistant.Session) error {
	if err := assistant.CheckSession(v); err != nil {
		return err
	}
	sets, err := skillsetsJSON(v.Skillsets)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO assistant_sessions (id, user_id, kind, workshop_id, subject, harness_session, integration_id, model, token_id, skillsets, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, clock_timestamp(), clock_timestamp())
		ON CONFLICT (id) DO UPDATE SET
		  subject = EXCLUDED.subject, harness_session = EXCLUDED.harness_session, integration_id = EXCLUDED.integration_id,
		  model = EXCLUDED.model, token_id = EXCLUDED.token_id, skillsets = EXCLUDED.skillsets, updated_at = clock_timestamp()
		WHERE assistant_sessions.user_id = EXCLUDED.user_id`,
		v.ID, v.UserID, string(v.Kind), v.Workshop, v.Subject, v.HarnessSession, v.Integration, v.Model, v.TokenID, sets)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.Join(assistant.ErrInvalid, errors.New("a session changes owner"))
	}
	return nil
}

func (s *Store) AppendAssistantMessage(ctx context.Context, m assistant.Message) (assistant.Message, error) {
	if err := assistant.CheckMessage(m); err != nil {
		return assistant.Message{}, err
	}
	if m.At.IsZero() {
		m.At = time.Now()
	}
	m.At = m.At.Truncate(time.Microsecond).UTC()
	var fill, req any
	if len(m.Fill) > 0 {
		fill = string(m.Fill)
	}
	if m.Request != nil {
		data, err := json.Marshal(m.Request)
		if err != nil {
			return assistant.Message{}, err
		}
		req = string(data)
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO assistant_messages (session_id, seq, role, text, fill, request, error, cost_usd, at)
		SELECT $1, COALESCE((SELECT MAX(seq) FROM assistant_messages WHERE session_id = $1), 0) + 1, $2, $3, $4::jsonb, $8::jsonb, $5, $6, $7
		WHERE EXISTS (SELECT 1 FROM assistant_sessions WHERE id = $1)
		RETURNING seq`, m.SessionID, m.Role, m.Text, fill, m.Error, m.CostUSD, m.At, req).Scan(&m.Seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return assistant.Message{}, errors.Join(assistant.ErrInvalid, errors.New("no session "+m.SessionID))
	}
	return m, err
}

func (s *Store) AssistantMessages(ctx context.Context, sessionID string, afterSeq int64) ([]assistant.Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT seq, role, text, fill, request, error, cost_usd, at FROM assistant_messages
		WHERE session_id = $1 AND seq > $2 ORDER BY seq`, sessionID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []assistant.Message
	for rows.Next() {
		m := assistant.Message{SessionID: sessionID}
		var fill, req []byte
		if err := rows.Scan(&m.Seq, &m.Role, &m.Text, &fill, &req, &m.Error, &m.CostUSD, &m.At); err != nil {
			return nil, err
		}
		if len(fill) > 0 {
			m.Fill = fill
		}
		if len(req) > 0 {
			var r assistant.Request
			if err := json.Unmarshal(req, &r); err != nil {
				return nil, err
			}
			m.Request = &r
		}
		m.At = m.At.UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) SetAssistantRequest(ctx context.Context, sessionID string, seq int64, from string, r assistant.Request) error {
	if err := assistant.CheckRequest(r); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE assistant_messages SET request = $3::jsonb
		WHERE session_id = $1 AND seq = $2 AND request ->> 'state' = $4`, sessionID, seq, string(data), from)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	// Why not: no such request, or it moved on.
	var state *string
	err = s.pool.QueryRow(ctx, `SELECT request ->> 'state' FROM assistant_messages WHERE session_id = $1 AND seq = $2`, sessionID, seq).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && state == nil) {
		return errors.Join(assistant.ErrInvalid, fmt.Errorf("no message %d holding a request in session %s", seq, sessionID))
	}
	if err != nil {
		return err
	}
	return errors.Join(assistant.ErrStale, fmt.Errorf("the request is %s, not %s", *state, from))
}

func (s *Store) AssistantChoice(ctx context.Context, userID string) (assistant.Choice, error) {
	c := assistant.Choice{UserID: userID}
	var sets []byte
	err := s.pool.QueryRow(ctx, `SELECT integration_id, model, skillsets FROM assistant_choices WHERE user_id = $1`, userID).Scan(&c.Integration, &c.Model, &sets)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, readSkillsets(sets, &c.Skillsets)
}

func (s *Store) SaveAssistantChoice(ctx context.Context, c assistant.Choice) error {
	if c.UserID == "" {
		return errors.Join(assistant.ErrInvalid, errors.New("a choice needs its person"))
	}
	sets, err := skillsetsJSON(c.Skillsets)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO assistant_choices (user_id, integration_id, model, skillsets, updated_at) VALUES ($1, $2, $3, $4::jsonb, now())
		ON CONFLICT (user_id) DO UPDATE SET integration_id = EXCLUDED.integration_id, model = EXCLUDED.model,
		  skillsets = EXCLUDED.skillsets, updated_at = now()`,
		c.UserID, c.Integration, c.Model, sets)
	return err
}

var _ assistant.Store = (*Store)(nil)
