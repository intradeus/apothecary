package workspace

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/kube"
	"apothecary/internal/ports/envport"
)

// Every runtime the port knows has a row here, and every row is a runtime
// the port knows: a name the engine accepts is one the adapter can build.
func TestEveryRuntimeHasTraits(t *testing.T) {
	for _, r := range envport.Runtimes() {
		rt, err := lookupRuntime(r)
		if err != nil || rt.name != r {
			t.Errorf("%s: %+v, %v", r, rt, err)
		}
	}
	for r := range sandboxRuntimes {
		if _, err := envport.ParseRuntime(string(r)); err != nil {
			t.Errorf("row %s: %v", r, err)
		}
	}
	if rt, err := lookupRuntime(""); err != nil || rt.name != envport.RuntimePrivileged {
		t.Errorf(`"" = %+v, %v, want privileged`, rt, err)
	}
}

// A runtime the adapter cannot build fails the integration's test and every
// pod, and never opens the daemon door.
func TestUnknownRuntimeIsRefused(t *testing.T) {
	k := &kubeFake{secrets: map[string]string{}}
	a := New(Config{RunsDir: testRunsDir, Kube: goldenKubeConfig(k, func(c *KubeConfig) { c.Runtime = "gvisor" })})
	h := a.host.(*k8sHost)
	if _, err := h.podManifest(context.Background(), "w", envport.EnvSpec{}, egressgw.Attachment{}); err == nil || !strings.Contains(err.Error(), `"gvisor"`) {
		t.Errorf("podManifest err = %v", err)
	}
	if err := h.test(context.Background()); err == nil || !strings.Contains(err.Error(), `"gvisor"`) {
		t.Errorf("test err = %v", err)
	}
	if h.contained("") {
		t.Error("an unknown runtime contains nothing")
	}
}

func TestRuntimeClassName(t *testing.T) {
	sysbox, _ := lookupRuntime(envport.RuntimeSysbox)
	priv, _ := lookupRuntime(envport.RuntimePrivileged)
	for _, c := range []struct {
		rt             sandboxRuntime
		override, want string
	}{
		{sysbox, "", "sysbox-runc"},
		{sysbox, "sysbox-runc-v2", "sysbox-runc-v2"},
		{priv, "", ""},
		{priv, "dind", "dind"},
	} {
		if got := c.rt.className(c.override); got != c.want {
			t.Errorf("%s with %q = %q, want %q", c.rt.name, c.override, got, c.want)
		}
	}
}

// The devcontainer definition builds in the sandbox's docker daemon, which
// both runtimes give.
func TestDevcontainerNeedsNestedContainers(t *testing.T) {
	need := devcontainerDef{}.Needs()
	if !need.NestedContainers || need.NestedKubernetes || need.UserNamespaces || need.Contained {
		t.Errorf("needs = %+v", need)
	}
	for _, r := range envport.Runtimes() {
		if m := r.Capabilities().Missing(need); len(m) != 0 {
			t.Errorf("%s lacks %v", r, m)
		}
	}
}

// Memory is requested at its limit, CPU at a quarter of it (overcommitted),
// never below a floor nor above the limit; an unset field sets nothing.
func TestSandboxResources(t *testing.T) {
	if r := sandboxResources(envport.SandboxSize{}, envport.SandboxSize{}); r != nil {
		t.Errorf("unset = %+v", r)
	}
	r := sandboxResources(envport.SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}, envport.SandboxSize{})
	if r.Limits["cpu"] != "4000m" || r.Requests["cpu"] != "1000m" || r.Limits["memory"] != "8192Mi" || r.Requests["memory"] != "8192Mi" {
		t.Errorf("4 CPUs, 8 GiB = %+v", r)
	}
	r = sandboxResources(envport.SandboxSize{MilliCPU: 200}, envport.SandboxSize{})
	if r.Requests["cpu"] != "100m" || r.Limits["cpu"] != "200m" || r.Limits["memory"] != "" {
		t.Errorf("0.2 CPU = %+v", r)
	}
	r = sandboxResources(envport.SandboxSize{MilliCPU: 50}, envport.SandboxSize{})
	if r.Requests["cpu"] != "50m" {
		t.Errorf("a request above its limit: %+v", r)
	}
	r = sandboxResources(envport.SandboxSize{MemoryMiB: 512}, envport.SandboxSize{})
	if _, ok := r.Limits["cpu"]; ok || r.Limits["memory"] != "512Mi" {
		t.Errorf("memory only = %+v", r)
	}
	// Under a ceiling, an unset field is limited at it and requested at a
	// floor: a 64 GiB ceiling never asks a node for 64 GiB.
	r = sandboxResources(envport.SandboxSize{}, envport.SandboxSize{MilliCPU: 16000, MemoryMiB: 65536})
	if r.Limits["cpu"] != "16000m" || r.Requests["cpu"] != "100m" || r.Limits["memory"] != "65536Mi" || r.Requests["memory"] != "1024Mi" {
		t.Errorf("capped = %+v", r)
	}
	r = sandboxResources(envport.SandboxSize{MemoryMiB: 2048}, envport.SandboxSize{MilliCPU: 50, MemoryMiB: 512})
	if r.Limits["cpu"] != "50m" || r.Requests["cpu"] != "50m" || r.Limits["memory"] != "2048Mi" || r.Requests["memory"] != "2048Mi" {
		t.Errorf("sized memory, tiny CPU cap = %+v", r)
	}
	r = sandboxResources(envport.SandboxSize{}, envport.SandboxSize{MemoryMiB: 768})
	if r.Requests["memory"] != "768Mi" {
		t.Errorf("a request above its cap: %+v", r)
	}
}

