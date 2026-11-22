package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"apothecary/internal/adapters/kube"
)

// k8sLabelRuntimeProbe marks the integration test's throwaway pods; they
// carry no run label, so nothing mistakes one for a sandbox.
const k8sLabelRuntimeProbe = "apothecary.io/runtime-probe"

// runtimeProbeTimeout bounds one probe (k8sHost.probeTimeout overrides it):
// the shim image may be pulled first.
const runtimeProbeTimeout = 2 * time.Minute

// probePod is a pod under rt that runs `shim version` once: the sandbox's
// runtime class, user namespace and privilege, nothing else of it.
func (h *k8sHost) probePod(name string, rt sandboxRuntime) kube.Pod {
	c := kube.Container{Name: "probe", Image: h.cfg.ShimImage, ImagePullPolicy: h.cfg.PullPolicy, Args: []string{"version"}}
	if rt.privileged {
		c.SecurityContext = &kube.SecurityContext{Privileged: kube.Ptr(true)}
	}
	pod := kube.Pod{TypeMeta: kube.PodType(),
		Metadata: kube.ObjectMeta{Name: name, Labels: map[string]string{k8sLabelRuntimeProbe: string(rt.name)}},
		Spec: kube.PodSpec{
			RestartPolicy:                 "Never",
			TerminationGracePeriodSeconds: kube.Ptr(int64(0)),
			Containers:                    []kube.Container{c},
			RuntimeClassName:              h.classFor(rt),
		}}
	if rt.ownUserNamespace {
		pod.Spec.HostUsers = kube.Ptr(false)
	}
	return pod
}

// probeRuntime starts a throwaway pod under rt and waits for it to run to
// completion, so a cluster without the runtime (no RuntimeClass, nodes
// without its handler, no user-namespace support) fails the integration's
// Test, not a run. The pod is deleted whatever happens.
func (h *k8sHost) probeRuntime(ctx context.Context, rt sandboxRuntime) error {
	k := h.cfg.Kube
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	name := "apo-rt-probe-" + string(rt.name) + "-" + hex.EncodeToString(b)
	class := h.classFor(rt)
	where := fmt.Sprintf("sandbox runtime %s", rt.name)
	if class != "" {
		where += fmt.Sprintf(" (runtime class %s)", class)
	}
	if err := k.Create(ctx, h.probePod(name, rt)); err != nil {
		return fmt.Errorf("%s: the cluster refused a pod under it: %w", where, err)
	}
	defer func() { _ = k.Delete(context.WithoutCancel(ctx), "pod", name, false) }()
	limit := h.probeTimeout
	if limit == 0 {
		limit = runtimeProbeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	every := h.probeEvery
	if every == 0 {
		every = 2 * time.Second
	}
	for {
		var pod struct {
			Status kube.PodStatus `json:"status"`
		}
		found, err := k.Get(ctx, "pod", name, &pod)
		switch {
		case err != nil && ctx.Err() == nil:
			return fmt.Errorf("%s: reading its probe pod: %w", where, err)
		case err == nil && !found:
			return fmt.Errorf("%s: its probe pod vanished", where)
		case err == nil && pod.Status.Phase == "Succeeded":
			return nil
		case err == nil && pod.Status.Phase == "Failed":
			return fmt.Errorf("%s: its probe pod failed%s", where, h.why(ctx, name))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: its probe pod did not run within %s%s", where, limit, h.why(context.WithoutCancel(ctx), name))
		case <-time.After(every):
		}
	}
}

// why is the pod's newest warning, as a clause; "" when there is none.
func (h *k8sHost) why(ctx context.Context, pod string) string {
	if w, err := h.cfg.Kube.LastWarning(ctx, pod); err == nil && w != "" {
		return " — " + w
	}
	return ""
}

// probeRuntimes probes every runtime the integration builds, in order.
func (h *k8sHost) probeRuntimes(ctx context.Context) error {
	for _, name := range h.approved() {
		rt, err := lookupRuntime(name)
		if err != nil {
			return err
		}
		if err := h.probeRuntime(ctx, rt); err != nil {
			return err
		}
	}
	return nil
}
