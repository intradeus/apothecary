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
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"

	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	pool *pgxpool.Pool

	mu   sync.Mutex
	subs map[string][]*sub

	// Clock stamps events (UTC); injectable for tests.
	Clock func() time.Time

	// metrics are the store's own instruments (metrics.go).
	metrics storeMetrics
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
// A migration whose file carries the `-- rebuild: projections` header also
// rebuilds the projections from the event log: a schema change can add a
// projection column (or table) that every existing row lacks, and the
// projections are derived data by design — rebuildable from events alone.
// It costs one pass over every run's events, once, at the boot that applies
// such a migration; a migration that touches no projection opts out by
// leaving the header off.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}
	// Every statement is a span under whatever request or run issued it.
	cfg.ConnConfig.Tracer = queryTracer{tracer: otel.Tracer("apothecary/pgstore")}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}
	s := &Store{pool: pool, subs: map[string][]*sub{}, Clock: time.Now, metrics: newStoreMetrics()}
	_, rebuild, err := s.migrate(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	if rebuild {
		if _, err := s.RebuildProjections(ctx); err != nil {
			pool.Close()
			return nil, fmt.Errorf("rebuild projections after migrating: %w", err)
		}
	}
	s.registerMetrics()
	return s, nil
}

func (s *Store) Close() {
	if s.metrics.reg != nil {
		_ = s.metrics.reg.Unregister()
	}
	s.pool.Close()
}

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

// rebuildHeader marks a migration that changes what the projections hold:
// applying it replays them from the log (needsRebuild).
const rebuildHeader = "-- rebuild: projections"

// needsRebuild reports whether a migration's SQL carries rebuildHeader on a
// line of its own.
func needsRebuild(sql []byte) bool {
	for line := range strings.Lines(string(sql)) {
		if strings.TrimSpace(line) == rebuildHeader {
			return true
		}
	}
	return false
}

