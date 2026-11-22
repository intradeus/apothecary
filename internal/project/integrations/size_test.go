package integrations

import (
	"context"
	"errors"
	"testing"

	"apothecary/internal/ports/envport"
)

// A run's sandbox size is its project's under the instance's policy: each
// field its own or the default, never above the ceiling. No policy, or one
// that cannot be read, leaves the project's own.
func TestResolveSizesTheSandbox(t *testing.T) {
	r, svc, p, _, _ := setup(t)
	ctx := context.Background()
	p.Workspace.Size = envport.SandboxSize{MemoryMiB: 12288}
	if _, err := svc.UpdateProject(ctx, p.ID, p); err != nil {
		t.Fatal(err)
	}
	size := func() envport.SandboxSize {
		t.Helper()
		res, err := r.Resolve(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Release()
		return res.WorkspaceSize
	}
	if got := size(); got != (envport.SandboxSize{MemoryMiB: 12288}) {
		t.Errorf("no policy = %+v", got)
	}
	r.SetSandboxSizes(func(context.Context) (envport.SandboxPolicy, error) {
		return envport.SandboxPolicy{Default: envport.SandboxSize{MilliCPU: 2000, MemoryMiB: 4096},
			Ceiling: envport.SandboxSize{MemoryMiB: 8192}}, nil
	})
	if got := size(); got != (envport.SandboxSize{MilliCPU: 2000, MemoryMiB: 8192}) {
		t.Errorf("under the policy = %+v", got)
	}
	r.SetSandboxSizes(func(context.Context) (envport.SandboxPolicy, error) {
		return envport.SandboxPolicy{}, errors.New("db down")
	})
	if got := size(); got != (envport.SandboxSize{MemoryMiB: 12288}) {
		t.Errorf("unreadable policy = %+v", got)
	}
}
