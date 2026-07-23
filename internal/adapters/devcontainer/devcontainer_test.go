package devcontainer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/ports"
)

type call struct {
	bin   string
	args  []string
	stdin []byte
	env   []string
}

// scriptRunner replays canned responses and records calls.
type scriptRunner struct {
	calls     []call
	responses []response
}

type response struct {
	stdout string
	stderr string
	code   int
	err    error
}

func (s *scriptRunner) Run(_ context.Context, bin string, args []string, stdin []byte, tee io.Writer, env ...string) ([]byte, []byte, int, error) {
	s.calls = append(s.calls, call{bin: bin, args: args, stdin: stdin, env: env})
	if len(s.responses) == 0 {
		return nil, nil, 0, nil
	}
	r := s.responses[0]
	s.responses = s.responses[1:]
	if tee != nil && r.stdout != "" {
		// Emulate live streaming: the tee sees output before Run returns.
		_, _ = tee.Write([]byte(r.stdout))
	}
	return []byte(r.stdout), []byte(r.stderr), r.code, r.err
}

func newTestAdapter(t *testing.T, r CmdRunner) *Adapter {
	t.Helper()
	shimSrc := filepath.Join(t.TempDir(), "apothecary-shim")
	if err := os.WriteFile(shimSrc, []byte("#!fake-shim"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := NewWithRunner(Config{
		RunsDir: t.TempDir(),
		ShimBin: func(context.Context) (string, error) { return shimSrc, nil },
		// Off by default so the base lifecycle tests stay firewall-agnostic;
		// the firewall tests below opt in explicitly.
		FirewallOff: true,
	}, r)
	a.retryDelay = time.Millisecond
	a.noSandboxGuard = true // t.TempDir() is under /tmp; the guard has its own test
	a.listen = func(network, addr string) (net.Listener, error) {
		return net.Listen("tcp", "127.0.0.1:0")
	}
	return a
}

// sandboxResp are the host-daemon responses ensureSandbox consumes for a
// FRESH sandbox (no existing one): ps-miss, volume create, docker run, and the
// readiness version probe. Prepend to a Provision test's response script.
func sandboxResp(rest []response) []response {
	return append([]response{
		{stdout: ""},       // sandboxIDs: none yet
		{code: 0},          // volume create
		{stdout: "sbx1\n"}, // docker run (sandbox container id)
		{stdout: "29.6.2"}, // version probe → ready
	}, rest...)
}

// armSandbox pre-creates the run folder + a dummy inner-daemon socket file, so
// waitSandboxReady's os.Stat passes (the scripted version probe then confirms)
// and dockerEnv activates → inner calls carry DOCKER_HOST. A plain file (not a
// real socket) suffices and avoids the unix path-length limit on long tempdirs.
func armSandbox(t *testing.T, a *Adapter, name string) {
	t.Helper()
	if err := os.MkdirAll(a.folderFor(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.sockPath(name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// envHas reports whether a recorded call carried an env entry with the prefix.
func envHas(c call, prefix string) bool {
	for _, e := range c.env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

const upOK = `{"outcome":"success","containerId":"cid1","remoteUser":"vscode","remoteWorkspaceFolder":"/workspaces/src"}`

// The checkout dir's BASENAME is the compose project name for compose-based
// devcontainers (`<basename>_devcontainer`, no CLI flag to override). Two runs
// must never share it: they did when every checkout was literally "src", so
// compose reused the first run's containers and the second run's id-label
// landed on nothing ("no running container").
func TestSrcDirBasenameIsUniquePerRun(t *testing.T) {
	a := newTestAdapter(t, &scriptRunner{})
	one, two := a.srcDir("apo-feat-a-11"), a.srcDir("apo-fix-b-22")
	if filepath.Base(one) == filepath.Base(two) {
		t.Fatalf("runs share a compose project basename %q — containers would collide", filepath.Base(one))
	}
	if filepath.Base(one) != "apo-feat-a-11" {
		t.Errorf("srcDir basename = %q, want the workspace name", filepath.Base(one))
	}

	// A pre-existing <folder>/src checkout still wins, so workspaces created
	// before this fix keep resolving to their existing clone.
	legacy := filepath.Join(a.folderFor("apo-old-99"), "src")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, ".git"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := a.srcDir("apo-old-99"); got != legacy {
		t.Errorf("legacy checkout not honored: got %q, want %q", got, legacy)
	}
}

// find returns the first recorded call whose bin and leading arg match.
func find(t *testing.T, calls []call, bin, firstArg string) call {
	t.Helper()
	for _, c := range calls {
		if c.bin == bin && len(c.args) > 0 && c.args[0] == firstArg {
			return c
		}
	}
	t.Fatalf("no %s %s call in %v", bin, firstArg, calls)
	return call{}
}

func TestProvisionClonesUpsAndInits(t *testing.T) {
	r := &scriptRunner{responses: append(
		[]response{{code: 0}}, // git clone (before ensureSandbox)
		sandboxResp([]response{
			{code: 0},          // loadPipelineImage: image inspect present → skip load
			{stdout: upOK},     // devcontainer up
			{code: 0},          // docker exec root-init
			{stdout: "cid1\n"}, // docker ps -q (relay)
			{code: 0},          // docker exec -d relay
			{code: 0},          // ready/relay probe
		})...)}
	a := newTestAdapter(t, r)
	a.cfg.ProxyPort = 9090
	armSandbox(t, a, "apothecary-run1")
	id, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "RUN1", RepoURL: "https://g.local/grp/repo.git",
		GitUser: "oauth2", GitPass: "sekret", Image: "apothecary/ws:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "apothecary-run1" {
		t.Errorf("env id = %s", id)
	}

	clone := find(t, r.calls, "git", "-c")
	joined := strings.Join(clone.args, " ")
	if strings.Contains(joined, "sekret") {
		t.Fatalf("credential leaked into argv: %v", clone.args)
	}
	if !strings.Contains(joined, "credential.helper=store --file=") {
		t.Errorf("clone must use a credential store file: %v", clone.args)
	}
	if _, err := os.Stat(filepath.Join(a.folderFor("apothecary-run1"), "git-credentials")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("credential file must be removed after clone")
	}

	up := find(t, r.calls, "devcontainer", "up")
	joined = strings.Join(up.args, " ")
	for _, want := range []string{
		// Fresh clone → checkout named after the workspace (unique compose
		// project per run); the legacy <folder>/src layout is covered below.
		"--workspace-folder " + filepath.Join(a.folderFor("apothecary-run1"), "apothecary-run1"),
		"--id-label apothecary.env=apothecary-run1",
		"target=/opt/apothecary",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("up args missing %q: %v", want, up.args)
		}
	}
	// Repo has no devcontainer config in this test: the pipeline-image
	// fallback must ride --override-config.
	if !strings.Contains(joined, "--override-config") {
		t.Errorf("fallback config expected for repo without devcontainer: %v", up.args)
	}
	data, err := os.ReadFile(filepath.Join(a.folderFor("apothecary-run1"), "override.json"))
	if err != nil || !strings.Contains(string(data), "apothecary/ws:abc") {
		t.Errorf("override.json = %s, %v", data, err)
	}
	shim, err := os.ReadFile(filepath.Join(a.folderFor("apothecary-run1"), "shim", "shim"))
	if err != nil || string(shim) != "#!fake-shim" {
		t.Errorf("shim binary not materialized: %q, %v", shim, err)
	}

	init := find(t, r.calls, "docker", "exec")
	joined = strings.Join(init.args, " ")
	for _, want := range []string{"-u root", "cid1", "mkdir -p /apothecary", "ln -sfn '/workspaces/src'"} {
		if !strings.Contains(joined, want) {
			t.Errorf("root-init missing %q: %v", want, init.args)
		}
	}

	// The engine bridge came up for both sockets (proxy mode on).
	if n := len(a.bridges["apothecary-run1"]); n != 2 {
		t.Errorf("bridge listeners = %d, want 2 (engine + proxy)", n)
	}
	a.Close()
}

// The engine supplies a human-friendly, slug-of-branch name; the adapter must
// key the env off it (id-label + checkout dir) AND rename the CLI-built
// container to it so `docker ps` shows the branch, not the run-id hash.
func TestProvisionUsesEngineSuppliedName(t *testing.T) {
	const name = "apo-fix-branch-abcd"
	r := &scriptRunner{responses: append(
		[]response{{code: 0}}, // git clone
		sandboxResp([]response{
			{code: 0},          // loadPipelineImage: inspect present → skip load
			{stdout: upOK},     // devcontainer up
			{code: 0},          // docker exec root-init
			{stdout: "cid1\n"}, // docker ps -q (relay)
			{code: 0},          // docker exec -d relay
			{code: 0},          // ready/relay probe
			{code: 0},          // docker rename
		})...)}
	a := newTestAdapter(t, r)
	armSandbox(t, a, name)
	id, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "RUN1", Name: name, Project: "Acme Web",
		RepoURL: "https://g.local/r.git", Image: "apothecary/ws:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != ports.EnvID(name) {
		t.Errorf("env id = %s, want %s", id, name)
	}
	// The env is tracked under the engine name — both the id-label and the
	// checkout dir (workspace-folder) key off it.
	up := find(t, r.calls, "devcontainer", "up")
	upJoined := strings.Join(up.args, " ")
	if !strings.Contains(upJoined, "--id-label apothecary.env="+name) {
		t.Errorf("up must id-label the engine name: %v", up.args)
	}
	// The checkout dir is named after the workspace, NOT a shared literal
	// "src": for compose-based devcontainers the CLI derives the compose
	// project name from this basename, so a shared one collapses every run
	// onto one compose project (and one set of containers).
	if !strings.Contains(upJoined, "--workspace-folder "+filepath.Join(a.folderFor(name), name)) {
		t.Errorf("workspace-folder must be keyed by the engine name: %v", up.args)
	}
	// Metadata labels: the apothecary marker (always) and the project.
	if !strings.Contains(upJoined, "--id-label apothecary=true") {
		t.Errorf("up must mark the container apothecary-managed: %v", up.args)
	}
	if !strings.Contains(upJoined, "--id-label apothecary.project=Acme Web") {
		t.Errorf("up must label the project: %v", up.args)
	}
	// ...and the container is renamed to it for docker ps.
	rn := find(t, r.calls, "docker", "rename")
	if len(rn.args) < 3 || rn.args[1] != "cid1" || rn.args[2] != name {
		t.Errorf("docker rename = %v, want [rename cid1 %s]", rn.args, name)
	}
	a.Close()
}

func TestProvisionHonorsRepoDevcontainer(t *testing.T) {
	r := &scriptRunner{responses: sandboxResp([]response{
		{stdout: upOK},     // up (no clone: src pre-seeded)
		{code: 0},          // root-init
		{stdout: "cid1\n"}, // docker ps -q (relay)
		{code: 0},          // docker exec -d relay
		{code: 0},          // ready/relay probe
	})}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	src := filepath.Join(a.folderFor("apothecary-run1"), "src")
	if err := os.MkdirAll(filepath.Join(src, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(src, ".git"), filepath.Join(src, ".devcontainer", "devcontainer.json")} {
		if err := os.WriteFile(f, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "run1", RepoURL: "https://g.local/r.git"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		if c.bin == "git" {
			t.Error("must not re-clone an existing checkout")
		}
	}
	up := find(t, r.calls, "devcontainer", "up")
	if strings.Contains(strings.Join(up.args, " "), "--override-config") {
		t.Errorf("repo devcontainer must be honored, not overridden: %v", up.args)
	}
}

// seedRepoDevcontainer pre-seeds a repo-authored checkout (legacy /src
// layout, so no clone is attempted) and returns its path.
func seedRepoDevcontainer(t *testing.T, a *Adapter, name string) string {
	t.Helper()
	src := filepath.Join(a.folderFor(name), "src")
	// .git is a real directory here (unlike the minimal fixtures elsewhere):
	// the injected features append to .git/info/exclude.
	for _, d := range []string{filepath.Join(src, ".devcontainer"), filepath.Join(src, ".git", "info")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, ".devcontainer", "devcontainer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

// A repo-authored dev container runs the REPO's image, which has no harness —
// so the engine injects one as a feature. Without it the first agent stage
// dies with "opencode: command not found".
func TestRepoDevcontainerGetsHarnessFeature(t *testing.T) {
	r := &scriptRunner{responses: sandboxResp([]response{
		{stdout: upOK}, {code: 0}, {stdout: "cid1\n"}, {code: 0}, {code: 0},
	})}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	src := seedRepoDevcontainer(t, a, "apothecary-run1")

	if _, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "run1", RepoURL: "https://g.local/r.git",
		HarnessInstall: []ports.HarnessInstall{
			{Binary: "opencode", Requires: []string{"npm"}, Commands: []string{"npm install -g opencode-ai@1.2.3"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	up := find(t, r.calls, "devcontainer", "up")
	if !strings.Contains(strings.Join(up.args, " "), "./apothecary-harness") {
		t.Fatalf("up must inject the harness feature: %v", up.args)
	}
	dir := filepath.Join(src, ".devcontainer", "apothecary-harness")
	script, err := os.ReadFile(filepath.Join(dir, "harness-install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "npm install -g opencode-ai@1.2.3") {
		t.Errorf("generated install missing the command:\n%s", script)
	}
	// Guarded: an image that already ships the harness is left alone.
	if !strings.Contains(string(script), "command -v opencode") {
		t.Errorf("generated install must skip an already-present harness:\n%s", script)
	}
	bins, err := os.ReadFile(filepath.Join(dir, "harness-binaries"))
	if err != nil || strings.TrimSpace(string(bins)) != "opencode" {
		t.Errorf("harness-binaries = %q, %v", bins, err)
	}
	// Prerequisites are declared per harness, not assumed by the script.
	reqs, err := os.ReadFile(filepath.Join(dir, "harness-requires"))
	if err != nil || strings.TrimSpace(string(reqs)) != "npm" {
		t.Errorf("harness-requires = %q, %v", reqs, err)
	}
	// The static feature (prereq bootstrap) rides along.
	if _, err := os.Stat(filepath.Join(dir, "install.sh")); err != nil {
		t.Errorf("static feature install.sh not materialized: %v", err)
	}
}

// The installer is runtime-agnostic: a harness that installs without Node
// declares its own prerequisite and never drags Node in.
func TestHarnessFeatureRequiresAreHarnessDeclared(t *testing.T) {
	r := &scriptRunner{responses: sandboxResp([]response{
		{stdout: upOK}, {code: 0}, {stdout: "cid1\n"}, {code: 0}, {code: 0},
	})}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	src := seedRepoDevcontainer(t, a, "apothecary-run1")

	if _, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "run1", RepoURL: "https://g.local/r.git",
		HarnessInstall: []ports.HarnessInstall{
			{Binary: "clyde", Requires: []string{"curl"}, Commands: []string{"curl -fsSL https://x.dev/install | sh"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	reqs, err := os.ReadFile(filepath.Join(src, ".devcontainer", "apothecary-harness", "harness-requires"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(reqs)); got != "curl" {
		t.Errorf("harness-requires = %q, want curl (no Node assumption)", got)
	}
}

// Firewall and harness are independent contributors to ONE
// --additional-features object.
func TestRepoDevcontainerCombinesFirewallAndHarness(t *testing.T) {
	r := &scriptRunner{responses: sandboxResp([]response{
		{stdout: `{"configuration":{"image":"x"},"mergedConfiguration":{"image":"x"}}`}, // read-configuration (isCompose): not compose
		{stdout: upOK},     // up
		{code: 0},          // root-init
		{stdout: "cid1\n"}, // docker ps -q (relay)
		{code: 0},          // docker exec -d relay
		{code: 0},          // ready/relay/firewall probe
	})}
	a := newTestAdapter(t, r)
	a.cfg.FirewallOff = false
	armSandbox(t, a, "apothecary-run1")
	seedRepoDevcontainer(t, a, "apothecary-run1")

	if _, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "run1", RepoURL: "https://g.local/r.git",
		HarnessInstall: []ports.HarnessInstall{
			{Binary: "opencode", Commands: []string{"npm install -g opencode-ai@1.2.3"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	up := find(t, r.calls, "devcontainer", "up")
	joined := strings.Join(up.args, " ")
	for _, want := range []string{"./apothecary-firewall", "./apothecary-harness"} {
		if !strings.Contains(joined, want) {
			t.Errorf("up args missing %q: %v", want, up.args)
		}
	}
	if n := strings.Count(joined, "--additional-features"); n != 1 {
		t.Errorf("features must ride in ONE --additional-features object, got %d", n)
	}
}

// The fallback path runs the §8 pipeline image, which already bakes the
// harness in — injecting it again would be redundant work every build.
func TestFallbackImageSkipsHarnessFeature(t *testing.T) {
	r := &scriptRunner{responses: append(
		[]response{{code: 0}}, // git clone
		sandboxResp([]response{
			{code: 0}, // loadPipelineImage: inspect present → skip load
			{stdout: upOK}, {code: 0}, {stdout: "cid1\n"}, {code: 0}, {code: 0},
		})...)}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	if _, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "run1", RepoURL: "https://g.local/r.git", Image: "apothecary/ws:abc",
		HarnessInstall: []ports.HarnessInstall{
			{Binary: "opencode", Commands: []string{"npm install -g opencode-ai@1.2.3"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	up := find(t, r.calls, "devcontainer", "up")
	if strings.Contains(strings.Join(up.args, " "), "apothecary-harness") {
		t.Errorf("fallback image already has the harness: %v", up.args)
	}
}

func TestFallbackConfigFirewallExtendsOverride(t *testing.T) {
	r := &scriptRunner{responses: append(
		[]response{{code: 0}}, // git clone
		sandboxResp([]response{
			{code: 0},          // loadPipelineImage: inspect present → skip load
			{stdout: upOK},     // up (no repo config → fallback)
			{code: 0},          // root-init
			{stdout: "cid1\n"}, // docker ps -q (relay)
			{code: 0},          // docker exec -d relay
			{code: 0},          // ready/relay/firewall probe
		})...)}
	a := newTestAdapter(t, r)
	a.cfg.FirewallOff = false // firewall ON
	a.cfg.ProxyPort = 9090
	armSandbox(t, a, "apothecary-run1")
	_, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "run1", RepoURL: "https://g.local/r.git", Image: "apothecary/ws:abc",
		EgressDomains: []string{"g.local", "api.anthropic.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(a.folderFor("apothecary-run1"), "override.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		CapAdd           []string          `json:"capAdd"`
		ContainerEnv     map[string]string `json:"containerEnv"`
		PostStartCommand string            `json:"postStartCommand"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.CapAdd, "NET_ADMIN") {
		t.Errorf("capAdd = %v", cfg.CapAdd)
	}
	if !strings.Contains(cfg.PostStartCommand, "init-firewall.sh") {
		t.Errorf("postStart = %q", cfg.PostStartCommand)
	}
	if cfg.ContainerEnv["APOTHECARY_EXTRA_DOMAINS"] != "g.local api.anthropic.com" ||
		cfg.ContainerEnv["APOTHECARY_EGRESS_PROXY"] != "127.0.0.1:9090" ||
		cfg.ContainerEnv["APOTHECARY_CODER_PORT"] != "1" {
		t.Errorf("containerEnv = %v", cfg.ContainerEnv)
	}
	// Firewalled fact recorded and reported (egress NOT open).
	open, _ := a.EgressOpen(context.Background(), "apothecary-run1")
	if open {
		t.Error("firewalled env must report EgressOpen=false")
	}
}

func TestRepoConfigInjectsFirewallFeature(t *testing.T) {
	r := &scriptRunner{responses: sandboxResp([]response{
		{stdout: `{"configuration":{"image":"x"},"mergedConfiguration":{"image":"x"}}`}, // read-configuration (isCompose): not compose
		{stdout: upOK},     // up
		{code: 0},          // root-init
		{stdout: "cid1\n"}, // docker ps -q (relay)
		{code: 0},          // docker exec -d relay
		{code: 0},          // ready/relay/firewall probe
	})}
	a := newTestAdapter(t, r)
	a.cfg.FirewallOff = false
	armSandbox(t, a, "apothecary-run1")
	src := filepath.Join(a.folderFor("apothecary-run1"), "src")
	// A real .git DIR (gitExclude writes .git/info/exclude).
	if err := os.MkdirAll(filepath.Join(src, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".devcontainer", "devcontainer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "run1", RepoURL: "https://g.local/r.git", EgressDomains: []string{"g.local"},
	}); err != nil {
		t.Fatal(err)
	}
	up := find(t, r.calls, "devcontainer", "up")
	joined := strings.Join(up.args, " ")
	if !strings.Contains(joined, `--additional-features {"./apothecary-firewall":{}}`) {
		t.Errorf("feature not injected: %v", up.args)
	}
	// Feature materialized into the checkout, git-excluded, with the shared
	// init-firewall.sh alongside.
	fdir := filepath.Join(src, ".devcontainer", "apothecary-firewall")
	for _, f := range []string{"devcontainer-feature.json", "install.sh", "apothecary-entrypoint.sh", "init-firewall.sh"} {
		if _, err := os.Stat(filepath.Join(fdir, f)); err != nil {
			t.Errorf("feature file %s missing: %v", f, err)
		}
	}
	excl, _ := os.ReadFile(filepath.Join(src, ".git", "info", "exclude"))
	if !strings.Contains(string(excl), ".devcontainer/apothecary-firewall/") {
		t.Errorf("feature not git-excluded: %q", excl)
	}
	// firewall.env written to the shim mount with shell-quoted values.
	fwEnv, err := os.ReadFile(filepath.Join(a.folderFor("apothecary-run1"), "shim", "firewall.env"))
	if err != nil || !strings.Contains(string(fwEnv), "APOTHECARY_CODER_PORT='1'") {
		t.Errorf("firewall.env = %q, %v", fwEnv, err)
	}
}

func TestComposeConfigStaysOpen(t *testing.T) {
	r := &scriptRunner{responses: sandboxResp([]response{
		{stdout: `{"mergedConfiguration":{"dockerComposeFile":"docker-compose.yml"}}`}, // isCompose: yes
		{stdout: upOK},     // up
		{code: 0},          // root-init
		{stdout: "cid1\n"}, // docker ps -q (relay)
		{code: 0},          // docker exec -d relay
		{code: 0},          // ready probe (no firewall done-file)
	})}
	a := newTestAdapter(t, r)
	a.cfg.FirewallOff = false
	armSandbox(t, a, "apothecary-run1")
	src := filepath.Join(a.folderFor("apothecary-run1"), "src")
	if err := os.MkdirAll(filepath.Join(src, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(src, ".git"), filepath.Join(src, ".devcontainer", "devcontainer.json")} {
		if err := os.WriteFile(f, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "run1", RepoURL: "https://g.local/r.git"}); err != nil {
		t.Fatal(err)
	}
	up := find(t, r.calls, "devcontainer", "up")
	if strings.Contains(strings.Join(up.args, " "), "--additional-features") {
		t.Errorf("compose config must NOT be firewalled: %v", up.args)
	}
	// Reported open, and the fact survives an adapter restart via the marker.
	open, _ := a.EgressOpen(context.Background(), "apothecary-run1")
	if !open {
		t.Error("compose env must report EgressOpen=true")
	}
	fresh := newTestAdapter(t, &scriptRunner{})
	fresh.cfg.RunsDir = a.cfg.RunsDir
	open, _ = fresh.EgressOpen(context.Background(), "apothecary-run1")
	if !open {
		t.Error("EgressOpen must survive restart via the on-disk marker")
	}
}

func TestFirewallOffSkipsInjection(t *testing.T) {
	r := &scriptRunner{responses: sandboxResp([]response{
		{stdout: upOK},     // up (no isCompose call when firewall off)
		{code: 0},          // root-init
		{stdout: "cid1\n"}, // relay ps
		{code: 0},          // relay exec
		{code: 0},          // ready probe
	})}
	a := newTestAdapter(t, r) // FirewallOff: true
	armSandbox(t, a, "apothecary-run1")
	src := filepath.Join(a.folderFor("apothecary-run1"), "src")
	if err := os.MkdirAll(filepath.Join(src, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(src, ".git"), filepath.Join(src, ".devcontainer", "devcontainer.json")} {
		if err := os.WriteFile(f, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "run1", RepoURL: "https://g.local/r.git"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		if c.bin == "devcontainer" && len(c.args) > 0 && c.args[0] == "read-configuration" {
			t.Error("firewall off must not probe for compose")
		}
		if strings.Contains(strings.Join(c.args, " "), "--additional-features") {
			t.Error("firewall off must not inject the feature")
		}
	}
	open, _ := a.EgressOpen(context.Background(), "apothecary-run1")
	if !open {
		t.Error("firewall-off env must report EgressOpen=true")
	}
}

func TestProvisionWithoutConfigOrImageFails(t *testing.T) {
	// ensureSandbox runs before the missing-image check, so its script is
	// consumed first; the error then fires with no further docker calls.
	r := &scriptRunner{responses: append([]response{{code: 0}}, sandboxResp(nil)...)} // clone + sandbox
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	_, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "run1", RepoURL: "https://g.local/r.git"})
	if err == nil || !strings.Contains(err.Error(), "no .devcontainer config") {
		t.Fatalf("want missing-config error, got %v", err)
	}
}

func TestProvisionSurfacesUpFailure(t *testing.T) {
	r := &scriptRunner{responses: append(
		[]response{{code: 0}}, // clone
		sandboxResp([]response{
			{code: 0}, // loadPipelineImage: inspect present → skip load
			{stdout: `{"outcome":"error","message":"Dockerfile failed","description":"exit 1"}`, stderr: "build log tail", code: 1},
		})...)}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	_, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "run1", RepoURL: "u", Image: "img"})
	if err == nil || !strings.Contains(err.Error(), "Dockerfile failed") {
		t.Fatalf("want up failure with message, got %v", err)
	}
}

func TestExecPropagatesExitAndCombinesOutput(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "out\n", stderr: "err\n", code: 7},
	}}
	a := newTestAdapter(t, r)
	res, err := a.Exec(context.Background(), "apothecary-run1", ports.Command{Script: "make test", Dir: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 {
		t.Errorf("exit = %d", res.ExitCode)
	}
	if res.Output != "out\nerr\n" {
		t.Errorf("output = %q", res.Output)
	}
	c := r.calls[0]
	if c.bin != "devcontainer" || c.args[0] != "exec" {
		t.Fatalf("exec transport = %v", c.args)
	}
	joined := strings.Join(c.args, " ")
	if !strings.Contains(joined, "--id-label apothecary.env=apothecary-run1") {
		t.Errorf("exec args missing label: %v", c.args)
	}
	stdin := string(c.stdin)
	if !strings.Contains(stdin, `cd "$HOME/repo"`) || !strings.Contains(stdin, "make test") || !strings.Contains(stdin, "proxy.env") {
		t.Errorf("stdin = %q", stdin)
	}
}

func TestExecStreamsToTailableLog(t *testing.T) {
	r := &scriptRunner{responses: []response{{stdout: "live output\n"}}}
	a := newTestAdapter(t, r)
	if _, err := a.Exec(context.Background(), "ws1", ports.Command{Script: "echo live output"}); err != nil {
		t.Fatal(err)
	}
	got, err := a.TailExecLog(context.Background(), "ws1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "live output") {
		t.Errorf("exec.log = %q", got)
	}
}

func TestReadFile(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("hello devcontainer"))
	r := &scriptRunner{responses: []response{
		{stdout: payload + "\n"},
		{code: 44},
	}}
	a := newTestAdapter(t, r)
	data, err := a.ReadFile(context.Background(), "ws", "artifacts/plan.md")
	if err != nil || string(data) != "hello devcontainer" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if _, err := a.ReadFile(context.Background(), "ws", "missing.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want fs.ErrNotExist, got %v", err)
	}
}

func TestWriteFileSendsScriptAndPayload(t *testing.T) {
	r := &scriptRunner{responses: []response{{code: 0}}}
	a := newTestAdapter(t, r)
	if err := a.WriteFile(context.Background(), "ws", "/apothecary/run.env", []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin := string(r.calls[0].stdin)
	if !strings.Contains(stdin, "base64 -d > '/apothecary/run.env'") ||
		!strings.Contains(stdin, base64.StdEncoding.EncodeToString([]byte("A=1\n"))) ||
		!strings.Contains(stdin, "chmod 600") {
		t.Errorf("stdin = %q", stdin)
	}
}

// Status is two-stage: the SANDBOX on the host daemon gates the WORKSPACE on
// the inner daemon.
func TestStatusMapping(t *testing.T) {
	cases := []struct {
		name string
		arm  bool       // pre-create the inner socket (a DinD env exists)
		resp []response // sandbox-state query, then (if running) inner workspace query
		want ports.EnvStatus
	}{
		{"sandbox running + workspace running", true,
			[]response{{stdout: "running\n"}, {stdout: "running\n"}}, ports.EnvRunning},
		{"sandbox running + workspace created", true,
			[]response{{stdout: "running\n"}, {stdout: "created\n"}}, ports.EnvStarting},
		{"sandbox running + workspace exited", true,
			[]response{{stdout: "running\n"}, {stdout: "exited\n"}}, ports.EnvStopped},
		{"sandbox created", true,
			[]response{{stdout: "created\n"}}, ports.EnvStarting},
		{"sandbox exited", true,
			[]response{{stdout: "exited\n"}}, ports.EnvStopped},
		{"DinD env, sandbox gone → absent", true,
			[]response{{stdout: ""}}, ports.EnvAbsent},
		// Legacy (pre-DinD) env: no sandbox, no socket → the old host-label query.
		{"legacy running", false,
			[]response{{stdout: ""}, {stdout: "running\n"}}, ports.EnvRunning},
		{"legacy absent", false,
			[]response{{stdout: ""}, {stdout: ""}}, ports.EnvAbsent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newTestAdapter(t, &scriptRunner{responses: c.resp})
			if c.arm {
				armSandbox(t, a, "apothecary-run1")
			}
			got, err := a.Status(context.Background(), "apothecary-run1")
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

// Legacy (pre-DinD) env: no sandbox → the workspace container is removed by
// id-label on the host daemon.
func TestDestroyLegacyRemovesContainersAndFolder(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: ""},       // sandboxIDs: none → legacy path
		{stdout: "cid1\n"}, // docker ps -aq (host workspace container)
		{code: 0},          // docker rm -f
	}}
	a := newTestAdapter(t, r)
	if err := os.MkdirAll(a.folderFor("apothecary-run1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Destroy(context.Background(), "apothecary-run1"); err != nil {
		t.Fatal(err)
	}
	rm := find(t, r.calls, "docker", "rm")
	if !strings.Contains(strings.Join(rm.args, " "), "-f cid1") {
		t.Errorf("rm args = %v", rm.args)
	}
	if _, err := os.Stat(a.folderFor("apothecary-run1")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("env folder not cleaned up")
	}
}

// DinD env: removing the sandbox tears down the whole inner daemon (every
// workspace + side service + network) and the named docker volume goes next —
// the side-service leak is gone by construction.
func TestDestroyRemovesSandboxAndVolume(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "sbx1\n"}, // sandboxIDs: the sandbox container
		{code: 0},          // docker rm -f sbx1
		{code: 0},          // docker volume rm -f
	}}
	a := newTestAdapter(t, r)
	const name = "apothecary-run1"
	armSandbox(t, a, name)
	if err := a.Destroy(context.Background(), ports.EnvID(name)); err != nil {
		t.Fatal(err)
	}
	rm := find(t, r.calls, "docker", "rm")
	if !strings.Contains(strings.Join(rm.args, " "), "-f sbx1") {
		t.Errorf("rm must target the sandbox: %v", rm.args)
	}
	vol := find(t, r.calls, "docker", "volume")
	if !strings.Contains(strings.Join(vol.args, " "), "rm -f "+name+"-docker") {
		t.Errorf("volume rm missing: %v", vol.args)
	}
	if _, err := os.Stat(a.folderFor(name)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("env folder not cleaned up")
	}
}

// --- Docker-in-Docker sandbox ------------------------------------------------

// provisionRepoDinD runs a repo-authored (seeded, no-clone, no-image)
// provision through the sandbox path and returns the recorded calls.
func provisionRepoDinD(t *testing.T, rest []response) (*Adapter, *scriptRunner) {
	t.Helper()
	r := &scriptRunner{responses: sandboxResp(rest)}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	seedRepoDevcontainer(t, a, "apothecary-run1")
	if _, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "run1", Project: "Acme", RepoURL: "https://g.local/r.git",
	}); err != nil {
		t.Fatal(err)
	}
	return a, r
}

var repoUpTail = []response{
	{stdout: upOK}, {code: 0}, {stdout: "cid1\n"}, {code: 0}, {code: 0},
}

// The sandbox is a --privileged host container carrying the apothecary labels,
// the /var/lib/docker volume, the identical-path parity mount, and a dockerd
// that listens ONLY on the parity-mount socket — and it must never carry the
// inner DOCKER_HOST (Invariant C).
func TestProvisionCreatesSandbox(t *testing.T) {
	a, r := provisionRepoDinD(t, repoUpTail)
	run := find(t, r.calls, "docker", "run")
	joined := strings.Join(run.args, " ")
	root := a.folderFor("apothecary-run1")
	for _, want := range []string{
		"--privileged",
		"--label apothecary.sandbox=true",
		"--label apothecary.env=apothecary-run1",
		"--label apothecary.project=Acme",
		"-v apothecary-run1-docker:/var/lib/docker",
		"-v " + root + ":" + root,
		"docker:29-dind dockerd --host=unix://" + a.sockPath("apothecary-run1"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sandbox run missing %q: %v", want, run.args)
		}
	}
	if envHas(run, "DOCKER_HOST") {
		t.Errorf("sandbox lifecycle must NOT carry the inner DOCKER_HOST: %v", run.env)
	}
}

// Every workspace/inner call targets the inner daemon; the `up` call also keeps
// the buildx entitlement env.
func TestInnerCallsCarryDockerHost(t *testing.T) {
	a, r := provisionRepoDinD(t, repoUpTail)
	dh := "DOCKER_HOST=unix://" + a.sockPath("apothecary-run1")
	up := find(t, r.calls, "devcontainer", "up")
	if !envHas(up, dh) {
		t.Errorf("up must target the inner daemon: env=%v", up.env)
	}
	if !envHas(up, "BUILDX_BAKE_ENTITLEMENTS_FS=0") {
		t.Errorf("up lost the buildx entitlement env: %v", up.env)
	}
	// A docker exec (root-init) also targets the inner daemon.
	ex := find(t, r.calls, "docker", "exec")
	if !envHas(ex, dh) {
		t.Errorf("inner docker exec must carry DOCKER_HOST: %v", ex.env)
	}
}

// A pre-DinD env (no socket) keeps every op on the host daemon with no
// DOCKER_HOST — the migration-safe fallback.
func TestLegacyEnvFallsBackToHostDaemon(t *testing.T) {
	r := &scriptRunner{responses: []response{{stdout: "out\n"}}}
	a := newTestAdapter(t, r) // no armSandbox → no socket
	if _, err := a.Exec(context.Background(), "legacy-run", ports.Command{Script: "true"}); err != nil {
		t.Fatal(err)
	}
	if envHas(r.calls[0], "DOCKER_HOST") {
		t.Errorf("legacy env must not carry DOCKER_HOST: %v", r.calls[0].env)
	}
}

// The §8 pipeline image (host daemon) is streamed into the inner daemon only
// when absent there; a repo with its own devcontainer never triggers it.
func TestPipelineImageLoadedIntoSandbox(t *testing.T) {
	r := &scriptRunner{responses: append(
		[]response{{code: 0}}, // git clone
		sandboxResp([]response{
			{code: 1}, // image inspect on inner: NOT present
			{code: 0}, // /bin/sh -c "docker save … | docker load"
			{stdout: upOK}, {code: 0}, {stdout: "cid1\n"}, {code: 0}, {code: 0},
		})...)}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	if _, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "run1", RepoURL: "u", Image: "apothecary/ws:abc",
	}); err != nil {
		t.Fatal(err)
	}
	load := find(t, r.calls, "/bin/sh", "-c")
	joined := strings.Join(load.args, " ")
	if !strings.Contains(joined, "docker save apothecary/ws:abc |") || !strings.Contains(joined, "docker load") {
		t.Errorf("expected save|load into the inner daemon: %v", load.args)
	}
}

// The guards refuse a /tmp RunsDir (dind masks it) and an over-long socket
// path — with the test seam OFF.
func TestSandboxGuardsRejectTmp(t *testing.T) {
	a := newTestAdapter(t, &scriptRunner{})
	a.noSandboxGuard = false
	a.cfg.RunsDir = filepath.Join(os.TempDir(), "apothecary-runs")
	err := a.ensureSandbox(context.Background(), "run1", ports.EnvSpec{})
	if err == nil || !strings.Contains(err.Error(), "masks /tmp") {
		t.Fatalf("want /tmp guard error, got %v", err)
	}
}

// Links must point the human's copy-paste docker command at the inner daemon.
func TestLinksPrefixesDockerHostForDinD(t *testing.T) {
	r := &scriptRunner{responses: []response{{stdout: "workspace_ctr\n"}}}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	links, err := a.Links(context.Background(), "apothecary-run1")
	if err != nil {
		t.Fatal(err)
	}
	want := "DOCKER_HOST=unix://" + a.sockPath("apothecary-run1") + " docker exec -it workspace_ctr bash"
	if len(links) != 1 || links[0].Command != want {
		t.Errorf("link command = %q, want %q", links[0].Command, want)
	}
}

func TestDestroyAbsentIsNoop(t *testing.T) {
	// No sandbox, no legacy workspace container → the two ps queries and then
	// the folder cleanup, nothing removed.
	r := &scriptRunner{responses: []response{{stdout: ""}, {stdout: ""}}}
	a := newTestAdapter(t, r)
	if err := a.Destroy(context.Background(), "apothecary-run1"); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		if len(c.args) > 0 && c.args[0] == "rm" {
			t.Errorf("absent destroy must not remove anything: %v", c.args)
		}
	}
}

func TestLinksAreTerminalOnly(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "eager_apothecary\n"}, // ps names
	}}
	a := newTestAdapter(t, r)
	links, err := a.Links(context.Background(), "apothecary-run1")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Kind != "terminal" ||
		links[0].Command != "docker exec -it eager_apothecary bash" {
		t.Errorf("links = %+v", links)
	}
}

func TestAppPortsParsesDeclaredPorts(t *testing.T) {
	rc := `{"configuration":{"appPort":9000},"mergedConfiguration":{"forwardPorts":[3000,"5432","db:5432"]}}`
	r := &scriptRunner{responses: []response{{stdout: rc + "\n"}}}
	a := newTestAdapter(t, r)
	got := a.AppPorts(context.Background(), "apothecary-run1")
	if !slices.Equal(got, []int{3000, 5432, 9000}) {
		t.Errorf("app ports = %v (service-qualified must be skipped)", got)
	}
}

// The relay runs INSIDE the workspace container, so its loopback only reaches
// what the workspace itself serves. A compose sibling (a database, a cache)
// has its own network namespace and answers at <service>:<port> — relaying to
// 127.0.0.1 there just gets connection-refused.
func TestRelayTargetResolvesComposeSiblings(t *testing.T) {
	const (
		selfCID  = "cid1"
		projectQ = `projA` // docker inspect <cid> -> compose project label
	)
	cases := []struct {
		name      string
		port      int
		responses []response
		want      string
	}{{
		name:      "not a compose workspace keeps loopback",
		port:      3000,
		responses: []response{{stdout: "\n"}}, // no compose project label
		want:      "127.0.0.1",
	}, {
		name: "sibling declaring the port wins",
		port: 5432,
		responses: []response{
			{stdout: projectQ},                   // project label
			{stdout: selfCID + "\ncid2\n"},       // ps -q (self + sibling)
			{stdout: `postgres {"5432/tcp":{}}`}, // cid2 metadata
		},
		want: "postgres",
	}, {
		// An app's dev-server port is started at runtime and never declared,
		// so an unmatched port must still resolve to the workspace itself.
		name: "no sibling declares it falls back to loopback",
		port: 3000,
		responses: []response{
			{stdout: projectQ},
			{stdout: selfCID + "\ncid2\n"},
			{stdout: `postgres {"5432/tcp":{}}`},
		},
		want: "127.0.0.1",
	}, {
		name: "ties resolve deterministically",
		port: 5432,
		responses: []response{
			{stdout: projectQ},
			{stdout: selfCID + "\ncid2\ncid3\n"},
			{stdout: `postgres {"5432/tcp":{}}`},
			{stdout: `cache {"5432/tcp":{}}`},
		},
		want: "cache", // alphabetically first, and logged
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestAdapter(t, &scriptRunner{responses: tc.responses})
			if got := a.relayTarget(context.Background(), "ws1", selfCID, tc.port); got != tc.want {
				t.Errorf("relayTarget(%d) = %q, want %q", tc.port, got, tc.want)
			}
		})
	}
}

// End to end through ExposePort: the reverse-relay spec must carry the
// sibling's service name, not the workspace's loopback.
func TestExposePortTargetsSiblingService(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "cid1\n"},                   // docker ps -q (workspace container)
		{stdout: "projA"},                    // compose project label
		{stdout: "cid1\ncid2\n"},             // ps -q for the project
		{stdout: `postgres {"5432/tcp":{}}`}, // sibling metadata
		{code: 0},                            // docker exec -d relay --reverse
	}}
	a := newTestAdapter(t, r)
	sock := filepath.Join(a.folderFor("apothecary-run1"), "shim", "app-5432.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	if _, err := a.ExposePort(context.Background(), "apothecary-run1", 5432); err != nil {
		t.Fatal(err)
	}
	relay := find(t, r.calls, "docker", "exec")
	want := "--reverse /opt/apothecary/app-5432.sock:postgres:5432"
	if !strings.Contains(strings.Join(relay.args, " "), want) {
		t.Errorf("relay spec must target the sibling service (%s): %v", want, relay.args)
	}
}

func TestExposePortStartsReverseRelayAndDials(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "cid1\n"}, // docker ps -q
		{code: 0},          // docker exec -d relay --reverse
	}}
	a := newTestAdapter(t, r)
	sock := filepath.Join(a.folderFor("apothecary-run1"), "shim", "app-3000.sock")
	// Play the in-container relay: create the unix listener the adapter
	// polls for, echoing one payload.
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = c.Write([]byte("app-payload"))
	}()

	pc, err := a.ExposePort(context.Background(), "apothecary-run1", 3000)
	if err != nil {
		t.Fatal(err)
	}
	relay := find(t, r.calls, "docker", "exec")
	joined := strings.Join(relay.args, " ")
	for _, want := range []string{"-d cid1 /opt/apothecary/shim relay", "--reverse /opt/apothecary/app-3000.sock:127.0.0.1:3000"} {
		if !strings.Contains(joined, want) {
			t.Errorf("reverse relay args missing %q: %v", want, relay.args)
		}
	}
	c, err := pc.Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(c)
	c.Close()
	if string(got) != "app-payload" {
		t.Errorf("dialed payload = %q", got)
	}
	_ = pc.Close()
}

func TestBridgePumpsBytesOverUnixSocket(t *testing.T) {
	// Real engine-side TCP listener the bridge forwards to.
	engine, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	go func() {
		for {
			c, err := engine.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err == nil {
					_, _ = c.Write([]byte("pong-" + string(buf)))
				}
			}(c)
		}
	}()
	_, portStr, _ := net.SplitHostPort(engine.Addr().String())
	port, _ := net.LookupPort("tcp", portStr)

	a := newTestAdapter(t, &scriptRunner{})
	a.listen = net.Listen
	a.cfg.EnginePort = port
	if err := a.ensureBridge("ws1"); err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	sock := filepath.Join(a.folderFor("ws1"), "shim", "engine.sock")
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "pong-ping" {
		t.Fatalf("round trip = %q, %v", got, err)
	}

	// Idempotent per env: a second ensure opens nothing new.
	if err := a.ensureBridge("ws1"); err != nil {
		t.Fatal(err)
	}
	if n := len(a.bridges["ws1"]); n != 1 {
		t.Errorf("listeners = %d, want 1", n)
	}
}

func TestBridgeOpensProxySocketWhenProxyModeOn(t *testing.T) {
	a := newTestAdapter(t, &scriptRunner{})
	a.cfg.ProxyPort = 9090
	var bound []string
	a.listen = func(network, addr string) (net.Listener, error) {
		bound = append(bound, network+"|"+filepath.Base(addr))
		return net.Listen("unix", addr)
	}
	if err := a.ensureBridge("ws1"); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	joined := strings.Join(bound, " ")
	for _, want := range []string{"unix|engine.sock", "unix|proxy.sock"} {
		if !strings.Contains(joined, want) {
			t.Errorf("bridge sockets missing %q: %v", want, bound)
		}
	}
}

func TestStartRelayDetachesStaticShim(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "cid1\n"}, // docker ps -q
		{code: 0},          // docker exec -d
	}}
	a := newTestAdapter(t, r)
	a.cfg.ProxyPort = 9090
	if err := a.startRelay(context.Background(), "apothecary-run1"); err != nil {
		t.Fatal(err)
	}
	relay := find(t, r.calls, "docker", "exec")
	joined := strings.Join(relay.args, " ")
	for _, want := range []string{
		"-d cid1 /opt/apothecary/shim relay",
		"8080:/opt/apothecary/engine.sock",
		"9090:/opt/apothecary/proxy.sock",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("relay start missing %q: %v", want, relay.args)
		}
	}
}

func TestTestProbeReportsMissingCLI(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{err: errors.New("exec: not found")},
		{err: errors.New("exec: not found")},
		{err: errors.New("exec: not found")},
	}}
	a := newTestAdapter(t, r)
	err := a.Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "npm install -g @devcontainers/cli") {
		t.Fatalf("want install hint, got %v", err)
	}
}

func TestSpawnFailureRetries(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{err: errors.New("fork: retry")},
		{stdout: "ok\n"},
	}}
	a := newTestAdapter(t, r)
	res, err := a.Exec(context.Background(), "ws", ports.Command{Script: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "ok\n" || len(r.calls) != 2 {
		t.Errorf("output=%q calls=%d", res.Output, len(r.calls))
	}
}

func TestWorkspaceName(t *testing.T) {
	if got := WorkspaceName("01J8ULIDULIDULIDULIDULID26"); got != "apothecary-idulidulidulid26" {
		t.Errorf("name = %s", got)
	}
	if got := WorkspaceName("RUN1"); got != "apothecary-run1" {
		t.Errorf("name = %s", got)
	}
}

func TestCredentialLine(t *testing.T) {
	line, err := credentialLine("https://gitlab.local/grp/repo.git", "", "p@ss w")
	if err != nil {
		t.Fatal(err)
	}
	if line != "https://oauth2:p%40ss+w@gitlab.local" {
		t.Errorf("line = %q", line)
	}
}
