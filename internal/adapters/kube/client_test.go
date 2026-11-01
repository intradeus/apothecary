package kube

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c := &Client{}
	if c.bin() != "kubectl" {
		t.Errorf("default bin = %q", c.bin())
	}
	if _, ok := c.runner().(ExecRunner); !ok {
		t.Errorf("default runner = %T", c.runner())
	}
	s := &script{}
	c = &Client{Bin: "/opt/kc", R: s}
	if c.bin() != "/opt/kc" || c.runner() != Runner(s) {
		t.Errorf("configured client = %s, %T", c.bin(), c.runner())
	}
	if got := (&Client{}).Global(); got != nil {
		t.Errorf("no flags expected: %v", got)
	}
}

func TestErrorTextAndNotFound(t *testing.T) {
	e := &Error{Args: []string{"get", "--x", "pod"}, Code: 1, Stderr: "  boom\n"}
	if got := e.Error(); got != "kubectl get --x: exit 1: boom" {
		t.Errorf("Error() = %q", got)
	}
	if one := (&Error{Args: []string{"version"}, Code: 2}).Error(); !strings.HasPrefix(one, "kubectl version: exit 2") {
		t.Errorf("one-arg Error() = %q", one)
	}
	if NotFound(e) {
		t.Error("boom is not NotFound")
	}
	if !NotFound(&Error{Stderr: `Error from server (NotFound): pods "x" not found`}) {
		t.Error("NotFound not recognised")
	}
	if NotFound(errors.New("NotFound")) {
		t.Error("a non-kubectl error is not NotFound, whatever it says")
	}
	if NotFound(nil) {
		t.Error("nil is not NotFound")
	}
}

func TestKubectlErrors(t *testing.T) {
	ctx := context.Background()
	spawn := errors.New("no such binary")
	s := &script{resps: []resp{{err: spawn}, {errb: "denied", code: 3}, {out: "ok"}}}
	c := &Client{Namespace: "ws", R: s}

	_, err := c.Kubectl(ctx, []string{"get", "pods", "-o", "json"}, nil)
	if !errors.Is(err, spawn) || !strings.Contains(err.Error(), "kubectl get pods:") {
		t.Errorf("spawn failure = %v", err)
	}
	_, err = c.Kubectl(ctx, []string{"get"}, []byte("in"))
	var ke *Error
	if !errors.As(err, &ke) || ke.Code != 3 || ke.Stderr != "denied" {
		t.Errorf("nonzero exit = %#v", err)
	}
	if out, err := c.Kubectl(ctx, []string{"get"}, nil); err != nil || string(out) != "ok" {
		t.Errorf("success = %q, %v", out, err)
	}
	if s.calls[1].args[0] != "--namespace" || string(s.calls[1].stdin) != "in" {
		t.Errorf("call = %+v", s.calls[1])
	}
}

func TestApplyIsServerSideAndForced(t *testing.T) {
	s := &script{resps: []resp{{}, {errb: "conflict", code: 1}}}
	c := &Client{R: s}
	if err := c.Apply(context.Background(), Secret{TypeMeta: SecretType()}); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(s.calls[0].args, " ")
	for _, want := range []string{"apply", "--server-side", "--field-manager=apothecary", "--force-conflicts", "-f -", requestTimeout} {
		if !strings.Contains(args, want) {
			t.Errorf("apply args missing %q: %s", want, args)
		}
	}
	if !strings.Contains(string(s.calls[0].stdin), `"kind":"Secret"`) {
		t.Errorf("manifest = %s", s.calls[0].stdin)
	}
	if err := c.Apply(context.Background(), Secret{}); err == nil {
		t.Error("a failed apply reported success")
	}
	if err := c.Apply(context.Background(), make(chan int)); err == nil {
		t.Error("an unmarshalable object reached kubectl")
	}
	if len(s.calls) != 2 {
		t.Errorf("calls = %d", len(s.calls))
	}
}

func TestCreateFailures(t *testing.T) {
	s := &script{resps: []resp{{errb: "Forbidden", code: 1}}}
	c := &Client{R: s}
	if err := c.Create(context.Background(), Pod{}); err == nil {
		t.Error("a refused create reported success")
	}
	if err := c.Create(context.Background(), make(chan int)); err == nil {
		t.Error("an unmarshalable object reached kubectl")
	}
	if len(s.calls) != 1 {
		t.Errorf("calls = %d", len(s.calls))
	}
}

func TestGetBadJSONIsAnError(t *testing.T) {
	s := &script{resps: []resp{{out: "{nope"}}}
	var p Pod
	if found, err := (&Client{R: s}).Get(context.Background(), "pod", "p", &p); err == nil || !found {
		t.Errorf("found=%v err=%v", found, err)
	}
	args := strings.Join(s.calls[0].args, " ")
	if !strings.Contains(args, "get "+requestTimeout+" pod p -o json --ignore-not-found") {
		t.Errorf("args = %s", args)
	}
}

