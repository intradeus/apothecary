// Package runports publishes a run's workspace ports to people: the ports a
// workspace declares or a person exposes (through the port relay, on URLs
// that survive restarts), the listeners discovered at runtime with
// click-to-expose, and the in-browser editor. Exposures are live,
// engine-process state — never run events — so this lives beside the engine;
// the engine calls it when a workspace comes up or is rebuilt.
package runports

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"apothecary/internal/portrelay"
	"apothecary/internal/ports/envport"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

// Config is how discovered ports are treated.
type Config struct {
	// AutoExpose, when set, auto-exposes newly discovered listening ports (up
	// to MaxAuto per run) instead of only listing them for click-to-expose.
	AutoExpose bool
	// MaxAuto caps auto-exposures per run; default 5.
	MaxAuto int
}

func (c Config) maxAuto() int {
	if c.MaxAuto == 0 {
		return 5
	}
	return c.MaxAuto
}

// Service exposes run ports through the relay; a nil Relay turns exposure off.
type Service struct {
	Runs     *run.Reader
	Resolver resolution.Resolver
	Relay    *portrelay.Manager
	Config   Config
	// Clock ages observed snapshots; nil is time.Now. Log nil is slog.Default.
	Clock func() time.Time
	Log   *slog.Logger

	// observed is the per-run set of listening ports discovered at runtime
	// (the in-workspace shim scanner + sibling scans), surfaced on the run
	// page with click-to-expose. Memory-only and TTL-aged: a discovered
	// listener is external state, not a run-authored transition, so it never
	// enters the event log. Empty/stale means "no live scanner" — the read
	// path returns nothing rather than a stale lie.
	mu       sync.Mutex
	observed map[string]*observedRun
}

func New(runs *run.Reader, resolver resolution.Resolver, relay *portrelay.Manager, cfg Config, log *slog.Logger) *Service {
	return &Service{Runs: runs, Resolver: resolver, Relay: relay, Config: cfg, Log: log, observed: map[string]*observedRun{}}
}

func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// ErrPortsOff marks an expose request when no port relay is configured.
var ErrPortsOff = errors.New("port exposure is not configured")

// Expose publishes a workspace port of a live run and returns the
// browser URL (the run-page "open the app" path). Idempotent per
// (run, port). The live listener is engine-process-scoped and never
// event-recorded; only the host-port ALLOCATION is persisted (portrelay's
// Store), so the same URL comes back after a restart. See portrelay's
// package comment.
func (s *Service) Expose(ctx context.Context, runID string, port int) (string, error) {
	if s.Relay == nil {
		return "", ErrPortsOff
	}
	if url := s.Relay.Existing(runID, port); url != "" {
		return url, nil
	}
	v, err := s.Runs.View(ctx, runID)
	if err != nil {
		return "", err
	}
	if v.EnvID == "" || v.ProjectID == "" {
		return "", fmt.Errorf("run %s has no environment", runID)
	}
	res, err := s.Resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return "", err
	}
	defer res.Close()
	exposer, ok := res.Envp.(envport.WorkspacePortExposer)
	if !ok {
		return "", fmt.Errorf("run %s: provisioner does not support port exposure", runID)
	}
	pc, err := exposer.ExposePort(ctx, envport.EnvID(v.EnvID), port)
	if err != nil {
		return "", err
	}
	return s.Relay.Expose(runID, port, pc)
}

// Enabled reports whether ports can be exposed at all (a relay is wired).
func (s *Service) Enabled() bool { return s.Relay != nil }

// List lists a run's live exposures (the run-page ports panel).
func (s *Service) List(runID string) []portrelay.Exposure {
	if s.Relay == nil {
		return nil
	}
	return s.Relay.List(runID)
}

// AutoExpose publishes the union of the ports the environment DECLARES
// (devcontainer forwardPorts) and the ports this run had exposed before a
// restart (portRelay.SavedPorts — includes MANUAL exposures, which would
// otherwise be lost on recovery). Each re-binds its persisted host port so
// the URL survives. Best-effort — a failed exposure is a warning, never a
// failed run. Synchronous, and it never appends: the caller records the links.
func (s *Service) AutoExpose(ctx context.Context, runID string, envp envport.EnvProvisioner, id envport.EnvID) []event.Link {
	exposer, ok := envp.(envport.WorkspacePortExposer)
	if !ok || s.Relay == nil {
		return nil
	}
	seen := map[int]bool{}
	wsPorts := exposer.AppPorts(ctx, id)
	wsPorts = append(wsPorts, s.Relay.SavedPorts(runID)...)
	var out []event.Link
	for _, port := range wsPorts {
		if seen[port] {
			continue
		}
		seen[port] = true
		if url := s.Relay.Existing(runID, port); url != "" {
			out = append(out, event.Link{Kind: "app", URL: url})
			continue
		}
		pc, err := exposer.ExposePort(ctx, id, port)
		if err != nil {
			s.log().Warn("auto-expose failed", "run", runID, "port", port, "err", err)
			continue
		}
		url, err := s.Relay.Expose(runID, port, pc)
		if err != nil {
			s.log().Warn("auto-expose failed", "run", runID, "port", port, "err", err)
			continue
		}
		out = append(out, event.Link{Kind: "app", URL: url})
	}
	return out
}

// ReopenAfterRebuild brings a rebuilt workspace's ports back. The old
// exposures dial relays that died with the old container: drop them first,
// or AutoExpose takes them for live and keeps them. The saved allocation
// stays, so every port comes back on its old URL. code-server died with the
// old container too: bring it back, so an editor tab left open reconnects
// instead of facing a dead port.
func (s *Service) ReopenAfterRebuild(ctx context.Context, runID string, envp envport.EnvProvisioner, id envport.EnvID, editor envport.EditorSettings) {
	editorWasOpen := false
	if s.Relay != nil {
		editorWasOpen = slices.Contains(s.Relay.SavedPorts(runID), envport.CodeServerPort)
		s.Relay.Reset(runID)
	}
	s.AutoExpose(ctx, runID, envp, id)
	if starter, ok := envp.(envport.EditorStarter); ok && editorWasOpen {
		if serr := starter.StartEditor(ctx, id, envport.CodeServerPort, editor); serr != nil {
			s.log().Warn("rebuild: restart editor", "run", runID, "err", serr)
		}
	}
}

// Forget is the engine's teardown hook: a run's exposures and its
// discovered-port snapshot die with its workspace.
func (s *Service) Forget(runID string) {
	if s.Relay != nil {
		s.Relay.CloseRun(runID)
	}
	s.forgetObserved(runID)
}

// BeforeDestroy is part of the teardown hook; there is nothing to flush.
func (s *Service) BeforeDestroy(context.Context, run.View, *resolution.Resolution) {}
