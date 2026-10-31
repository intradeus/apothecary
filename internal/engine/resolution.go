package engine

import (
	"context"
	"errors"
	"fmt"

	"apothecary/internal/flow"
	"apothecary/internal/flow/actionspec"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
)

// preflightTicket is the per-TICKET half of preflight: a tracker action
// writes through the integration the ticket was imported from, so whether
// that integration can write is checked here, where the ticket is known,
// rather than in preflight (per project, also run without a ticket by
// PreflightProject). A ticket with no tracker passes — the engine skips
// those stages, visibly — and so does a flow with no tracker action.
func (e *Engine) preflightTicket(ctx context.Context, res *resolution.Resolution, fl *flow.Flow, t runinput.Ticket) error {
	if t.SourceID == "" {
		return nil
	}
	for i := range fl.Stages {
		st := &fl.Stages[i]
		if st.Type != flow.StageAction {
			continue
		}
		act := actionspec.Get(st.Action)
		if act == nil || act.Requires != actionspec.RequiresTracker {
			continue
		}
		if res.TicketWriter == nil {
			return fmt.Errorf("preflight: stage %q: action %q writes to the ticket's tracker, which this build cannot", st.ID, st.Action)
		}
		if _, err := res.TicketWriter(ctx, t.SourceID); err != nil {
			return fmt.Errorf("preflight: stage %q: action %q writes to ticket %s's tracker, but %w", st.ID, st.Action, t.SourceKey, err)
		}
		return nil // every tracker stage writes through the same integration
	}
	return nil
}

// pinnedFlow loads the exact document a run started with (PLAN5 M19). A run
// records its flow's content hash at creation, and since v3 the document
// carries its agents — so pinning by hash means an edit to a flow (or to a
// prompt inside it) never changes a run already in flight. Recovery after a
// restart re-reads the same pinned version.
//
// Falls back to the current document by name when the pinned version cannot
// be resolved: runs created before pinning carry no usable hash, and a
// pruned version should not strand a live run.
func (e *Engine) pinnedFlow(ctx context.Context, projectID, name, hash string) (*flow.Flow, error) {
	ps, ok := e.flows.(flow.PinnedSource)
	if !ok || hash == "" {
		return e.flows.Get(ctx, projectID, name)
	}
	f, err := ps.GetVersion(ctx, projectID, name, hash)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, flow.ErrNotFound) {
		return nil, err
	}
	e.log.Warn("pinned flow version is gone; falling back to the current document",
		"flow", name, "pinned", hash)
	return e.flows.Get(ctx, projectID, name)
}

// resolveForRun is resolution.ForRun over the engine's resolver.
func (e *Engine) resolveForRun(ctx context.Context, v run.View) (*resolution.Resolution, error) {
	return resolution.ForRun(ctx, e.resolver, v)
}

// SettingsSource reads the §6.4 policy settings — on every admission
// decision, no cache, no watch.
type SettingsSource interface {
	Limits(ctx context.Context) (Limits, error)
}

// StaticSettings serves fixed limits (tests, envcheck).
type StaticSettings struct{ L Limits }

func (s StaticSettings) Limits(context.Context) (Limits, error) { return s.L, nil }
