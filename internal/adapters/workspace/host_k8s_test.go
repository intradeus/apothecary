package workspace

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/kube"
	"apothecary/internal/ports/envport"
	wsassets "apothecary/workspace"
)

// kubeFake plays kubectl: API verbs from state, `exec` through a handler that
// sees the in-pod argv (env prefix stripped) and stdin.
type kubeFake struct {
	mu      sync.Mutex
	calls   [][]string
	created []map[string]any
	applied []map[string]any
	deleted []string
	pod     string // `get pod` answer ("" = absent)
	secrets map[string]string
	exec    func(argv []string, stdin []byte) (string, string, int)
}

func (k *kubeFake) Run(_ context.Context, _ string, args []string, stdin []byte, tee io.Writer, _ ...string) ([]byte, []byte, int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls = append(k.calls, args)
	// Drop the global flags.
	for len(args) >= 2 && strings.HasPrefix(args[0], "--") && !strings.Contains(args[0], "=") {
		args = args[2:]
	}
	switch args[0] {
	case "exec":
		i := slices.Index(args, "--")
		argv := args[i+1:]
		if argv[0] == "env" {
			argv = argv[1:]
			for len(argv) > 0 && strings.Contains(argv[0], "=") {
				argv = argv[1:]
			}
		}
		out, errb, code := "", "", 0
		if k.exec != nil {
			out, errb, code = k.exec(argv, stdin)
		}
		if tee != nil {
			tee.Write([]byte(out))
		}
		if code != 0 {
			errb += "command terminated with exit code " + strconv.Itoa(code) + "\n"
		}
		return []byte(out), []byte(errb), code, nil
	case "create":
		var m map[string]any
		_ = json.Unmarshal(stdin, &m)
		k.created = append(k.created, m)
	case "apply":
		var m map[string]any
		_ = json.Unmarshal(stdin, &m)
		k.applied = append(k.applied, m)
	case "delete":
		k.deleted = append(k.deleted, args[1]+"/"+args[2])
	case "get":
		kind, name := args[2], args[3]
		switch kind {
		case "pod":
			return []byte(k.pod), nil, 0, nil
		case "secret":
			return []byte(k.secrets[name]), nil, 0, nil
		}
	}
	return nil, nil, 0, nil
}

func (k *kubeFake) obj(list []map[string]any, kind string) map[string]any {
	for _, m := range list {
		if m["kind"] == kind {
			return m
		}
	}
	return nil
}

const testRunsDir = "/var/lib/apothecary/runs"

func newK8sTestAdapter(t *testing.T, k *kubeFake, firewall bool) *Adapter {
	t.Helper()
	a := New(Config{
		RunsDir: t.TempDir(),
		Kube: &KubeConfig{
			Kube:         &kube.Client{Namespace: "ws", R: k},
			SandboxImage: "sbx:1", ShimImage: "shim:1", ToolsImage: "tools:1", GatewayImage: "gw:1",
			EngineURL:       "http://10.43.0.20:8080",
			EngineNamespace: "apothecary",
			Privileged:      true,
			Firewall:        firewall,
		},
	})
	a.retryDelay = time.Millisecond
	a.noSandboxGuard = true // t.TempDir() is under /tmp
	return a
}

func readyPod(ip string) string {
	return `{"metadata":{"name":"apo-x"},"spec":{"containers":[]},"status":{"phase":"Running","podIP":"` + ip + `","conditions":[{"type":"Ready","status":"True"}]}}`
}

// inPod answers the provision's in-pod commands: a fresh pod, a repo with a
// devcontainer.json, a shim of the engine's own sources.
func inPod(seen *[]string, stdins *[][]byte) func([]string, []byte) (string, string, int) {
	return func(argv []string, stdin []byte) (string, string, int) {
		line := strings.Join(argv, " ")
		*seen = append(*seen, line)
		*stdins = append(*stdins, stdin)
		switch {
		case argv[0] == "test" && argv[1] == "-d":
			return "", "", 1 // not cloned yet
		case argv[0] == "bash" && strings.Contains(string(stdin), "shim.hash") && strings.Contains(string(stdin), "base64 <"):
			return base64.StdEncoding.EncodeToString([]byte(wsassets.ShimSourceHash()+"\n")) + "\n", "", 0
		case argv[0] == "bash" && strings.Contains(string(stdin), "TOOLS_HASH") && strings.Contains(string(stdin), "base64 <"):
			return base64.StdEncoding.EncodeToString([]byte(wsassets.ToolsHash()+"\n")) + "\n", "", 0
		case argv[0] == "bash" && strings.Contains(string(stdin), "devcontainer.json") && strings.Contains(string(stdin), "[ -e"):
			return "", "", 0
		case argv[0] == "bash" && strings.Contains(string(stdin), "[ -e"):
			return "", "", 1
		case argv[0] == "devcontainer" && argv[1] == "up":
			return `{"outcome":"success","containerId":"cid1","remoteUser":"vscode","remoteWorkspaceFolder":"/workspaces/x"}` + "\n", "", 0
		case argv[0] == "docker" && argv[1] == "ps":
			return "cid1\n", "", 0
		}
		return "", "", 0
	}
}

