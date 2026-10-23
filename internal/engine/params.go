package engine

// Parameters (FLOWLANG §1): a flow declares its knobs with defaults; a
// project binds values (the storage plane's FlowBindings), and a run may
// override them. The values a run uses are resolved once, at creation, and
// recorded on run_created — every declared parameter, so recovery, a
// rebuild and the phases listing all resolve the pinned document to the same
// flow, whatever the defaults or the project's bindings say later.

import (
	"context"
	"fmt"
	"strings"

	"apothecary/internal/flow"
	"apothecary/internal/run"
	"apothecary/internal/signalspec"
)

// FlowBindings is the storage plane's per-project parameter bindings: the
// values a project set for a flow's parameters, read when a run is created.
type FlowBindings interface {
	// Binding returns the project's values for the named flow; nil, nil
	// when it set none.
	Binding(ctx context.Context, projectID, flowName string) (map[string]string, error)
}

// SetFlowBindings wires the project parameter bindings (nil = none: every
// parameter at its default unless a run overrides it).
func (e *Engine) SetFlowBindings(b FlowBindings) { e.bindings = b }

// resolveParams layers a run's parameter values: the flow's defaults, then
// the project's bindings, then this attempt's overrides. A binding for a
// parameter the flow no longer declares is dropped with a warning (a stale
// project setting must not block runs); an override for one is refused (the
// request is wrong). The result names every declared parameter.
func (e *Engine) resolveParams(ctx context.Context, fl *flow.Flow, projectID string, override map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(fl.Params))
	for _, pr := range fl.Params {
		out[pr.Name] = pr.Default
	}
	if e.bindings != nil {
		bound, err := e.bindings.Binding(ctx, projectID, fl.Name)
		if err != nil {
			return nil, fmt.Errorf("project bindings: %w", err)
		}
		for k, v := range bound {
			if fl.Param(k) == nil {
				e.log.Warn("flow binding names a parameter the flow no longer declares; ignored",
					"project", projectID, "flow", fl.Name, "param", k)
				continue
			}
			out[k] = v
		}
	}
	for k, v := range override {
		if fl.Param(k) == nil {
			return nil, fmt.Errorf("parameter %q is not declared by flow %q", k, fl.Name)
		}
		out[k] = v
	}
	return out, nil
}

// bindFlow resolves a flow with the project's bindings and nothing
// overridden: what a project-level check (preflight, the environment probe)
// sees.
func (e *Engine) bindFlow(ctx context.Context, fl *flow.Flow, projectID string) (*flow.Flow, error) {
	params, err := e.resolveParams(ctx, fl, projectID, nil)
	if err != nil {
		return nil, err
	}
	return fl.Resolve(params)
}

// boundFlow is the project's current document for name, resolved with its
// bindings.
func (e *Engine) boundFlow(ctx context.Context, projectID, name string) (*flow.Flow, error) {
	fl, err := e.flows.Get(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	return e.bindFlow(ctx, fl, projectID)
}

// runFlow is the document a run executes: the version pinned at creation,
// resolved with the values recorded then. Every loader of a running run's
// flow goes through here, so they all see the same resolved flow.
func (e *Engine) runFlow(ctx context.Context, v run.View) (*flow.Flow, error) {
	fl, err := e.pinnedFlow(ctx, v.ProjectID, v.FlowName, v.FlowHash)
	if err != nil {
		return nil, err
	}
	return fl.Resolve(v.Params)
}

// entrypointOf picks the phase a run begins at: the named one, which must be
// an entrypoint, or the flow's first entrypoint.
func entrypointOf(fl *flow.Flow, name string, onMR bool) (string, error) {
	var names []string
	for _, p := range fl.Entrypoints() {
		names = append(names, p.ID)
	}
	if name == "" {
		// The first entrypoint of the kind asked for: one created on a
		// merge request, or a fresh one.
		for _, p := range fl.Entrypoints() {
			if p.OnMR() == onMR {
				return p.ID, nil
			}
		}
		if onMR {
			return "", fmt.Errorf("flow %q has no entrypoint that begins a run on a merge request (on: %s)", fl.Name, signalspec.MROpened)
		}
		return "", fmt.Errorf("flow %q begins runs on a merge request someone opened: name one (entrypoints: %s)", fl.Name, strings.Join(names, ", "))
	}
	ph := fl.Phase(name)
	if ph == nil || !ph.IsEntrypoint() {
		return "", fmt.Errorf("phase %q is not an entrypoint of flow %q (entrypoints: %s)", name, fl.Name, strings.Join(names, ", "))
	}
	switch {
	case ph.OnMR() && !onMR:
		return "", fmt.Errorf("phase %q begins a run on a merge request someone opened (on: %s): name the merge request", name, signalspec.MROpened)
	case !ph.OnMR() && onMR:
		return "", fmt.Errorf("phase %q begins a fresh run (on: %s), not one on a merge request: begin at a phase on %s", name, ph.On.Signal, signalspec.MROpened)
	}
	return name, nil
}
