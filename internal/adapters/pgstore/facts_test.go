package pgstore

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"apothecary/internal/adapters/pgstore/pgtest"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

func TestNeedsRebuild(t *testing.T) {
	for sql, want := range map[string]bool{
		"-- rebuild: projections\nALTER TABLE runs ADD x int;": true,
		"-- a comment\n  -- rebuild: projections  \nSELECT 1;": true,
		"ALTER TABLE x ADD y int;":                             false,
		"-- rebuild: projections, later\nSELECT 1;":            false,
		"SELECT '-- rebuild: projections';":                    false,
		"":                                                     false,
	} {
		if got := needsRebuild([]byte(sql)); got != want {
			t.Errorf("needsRebuild(%q) = %v, want %v", sql, got, want)
		}
	}
}

// A migration without the header applies without touching the projections;
// one with it replays them from the log.
func TestMigrateRebuildsOnlyWhenAsked(t *testing.T) {
	dsn := pgtest.Empty(t)
	ctx := context.Background()
	s := openDSN(t, dsn)
	if _, err := s.Append(ctx, "r1", &event.RunCreated{TicketTitle: "T"}); err != nil {
		t.Fatal(err)
	}
	stale := func(s *Store, version int) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, fmt.Sprintf(`UPDATE runs SET ticket_title='stale'; DELETE FROM schema_migrations WHERE version=%d`, version)); err != nil {
			t.Fatal(err)
		}
	}
	title := func(s *Store) string {
		t.Helper()
		var got string
		if err := s.pool.QueryRow(ctx, `SELECT ticket_title FROM runs WHERE run_id='r1'`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	stale(s, 21) // 0021_ticket_tracker: no header
	s.Close()
	s = openDSN(t, dsn)
	if got, want := ledger(t, s), shipped(t); !slices.Equal(got, want) {
		t.Fatalf("ledger = %v, want %v", got, want)
	}
	if got := title(s); got != "stale" {
		t.Errorf("a migration without the header rebuilt the projections (title %q)", got)
	}
	stale(s, 22) // 0022_run_analytics: the header
	s.Close()
	if got := title(openDSN(t, dsn)); got != "T" {
		t.Errorf("a migration with the header left the projections alone (title %q)", got)
	}
}

// factsLog appends the run of run.TestFactsOfARun one event at a time, the
// clock at each event's minute.
func appendFactsLog(t *testing.T, s *Store, runID string, t0 time.Time) {
	t.Helper()
	steps := []struct {
		min int
		p   event.Payload
	}{
		{0, &event.RunCreated{UserID: "u1", ProjectID: "p1", TicketID: "t1", TicketChangeType: "feat", FlowName: "f", FlowHash: "h",
			By: "u1", TicketSourceKind: "jira", Skillsets: []event.SkillsetPin{{ID: "k1", Name: "kit", Seq: 2, Hash: "kh"}}}},
		{1, &event.EnvRequested{}},
		{3, &event.EnvReady{EnvID: "env-1"}},
		{3, &event.StageStarted{StageID: "build", Iteration: 1, Harness: "claude", Model: "m1"}},
		{5, &event.PingRaised{PingID: runID + "-p1", Type: event.PingClarification, StageID: "build"}},
		{9, &event.PingAnswered{PingID: runID + "-p1", Answer: "accept"}},
		{10, &event.StageFailed{StageID: "build", Reason: event.ReasonExitNonzero, TurnsUsed: 2, Usage: &event.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.2}}},
		{10, &event.EdgeTaken{From: "build", To: "build", Edge: "on_fail", Count: 1, Max: 3}},
		{10, &event.StageStarted{StageID: "build", Iteration: 2, Harness: "claude", Model: "m1"}},
		{11, &event.HoldTaken{StageID: "build", Iteration: 2, UserID: "u1"}},
		{13, &event.HoldReleased{StageID: "build", Iteration: 2}},
		{15, &event.StageCompleted{StageID: "build", TurnsUsed: 3, Usage: &event.Usage{InputTokens: 20, OutputTokens: 7, CostUSD: 0.3}}},
		{15, &event.RunCompleted{}},
	}
	for _, st := range steps {
		s.Clock = func() time.Time { return t0.Add(time.Duration(st.min) * time.Minute) }
		if _, err := s.Append(context.Background(), runID, st.p); err != nil {
			t.Fatal(err)
		}
	}
}

