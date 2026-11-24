package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/kube"
	"apothecary/internal/ports/envport"
)

func TestSizeHints(t *testing.T) {
	for _, c := range []struct {
		limits envport.SandboxSize
		want   map[string]string
	}{
		{envport.SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}, map[string]string{"APO_CPUS": "4", "APO_MEMORY_MIB": "8192"}},
		{envport.SandboxSize{MilliCPU: 1500}, map[string]string{"APO_CPUS": "2"}}, // rounded up
		{envport.SandboxSize{MilliCPU: 100}, map[string]string{"APO_CPUS": "1"}},  // never 0
		{envport.SandboxSize{MemoryMiB: 512}, map[string]string{"APO_MEMORY_MIB": "512"}},
		{envport.SandboxSize{}, map[string]string{}}, // no limit, no hint
	} {
		if got := sizeHints(c.limits); !equalMaps(got, c.want) {
			t.Errorf("sizeHints(%+v) = %v, want %v", c.limits, got, c.want)
		}
	}
}

func TestComposeEnv(t *testing.T) {
	hints := sizeHints(envport.SandboxSize{MilliCPU: 4000, MemoryMiB: 8192})
	project := map[string]string{
		"NX_PARALLEL":      "${APO_CPUS}",
		"NODE_OPTIONS":     "--max-old-space-size=${APO_MEMORY_MIB}",
		"JEST_MAX_WORKERS": "2",
		"LITERAL":          "${HOME} $APO_CPUS ${APO_CPUS",
	}
	got := composeEnv(hints, project)
	want := []string{
		"APO_CPUS=4", "APO_MEMORY_MIB=8192", // hints first: the project's come later and win in `env`
		"JEST_MAX_WORKERS=2",
		"LITERAL=${HOME} $APO_CPUS ${APO_CPUS", // only the two hints, spelled ${…}, expand
		"NODE_OPTIONS=--max-old-space-size=8192",
		"NX_PARALLEL=4",
	}
	if !slices.Equal(got, want) {
		t.Errorf("composeEnv =\n%q\nwant\n%q", got, want)
	}
	// With no limit a hint expands to nothing.
	if got := composeEnv(nil, map[string]string{"NX_PARALLEL": "${APO_CPUS}"}); !slices.Equal(got, []string{"NX_PARALLEL="}) {
		t.Errorf("no hints: %q", got)
	}
	if got := composeEnv(nil, nil); len(got) != 0 || envPrefix(got) != nil {
		t.Errorf("nothing: %q, prefix %q", got, envPrefix(got))
	}
}

func TestPodLimitsParse(t *testing.T) {
	pod := func(cpu, mem string) kube.Pod {
		return kube.Pod{Spec: kube.PodSpec{Containers: []kube.Container{
			{Name: "relay", Resources: &kube.ResourceRequirements{Limits: map[string]string{"cpu": "9", "memory": "9Gi"}}},
			{Name: sandboxContainer, Resources: &kube.ResourceRequirements{Limits: map[string]string{"cpu": cpu, "memory": mem}}},
		}}}
	}
	for _, c := range []struct {
		cpu, mem string
		want     envport.SandboxSize
	}{
		{"4000m", "8192Mi", envport.SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}},
		{"2", "2Gi", envport.SandboxSize{MilliCPU: 2000, MemoryMiB: 2048}},
		{"0.5", "1Ti", envport.SandboxSize{MilliCPU: 500, MemoryMiB: 1 << 20}},
		{"", "", envport.SandboxSize{}},
		{"lots", "1G", envport.SandboxSize{}}, // what sandboxResources never writes is no limit
		{"-1m", "-5Mi", envport.SandboxSize{}},
		{"-1", "xMi", envport.SandboxSize{}},
	} {
		if got := podLimits(pod(c.cpu, c.mem)); got != c.want {
			t.Errorf("podLimits(%q, %q) = %+v, want %+v", c.cpu, c.mem, got, c.want)
		}
	}
	if got := podLimits(kube.Pod{Spec: kube.PodSpec{Containers: []kube.Container{{Name: sandboxContainer}}}}); !got.IsZero() {
		t.Errorf("no resources: %+v", got)
	}
}

