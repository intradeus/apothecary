package workspace

import (
	"context"

	"apothecary/internal/ports/envport"
)

// SampleResources implements envport.ResourceSampler: one reading of the run's
// sandbox pod (host_k8s.go), which holds the whole inner world: inner dockerd,
// workspace, compose siblings, builds.
func (a *Adapter) SampleResources(ctx context.Context, env envport.EnvID) (envport.ResourceSample, error) {
	return a.host.sample(ctx, string(env))
}
