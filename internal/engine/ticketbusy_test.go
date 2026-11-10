package engine

import (
	"context"
	"errors"
	"sync"
	"testing"

	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

// Concurrent starts on one ticket: exactly one run is created, every other
// start is refused naming it — the lock holds from the check to run_created.
func TestConcurrentStartsOnOneTicketCreateOneRun(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	// The winner stays in provisioning, so it is active for the whole race.
	entered := make(chan struct{})
	h.res.Envp = blockProvision{h.envp, entered, ""}
	ctx := context.Background()

	const n = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ids  []string
		errs []error
	)
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, err := h.e.CreateRun(ctx, runinput.Ticket{TicketID: "tk-race", Title: "T"}, "loop", testProject)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			ids = append(ids, id)
		}()
	}
	close(start)
	wg.Wait()
	if len(ids) != 1 {
		t.Fatalf("%d runs created, want exactly 1 (errors: %v)", len(ids), errs)
	}
	for _, err := range errs {
		var busy ErrTicketBusy
		if !errors.As(err, &busy) || busy.RunID != ids[0] || busy.TicketID != "tk-race" {
			t.Errorf("refusal = %v, want ErrTicketBusy naming %s", err, ids[0])
		}
	}
	<-entered
	if err := h.e.Abort(ids[0]); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(ids[0])
}

// Only a non-terminal run holds its ticket: once it ended, the ticket takes
// a new run. Runs without a ticket never conflict.
func TestTicketBusyRule(t *testing.T) {
	ctx := context.Background()
	t.Run("an active run refuses another on its ticket", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.seed(t, "r-live", &event.RunCreated{TicketTitle: "T", FlowName: "loop", ProjectID: testProject, TicketID: "tk-1"})
		_, err := h.e.CreateRun(ctx, runinput.Ticket{TicketID: "tk-1", Title: "T"}, "loop", testProject)
		var busy ErrTicketBusy
		if !errors.As(err, &busy) || busy.RunID != "r-live" {
			t.Fatalf("CreateRun = %v, want ErrTicketBusy naming r-live", err)
		}
		if got := err.Error(); got != "ticket tk-1 is busy: run r-live is still active; abort it or wait for it to end" {
			t.Errorf("message = %q", got)
		}
		// Another ticket is not held by it.
		id, err := h.e.CreateRun(ctx, runinput.Ticket{TicketID: "tk-2", Title: "T"}, "loop", testProject)
		if err != nil {
			t.Fatal(err)
		}
		h.e.Wait(id)
	})
	t.Run("terminal runs do not hold the ticket", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.seed(t, "r-old", &event.RunCreated{TicketTitle: "T", FlowName: "loop", ProjectID: testProject, TicketID: "tk-1"},
			&event.RunAbandoned{StalledAt: "build", Reason: "gave up"})
		id, err := h.e.CreateRun(ctx, runinput.Ticket{TicketID: "tk-1", Title: "T"}, "loop", testProject)
		if err != nil {
			t.Fatalf("a ticket whose only run ended was refused: %v", err)
		}
		h.e.Wait(id)
		if s := h.view(t, id).State; !s.Terminal() {
			t.Fatalf("state = %s", s)
		}
		// Nor does the run just finished.
		again, err := h.e.CreateRun(ctx, runinput.Ticket{TicketID: "tk-1", Title: "T"}, "loop", testProject)
		if err != nil {
			t.Fatalf("second attempt after the first ended: %v", err)
		}
		h.e.Wait(again)
	})
	t.Run("runs without a ticket are exempt", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.seed(t, "r-direct", &event.RunCreated{TicketTitle: "T", FlowName: "loop", ProjectID: testProject})
		id, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "T"}, "loop", testProject)
		if err != nil {
			t.Fatalf("a ticketless run was refused: %v", err)
		}
		h.e.Wait(id)
	})
}

// stubTickets is a TicketReader over a fixed answer.
type stubTickets struct {
	t     runinput.Ticket
	now   runinput.TicketNow
	found bool
	err   error
}

func (s *stubTickets) RunTicket(context.Context, string) (runinput.Ticket, bool, error) {
	return s.t, s.found, s.err
}

func (s *stubTickets) TicketNow(context.Context, string) (runinput.TicketNow, bool, error) {
	return s.now, s.found, s.err
}

// A rerun starts from the ticket as it is now, not the old run's copy; a
// deleted ticket falls back to that copy, and a ticket that cannot be read
// refuses the rerun.
func TestRerunReadsTheCurrentTicket(t *testing.T) {
	tickets := &stubTickets{}
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Tickets = tickets }, mustFlow(t, loopFlowDoc))
	ctx := context.Background()
	id, err := h.e.CreateRun(ctx, runinput.Ticket{TicketID: "tk-r", Title: "Old title", Goal: "old goal", Branch: "feat/old"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)

	tickets.t, tickets.found = runinput.Ticket{
		TicketID: "tk-r", Title: "New title", Goal: "new goal", ChangeType: "fix", Branch: "fix/new",
		SourceID: "int-1", SourceKind: "fake", SourceKey: "DEV-9", SourceURL: "https://tracker.local/browse/DEV-9",
	}, true
	next, err := h.e.Rerun(ctx, id, runinput.Input{Goal: "this time"})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(next)
	v := h.view(t, next)
	if v.TicketID != "tk-r" || v.TicketTitle != "New title" || v.TicketGoal != "new goal" || v.TicketChangeType != "fix" ||
		v.BranchBase != "fix/new" || v.TicketSourceID != "int-1" || v.TicketKey != "DEV-9" || v.RunGoal != "this time" {
		t.Errorf("rerun ticket = %+v", v)
	}

	// Deleted since: the old run's copy.
	tickets.t, tickets.found = runinput.Ticket{}, false
	fallback, err := h.e.Rerun(ctx, next, runinput.Input{})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(fallback)
	if v := h.view(t, fallback); v.TicketID != "tk-r" || v.TicketTitle != "New title" || v.TicketKey != "DEV-9" {
		t.Errorf("rerun of a deleted ticket = %+v", v)
	}

	tickets.err = errors.New("db down")
	if _, err := h.e.Rerun(ctx, fallback, runinput.Input{}); err == nil || err.Error() != "ticket tk-r: db down" {
		t.Errorf("rerun with an unreadable ticket = %v", err)
	}
}

// A rerun is a run on the ticket like any other: refused while another run
// of the ticket is active.
func TestRerunRefusedWhileTheTicketIsBusy(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()
	id, err := h.e.CreateRun(ctx, runinput.Ticket{TicketID: "tk-b", Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	h.seed(t, "r-live", &event.RunCreated{TicketTitle: "T", FlowName: "loop", ProjectID: testProject, TicketID: "tk-b"})
	_, err = h.e.Rerun(ctx, id, runinput.Input{})
	var busy ErrTicketBusy
	if !errors.As(err, &busy) || busy.RunID != "r-live" {
		t.Fatalf("Rerun = %v, want ErrTicketBusy naming r-live", err)
	}
	if v := h.view(t, "r-live"); v.State != run.Pending {
		t.Errorf("the active run was touched: %s", v.State)
	}
}
