package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"apothecary/internal/adapters/kube"
)

// A sandbox pod's shape is what a workshop's settings decide of it and a
// live pod cannot take: its runtime (class, user namespace, VM or not) and
// its sandbox container's requests and limits. The pod carries its shape's
// digest; a rebuild compares it with what the settings now give and
// replaces a pod whose shape changed (shapeChange), so a new size or runtime
// applies there as the workshop form says. A provision reuses the pod as it
// is: a stage must not lose a pod it did not ask to change.

// shapeAnnotation holds the digest of the pod's shape.
const shapeAnnotation = "apothecary.io/shape"

// podShape is the digest of pod's shape under rt.
func podShape(rt sandboxRuntime, pod kube.Pod) string {
	var res *kube.ResourceRequirements
	for _, c := range pod.Spec.Containers {
		if c.Name == sandboxContainer {
			res = c.Resources
		}
	}
	b, _ := json.Marshal(struct {
		Runtime   string                     `json:"runtime"`
		Class     string                     `json:"class,omitempty"`
		HostUsers *bool                      `json:"hostUsers,omitempty"`
		Resources *kube.ResourceRequirements `json:"resources,omitempty"`
	}{string(rt.name), pod.Spec.RuntimeClassName, pod.Spec.HostUsers, res})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// usesClaim: the pod keeps the run's checkout and inner docker data on the
// run's claim, which outlives it.
func usesClaim(pod kube.Pod, name string) bool { return claimUse(pod, name) != "" }

// claimUse is how the pod holds the run's claim: "mount" (a filesystem
// claim, mounted), "device" (a raw-block one, a VM's disk), "" not at all.
// A claim's mode is fixed when it is made, so a pod of one use cannot take
// over another's claim.
func claimUse(pod kube.Pod, name string) string {
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ClaimName != dataClaimName(name) {
			continue
		}
		for _, c := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
			for _, d := range c.VolumeDevices {
				if d.Name == v.Name {
					return "device"
				}
			}
		}
		return "mount"
	}
	return ""
}

// shapeChange is what a rebuild does with the running pod live, given the
// pod the settings now give (want): replace it when its shape changed and
// both keep the run on its claim; keep it when its shape is the same, or
// when replacing it would lose the checkout (an emptyDir goes with its pod,
// and a VM runtime's disks are emptyDirs) — then note says why, and the new
// shape applies to the workshop's next run.
func shapeChange(live, want kube.Pod, name string) (replace bool, note string) {
	if live.Metadata.Annotations[shapeAnnotation] == want.Metadata.Annotations[shapeAnnotation] {
		return false, ""
	}
	if use := claimUse(live, name); use != "" && use == claimUse(want, name) {
		return true, ""
	}
	if usesClaim(live, name) && usesClaim(want, name) {
		return false, "the workshop's sandbox runtime changed between a VM and a container: the run's claim is a disk for one and a filesystem for the other, so the change applies to the workshop's next run"
	}
	return false, "the workshop's sandbox size or runtime changed, but this sandbox keeps its checkout on disks that go with its pod: the change applies to the workshop's next run"
}
