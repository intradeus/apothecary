// Package envstate is what is known about run workspaces beyond the event
// log: the reconciled live container status and "config changed" hint per
// run, and the per-run workspace-operation gate (provision/rebuild/destroy)
// whose in-flight op overlays the badge. All of it is external, memory-only
// state — never folded into a run's history — so it lives apart from the
// engine, which claims the gate and the API, which reads the badges.
package envstate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"time"

	"apothecary/internal/ports/envport"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
)

// op is a run's in-flight (or recently-failed) workspace operation.
type op struct {
	kind   string    // "provisioning" | "rebuilding" | "destroying"
	failed bool      // op ended in error; the entry lingers as the "error" badge
	at     time.Time // claim/failure time (failed-badge TTL sweep + diagnostics)
}

// ErrorTTL bounds how long a failed op shows the "error" badge before it
// ages out (mirrors observedTTL — a permanent error badge would be the exact
// stale-lie the reconciler comment warns against).
const ErrorTTL = 60 * time.Second

// ErrInFlight refuses a workspace op when another is already running on
// the same run's env.
var ErrInFlight = errors.New("another workspace operation is in progress")

// Begin claims the per-run workspace-op slot. It refuses with
// ErrInFlight when a non-failed op is already in flight; a lingering
// FAILED entry does not block (a retry overwrites it). The returned release
// records the terminal outcome — success clears the slot, failure leaves the
// "error" badge for ErrorTTL — and is idempotent (sync.Once), so a
// double release (e.g. defer + explicit) is harmless. Callers MUST pair it
// with an immediately-following `defer rel(err)` on a named error return.
func (s *Store) Begin(runID, kind string) (func(error), error) {
	s.opMu.Lock()
	if op := s.ops[runID]; op != nil && !op.failed {
		s.opMu.Unlock()
		return nil, fmt.Errorf("run %s: %w", runID, ErrInFlight)
	}
	s.ops[runID] = &op{kind: kind, at: s.now()}
	s.opMu.Unlock()

	var once sync.Once
	return func(opErr error) {
		once.Do(func() {
			s.opMu.Lock()
			defer s.opMu.Unlock()
			if opErr != nil {
				s.ops[runID] = &op{kind: kind, failed: true, at: s.now()}
			} else {
				delete(s.ops, runID)
			}
		})
	}, nil
}

// Store holds the reconciled statuses and the op gate.
type Store struct {
	Runs     *run.Reader
	Resolver resolution.Resolver
	// Clock stamps ops (the failed-badge TTL); nil is time.Now.
	Clock func() time.Time
	// Log is nil for slog.Default.
	Log *slog.Logger

	// status is the reconciled live container status per run (keyed by
	// run id), refreshed by Reconcile. It is deliberately NOT part of
	// the event-sourced projection: a container stopped/removed out-of-band
	// is external state, not a run-authored transition, so it is tracked in
	// memory and surfaced to the workspace badge, never folded into the log.
	// Empty for a run means "not yet reconciled" — the read path falls back
	// to the env_id-based liveness guess.
	statusMu sync.Mutex
	status   map[string]envport.EnvStatus
	// dirty is, per run, whether the workspace's build inputs changed
	// since its container was built (envport.WorkspaceDirtyChecker), refreshed
	// with status. Same discipline: external state, memory only.
	dirty map[string]bool

	// ops is the per-run in-flight workspace-operation gate (keyed by run id).
	// It serializes provision/rebuild/destroy on one env — the reaper,
	// reconciler and user actions otherwise race on the same workspace — and
	// surfaces the transitional badge (rebuilding/destroying) that wins over
	// the 30s docker-observed status. A CLAIM, not a held mutex: opMu is taken
	// only for O(1) map ops, NEVER across a Provision/Destroy/Rebuild/Status
	// call and NEVER nested with statusMu (or the engine's locks).
	// Memory-only like status: a crash mid-op leaves no stuck state
	// (re-appears as unreconciled).
	opMu sync.Mutex
	ops  map[string]*op
}

func New(runs *run.Reader, resolver resolution.Resolver, clock func() time.Time, log *slog.Logger) *Store {
	return &Store{
		Runs: runs, Resolver: resolver, Clock: clock, Log: log,
		status: map[string]envport.EnvStatus{},
		dirty:  map[string]bool{},
		ops:    map[string]*op{},
	}
}

func (s *Store) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Store) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Run reconciles at once and then every interval until ctx ends. A failed
// pass is logged; the next one tries again.
func (s *Store) Run(ctx context.Context, every time.Duration) {
	pass := func() {
		if err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
			s.log().Error("env reconcile pass failed", "err", err)
		}
	}
	pass()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			pass()
		case <-ctx.Done():
			return
		}
	}
}

// SetStatus records one run's status outside a reconcile pass (a targeted
// probe right after a rebuild, so the badge does not wait for the next pass).
func (s *Store) SetStatus(runID string, st envport.EnvStatus) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.status[runID] = st
}

// ClearDirty drops a run's "config changed" hint: its container was just
// built from the current inputs.
func (s *Store) ClearDirty(runID string) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	delete(s.dirty, runID)
}

