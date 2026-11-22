package resources

import (
	"context"
	"fmt"
	"testing"
	"time"

	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
)

// A project's usage keeps each recent run's peak memory and OOM kills, the
// newest run first, a bounded few, and past the run's end.
func TestUsageKeepsRecentRunsPeaks(t *testing.T) {
	s := &Sampler{}
	at := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	s.mu.Lock()
	s.recordUsage("p", "r1", envport.ResourceSample{At: at, MemUsed: 3 << 30, MemLimit: 8 << 30})
	s.recordUsage("p", "r1", envport.ResourceSample{At: at.Add(time.Minute), MemUsed: 1 << 30, MemLimit: 8 << 30, OOMKills: 2})
	s.recordUsage("p", "r2", envport.ResourceSample{At: at.Add(2 * time.Minute), MemUsed: 5 << 30})
	s.recordUsage("q", "r9", envport.ResourceSample{MemUsed: 1})
	s.mu.Unlock()
	got := s.Usage("p")
	if len(got) != 2 || got[0].RunID != "r2" || got[1].RunID != "r1" {
		t.Fatalf("usage = %+v", got)
	}
	if r1 := got[1]; r1.PeakMem != 3<<30 || r1.OOMKills != 2 || r1.MemLimit != 8<<30 || !r1.At.Equal(at.Add(time.Minute)) {
		t.Errorf("r1 = %+v: the peak, not the last reading", r1)
	}
	got[0].RunID = "mutated"
	if s.Usage("p")[0].RunID != "r2" {
		t.Error("Usage must return a copy")
	}
	if u := s.Usage("nobody"); u == nil || len(u) != 0 {
		t.Errorf("unknown project = %#v, want empty", u)
	}
	s.mu.Lock()
	for i := range usageRuns + 3 {
		s.recordUsage("p", fmt.Sprintf("n%d", i), envport.ResourceSample{})
	}
	s.mu.Unlock()
	if got := s.Usage("p"); len(got) != usageRuns || got[0].RunID != fmt.Sprintf("n%d", usageRuns+2) {
		t.Errorf("bounded: %d runs, newest %s", len(got), got[0].RunID)
	}
}

// A sample pass feeds the run's project's usage.
func TestSampleOnceRecordsUsage(t *testing.T) {
	h := newFixture()
	ctx := context.Background()
	envID, _ := h.envp.Provision(ctx, envport.EnvSpec{RunID: "r-live"})
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "live", FlowName: "loop", ProjectID: fixtureProject},
		&event.EnvRequested{}, &event.EnvReady{EnvID: string(envID)},
	} {
		if _, err := h.store.Append(ctx, "r-live", p); err != nil {
			t.Fatal(err)
		}
	}
	h.res.Envp = samplerEnvp{EnvProvisioner: h.envp}
	if err := h.s.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if u := h.s.Usage(fixtureProject); len(u) != 1 || u[0].RunID != "r-live" || u[0].PeakMem != 100 || u[0].MemLimit != 200 {
		t.Errorf("usage = %+v", u)
	}
}
