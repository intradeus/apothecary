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
	"database/sql"
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
//
// Applying a migration also rebuilds the projections from the event log: a
// schema change can add a projection column that every existing row lacks
// (runs.view did exactly that), and the projections are derived data by
// design — rebuildable from events alone — so replaying them is always safe.
// It costs one pass over every run's events, once, at the boot that migrates.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}
	s := &Store{pool: pool, subs: map[string][]*sub{}, Clock: time.Now}
	applied, err := s.migrate(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	if applied > 0 {
		if _, err := s.RebuildProjections(ctx); err != nil {
			pool.Close()
			return nil, fmt.Errorf("rebuild projections after migrating: %w", err)
		}
	}
	return s, nil
}

func (s *Store) Close() { s.pool.Close() }

// preSquashHead is the last version of the migration chain folded into
// 0001_init.sql (2026-10-19). A DB that applied that whole chain already has
// exactly the init schema, so migrate collapses its ledger to {1}. Remove
// once no pre-squash DB is left.
//
// That equivalence is why 0001_init.sql is FROZEN at its squash-time content
// (TestInitMigrationIsFrozen): a database that applied version 1 — collapsed
// or fresh — never re-runs it, so an edit to the file reaches only databases
// created after the edit. Every schema change since the squash is its own
// NNNN_name.sql, idempotent where a fresh database already has the result.
//
// One deliberate exception: on 2026-10-24 the repository port and the
// project column that names its integration (repo_account_id) were renamed
// in place, in 0001 and 0006, and the freeze re-taken. A database created
// before that does not match the code; it is reset (the project is
// unreleased), not migrated.
const preSquashHead = 25

// migration is one embedded migrations/NNNN_name.sql file.
type migration struct {
	version int
	name    string
}

