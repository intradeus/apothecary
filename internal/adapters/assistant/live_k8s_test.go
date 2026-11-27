//go:build live

package assistant

// Live test of a person's assistant pod against a real cluster (k3d): phase
// 2's "done when" (docs/assistant-plan.md). The pod comes up beside the
// person's gateway; a fetch clones a public repository through the gateway,
// the repository host open for its length only; a confined session on each
// harness reads a file of it with its own read tool and its history with
// the git tool, answering a fake model run beside it in the cluster
// (testdata/fakemodel, image live-model:local); the pod is
// stopped for idleness and comes back on the same volume.
//
//   - APOTHECARY_LIVE_KUBECONFIG: a kubeconfig for a THROWAWAY cluster. The
//     test creates and deletes its namespace (apothecary-live-assistant).
//   - APOTHECARY_LIVE_IMAGE_PREFIX: where this tree's shim, assistant,
//     tools, egress-gw and live-model images are (tag :local).
//   - APOTHECARY_LIVE_PULL_POLICY (default Always): IfNotPresent when they
//     were imported into the nodes.
//   - APOTHECARY_LIVE_HOST_GATEWAY: the cluster network's gateway on the
//     docker host: the engine's address in the pod's network policy.
//   - APOTHECARY_LIVE_KEEP=1 leaves the namespace for inspection.
//
// It reaches github.com (the fetch): the cluster needs the internet.

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/egressgw/policyfile"
	"apothecary/internal/adapters/kube"
	"apothecary/internal/platform/livetest"
	"apothecary/internal/ports/assistantport"
	"apothecary/internal/workspace/sidecar"
)

const liveRepoURL = "https://github.com/octocat/Hello-World.git"

