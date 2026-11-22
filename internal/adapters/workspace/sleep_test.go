package workspace

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"apothecary/internal/ports/envport"
)

// Sleep stops the sandbox and keeps the run folder; until the next
// provision the run is asleep, not absent. A host that cannot keep a
// stopped sandbox refuses, and so does a run that is not there.
func TestSleepKeepsTheRunAndStatusSaysAsleep(t *testing.T) {
	a := newTestAdapter(t, &scriptRunner{})
	const name = "apothecary-run1"
	armSandbox(t, a, name)
	h := hostOf(t, a)
	ctx := context.Background()
	gone := false
	h.state = func(string) (string, error) {
		if gone {
			return "", nil
		}
		return "running", nil
	}

	if err := a.CanSleep(); err != nil {
		t.Fatal(err)
	}
	if err := a.Sleep(ctx, name); err != nil {
		t.Fatal(err)
	}
	gone = true
	if !slices.Equal(h.stops, []string{name}) || len(h.destroys) != 0 {
		t.Errorf("stops %v, destroys %v", h.stops, h.destroys)
	}
	if st, err := a.Status(ctx, name); err != nil || st != envport.EnvAsleep {
		t.Errorf("status = %s, %v", st, err)
	}
	// Destroyed, it is gone for good.
	if err := a.Destroy(ctx, name); err != nil {
		t.Fatal(err)
	}
	if st, _ := a.Status(ctx, name); st != envport.EnvAbsent {
		t.Errorf("after destroy: %s", st)
	}

	if err := a.Sleep(ctx, "apothecary-nope"); err == nil || !strings.Contains(err.Error(), "no such workspace") {
		t.Errorf("absent run: %v", err)
	}
	h.canStopErr = errors.New("emptydir")
	if a.CanSleep() == nil || a.Sleep(ctx, name) == nil {
		t.Error("a host that cannot stop refuses")
	}
}

// A failed stop leaves the marker: the run still reads asleep, and the retry
// finishes the job.
func TestSleepThatFailsStaysAsleep(t *testing.T) {
	a := newTestAdapter(t, &scriptRunner{})
	const name = "apothecary-run1"
	armSandbox(t, a, name)
	h := hostOf(t, a)
	h.stopErr = errors.New("api down")
	if err := a.Sleep(context.Background(), name); err == nil {
		t.Fatal("want the stop's error")
	}
	if !a.asleep(name) {
		t.Error("the marker went with the failure")
	}
}

// The Kubernetes host deletes the pod alone, and refuses on emptyDir storage.
func TestK8sStopKeepsTheClaim(t *testing.T) {
	k := &kubeFake{secrets: map[string]string{}}
	h := New(Config{RunsDir: testRunsDir, Kube: goldenKubeConfig(k, func(c *KubeConfig) { c.Storage = "pvc" })}).host.(*k8sHost)
	if err := h.stopSandbox(context.Background(), "w"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(k.deleted, []string{"pod/w"}) {
		t.Errorf("deleted %v, want the pod alone", k.deleted)
	}
	h.cfg.Storage = "emptydir"
	if err := h.stopSandbox(context.Background(), "w"); err == nil || !strings.Contains(err.Error(), "storage set to pvc") {
		t.Errorf("emptydir: %v", err)
	}
}
