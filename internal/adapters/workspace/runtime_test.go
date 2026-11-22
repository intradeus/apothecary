package workspace

import (
	"bufio"
	"context"
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
	if r := sandboxResources(envport.SandboxSize{}); r != nil {
		t.Errorf("unset = %+v", r)
	}
	r := sandboxResources(envport.SandboxSize{MilliCPU: 4000, MemoryMiB: 8192})
	if r.Limits["cpu"] != "4000m" || r.Requests["cpu"] != "1000m" || r.Limits["memory"] != "8192Mi" || r.Requests["memory"] != "8192Mi" {
		t.Errorf("4 CPUs, 8 GiB = %+v", r)
	}
	r = sandboxResources(envport.SandboxSize{MilliCPU: 200})
	if r.Requests["cpu"] != "100m" || r.Limits["cpu"] != "200m" || r.Limits["memory"] != "" {
		t.Errorf("0.2 CPU = %+v", r)
	}
	r = sandboxResources(envport.SandboxSize{MilliCPU: 50})
	if r.Requests["cpu"] != "50m" {
		t.Errorf("a request above its limit: %+v", r)
	}
	r = sandboxResources(envport.SandboxSize{MemoryMiB: 512})
	if _, ok := r.Limits["cpu"]; ok || r.Limits["memory"] != "512Mi" {
		t.Errorf("memory only = %+v", r)
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
		if spec["runtimeClassName"] != "sysbox-runc" || spec["hostUsers"] != false || spec["restartPolicy"] != "Never" {
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
}

// A VM runtime's sandbox: no subPath (a volume each for the run folder and
// docker), the /dev prelude asked for, privileged inside its VM, no
// hostUsers; refused unsized, or on claims.
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
	if _, err := h.podManifest(ctx, "w", sized, egressgw.Attachment{}); err == nil || !strings.Contains(err.Error(), "storage set to emptydir") {
		t.Errorf("pvc: %v", err)
	}
	// Other runtimes keep the one data volume, split by subPath.
	h.rt, _ = lookupRuntime(envport.RuntimeSysbox)
	pod, err = h.podManifest(ctx, "w", envport.EnvSpec{}, egressgw.Attachment{})
	if err != nil || pod.Spec.Containers[0].VolumeMounts[0].SubPath != "docker" {
		t.Errorf("sysbox mounts = %+v, %v", pod.Spec.Containers[0].VolumeMounts, err)
	}
}

func TestCheckStorage(t *testing.T) {
	for _, c := range []struct {
		rt      envport.Runtime
		storage string
		ok      bool
	}{
		{envport.RuntimeKata, "emptydir", true},
		{envport.RuntimeKata, "pvc", false},
		{envport.RuntimeSysbox, "pvc", true},
		{envport.RuntimePrivileged, "emptydir", true},
		{"gvisor", "emptydir", false},
	} {
		if err := CheckStorage(c.rt, c.storage); (err == nil) != c.ok {
			t.Errorf("%s on %s: %v", c.rt, c.storage, err)
		}
	}
	if got := DefaultStorage([]envport.Runtime{envport.RuntimeSysbox}); got != "pvc" {
		t.Errorf("sysbox defaults to %s", got)
	}
	if got := DefaultStorage([]envport.Runtime{envport.RuntimeSysbox, envport.RuntimeKata}); got != "emptydir" {
		t.Errorf("with kata: %s", got)
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