// migrate applies the pending migrations, reporting how many it applied and
// whether any of them asked for a projections rebuild.
func (s *Store) migrate(ctx context.Context) (applied int, rebuild bool, err error) {
	migs, err := migrations()
	if err != nil {
		return 0, false, err
	}

	// Advisory lock serializes concurrently booting instances — without it,
	// two boots can both pass the applied-version check and race the DDL.
	lockConn, err := s.pool.Acquire(ctx)
	if err != nil {
		return 0, false, err
	}
	defer lockConn.Release()
	const migLockKey = int64(0x61706f7468656361) // "apotheca"
	if _, err := lockConn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migLockKey); err != nil {
		return 0, false, fmt.Errorf("migration lock: %w", err)
	}
	defer func() { _, _ = lockConn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migLockKey) }()

	if _, err := s.pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return 0, false, fmt.Errorf("migrations table: %w", err)
	}
	done, err := s.appliedMigrations(ctx)
	if err != nil {
		return 0, false, err
	}
	if squashed(done) {
		if _, err := s.pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version > 1`); err != nil {
			return 0, false, fmt.Errorf("collapse pre-squash migrations: %w", err)
		}
		done = map[int]bool{1: true}
	}

	// A version this binary doesn't ship means the schema was written by a
	// newer apothecaryd (or a pre-squash chain applied only partway): refuse
	// rather than run against tables we don't know.
	known := map[int]bool{}
	for _, m := range migs {
		known[m.version] = true
	}
	for v := range done {
		if !known[v] {
			return 0, false, fmt.Errorf("database has migration %d applied, which this apothecaryd does not ship — upgrade apothecaryd, or reset a dev database", v)
		}
	}

	for _, m := range migs {
		if done[m.version] {
			continue
		}
		sql, err := migrationsFS.ReadFile("migrations/" + m.name)
		if err != nil {
			return applied, rebuild, err
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return applied, rebuild, err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return applied, rebuild, fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, m.version); err != nil {
			_ = tx.Rollback(ctx)
			return applied, rebuild, err
		}
		if err := tx.Commit(ctx); err != nil {
			return applied, rebuild, err
		}
		applied++
		rebuild = rebuild || needsRebuild(sql)
	}
	return applied, rebuild, nil
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
	defer s.metrics.appendTook(time.Now())
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
	// timestamptz keeps microseconds: the events Append hands back (and folds
	// into the projections) carry the time a read will, so an incremental
	// fold and a rebuild agree to the millisecond.
	ts := s.Clock().UTC().Truncate(time.Microsecond)
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
	if err := s.projectTx(ctx, tx, runID, out, nil); err != nil {
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

// projectTx folds the new events into the projections inside the append
// transaction (§6): runs, pings, snapshots and the run analytics (run_facts,
// stage_attempts, run_skillsets). v is the view the events fold onto; nil
// loads the stored row (Append), a fresh view replays a whole log (rebuild).
// Every statement goes out in one batch: one round trip per append, however
// many rows it touches.
func (s *Store) projectTx(ctx context.Context, tx pgx.Tx, runID string, evs []event.Event, v *run.View) error {
	if v == nil {
		loaded, _, err := loadView(ctx, tx, runID)
		if err != nil {
			return err
		}
		v = &loaded
	}
	factBearing, created := false, false
	for i := range evs {
		run.Apply(v, &evs[i])
		factBearing = factBearing || run.FactBearing(evs[i].Type)
		created = created || evs[i].Type == event.TypeRunCreated
	}
	ended := v.Facts.Ended
	v.Facts.Ended = nil
	// The row carries the whole view as JSON next to the columns SQL filters
	// on; Dormant and Rebuilding are not in the JSON (json:"-") and get their
	// own columns — Apply reads both, so they must round-trip for the
	// incremental fold to agree with a full reduce.
	view, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b := &pgx.Batch{}
	b.Queue(`
		INSERT INTO runs (run_id, project_id, state, ticket_title, flow_name, flow_hash, current_stage, mr_url, env_id, created_at, updated_at, view, dormant, rebuilding, user_id, ticket_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (run_id) DO UPDATE SET
			project_id=$2, state=$3, ticket_title=$4, flow_name=$5, flow_hash=$6,
			current_stage=$7, mr_url=$8, env_id=$9, created_at=$10, updated_at=$11,
			view=$12, dormant=$13, rebuilding=$14, user_id=$15, ticket_id=$16`,
		v.RunID, v.ProjectID, string(v.State), v.TicketTitle, v.FlowName, v.FlowHash,
		v.CurrentStage, v.MRURL, v.EnvID, v.CreatedAt, v.UpdatedAt, view, v.Dormant, v.Rebuilding, v.UserID, v.TicketID)
	for i := range evs {
		switch p := evs[i].Payload.(type) {
		case *event.PingRaised:
			var form []byte
			if p.Form != nil {
				form, _ = json.Marshal(p.Form)
			}
			var files, fileTypes []byte
			if len(p.Files) > 0 {
				files, _ = json.Marshal(p.Files)
			}
			if len(p.FileTypes) > 0 {
				fileTypes, _ = json.Marshal(p.FileTypes)
			}
			b.Queue(`
				INSERT INTO pings (ping_id, run_id, type, question, context, files, link, stage_id, form, answer_kind, phase, ends_phase, status, raised_at, user_id, file_types)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'pending',$13,$14,$15)
				ON CONFLICT (ping_id) DO NOTHING`,
				p.PingID, runID, p.Type, p.Question, p.Context, files, p.Link, p.StageID, form, p.Kind(), p.Phase, p.EndsPhase, evs[i].TS, v.UserID, fileTypes)
		case *event.PingAnswered:
			var ids, content, curated []byte
			if len(p.AttachmentIDs) > 0 {
				ids, _ = json.Marshal(p.AttachmentIDs)
			}
			if len(p.Content) > 0 {
				content, _ = json.Marshal(p.Content)
			}
			if len(p.Curated) > 0 {
				curated, _ = json.Marshal(p.Curated)
			}
			b.Queue(`
				UPDATE pings SET status='answered', answer=$2, feedback=$3, content=$4, answer_attachment_ids=$5, answered_at=$6, curated=$7 WHERE ping_id=$1`,
				p.PingID, p.Answer, p.Feedback, content, ids, evs[i].TS, curated)
			for j, c := range p.Review {
				var att []byte
				if len(c.AttachmentIDs) > 0 {
					att, _ = json.Marshal(c.AttachmentIDs)
				}
				b.Queue(`
					INSERT INTO ping_review_comments (ping_id, run_id, idx, path, start_line, end_line, heading, quote, body, attachment_ids)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
					ON CONFLICT (ping_id, idx) DO NOTHING`,
					p.PingID, runID, j, c.Path, c.StartLine, c.EndLine, c.Heading, c.Quote, c.Body, att)
			}
		case *event.SnapshotCaptured:
			// The snapshots table is a PROJECTION of this event (DESIGN-FILES
			// §3.3). On the live path the row already exists — Capture inserts
			// it in the same logical operation and this no-ops on the id — so
			// this case is what makes rebuild-projections able to reconstruct
			// the table from the log alone (a row whose event never landed
			// disappears on rebuild, which is the correct reading of a crash
			// between capture and append).
			b.Queue(`
				INSERT INTO snapshots (id, run_id, kind, stage_id, iteration, ping_id, name, path, filename, mime, size, sha256, truncated, created_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
				ON CONFLICT (id) DO NOTHING`,
				p.SnapshotID, runID, p.Kind, p.StageID, p.Iteration,
				nullStr(p.PingID), nullStr(p.Name), nullStr(p.Path),
				p.Filename, p.Mime, p.Size, p.SHA256, p.Truncated, evs[i].TS)
		case *event.RunAbandoned, *event.RunFailed, *event.RunArchived, *event.RunCompleted:
			// Terminal run ⇒ withdraw any still-open ping: its live handler is
			// gone, so it can never be answered. Keeps the inbox from showing a
			// dead request (parity with run.ReducePings). The view's open pings
			// are exactly the pending rows (an answer drops one from both), so
			// each is found by key. No status predicate: given one, the planner
			// scans pings_status_idx, which inside a rebuild's transaction holds
			// every pending entry it has superseded — quadratic in the pings.
			for _, id := range v.OpenPings {
				b.Queue(`UPDATE pings SET status='withdrawn' WHERE ping_id=$1 AND run_id=$2`, id, runID)
			}
		}
	}
	if factBearing {
		queueFacts(b, v, ended)
	}
	if created {
		for _, pin := range v.Skillsets {
			b.Queue(`INSERT INTO run_skillsets (run_id, skillset_id, user_id, name, seq, hash)
				VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`,
				v.RunID, pin.ID, v.UserID, pin.Name, pin.Seq, pin.Hash)
		}
	}
	br := tx.SendBatch(ctx, b)
	for range b.Len() {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return err
		}
	}
	return br.Close()
}

// runFactsCols and attemptCols are the analytics projections' columns, in
// the order queueFacts binds them.
var (
	runFactsCols = []string{"run_id", "user_id", "project_id", "ticket_id", "ticket_change_type", "tracker_kind",
		"flow_name", "flow_hash", "started_by", "state", "end_reason", "stalled_at", "created_at", "ended_at",
		"wall_ms", "queued_ms", "provisioning_ms", "active_ms", "waiting_human_ms", "held_ms", "paused_ms", "human_response_ms",
		"cost_usd", "rework_cost_usd", "input_tokens", "output_tokens", "turns", "stages", "attempts", "failed_attempts",
		"retries", "try_agains", "pings", "pings_unwanted", "pings_answered", "gates_rejected", "holds", "mrs_created",
		"success", "first_pass"}
	attemptCols = []string{"run_id", "seq", "user_id", "project_id", "flow_name", "flow_hash", "ticket_change_type",
		"stage_id", "iteration", "harness", "model", "agent", "agent_hash", "outcome", "failure_reason",
		"started_at", "ended_at", "active_ms", "waiting_ms", "cost_usd", "input_tokens", "output_tokens", "turns", "pings"}
	runFactsUpsert = upsertSQL("run_facts", 1, runFactsCols)
	attemptUpsert  = upsertSQL("stage_attempts", 2, attemptCols)
)

// upsertSQL is an INSERT of cols that overwrites every non-key column on
// conflict with the first nKey columns.
func upsertSQL(table string, nKey int, cols []string) string {
	ph := make([]string, len(cols))
	set := make([]string, 0, len(cols)-nKey)
	for i, c := range cols {
		ph[i] = fmt.Sprintf("$%d", i+1)
		if i >= nKey {
			set = append(set, c+"=EXCLUDED."+c)
		}
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO UPDATE SET %s",
		table, strings.Join(cols, ", "), strings.Join(ph, ","), strings.Join(cols[:nKey], ", "), strings.Join(set, ", "))
}

// queueFacts writes the run's facts row and a row per attempt that ended
// in this batch or is still in flight.
func queueFacts(b *pgx.Batch, v *run.View, ended []run.AttemptFacts) {
	f := &v.Facts
	success := v.State == run.Completed
	firstPass := success && f.FailedAttempts == 0 && f.Retries == 0 && f.PingsUnwanted == 0
	b.Queue(runFactsUpsert, textArgs(
		v.RunID, v.UserID, v.ProjectID, v.TicketID, v.TicketChangeType, f.TrackerKind,
		v.FlowName, v.FlowHash, v.StartedBy, string(v.State), f.EndReason, f.StalledAt, v.CreatedAt, f.EndedAt,
		f.WallMS(), f.QueuedMS, f.ProvisioningMS, f.ActiveMS, f.WaitingHumanMS, f.HeldMS, f.PausedMS, f.HumanResponseMS,
		f.CostUSD, f.ReworkCostUSD, f.InputTokens, f.OutputTokens, f.Turns, len(f.Stages), f.Attempts, f.FailedAttempts,
		f.Retries, f.TryAgains, f.Pings, f.PingsUnwanted, f.PingsAnswered, f.GatesRejected, f.Holds, f.MRsCreated,
		success, firstPass)...)
	attempt := func(a *run.AttemptFacts) {
		b.Queue(attemptUpsert, textArgs(
			v.RunID, a.Seq, v.UserID, v.ProjectID, v.FlowName, v.FlowHash, v.TicketChangeType,
			a.StageID, a.Iteration, a.Harness, a.Model, a.Agent, a.AgentHash, a.Outcome, a.FailureReason,
			a.StartedAt, a.EndedAt, a.ActiveMS, a.WaitingMS, a.CostUSD, a.InputTokens, a.OutputTokens, a.Turns, a.Pings)...)
	}
	for i := range ended {
		attempt(&ended[i])
	}
	for _, id := range slices.Sorted(maps.Keys(f.Open)) {
		attempt(f.Open[id])
	}
}

// textArgs makes every string argument storable as text, the way the view's
// JSON already does: free text from a payload (a failure reason) can hold
// invalid UTF-8, which becomes U+FFFD, or a NUL, which text cannot hold.
func textArgs(args ...any) []any {
	for i, a := range args {
		if s, ok := a.(string); ok {
			args[i] = strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
		}
	}
	return args
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
// companions and the owner. The typed columns exist for SQL to filter on,
// not to be read back — the JSON is the row — with one exception: user_id
// is the authority when the JSON has none, so an owner set on the column
// (a hand-run assignment) holds without a projection rebuild.
const viewColumns = `run_id, view, dormant, rebuilding, user_id`

func scanView(row pgx.Row) (run.View, error) {
	var v run.View
	var id, owner string
	var view []byte
	if err := row.Scan(&id, &view, &v.Dormant, &v.Rebuilding, &owner); err != nil {
		return v, err
	}
	if err := json.Unmarshal(view, &v); err != nil {
		return v, fmt.Errorf("decode runs.view for %s: %w", id, err)
	}
	if v.UserID == "" {
		v.UserID = owner
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
		e, err := decodeEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// decodeEvent reads one `run_id, seq, ts, type, payload` row.
func decodeEvent(rows pgx.Rows) (event.Event, error) {
	var e event.Event
	var typ string
	var payload []byte
	if err := rows.Scan(&e.RunID, &e.Seq, &e.TS, &typ, &payload); err != nil {
		return e, err
	}
	e.Type = event.Type(typ)
	p, err := event.UnmarshalPayload(e.Type, payload)
	if err != nil {
		return e, fmt.Errorf("run %s seq %d: %w", e.RunID, e.Seq, err)
	}
	e.Payload = p
	return e, nil
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

// RebuildProjections truncates every projection (runs, pings, snapshots,
// the run analytics) and replays every run's events through the same
// projection code the append path uses. Snapshot BLOBS are untouched —
// rebuilt rows re-point at them by sha (DESIGN-FILES §3.3).
//
// The log is read as one stream in (run_id, seq) order on its own
// connection, each run projected as its last row goes by: one query for the
// whole log rather than one per run, and one batch per run. Reading and
// decoding run on their own goroutine, so the next runs are decoded while
// the database executes this one's batch.
func (s *Store) RebuildProjections(ctx context.Context) (int, error) {
	defer s.metrics.rebuildTook(time.Now())
	start := time.Now()
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT run_id) FROM events`).Scan(&total); err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `TRUNCATE runs, pings, ping_review_comments, snapshots, run_facts, stage_attempts, run_skillsets`); err != nil {
		return 0, err
	}
	readCtx, cancel := context.WithCancel(ctx)
	runs := make(chan []event.Event, 64)
	var readErr error // set before runs closes
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		defer close(done)
		defer close(runs)
		readErr = s.streamRuns(readCtx, runs)
	}()
	n := 0
	for evs := range runs {
		if err := s.projectTx(ctx, tx, evs[0].RunID, evs, &run.View{}); err != nil {
			return 0, fmt.Errorf("rebuild %s: %w", evs[0].RunID, err)
		}
		n++
		if n%1000 == 0 {
			slog.Default().Info("rebuilding projections", "runs", n, "of", total)
		}
	}
	if readErr != nil {
		return 0, readErr
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	slog.Default().Info("projections rebuilt", "runs", n, "duration_ms", time.Since(start).Milliseconds())
	return n, nil
}

