package engine

import (
	"context"
	"fmt"

	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// OpenEditor lazily starts code-server (VS Code in the browser) in the run's
// workspace and exposes it, returning the browser URL — the run page's "Open
// editor" action. Idempotent: an already-exposed editor returns its URL
// immediately (e.g. after a plain engine restart, where the container +
// code-server survive and autoExpose rebuilt the relay). Otherwise it starts
// code-server if not listening, waits for it, and exposes it through the same
// portrelay path as any workspace port.
//
// No op-gate: it neither recreates the container nor races the reaper/rebuild;
// it is safe to open the editor alongside a live run.
func (e *Engine) OpenEditor(ctx context.Context, runID string) (string, error) {
	if e.portRelay == nil {
		return "", ErrPortsOff
	}
	if url := e.portRelay.Existing(runID, ports.CodeServerPort); url != "" {
		return url, nil
	}
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return "", err
	}
	if len(evs) == 0 {
		return "", fmt.Errorf("run %s not found", runID)
	}
	v := run.Reduce(evs)
	if v.EnvID == "" || v.ProjectID == "" {
		return "", fmt.Errorf("run %s has no environment", runID)
	}
	res, err := e.resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return "", err
	}
	defer res.release()

	starter, ok := res.Envp.(ports.EditorStarter)
	if !ok {
		return "", fmt.Errorf("run %s: provisioner does not support the editor", runID)
	}
	exposer, ok := res.Envp.(ports.WorkspacePortExposer)
	if !ok {
		return "", fmt.Errorf("run %s: provisioner does not support port exposure", runID)
	}

	// Start code-server (no-op if already listening), then expose it exactly
	// like a discovered app port.
	if err := starter.StartEditor(ctx, ports.EnvID(v.EnvID), ports.CodeServerPort, res.EditorSettings); err != nil {
		return "", err
	}
	pc, err := exposer.ExposePort(ctx, ports.EnvID(v.EnvID), ports.CodeServerPort)
	if err != nil {
		return "", err
	}
	return e.portRelay.Expose(runID, ports.CodeServerPort, pc)
}
