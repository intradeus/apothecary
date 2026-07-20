// Package pgstore is the production event.Store (DESIGN.md §0, §6, §9):
// PostgreSQL via pgx/v5, hand-written SQL, numbered migrations applied at
// boot. The runs and pings projections are updated in the same transaction
// as each append and are rebuildable from events alone.
//
// Watch fan-out is in-process: the engine is the only writer (§2
// single-writer rule, one apothecaryd per machine in v1), so subscribers are
// notified after commit without LISTEN/NOTIFY.
package pgstore

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"apothecary/internal/event"
	"apothecary/internal/run"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	pool *pgxpool.Pool

	mu   sync.Mutex
	subs map[string][]*sub

	// Clock stamps events (UTC); injectable for tests.
	Clock func() time.Time
}

type sub struct {
	ch     chan event.Event
	cancel <-chan struct{}
}

// Open connects, applies pending migrations, and returns the store.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}
	s := &Store{pool: pool, subs: map[string][]*sub{}, Clock: time.Now}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("migrations table: %w", err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		var version int
		if _, err := fmt.Sscanf(name, "%d_", &version); err != nil {
			return fmt.Errorf("migration %s: name must start with a number", name)
		}
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sql, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// --- event.Store ------------------------------------------------------------

func (s *Store) Append(ctx context.Context, runID string, payloads ...event.Payload) ([]event.Event, error) {
	if runID == "" {
		return nil, fmt.Errorf("append: empty run id")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var next int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq),0)+1 FROM events WHERE run_id=$1`, runID).Scan(&next); err != nil {
		return nil, err
	}
	ts := s.Clock().UTC()
	out := make([]event.Event, 0, len(payloads))
	for i, p := range payloads {
		data, err := json.Marshal(p)
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", p.EventType(), err)
		}
		e := event.Event{RunID: runID, Seq: next + int64(i), TS: ts, Type: p.EventType(), Payload: p}
		if _, err := tx.Exec(ctx,
			`INSERT INTO events (run_id, seq, ts, type, payload) VALUES ($1,$2,$3,$4,$5)`,
			e.RunID, e.Seq, e.TS, string(e.Type), data); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := s.projectTx(ctx, tx, runID, out); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	s.mu.Lock()
	for _, sb := range s.subs[runID] {
		for _, e := range out {
			select {
			case sb.ch <- e:
			case <-sb.cancel:
			}
		}
	}
	s.mu.Unlock()
	return out, nil
}

// projectTx folds the new events into the runs and pings projections inside
// the append transaction (§6). Rebuild uses the same code.
func (s *Store) projectTx(ctx context.Context, tx pgx.Tx, runID string, evs []event.Event) error {
	v, found, err := loadView(ctx, tx, runID)
	if err != nil {
		return err
	}
	if !found {
		v = run.View{}
	}
	for i := range evs {
		run.Apply(&v, &evs[i])
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO runs (run_id, project_id, state, ticket_title, flow_name, flow_hash, current_stage, mr_url, env_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (run_id) DO UPDATE SET
			project_id=$2, state=$3, ticket_title=$4, flow_name=$5, flow_hash=$6,
			current_stage=$7, mr_url=$8, env_id=$9, created_at=$10, updated_at=$11`,
		v.RunID, v.ProjectID, string(v.State), v.TicketTitle, v.FlowName, v.FlowHash,
		v.CurrentStage, v.MRURL, v.EnvID, v.CreatedAt, v.UpdatedAt); err != nil {
		return err
	}
	for i := range evs {
		switch p := evs[i].Payload.(type) {
		case *event.PingRaised:
			if _, err := tx.Exec(ctx, `
				INSERT INTO pings (ping_id, run_id, type, question, context, payload_ref, status, raised_at)
				VALUES ($1,$2,$3,$4,$5,$6,'pending',$7)
				ON CONFLICT (ping_id) DO NOTHING`,
				p.PingID, runID, p.Type, p.Question, p.Context, p.PayloadRef, evs[i].TS); err != nil {
				return err
			}
		case *event.PingAnswered:
			if _, err := tx.Exec(ctx, `
				UPDATE pings SET status='answered', answer=$2, feedback=$3, answered_at=$4 WHERE ping_id=$1`,
				p.PingID, p.Answer, p.Feedback, evs[i].TS); err != nil {
				return err
			}
		}
	}
	return nil
}

