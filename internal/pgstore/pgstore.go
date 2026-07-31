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
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"apothecary/internal/artifacts"
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
	// overflow is closed by Append when the buffer is full: appends hold
	// s.mu and must never block on a slow consumer. The Watch goroutine
	// exits; the SSE client re-syncs via after_seq on reconnect.
	overflow chan struct{}
	dropOnce sync.Once
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
	// Advisory lock serializes concurrently booting instances — without it,
	// two boots can both pass the applied-version check and race the DDL.
	lockConn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lockConn.Release()
	const migLockKey = int64(0x61706f7468656361) // "apotheca"
	if _, err := lockConn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migLockKey); err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	defer func() { _, _ = lockConn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migLockKey) }()

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
			default:
				sb.dropOnce.Do(func() { close(sb.overflow) })
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
			var opts []byte
			if len(p.Options) > 0 {
				opts, _ = json.Marshal(p.Options)
			}
			var refs []byte
			if len(p.PayloadRefs) > 0 {
				refs, _ = json.Marshal(p.PayloadRefs)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO pings (ping_id, run_id, type, question, context, payload_ref, payload_refs, stage_id, options, status, raised_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10)
				ON CONFLICT (ping_id) DO NOTHING`,
				p.PingID, runID, p.Type, p.Question, p.Context, p.PayloadRef, refs, p.StageID, opts, evs[i].TS); err != nil {
				return err
			}
		case *event.PingAnswered:
			if _, err := tx.Exec(ctx, `
				UPDATE pings SET status='answered', answer=$2, feedback=$3, answered_at=$4 WHERE ping_id=$1`,
				p.PingID, p.Answer, p.Feedback, evs[i].TS); err != nil {
				return err
			}
		case *event.RunAbandoned, *event.RunFailed, *event.RunArchived, *event.RunCompleted:
			// Terminal run ⇒ withdraw any still-open ping: its live handler is
			// gone, so it can never be answered. Keeps the inbox from showing a
			// dead request (parity with run.ReducePings).
			if _, err := tx.Exec(ctx, `
				UPDATE pings SET status='withdrawn' WHERE run_id=$1 AND status='pending'`,
				runID); err != nil {
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
	sb := &sub{ch: make(chan event.Event, 256), cancel: ctx.Done(), overflow: make(chan struct{})}
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
			case <-sb.overflow:
				return
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

// --- run_exposed_ports (portrelay persistence) -----------------------------

// SaveExposedPort records (or updates) a run's workspace-port → host-port
// mapping so the exposure can be re-created on the same host port after an
// engine restart. Standalone durable state, not an event.
func (s *Store) SaveExposedPort(ctx context.Context, runID string, wsPort, hostPort int) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO run_exposed_ports (run_id, ws_port, host_port) VALUES ($1,$2,$3)
		 ON CONFLICT (run_id, ws_port) DO UPDATE SET host_port = EXCLUDED.host_port`,
		runID, wsPort, hostPort)
	return err
}

// DeleteExposedPorts drops every saved port of a run (its environment is
// being torn down).
func (s *Store) DeleteExposedPorts(ctx context.Context, runID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM run_exposed_ports WHERE run_id=$1`, runID)
	return err
}

// LoadExposedPorts returns every saved exposure as runID → wsPort →
// hostPort (read once at boot).
func (s *Store) LoadExposedPorts(ctx context.Context) (map[string]map[int]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT run_id, ws_port, host_port FROM run_exposed_ports`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[int]int{}
	for rows.Next() {
		var runID string
		var wsPort, hostPort int
		if err := rows.Scan(&runID, &wsPort, &hostPort); err != nil {
			return nil, err
		}
		if out[runID] == nil {
			out[runID] = map[int]int{}
		}
		out[runID][wsPort] = hostPort
	}
	return out, rows.Err()
}

// --- artifacts (human-attached files: metadata; bytes live on disk) --------

// InsertArtifact records an artifact: a freshly-uploaded orphan, or an
// engine-captured attempt snapshot that arrives already attached (M27).
func (s *Store) InsertArtifact(ctx context.Context, m artifacts.Meta) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO artifacts (id, run_id, ping_id, scope, filename, mime, size, sha256, stage_id, iteration, output_name)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		m.ID, m.RunID, m.PingID, m.Scope, m.Filename, m.Mime, m.Size, m.SHA256,
		m.StageID, m.Iteration, m.OutputName)
	return err
}