func TestList(t *testing.T) {
	var list struct {
		Items []Secret `json:"items"`
	}
	ctx := context.Background()
	s := &script{resps: []resp{
		{out: `{"items":[{"metadata":{"name":"a"}}]}`},
		{out: `{"items":[]}`},
		{errb: "Unable to connect", code: 1},
		{out: "{nope"},
	}}
	c := &Client{R: s}
	if err := c.List(ctx, "secrets", "a=b", &list); err != nil || len(list.Items) != 1 || list.Items[0].Metadata.Name != "a" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if !strings.HasSuffix(strings.Join(s.calls[0].args, " "), "secrets -o json -l a=b") {
		t.Errorf("selector args = %v", s.calls[0].args)
	}
	if err := c.List(ctx, "secrets", "", &list); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(s.calls[1].args, " "), "-l") {
		t.Errorf("empty selector still filtered: %v", s.calls[1].args)
	}
	if err := c.List(ctx, "secrets", "", &list); err == nil {
		t.Error("API failure read as an empty list")
	}
	if err := c.List(ctx, "secrets", "", &list); err == nil {
		t.Error("garbage read as an empty list")
	}
}

func TestDeleteVariants(t *testing.T) {
	ctx := context.Background()
	s := &script{}
	c := &Client{R: s}
	if err := c.Delete(ctx, "pod", "p", true); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "secret", "s", false); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteSelected(ctx, "pods,secrets", "a=b"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"delete pod p --ignore-not-found --wait=true --timeout=120s",
		"delete secret s --ignore-not-found --wait=false " + requestTimeout,
		"delete pods,secrets -l a=b --ignore-not-found --wait=false " + requestTimeout,
	}
	for i, w := range want {
		if got := strings.Join(s.calls[i].args, " "); got != w {
			t.Errorf("call %d = %q, want %q", i, got, w)
		}
	}
	s.resps = []resp{{errb: "x", code: 1}}
	if err := c.Delete(ctx, "pod", "p", false); err == nil {
		t.Error("a failed delete reported success")
	}
}

func TestWaitsCarryTheirTimeout(t *testing.T) {
	s := &script{resps: []resp{{}, {errb: "timed out", code: 1}}}
	c := &Client{R: s}
	if err := c.WaitReady(context.Background(), "p", 90*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := c.RolloutStatus(context.Background(), "d", 3*time.Minute); err == nil {
		t.Error("a failed rollout reported success")
	}
	if got := strings.Join(s.calls[0].args, " "); got != "wait --for=condition=Ready pod/p --timeout=90s" {
		t.Errorf("wait = %q", got)
	}
	if got := strings.Join(s.calls[1].args, " "); got != "rollout status deployment/d --timeout=180s" {
		t.Errorf("rollout = %q", got)
	}
}

func TestCanISpawnFailure(t *testing.T) {
	boom := errors.New("gone")
	s := &script{resps: []resp{{err: boom}}}
	if ok, err := (&Client{Namespace: "ws", R: s}).CanI(context.Background(), "get", "pods"); ok || !errors.Is(err, boom) {
		t.Errorf("CanI = %v, %v", ok, err)
	}
	if got := strings.Join(s.calls[0].args, " "); got != "--namespace ws auth can-i get pods" {
		t.Errorf("args = %q", got)
	}
}

func TestVersion(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		r    resp
		want string
		bad  bool
	}{
		{"answered", resp{out: `{"serverVersion":{"gitVersion":"v1.31.0"}}`}, "v1.31.0", false},
		{"client only", resp{out: `{"clientVersion":{"gitVersion":"v1.31.0"}}`}, "", true},
		{"garbage", resp{out: "nope"}, "", true},
		{"unreachable", resp{errb: "Unable to connect", code: 1}, "", true},
	}
	for _, c := range cases {
		got, err := (&Client{R: &script{resps: []resp{c.r}}}).Version(ctx)
		if got != c.want || (err != nil) != c.bad {
			t.Errorf("%s: %q, %v", c.name, got, err)
		}
	}
}

