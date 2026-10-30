package wsinfo

import (
	"context"

	"apothecary/internal/ports/envport"
)

// buildCacheCtrl resolves a project to its provisioner's build-cache
// controller. ErrBuildCacheOff when the provisioner has none — a k8s or remote
// provisioner may cache elsewhere, or not at all.
// It returns the project id alongside: the gateway that holds the cache is
// keyed by it (the same value Provision stamped on the run's EnvSpec).
func (s *Service) buildCacheCtrl(ctx context.Context, projectID string) (envport.BuildCacheController, string, func(), error) {
	res, err := s.Resolver.Resolve(ctx, projectID)
	if err != nil {
		return nil, "", nil, err
	}
	ctrl, ok := res.Envp.(envport.BuildCacheController)
	if !ok {
		res.Close()
		return nil, "", nil, ErrBuildCacheOff
	}
	return ctrl, res.ProjectID, res.Close, nil
}

// BuildCache reports what the project's devcontainer build cache holds. The
// cache is per project and outlives its runs: it is what stops every run
// rebuilding the image into its own throwaway daemon.
func (s *Service) BuildCache(ctx context.Context, projectID string) (envport.BuildCacheInfo, error) {
	ctrl, project, release, err := s.buildCacheCtrl(ctx, projectID)
	if err != nil {
		return envport.BuildCacheInfo{}, err
	}
	defer release()
	return ctrl.BuildCache(ctx, project)
}

// ClearBuildCache empties it. Safe by construction — the next build refills it,
// so the cost of clearing when you did not need to is one slow build.
func (s *Service) ClearBuildCache(ctx context.Context, projectID string) error {
	ctrl, project, release, err := s.buildCacheCtrl(ctx, projectID)
	if err != nil {
		return err
	}
	defer release()
	return ctrl.ClearBuildCache(ctx, project)
}