// AssociateArtifacts claims orphan artifacts for a run/ping/scope. Only rows
// still in scope='orphan' are claimed, so a replayed or forged id can never
// re-home another run's file (or steal one already attached).
func (s *Store) AssociateArtifacts(ctx context.Context, ids []string, runID, pingID, scope string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE artifacts SET run_id=$1, ping_id=$2, scope=$3
		 WHERE id = ANY($4) AND scope='orphan'`,
		runID, pingID, scope, ids)
	return err
}

// ArtifactsForRun lists the run's HUMAN attachments. Stage snapshots are
// excluded: they are the audit trail, not something to re-deliver into a
// workspace (M27).
func (s *Store) ArtifactsForRun(ctx context.Context, runID string) ([]artifacts.Meta, error) {
	return s.scanArtifacts(ctx, `
		SELECT id, run_id, ping_id, scope, filename, mime, size, sha256, created_at, stage_id, iteration, output_name
		FROM artifacts WHERE run_id=$1 AND scope NOT IN ('stage_output','stage_log')
		ORDER BY created_at`, runID)
}

// ArtifactsForStage lists a run's per-attempt snapshots in attempt order.
func (s *Store) ArtifactsForStage(ctx context.Context, runID string) ([]artifacts.Meta, error) {
	return s.scanArtifacts(ctx, `
		SELECT id, run_id, ping_id, scope, filename, mime, size, sha256, created_at, stage_id, iteration, output_name
		FROM artifacts WHERE run_id=$1 AND scope IN ('stage_output','stage_log')
		ORDER BY stage_id, iteration, created_at`, runID)
}

func (s *Store) ArtifactsForPing(ctx context.Context, pingID, scope string) ([]artifacts.Meta, error) {
	return s.scanArtifacts(ctx, `
		SELECT id, run_id, ping_id, scope, filename, mime, size, sha256, created_at, stage_id, iteration, output_name
		FROM artifacts WHERE ping_id=$1 AND scope=$2 ORDER BY created_at`, pingID, scope)
}

func (s *Store) GetArtifact(ctx context.Context, id string) (artifacts.Meta, error) {
	rows, err := s.scanArtifacts(ctx, `
		SELECT id, run_id, ping_id, scope, filename, mime, size, sha256, created_at, stage_id, iteration, output_name
		FROM artifacts WHERE id=$1`, id)
	if err != nil {
		return artifacts.Meta{}, err
	}
	if len(rows) == 0 {
		return artifacts.Meta{}, artifacts.ErrNotFound
	}
	return rows[0], nil
}

// DeleteArtifactsForRun removes a run's artifact rows, returning the deleted
// ids so the caller can unlink the blobs.
// Audit-scoped rows are deliberately kept: archiving a run reclaims its
// workspace and uploads, but the audit trail is the point of M27 — it must
// outlive the run it describes. That covers stage snapshots and the ping
// payloads a human was shown when they approved something.
func (s *Store) DeleteArtifactsForRun(ctx context.Context, runID string) ([]string, error) {
	return s.scanArtifactIDs(ctx,
		`DELETE FROM artifacts WHERE run_id=$1 AND scope NOT IN ('stage_output','stage_log','ping_payload') RETURNING id`, runID)
}

// OrphanArtifactIDsBefore returns never-attached uploads older than cutoff.
func (s *Store) OrphanArtifactIDsBefore(ctx context.Context, cutoff time.Time) ([]string, error) {
	return s.scanArtifactIDs(ctx,
		`SELECT id FROM artifacts WHERE scope='orphan' AND created_at < $1`, cutoff)
}

// DeleteArtifacts removes rows by id (blobs unlinked by the caller).
func (s *Store) DeleteArtifacts(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM artifacts WHERE id = ANY($1)`, ids)
	return err
}

func (s *Store) scanArtifacts(ctx context.Context, q string, args ...any) ([]artifacts.Meta, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []artifacts.Meta
	for rows.Next() {
		var m artifacts.Meta
		if err := rows.Scan(&m.ID, &m.RunID, &m.PingID, &m.Scope, &m.Filename,
			&m.Mime, &m.Size, &m.SHA256, &m.CreatedAt,
			&m.StageID, &m.Iteration, &m.OutputName); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) scanArtifactIDs(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListPings reads the pings projection (PLAN4 M15): the durable
// request/response surface. status filters pending|answered; "" or "all"
// returns both. Newest first, capped by limit.
func (s *Store) ListPings(ctx context.Context, status string, limit int) ([]run.PingView, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ping_id, run_id, type, stage_id, question, context, payload_ref, payload_refs,
	             options, status, answer, feedback, raised_at, answered_at
	      FROM pings`
	args := []any{}
	if status != "" && status != "all" {
		q += ` WHERE status=$1`
		args = append(args, status)
	}
	q += ` ORDER BY raised_at DESC LIMIT ` + fmt.Sprint(limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []run.PingView
	for rows.Next() {
		v, err := scanPing(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetPing reads one projected request/response pair by id.
func (s *Store) GetPing(ctx context.Context, id string) (run.PingView, error) {
	v, err := scanPing(s.pool.QueryRow(ctx, `
		SELECT ping_id, run_id, type, stage_id, question, context, payload_ref, payload_refs,
		       options, status, answer, feedback, raised_at, answered_at
		FROM pings WHERE ping_id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return run.PingView{}, fmt.Errorf("ping %s: %w", id, ErrPingNotFound)
	}
	return v, err
}

// ErrPingNotFound is the projection-read miss sentinel.
var ErrPingNotFound = errors.New("ping not found")

func scanPing(row pgx.Row) (run.PingView, error) {
	var v run.PingView
	var opts, refs []byte
	err := row.Scan(&v.PingID, &v.RunID, &v.Type, &v.StageID, &v.Question, &v.Context,
		&v.PayloadRef, &refs, &opts, &v.Status, &v.Answer, &v.Feedback, &v.RaisedAt, &v.AnsweredAt)
	if err == nil {
		if len(opts) > 0 {
			_ = json.Unmarshal(opts, &v.Options)
		}
		if len(refs) > 0 {
			_ = json.Unmarshal(refs, &v.PayloadRefs)
		}
	}
	return v, err
}

var _ event.Store = (*Store)(nil)

// Sanity: reject accidental use of a non-postgres DSN early.
func ValidateDSN(dsn string) error {
	if !strings.HasPrefix(dsn, "postgres://") && !strings.Contains(dsn, "host=") {
		return fmt.Errorf("postgres_dsn %q does not look like a Postgres DSN", dsn)
	}
	return nil
}