func TestK8sProvisionSequence(t *testing.T) {
	k := &kubeFake{secrets: map[string]string{}}
	var seen []string
	var stdins [][]byte
	k.exec = inPod(&seen, &stdins)
	a := newK8sTestAdapter(t, k, false)
	env, err := a.Provision(context.Background(), envport.EnvSpec{
		RunID: "01R", Name: "apo-x", Project: "P1", ProjectName: "Proj",
		RepoURL: "https://git.example/r.git", GitUser: "u", GitPass: "s3cret",
	})
	if err != nil {
		t.Fatalf("provision: %v\nin-pod: %v", err, seen)
	}
	if env != "apo-x" {
		t.Fatalf("env = %q", env)
	}
	folder := a.folderFor("apo-x")

	// The credential never reaches an argv, only the clone's stdin.
	for _, c := range k.calls {
		if strings.Contains(strings.Join(c, " "), "s3cret") {
			t.Fatalf("credential in argv: %v", c)
		}
	}
	cloned := false
	for i, l := range seen {
		if l == "bash -s" && strings.Contains(string(stdins[i]), " clone -- ") {
			cloned = true
			lines := strings.Split(string(stdins[i]), "\n")
			if !strings.Contains(lines[1], "s3cret") || strings.Contains(lines[0], "s3cret") {
				t.Fatalf("clone stdin = %q", stdins[i])
			}
		}
	}
	if !cloned {
		t.Fatalf("no in-pod clone: %v", seen)
	}

	pod := k.obj(k.created, "Pod")
	if pod == nil {
		t.Fatal("no pod created")
	}
	if a.host.unprivileged() {
		t.Error("the default pod is privileged: not an unprivileged sandbox")
	}
	b, _ := json.Marshal(pod)
	ps := string(b)
	for _, want := range []string{
		`"name":"apo-x"`, `"apothecary.io/sandbox":"true"`, `"apothecary.io/project":"p1"`,
		`"privileged":true`, `"image":"sbx:1"`, `"command":["apo-dockerd"]`,
		`"mountPath":"` + folder + `","name":"data","subPath":"run"`, `"mountPath":"/var/lib/docker","name":"data","subPath":"docker"`,
		`"args":["seed","` + filepath.Join(folder, "shim") + `"]`,
		`"name":"tools-seed"`, `"image":"tools:1"`, `"args":["` + filepath.Join(folder, "tools") + `"]`,
		`"name":"relay"`, `"restartPolicy":"Always"`, `"--serve","0.0.0.0:7070"`,
		`"claimName":"apo-x-data"`, `"automountServiceAccountToken":false`,
	} {
		if !strings.Contains(ps, want) {
			t.Errorf("pod manifest missing %s\n%s", want, ps)
		}
	}
	if strings.Contains(ps, `"dnsPolicy"`) || strings.Contains(ps, `"name":"wg"`) {
		t.Errorf("firewall off but the pod is tunnelled: %s", ps)
	}
	if strings.Contains(ps, `"hostUsers"`) {
		t.Errorf("a privileged pod must not ask for a user namespace: %s", ps)
	}
	np := k.obj(k.applied, "NetworkPolicy")
	nb, _ := json.Marshal(np)
	if !strings.Contains(string(nb), `"policyTypes":["Ingress"]`) || !strings.Contains(string(nb), `"port":7070`) {
		t.Errorf("network policy = %s", nb)
	}
	var up string
	for _, l := range seen {
		if strings.HasPrefix(l, "devcontainer up") {
			up = l
		}
	}
	if !strings.Contains(up, "--mount type=bind,source="+filepath.Join(folder, "shim")+",target=/opt/apothecary ") ||
		!strings.Contains(up, "--mount type=bind,source="+filepath.Join(folder, "tools")+",target=/opt/apothecary/tools") ||
		!strings.Contains(up, "--workspace-folder "+filepath.Join(folder, "apo-x")) {
		t.Errorf("up = %s", up)
	}
	if strings.Contains(up, "--cache-from") {
		t.Errorf("build cache flags without a gateway: %s", up)
	}
	// The workspace is told the engine's Service, and readiness probes it.
	if got := a.EngineURL("apo-x"); got != "http://10.43.0.20:8080" {
		t.Errorf("EngineURL = %q", got)
	}
	probed := false
	for _, l := range seen {
		if strings.Contains(l, "/dev/tcp/10.43.0.20/8080") {
			probed = true
		}
	}
	if !probed {
		t.Errorf("readiness never probed the engine Service: %v", seen)
	}
}

