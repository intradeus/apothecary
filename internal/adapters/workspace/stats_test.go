package workspace

import (
	"context"
	"testing"

	"apothecary/internal/ports/envport"
)

// SampleResources is one reading of the run's sandbox, taken by the host
// (host_k8s_test.go covers the pod's cgroup reading): the adapter adds nothing
// and runs nothing inside the workspace for it.
func TestSampleResourcesIsTheHostsReading(t *testing.T) {
	r := &scriptRunner{}
	a := newTestAdapter(t, r)
	h := hostOf(t, a)
	h.sampled = envport.ResourceSample{CPUPerc: 12.5, CPUs: 4, MemUsed: 64 << 20, MemLimit: 1 << 30, PIDs: 7}
	got, err := a.SampleResources(context.Background(), "apo-x-11")
	if err != nil {
		t.Fatal(err)
	}
	if got != h.sampled {
		t.Errorf("sample = %+v, want the host's %+v", got, h.sampled)
	}
	if len(r.calls) != 0 {
		t.Errorf("sampling ran workspace calls: %v", r.calls)
	}
}