// probeGet matches a read of a probe pod (not its creation or deletion).
const probeGet = "-o json --ignore-not-found"

// probeRan answers a runtime probe's pod as run to completion.
var probeRan = kubeRule{match: probeGet, out: `{"status":{"phase":"Succeeded"}}`}

// An env may run under another runtime its integration approves: that
// runtime's own class and pod shape, never the integration's class
// override (the default runtime's). One not approved is refused.
func TestEnvRunsUnderItsRuntime(t *testing.T) {
	k := &kubeFake{secrets: map[string]string{}}
	a := New(Config{RunsDir: testRunsDir, Kube: goldenKubeConfig(k, func(c *KubeConfig) {
		c.Runtime, c.RuntimeClass = envport.RuntimeSysbox, "sysbox-runc-v2"
		c.Runtimes = []envport.Runtime{envport.RuntimeSysbox, envport.RuntimePrivileged}
	})})
	h := a.host.(*k8sHost)
	ctx := context.Background()
	pod, err := h.podManifest(ctx, "w", envport.EnvSpec{}, egressgw.Attachment{})
	if err != nil || pod.Spec.RuntimeClassName != "sysbox-runc-v2" || pod.Spec.HostUsers == nil || *pod.Spec.HostUsers {
		t.Errorf("default: class %q, hostUsers %v, %v", pod.Spec.RuntimeClassName, pod.Spec.HostUsers, err)
	}
	pod, err = h.podManifest(ctx, "w", envport.EnvSpec{Runtime: envport.RuntimePrivileged}, egressgw.Attachment{})
	if err != nil || pod.Spec.RuntimeClassName != "" || pod.Spec.HostUsers != nil {
		t.Fatalf("privileged: class %q, hostUsers %v, %v", pod.Spec.RuntimeClassName, pod.Spec.HostUsers, err)
	}
	if sc := pod.Spec.Containers[0].SecurityContext; sc == nil || sc.Privileged == nil || !*sc.Privileged {
		t.Errorf("privileged sandbox container = %+v", sc)
	}
	if !h.contained("") || h.contained(envport.RuntimePrivileged) {
		t.Error("contained follows the env's runtime")
	}
	// Only the default is built when nothing else is approved.
	only := New(Config{RunsDir: testRunsDir, Kube: goldenKubeConfig(k, func(c *KubeConfig) { c.Runtime = envport.RuntimeSysbox })}).host.(*k8sHost)
	if _, err := only.podManifest(ctx, "w", envport.EnvSpec{Runtime: envport.RuntimePrivileged}, egressgw.Attachment{}); err == nil ||
		!strings.Contains(err.Error(), "not approved on this provisioner (approved: [sysbox])") {
		t.Errorf("unapproved: %v", err)
	}
	if only.contained(envport.RuntimePrivileged) {
		t.Error("an unapproved runtime contains nothing")
	}
}

// The daemon door under auto follows the env's runtime, not the default's.
func TestDaemonDoorFollowsTheEnvRuntime(t *testing.T) {
	k := &kubeFake{secrets: map[string]string{}}
	a := New(Config{RunsDir: testRunsDir, Kube: goldenKubeConfig(k, func(c *KubeConfig) {
		c.Runtime = envport.RuntimeSysbox
		c.Runtimes = []envport.Runtime{envport.RuntimePrivileged}
	})})
	if err := a.checkDaemonDoor("feature x", ""); err != nil {
		t.Errorf("sysbox: %v", err)
	}
	if err := a.checkDaemonDoor("feature x", envport.RuntimePrivileged); err == nil {
		t.Error("privileged opened the door on auto")
	}
}