func TestK8sPodWithGatewayIsTunnelled(t *testing.T) {
	k := &kubeFake{secrets: map[string]string{}}
	a := newK8sTestAdapter(t, k, true)
	h := a.host.(*k8sHost)
	pod, err := h.podManifest(context.Background(), "apo-x", envport.EnvSpec{Project: "P1"}, egressgw.Attachment{
		GatewayIP: "10.90.0.1", Subnet: "10.90.0.5/32", WGEndpoint: "10.43.9.9:51820", WGSecret: "apo-wg-apo-x"})
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.DNSPolicy != "None" || pod.Spec.DNSConfig.Nameservers[0] != "10.90.0.1" ||
		pod.Spec.DNSConfig.Searches[0] != "ws.svc.cluster.local" {
		t.Errorf("dns = %s %+v", pod.Spec.DNSPolicy, pod.Spec.DNSConfig)
	}
	var names []string
	for _, c := range pod.Spec.InitContainers {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "shim-seed,tools-seed,wg,relay" {
		t.Fatalf("init order = %v (the tunnel must be up before the relay and the sandbox)", names)
	}
	wg := pod.Spec.InitContainers[2]
	env := map[string]string{}
	for _, e := range wg.Env {
		env[e.Name] = e.Value
	}
	if env["GW_ENDPOINT"] != "10.43.9.9:51820" || env["GW_SVC_IP"] != "10.43.9.9" || env["ENGINE_IP"] != "10.43.0.20" ||
		wg.RestartPolicy != "Always" || wg.StartupProbe == nil {
		t.Errorf("wg container = %+v", wg)
	}
	found := false
	for _, e := range pod.Spec.Containers[0].Env {
		found = found || (e.Name == "APO_GATEWAY_IP" && e.Value == "10.90.0.1")
	}
	if !found {
		t.Error("sandbox not pointed at the gateway")
	}
	np, _ := h.networkPolicy("apo-x", "P1")
	b, _ := json.Marshal(np)
	for _, want := range []string{`"policyTypes":["Ingress","Egress"]`, `"apothecary.io/gateway":"p1"`, `"port":51820`,
		`"kubernetes.io/metadata.name":"apothecary"`, `"port":8080`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("network policy missing %s: %s", want, b)
		}
	}
}

// The seeded tools bundle is checked like the shim, with one difference: a
// bundle of another version is a warning (the harness versions differ,
// nothing of the engine's protocol), while a missing one is an error.
func TestK8sToolsHashMismatchWarnsMissingFails(t *testing.T) {
	k := &kubeFake{secrets: map[string]string{}}
	a := newK8sTestAdapter(t, k, false)
	h := a.host.(*k8sHost)
	var drift []error
	h.cfg.Drift = func(_ context.Context, part string, err error) {
		if part != "tools" {
			t.Errorf("drift part = %q, want tools", part)
		}
		drift = append(drift, err)
	}
	k.exec = func(argv []string, stdin []byte) (string, string, int) {
		return base64.StdEncoding.EncodeToString([]byte("0123456789abcdef\n")) + "\n", "", 0
	}
	if err := h.checkTools(context.Background(), "apo-x"); err != nil {
		t.Errorf("another bundle version must only warn: %v", err)
	}
	if len(drift) != 1 || drift[0] == nil {
		t.Errorf("mismatch must report drift once: %v", drift)
	}
	k.exec = func(argv []string, stdin []byte) (string, string, int) {
		return base64.StdEncoding.EncodeToString([]byte(wsassets.ToolsHash()+"\n")) + "\n", "", 0
	}
	if err := h.checkTools(context.Background(), "apo-x"); err != nil {
		t.Errorf("matching bundle: %v", err)
	}
	if len(drift) != 2 || drift[1] != nil {
		t.Errorf("match must report nil drift: %v", drift)
	}
	k.exec = func(argv []string, stdin []byte) (string, string, int) { return "", "", 44 }
	err := h.checkTools(context.Background(), "apo-x")
	if err == nil || !strings.Contains(err.Error(), "tools:1") {
		t.Errorf("a missing bundle must fail naming the image: %v", err)
	}
}