// ExecRunner is exercised on sh, never kubectl: what it owes every caller is
// the exit-code contract, which any process shows.
func TestExecRunnerContract(t *testing.T) {
	ctx := context.Background()
	var r ExecRunner
	var tee bytes.Buffer
	out, errb, code, err := r.Run(ctx, "sh", []string{"-c", `cat; echo "$APO_T"; echo oops >&2; exit 7`}, []byte("in\n"), &tee, "APO_T=val")
	if err != nil || code != 7 || string(out) != "in\nval\n" || string(errb) != "oops\n" {
		t.Errorf("run = %q %q %d %v", out, errb, code, err)
	}
	if !strings.Contains(tee.String(), "val") || !strings.Contains(tee.String(), "oops") {
		t.Errorf("tee = %q", tee.String())
	}
	if _, _, code, err := r.Run(ctx, "sh", []string{"-c", "exit 0"}, nil, nil); err != nil || code != 0 {
		t.Errorf("clean = %d, %v", code, err)
	}
	if _, _, _, err := r.Run(ctx, filepath.Join(t.TempDir(), "absent"), nil, nil, nil); err == nil {
		t.Error("a process that never ran must be an error, not an exit code")
	}
}

func TestPodRunnerSpawnFailureAndNoContainer(t *testing.T) {
	boom := errors.New("spawn")
	s := &script{resps: []resp{{err: boom}}}
	r := PodRunner{C: &Client{R: s}, Pod: "p"}
	if _, _, _, err := r.Run(context.Background(), "sh", nil, nil, io.Discard); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	if got := strings.Join(r.ExecArgs(false, nil, []string{"ls"}), " "); got != "exec --pod-running-timeout=5s p -- ls" {
		t.Errorf("argv = %q", got)
	}
	te := &TransportError{Code: 1, Stderr: " Unable to connect\n"}
	if te.Error() != "kubectl exec: exit 1: Unable to connect" {
		t.Errorf("Error() = %q", te.Error())
	}
}

// Every sign of kubectl failing by itself is a transport error, retried by
// callers; none of them may read as the command's own failure.
func TestPodRunnerTransportSigns(t *testing.T) {
	for _, sign := range transportSigns {
		s := &script{resps: []resp{{errb: "x: " + sign + "\n", code: 1}}}
		_, _, _, err := PodRunner{C: &Client{R: s}, Pod: "p"}.Run(context.Background(), "sh", nil, nil, nil)
		var te *TransportError
		if !errors.As(err, &te) {
			t.Errorf("%q: err = %v", sign, err)
		}
	}
}

// The exit marker wins over a transport sign in the command's own stderr: a
// command that printed "connection refused" and failed is a result.
func TestPodRunnerRemoteFailureMentioningATransportSign(t *testing.T) {
	s := &script{resps: []resp{{errb: "curl: connection refused\ncommand terminated with exit code 7\n", code: 7}}}
	_, errb, code, err := PodRunner{C: &Client{R: s}, Pod: "p"}.Run(context.Background(), "sh", nil, nil, nil)
	if err != nil || code != 7 || string(errb) != "curl: connection refused\n" {
		t.Errorf("errb=%q code=%d err=%v", errb, code, err)
	}
}

type chunkTee struct{ bytes.Buffer }