func TestLiveK8sAssistantPod(t *testing.T) {
	kc := os.Getenv("APOTHECARY_LIVE_KUBECONFIG")
	prefix := os.Getenv("APOTHECARY_LIVE_IMAGE_PREFIX")
	// The task passes candidates; the first is an address the pod routes.
	hostIP := strings.Fields(os.Getenv("APOTHECARY_LIVE_HOST_GATEWAY") + " ")[0]
	if kc == "" || prefix == "" || os.Getenv("APOTHECARY_LIVE_HOST_GATEWAY") == "" {
		livetest.Skip(t, "needs APOTHECARY_LIVE_KUBECONFIG, APOTHECARY_LIVE_IMAGE_PREFIX and APOTHECARY_LIVE_HOST_GATEWAY: see the header comment")
	}
	pull := os.Getenv("APOTHECARY_LIVE_PULL_POLICY")
	if pull == "" {
		pull = "Always"
	}
	const ns = "apothecary-live-assistant"
	_ = exec.Command("kubectl", "--kubeconfig", kc, "delete", "namespace", ns, "--wait=true").Run()
	if out, err := exec.Command("kubectl", "--kubeconfig", kc, "create", "namespace", ns).CombinedOutput(); err != nil {
		t.Fatalf("create namespace: %v %s", err, out)
	}
	if os.Getenv("APOTHECARY_LIVE_KEEP") != "1" {
		defer exec.Command("kubectl", "--kubeconfig", kc, "delete", "namespace", ns, "--wait=false").Run()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	c := &kube.Client{Kubeconfig: kc, Namespace: ns}
	if ok, why := userNamespaces(ctx, t, c, prefix+"/egress-gw:local", pull); !ok {
		livetest.Skip(t, "this cluster cannot make a pod's user namespace, and the assistant pod has one (hostUsers: false): %s. "+
			"A k3d cluster on a docker daemon that is itself in a user namespace cannot (a sysbox workspace; a userns one "+
			"unless its provisioner's nested_user_namespaces is on, docs/kubernetes.md): "+
			"see docs/notes/2026-11-20-live-suites-skip-loudly-and-never-in-ci.md. CI's sandbox-test (k3d) runs this test", why)
	}

	repo := assistantport.Repo{Workshop: "01LIVEWORKSHOP", WorkshopName: "live", Name: "hello", URL: liveRepoURL}
	modelIP := deployModel(ctx, t, c, prefix+"/live-model:local", pull, map[string]string{
		"FILE": Folder(repo) + "/README", "REPO": slug(repo.Name), "SIBLING": "/state/live-other/secret.txt"})
	modelURL := "http://" + net.JoinHostPort(modelIP, "8080")
	modelLogs := func() string {
		out, _ := exec.Command("kubectl", "--kubeconfig", kc, "-n", ns, "logs", "live-model", "--tail", "40").CombinedOutput()
		return string(out)
	}
	gwm := egressgw.NewManager(&egressgw.K8sHost{Kube: c, Image: prefix + "/egress-gw:local", PullPolicy: pull, Forward: true, StorageSize: "1Gi"})
	host := NewK8sHost(Config{Kube: c, Image: prefix + "/assistant:local", ShimImage: prefix + "/shim:local",
		ToolsImage: prefix + "/tools:local", TunnelImage: prefix + "/egress-gw:local", PullPolicy: pull,
		VolumeSize: "3Gi", EngineCIDR: hostIP + "/32", Forward: true})
	m := &Manager{Host: host, Image: prefix + "/assistant:local", Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		Gateway: EgressGateway{M: gwm, EngineIP: func(context.Context) (string, error) { return hostIP, nil },
			// The fake model is a Service, on a private address and port.
			Base: policyfile.File{AllowedCIDRs: []string{modelIP + "/32"}, AllowedPorts: []int{8080}}}}
	const person = "usr-live"

	start := time.Now()
	if err := m.Ensure(ctx, person, nil); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	t.Logf("pod ready in %s", time.Since(start).Round(time.Second))

	// Before a fetch the repository host is refused; during one it is not.
	if _, _, code, _ := host.Exec(ctx, person, gitContainer, []string{gitBin, "ls-remote", liveRepoURL}, nil); code == 0 {
		t.Error("the repository host was reachable outside a fetch")
	}
	start = time.Now()
	states, err := m.Fetch(ctx, person, []assistantport.FetchItem{{Repo: repo}})
	if err != nil || len(states) != 1 || !states[0].OK {
		t.Fatalf("fetch = %+v, %v", states, err)
	}
	t.Logf("fetched %s at %s in %s", repo.URL, states[0].Head, time.Since(start).Round(time.Second))
	if _, _, code, _ := host.Exec(ctx, person, gitContainer, []string{gitBin, "ls-remote", liveRepoURL}, nil); code == 0 {
		t.Error("the repository host stayed reachable after the fetch")
	}
	// The agent container reads the clone and cannot write it.
	if _, _, code, _ := host.Exec(ctx, person, agentContainer, []string{gitBin, "-C", Folder(repo), "log", "-1"}, nil); code != 0 {
		t.Error("the agent container cannot read the clone")
	}
	if _, _, code, _ := host.Exec(ctx, person, agentContainer, []string{gitBin, "-C", Folder(repo), "commit", "--allow-empty", "-m", "x"}, nil); code == 0 {
		t.Error("the agent container wrote the clone")
	}
	if _, _, code, _ := host.Exec(ctx, person, agentContainer, []string{"/bin/sh", "-c", "true"}, nil); code == 0 {
		t.Error("the agent container has a shell")
	}

	for _, h := range liveHarnesses(modelURL) {
		t.Run(h.name, func(t *testing.T) { liveSession(ctx, t, m, person, repo, h, modelLogs) })
	}

	// Idle: the pod goes, the volume stays, the next Ensure brings it back
	// on the same clone.
	m.IdleAfter = time.Nanosecond
	m.Sweep(ctx)
	if _, _, found, _ := host.Inspect(ctx, person); found {
		// Deletion is asynchronous: wait for it.
		deadline := time.Now().Add(2 * time.Minute)
		for found && time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
			_, _, found, _ = host.Inspect(ctx, person)
		}
		if found {
			t.Fatal("the idle pod was not stopped")
		}
	}
	m.IdleAfter = 0
	start = time.Now()
	if err := m.Ensure(ctx, person, nil); err != nil {
		t.Fatalf("ensure again: %v", err)
	}
	t.Logf("pod back on its volume in %s", time.Since(start).Round(time.Second))
	again, err := m.Fetch(ctx, person, []assistantport.FetchItem{{Repo: repo}})
	if err != nil || !again[0].OK {
		t.Fatalf("fetch after the restart = %+v, %v", again, err)
	}
	out, _, _, _ := host.Exec(ctx, person, agentContainer, []string{gitBin, "-C", Folder(repo), "rev-parse", "HEAD"}, nil)
	if strings.TrimSpace(string(out)) != states[0].Head {
		t.Errorf("after the restart the clone is at %q, before %q", out, states[0].Head)
	}
}

