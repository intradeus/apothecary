package engine

// The ticket world of the engine tests: a config store holding the tickets a
// run's tracker actions resolve, and the trackers they resolve to — the fake
// remote tracker for a ticket with a source, the real internal tracker for
// one without — wired into a resolution as the daemon's resolver does
// (integrations.Resolver.TicketTracker / TicketStep).

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/confstore/conftest"
	"apothecary/internal/project/resolution"
	"apothecary/internal/project/tickets/internaltracker"
)

// remoteIntegration is the fake tracker's integration id, linkedTicket's
// source.
const remoteIntegration = "int-1"

type ticketWorld struct {
	t      *testing.T
	conf   *confstore.Service
	remote *fake.TicketSource
	// noAuto: a ticket the world does not hold is a deleted one (the
	// resolver's ErrNotApplicable), not a hand-written one made on demand.
	noAuto bool
}

// newTicketWorld holds linkedTicket (tk-1, DEV-1 on remote) and the remote's
// status list, each flow step on its stock status. Any other ticket a run
// names is made a hand-written one on first resolution.
func newTicketWorld(t *testing.T, remote *fake.TicketSource) *ticketWorld {
	t.Helper()
	ctx := context.Background()
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	w := &ticketWorld{t: t, conf: confstore.New(confstore.NewMem(), key), remote: remote}
	conftest.SeedProject(t, w.conf, confstore.Project{ID: testProject, Name: "test",
		Repo: confstore.RepoConfig{URL: "https://gitlab.local/g/p.git", DefaultBranch: "main"}})
	if err := w.conf.DB().UpsertIntegration(ctx, confstore.StoredIntegration{ID: remoteIntegration,
		Port: confstore.PortTicketSource, Kind: "fake", Name: "tracker", Config: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.conf.EnsureRemoteStatuses(ctx, remoteIntegration, fake.DefaultStatuses); err != nil {
		t.Fatal(err)
	}
	w.steps(map[string]trackerport.FlowStep{"To Do": trackerport.StepTodo, "In Progress": trackerport.StepInProgress,
		"In Review": trackerport.StepInReview, "Done": trackerport.StepDone})
	w.linked(linkedTicket.TicketID, linkedTicket.SourceKey)
	return w
}

// steps gives the remote's statuses (by name, added when missing) these
// steps, and every other status none.
func (w *ticketWorld) steps(byName map[string]trackerport.FlowStep) {
	w.t.Helper()
	ctx := context.Background()
	list, err := w.conf.TrackerStatuses(ctx, remoteIntegration)
	if err != nil {
		w.t.Fatal(err)
	}
	var missing []trackerport.RemoteStatus
	for name := range byName {
		if !slices.ContainsFunc(list, func(s confstore.TrackerStatus) bool { return s.Name == name }) {
			missing = append(missing, trackerport.RemoteStatus{ID: "id-" + name, Name: name})
		}
	}
	if list, err = w.conf.EnsureRemoteStatuses(ctx, remoteIntegration, missing); err != nil {
		w.t.Fatal(err)
	}
	for i := range list {
		list[i].FlowStep = byName[list[i].Name]
	}
	if _, _, err := w.conf.PutTrackerStatuses(ctx, remoteIntegration, list, nil); err != nil {
		w.t.Fatal(err)
	}
}

// linked records a ticket mirroring the remote's issue key.
func (w *ticketWorld) linked(id, key string) {
	w.t.Helper()
	if err := w.conf.DB().UpsertTicket(context.Background(), confstore.Ticket{ID: id, ProjectID: testProject, Title: "T",
		SourceID: remoteIntegration, SourceKind: "fake", SourceKey: key, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		w.t.Fatal(err)
	}
}

// handWritten records a ticket with no source: on the internal tracker, in
// To do.
func (w *ticketWorld) handWritten(id string) confstore.Ticket {
	w.t.Helper()
	t, err := w.newHandWritten(id)
	if err != nil {
		w.t.Fatal(err)
	}
	return t
}

func (w *ticketWorld) newHandWritten(id string) (confstore.Ticket, error) {
	ctx := context.Background()
	in, err := w.conf.InternalTracker(ctx, "")
	if err != nil {
		return confstore.Ticket{}, err
	}
	todo, err := w.status(in.ID, trackerport.StepTodo)
	if err != nil {
		return confstore.Ticket{}, err
	}
	t := confstore.Ticket{ID: id, ProjectID: testProject, Title: "T", StatusID: todo.ID, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	return t, w.conf.DB().UpsertTicket(ctx, t)
}

func (w *ticketWorld) status(integrationID string, step trackerport.FlowStep) (confstore.TrackerStatus, error) {
	list, err := w.conf.TrackerStatuses(context.Background(), integrationID)
	if err != nil {
		return confstore.TrackerStatus{}, err
	}
	for _, s := range list {
		if s.FlowStep == step {
			return s, nil
		}
	}
	return confstore.TrackerStatus{}, errors.New("no status holds " + string(step))
}

// ticket reads a ticket's row.
func (w *ticketWorld) ticket(id string) confstore.Ticket {
	w.t.Helper()
	t, err := w.conf.GetTicket(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	return t
}

// statusName is the name of a ticket's status.
func (w *ticketWorld) statusName(id string) string {
	w.t.Helper()
	t := w.ticket(id)
	trackerID, err := w.conf.TicketTrackerID(context.Background(), t)
	if err != nil {
		w.t.Fatal(err)
	}
	list, _ := w.conf.TrackerStatuses(context.Background(), trackerID)
	for _, s := range list {
		if s.ID == t.StatusID {
			return s.Name
		}
	}
	return ""
}

// tracker is Resolution.TicketTracker over the world.
func (w *ticketWorld) tracker(ctx context.Context, ticketID string) (trackerport.TicketWriter, string, error) {
	t, err := w.conf.GetTicket(ctx, ticketID)
	if errors.Is(err, confstore.ErrNotFound) && w.noAuto {
		return nil, "", &trackerport.ErrNotApplicable{Key: ticketID, Reason: "the ticket was deleted"}
	}
	if errors.Is(err, confstore.ErrNotFound) {
		t, err = w.newHandWritten(ticketID)
	}
	if err != nil {
		return nil, "", err
	}
	if t.SourceID == "" {
		return internaltracker.New(w.conf), t.ID, nil
	}
	return w.remote, t.SourceKey, nil
}

// step is Resolution.TicketStep over the world.
func (w *ticketWorld) step(ctx context.Context, ticketID string, step trackerport.FlowStep) (string, error) {
	st, err := w.conf.StepStatus(ctx, ticketID, step)
	return st.Name, err
}

// wire makes res resolve tickets through the world.
func (w *ticketWorld) wire(res *resolution.Resolution) *ticketWorld {
	res.TicketTracker, res.TicketStep = w.tracker, w.step
	return w
}
