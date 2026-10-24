package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"apothecary/internal/builtin"
	"apothecary/internal/confstore"
	"apothecary/internal/event"
	"apothecary/internal/filestore"
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
		`TRUNCATE events, runs, pings, channels, run_exposed_ports, attachments, snapshots, tickets CASCADE`); err != nil {
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

// The projection row must agree with a full reduce after any sequence of
// appends: that needs every field Apply reads to round-trip through the row —
// including Dormant and Rebuilding, which the JSON view omits.
func TestRunsProjectionRoundTripsWhatApplyReads(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	steps := [][]event.Payload{
		{&event.RunCreated{TicketTitle: "t", ProjectID: "p", Branch: "feat/x", TicketID: "tk-1"},
			&event.EnvRequested{}, &event.EnvReady{EnvID: "env-1"},
			&event.StageStarted{StageID: "build", Iteration: 1}},
		{&event.PingRaised{PingID: "q1", Type: "blocker", Question: "which db?"}},
		// The engine parks the run: no loop, waiting on the human.
		{&event.RunPaused{StageID: "build", Reason: event.PauseWaiting}},
		// The answer lands while it is parked: it must keep reading
		// waiting_on_human (the row's dormant flag is what Apply consults).
		{&event.PingAnswered{PingID: "q1", Answer: "postgres"}},
		{&event.RunUnpaused{Reason: event.UnpauseAnswered}, &event.StageCompleted{StageID: "build"}, &event.RunCompleted{}},
		{&event.EnvRebuildStarted{EnvID: "env-1"}},
		{&event.EnvRebuildFailed{EnvID: "env-1", Error: "interrupted"}},
	}
	var all []event.Event
	for i, ps := range steps {
		evs, err := s.Append(ctx, "r1", ps...)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, evs...)
		want := run.Reduce(all)
		views, err := s.RunViews(ctx)
		if err != nil || len(views) != 1 {
			t.Fatalf("step %d: views = %+v, %v", i, views, err)
		}
		got := views[0]
		if got.State != want.State || got.Dormant != want.Dormant || got.Rebuilding != want.Rebuilding ||
			got.Branch != want.Branch || got.TicketID != want.TicketID || got.CurrentStage != want.CurrentStage {
			t.Errorf("step %d: projection %s/dormant=%v/rebuilding=%v/%s/%s/%s, reduce %s/dormant=%v/rebuilding=%v/%s/%s/%s",
				i, got.State, got.Dormant, got.Rebuilding, got.Branch, got.TicketID, got.CurrentStage,
				want.State, want.Dormant, want.Rebuilding, want.Branch, want.TicketID, want.CurrentStage)
		}
	}
	if views, _ := s.RunViews(ctx); views[0].State != run.Completed || views[0].Rebuilding {
		t.Errorf("final row = %+v", views[0])
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
			&event.SnapshotCaptured{StageID: "build", Iteration: 1, Kind: "log",
				SnapshotID: "sn-" + id, Filename: "build-1.log", Mime: "text/plain",
				SHA256: "ee55", Size: 9},
		); err != nil {
			t.Fatal(err)
		}
	}
	// Corrupt the projections, then rebuild from events. Snapshots are a
	// projection too (DESIGN-FILES §3.3): the rows come back from the log,
	// re-pointing at the untouched blobs by sha.
	if _, err := s.pool.Exec(ctx, `TRUNCATE runs, pings, snapshots`); err != nil {
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
	snaps, _ := s.SnapshotsForRun(ctx, "r0")
	if len(snaps) != 1 || snaps[0].ID != "sn-r0" || snaps[0].Kind != filestore.KindLog {
		t.Errorf("snapshots after rebuild = %+v, want the captured log back", snaps)
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

// mustSeedProjectAndTicket satisfies the tickets FK chain for attachment
// claims (attachments.ticket_id REFERENCES tickets).
func mustSeedProjectAndTicket(t *testing.T, s *Store, ticketID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := s.UpsertIntegration(ctx, confstore.StoredIntegration{
		ID: "fint1", Port: "repository", Kind: "gitlab", Name: "gl-files",
		Config: map[string]any{}, Sealed: []byte{1}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProject(ctx, confstore.Project{
		ID: "fprj1", Name: "files-prj", RepoAccountID: "fint1", ProvisionerID: "fint1",
		HarnessIDs: []string{"fint1"}, DefaultFlow: builtin.DefaultFlowName,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTicket(ctx, confstore.Ticket{
		ID: ticketID, ProjectID: "fprj1", Title: "T", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAttachmentsCRUD(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	// The FK backstop needs a real ticket row for ticket claims.
	mustSeedProjectAndTicket(t, s, "tick1")

	mk := func(id string) filestore.Attachment {
		return filestore.Attachment{ID: id, Filename: id + ".txt", Mime: "text/plain",
			Size: 3, SHA256: "ab12", CreatedAt: time.Now().UTC()}
	}
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		if err := s.InsertAttachment(ctx, mk(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ClaimAttachments(ctx, []string{"a1"}, "", "run1", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimAttachments(ctx, []string{"a2"}, "", "run1", "ping1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimAttachments(ctx, []string{"a4"}, "tick1", "", ""); err != nil {
		t.Fatal(err)
	}
	// Claims are one-shot: re-homing a1 to another run is a silent no-op.
	if err := s.ClaimAttachments(ctx, []string{"a1"}, "", "run2", ""); err != nil {
		t.Fatal(err)
	}
	if forRun, _ := s.AttachmentsForRun(ctx, "run1"); len(forRun) != 2 {
		t.Errorf("ForRun(run1) = %d, want 2", len(forRun))
	}
	if forRun, _ := s.AttachmentsForRun(ctx, "run2"); len(forRun) != 0 {
		t.Errorf("a1 was wrongly re-homed to run2: %d", len(forRun))
	}
	if forPing, _ := s.AttachmentsForPing(ctx, "ping1"); len(forPing) != 1 || forPing[0].ID != "a2" {
		t.Errorf("ForPing = %+v", forPing)
	}
	if forTick, _ := s.AttachmentsForTicket(ctx, "tick1"); len(forTick) != 1 || forTick[0].ID != "a4" {
		t.Errorf("ForTicket = %+v", forTick)
	}
	if got, err := s.GetAttachment(ctx, "a1"); err != nil || got.RunID != "run1" || got.TicketID != "" {
		t.Errorf("GetAttachment(a1) = %+v, %v", got, err)
	}
	if _, err := s.GetAttachment(ctx, "nope"); !errors.Is(err, filestore.ErrNotFound) {
		t.Errorf("GetAttachment(nope) err = %v, want ErrNotFound", err)
	}
	// a3 is still unclaimed.
	if orphans, _ := s.UnclaimedAttachmentIDsBefore(ctx, time.Now().Add(time.Hour)); len(orphans) != 1 || orphans[0] != "a3" {
		t.Errorf("unclaimed = %v, want [a3]", orphans)
	}
	// Live sha set covers every row (blob sweep mark set).
	if live, _ := s.LiveBlobSHAs(ctx, time.Time{}); !live["ab12"] {
		t.Errorf("live shas = %v", live)
	}
	if err := s.DeleteAttachmentsForRun(ctx, "run1"); err != nil {
		t.Fatal(err)
	}
	if forRun, _ := s.AttachmentsForRun(ctx, "run1"); len(forRun) != 0 {
		t.Errorf("run1 attachments survived delete: %d", len(forRun))
	}
	// The ticket-delete leak fix at the SQL layer.
	if err := s.DeleteAttachmentsForTicket(ctx, "tick1"); err != nil {
		t.Fatal(err)
	}
	if forTick, _ := s.AttachmentsForTicket(ctx, "tick1"); len(forTick) != 0 {
		t.Errorf("ticket attachments survived delete: %d", len(forTick))
	}
}

func TestSnapshotsCRUDAndProjection(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	sn := filestore.Snapshot{
		ID: "sn1", RunID: "run1", Kind: filestore.KindOutput, StageID: "plan",
		Iteration: 1, Name: "plan", Path: "artifacts/plan.md",
		Filename: "plan.md", Mime: "text/markdown", Size: 6, SHA256: "cd34",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.InsertSnapshot(ctx, sn); err != nil {
		t.Fatal(err)
	}
	// The CHECKs refuse incoherent attributions at the schema.
	bad := sn
	bad.ID, bad.Kind = "sn-bad", filestore.KindLog // a log with name+path
	if err := s.InsertSnapshot(ctx, bad); err == nil {
		t.Error("schema accepted a log with an output role")
	}
	pf := filestore.Snapshot{
		ID: "sn2", RunID: "run1", Kind: filestore.KindPingFile, StageID: "plan",
		PingID: "ping1", Path: "artifacts/plan.md", Filename: "plan.md",
		Mime: "text/markdown", Size: 6, SHA256: "cd34", CreatedAt: time.Now().UTC(),
	}
	if err := s.InsertSnapshot(ctx, pf); err != nil {
		t.Fatal(err)
	}
	if forRun, _ := s.SnapshotsForRun(ctx, "run1"); len(forRun) != 2 {
		t.Errorf("ForRun = %d, want 2", len(forRun))
	}
	if forPing, _ := s.SnapshotsForPing(ctx, "ping1"); len(forPing) != 1 || forPing[0].ID != "sn2" {
		t.Errorf("ForPing = %+v", forPing)
	}
	// Retention cutoff drops old snapshot shas from the mark set.
	if live, _ := s.LiveBlobSHAs(ctx, time.Now().Add(time.Hour)); live["cd34"] {
		t.Errorf("expired snapshot sha still live: %v", live)
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
