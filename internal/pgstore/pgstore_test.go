package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"apothecary/internal/artifacts"
	"apothecary/internal/event"
	"apothecary/internal/run"
)

// open connects to TEST_POSTGRES_DSN or skips. Each test gets a clean slate
// via TRUNCATE (the migration already ran on Open).
func open(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping Postgres store tests")
	}
	s, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.pool.Exec(context.Background(),
		`TRUNCATE events, runs, pings, channels, run_exposed_ports, artifacts`); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAppendEventsAndSeq(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	ev1, err := s.Append(ctx, "r1", &event.RunCreated{TicketTitle: "a", FlowName: "v1"}, &event.EnvRequested{})
	if err != nil {
		t.Fatal(err)
	}
	ev2, err := s.Append(ctx, "r1", &event.EnvReady{EnvID: "e1"})
	if err != nil {
		t.Fatal(err)
	}
	if ev1[0].Seq != 1 || ev1[1].Seq != 2 || ev2[0].Seq != 3 {
		t.Errorf("seqs = %d,%d,%d", ev1[0].Seq, ev1[1].Seq, ev2[0].Seq)
	}

	got, err := s.Events(ctx, "r1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Seq != 2 || got[1].Type != event.TypeEnvReady {
		t.Errorf("events after 1 = %+v", got)
	}
	// Payloads round-trip through jsonb with concrete types.
	if p, ok := got[1].Payload.(*event.EnvReady); !ok || p.EnvID != "e1" {
		t.Errorf("payload = %#v", got[1].Payload)
	}

	ids, _ := s.RunIDs(ctx)
	if len(ids) != 1 || ids[0] != "r1" {
		t.Errorf("run ids = %v", ids)
	}
}

func TestRunsProjectionUpdatedInTx(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	_, err := s.Append(ctx, "r1",
		&event.RunCreated{TicketTitle: "Add login", FlowName: "v1", FlowHash: "h"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: "env-9"},
		&event.StageStarted{StageID: "plan", Iteration: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	views, err := s.RunViews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("views = %+v", views)
	}
	v := views[0]
	if v.State != run.Running || v.TicketTitle != "Add login" || v.CurrentStage != "plan" || v.EnvID != "env-9" {
		t.Errorf("projection = %+v", v)
	}

	// Incremental update across a second append.
	if _, err := s.Append(ctx, "r1", &event.RunCompleted{}); err != nil {
		t.Fatal(err)
	}
	views, _ = s.RunViews(ctx)
	if views[0].State != run.Completed {
		t.Errorf("state after complete = %s", views[0].State)
	}
}

func TestPingsProjection(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	_, err := s.Append(ctx, "r1",
		&event.RunCreated{TicketTitle: "T"},
		&event.PingRaised{PingID: "p1", Type: "clarification", Question: "db?", Context: "pg or sqlite", StageID: "build"},
	)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPings(ctx, "pending", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].PingID != "p1" || pending[0].Context != "pg or sqlite" || pending[0].StageID != "build" {
		t.Errorf("pending = %+v", pending)
	}

	if _, err := s.Append(ctx, "r1", &event.PingAnswered{PingID: "p1", Answer: "text", Feedback: "pg"}); err != nil {
		t.Fatal(err)
	}
	pending, _ = s.ListPings(ctx, "pending", 0)
	if len(pending) != 0 {
		t.Errorf("pending after answer = %+v", pending)
	}
	// The pair is durable (PLAN4 M15): answered listing + by-id fetch.
	answered, _ := s.ListPings(ctx, "answered", 0)
	if len(answered) != 1 || answered[0].Answer != "text" || answered[0].Feedback != "pg" || answered[0].AnsweredAt == nil {
		t.Errorf("answered = %+v", answered)
	}
	one, err := s.GetPing(ctx, "p1")
	if err != nil || one.Status != "answered" {
		t.Errorf("GetPing = %+v, %v", one, err)
	}
	if _, err := s.GetPing(ctx, "nope"); !errors.Is(err, ErrPingNotFound) {
		t.Errorf("missing ping err = %v", err)
	}
}

func TestWatchReplaysThenStreams(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := s.Append(ctx, "r1", &event.RunCreated{}, &event.EnvRequested{}); err != nil {
		t.Fatal(err)
	}
	ch, err := s.Watch(ctx, "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if e := <-ch; e.Type != event.TypeRunCreated {
		t.Errorf("replay[0] = %s", e.Type)
	}
	if e := <-ch; e.Type != event.TypeEnvRequested {
		t.Errorf("replay[1] = %s", e.Type)
	}
	if _, err := s.Append(ctx, "r1", &event.EnvReady{EnvID: "e"}); err != nil {
		t.Fatal(err)
	}
	if e := <-ch; e.Type != event.TypeEnvReady || e.Seq != 3 {
		t.Errorf("live = %s seq %d", e.Type, e.Seq)
	}
	cancel()
	for range ch {
	}
}

func TestRebuildProjections(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	for i := range 3 {
		id := fmt.Sprintf("r%d", i)
		if _, err := s.Append(ctx, id,
			&event.RunCreated{TicketTitle: id},
			&event.PingRaised{PingID: "p-" + id, Type: "blocker", Question: "q"},
		); err != nil {
			t.Fatal(err)
		}
	}
	// Corrupt the projections, then rebuild from events.
	if _, err := s.pool.Exec(ctx, `TRUNCATE runs, pings`); err != nil {
		t.Fatal(err)
	}
	n, err := s.RebuildProjections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("rebuilt %d runs, want 3", n)
	}
	views, _ := s.RunViews(ctx)
	pending, _ := s.ListPings(ctx, "pending", 0)
	if len(views) != 3 || len(pending) != 3 {
		t.Errorf("after rebuild: %d views, %d pings", len(views), len(pending))
	}
}

