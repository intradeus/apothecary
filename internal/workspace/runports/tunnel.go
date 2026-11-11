package runports

import (
	"context"
	"fmt"
	"net"

	"apothecary/internal/ports/envport"
)

// Dial reaches a workspace port of a live run for one tunnel connection
// (`apothecary forward`): the person's machine gets the port on its own
// localhost, over the engine's HTTPS address, with nothing published. It
// does not need the port relay: a tunnel is a connection, not a URL.
//
// Opening a path to a port costs a relay process in the workspace, so the
// provisioner's PortConn is kept per (run, port) and every connection dials
// it. A kept one that no longer answers (the workspace was rebuilt, the relay
// died) is opened afresh once.
func (s *Service) Dial(ctx context.Context, runID string, port int) (net.Conn, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("port must be 1-65535, got %d", port)
	}
	if pc := s.keptConn(runID, port); pc != nil {
		if c, err := pc.Dial(ctx); err == nil {
			return c, nil
		}
		s.dropConn(runID, port, pc)
	}
	pc, err := s.openConn(ctx, runID, port)
	if err != nil {
		return nil, err
	}
	c, err := pc.Dial(ctx)
	if err != nil {
		s.dropConn(runID, port, pc)
		return nil, fmt.Errorf("port %d of run %s: %w", port, runID, err)
	}
	return c, nil
}

// openConn asks the run's provisioner for a path to the port and keeps it.
func (s *Service) openConn(ctx context.Context, runID string, port int) (envport.PortConn, error) {
	v, err := s.Runs.View(ctx, runID)
	if err != nil {
		return nil, err
	}
	if v.EnvID == "" || v.ProjectID == "" {
		return nil, fmt.Errorf("run %s has no workspace", runID)
	}
	res, err := s.Resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	exposer, ok := res.Envp.(envport.WorkspacePortExposer)
	if !ok {
		return nil, fmt.Errorf("run %s: provisioner does not reach workspace ports", runID)
	}
	pc, err := exposer.ExposePort(ctx, envport.EnvID(v.EnvID), port)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns == nil {
		s.conns = map[string]map[int]envport.PortConn{}
	}
	if s.conns[runID] == nil {
		s.conns[runID] = map[int]envport.PortConn{}
	}
	if old := s.conns[runID][port]; old != nil {
		// Another connection opened one meanwhile: keep the first.
		_ = pc.Close()
		return old, nil
	}
	s.conns[runID][port] = pc
	return pc, nil
}

func (s *Service) keptConn(runID string, port int) envport.PortConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[runID][port]
}

// dropConn forgets pc if it is still the one kept for (run, port).
func (s *Service) dropConn(runID string, port int, pc envport.PortConn) {
	s.mu.Lock()
	if s.conns[runID][port] == pc {
		delete(s.conns[runID], port)
	}
	s.mu.Unlock()
	_ = pc.Close()
}

// dropConns forgets every path kept for a run: its workspace was torn down
// or rebuilt, and the relays behind them died with it.
func (s *Service) dropConns(runID string) {
	s.mu.Lock()
	kept := s.conns[runID]
	delete(s.conns, runID)
	s.mu.Unlock()
	for _, pc := range kept {
		_ = pc.Close()
	}
}
