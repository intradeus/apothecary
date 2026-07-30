package engine

import (
	"context"
	"errors"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
)

// samplerEnvp adds the OPTIONAL ResourceSampler capability to the fake.
type samplerEnvp struct {
	*fake.EnvProvisioner
	cpu  float64
	fail bool
}

func (s samplerEnvp) SampleResources(context.Context, ports.EnvID) (ports.ResourceSample, error) {
	if s.fail {
		return ports.ResourceSample{}, errors.New("docker stats: container churning")
	}
	return ports.ResourceSample{CPUPerc: s.cpu, MemUsed: 100, MemLimit: 200}, nil
}

func TestSampleResourcesOnce(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	// An active (Running, non-terminal) run with a live env.
	envID, _ := h.envp.Provision(ctx, ports.EnvSpec{RunID: "r-live"})
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "live", FlowName: "loop", ProjectID: testProject},
		&event.EnvRequested{}, &event.EnvReady{EnvID: string(envID)},
	} {
		if _, err := h.store.Append(ctx, "r-live", p); err != nil {
			t.Fatal(err)
		}
	}
	h.res.Envp = samplerEnvp{EnvProvisioner: h.envp, cpu: 42}

	if got := h.e.RunResources("r-live"); len(got) != 0 {
		t.Fatalf("samples before any pass: %v", got)
	}
	for pass := 1; pass <= 2; pass++ {
		if err := h.e.SampleResourcesOnce(ctx); err != nil {
			t.Fatal(err)
		}
		got := h.e.RunResources("r-live")
		if len(got) != pass {
			t.Fatalf("after pass %d: %d samples, want %d", pass, len(got), pass)
		}
		if got[pass-1].CPUPerc != 42 || got[pass-1].At.IsZero() {
			t.Fatalf("sample not recorded/stamped: %+v", got[pass-1])
		}
	}

	// A failed probe is a GAP: nothing appended, history kept — never a
	// carried-forward value.
	h.res.Envp = samplerEnvp{EnvProvisioner: h.envp, fail: true}
	if err := h.e.SampleResourcesOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.e.RunResources("r-live"); len(got) != 2 {
		t.Fatalf("failed probe changed the buffer: %d samples, want 2", len(got))
	}

	// A terminal run's series is dropped at the end of the next pass.
	if _, err := h.store.Append(ctx, "r-live", &event.RunCompleted{}); err != nil {
		t.Fatal(err)
	}
	if err := h.e.SampleResourcesOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.e.RunResources("r-live"); len(got) != 0 {
		t.Fatalf("terminal run kept %d samples, want 0", len(got))
	}
}

// A provisioner without the capability degrades honestly: no samples, no error.
func TestSampleResourcesOnceWithoutCapability(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	envID, _ := h.envp.Provision(ctx, ports.EnvSpec{RunID: "r-nocap"})
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "nocap", FlowName: "loop", ProjectID: testProject},
		&event.EnvRequested{}, &event.EnvReady{EnvID: string(envID)},
	} {
		if _, err := h.store.Append(ctx, "r-nocap", p); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.e.SampleResourcesOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.e.RunResources("r-nocap"); len(got) != 0 {
		t.Fatalf("fake has no ResourceSampler, got %d samples", len(got))
	}
}