// The Test button starts a pod under every runtime the integration builds,
// in its shape, and deletes it; a runtime the cluster lacks fails it.
func TestRuntimeProbe(t *testing.T) {
	ctx := context.Background()
	mk := func(rules ...kubeRule) (*k8sHost, *kubeFake) {
		k := &kubeFake{secrets: map[string]string{}}
		_, h := k8sWith(t, k, false, rules...)
		h.cfg.Runtime, h.cfg.Runtimes = envport.RuntimeSysbox, []envport.Runtime{envport.RuntimePrivileged}
		h.rt, h.rtErr = lookupRuntime(envport.RuntimeSysbox)
		h.probeEvery = time.Millisecond
		return h, k
	}
	t.Run("every approved runtime runs", func(t *testing.T) {
		h, k := mk(probeRan)
		if err := h.probeRuntimes(ctx); err != nil {
			t.Fatal(err)
		}
		if len(k.created) != 2 {
			t.Fatalf("created %d pods", len(k.created))
		}
		spec := k.created[0]["spec"].(map[string]any)
		if spec["runtimeClassName"] != "sysbox-runc" || spec["hostUsers"] != false || spec["restartPolicy"] != "Never" ||
			spec["automountServiceAccountToken"] != false {
			t.Errorf("sysbox probe spec = %v", spec)
		}
		c := spec["containers"].([]any)[0].(map[string]any)
		if c["image"] != "shim:1" || c["securityContext"] != nil {
			t.Errorf("sysbox probe container = %v", c)
		}
		priv := k.created[1]["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		if priv["securityContext"].(map[string]any)["privileged"] != true {
			t.Errorf("privileged probe container = %v", priv)
		}
		if len(k.deleted) != 2 || !strings.HasPrefix(k.deleted[0], "pod/apo-rt-probe-sysbox-") {
			t.Errorf("deleted = %v", k.deleted)
		}
		labels := k.created[0]["metadata"].(map[string]any)["labels"].(map[string]any)
		if labels[k8sLabelRuntimeProbe] != "sysbox" || labels[k8sLabelRun] != nil {
			t.Errorf("labels = %v", labels)
		}
	})
	t.Run("a missing runtime class is refused at creation", func(t *testing.T) {
		h, _ := mk(kubeRule{match: "create", stderr: `Error from server (Forbidden): pod rejected: RuntimeClass "sysbox-runc" not found`, code: 1})
		err := h.probeRuntimes(ctx)
		if err == nil || !strings.Contains(err.Error(), "sandbox runtime sysbox (runtime class sysbox-runc): the cluster refused a pod under it") ||
			!strings.Contains(err.Error(), "not found") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a pod that fails says why", func(t *testing.T) {
		h, k := mk(kubeRule{match: probeGet, out: `{"status":{"phase":"Failed"}}`},
			kubeRule{match: "events", out: `{"items":[{"reason":"FailedCreatePodSandBox","message":"no runtime for \"sysbox-runc\" is configured"}]}`})
		err := h.probeRuntimes(ctx)
		if err == nil || !strings.Contains(err.Error(), "probe pod failed — FailedCreatePodSandBox: no runtime") {
			t.Errorf("err = %v", err)
		}
		if len(k.deleted) != 1 {
			t.Errorf("a failed probe was left: %v", k.deleted)
		}
	})
	t.Run("a pod that goes away", func(t *testing.T) {
		h, _ := mk()
		if err := h.probeRuntimes(ctx); err == nil || !strings.Contains(err.Error(), "vanished") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a pod that cannot be read", func(t *testing.T) {
		h, _ := mk(kubeRule{match: probeGet, stderr: "boom", code: 1})
		if err := h.probeRuntimes(ctx); err == nil || !strings.Contains(err.Error(), "reading its probe pod") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a pod still pending when the caller gives up", func(t *testing.T) {
		h, _ := mk(kubeRule{match: probeGet, out: `{"status":{"phase":"Pending"}}`})
		cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		if err := h.probeRuntimes(cctx); err == nil || !strings.Contains(err.Error(), "did not run within") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("an unknown approved runtime", func(t *testing.T) {
		h, _ := mk(probeRan)
		h.cfg.Runtimes = []envport.Runtime{"gvisor"}
		if err := h.probeRuntimes(ctx); err == nil || !strings.Contains(err.Error(), `"gvisor"`) {
			t.Errorf("err = %v", err)
		}
	})
}

// The live test's switch puts userspace WireGuard in the tunnel's
// container; without it the script decides from the kernel.
func TestWireGuardUserspaceSwitch(t *testing.T) {
	att := egressgw.Attachment{GatewayIP: "10.90.0.1", Subnet: "10.90.0.5/32", WGEndpoint: "10.43.9.9:51820", WGSecret: "apo-wg-apo-x"}
	env := func(on bool) []kube.EnvVar {
		k := &kubeFake{secrets: map[string]string{}}
		h := New(Config{RunsDir: testRunsDir, Kube: goldenKubeConfig(k, func(c *KubeConfig) { c.WireGuardUserspace = on })}).host.(*k8sHost)
		pod, err := h.podManifest(context.Background(), "w", envport.EnvSpec{}, att)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range pod.Spec.InitContainers {
			if c.Name == "wg" {
				return c.Env
			}
		}
		t.Fatal("no wg container")
		return nil
	}
	has := func(e []kube.EnvVar) bool {
		return slices.ContainsFunc(e, func(v kube.EnvVar) bool { return v.Name == "APO_WG_USERSPACE" && v.Value == "1" })
	}
	if has(env(false)) || !has(env(true)) {
		t.Errorf("off = %v, on = %v", env(false), env(true))
	}
	// The same switch reaches the project's gateway, so the live suite
	// covers both ends of a Kata guest's tunnel.
	for _, on := range []bool{false, true} {
		k := &kubeFake{secrets: map[string]string{}}
		h := New(Config{RunsDir: testRunsDir, Kube: goldenKubeConfig(k, func(c *KubeConfig) {
			c.Firewall, c.WireGuardUserspace = true, on
		})}).host.(*k8sHost)
		if h.gwh == nil || h.gwh.WireGuardUserspace != on {
			t.Errorf("WireGuardUserspace %v: gateway host %+v", on, h.gwh)
		}
	}
}

// A VM runtime's sandbox: no subPath (a volume each for the run folder and
// docker), the /dev prelude asked for, privileged inside its VM, no
// hostUsers; refused unsized. Its disks are emptyDirs of its own whatever
// the integration's storage.
func TestKataSandboxShape(t *testing.T) {
	k := &kubeFake{secrets: map[string]string{}}
	h := New(Config{RunsDir: testRunsDir, Kube: goldenKubeConfig(k, func(c *KubeConfig) {
		c.Runtime, c.Storage = envport.RuntimeKata, "emptydir"
	})}).host.(*k8sHost)
	ctx := context.Background()
	sized := envport.EnvSpec{Size: envport.SandboxSize{MilliCPU: 2000, MemoryMiB: 4096}}
	pod, err := h.podManifest(ctx, "w", sized, egressgw.Attachment{})
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.RuntimeClassName != "kata" || pod.Spec.HostUsers != nil {
		t.Errorf("class %q, hostUsers %v", pod.Spec.RuntimeClassName, pod.Spec.HostUsers)
	}
	for _, c := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		for _, m := range c.VolumeMounts {
			if m.SubPath != "" {
				t.Errorf("%s mounts %s with subPath %q", c.Name, m.Name, m.SubPath)
			}
		}
	}
	sb := pod.Spec.Containers[0]
	if !slices.ContainsFunc(sb.Env, func(e kube.EnvVar) bool { return e.Name == "APO_DEV_PRELUDE" && e.Value == "1" }) {
		t.Errorf("sandbox env = %v", sb.Env)
	}
	if sb.SecurityContext == nil || sb.SecurityContext.Privileged == nil || !*sb.SecurityContext.Privileged {
		t.Errorf("sandbox security = %+v", sb.SecurityContext)
	}
	var vols []string
	for _, v := range pod.Spec.Volumes {
		if v.EmptyDir != nil && (v.Name == "run" || v.Name == "docker") {
			vols = append(vols, v.Name+":"+v.EmptyDir.SizeLimit)
		}
	}
	if !slices.Equal(vols, []string{"run:20Gi", "docker:20Gi"}) {
		t.Errorf("volumes = %v", vols)
	}
	if !h.contained("") {
		t.Error("a VM contains its sandbox")
	}

	for _, s := range []envport.SandboxSize{{}, {MilliCPU: 1000}, {MemoryMiB: 1024}} {
		if _, err := h.podManifest(ctx, "w", envport.EnvSpec{Size: s}, egressgw.Attachment{}); err == nil || !strings.Contains(err.Error(), "needs a size") {
			t.Errorf("size %+v: %v", s, err)
		}
	}
	h.cfg.Storage = "pvc"
	pod, err = h.podManifest(ctx, "w", sized, egressgw.Attachment{})
	if err != nil {
		t.Fatalf("pvc: %v", err)
	}
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim != nil || v.Name == "data" {
			t.Errorf("pvc: a VM sandbox mounts %+v", v)
		}
	}
	// Other runtimes keep the one data volume, split by subPath.
	h.rt, _ = lookupRuntime(envport.RuntimeSysbox)
	pod, err = h.podManifest(ctx, "w", envport.EnvSpec{}, egressgw.Attachment{})
	if err != nil || pod.Spec.Containers[0].VolumeMounts[0].SubPath != "docker" {
		t.Errorf("sysbox mounts = %+v, %v", pod.Spec.Containers[0].VolumeMounts, err)
	}
}