// streamRuns reads the whole log in (run_id, seq) order and sends each run's
// events as one slice.
func (s *Store) streamRuns(ctx context.Context, out chan<- []event.Event) error {
	rows, err := s.pool.Query(ctx, `SELECT run_id, seq, ts, type, payload FROM events ORDER BY run_id, seq`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var evs []event.Event
	send := func() error {
		select {
		case out <- evs:
			evs = nil
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for rows.Next() {
		e, err := decodeEvent(rows)
		if err != nil {
			return err
		}
		if len(evs) > 0 && evs[0].RunID != e.RunID {
			if err := send(); err != nil {
				return err
			}
		}
		evs = append(evs, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(evs) == 0 {
		return nil
	}
	return send()
}

// RunView reads one run's row (run.ViewLookup): the API's ownership check
// on every run route, without reducing the log.
func (s *Store) RunView(ctx context.Context, runID string) (run.View, bool, error) {
	return loadView(ctx, s.pool, runID)
}

// RunByEnv reads the live run behind a workspace (run.EnvLookup): env_id is
// cleared when a workspace is destroyed, so at most one live run carries a
// name; the newest wins should a rebuild race leave two.
func (s *Store) RunByEnv(ctx context.Context, envID string) (run.View, bool, error) {
	if envID == "" {
		return run.View{}, false, nil
	}
	v, err := scanView(s.pool.QueryRow(ctx, `SELECT `+viewColumns+` FROM runs WHERE env_id=$1 ORDER BY created_at DESC LIMIT 1`, envID))
	if errors.Is(err, pgx.ErrNoRows) {
		return run.View{}, false, nil
	}
	if err != nil {
		return run.View{}, false, err
	}
	return v, true, nil
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

// ListRunViews reads one user's runs, optionally of one project and one
// ticket, newest first (run.ViewLister — the run list's backing).
func (s *Store) ListRunViews(ctx context.Context, f run.ListFilter) ([]run.View, error) {
	q := `SELECT ` + viewColumns + ` FROM runs WHERE user_id=$1`
	args := []any{f.UserID}
	if f.ProjectID != "" {
		args = append(args, f.ProjectID)
		q += fmt.Sprintf(` AND project_id=$%d`, len(args))
	}
	if f.TicketID != "" {
		args = append(args, f.TicketID)
		q += fmt.Sprintf(` AND ticket_id=$%d`, len(args))
	}
	rows, err := s.pool.Query(ctx, q+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []run.View{}
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

func (s *Store) SaveChannel(ctx context.Context, id, userID, kind string, config map[string]any) error {
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO channels (id, kind, config, user_id) VALUES ($1,$2,$3,$4)`, id, kind, data, userID)
	return err
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM channels WHERE id=$1`, id)
	return err
}

type ChannelRow struct {
	ID     string
	UserID string
	Kind   string
	Config map[string]any
}

func (s *Store) LoadChannels(ctx context.Context) ([]ChannelRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, user_id, kind, config FROM channels ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChannelRow
	for rows.Next() {
		var r ChannelRow
		var data []byte
		if err := rows.Scan(&r.ID, &r.UserID, &r.Kind, &data); err != nil {
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

const pingCols = `ping_id, run_id, user_id, type, stage_id, question, context, files, link,
	             form, answer_kind, phase, ends_phase, status, answer, feedback, content, answer_attachment_ids, raised_at, answered_at,
	             file_types, curated`

// ListPings reads the pings projection (PLAN4 M15): the durable
// request/response surface, one user's ("" = everyone's). status filters
// pending|answered; "" or "all" returns both. Newest first, capped by limit.
func (s *Store) ListPings(ctx context.Context, userID, status string, limit int) ([]run.PingView, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + pingCols + ` FROM pings`
	var where []string
	args := []any{}
	if userID != "" {
		args = append(args, userID)
		where = append(where, fmt.Sprintf("user_id=$%d", len(args)))
	}
	if status != "" && status != "all" {
		args = append(args, status)
		where = append(where, fmt.Sprintf("status=$%d", len(args)))
	}
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
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
	v, err := scanPing(s.pool.QueryRow(ctx, `SELECT `+pingCols+` FROM pings WHERE ping_id=$1`, id))
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
	var form, files, content, ansIDs, fileTypes, curated []byte
	var link sql.NullString
	err := row.Scan(&v.PingID, &v.RunID, &v.UserID, &v.Type, &v.StageID, &v.Question, &v.Context,
		&files, &link, &form, &v.AnswerKind, &v.Phase, &v.EndsPhase, &v.Status, &v.Answer, &v.Feedback, &content, &ansIDs, &v.RaisedAt, &v.AnsweredAt,
		&fileTypes, &curated)
	if err == nil {
		if len(form) > 0 {
			_ = json.Unmarshal(form, &v.Form)
		}
		if len(fileTypes) > 0 {
			_ = json.Unmarshal(fileTypes, &v.FileTypes)
		}
		if len(curated) > 0 {
			_ = json.Unmarshal(curated, &v.Curated)
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
