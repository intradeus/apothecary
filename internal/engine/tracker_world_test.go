package engine

// The ticket world of the engine tests: a config store holding the tickets a
// run's tracker actions resolve, and the trackers they resolve to — the fake
// remote tracker for a record of a remote issue, the real internal tracker
// for an internal ticket — wired into a resolution as the daemon's resolver
// does (integrations.Resolver.TicketTracker).

import (
	"context"
	"errors"
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
	// resolver's ErrNotApplicable), not an internal one made on demand.
	noAuto bool
}

// newTicketWorld holds linkedTicket (tk-1, a record of DEV-1 on remote). Any
// other ticket a run names is made an internal one on first resolution.
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
	w.linked(linkedTicket.TicketID, linkedTicket.SourceKey)
	return w
}

// linked records a remote issue by its key (the fake's stable id too).
func (w *ticketWorld) linked(id, key string) {
	w.t.Helper()
	if err := w.conf.DB().UpsertTicket(context.Background(), confstore.Ticket{ID: id, Title: "T",
		SourceID: remoteIntegration, SourceKind: "fake", SourceKey: key, RemoteID: key, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		w.t.Fatal(err)
	}
}

// handWritten holds an internal ticket: on the internal tracker, in To do.
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
	todo, err := w.status(in.ID, "To do")
	if err != nil {
		return confstore.Ticket{}, err
	}
	t := confstore.Ticket{ID: id, Title: "T", StatusID: todo.ID, SourceID: in.ID, SourceKind: confstore.KindInternal, RemoteID: id,
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	return t, w.conf.DB().UpsertTicket(ctx, t)
}

// status is the status of a tracker named name.
func (w *ticketWorld) status(integrationID, name string) (confstore.TrackerStatus, error) {
	list, err := w.conf.TrackerStatuses(context.Background(), integrationID)
	if err != nil {
		return confstore.TrackerStatus{}, err
	}
	for _, s := range list {
		if s.Name == name {
			return s, nil
		}
	}
	return confstore.TrackerStatus{}, errors.New("no status named " + name)
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
	if t.Internal() {
		return internaltracker.New(w.conf), t.ID, nil
	}
	return w.remote, t.SourceKey, nil
}

// wire makes res resolve tickets through the world.
func (w *ticketWorld) wire(res *resolution.Resolution) *ticketWorld {
	res.TicketTracker = w.tracker
	return w
}