// What can be stopped and kept: a sandbox on a claim. A VM's is the VM
// storage class's raw-block claim, the others' the integration's storage.
func TestCanStop(t *testing.T) {
	if CanStop("pvc") != nil || CanStop("emptydir") == nil {
		t.Error("storage")
	}
	for _, c := range []struct {
		rt               envport.Runtime
		storage, vmClass string
		ok               bool
	}{
		{envport.RuntimePrivileged, "pvc", "", true},
		{envport.RuntimeSysbox, "pvc", "", true},
		{envport.RuntimeSysbox, "emptydir", "block", false},
		{envport.RuntimeKata, "pvc", "", false},
		{envport.RuntimeKata, "emptydir", "block", true},
		{"gvisor", "pvc", "block", false},
	} {
		if err := CanStopUnder(c.rt, c.storage, c.vmClass); (err == nil) != c.ok {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

// With a VM storage class a kata sandbox keeps the run on one raw-block
// claim from it, formatted and mounted by the sandbox: the claim a device in
// the sandbox container alone, the init containers and the relay on the
// seeded run volume, which the sandbox mounts aside.
func TestAVMSandboxOnABlockClaim(t *testing.T) {
	ctx := context.Background()
	k := &kubeFake{secrets: map[string]string{}}
	_, h := k8sWith(t, k, false)
	h.cfg.Storage, h.cfg.StorageClass, h.cfg.VMStorageClass = "emptydir", "local-path", "block"
	h.rt, _ = lookupRuntime(envport.RuntimeKata)
	sized := envport.EnvSpec{Size: envport.SandboxSize{MilliCPU: 2000, MemoryMiB: 4096}}
	if _, err := h.ensurePod(ctx, "w", sized, false, egressgw.Attachment{}, discard); err != nil {
		t.Fatal(err)
	}
	var pvc kube.PersistentVolumeClaim
	raw, _ := json.Marshal(k.obj(k.created, "PersistentVolumeClaim"))
	if err := json.Unmarshal(raw, &pvc); err != nil {
		t.Fatal(err)
	}
	if pvc.Metadata.Name != "w-data" || pvc.Spec.VolumeMode != "Block" || pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "block" {
		t.Errorf("claim = %+v", pvc)
	}
	pod, err := h.podManifest(ctx, "w", sized, egressgw.Attachment{})
	if err != nil {
		t.Fatal(err)
	}
	sb := pod.Spec.Containers[0]
	if !slices.Equal(sb.VolumeDevices, []kube.VolumeDevice{{Name: "data", DevicePath: vmDataDevice}}) {
		t.Errorf("devices = %+v", sb.VolumeDevices)
	}
	env := map[string]string{}
	for _, e := range sb.Env {
		env[e.Name] = e.Value
	}
	if env["APO_DATA_DEVICE"] != vmDataDevice || env["APO_SEED_DIR"] != vmSeedDir || env["APO_RUN_DIR"] != h.a.folderFor("w") || env["APO_DEV_PRELUDE"] != "1" {
		t.Errorf("env = %v", env)
	}
	for _, m := range sb.VolumeMounts {
		if m.Name == "run" && m.MountPath != vmSeedDir || m.Name == "data" || m.Name == "docker" {
			t.Errorf("sandbox mounts %+v", m)
		}
	}
	for _, c := range pod.Spec.InitContainers {
		if len(c.VolumeDevices) != 0 {
			t.Errorf("%s takes the device", c.Name)
		}
	}
	if claimUse(pod, "w") != "device" || h.canStop(envport.RuntimeKata) != nil {
		t.Errorf("use %q, stop %v", claimUse(pod, "w"), h.canStop(envport.RuntimeKata))
	}
	// A rebuild never hands a VM's disk to a container's mount, or back.
	h.rt, _ = lookupRuntime(envport.RuntimeSysbox)
	h.cfg.Storage = "pvc"
	sysbox, err := h.podManifest(ctx, "w", envport.EnvSpec{}, egressgw.Attachment{})
	if err != nil || claimUse(sysbox, "w") != "mount" {
		t.Fatalf("sysbox: %v %q", err, claimUse(sysbox, "w"))
	}
	if replace, note := shapeChange(pod, sysbox, "w"); replace || !strings.Contains(note, "between a VM and a container") {
		t.Errorf("kata → sysbox: %v %q", replace, note)
	}
}

// On claims, a sysbox sandbox gets its claim and a kata one none: approving
// kata leaves the integration's storage to its other runtimes. An env under
// a runtime the integration does not approve is refused before anything is
// made.
func TestAVMSandboxTakesNoClaim(t *testing.T) {
	ctx := context.Background()
	sized := envport.EnvSpec{Size: envport.SandboxSize{MilliCPU: 2000, MemoryMiB: 4096}}
	for rt, claims := range map[envport.Runtime]bool{envport.RuntimeSysbox: true, envport.RuntimeKata: false} {
		k := &kubeFake{secrets: map[string]string{}}
		_, h := k8sWith(t, k, false)
		h.cfg.Storage = "pvc"
		h.rt, _ = lookupRuntime(rt)
		if _, err := h.ensurePod(ctx, "w", sized, false, egressgw.Attachment{}, discard); err != nil {
			t.Fatalf("%s: %v", rt, err)
		}
		if got := k.obj(k.created, "PersistentVolumeClaim") != nil; got != claims {
			t.Errorf("%s: claim created = %v", rt, got)
		}
	}
	k := &kubeFake{secrets: map[string]string{}}
	_, h := k8sWith(t, k, false)
	if _, err := h.ensurePod(ctx, "w", envport.EnvSpec{Runtime: envport.RuntimeKata}, false, egressgw.Attachment{}, discard); err == nil ||
		!strings.Contains(err.Error(), "not approved") || len(k.created) != 0 {
		t.Errorf("unapproved: %v, created %d", err, len(k.created))
	}
}

// An integration may name another class for any runtime it approves; it
// wins over the default's override, and a VM pod is known by its class.
func TestRuntimeClassesPerRuntime(t *testing.T) {
	k := &kubeFake{secrets: map[string]string{}}
	h := New(Config{RunsDir: testRunsDir, Kube: goldenKubeConfig(k, func(c *KubeConfig) {
		c.Runtime, c.RuntimeClass = envport.RuntimeSysbox, "sysbox-runc-v2"
		c.Runtimes = []envport.Runtime{envport.RuntimeSysbox, envport.RuntimeKata}
		c.RuntimeClasses = map[envport.Runtime]string{envport.RuntimeKata: "kata-clh"}
	})}).host.(*k8sHost)
	sysbox, _ := lookupRuntime(envport.RuntimeSysbox)
	kata, _ := lookupRuntime(envport.RuntimeKata)
	if h.classFor(sysbox) != "sysbox-runc-v2" || h.classFor(kata) != "kata-clh" {
		t.Errorf("classes %q, %q", h.classFor(sysbox), h.classFor(kata))
	}
	h.cfg.RuntimeClasses[envport.RuntimeSysbox] = "sysbox-runc-v3"
	if h.classFor(sysbox) != "sysbox-runc-v3" {
		t.Errorf("a pair wins over the default's override: %q", h.classFor(sysbox))
	}
	pod := func(class string) kube.Pod { var p kube.Pod; p.Spec.RuntimeClassName = class; return p }
	if !h.isVMPod(pod("kata-clh")) || h.isVMPod(pod("kata")) || h.isVMPod(pod("sysbox-runc-v3")) {
		t.Error("a VM pod is the one under the VM runtime's class")
	}
}

// An engine outside the cluster reaches a VM sandbox's sockets through
// kubectl exec (port-forward cannot reach the guest); every other pod, and
// every pod from inside the cluster, through the relay's mux as before.
func TestOutOfClusterDialsAVMPodByExec(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "kubectl")
	// The relay's shim: the ack, then an echo; or nothing served.
	script := "#!/bin/sh\necho \"$@\" > " + filepath.Join(dir, "args") + "\n" +
		"case \"$*\" in *gone.sock*) exit 1;; esac\nprintf 'OK\\n'; exec cat\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	host := func(class string, forward bool) *k8sHost {
		k := &kubeFake{secrets: map[string]string{}, pod: `{"spec":{"runtimeClassName":"` + class + `"},"status":{"phase":"Running","podIP":"10.42.0.9"}}`}
		cfg := goldenKubeConfig(k, func(c *KubeConfig) {
			c.Runtime, c.Storage, c.Forward = envport.RuntimeSysbox, "emptydir", forward
			c.Runtimes = []envport.Runtime{envport.RuntimeKata}
		})
		cfg.Kube.Bin = bin
		return New(Config{RunsDir: testRunsDir, Kube: cfg}).host.(*k8sHost)
	}
	ctx := context.Background()

	h := host("kata", true)
	c, err := h.dial("w", "sidecar.sock").Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(c, "hi\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || line != "hi\n" {
		t.Errorf("echo = %q, %v", line, err)
	}
	c.Close()
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if want := "exec -i w -c relay -- /apothecary-shim relay --stdio-ack " + filepath.Join(h.a.folderFor("w"), "shim", "sidecar.sock"); !strings.Contains(string(args), want) {
		t.Errorf("kubectl %s, want %q", args, want)
	}
	if _, err := h.dial("w", "gone.sock").Dial(ctx); err == nil || !strings.Contains(err.Error(), "gone.sock is not being served") {
		t.Errorf("nothing served: %v", err)
	}

	for _, c := range []struct {
		class   string
		forward bool
	}{{"sysbox-runc", true}, {"kata", false}} {
		if vm, err := host(c.class, c.forward).viaExec(ctx, "w"); err != nil || vm {
			t.Errorf("%s, forward %v: by exec = %v, %v", c.class, c.forward, vm, err)
		}
	}
	h = host("kata", true)
	h.cfg.Kube.R = &kubeFake{secrets: map[string]string{}}
	if _, err := h.viaExec(ctx, "w"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("no pod: %v", err)
	}
}

// A runtime that nests in the pod's own user namespace with plain runc asks
// the kubelet for that namespace and is never privileged: its security
// context is the namespace's capabilities, not the node's.
func TestUsernsNestingRowsAreUnprivilegedAndOwnTheirNamespace(t *testing.T) {
	n := 0
	for name, rt := range sandboxRuntimes {
		if !rt.usernsNesting {
			continue
		}
		n++
		if !rt.ownUserNamespace || rt.privileged || rt.vm {
			t.Errorf("%s: %+v", name, rt)
		}
		if sc := rt.sandboxSecurity(); sc == nil || sc.Privileged != nil || sc.ProcMount != "Unmasked" {
			t.Errorf("%s: security %+v", name, sc)
		}
	}
	if n == 0 {
		t.Error("no row nests in a user namespace (userns)")
	}
}

// The userns sandbox: its class (or the integration's override), its own
// user namespace either way, the nesting security context and switch on the
// sandbox container alone; the tunnel keeps NET_ADMIN alone, with a
// /proc/sys it can write, and the seeds and the relay nothing.
func TestUsernsSandboxShape(t *testing.T) {
	ctx := context.Background()
	att := egressgw.Attachment{GatewayIP: "10.90.0.1", Subnet: "10.90.0.5/32", WGEndpoint: "10.43.9.9:51820", WGSecret: "apo-wg-apo-x"}
	for _, override := range []string{"", "runc-userns-v2"} {
		k := &kubeFake{secrets: map[string]string{}}
		h := New(Config{RunsDir: testRunsDir, Kube: goldenKubeConfig(k, func(c *KubeConfig) {
			c.Runtime = envport.RuntimeSysbox
			c.Runtimes = []envport.Runtime{envport.RuntimeUserns}
			if override != "" {
				c.RuntimeClasses = map[envport.Runtime]string{envport.RuntimeUserns: override}
			}
		})}).host.(*k8sHost)
		pod, err := h.podManifest(ctx, "w", envport.EnvSpec{Runtime: envport.RuntimeUserns}, att)
		if err != nil {
			t.Fatal(err)
		}
		if want := orDefault(override, "runc-userns"); pod.Spec.RuntimeClassName != want || pod.Spec.HostUsers == nil || *pod.Spec.HostUsers {
			t.Errorf("override %q: class %q, hostUsers %v", override, pod.Spec.RuntimeClassName, pod.Spec.HostUsers)
		}
		for _, c := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
			sc := c.SecurityContext
			nesting := slices.ContainsFunc(c.Env, func(e kube.EnvVar) bool { return e.Name == "APO_USERNS_NESTING" && e.Value == "1" })
			switch c.Name {
			case sandboxContainer:
				if sc == nil || sc.ProcMount != "Unmasked" || sc.SeccompProfile.Type != "Unconfined" || sc.AppArmorProfile.Type != "Unconfined" ||
					!slices.Equal(sc.Capabilities.Add, []string{"SYS_ADMIN", "NET_ADMIN", "MKNOD", "SYS_RESOURCE", "SYS_PTRACE"}) || !nesting {
					t.Errorf("sandbox: %+v, switch %v", sc, nesting)
				}
			case "wg":
				// NET_ADMIN alone, and a /proc/sys it can write: no SYS_ADMIN,
				// seccomp left as it is.
				if sc == nil || sc.Privileged != nil || sc.ProcMount != "Unmasked" || sc.AppArmorProfile == nil || sc.SeccompProfile != nil ||
					!slices.Equal(sc.Capabilities.Add, []string{"NET_ADMIN"}) {
					t.Errorf("wg: %+v", sc)
				}
			default:
				if sc != nil || nesting {
					t.Errorf("%s: %+v, switch %v", c.Name, sc, nesting)
				}
			}
		}
		if !h.contained(envport.RuntimeUserns) {
			t.Error("a userns sandbox is contained")
		}
	}
}

