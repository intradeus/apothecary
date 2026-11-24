package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/kube"
	"apothecary/internal/ports/envport"
)

// livePod is the pod h builds for spec, as the API answers it running.
func livePod(t *testing.T, h *k8sHost, spec envport.EnvSpec, strip bool) string {
	t.Helper()
	p, err := h.podManifest(context.Background(), "w", spec, egressgw.Attachment{})
	if err != nil {
		t.Fatal(err)
	}
	if strip { // a pod an older engine made: no shape on it
		delete(p.Metadata.Annotations, shapeAnnotation)
	}
	p.Status = kube.PodStatus{Phase: "Running", PodIP: "10.42.0.9", Conditions: []kube.PodCondition{{Type: "Ready", Status: "True"}}}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A rebuild applies the workshop's new size or runtime: a pod of another
// shape is replaced, its claim (and the checkout on it) kept. A provision
// reuses the pod as it is, and a pod whose disks go with it is kept, the
// log saying the change waits for the next run.
func TestK8sRebuildReplacesAPodOfAnotherShape(t *testing.T) {
	ctx := context.Background()
	small := envport.EnvSpec{Size: envport.SandboxSize{MilliCPU: 1000, MemoryMiB: 2048}}
	big := envport.EnvSpec{Size: envport.SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}}

	run := func(storage string, live, want envport.EnvSpec, rebuild, strip bool, rules ...kubeRule) (*kubeFake, string, error) {
		k := &kubeFake{secrets: map[string]string{}}
		_, h := k8sWith(t, k, false, rules...)
		h.cfg.Storage = storage
		k.pod = livePod(t, h, live, strip)
		var log strings.Builder
		plog := func(f string, args ...any) { fmt.Fprintf(&log, f, args...) }
		_, err := h.ensurePod(ctx, "w", want, rebuild, egressgw.Attachment{}, plog)
		return k, log.String(), err
	}
	replaced := func(k *kubeFake) bool {
		return slices.Equal(k.deleted, []string{"pod/w"}) && k.obj(k.created, "Pod") != nil
	}

	k, log, err := run("pvc", small, big, true, false)
	if err != nil || !replaced(k) || !strings.Contains(log, "size or runtime changed; its volume and checkout are kept") {
		t.Fatalf("rebuild, new size: err = %v, deleted = %v, log = %q", err, k.deleted, log)
	}
	created, _ := json.Marshal(k.obj(k.created, "Pod"))
	if !strings.Contains(string(created), `"cpu":"4000m"`) {
		t.Errorf("the new pod is not the new size: %s", created)
	}

	k, log, err = run("pvc", small, small, true, false)
	if err != nil || len(k.deleted) != 0 || !strings.Contains(log, "reusing the sandbox pod") {
		t.Errorf("rebuild, same size: err = %v, deleted = %v, log = %q", err, k.deleted, log)
	}
	k, _, err = run("pvc", small, big, false, false)
	if err != nil || len(k.deleted) != 0 {
		t.Errorf("a provision replaced a pod: err = %v, deleted = %v", err, k.deleted)
	}
	k, _, err = run("pvc", small, small, true, true)
	if err != nil || !replaced(k) {
		t.Errorf("rebuild of an unshaped pod: err = %v, deleted = %v", err, k.deleted)
	}
	k, log, err = run("emptydir", small, big, true, false)
	if err != nil || len(k.deleted) != 0 || !strings.Contains(log, "applies to the workshop's next run") {
		t.Errorf("emptydir: err = %v, deleted = %v, log = %q", err, k.deleted, log)
	}

	// A runtime switch between a claim and a VM's own disks keeps the pod.
	k = &kubeFake{secrets: map[string]string{}}
	_, h := k8sWith(t, k, false)
	h.cfg.Runtimes = []envport.Runtime{envport.RuntimeKata}
	k.pod = livePod(t, h, small, false)
	var b strings.Builder
	kata := small
	kata.Runtime = envport.RuntimeKata
	if _, err := h.ensurePod(ctx, "w", kata, true, egressgw.Attachment{}, func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }); err != nil {
		// Today a kata pod refuses pvc storage at podManifest; either way the
		// live pod must not have gone.
		if len(k.deleted) != 0 {
			t.Errorf("kata on pvc: deleted %v (%v)", k.deleted, err)
		}
	} else if len(k.deleted) != 0 || !strings.Contains(b.String(), "next run") {
		t.Errorf("to kata: deleted = %v, log = %q", k.deleted, b.String())
	}

	if _, _, err := run("pvc", small, big, true, false, kubeRule{match: "delete pod", stderr: "boom", code: 1}); err == nil {
		t.Error("the old pod could not go, but the rebuild went on")
	}
	if _, _, err := run("pvc", small, envport.EnvSpec{Runtime: "gvisor"}, true, false); err == nil {
		t.Error("a rebuild to a runtime the integration does not build went on")
	}
	k = &kubeFake{secrets: map[string]string{}}
	_, h = k8sWith(t, k, false)
	h.cfg.Kube.R = failPodCreate{k}
	k.pod = livePod(t, h, small, false)
	if _, err := h.ensurePod(ctx, "w", big, true, egressgw.Attachment{}, discard); err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("the new pod could not be made: err = %v", err)
	}
	// No pod yet, and one that cannot be built: refused before anything.
	k = &kubeFake{secrets: map[string]string{}}
	_, h = k8sWith(t, k, false)
	if _, err := h.ensurePod(ctx, "w", envport.EnvSpec{Runtime: "gvisor"}, false, egressgw.Attachment{}, discard); err == nil || k.obj(k.created, "Pod") != nil {
		t.Errorf("an unbuildable pod: err = %v", err)
	}
}

// The shape is the runtime and the sandbox's resources; nothing else.
func TestPodShape(t *testing.T) {
	priv, _ := lookupRuntime(envport.RuntimePrivileged)
	sys, _ := lookupRuntime(envport.RuntimeSysbox)
	base := kube.Pod{Spec: kube.PodSpec{Containers: []kube.Container{{Name: sandboxContainer}}}}
	sized := kube.Pod{Spec: kube.PodSpec{Containers: []kube.Container{{Name: sandboxContainer, Resources: sandboxResources(envport.SandboxSize{MemoryMiB: 1024}, envport.SandboxSize{})}}}}
	other := base
	other.Spec.Hostname = "elsewhere"
	if podShape(priv, base) != podShape(priv, other) {
		t.Error("a field outside the shape changed it")
	}
	if podShape(priv, base) == podShape(sys, base) || podShape(priv, base) == podShape(priv, sized) {
		t.Error("runtime or size left the shape unchanged")
	}
}

// failPodCreate is the fake cluster refusing every pod it is asked to make.
type failPodCreate struct{ *kubeFake }

func (f failPodCreate) Run(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer, env ...string) ([]byte, []byte, int, error) {
	if slices.Contains(args, "create") && bytes.Contains(stdin, []byte(`"kind":"Pod"`)) {
		return nil, []byte("quota exceeded"), 1, nil
	}
	return f.kubeFake.Run(ctx, bin, args, stdin, tee, env...)
}