func TestFilterTeeKeepsPartialLinesAndDropsTheMarker(t *testing.T) {
	var w chunkTee
	f := &filterTee{w: &w}
	for _, chunk := range []string{"hel", "lo\nwor", "ld\ncommand termin", "ated with exit code 3\n", "tail"} {
		if n, err := f.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	if w.String() != "hello\nworld\n" {
		t.Errorf("before flush = %q (a partial line must wait for its newline)", w.String())
	}
	f.flush()
	if w.String() != "hello\nworld\ntail" {
		t.Errorf("after flush = %q", w.String())
	}
	f.flush()
	if w.String() != "hello\nworld\ntail" {
		t.Errorf("second flush wrote again: %q", w.String())
	}
	// A marker that never got its newline is still kubectl talking.
	var w2 chunkTee
	f2 := &filterTee{w: &w2}
	f2.Write([]byte("command terminated with exit code 1"))
	f2.flush()
	if w2.Len() != 0 {
		t.Errorf("marker leaked: %q", w2.String())
	}
}

func TestPodStatusReady(t *testing.T) {
	cases := []struct {
		conds []PodCondition
		want  bool
	}{
		{nil, false},
		{[]PodCondition{{Type: "Initialized", Status: "True"}}, false},
		{[]PodCondition{{Type: "Ready", Status: "False"}}, false},
		{[]PodCondition{{Type: "Scheduled", Status: "True"}, {Type: "Ready", Status: "True"}}, true},
	}
	for _, c := range cases {
		if got := (PodStatus{Conditions: c.conds}).Ready(); got != c.want {
			t.Errorf("%+v = %v", c.conds, got)
		}
	}
}

func TestManifestHelpers(t *testing.T) {
	for want, got := range map[string]TypeMeta{
		"v1/Pod":                             PodType(),
		"v1/PersistentVolumeClaim":           PVCType(),
		"v1/Secret":                          SecretType(),
		"v1/Service":                         ServiceType(),
		"apps/v1/Deployment":                 DeploymentType(),
		"networking.k8s.io/v1/NetworkPolicy": NetworkPolicyType(),
	} {
		if got.APIVersion+"/"+got.Kind != want {
			t.Errorf("%s = %+v", want, got)
		}
	}
	if p := Ptr(int32(3)); *p != 3 {
		t.Errorf("Ptr = %d", *p)
	}
	if s := NamespaceSelector("ws"); s.MatchLabels["kubernetes.io/metadata.name"] != "ws" {
		t.Errorf("selector = %+v", s)
	}
}

func TestKubeconfigValues(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	dir := filepath.Join(t.TempDir(), "kc")
	// Whitespace is no value at all, not content.
	if p, cleanup, err := Kubeconfig(dir, " \n "); p != "" || err != nil {
		t.Errorf("blank = %q, %v", p, err)
	} else {
		cleanup()
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("a blank value created the directory")
	}
	// A leading slash alone does not make a path: a colon or a newline is YAML.
	for _, content := range []string{"/not: a path", "/first\nsecond"} {
		p, cleanup, err := Kubeconfig(dir, content)
		if err != nil || p == content || filepath.Dir(p) != dir {
			t.Errorf("%q = %q, %v", content, p, err)
			continue
		}
		b, _ := os.ReadFile(p)
		if string(b) != content+"\n" {
			t.Errorf("written = %q", b)
		}
		cleanup()
	}
	// The name is a digest of the content: same content, same file.
	a, _, _ := Kubeconfig(dir, "one")
	b, _, _ := Kubeconfig(dir, "one")
	c, _, _ := Kubeconfig(dir, "two")
	if a != b || a == c {
		t.Errorf("paths: %q %q %q", a, b, c)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("dir = %v, %v", info, err)
	}
}

func TestKubeconfigUnwritableDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if p, cleanup, err := Kubeconfig(filepath.Join(file, "sub"), "content"); err == nil || p != "" {
		t.Errorf("under a file = %q, %v", p, err)
	} else {
		cleanup() // always safe to call
	}
	// The directory exists but a directory sits at the file's name.
	dir := t.TempDir()
	p, _, err := Kubeconfig(dir, "content")
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(p)
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Kubeconfig(dir, "content"); err == nil {
		t.Error("write onto a directory succeeded")
	}
}

// Outside a cluster the service-account env being half-set changes nothing.
func TestKubeconfigInClusterNeedsItsEnvAndToken(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	if p, _, err := Kubeconfig(t.TempDir(), ""); p != "" || err != nil {
		t.Errorf("no port = %q, %v", p, err)
	}
	if _, err := os.Stat(filepath.Join(saDir, "token")); err == nil {
		t.Skip("running inside a pod: the service account is real")
	}
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	if p, _, err := Kubeconfig(t.TempDir(), ""); p != "" || err != nil {
		t.Errorf("no token = %q, %v", p, err)
	}
}

// In a pod, an empty setting becomes an explicit kubeconfig: kubectl's own
// fallback is skipped once --request-timeout is passed. The token stays a
// reference, since it rotates.
func TestKubeconfigInCluster(t *testing.T) {
	old := saDir
	saDir = t.TempDir()
	t.Cleanup(func() { saDir = old })
	if err := os.WriteFile(filepath.Join(saDir, "token"), []byte("s3cret-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBERNETES_SERVICE_HOST", "fd00::1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "6443")
	dir := filepath.Join(t.TempDir(), "kc")
	p, cleanup, err := Kubeconfig(dir, "")
	if err != nil || p != filepath.Join(dir, "in-cluster.kubeconfig") {
		t.Fatalf("path = %q, %v", p, err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(b)
	for _, want := range []string{"server: https://[fd00::1]:6443", "certificate-authority: " + saDir + "/ca.crt", "tokenFile: " + saDir + "/token"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("kubeconfig missing %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "s3cret-token") {
		t.Error("the token was copied into the kubeconfig")
	}
	if info, _ := os.Stat(p); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode())
	}
	// The generated file is kept: nothing is the engine's to clean up.
	cleanup()
	if _, err := os.Stat(p); err != nil {
		t.Errorf("cleanup removed the shared kubeconfig: %v", err)
	}
	// A kubeconfig the engine must have but cannot write is an error, not a
	// silent fall back to localhost:8080.
	file := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if p, _, err := Kubeconfig(filepath.Join(file, "sub"), ""); err == nil {
		t.Errorf("unwritable dir = %q", p)
	}
	blocked := t.TempDir()
	if err := os.Mkdir(filepath.Join(blocked, "in-cluster.kubeconfig"), 0o700); err != nil {
		t.Fatal(err)
	}
	if p, _, err := Kubeconfig(blocked, ""); err == nil {
		t.Errorf("unwritable file = %q", p)
	}
}
