package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

// teardownEnvp is the fake provisioner with a destroy the test scripts: it
// can block until released (then fail if its context was cancelled, as a
// kubectl killed with the request does), fail from a queue, and record the
// egress prunes it was asked for.
type teardownEnvp struct {
	*fake.EnvProvisioner
	started chan struct{} // closed when Destroy is entered (nil = no block)
	release chan struct{}
	once    sync.Once

	mu     sync.Mutex
	errs   []error // Destroy's answers in order; nil entries succeed
	prunes []prune
}

type prune struct {
	project string
	live    map[envport.EnvID]bool
}

func (p *teardownEnvp) Destroy(ctx context.Context, id envport.EnvID) error {
	if p.started != nil {
		p.once.Do(func() { close(p.started) })
		<-p.release
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	p.mu.Lock()
	var err error
	if len(p.errs) > 0 {
		err, p.errs = p.errs[0], p.errs[1:]
	}
	p.mu.Unlock()
	if err != nil {
		return err
	}
	return p.EnvProvisioner.Destroy(ctx, id)
}

func (p *teardownEnvp) PruneEgress(ctx context.Context, project string, live func(context.Context) (map[envport.EnvID]bool, error)) ([]envport.EnvID, error) {
	l, err := live(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prunes = append(p.prunes, prune{project, l})
	return []envport.EnvID{"apo-orphan"}, nil
}

func sentOfType(h *harness, ty string) []notifyport.Notification {
	var out []notifyport.Notification
	for _, n := range h.ch.Sent() {
		if n.Type == ty {
			out = append(out, n)
		}
	}
	return out
}

// A client that goes away mid-archive (a cancelled request, a page left) must
// not cut the destroy short: everything after the pod would be stranded, the
// env counted against the cap forever.
func TestArchiveDestroyOutlivesTheRequest(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	id := completedWithEnv(t, h)
	env := h.view(t, id).EnvID
	td := &teardownEnvp{EnvProvisioner: h.envp, started: make(chan struct{}), release: make(chan struct{})}
	h.res.Envp = td

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.e.Archive(ctx, id) }()
	<-td.started
	cancel()
	close(td.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	evs := h.events(t, id)
	if countType(evs, event.TypeEnvDestroyed) != 1 || countType(evs, event.TypeEnvDestroyFailed) != 0 {
		t.Fatalf("events = %v", eventTypes(evs))
	}
	if v := h.view(t, id); v.State != run.Completed || v.ArchivedAt == nil || v.EnvID != "" {
		t.Errorf("view = %s env %q", v.State, v.EnvID)
	}
	if len(h.envp.Destroyed) != 1 || string(h.envp.Destroyed[0]) != env {
		t.Errorf("destroyed = %v", h.envp.Destroyed)
	}
}

// A destroy that fails is on the run's timeline and with its owner — once,
// not on every retry — and is retried: by archiving again (which reports the
// failure) and by the reaper, until it goes.
func TestArchiveDestroyFailureIsRecordedAndRetried(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	id := completedWithEnv(t, h)
	stuck := errors.New("delete pod: exit -1")
	td := &teardownEnvp{EnvProvisioner: h.envp, errs: []error{stuck, stuck, stuck}}
	h.res.Envp = td

	if err := h.e.Archive(ctx, id); err != nil {
		t.Fatalf("archive itself succeeded, its destroy is the timeline's: %v", err)
	}
	v := h.view(t, id)
	if v.State != run.Completed || v.ArchivedAt == nil || v.EnvID == "" || !strings.Contains(v.EnvDestroyError, "exit -1") {
		t.Fatalf("view = %s env %q error %q", v.State, v.EnvID, v.EnvDestroyError)
	}
	sent := sentOfType(h, string(event.TypeEnvDestroyFailed))
	if len(sent) != 1 || sent[0].RunID != id || !strings.Contains(sent[0].Body, "exit -1") {
		t.Fatalf("owner notified = %+v", sent)
	}

	// Archiving again is the manual retry, and says why it failed.
	wantErrText(t, h.e.Archive(ctx, id), "exit -1")
	// The reaper retries an archived run's env with no window.
	if err := h.e.ReapOnce(ctx, Window(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if evs := h.events(t, id); countType(evs, event.TypeEnvDestroyFailed) != 1 || countType(evs, event.TypeEnvDestroyed) != 0 {
		t.Errorf("a failure streak is recorded once: %v", eventTypes(evs))
	}
	if n := len(sentOfType(h, string(event.TypeEnvDestroyFailed))); n != 1 {
		t.Errorf("owner notified %d times for one streak", n)
	}

	if err := h.e.ReapOnce(ctx, Window(time.Hour)); err != nil {
		t.Fatal(err)
	}
	v = h.view(t, id)
	if v.EnvID != "" || v.EnvDestroyError != "" {
		t.Fatalf("after the reaper's retry: env %q error %q", v.EnvID, v.EnvDestroyError)
	}
	if countType(h.events(t, id), event.TypeEnvDestroyed) != 1 {
		t.Errorf("events = %v", eventTypes(h.events(t, id)))
	}
	wantErrText(t, h.e.Archive(ctx, id), "is already archived")
}

// A completed run's reap that failed is retried on the next pass, not a
// whole window later (its env_destroy_failed moved the run's UpdatedAt).
func TestReaperRetriesAFailedReapAtOnce(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	id := completedWithEnv(t, h)
	h.res.Envp = &teardownEnvp{EnvProvisioner: h.envp, errs: []error{errors.New("pvc stuck")}}

	time.Sleep(5 * time.Millisecond)
	if err := h.e.ReapOnce(ctx, Window(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if v := h.view(t, id); v.EnvID == "" || v.EnvDestroyError == "" {
		t.Fatalf("after the failed reap: env %q error %q", v.EnvID, v.EnvDestroyError)
	}
	if err := h.e.ReapOnce(ctx, Window(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if v := h.view(t, id); v.EnvID != "" || v.State != run.Completed {
		t.Fatalf("not retried inside the window: env %q state %s", v.EnvID, v.State)
	}
}

// An archived run's leftover workspace is reclaimed on the next pass whatever
// the run ended as, with no window: archiving keeps the outcome, and the
// reaper reads the flag, not the state.
func TestReaperReclaimsArchivedLeftoversWhateverTheOutcome(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ends := map[string]event.Payload{
		"completed": &event.RunCompleted{},
		"abandoned": &event.RunAbandoned{Reason: "edge exhausted"},
		"failed":    &event.RunFailed{Reason: "aborted"},
	}
	for name, end := range ends {
		h.seed(t, name, created(testProject), &event.EnvReady{EnvID: "e-" + name}, end, &event.RunArchived{})
	}
	h.seed(t, "fresh", created(testProject), &event.EnvReady{EnvID: "e-fresh"}, &event.RunCompleted{})
	if err := h.e.ReapOnce(ctx, Window(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for name := range ends {
		if v := h.view(t, name); v.EnvID != "" || v.State != run.State(name) {
			t.Errorf("%s: env %q state %s", name, v.EnvID, v.State)
		}
	}
	if v := h.view(t, "fresh"); v.EnvID != "e-fresh" {
		t.Errorf("a completed run inside its window was reaped: env %q", v.EnvID)
	}
}

// The cap's message never tells anyone to archive what already is: an
// archived run whose workspace is left is a cleanup pending, said apart.
func TestEnvironmentCapacityNamesPendingCleanup(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.settings.L.MaxEnvironments = 2
	h.seed(t, "finished", created(testProject), &event.EnvReady{EnvID: "e1"}, &event.RunCompleted{})
	h.seed(t, "stranded", created(testProject), &event.EnvReady{EnvID: "e2"}, &event.RunCompleted{},
		&event.RunArchived{}, &event.EnvDestroyFailed{EnvID: "e2", Error: "boom"})
	_, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "T"}, "loop", testProject)
	if !errors.Is(err, ErrEnvCapacity) {
		t.Fatalf("err = %v", err)
	}
	wantErrText(t, err, "archivable runs: finished; workspaces pending cleanup")
	if strings.Contains(err.Error(), "archivable runs: finished, stranded") || !strings.Contains(err.Error(), "stranded") {
		t.Errorf("err = %v", err)
	}

	// Only stranded workspaces: no archiving advice at all.
	h = newHarness(t, mustFlow(t, loopFlowDoc))
	h.settings.L.MaxEnvironments = 1
	h.seed(t, "stranded", created(testProject), &event.EnvReady{EnvID: "e2"}, &event.RunFailed{Reason: "x"}, &event.RunArchived{})
	_, err = h.e.CreateRun(ctx, runinput.Ticket{Title: "T"}, "loop", testProject)
	wantErrText(t, err, "(1 live, max 1); workspaces pending cleanup")
	if strings.Contains(err.Error(), "archivable") {
		t.Errorf("advised archiving an archived run: %v", err)
	}
	if sent := sentOfType(h, "blocker"); len(sent) != 1 || strings.Contains(sent[0].Body, "archive finished runs") {
		t.Errorf("notified = %+v", sent)
	}
}

// Each reaper pass prunes the project's gateway, and what it keeps is every
// recorded workspace plus the workspace of every run not over (one being
// provisioned is on the gateway before its env_ready).
func TestReaperPrunesTheGatewaysOrphans(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	td := &teardownEnvp{EnvProvisioner: h.envp}
	h.res.Envp = td
	h.seed(t, "kept", created(testProject), &event.EnvReady{EnvID: "e-kept"}, &event.RunCompleted{})
	h.seed(t, "provisioning", created(testProject), &event.EnvRequested{})
	h.seed(t, "gone", created(testProject), &event.EnvReady{EnvID: "e-gone"}, &event.RunCompleted{},
		&event.RunArchived{}, &event.EnvDestroyed{EnvID: "e-gone"})
	h.seed(t, "legacy", created(""), &event.RunCompleted{})

	if err := h.e.ReapOnce(ctx, Window(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(td.prunes) != 1 || td.prunes[0].project != testProject {
		t.Fatalf("prunes = %+v (one per project with runs)", td.prunes)
	}
	live := td.prunes[0].live
	provisioning := envport.EnvID(workspaceName(h.view(t, "provisioning")))
	gone := envport.EnvID(workspaceName(h.view(t, "gone")))
	if !live["e-kept"] || !live[provisioning] || live["e-gone"] || live[gone] {
		t.Errorf("live = %v (want e-kept and %s, not the destroyed run's)", live, provisioning)
	}
}

// namingEnvp records the workspace name each provisioning asked for.
type namingEnvp struct {
	*fake.EnvProvisioner
	names []string
}

func (p *namingEnvp) Provision(ctx context.Context, spec envport.EnvSpec) (envport.EnvID, error) {
	p.names = append(p.names, spec.Name)
	return p.EnvProvisioner.Provision(ctx, spec)
}

// The workspace name the prune keeps for a provisioning run is the one its
// provisioning asks for.
func TestWorkspaceNameIsTheProvisionedOne(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	np := &namingEnvp{EnvProvisioner: h.envp}
	h.res.Envp = np
	id := completedWithEnv(t, h)
	if got := workspaceName(h.view(t, id)); len(np.names) != 1 || got != np.names[0] {
		t.Errorf("workspaceName = %q, provisioned as %v", got, np.names)
	}
	legacy := run.View{RunID: "01ABCDEFGHJKMNPQRSTVWXYZ00", Branch: "feat/x"}
	if workspaceName(legacy) == "" {
		t.Error("a run with no branch stem has no workspace name")
	}
}