func TestK8sStatusMapping(t *testing.T) {
	cases := []struct {
		pod     string
		inner   string
		want    envport.EnvStatus
		wantErr bool
	}{
		{"", "", envport.EnvAbsent, false},
		{`{"status":{"phase":"Pending"}}`, "", envport.EnvStarting, false},
		{`{"status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"}]}}`, "", envport.EnvStarting, false},
		{readyPod("10.42.0.9"), "running", envport.EnvRunning, false},
		{readyPod("10.42.0.9"), "exited", envport.EnvStopped, false},
		{readyPod("10.42.0.9"), "", envport.EnvAbsent, false},
		{`{"status":{"phase":"Failed"}}`, "", envport.EnvStopped, false},
	}
	for _, c := range cases {
		k := &kubeFake{pod: c.pod, exec: func(argv []string, _ []byte) (string, string, int) {
			if argv[0] == "docker" && argv[1] == "ps" {
				return c.inner + "\n", "", 0
			}
			return "", "", 0
		}}
		a := newK8sTestAdapter(t, k, false)
		got, err := a.Status(context.Background(), "apo-x")
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("pod %s inner %q: got %s %v, want %s", c.pod, c.inner, got, err, c.want)
		}
	}
}

func TestK8sDestroyRemovesEverything(t *testing.T) {
	k := &kubeFake{}
	a := newK8sTestAdapter(t, k, false)
	if err := a.Destroy(context.Background(), "apo-x"); err != nil {
		t.Fatal(err)
	}
	want := []string{"pod/apo-x", "persistentvolumeclaim/apo-x-data", "networkpolicy/apo-run-apo-x", "secret/apo-run-apo-x"}
	if !slices.Equal(k.deleted, want) {
		t.Fatalf("deleted %v, want %v (pod first: a claim cannot go while mounted)", k.deleted, want)
	}
}

func TestK8sShimHashMismatchFails(t *testing.T) {
	k := &kubeFake{exec: func(argv []string, stdin []byte) (string, string, int) {
		return base64.StdEncoding.EncodeToString([]byte("0000000000000000\n")) + "\n", "", 0
	}}
	a := newK8sTestAdapter(t, k, false)
	err := a.host.(*k8sHost).checkShim(context.Background(), "apo-x")
	if err == nil || !strings.Contains(err.Error(), "shim image shim:1") {
		t.Fatalf("err = %v", err)
	}
}

func TestK8sDialThroughTheMux(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	old := relayPort
	relayPort = ln.Addr().(*net.TCPAddr).Port
	defer func() { relayPort = old }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				pre, _ := br.ReadString('\n')
				if pre != "APO1 tok sidecar.sock\n" {
					return // refused: closed, no ack
				}
				c.Write([]byte("OK\nhello"))
			}(c)
		}
	}()
	k := &kubeFake{pod: readyPod("127.0.0.1"), secrets: map[string]string{
		"apo-run-apo-x": `{"data":{"relay-token":"` + base64.StdEncoding.EncodeToString([]byte("tok")) + `"}}`}}
	a := newK8sTestAdapter(t, k, false)
	h := a.host.(*k8sHost)
	c, err := h.dial("apo-x", "sidecar.sock").Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(c)
	if string(b) != "hello" {
		t.Fatalf("got %q (the ack must not reach the caller)", b)
	}
	if _, err := h.dial("apo-x", "app-3000.sock").Dial(context.Background()); err == nil {
		t.Fatal("an unserved socket dialed fine")
	}
}

func TestParseCgroupSample(t *testing.T) {
	out := "cpu 2000000\nmem 104857600\nmax max\npids 42\nquota 200000 100000\nnproc 8\nnet 1000 2000\n"
	s, err := parseCgroupSample(out)
	if err != nil {
		t.Fatal(err)
	}
	if s.usageUS != 2000000 || s.memUsed != 104857600 || s.memLimit != 0 || s.pids != 42 || s.cpus != 2 || s.rx != 1000 || s.tx != 2000 {
		t.Fatalf("sample = %+v", s)
	}
	prev := s
	prev.at = time.Unix(100, 0)
	cur := s
	cur.at = time.Unix(101, 0)
	cur.usageUS += 500000 // half a core over one second
	if got := cpuSample(prev, cur); got.CPUPerc < 49.9 || got.CPUPerc > 50.1 || got.CPUs != 2 {
		t.Fatalf("cpu = %+v", got)
	}
	if _, err := parseCgroupSample("mem 1\n"); err == nil {
		t.Fatal("no cpu accepted")
	}
}