type liveHarness struct {
	name  string
	argv  []string
	exec  []string
	env   map[string]string
	model string
	// mode is the session's mode: claude-code's default (auto) asks a
	// classifier model before each tool, which a fake model cannot be.
	mode string
	// files are written into the session's home first: name → content.
	files map[string]string
}

func liveHarnesses(modelURL string) []liveHarness {
	return []liveHarness{
		{name: "claude-code", argv: []string{toolsMusl + "/libexec/claude-agent-acp"}, exec: []string{toolsMusl + "/libexec/claude"}, mode: "bypassPermissions",
			env: map[string]string{"ANTHROPIC_BASE_URL": modelURL, "ANTHROPIC_API_KEY": "sk-ant-live", "CLAUDE_CODE_EXECUTABLE": toolsMusl + "/libexec/claude",
				"IS_SANDBOX": "1", "DISABLE_AUTOUPDATER": "1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "ENABLE_TOOL_SEARCH": "false"}},
		{name: "opencode", argv: []string{toolsMusl + "/libexec/opencode", "acp"}, model: "anthropic/acme-1",
			env:   map[string]string{"ANTHROPIC_API_KEY": "sk-ant-live"},
			files: map[string]string{".config/opencode/opencode.json": `{"provider":{"anthropic":{"options":{"baseURL":"` + modelURL + `/v1"},"models":{"acme-1":{"name":"Acme 1","tool_call":true}}}}}`}},
	}
}

func liveSession(ctx context.Context, t *testing.T, m *Manager, person string, repo assistantport.Repo, h liveHarness, modelLogs func() string) {
	sid := "live-" + h.name
	for name, content := range h.files {
		if err := m.WriteFile(ctx, person, Home(sid)+"/"+name, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	req, err := m.StartRequest(person, assistantport.Session{ID: sid, Repos: []assistantport.Repo{repo}, Argv: h.argv, Exec: h.exec, Env: h.env})
	if err != nil {
		t.Fatal(err)
	}
	ah, err := m.AgentHost(ctx, person)
	if err != nil {
		t.Fatal(err)
	}
	cl := sidecar.NewWithHeader(ah.Dialer, assistantport.AgentHostTokenHeader, ah.Token)
	// Without the token the agent host answers nothing.
	if _, err := sidecar.New(ah.Dialer).Health(ctx); err == nil {
		t.Error("the agent host answered a request without its token")
	}
	start := time.Now()
	st, err := cl.Start(ctx, sidecar.StartRequest{Argv: req.Argv, Env: req.Env, Cwd: req.Cwd, Confine: &sidecar.Confine{
		Read: req.Confine.Read, Write: req.Confine.Write, Exec: req.Confine.Exec, Devices: req.Confine.Devices}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	cmd, args := GitServer(sid, []assistantport.Repo{repo})
	sess, err := cl.Session(ctx, sidecar.SessionRequest{Proc: st.Proc, Cwd: req.Cwd, Model: h.model, Mode: h.mode,
		MCPServers: []sidecar.MCPServer{{Name: "git", Command: cmd, Args: args}}})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Logf("%s session ready in %s", h.name, time.Since(start).Round(time.Millisecond))
	ask := func(text string) string {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		turn := fmt.Sprintf("t%d", time.Now().UnixNano())
		evs, errs, err := cl.Events(ctx, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := cl.Prompt(ctx, sidecar.PromptRequest{SessionID: sess.SessionID, Turn: turn, Text: text}); err != nil {
			t.Fatalf("prompt: %v", err)
		}
		var said strings.Builder
		var seen []string
		for {
			select {
			case ev, ok := <-evs:
				if !ok {
					t.Fatalf("the event stream ended; saw %v", seen)
				}
				if ev.Session == sess.SessionID || ev.Turn == turn || ev.Type == "agent_exit" {
					seen = append(seen, ev.Type+":"+ev.Turn+":"+trim(ev.Text+ev.Error, 300))
				}
				if ev.Turn != turn {
					continue
				}
				switch ev.Type {
				case "message":
					said.WriteString(ev.Text)
				case "turn_end":
					if ev.Error != "" {
						t.Fatalf("turn failed: %s", ev.Error)
					}
					return said.String()
				}
			case err := <-errs:
				t.Fatalf("events: %v", err)
			case <-ctx.Done():
				t.Fatalf("no turn end; events %v; the model's log:\n%s", seen, modelLogs())
			}
		}
	}
	if got := ask("QUESTION_READ: what does the README say?"); !strings.Contains(got, "Hello World") {
		t.Errorf("%s read: %q", h.name, got)
	}
	if got := ask("QUESTION_HISTORY: when did the README change?"); !strings.Contains(got, "first commit") {
		t.Errorf("%s history: %q", h.name, got)
	}
	// Another session's state is out of reach, whatever the model asks.
	if err := m.WriteFile(ctx, person, "/state/live-other/secret.txt", []byte("TOP-SECRET-MARKER"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ask("QUESTION_SIBLING: read the other session's file"); strings.Contains(got, "TOP-SECRET-MARKER") {
		t.Errorf("%s read another session's state: %q", h.name, got)
	}
}

// userNamespaces starts the smallest user-namespaced pod and reports whether
// it ran. The assistant pod is one (hostUsers: false); a node that cannot
// make the namespace leaves the pod at Init for good, with only a sandbox
// event to say why, so the test asks first and quotes the event. Any other
// failure of the probe fails the test: only this one is the environment's.
func userNamespaces(ctx context.Context, t *testing.T, c *kube.Client, image, pull string) (bool, string) {
	t.Helper()
	const name = "live-userns"
	pod := kube.Pod{TypeMeta: kube.PodType(), Metadata: kube.ObjectMeta{Name: name},
		Spec: kube.PodSpec{RestartPolicy: "Never", HostUsers: ptr(false), AutomountServiceAccountToken: ptr(false),
			Containers: []kube.Container{{Name: "probe", Image: image, ImagePullPolicy: pull, Command: []string{"true"}}}}}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatalf("the user-namespace probe: %v", err)
	}
	defer c.Delete(context.WithoutCancel(ctx), "pod", name, false)
	last := ""
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		var p kube.Pod
		if _, err := c.Get(ctx, "pod", name, &p); err != nil {
			t.Fatalf("the user-namespace probe: %v", err)
		}
		switch p.Status.Phase {
		case "Succeeded":
			return true, ""
		case "Failed":
			t.Fatalf("the user-namespace probe failed: %+v", p.Status)
		}
		w, err := c.LastWarning(ctx, name)
		if err != nil {
			t.Fatalf("the user-namespace probe's events: %v", err)
		}
		if userNamespaceRefused(w) {
			return false, w
		}
		last = w
	}
	t.Fatalf("the user-namespace probe neither ran nor was refused in 2 minutes; its last warning: %q", last)
	return false, ""
}

// deployModel runs the fake model (testdata/fakemodel) as a pod and a
// Service, and returns the Service's address.
func deployModel(ctx context.Context, t *testing.T, c *kube.Client, image, pull string, env map[string]string) string {
	t.Helper()
	var vars []kube.EnvVar
	for k, v := range env {
		vars = append(vars, kube.EnvVar{Name: k, Value: v})
	}
	labels := map[string]string{"app": "live-model"}
	pod := kube.Pod{TypeMeta: kube.PodType(), Metadata: kube.ObjectMeta{Name: "live-model", Labels: labels},
		Spec: kube.PodSpec{Containers: []kube.Container{{Name: "model", Image: image, ImagePullPolicy: pull, Env: vars,
			Ports: []kube.ContainerPort{{Name: "http", ContainerPort: 8080}}}}}}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	svc := kube.Service{TypeMeta: kube.ServiceType(), Metadata: kube.ObjectMeta{Name: "live-model"},
		Spec: kube.ServiceSpec{Selector: labels, Ports: []kube.ServicePort{{Name: "http", Port: 8080, TargetPort: 8080}}}}
	if err := c.Apply(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitReady(ctx, "live-model", 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	var got kube.Service
	if _, err := c.Get(ctx, "service", "live-model", &got); err != nil || got.Spec.ClusterIP == "" {
		t.Fatalf("the model's service: %+v, %v", got, err)
	}
	return got.Spec.ClusterIP
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
