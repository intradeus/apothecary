package integrations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"apothecary/internal/ports/envport"
)

// A run's sandbox size is its project's under the instance's policy: each
// field its own or the default, never above the ceiling; a field neither
// sets is capped by the ceiling, not sized at it. A policy that cannot be
// read is the last one read, and refuses the resolve when there was none.
func TestResolveSizesTheSandbox(t *testing.T) {
	r, svc, p, _, _ := setup(t)
	ctx := context.Background()
	p.Workspace.Size = envport.SandboxSize{MemoryMiB: 12288}
	p.Workspace.IdleMinutes = 15
	if _, err := svc.UpdateProject(ctx, p.ID, p); err != nil {
		t.Fatal(err)
	}
	type got struct {
		size, cap envport.SandboxSize
		idle      time.Duration
	}
	resolve := func() (got, error) {
		t.Helper()
		res, err := r.Resolve(ctx, p.ID)
		if err != nil {
			return got{}, err
		}
		defer res.Release()
		return got{res.WorkspaceSize, res.WorkspaceSizeCap, res.WorkspaceIdleAfter}, nil
	}
	must := func() got {
		t.Helper()
		g, err := resolve()
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	if g := must(); g.size != (envport.SandboxSize{MemoryMiB: 12288}) || !g.cap.IsZero() || g.idle != 15*time.Minute {
		t.Errorf("no policy = %+v", g)
	}

	// A failing read before any good one: refused, never unbounded.
	r.SetSandboxSizes(func(context.Context) (envport.SandboxPolicy, error) {
		return envport.SandboxPolicy{}, errors.New("db down")
	})
	if _, err := resolve(); err == nil || !strings.Contains(err.Error(), "sandbox limits could not be read") {
		t.Errorf("unreadable, none known: err = %v", err)
	}

	policy := envport.SandboxPolicy{Ceiling: envport.SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}}
	r.SetSandboxSizes(func(context.Context) (envport.SandboxPolicy, error) { return policy, nil })
	want := got{size: envport.SandboxSize{MemoryMiB: 8192}, cap: envport.SandboxSize{MilliCPU: 4000}, idle: 15 * time.Minute}
	if g := must(); g != want {
		t.Errorf("under a ceiling = %+v, want %+v", g, want)
	}

	// The store blips: the last policy read still holds.
	r.SetSandboxSizes(func(context.Context) (envport.SandboxPolicy, error) {
		return envport.SandboxPolicy{}, errors.New("db down")
	})
	if g := must(); g != want {
		t.Errorf("unreadable, one known = %+v, want %+v", g, want)
	}
}
