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

	if err := a.CanSleep(""); err != nil {
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
	if a.CanSleep("") == nil || a.Sleep(ctx, name) == nil {
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

// On claims, a sandbox under a VM runtime still cannot be stopped and kept:
// its disks are its own emptyDirs. The answer follows the runtime asked
// about, and Sleep asks the live pod (built under whichever runtime it was).
func TestK8sAVMSandboxCannotSleep(t *testing.T) {
	ctx := context.Background()
	k := &kubeFake{secrets: map[string]string{}}
	a := New(Config{RunsDir: testRunsDir, Kube: goldenKubeConfig(k, func(c *KubeConfig) {
		c.Runtime, c.Storage = envport.RuntimeSysbox, "pvc"
		c.Runtimes = []envport.Runtime{envport.RuntimeSysbox, envport.RuntimeKata}
	})})
	h := a.host.(*k8sHost)
	if err := a.CanSleep(""); err != nil {
		t.Errorf("sysbox: %v", err)
	}
	if err := a.CanSleep(envport.RuntimeKata); err == nil || !strings.Contains(err.Error(), "inside its VM") {
		t.Errorf("kata: %v", err)
	}
	if err := a.CanSleep(envport.RuntimePrivileged); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Errorf("unapproved: %v", err)
	}
	h.cfg.Storage = "emptydir"
	if a.CanSleep("") == nil {
		t.Error("emptydir")
	}
	// A VM storage class keeps a kata sandbox's disk, whatever the others'
	// storage.
	h.cfg.VMStorageClass = "block"
	if err := a.CanSleep(envport.RuntimeKata); err != nil {
		t.Errorf("kata on a block claim: %v", err)
	}
	h.cfg.Storage, h.cfg.VMStorageClass = "pvc", ""

	// The live pod decides, by what it stands on: a kata pod on disks of its
	// own refuses, and so does any pod on an emptyDir; a pod on the run's
	// claim (mounted, or a VM's raw-block disk) stops.
	claim := `"volumes":[{"name":"data","persistentVolumeClaim":{"claimName":"w-data"}}]`
	for _, c := range []struct{ pod, refusal string }{
		{`{"spec":{"runtimeClassName":"kata"},"status":{"phase":"Running"}}`, "runs in a VM"},
		{`{"spec":{"runtimeClassName":"sysbox-runc","volumes":[{"name":"data","emptyDir":{}}]},"status":{"phase":"Running"}}`, "emptydir storage"},
	} {
		k.pod, k.deleted = c.pod, nil
		if err := h.stopSandbox(ctx, "w"); err == nil || !strings.Contains(err.Error(), c.refusal) || len(k.deleted) != 0 {
			t.Errorf("%s: %v, deleted %v", c.pod, err, k.deleted)
		}
	}
	for _, pod := range []string{
		`{"spec":{"runtimeClassName":"sysbox-runc",` + claim + `},"status":{"phase":"Running"}}`,
		`{"spec":{"runtimeClassName":"kata",` + claim + `,"containers":[{"name":"sandbox","volumeDevices":[{"name":"data","devicePath":"/dev/apo-data"}]}]},"status":{"phase":"Running"}}`,
	} {
		k.pod, k.deleted = pod, nil
		if err := h.stopSandbox(ctx, "w"); err != nil || !slices.Equal(k.deleted, []string{"pod/w"}) {
			t.Errorf("%s: %v, deleted %v", pod, err, k.deleted)
		}
	}
	_, h2 := k8sWith(t, &kubeFake{secrets: map[string]string{}}, false, kubeRule{match: "get --request-timeout=30s pod", stderr: "boom", code: 1})
	if err := h2.checkStop(ctx, "w"); err == nil {
		t.Error("an unreadable pod is no answer")
	}
}