func loadView(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, runID string) (run.View, bool, error) {
	var v run.View
	var state string
	err := q.QueryRow(ctx, `
		SELECT run_id, project_id, state, ticket_title, flow_name, flow_hash, current_stage, mr_url, env_id, created_at, updated_at
		FROM runs WHERE run_id=$1`, runID).
		Scan(&v.RunID, &v.ProjectID, &state, &v.TicketTitle, &v.FlowName, &v.FlowHash,
			&v.CurrentStage, &v.MRURL, &v.EnvID, &v.CreatedAt, &v.UpdatedAt)
	if err == pgx.ErrNoRows {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	v.State = run.State(state)
	return v, true, nil
}

func (s *Store) Events(ctx context.Context, runID string, afterSeq int64) ([]event.Event, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT run_id, seq, ts, type, payload FROM events WHERE run_id=$1 AND seq>$2 ORDER BY seq`,
		runID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []event.Event
	for rows.Next() {
		var e event.Event
		var typ string
		var payload []byte
		if err := rows.Scan(&e.RunID, &e.Seq, &e.TS, &typ, &payload); err != nil {
			return nil, err
		}
		e.Type = event.Type(typ)
		p, err := event.UnmarshalPayload(e.Type, payload)
		if err != nil {
			return nil, fmt.Errorf("run %s seq %d: %w", e.RunID, e.Seq, err)
		}
		e.Payload = p
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) RunIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT run_id FROM events ORDER BY run_id`)
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

func (s *Store) Watch(ctx context.Context, runID string, afterSeq int64) (<-chan event.Event, error) {
	// Subscribe first, then replay, deduplicating by seq — live events
	// committed during the replay query would otherwise be lost.
	sb := &sub{ch: make(chan event.Event, 256), cancel: ctx.Done()}
	s.mu.Lock()
	s.subs[runID] = append(s.subs[runID], sb)
	s.mu.Unlock()

	replay, err := s.Events(ctx, runID, afterSeq)
	if err != nil {
		s.unsubscribe(runID, sb)
		return nil, err
	}
	out := make(chan event.Event)
	go func() {
		defer close(out)
		defer s.unsubscribe(runID, sb)
		last := afterSeq
		for _, e := range replay {
			select {
			case out <- e:
				last = e.Seq
			case <-ctx.Done():
				return
			}
		}
		for {
			select {
			case e := <-sb.ch:
				if e.Seq <= last {
					continue
				}
				select {
				case out <- e:
					last = e.Seq
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (s *Store) unsubscribe(runID string, sb *sub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	subs := s.subs[runID]
	for i, x := range subs {
		if x == sb {
			s.subs[runID] = append(subs[:i], subs[i+1:]...)
			return
		}
	}
}

// --- projections rebuild (§6: apothecaryd rebuild-projections) -----------------

// RebuildProjections truncates the runs and pings tables and replays every
// run's events through the same projection code the append path uses.
func (s *Store) RebuildProjections(ctx context.Context) (int, error) {
	ids, err := s.RunIDs(ctx)
	if err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `TRUNCATE runs, pings`); err != nil {
		return 0, err
	}
	for _, id := range ids {
		evs, err := s.Events(ctx, id, 0)
		if err != nil {
			return 0, err
		}
		if err := s.projectTx(ctx, tx, id, evs); err != nil {
			return 0, fmt.Errorf("rebuild %s: %w", id, err)
		}
	}
	return len(ids), tx.Commit(ctx)
}

// RunViews reads the runs read-model table (the §8 list endpoint's backing
// once serve wires this store).
func (s *Store) RunViews(ctx context.Context) ([]run.View, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT run_id, project_id, state, ticket_title, flow_name, flow_hash, current_stage, mr_url, env_id, created_at, updated_at
		FROM runs ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []run.View
	for rows.Next() {
		var v run.View
		var state string
		if err := rows.Scan(&v.RunID, &v.ProjectID, &state, &v.TicketTitle, &v.FlowName, &v.FlowHash,
			&v.CurrentStage, &v.MRURL, &v.EnvID, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.State = run.State(state)
		out = append(out, v)
	}
	return out, rows.Err()
}

// --- channels persistence (§9) ----------------------------------------------

func (s *Store) SaveChannel(ctx context.Context, id, kind string, config map[string]any) error {
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO channels (id, kind, config) VALUES ($1,$2,$3)`, id, kind, data)
	return err
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM channels WHERE id=$1`, id)
	return err
}

type ChannelRow struct {
	ID     string
	Kind   string
	Config map[string]any
}

func (s *Store) LoadChannels(ctx context.Context) ([]ChannelRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, kind, config FROM channels ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChannelRow
	for rows.Next() {
		var r ChannelRow
		var data []byte
		if err := rows.Scan(&r.ID, &r.Kind, &data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &r.Config); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PendingPings reads the pings projection (CPUI inbox backing for a future
// step; the live engine registry remains canonical while runs are active).
func (s *Store) PendingPings(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ping_id, run_id, type, question, context, payload_ref, raised_at
		FROM pings WHERE status='pending' ORDER BY raised_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var pingID, runID, typ, question, context_, payloadRef string
		var raisedAt time.Time
		if err := rows.Scan(&pingID, &runID, &typ, &question, &context_, &payloadRef, &raisedAt); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"ping_id": pingID, "run_id": runID, "type": typ, "question": question,
			"context": context_, "payload_ref": payloadRef, "raised_at": raisedAt,
		})
	}
	return out, rows.Err()
}

var _ event.Store = (*Store)(nil)

// Sanity: reject accidental use of a non-postgres DSN early.
func ValidateDSN(dsn string) error {
	if !strings.HasPrefix(dsn, "postgres://") && !strings.Contains(dsn, "host=") {
		return fmt.Errorf("postgres_dsn %q does not look like a Postgres DSN", dsn)
	}
	return nil
}