// Reconcile refreshes the live container status of every run that
// still holds an env_id, by querying the provisioner (docker/kubernetes) rather
// than the event log. This is what lets the workspace badge tell "live" from
// "stopped/removed out-of-band" — the projection only knows an env was
// provisioned and not yet destroyed, never whether the container is actually
// up. The whole map is rebuilt each pass (runs whose env_id is gone drop out).
//
// A failed probe (Resolve or Status errored: provisioner unreachable, expired
// cluster credentials, deleted project) records EnvUnknown — NEVER the run's
// prior value. Carrying the last-known status forward turns a persistent
// failure into a permanent lie: a workspace last seen "running" before the
// credentials expired would stay green "live" forever. Honest "unknown" beats a
// stale "live"; a genuinely transient blip self-heals on the next 30s pass.
func (s *Store) Reconcile(ctx context.Context) error {
	views, err := s.Runs.All(ctx)
	if err != nil {
		return err
	}
	next := map[string]envport.EnvStatus{}
	dirty := map[string]bool{}
	// Runs with an op in flight (rebuild/destroy) are skipped: their docker
	// state is churning and the Badges overlay already covers the badge, so
	// probing would only manufacture a spurious EnvUnknown mid-op.
	s.opMu.Lock()
	inflight := make(map[string]bool, len(s.ops))
	for id, op := range s.ops {
		if !op.failed {
			inflight[id] = true
		}
	}
	s.opMu.Unlock()
	// Resolutions are cached per project within the pass — many runs share
	// one project, and Resolve is not free.
	resolved := map[string]*resolution.Resolution{}
	defer func() {
		for _, res := range resolved {
			res.Close()
		}
	}()
	for _, v := range views {
		id := v.RunID
		if inflight[id] {
			continue
		}
		// A run with no env_id has no workspace to report on; pre-v2 runs with
		// no project can't be resolved to a provisioner (§6.1).
		if v.EnvID == "" || v.ProjectID == "" {
			continue
		}
		res, ok := resolved[v.ProjectID]
		if !ok {
			res, err = s.Resolver.Resolve(ctx, v.ProjectID)
			if err != nil {
				s.log().Debug("env reconcile: resolve failed", "run", id, "project", v.ProjectID, "err", err)
				next[id] = envport.EnvUnknown
				continue
			}
			resolved[v.ProjectID] = res
		}
		st, err := res.Envp.Status(ctx, envport.EnvID(v.EnvID))
		if err != nil {
			s.log().Debug("env reconcile: status probe failed", "run", id, "env", v.EnvID, "err", err)
			next[id] = envport.EnvUnknown
			continue
		}
		next[id] = st
		// Same pass, same honesty: a failed check records nothing, so the
		// hint disappears rather than carrying a stale "changed" forward.
		if dc, ok := res.Envp.(envport.WorkspaceDirtyChecker); ok {
			if d, derr := dc.ConfigDirty(ctx, envport.EnvID(v.EnvID)); derr == nil {
				dirty[id] = d
			} else {
				s.log().Debug("env reconcile: config dirty check failed", "run", id, "env", v.EnvID, "err", derr)
			}
		}
	}
	s.statusMu.Lock()
	s.status = next
	s.dirty = dirty
	s.statusMu.Unlock()
	return nil
}

// Dirty returns a snapshot copy of which runs' workspaces have build
// inputs newer than their container (keyed by run id); absent means "no" or
// "not reconciled yet".
func (s *Store) Dirty() map[string]bool {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	out := make(map[string]bool, len(s.dirty))
	maps.Copy(out, s.dirty)
	return out
}

// Statuses returns a snapshot copy of the reconciled per-run live
// container status (keyed by run id). A run absent from the map has not been
// reconciled yet; callers should fall back to the env_id-based guess.
func (s *Store) Statuses() map[string]envport.EnvStatus {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	out := make(map[string]envport.EnvStatus, len(s.status))
	maps.Copy(out, s.status)
	return out
}

// Badges returns the per-run workspace badge string: the reconciled docker
// status, OVERRIDDEN by any in-flight op ("rebuilding"/"destroying"/
// "provisioning") or a recently-failed op ("error"). The op overlay wins so a
// transient action shows immediately without touching the 30s reconciler map
// or the event log. Locks are taken SEQUENTIALLY (opMu, released, then
// statusMu via Statuses) — never nested.
func (s *Store) Badges() map[string]string {
	now := s.now()
	s.opMu.Lock()
	ops := make(map[string]*op, len(s.ops))
	for id, op := range s.ops {
		if op.failed && now.Sub(op.at) > ErrorTTL {
			delete(s.ops, id) // lazy TTL sweep, like observedTTL
			continue
		}
		ops[id] = op
	}
	s.opMu.Unlock()

	st := s.Statuses() // takes statusMu — sequential, not nested with opMu
	out := make(map[string]string, len(st)+len(ops))
	for id, status := range st {
		out[id] = string(status)
	}
	for id, op := range ops {
		if op.failed {
			out[id] = "error"
		} else {
			out[id] = op.kind
		}
	}
	return out
}