// envAdapter is a test adapter whose env w belongs to project P1, made with
// limits, the project's variables read from vars at each call.
func envAdapter(t *testing.T, r CmdRunner, limits envport.SandboxSize, vars *map[string]string) *Adapter {
	t.Helper()
	a := newTestAdapter(t, r)
	armSandbox(t, a, "w")
	var mu sync.Mutex
	a.cfg.ProjectEnv = func(_ context.Context, project string) (map[string]string, error) {
		mu.Lock()
		defer mu.Unlock()
		if project != "P1" {
			t.Errorf("project = %q", project)
		}
		if *vars == nil {
			return nil, errors.New("store down")
		}
		return *vars, nil
	}
	a.recordEnvSource("w", envSource{Project: "P1", Limits: limits})
	return a
}

// execArgs is the argv of the last `devcontainer exec`.
func execArgs(t *testing.T, r *ruleRunner) []string {
	t.Helper()
	var last []string
	for _, c := range r.snapshot() {
		if len(c.args) > 0 && c.args[0] == "exec" {
			last = c.args
		}
	}
	if last == nil {
		t.Fatal("no devcontainer exec")
	}
	return last
}

// The env an exec gets: `env` over the program, after the CLI's own flags
// (the marker's --remote-env included), hints then the project's variables.
func TestExecCarriesTheWorkspaceEnv(t *testing.T) {
	r := &ruleRunner{}
	vars := map[string]string{"JEST_MAX_WORKERS": "2", "NX_PARALLEL": "${APO_CPUS}"}
	a := envAdapter(t, r, envport.SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}, &vars)
	ctx := context.Background()
	if _, err := a.Exec(ctx, "w", envport.Command{Script: "npm test", Marker: "m1"}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(execArgs(t, r), " ")
	want := "exec --id-label apothecary.env=w --remote-env APOTHECARY_CMD=m1 env APO_CPUS=4 APO_MEMORY_MIB=8192 JEST_MAX_WORKERS=2 NX_PARALLEL=4 bash -eo pipefail -s"
	if got != want {
		t.Errorf("exec argv =\n%s\nwant\n%s", got, want)
	}

	// A change reaches the next exec, files included; no rebuild.
	vars = map[string]string{"JEST_MAX_WORKERS": "1"}
	if _, err := a.ReadFile(ctx, "w", "README.md"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(execArgs(t, r), " "); !strings.Contains(got, " env APO_CPUS=4 APO_MEMORY_MIB=8192 JEST_MAX_WORKERS=1 bash -s") {
		t.Errorf("after the change: %s", got)
	}

	// The store failing keeps the last set read.
	vars = nil
	if _, err := a.Exec(ctx, "w", envport.Command{Script: "true"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(execArgs(t, r), " "); !strings.Contains(got, "JEST_MAX_WORKERS=1 bash") {
		t.Errorf("store down: %s", got)
	}
}

// No limit, no hint; nothing at all, no `env`.
func TestExecWithoutLimitsOrVars(t *testing.T) {
	r := &ruleRunner{}
	vars := map[string]string{"NX_PARALLEL": "${APO_CPUS}"}
	a := envAdapter(t, r, envport.SandboxSize{}, &vars)
	if _, err := a.Exec(context.Background(), "w", envport.Command{Script: "true"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(execArgs(t, r), " "); !strings.HasSuffix(got, "--id-label apothecary.env=w env NX_PARALLEL= bash -eo pipefail -s") {
		t.Errorf("no limits: %s", got)
	}

	vars = map[string]string{}
	if _, err := a.Exec(context.Background(), "w", envport.Command{Script: "true"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(execArgs(t, r), " "); got != "exec --id-label apothecary.env=w bash -eo pipefail -s" {
		t.Errorf("nothing to set: %s", got)
	}

	// An env provisioned before the environment existed has no source.
	b := newTestAdapter(t, r)
	b.cfg.ProjectEnv = a.cfg.ProjectEnv
	if got := b.workspaceEnv(context.Background(), "old"); len(got) != 0 {
		t.Errorf("no source: %q", got)
	}
}

// What the provision records outlives the engine: a new adapter on the same
// runs dir reads it back.
func TestEnvSourceSurvivesARestart(t *testing.T) {
	r := &ruleRunner{}
	vars := map[string]string{}
	a := envAdapter(t, r, envport.SandboxSize{MilliCPU: 2000}, &vars)
	b := NewWithRunner(Config{RunsDir: a.cfg.RunsDir, ProjectEnv: a.cfg.ProjectEnv}, r)
	b.host = newTestHost(b)
	if got := b.workspaceEnv(context.Background(), "w"); !slices.Equal(got, []string{"APO_CPUS=2"}) {
		t.Errorf("after a restart: %q", got)
	}
}

// The provision records the sandbox's limits and project, and says once
// what the workspace's processes get.
func TestProvisionLogsTheWorkspaceEnv(t *testing.T) {
	a, _ := armedAdapter(t)
	a.host.(*testHost).limits = envport.SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}
	a.cfg.ProjectEnv = func(context.Context, string) (map[string]string, error) {
		return map[string]string{"NX_PARALLEL": "${APO_CPUS}", "JEST_MAX_WORKERS": "2"}, nil
	}
	if _, err := a.Provision(context.Background(), envport.EnvSpec{Name: "w", Project: "P1", RepoURL: "https://g.local/r.git"}); err != nil {
		t.Fatal(err)
	}
	chunk, err := a.ReadExecLog(context.Background(), "w", -1, 0)
	if err != nil {
		t.Fatal(err)
	}
	log := string(chunk.Data)
	for _, want := range []string{
		"→ workspace environment: size hints APO_CPUS=4 APO_MEMORY_MIB=8192",
		"→ workspace environment: project JEST_MAX_WORKERS=2\n",
		"→ workspace environment: project NX_PARALLEL=${APO_CPUS} (→ 4)",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if src := a.envSourceFor("w"); src.Project != "P1" || src.Limits.MilliCPU != 4000 {
		t.Errorf("source = %+v", src)
	}

	// None of either says so.
	b, _ := armedAdapter(t)
	if _, err := b.Provision(context.Background(), envport.EnvSpec{Name: "w", RepoURL: "https://g.local/r.git"}); err != nil {
		t.Fatal(err)
	}
	chunk, _ = b.ReadExecLog(context.Background(), "w", -1, 0)
	for _, want := range []string{"size hints none (no CPU or memory limit)", "no project variables"} {
		if !strings.Contains(string(chunk.Data), want) {
			t.Errorf("log lacks %q:\n%s", want, chunk.Data)
		}
	}
}

// The hints are the limits of the pod the sandbox runs as: a new pod's,
// and a reused pod's own, whatever the settings now say. A kata sandbox's
// is its limit, not its VM's vCPU count.
func TestSandboxLimitsFromThePod(t *testing.T) {
	ctx := context.Background()
	sized := envport.EnvSpec{Size: envport.SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}}

	k := &kubeFake{secrets: map[string]string{}}
	_, h := k8sWith(t, k, false)
	h.cfg.Storage = "pvc"
	h.rt, _ = lookupRuntime(envport.RuntimeKata)
	got, err := h.ensurePod(ctx, "w", sized, false, egressgw.Attachment{}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if h := sizeHints(got); h["APO_CPUS"] != "4" || h["APO_MEMORY_MIB"] != "8192" {
		t.Errorf("kata hints = %v", h)
	}

	// A pod made at 2 CPUs and reused under a 4-CPU setting keeps 2.
	k = &kubeFake{secrets: map[string]string{}}
	_, h = k8sWith(t, k, false)
	k.pod = livePod(t, h, envport.EnvSpec{Size: envport.SandboxSize{MilliCPU: 2000, MemoryMiB: 4096}}, false)
	got, err = h.ensurePod(ctx, "w", sized, false, egressgw.Attachment{}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if got != (envport.SandboxSize{MilliCPU: 2000, MemoryMiB: 4096}) {
		t.Errorf("reused pod limits = %+v", got)
	}

	// Unsized under a ceiling: the ceiling is the limit, so the hint.
	k = &kubeFake{secrets: map[string]string{}}
	_, h = k8sWith(t, k, false)
	got, err = h.ensurePod(ctx, "w", envport.EnvSpec{SizeCap: envport.SandboxSize{MilliCPU: 8000}}, false, egressgw.Attachment{}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if got != (envport.SandboxSize{MilliCPU: 8000}) {
		t.Errorf("ceiling limits = %+v", got)
	}
}

// fakeSidecar serves env w's sidecar socket: PUT /env records each set it
// is told; noRoute answers like a sidecar that predates the route.
type fakeSidecar struct {
	mu      sync.Mutex
	told    []map[string]string
	noRoute bool
}

func serveFakeSidecar(t *testing.T, a *Adapter) *fakeSidecar {
	t.Helper()
	f := &fakeSidecar{}
	ln := serveSock(t, a, sidecarSock)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /env", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.noRoute {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Env map[string]string `json:"env"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.told = append(f.told, req.Env)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return f
}

func (f *fakeSidecar) sets() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.told)
}

// Each dial tells the sidecar the set when it changed, and only then.
func TestSidecarIsToldTheWorkspaceEnv(t *testing.T) {
	r := &ruleRunner{}
	vars := map[string]string{"NX_PARALLEL": "${APO_CPUS}"}
	a := envAdapter(t, r, envport.SandboxSize{MilliCPU: 3000}, &vars)
	a.sidecarEnvTimeout = 5 * time.Second // this sidecar answers
	f := serveFakeSidecar(t, a)
	ctx := context.Background()
	for range 2 {
		if _, err := a.Sidecar(ctx, "w"); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{"APO_CPUS": "3", "NX_PARALLEL": "3"}
	if got := f.sets(); len(got) != 1 || !equalMaps(got[0], want) {
		t.Fatalf("told %v, want once %v", got, want)
	}
	vars = map[string]string{}
	if _, err := a.Sidecar(ctx, "w"); err != nil {
		t.Fatal(err)
	}
	if got := f.sets(); len(got) != 2 || !equalMaps(got[1], map[string]string{"APO_CPUS": "3"}) {
		t.Errorf("after the change: %v", got)
	}

	// A sidecar that predates the route is not asked again for the same set.
	f.mu.Lock()
	f.noRoute = true
	f.mu.Unlock()
	vars = map[string]string{"A": "1"}
	for range 2 {
		if _, err := a.Sidecar(ctx, "w"); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(r.snapshot()); n != 0 {
		t.Errorf("a live sidecar was started again: %v", r.lines())
	}
}

// The sidecar starts without the environment: it is told it instead, so a
// variable the project drops is not left in what it starts.
func TestSidecarStartsWithoutTheWorkspaceEnv(t *testing.T) {
	vars := map[string]string{"JEST_MAX_WORKERS": "2"}
	a := envAdapter(t, &ruleRunner{}, envport.SandboxSize{MilliCPU: 2000}, &vars)
	a.sidecarEnvTimeout = 5 * time.Second
	var f *fakeSidecar
	var once sync.Once
	r := &ruleRunner{rules: []rule{{match: "exec --id-label", fn: func(_ []string, stdin []byte) *response {
		if strings.Contains(string(stdin), "sidecar --daemon") {
			once.Do(func() { f = serveFakeSidecar(t, a) })
		}
		return nil
	}}}}
	a.runner = r
	// Told something before: a new sidecar is told again.
	a.sidecarEnv["w"] = strings.Join(a.workspaceEnv(context.Background(), "w"), "\x00")
	if _, err := a.Sidecar(context.Background(), "w"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(execArgs(t, r), " "); strings.Contains(got, " env ") {
		t.Errorf("the sidecar started with the environment: %s", got)
	}
	if got := f.sets(); len(got) != 1 || got[0]["JEST_MAX_WORKERS"] != "2" || got[0]["APO_CPUS"] != "2" {
		t.Errorf("told %v", got)
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