func TestChannelsCRUD(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.SaveChannel(ctx, "c1", "matrix",
		map[string]any{"homeserver": "https://m.local", "room_id": "!r:m.local"}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.LoadChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Kind != "matrix" || rows[0].Config["room_id"] != "!r:m.local" {
		t.Errorf("channels = %+v", rows)
	}
	if err := s.DeleteChannel(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.LoadChannels(ctx)
	if len(rows) != 0 {
		t.Errorf("after delete = %+v", rows)
	}
}

func TestArtifactsCRUD(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	mk := func(id string) artifacts.Meta {
		return artifacts.Meta{ID: id, Scope: artifacts.ScopeOrphan, Filename: id + ".txt", Mime: "text/plain", Size: 3, SHA256: "abc"}
	}
	for _, id := range []string{"a1", "a2", "a3"} {
		if err := s.InsertArtifact(ctx, mk(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AssociateArtifacts(ctx, []string{"a1"}, "run1", "", artifacts.ScopeGoal); err != nil {
		t.Fatal(err)
	}
	if err := s.AssociateArtifacts(ctx, []string{"a2"}, "run1", "ping1", artifacts.ScopePingAnswer); err != nil {
		t.Fatal(err)
	}
	// Only orphans are claimed: re-homing a1 to another run is a silent no-op.
	if err := s.AssociateArtifacts(ctx, []string{"a1"}, "run2", "", artifacts.ScopeGoal); err != nil {
		t.Fatal(err)
	}
	if forRun, _ := s.ArtifactsForRun(ctx, "run1"); len(forRun) != 2 {
		t.Errorf("ForRun(run1) = %d, want 2", len(forRun))
	}
	if forRun, _ := s.ArtifactsForRun(ctx, "run2"); len(forRun) != 0 {
		t.Errorf("a1 was wrongly re-homed to run2: %d", len(forRun))
	}
	forPing, _ := s.ArtifactsForPing(ctx, "ping1", artifacts.ScopePingAnswer)
	if len(forPing) != 1 || forPing[0].ID != "a2" {
		t.Errorf("ForPing = %+v", forPing)
	}
	if got, err := s.GetArtifact(ctx, "a1"); err != nil || got.RunID != "run1" || got.Scope != artifacts.ScopeGoal {
		t.Errorf("GetArtifact(a1) = %+v, %v", got, err)
	}
	if _, err := s.GetArtifact(ctx, "nope"); err != artifacts.ErrNotFound {
		t.Errorf("GetArtifact(nope) err = %v, want ErrNotFound", err)
	}
	// a3 is still an orphan.
	if orphans, _ := s.OrphanArtifactIDsBefore(ctx, time.Now().Add(time.Hour)); len(orphans) != 1 || orphans[0] != "a3" {
		t.Errorf("orphans = %v, want [a3]", orphans)
	}
	// DeleteArtifactsForRun returns the ids (for blob cleanup) and drops them.
	if del, _ := s.DeleteArtifactsForRun(ctx, "run1"); len(del) != 2 {
		t.Errorf("deleted = %v, want 2", del)
	}
	if forRun, _ := s.ArtifactsForRun(ctx, "run1"); len(forRun) != 0 {
		t.Errorf("run1 artifacts survived delete: %d", len(forRun))
	}
}

func TestExposedPortsCRUD(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.SaveExposedPort(ctx, "run1", 3000, 41001); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveExposedPort(ctx, "run1", 5432, 41002); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveExposedPort(ctx, "run2", 8080, 41003); err != nil {
		t.Fatal(err)
	}
	// Upsert: same (run, ws_port) updates the host port.
	if err := s.SaveExposedPort(ctx, "run1", 3000, 41099); err != nil {
		t.Fatal(err)
	}
	all, err := s.LoadExposedPorts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if all["run1"][3000] != 41099 || all["run1"][5432] != 41002 || all["run2"][8080] != 41003 {
		t.Errorf("loaded = %+v", all)
	}
	// Delete is per-run.
	if err := s.DeleteExposedPorts(ctx, "run1"); err != nil {
		t.Fatal(err)
	}
	all, _ = s.LoadExposedPorts(ctx)
	if _, ok := all["run1"]; ok {
		t.Errorf("run1 not pruned: %+v", all)
	}
	if all["run2"][8080] != 41003 {
		t.Errorf("run2 must survive run1 delete: %+v", all)
	}
}