// migrations lists the embedded migrations in version order, rejecting
// unnumbered names and duplicate versions.
func migrations() ([]migration, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	out := make([]migration, 0, len(entries))
	seen := map[int]string{}
	for _, e := range entries {
		var version int
		if _, err := fmt.Sscanf(e.Name(), "%d_", &version); err != nil || version <= 0 {
			return nil, fmt.Errorf("migration %s: name must start with a positive number", e.Name())
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %s and %s share version %d", prev, e.Name(), version)
		}
		seen[version] = e.Name()
		out = append(out, migration{version, e.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// migrate applies the pending migrations and reports how many it applied.
func (s *Store) migrate(ctx context.Context) (int, error) {
	migs, err := migrations()
	if err != nil {
		return 0, err
	}

	// Advisory lock serializes concurrently booting instances — without it,
	// two boots can both pass the applied-version check and race the DDL.
	lockConn, err := s.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer lockConn.Release()
	const migLockKey = int64(0x61706f7468656361) // "apotheca"
	if _, err := lockConn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migLockKey); err != nil {
		return 0, fmt.Errorf("migration lock: %w", err)
	}
	defer func() { _, _ = lockConn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migLockKey) }()

	if _, err := s.pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return 0, fmt.Errorf("migrations table: %w", err)
	}
	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		return 0, err
	}
	if squashed(applied) {
		if _, err := s.pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version > 1`); err != nil {
			return 0, fmt.Errorf("collapse pre-squash migrations: %w", err)
		}
		applied = map[int]bool{1: true}
	}

	// A version this binary doesn't ship means the schema was written by a
	// newer apothecaryd (or a pre-squash chain applied only partway): refuse
	// rather than run against tables we don't know.
	known := map[int]bool{}
	for _, m := range migs {
		known[m.version] = true
	}
	for v := range applied {
		if !known[v] {
			return 0, fmt.Errorf("database has migration %d applied, which this apothecaryd does not ship — upgrade apothecaryd, or reset a dev database", v)
		}
	}

	n := 0
	for _, m := range migs {
		if applied[m.version] {
			continue
		}
		sql, err := migrationsFS.ReadFile("migrations/" + m.name)
		if err != nil {
			return n, err
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return n, err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return n, fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, m.version); err != nil {
			_ = tx.Rollback(ctx)
			return n, err
		}
		if err := tx.Commit(ctx); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *Store) appliedMigrations(ctx context.Context) (map[int]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read migrations table: %w", err)
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return nil, fmt.Errorf("read migrations table: %w", err)
	}
	applied := make(map[int]bool, len(versions))
	for _, v := range versions {
		applied[v] = true
	}
	return applied, nil
}

// squashed reports a ledger holding the whole pre-squash chain (1..preSquashHead).
func squashed(applied map[int]bool) bool {
	if len(applied) != preSquashHead {
		return false
	}
	for v := 1; v <= preSquashHead; v++ {
		if !applied[v] {
			return false
		}
	}
	return true
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
	// The row carries the whole view as JSON next to the columns SQL filters
	// on; Dormant and Rebuilding are not in the JSON (json:"-") and get their
	// own columns — Apply reads both, so they must round-trip for the
	// incremental fold to agree with a full reduce.
	view, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO runs (run_id, project_id, state, ticket_title, flow_name, flow_hash, current_stage, mr_url, env_id, created_at, updated_at, view, dormant, rebuilding)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (run_id) DO UPDATE SET
			project_id=$2, state=$3, ticket_title=$4, flow_name=$5, flow_hash=$6,
			current_stage=$7, mr_url=$8, env_id=$9, created_at=$10, updated_at=$11,
			view=$12, dormant=$13, rebuilding=$14`,
		v.RunID, v.ProjectID, string(v.State), v.TicketTitle, v.FlowName, v.FlowHash,
		v.CurrentStage, v.MRURL, v.EnvID, v.CreatedAt, v.UpdatedAt, view, v.Dormant, v.Rebuilding); err != nil {
		return err
	}
	for i := range evs {
		switch p := evs[i].Payload.(type) {
		case *event.PingRaised:
			var form []byte
			if p.Form != nil {
				form, _ = json.Marshal(p.Form)
			}
			var files []byte
			if len(p.Files) > 0 {
				files, _ = json.Marshal(p.Files)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO pings (ping_id, run_id, type, question, context, files, link, stage_id, form, answer_kind, phase, ends_phase, status, raised_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'pending',$13)
				ON CONFLICT (ping_id) DO NOTHING`,
				p.PingID, runID, p.Type, p.Question, p.Context, files, p.Link, p.StageID, form, p.Kind(), p.Phase, p.EndsPhase, evs[i].TS); err != nil {
				return err
			}
		case *event.PingAnswered:
			var ids, content []byte
			if len(p.AttachmentIDs) > 0 {
				ids, _ = json.Marshal(p.AttachmentIDs)
			}
			if len(p.Content) > 0 {
				content, _ = json.Marshal(p.Content)
			}
			if _, err := tx.Exec(ctx, `
				UPDATE pings SET status='answered', answer=$2, feedback=$3, content=$4, answer_attachment_ids=$5, answered_at=$6 WHERE ping_id=$1`,
				p.PingID, p.Answer, p.Feedback, content, ids, evs[i].TS); err != nil {
				return err
			}
			for j, c := range p.Review {
				var att []byte
				if len(c.AttachmentIDs) > 0 {
					att, _ = json.Marshal(c.AttachmentIDs)
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO ping_review_comments (ping_id, run_id, idx, path, start_line, end_line, heading, quote, body, attachment_ids)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
					ON CONFLICT (ping_id, idx) DO NOTHING`,
					p.PingID, runID, j, c.Path, c.StartLine, c.EndLine, c.Heading, c.Quote, c.Body, att); err != nil {
					return err
				}
			}
		case *event.SnapshotCaptured:
			// The snapshots table is a PROJECTION of this event (DESIGN-FILES
			// §3.3). On the live path the row already exists — Capture inserts
			// it in the same logical operation and this no-ops on the id — so
			// this case is what makes rebuild-projections able to reconstruct
			// the table from the log alone (a row whose event never landed
			// disappears on rebuild, which is the correct reading of a crash
			// between capture and append).
			if _, err := tx.Exec(ctx, `
				INSERT INTO snapshots (id, run_id, kind, stage_id, iteration, ping_id, name, path, filename, mime, size, sha256, truncated, created_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
				ON CONFLICT (id) DO NOTHING`,
				p.SnapshotID, runID, p.Kind, p.StageID, p.Iteration,
				nullStr(p.PingID), nullStr(p.Name), nullStr(p.Path),
				p.Filename, p.Mime, p.Size, p.SHA256, p.Truncated, evs[i].TS); err != nil {
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
	v, err := scanView(q.QueryRow(ctx, `SELECT `+viewColumns+` FROM runs WHERE run_id=$1`, runID))
	if err == pgx.ErrNoRows {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	return v, true, nil
}

// viewColumns is what scanView reads: the JSON view plus its two json:"-"
// companions. The typed columns exist for SQL to filter on, not to be read
// back — the JSON is the row.
const viewColumns = `run_id, view, dormant, rebuilding`

func scanView(row pgx.Row) (run.View, error) {
	var v run.View
	var id string
	var view []byte
	if err := row.Scan(&id, &view, &v.Dormant, &v.Rebuilding); err != nil {
		return v, err
	}
	if err := json.Unmarshal(view, &v); err != nil {
		return v, fmt.Errorf("decode runs.view for %s: %w", id, err)
	}
	return v, nil
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

// RebuildProjections truncates the runs, pings and snapshots tables and
// replays every run's events through the same projection code the append
// path uses. Snapshot BLOBS are untouched — rebuilt rows re-point at them by
// sha (DESIGN-FILES §3.3).
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
	if _, err := tx.Exec(ctx, `TRUNCATE runs, pings, ping_review_comments, snapshots`); err != nil {
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

// RunViews reads the runs read-model table: every run's view in one query
// (run.ViewSource — the list endpoint's and the engine pollers' backing).
func (s *Store) RunViews(ctx context.Context) ([]run.View, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+viewColumns+` FROM runs ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []run.View
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			return nil, err
		}
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

// ListPings reads the pings projection (PLAN4 M15): the durable
// request/response surface. status filters pending|answered; "" or "all"
// returns both. Newest first, capped by limit.
func (s *Store) ListPings(ctx context.Context, status string, limit int) ([]run.PingView, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ping_id, run_id, type, stage_id, question, context, files, link,
	             form, answer_kind, phase, ends_phase, status, answer, feedback, content, answer_attachment_ids, raised_at, answered_at
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.loadReviews(ctx, out)
}

// loadReviews fills each answered ping's review comments, in one query.
func (s *Store) loadReviews(ctx context.Context, vs []run.PingView) error {
	var ids []string
	at := map[string]int{}
	for i := range vs {
		if vs[i].Status == "answered" {
			ids = append(ids, vs[i].PingID)
			at[vs[i].PingID] = i
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT ping_id, path, start_line, end_line, heading, quote, body, attachment_ids
		FROM ping_review_comments WHERE ping_id = ANY($1) ORDER BY ping_id, idx`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var c event.ReviewComment
		var att []byte
		if err := rows.Scan(&id, &c.Path, &c.StartLine, &c.EndLine, &c.Heading, &c.Quote, &c.Body, &att); err != nil {
			return err
		}
		if len(att) > 0 {
			_ = json.Unmarshal(att, &c.AttachmentIDs)
		}
		v := &vs[at[id]]
		v.Review = append(v.Review, c)
	}
	return rows.Err()
}

// GetPing reads one projected request/response pair by id.
func (s *Store) GetPing(ctx context.Context, id string) (run.PingView, error) {
	v, err := scanPing(s.pool.QueryRow(ctx, `
		SELECT ping_id, run_id, type, stage_id, question, context, files, link,
		       form, answer_kind, phase, ends_phase, status, answer, feedback, content, answer_attachment_ids, raised_at, answered_at
		FROM pings WHERE ping_id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return run.PingView{}, fmt.Errorf("ping %s: %w", id, ErrPingNotFound)
	}
	if err != nil {
		return v, err
	}
	one := []run.PingView{v}
	err = s.loadReviews(ctx, one)
	return one[0], err
}

// ErrPingNotFound is the projection-read miss sentinel.
var ErrPingNotFound = errors.New("ping not found")

func scanPing(row pgx.Row) (run.PingView, error) {
	var v run.PingView
	var form, files, content, ansIDs []byte
	var link sql.NullString
	err := row.Scan(&v.PingID, &v.RunID, &v.Type, &v.StageID, &v.Question, &v.Context,
		&files, &link, &form, &v.AnswerKind, &v.Phase, &v.EndsPhase, &v.Status, &v.Answer, &v.Feedback, &content, &ansIDs, &v.RaisedAt, &v.AnsweredAt)
	if err == nil {
		if len(form) > 0 {
			_ = json.Unmarshal(form, &v.Form)
		}
		if len(content) > 0 {
			_ = json.Unmarshal(content, &v.Content)
		}
		if v.AnswerKind == "" {
			v.AnswerKind = (&event.PingRaised{Type: v.Type}).Kind()
		}
		if len(files) > 0 {
			_ = json.Unmarshal(files, &v.Files)
		}
		if len(ansIDs) > 0 {
			_ = json.Unmarshal(ansIDs, &v.AnswerAttachmentIDs)
		}
		v.Link = link.String
		v.PayloadRef = run.Primary(v.Files, v.Link)
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