// The integration's flag widens the runtimes that nest with plain runc, and
// only those: sysbox maps its own 65536 whatever the node gives.
func TestNestedUserNamespaceAdds(t *testing.T) {
	if got := NestedUserNamespaceAdds(false); got != nil {
		t.Errorf("off = %v", got)
	}
	got := NestedUserNamespaceAdds(true)
	if len(got) != 1 || !got[envport.RuntimeUserns].UserNamespaces {
		t.Errorf("on = %v", got)
	}
}

// With the flag on, a provision says when the sandbox's node gave it 65536
// ids or fewer; with more, or the flag off, or another runtime, nothing.
func TestNestedIDsWarning(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		rt     envport.Runtime
		flag   bool
		uidMap string
		warns  bool
	}{
		{"a narrow node", envport.RuntimeUserns, true, "         0  658505728      65536\n", true},
		{"a wide node", envport.RuntimeUserns, true, "         0  658505728    4194304\n", false},
		{"the flag off", envport.RuntimeUserns, false, "0 658505728 65536\n", false},
		{"sysbox", envport.RuntimeSysbox, true, "0 658505728 65536\n", false},
		{"an unreadable map", envport.RuntimeUserns, true, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := &kubeFake{secrets: map[string]string{}, exec: func(argv []string, _ []byte) (string, string, int) {
				if slices.Contains(argv, "/proc/self/uid_map") {
					if c.uidMap == "" {
						return "", "no such file", 1
					}
					return c.uidMap, "", 0
				}
				return "", "", 0
			}}
			_, h := k8sWith(t, k, false)
			h.cfg.NestedUserNamespaces = c.flag
			rt, _ := lookupRuntime(c.rt)
			var log strings.Builder
			h.checkNestedIDs(ctx, "w", rt, func(f string, a ...any) { fmt.Fprintf(&log, f, a...) })
			if got := strings.Contains(log.String(), "gave it 65536 user ids"); got != c.warns {
				t.Errorf("warned %v: %q", got, log.String())
			}
		})
	}
	if n := mappedIDs("0 100 65536\n65536 1 2\nnot a line\n1 2 x\n"); n != 65538 {
		t.Errorf("mappedIDs = %d", n)
	}
}

// The Test button's pod under userns carries the sandbox's security context,
// so a cluster that refuses procMount: Unmasked fails the integration's test.
func TestUsernsProbePod(t *testing.T) {
	k := &kubeFake{secrets: map[string]string{}}
	h := New(Config{RunsDir: testRunsDir, Kube: goldenKubeConfig(k, func(c *KubeConfig) { c.Runtime = envport.RuntimeUserns })}).host.(*k8sHost)
	rt, _ := lookupRuntime(envport.RuntimeUserns)
	pod := h.probePod("p", rt)
	if pod.Spec.RuntimeClassName != "runc-userns" || pod.Spec.HostUsers == nil || *pod.Spec.HostUsers {
		t.Errorf("class %q, hostUsers %v", pod.Spec.RuntimeClassName, pod.Spec.HostUsers)
	}
	if sc := pod.Spec.Containers[0].SecurityContext; sc == nil || sc.ProcMount != "Unmasked" || sc.AppArmorProfile == nil {
		t.Errorf("probe security = %+v", sc)
	}
}