// snapshot is every analytics row as JSON, in key order: what a rebuild
// must reproduce exactly.
func snapshot(t *testing.T, s *Store) [3]string {
	t.Helper()
	var out [3]string
	for i, q := range []string{
		`SELECT coalesce(jsonb_agg(to_jsonb(f) ORDER BY run_id), '[]')::text FROM run_facts f`,
		`SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY run_id, seq), '[]')::text FROM stage_attempts a`,
		`SELECT coalesce(jsonb_agg(to_jsonb(k) ORDER BY run_id, skillset_id), '[]')::text FROM run_skillsets k`,
	} {
		if err := s.pool.QueryRow(context.Background(), q).Scan(&out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestRunFactsProjection(t *testing.T) {
	s := openDSN(t, pgtest.Empty(t))
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	appendFactsLog(t, s, "r1", t0)

	const m = int64(time.Minute / time.Millisecond)
	var (
		userID, ticketID, tracker, state, changeType                string
		wall, queued, prov, active, waiting, held, paused, response int64
		cost, rework                                                float64
		attempts, failed, retries, stages, turns, pings, unwanted   int
		success, firstPass                                          bool
		endedAt                                                     *time.Time
	)
	if err := s.pool.QueryRow(ctx, `SELECT user_id, ticket_id, tracker_kind, state, ticket_change_type,
			wall_ms, queued_ms, provisioning_ms, active_ms, waiting_human_ms, held_ms, paused_ms, human_response_ms,
			cost_usd, rework_cost_usd, attempts, failed_attempts, retries, stages, turns, pings, pings_unwanted,
			success, first_pass, ended_at FROM run_facts`).Scan(
		&userID, &ticketID, &tracker, &state, &changeType,
		&wall, &queued, &prov, &active, &waiting, &held, &paused, &response,
		&cost, &rework, &attempts, &failed, &retries, &stages, &turns, &pings, &unwanted,
		&success, &firstPass, &endedAt); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"user", userID, "u1"}, {"ticket", ticketID, "t1"}, {"tracker", tracker, "jira"}, {"state", state, "completed"},
		{"change type", changeType, "feat"},
		{"wall", wall, 15 * m}, {"queued", queued, 1 * m}, {"provisioning", prov, 2 * m}, {"active", active, 6 * m},
		{"waiting", waiting, 4 * m}, {"held", held, 2 * m}, {"paused", paused, int64(0)}, {"response", response, 4 * m},
		{"cost", cost, 0.5}, {"rework", rework, 0.3}, {"attempts", attempts, 2}, {"failed", failed, 1}, {"retries", retries, 1},
		{"stages", stages, 1}, {"turns", turns, 5}, {"pings", pings, 1}, {"unwanted", unwanted, 1},
		{"success", success, true}, {"first pass", firstPass, false},
		{"ended", endedAt != nil && endedAt.Equal(t0.Add(15*time.Minute)), true},
	} {
		if c.got != c.want {
			t.Errorf("run_facts.%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	rows, err := s.pool.Query(ctx, `SELECT seq, iteration, outcome, failure_reason, cost_usd, active_ms, waiting_ms, user_id, flow_hash
		FROM stage_attempts ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	type attempt struct {
		seq             int64
		iteration       int
		outcome, reason string
		cost            float64
		active, waiting int64
		user, flowHash  string
	}
	var got []attempt
	for rows.Next() {
		var a attempt
		if err := rows.Scan(&a.seq, &a.iteration, &a.outcome, &a.reason, &a.cost, &a.active, &a.waiting, &a.user, &a.flowHash); err != nil {
			t.Fatal(err)
		}
		got = append(got, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []attempt{
		{4, 1, run.OutcomeFailed, event.ReasonExitNonzero, 0.2, 3 * m, 4 * m, "u1", "h"},
		{9, 2, run.OutcomeCompleted, "", 0.3, 3 * m, 2 * m, "u1", "h"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("stage_attempts = %+v\nwant %+v", got, want)
	}
	var kit string
	if err := s.pool.QueryRow(ctx, `SELECT name || ' v' || seq || ' ' || hash || ' ' || user_id FROM run_skillsets WHERE run_id='r1' AND skillset_id='k1'`).Scan(&kit); err != nil || kit != "kit v2 kh u1" {
		t.Errorf("run_skillsets = %q, %v", kit, err)
	}

	// The incremental fold and a full replay land on the same rows.
	before := snapshot(t, s)
	n, err := s.RebuildProjections(ctx)
	if err != nil || n != 1 {
		t.Fatalf("rebuild = %d, %v", n, err)
	}
	if after := snapshot(t, s); after != before {
		t.Errorf("rebuild changed the analytics rows:\nbefore %v\nafter  %v", before, after)
	}
}

// Rebuild streams the log run by run: every run comes back, each folded
// from its own events only.
func TestRebuildProjectsEveryRun(t *testing.T) {
	s := openDSN(t, pgtest.Empty(t))
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	for _, id := range []string{"r1", "r2", "r3"} {
		appendFactsLog(t, s, id, t0)
	}
	before := snapshot(t, s)
	views, err := s.RunViews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.RebuildProjections(ctx)
	if err != nil || n != 3 {
		t.Fatalf("rebuild = %d, %v", n, err)
	}
	if after := snapshot(t, s); after != before {
		t.Errorf("rebuild changed the analytics rows:\nbefore %v\nafter  %v", before, after)
	}
	again, err := s.RunViews(ctx)
	if err != nil || len(again) != len(views) {
		t.Fatalf("views after rebuild = %d, %v", len(again), err)
	}
	for _, v := range again {
		if v.Facts.Attempts != 2 || v.Facts.CostUSD != 0.5 {
			t.Errorf("%s facts after rebuild = %+v", v.RunID, v.Facts)
		}
	}
}

func TestListRunViewsScopesInSQL(t *testing.T) {
	s := openDSN(t, pgtest.Empty(t))
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	for i, r := range []struct {
		id string
		c  event.RunCreated
	}{
		{"a1", event.RunCreated{UserID: "u1", ProjectID: "p1", TicketID: "t1"}},
		{"b1", event.RunCreated{UserID: "u2", ProjectID: "p1", TicketID: "t1"}},
		{"a2", event.RunCreated{UserID: "u1", ProjectID: "p2", TicketID: "t2"}},
		{"a3", event.RunCreated{UserID: "u1", ProjectID: "p1", TicketID: "t2"}},
	} {
		s.Clock = func() time.Time { return t0.Add(time.Duration(i) * time.Minute) }
		if _, err := s.Append(ctx, r.id, &r.c); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		f    run.ListFilter
		want []string
	}{
		{run.ListFilter{UserID: "u1"}, []string{"a3", "a2", "a1"}},
		{run.ListFilter{UserID: "u1", ProjectID: "p1"}, []string{"a3", "a1"}},
		{run.ListFilter{UserID: "u1", TicketID: "t2"}, []string{"a3", "a2"}},
		{run.ListFilter{UserID: "u1", ProjectID: "p1", TicketID: "t1"}, []string{"a1"}},
		{run.ListFilter{UserID: "nobody"}, []string{}},
	} {
		vs, err := s.ListRunViews(ctx, c.f)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, v := range vs {
			ids = append(ids, v.RunID)
		}
		if !slices.Equal(ids, c.want) {
			t.Errorf("ListRunViews(%+v) = %v, want %v", c.f, ids, c.want)
		}
	}
	var _ run.ViewLister = s
}
