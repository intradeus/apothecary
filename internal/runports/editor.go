package runports

import (
	"context"
	"fmt"

	"apothecary/internal/ports"
)

// OpenEditor lazily starts code-server (VS Code in the browser) in the run's
// workspace and exposes it, returning the browser URL — the run page's "Open
// editor" action. Idempotent: it always (re)starts code-server if it is not
// listening — an exposure outliving it (a recreated container, a crash) must
// not hand out a dead editor — and reuses an existing exposure's URL;
// otherwise it exposes the port through the same portrelay path as any
// workspace port.
//
// No op-gate: it neither recreates the container nor races the reaper/rebuild;
// it is safe to open the editor alongside a live run.
func (s *Service) OpenEditor(ctx context.Context, runID string) (string, error) {
	if s.Relay == nil {
		return "", ErrPortsOff
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
	if url := s.Relay.Existing(runID, ports.CodeServerPort); url != "" {
		return url, nil
	}
	pc, err := exposer.ExposePort(ctx, ports.EnvID(v.EnvID), ports.CodeServerPort)
	if err != nil {
		return "", err
	}
	return s.Relay.Expose(runID, ports.CodeServerPort, pc)
}
