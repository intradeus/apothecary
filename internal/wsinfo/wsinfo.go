// Package wsinfo answers questions about a run's workspace that are not
// steps of its flow: which repositories it holds and a client for its
// sidecar (the run page's files, diffs and terminal), the live exec log, the
// SSH door's way in, and the project's build cache. None of it appends to a
// run's log, so it reads run views and resolves providers without the engine.
package wsinfo

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"apothecary/internal/envstate"
	"apothecary/internal/event"
	"apothecary/internal/ports/envport"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
	"apothecary/internal/sidecar"
	"apothecary/internal/wsname"
)

var (
	// ErrNoSidecar: the run's workspace cannot host the sidecar (provisioner
	// without one, or no workspace yet).
	ErrNoSidecar = errors.New("this run's workspace has no sidecar")
	// ErrNoWorkspace: the run has no workspace (none yet, archived, or
	// stopped) — for a service to run in, or a person to reach.
	ErrNoWorkspace = errors.New("the run has no workspace")
	// ErrNoLiveLog marks a run whose provisioner does not implement the
	// optional ExecLogTailer capability, or which has no environment yet.
	ErrNoLiveLog = errors.New("no live workspace log")
	// ErrBuildCacheOff: this provisioner keeps no build cache of its own.
	ErrBuildCacheOff = errors.New("this provisioner has no build cache")
)

// Service reaches run workspaces through each project's resolved provisioner.
type Service struct {
	Runs     *run.Reader
	Resolver resolution.Resolver
	// EnvState says which workspaces are known to be stopped or gone.
	EnvState *envstate.Store

	mu sync.Mutex
	// doorKey is the SSH door's public key (an authorized_keys line),
	// written into a workspace before the door dials its sidecar.
	doorKey string
}

func New(runs *run.Reader, resolver resolution.Resolver, envState *envstate.Store) *Service {
	return &Service{Runs: runs, Resolver: resolver, EnvState: envState}
}

// ErrNoSuchCheckout: the run has no checkout of that name.
var ErrNoSuchCheckout = errors.New("no such checkout in this run")

// Checkout is one of a run's repositories as the workspace views address
// it: the recorded checkout plus the git ref its changes are measured
// against (the branch it forked from).
type Checkout struct {
	event.Checkout
	Base string
}

// Checkouts lists the run's repositories (the primary first).
func (s *Service) Checkouts(ctx context.Context, runID string) ([]event.Checkout, error) {
	v, err := s.Runs.View(ctx, runID)
	if err != nil {
		return nil, err
	}
	return v.Checkouts, nil
}

// Workspace returns a client for the run's workspace sidecar (starting it
// when nothing serves yet) and the checkout named by repo ("" = the primary).
func (s *Service) Workspace(ctx context.Context, runID, repo string) (*sidecar.Client, Checkout, error) {
	v, err := s.Runs.View(ctx, runID)
	if err != nil {
		return nil, Checkout{}, err
	}
	if v.ProjectID == "" || v.EnvID == "" {
		return nil, Checkout{}, fmt.Errorf("run %s: %w", runID, ErrNoSidecar)
	}
	if v.State == run.Archived {
		return nil, Checkout{}, fmt.Errorf("run %s is archived; its workspace is gone: %w", runID, ErrNoSidecar)
	}
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
	var co *event.Checkout
	for i := range v.Checkouts {
		if v.Checkouts[i].Name == repo {
			co = &v.Checkouts[i]
		}
	}
	if co == nil {
		return nil, Checkout{}, fmt.Errorf("%w: %q", ErrNoSuchCheckout, repo)
	}
	res, err := s.Resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return nil, Checkout{}, err
	}
	defer res.Close()
	sc, ok := res.Envp.(envport.Sidecar)
	if !ok {
		return nil, Checkout{}, ErrNoSidecar
	}
	conn, err := sc.Sidecar(ctx, envport.EnvID(wsname.ForRun(v, runID)))
	if err != nil {
		return nil, Checkout{}, fmt.Errorf("workspace sidecar: %w", err)
	}
	base := co.Source
	if base == "" {
		base = res.RepoRef.DefaultBranch
	}
	if base != "" {
		base = "origin/" + base
	}
	return sidecar.New(conn), Checkout{Checkout: *co, Base: base}, nil
}

// LogTail serves the live tail of a run's workspace exec output
// via the provisioner's OPTIONAL ExecLogTailer capability — the run-page
// "is it actually doing something" feedback.
func (s *Service) LogTail(ctx context.Context, runID string, n int) ([]byte, error) {
	v, err := s.Runs.View(ctx, runID)
	if err != nil {
		return nil, err
	}
	if v.ProjectID == "" {
		return nil, fmt.Errorf("run %s: %w", runID, ErrNoLiveLog)
	}
	env := wsname.ForRun(v, runID)
	res, err := s.Resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	tailer, ok := res.Envp.(envport.ExecLogTailer)
	if !ok {
		return nil, fmt.Errorf("run %s: provisioner has no live log: %w", runID, ErrNoLiveLog)
	}
	return tailer.TailExecLog(ctx, envport.EnvID(env), n)
}
