package workspace

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
	runs := t.TempDir()
	// The tools bundle, pre-"built": the dir a real EnsureTools returns.
	toolsDir := filepath.Join(runs, "tools-cache", "t0st")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	a := NewWithRunner(Config{
		RunsDir:  runs,
		ShimBin:  func(context.Context) (string, error) { return shimSrc, nil },
		ToolsDir: func(context.Context) (string, error) { return toolsDir, nil },
		// No GatewayImage ⇒ a.gw is nil ⇒ the egress gateway path is skipped and
		// the sandbox runs on the default bridge, keeping the scripted-runner
		// lifecycle tests independent of the (docker-heavy) gateway.
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

// seedDevcontainer writes a minimal .devcontainer/devcontainer.json into the
// env's checkout so hasRepoConfig passes — every repo must ship one now that
// the pipeline-image fallback is gone.
func seedDevcontainer(t *testing.T, a *Adapter, name string) {
	t.Helper()
	dc := filepath.Join(a.srcDir(name), ".devcontainer")
	if err := os.MkdirAll(dc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dc, "devcontainer.json"), []byte(`{"image":"debian:bookworm-slim"}`), 0o644); err != nil {
		t.Fatal(err)
	}
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
			{stdout: upOK},     // devcontainer up
			{code: 0},          // docker exec root-init
			{stdout: "cid1\n"}, // docker ps -q (relay)
			{code: 0},          // docker exec -d relay
			{code: 0},          // docker exec -d portscan
			{code: 0},          // ready/relay probe
		})...)}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	seedDevcontainer(t, a, "apothecary-run1")
	id, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "RUN1", RepoURL: "https://g.local/grp/repo.git",
		GitUser: "oauth2", GitPass: "sekret",
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
		// The tools bundle (harness CLIs + code-server) rides in beside the
		// shim — nothing is injected into the repo's build.
		"source=" + filepath.Join(a.cfg.RunsDir, "tools-cache", "t0st") + ",target=/opt/apothecary/tools",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("up args missing %q: %v", want, up.args)
		}
	}
	// The repo brings its own devcontainer — no apothecary override config.
	if strings.Contains(joined, "--override-config") {
		t.Errorf("repo has its own devcontainer; no override expected: %v", up.args)
	}
	shim, err := os.ReadFile(filepath.Join(a.folderFor("apothecary-run1"), "shim", "shim"))
	if err != nil || string(shim) != "#!fake-shim" {
		t.Errorf("shim binary not materialized: %q, %v", shim, err)
	}
	// The callable alias: relative, so it resolves in the container too.
	if target, err := os.Readlink(filepath.Join(a.folderFor("apothecary-run1"), "shim", "apothecary-shim")); err != nil || target != "shim" {
		t.Errorf("apothecary-shim alias = %q, %v; want -> shim", target, err)
	}

	init := find(t, r.calls, "docker", "exec")
	joined = strings.Join(init.args, " ")
	for _, want := range []string{"-u root", "cid1", "mkdir -p /apothecary", "ln -sfn '/workspaces/src'",
		// The bundle's entries, linked into /opt/apothecary for the libc the
		// image can run; the editor's per-run state dir, owned by the remote user.
		"/opt/apothecary/tools/$libc/bin", "ld-musl-x86_64", "/apothecary/code-server", "chown 'vscode' /apothecary /apothecary/code-server",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("root-init missing %q: %v", want, init.args)
		}
	}

	// The engine bridge came up (only the engine socket now — egress is external).
	if n := len(a.bridges["apothecary-run1"]); n != 1 {
		t.Errorf("bridge listeners = %d, want 1 (engine)", n)
	}
	a.Close()
}

// Provisioning streams into the same exec.log the UI tails (TailExecLog), so
// init/build progress shows live before the env is ready — not just post-ready
// command output.
func TestProvisionStreamsToExecLog(t *testing.T) {
	r := &scriptRunner{responses: append(
		[]response{{code: 0}}, // git clone
		sandboxResp([]response{
			{stdout: upOK},     // devcontainer up (teed into exec.log)
			{code: 0},          // root-init
			{stdout: "cid1\n"}, // ps -q (relay)
			{code: 0},          // relay exec
			{code: 0},          // portscan exec
			{code: 0},          // ready probe
		})...)}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	seedDevcontainer(t, a, "apothecary-run1")
	if _, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "RUN1", RepoURL: "https://g.local/grp/repo.git",
	}); err != nil {
		t.Fatal(err)
	}
	log, err := a.TailExecLog(context.Background(), "apothecary-run1", 0)
	if err != nil {
		t.Fatalf("tail exec log: %v", err)
	}
	for _, want := range []string{"provisioning apothecary-run1", "preparing source checkout",
		"per-run sandbox + egress gateway", "building devcontainer"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("exec.log missing provisioning marker %q:\n%s", want, log)
		}
	}
}

// The engine supplies a human-friendly, slug-of-branch name; the adapter must
// key the env off it (id-label + checkout dir) AND rename the CLI-built
// container to it so `docker ps` shows the branch, not the run-id hash.
func TestProvisionUsesEngineSuppliedName(t *testing.T) {
	const name = "apo-fix-branch-abcd"
	r := &scriptRunner{responses: append(
		[]response{{code: 0}}, // git clone
		sandboxResp([]response{
			{stdout: upOK},     // devcontainer up
			{code: 0},          // docker exec root-init
			{stdout: "cid1\n"}, // docker ps -q (relay)
			{code: 0},          // docker exec -d relay
			{code: 0},          // docker exec -d portscan
			{code: 0},          // ready/relay probe
			{code: 0},          // docker rename
		})...)}
	a := newTestAdapter(t, r)
	armSandbox(t, a, name)
	seedDevcontainer(t, a, name)
	id, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "RUN1", Name: name, Project: "01HACMEWEB", ProjectName: "Acme Web",
		RepoURL: "https://g.local/r.git",
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
	if !strings.Contains(upJoined, "--id-label apothecary.project=01HACMEWEB") {
		t.Errorf("up must label the project by id: %v", up.args)
	}
	// The name is NOT an id-label: the CLI matches an existing container on
	// every id-label, so a renamed project would lose its own container.
	if strings.Contains(upJoined, "apothecary.project.name") {
		t.Errorf("up must not id-label the project name: %v", up.args)
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
		{code: 0},          // docker exec -d portscan
		{code: 0},          // ready/relay probe
	})}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	src := filepath.Join(a.folderFor("apothecary-run1"), "src")
	// .git is a real dir: its presence makes ensureSrc skip cloning.
	for _, d := range []string{filepath.Join(src, ".devcontainer"), filepath.Join(src, ".git")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, ".devcontainer", "devcontainer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
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
	// .git is a real directory here (unlike the minimal fixtures elsewhere),
	// so the checkout counts as cloned.
	for _, d := range []string{filepath.Join(src, ".devcontainer"), filepath.Join(src, ".git")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, ".devcontainer", "devcontainer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

// A repo-authored dev container runs the REPO's image, which has no harness
// and no code-server. They arrive as the bind-mounted tools bundle, exactly
// like the shim: `up` carries the mount and injects NOTHING into the repo's
// build — no features (the firewall is the external gateway, the harness and
// code-server are the bundle), no override config, nothing written under
// .devcontainer/.
func TestRepoDevcontainerMountsToolsNotFeatures(t *testing.T) {
	r := &scriptRunner{responses: sandboxResp([]response{
		{stdout: upOK},     // up
		{code: 0},          // root-init
		{stdout: "cid1\n"}, // docker ps -q (relay)
		{code: 0},          // docker exec -d relay
		{code: 0},          // docker exec -d portscan
		{code: 0},          // ready/relay probe
	})}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	src := seedRepoDevcontainer(t, a, "apothecary-run1")

	if _, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "run1", RepoURL: "https://g.local/r.git"}); err != nil {
		t.Fatal(err)
	}
	up := find(t, r.calls, "devcontainer", "up")
	joined := strings.Join(up.args, " ")
	for _, want := range []string{
		"--mount type=bind,source=" + filepath.Join(a.folderFor("apothecary-run1"), "shim") + ",target=/opt/apothecary",
		"--mount type=bind,source=" + filepath.Join(a.cfg.RunsDir, "tools-cache", "t0st") + ",target=/opt/apothecary/tools",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("up args missing %q: %v", want, up.args)
		}
	}
	for _, banned := range []string{"--additional-features", "apothecary-harness", "apothecary-codeserver", "apothecary-firewall", "--override-config"} {
		if strings.Contains(joined, banned) {
			t.Errorf("up must inject nothing (%q): %v", banned, up.args)
		}
	}
	entries, err := os.ReadDir(filepath.Join(src, ".devcontainer"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "devcontainer.json" {
		t.Errorf("the repo's .devcontainer must stay untouched, got %v", entries)
	}
	if _, err := os.Stat(filepath.Join(src, ".git", "info", "exclude")); err == nil {
		t.Error("nothing is materialised into the checkout, so there is nothing to git-exclude")
	}
}

func TestProvisionRejectsRepoWithoutDevcontainer(t *testing.T) {
	// The guard fires right after the checkout, before the sandbox — only the
	// clone runs; no config on disk means the repo is unsupported.
	r := &scriptRunner{responses: []response{{code: 0}}} // clone
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	_, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "run1", RepoURL: "https://g.local/r.git"})
	if err == nil || !strings.Contains(err.Error(), ".devcontainer/devcontainer.json") {
		t.Fatalf("want no-devcontainer error, got %v", err)
	}
}

func TestProvisionSurfacesUpFailure(t *testing.T) {
	r := &scriptRunner{responses: append(
		[]response{{code: 0}}, // clone
		sandboxResp([]response{
			{stdout: `{"outcome":"error","message":"Dockerfile failed","description":"exit 1"}`, stderr: "build log tail", code: 1},
		})...)}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	seedDevcontainer(t, a, "apothecary-run1")
	_, err := a.Provision(context.Background(), ports.EnvSpec{RunID: "run1", RepoURL: "u"})
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
	if !strings.Contains(stdin, `cd "$HOME/repo"`) || !strings.Contains(stdin, "make test") {
		t.Errorf("stdin = %q", stdin)
	}
	// The shim dir is appended (never prepended) to the script's PATH.
	if !strings.HasPrefix(stdin, `export PATH="$PATH:/opt/apothecary"`+"\n") {
		t.Errorf("stdin missing the shim PATH append: %q", stdin)
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

// Non-compose Rebuild recreates the single container: the up argv carries
// --remove-existing-container, and the build output is teed into exec.log so
// the run page's workspace-log poll shows it.
func TestRebuildNonComposeRemovesContainer(t *testing.T) {
	r := &scriptRunner{responses: append(
		[]response{
			{stdout: `{"mergedConfiguration":{}}`}, // Rebuild isCompose: not compose
			{code: 0},                              // ensureSrc git clone
		},
		sandboxResp([]response{
			{stdout: upOK},     // devcontainer up (teed into exec.log)
			{code: 0},          // root-init
			{stdout: "cid1\n"}, // docker ps -q (relay)
			{code: 0},          // docker exec -d relay
			{code: 0},          // docker exec -d portscan
			{code: 0},          // ready probe
		})...)}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	seedDevcontainer(t, a, "apothecary-run1") // non-compose devcontainer
	err := a.Rebuild(context.Background(), ports.EnvSpec{
		RunID: "run1", Name: "apothecary-run1",
		RepoURL: "https://g.local/r.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	up := find(t, r.calls, "devcontainer", "up")
	if !strings.Contains(strings.Join(up.args, " "), "--remove-existing-container") {
		t.Errorf("rebuild up must force recreate: %v", up.args)
	}
	// The up output was teed into exec.log (proves the streaming wiring).
	log, err := os.ReadFile(filepath.Join(a.folderFor("apothecary-run1"), "exec.log"))
	if err != nil || !strings.Contains(string(log), "success") {
		t.Errorf("up output not teed to exec.log: %q (%v)", log, err)
	}
}

// editorExec is the one call StartEditor makes: an Exec (devcontainer exec,
// the script on stdin), so the editor runs as the remote user with the
// workspace's own environment — what makes its integrated terminal a real
// workspace shell.
func editorExec(t *testing.T, r *scriptRunner) string {
	t.Helper()
	c := find(t, r.calls, "devcontainer", "exec")
	if !strings.Contains(strings.Join(c.args, " "), "--id-label apothecary.env=apothecary-run1") {
		t.Errorf("editor must start through the workspace exec path: %v", c.args)
	}
	return string(c.stdin)
}

// The extensions the editor installs are the dedup union of the repo's
// devcontainer.json customizations and the project list (sorted, so the
// installed-list marker is stable), installed on first open into the per-run
// extensions dir — never baked into an image.
func TestStartEditorInstallsExtensionUnion(t *testing.T) {
	r := &scriptRunner{responses: []response{{code: 0}}} // the one exec
	a := newTestAdapter(t, r)
	// Repo devcontainer declares extensions (JSONC with a comment + trailing
	// comma); the project adds one, with an overlap to dedup.
	dc := filepath.Join(a.srcDir("apothecary-run1"), ".devcontainer")
	if err := os.MkdirAll(dc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dc, "devcontainer.json"), []byte(
		`{ "image": "debian", // repo config
  "customizations": { "vscode": { "extensions": ["golang.go", "ms-python.python",] } } }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.StartEditor(context.Background(), "apothecary-run1", ports.CodeServerPort, ports.EditorSettings{
		Extensions: []string{"ms-python.python", " esbenp.prettier-vscode "},
	}); err != nil {
		t.Fatal(err)
	}
	script := editorExec(t, r)
	for _, want := range []string{
		"want='esbenp.prettier-vscode\ngolang.go\nms-python.python'",
		`"$cs" --install-extension "$ext" --extensions-dir "$state/extensions"`,
		"state=/apothecary/code-server",
		`"$state/extensions.installed"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("editor script missing %q:\n%s", want, script)
		}
	}
	// No gateway marker: code-server reaches Open VSX itself.
	if strings.Contains(script, "EXTENSIONS_GALLERY") {
		t.Errorf("no gateway, so no gallery override:\n%s", script)
	}
}

func TestExtensionUnion(t *testing.T) {
	got := extensionUnion([]string{"b.x", " a.y ", ""}, []string{"a.y", "c.z"}, nil)
	if want := []string{"a.y", "b.x", "c.z"}; !slices.Equal(got, want) {
		t.Errorf("union = %v, want %v", got, want)
	}
	if got := extensionUnion(nil, []string{" "}); len(got) != 0 {
		t.Errorf("union of nothing = %v", got)
	}
}

// StartEditor's script: the bundle's code-server (an image without it — musl
// — exits with the dedicated code, reported as such), the project's settings
// re-applied on every call, a probe that leaves a listening code-server
// alone, otherwise a detached start and a bounded wait; with a gateway, the
// extension gallery is the gateway's cache.
func TestStartEditor(t *testing.T) {
	t.Run("script shape", func(t *testing.T) {
		r := &scriptRunner{responses: []response{{code: 0}}}
		a := newTestAdapter(t, r)
		if err := a.StartEditor(context.Background(), "apothecary-run1", ports.CodeServerPort, ports.EditorSettings{Theme: "Monokai"}); err != nil {
			t.Fatal(err)
		}
		script := editorExec(t, r)
		for _, want := range []string{
			"cs=/opt/apothecary/code-server",
			"addr=127.0.0.1:8471",
			`if ! apothecary-shim probe "$addr"; then`,
			`exec "$cs" --auth none --disable-telemetry --bind-addr "$addr" --extensions-dir "$state/extensions" "$HOME/repo"`,
			`</dev/null >>"$state/code-server.log" 2>&1 &`,
			`apothecary-shim probe --wait 20s "$addr"`,
			`"workbench.colorTheme": "Monokai"`,
			`"$HOME/.local/share/code-server/User/settings.json"`,
		} {
			if !strings.Contains(script, want) {
				t.Errorf("editor script missing %q:\n%s", want, script)
			}
		}
	})
	t.Run("no settings → settings.json untouched", func(t *testing.T) {
		r := &scriptRunner{responses: []response{{code: 0}}}
		a := newTestAdapter(t, r)
		if err := a.StartEditor(context.Background(), "apothecary-run1", ports.CodeServerPort, ports.EditorSettings{}); err != nil {
			t.Fatal(err)
		}
		if script := editorExec(t, r); strings.Contains(script, "settings.json") {
			t.Errorf("nothing set, yet the script writes settings.json:\n%s", script)
		}
	})
	t.Run("gateway → gallery through its cache", func(t *testing.T) {
		r := &scriptRunner{responses: []response{{code: 0}}}
		a := newTestAdapter(t, r)
		a.recordGateway("apothecary-run1", "01HACME", "172.20.0.2")
		if err := a.StartEditor(context.Background(), "apothecary-run1", ports.CodeServerPort, ports.EditorSettings{}); err != nil {
			t.Fatal(err)
		}
		script := editorExec(t, r)
		for _, want := range []string{
			`export EXTENSIONS_GALLERY='{"serviceUrl":"http://172.20.0.2:5002/vscode/gallery","itemUrl":"http://172.20.0.2:5002/vscode/item","resourceUrlTemplate":"http://172.20.0.2:5002/vscode/unpkg/{publisher}/{name}/{version}/{path}"}'`,
		} {
			if !strings.Contains(script, want) {
				t.Errorf("editor script missing %q:\n%s", want, script)
			}
		}
	})
	t.Run("musl image → no code-server", func(t *testing.T) {
		r := &scriptRunner{responses: []response{{code: codeServerMissingExit, stderr: "no code-server"}}}
		a := newTestAdapter(t, r)
		err := a.StartEditor(context.Background(), "apothecary-run1", ports.CodeServerPort, ports.EditorSettings{})
		if err == nil || !strings.Contains(err.Error(), "glibc") {
			t.Fatalf("err = %v, want the glibc explanation", err)
		}
	})
	t.Run("other failure → reported", func(t *testing.T) {
		r := &scriptRunner{responses: []response{{code: 1, stderr: "did not start listening"}}}
		a := newTestAdapter(t, r)
		err := a.StartEditor(context.Background(), "apothecary-run1", ports.CodeServerPort, ports.EditorSettings{})
		if err == nil || !strings.Contains(err.Error(), "did not start listening") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestStripJSONC(t *testing.T) {
	in := `{
  // a comment with a // and a "quote"
  "a": "http://x//y", /* block */
  "b": [1, 2,],
}`
	var got map[string]any
	if err := json.Unmarshal(stripJSONC([]byte(in)), &got); err != nil {
		t.Fatalf("stripped JSONC did not parse: %v\n%s", err, stripJSONC([]byte(in)))
	}
	if got["a"] != "http://x//y" { // // inside a string must be preserved
		t.Errorf("string with // corrupted: %q", got["a"])
	}
}

// A host-daemon failure on the sandbox probe must surface as an ERROR — not a
// silent "" that maps to EnvAbsent — so recovery waits instead of nuking a
// live workspace (the recovery-disaster fix).
func TestStatusHostDaemonErrorPropagates(t *testing.T) {
	for _, c := range []struct {
		name string
		resp []response
	}{
		// a.run retries an exec error 3× before giving up, so exhaust them.
		{"exec error", []response{
			{err: errors.New("cannot connect to docker daemon")},
			{err: errors.New("cannot connect to docker daemon")},
			{err: errors.New("cannot connect to docker daemon")},
		}},
		{"nonzero exit", []response{{code: 1, stderr: "daemon not running"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newTestAdapter(t, &scriptRunner{responses: c.resp})
			armSandbox(t, a, "apothecary-run1")
			st, err := a.Status(context.Background(), "apothecary-run1")
			if err == nil {
				t.Fatalf("want error, got status %q", st)
			}
			if st == ports.EnvAbsent {
				t.Error("a daemon blip must not read as EnvAbsent")
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
		RunID: "run1", Project: "01HACME", ProjectName: "Acme", RepoURL: "https://g.local/r.git",
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
		"--label apothecary.project=01HACME",
		"--label apothecary.project.name=Acme",
		"-v apothecary-run1-docker:/var/lib/docker",
		"-v " + root + ":" + root,
		// The tools bundles (every version, read-only), same path in host +
		// sandbox; and the shape label that says this sandbox has the mount.
		"-v " + filepath.Join(a.cfg.RunsDir, "tools-cache") + ":" + filepath.Join(a.cfg.RunsDir, "tools-cache") + ":ro",
		"--label apothecary.sandbox.shape=2",
		"docker:29-dind dockerd --host=unix://" + a.sockPath("apothecary-run1"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sandbox run missing %q: %v", want, run.args)
		}
	}
	if envHas(run, "DOCKER_HOST") {
		t.Errorf("sandbox lifecycle must NOT carry the inner DOCKER_HOST: %v", run.env)
	}
	// This adapter has no gateway (newTestAdapter passes no GatewayImage), so
	// there is no mirror to point the inner daemon at.
	if strings.Contains(joined, "--registry-mirror") {
		t.Errorf("gatewayless sandbox must not get mirror flags: %v", run.args)
	}
}

// Opting into a user-namespace runtime (APOTHECARY_SANDBOX_RUNTIME) swaps
// --privileged for --runtime: the two are mutually exclusive (sysbox rejects
// privileged containers), and keeping the flag would defeat the point.
func TestSandboxRuntimeReplacesPrivileged(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: `{"runc":{},"sysbox-runc":{}}`}, // docker info: runtime present
		{stdout: ""},                             // sandboxIDs: none yet
		{code: 0},                                // volume create
		{stdout: "sbx1\n"},                       // docker run
		{stdout: "29.6.2"},                       // readiness probe
	}}
	a := newTestAdapter(t, r)
	a.cfg.SandboxRuntime = "sysbox-runc"
	const name = "apothecary-run1"
	armSandbox(t, a, name)
	if err := a.ensureSandbox(context.Background(), name, ports.EnvSpec{Project: "Acme"}, "", "/tc"); err != nil {
		t.Fatal(err)
	}
	run := find(t, r.calls, "docker", "run")
	joined := strings.Join(run.args, " ")
	if !strings.Contains(joined, "--runtime sysbox-runc") {
		t.Errorf("sandbox run missing the configured runtime: %v", run.args)
	}
	if strings.Contains(joined, "--privileged") {
		t.Errorf("an unprivileged runtime must NOT also get --privileged: %v", run.args)
	}
	// The labels/mounts/dockerd tail are unchanged by the runtime swap.
	for _, want := range []string{
		"--label apothecary.sandbox=true",
		"-v apothecary-run1-docker:/var/lib/docker",
		"docker:29-dind dockerd --host=unix://" + a.sockPath(name),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sandbox run missing %q: %v", want, run.args)
		}
	}
}

// A runtime the daemon doesn't have fails BEFORE anything is created, naming
// the deployment variable — not minutes later with docker's bare "Unknown
// runtime specified".
func TestSandboxRuntimeMissingFailsEarly(t *testing.T) {
	r := &scriptRunner{responses: []response{{stdout: `{"runc":{},"nvidia":{}}`}}}
	a := newTestAdapter(t, r)
	a.cfg.SandboxRuntime = "sysbox-runc"
	err := a.ensureSandbox(context.Background(), "apothecary-run1", ports.EnvSpec{}, "", "/tc")
	if err == nil {
		t.Fatal("expected a runtime-not-installed error")
	}
	for _, want := range []string{"APOTHECARY_SANDBOX_RUNTIME=sysbox-runc", "nvidia, runc"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
	for _, c := range r.calls {
		if len(c.args) > 0 && c.args[0] == "run" {
			t.Errorf("nothing must be created when the runtime is missing: %v", c.args)
		}
	}
}

// The default (unset) deployment is byte-identical to before: privileged
// sandbox, and no docker info probe at all.
func TestSandboxRuntimeUnsetKeepsPrivileged(t *testing.T) {
	_, r := provisionRepoDinD(t, repoUpTail)
	run := find(t, r.calls, "docker", "run")
	if !strings.Contains(strings.Join(run.args, " "), "--privileged") {
		t.Errorf("default sandbox must stay privileged: %v", run.args)
	}
	for _, c := range r.calls {
		if len(c.args) > 0 && c.args[0] == "info" {
			t.Errorf("no runtime probe when APOTHECARY_SANDBOX_RUNTIME is unset: %v", c.args)
		}
	}
}

// A sandbox created before the tools-cache parity mount existed (no shape
// label) would make `up` fail on the missing bind source — on a Rebuild or a
// recovery re-provision, both of which reuse an existing sandbox. It is
// recreated on first use instead; its /var/lib/docker volume stays.
func TestPreBundleSandboxIsRecreated(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "sbx0\n"}, // sandboxIDs: an existing sandbox...
		{stdout: ""},       // ...without the current shape label
		{code: 0},          // docker rm -f sbx0
		{code: 0},          // volume create (idempotent)
		{stdout: "sbx1\n"}, // docker run
		{stdout: "29.6.2"}, // readiness probe
	}}
	a := newTestAdapter(t, r)
	const name = "apothecary-run1"
	armSandbox(t, a, name)
	tc := filepath.Join(a.cfg.RunsDir, "tools-cache")
	if err := a.ensureSandbox(context.Background(), name, ports.EnvSpec{}, "", tc); err != nil {
		t.Fatal(err)
	}
	rmAt, runAt := -1, -1
	for i, c := range r.calls {
		joined := strings.Join(c.args, " ")
		switch {
		case strings.HasPrefix(joined, "rm -f"):
			rmAt = i
			if !strings.Contains(joined, "sbx0") {
				t.Errorf("must remove the old sandbox: %v", c.args)
			}
		case strings.HasPrefix(joined, "run "):
			runAt = i
			if !strings.Contains(joined, "-v "+tc+":"+tc+":ro") || !strings.Contains(joined, "--label apothecary.sandbox.shape=2") {
				t.Errorf("recreated sandbox lacks the mount or the shape label: %v", c.args)
			}
		case strings.HasPrefix(joined, "start"):
			t.Errorf("a previous-shape sandbox must be recreated, not started: %v", c.args)
		case strings.HasPrefix(joined, "volume rm"):
			t.Errorf("the inner daemon's volume must survive: %v", c.args)
		}
	}
	if rmAt < 0 || runAt < 0 || rmAt > runAt {
		t.Errorf("expected rm -f then run, got rm@%d run@%d: %v", rmAt, runAt, r.calls)
	}
}

// A sandbox of the current shape is simply started (a no-op when up).
func TestCurrentShapeSandboxIsStarted(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "sbx1\n"}, // sandboxIDs
		{stdout: "sbx1\n"}, // ...and it carries the shape label
		{code: 0},          // docker start
		{stdout: "29.6.2"}, // readiness probe
	}}
	a := newTestAdapter(t, r)
	const name = "apothecary-run1"
	armSandbox(t, a, name)
	if err := a.ensureSandbox(context.Background(), name, ports.EnvSpec{}, "", "/tc"); err != nil {
		t.Fatal(err)
	}
	started := false
	for _, c := range r.calls {
		switch c.args[0] {
		case "start":
			started = true
		case "rm", "run":
			t.Errorf("a current sandbox must be reused: %v", c.args)
		}
	}
	if !started {
		t.Errorf("sandbox never started: %v", r.calls)
	}
}

// With a gateway the inner daemon is pointed at the project's pull-through
// cache on the gateway, over plain HTTP inside the run's --internal network.
func TestCreateSandboxWiresGatewayMirror(t *testing.T) {
	r := &scriptRunner{}
	a := newTestAdapter(t, r)
	const name = "apothecary-run1"
	if err := a.createSandbox(context.Background(), name, ports.EnvSpec{Project: "Acme"}, "172.20.0.2", "/tc"); err != nil {
		t.Fatal(err)
	}
	run := find(t, r.calls, "docker", "run")
	joined := strings.Join(run.args, " ")
	for _, want := range []string{
		"--network " + egressNet(name),
		"--dns 172.20.0.2",
		"--registry-mirror=http://172.20.0.2:5000",
		"--insecure-registry=172.20.0.2:5000",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sandbox run missing %q: %v", want, run.args)
		}
	}
	// The mirror flags are dockerd's, so they must follow the image + dockerd.
	dockerd := strings.Index(joined, "dockerd --host=")
	if mirror := strings.Index(joined, "--registry-mirror"); dockerd < 0 || mirror < dockerd {
		t.Errorf("mirror flags must be dockerd args, not docker run args: %v", run.args)
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

// The guards refuse a /tmp RunsDir (dind masks it) and an over-long socket
// path — with the test seam OFF.
func TestSandboxGuardsRejectTmp(t *testing.T) {
	a := newTestAdapter(t, &scriptRunner{})
	a.noSandboxGuard = false
	a.cfg.RunsDir = filepath.Join(os.TempDir(), "apothecary-runs")
	err := a.ensureSandbox(context.Background(), "run1", ports.EnvSpec{}, "", "/tc")
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

// startRelay also launches the port-discovery scanner alongside the relay,
// excluding the bridge's own forward ports, and a scanner that fails to start
// must not fail the relay (discovery is auxiliary).
func TestStartRelayAlsoStartsPortScan(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "cid1\n"},                     // docker ps -q (relay)
		{code: 0},                              // docker exec -d relay
		{code: 1, stderr: "no such container"}, // docker exec -d portscan FAILS
	}}
	a := newTestAdapter(t, r)
	a.cfg.EnginePort = 8080
	if err := a.startRelay(context.Background(), "apothecary-run1"); err != nil {
		t.Fatalf("startRelay must not fail when the scanner does not start: %v", err)
	}
	var scan *call
	for i := range r.calls {
		if strings.Contains(strings.Join(r.calls[i].args, " "), "portscan") {
			scan = &r.calls[i]
		}
	}
	if scan == nil {
		t.Fatal("no portscan exec issued")
	}
	joined := strings.Join(scan.args, " ")
	for _, want := range []string{"-d cid1 /opt/apothecary/shim portscan", "--exclude", "8080"} {
		if !strings.Contains(joined, want) {
			t.Errorf("portscan args missing %q: %v", want, scan.args)
		}
	}
}

func TestParseWildcardListeners(t *testing.T) {
	table := "sl local rem st\n" +
		" 0: 00000000:1538 00000000:0000 0A 0 0 0 0 0 1\n" + // 0.0.0.0:5432 wildcard LISTEN
		" 1: 0100007F:1F90 00000000:0000 0A 0 0 0 0 0 2\n" + // 127.0.0.1:8080 loopback — NOT reachable cross-container
		" 2: 00000000:0050 0100007F:C001 01 0 0 0 0 0 3\n" + // established — skip
		" 3: 00000000000000000000000000000000:0BB8 x:0 0A 0 0 0 0 0 4\n" // :::3000 v6 wildcard
	got := parseWildcardListeners(table)
	slices.Sort(got)
	if !slices.Equal(got, []int{3000, 5432}) {
		t.Errorf("wildcard listeners = %v, want [3000 5432] (loopback + established excluded)", got)
	}
}

// ScanSiblingPorts discovers listeners in compose SIBLING containers, tags
// them with the service name, primes the expose-routing cache, and degrades
// silently for a sibling whose exec fails.
func TestScanSiblingPorts(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "cid1\n"},             // containerID: ps -q (workspace)
		{stdout: "projA"},              // compose project label
		{stdout: "cid1\ncid2\ncid3\n"}, // ps -q for the project
		{stdout: "db"},                 // cid2 service label
		{stdout: "h\n 0: 00000000:1538 0:0 0A 0 0 0 0 0 1\n"}, // cid2 /proc/net/tcp: 5432
		{stdout: "cache"},           // cid3 service label
		{code: 1, stderr: "no cat"}, // cid3 exec FAILS → skipped silently
	}}
	a := newTestAdapter(t, r)
	got := a.ScanSiblingPorts(context.Background(), "apothecary-run1")
	if len(got) != 1 || got[0].Port != 5432 || got[0].Service != "db" {
		t.Fatalf("sibling ports = %+v, want one {5432 db}", got)
	}
	// The scan primed the routing cache: an undeclared sibling port now
	// resolves to its service instead of loopback.
	if svc := a.scannedSibling("apothecary-run1", 5432); svc != "db" {
		t.Errorf("scannedSibling(5432) = %q, want db", svc)
	}
}

// After a sibling scan, relayTarget routes an UNDECLARED port (no image
// ExposedPorts entry) to the scanned service rather than connection-refusing
// on loopback.
func TestRelayTargetPrefersScannedSibling(t *testing.T) {
	a := newTestAdapter(t, &scriptRunner{responses: []response{
		{stdout: "projA"},        // compose project label
		{stdout: "cid1\ncid2\n"}, // ps -q for the project
		{stdout: `db {}`},        // cid2 declares NO ports
	}})
	a.sibPorts["ws1"] = map[int]string{6379: "cache"} // prior scan
	if got := a.relayTarget(context.Background(), "ws1", "cid1", 6379); got != "cache" {
		t.Errorf("relayTarget(6379) = %q, want cache (from scan cache)", got)
	}
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

func TestStartRelayDetachesStaticShim(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "cid1\n"}, // docker ps -q
		{code: 0},          // docker exec -d
	}}
	a := newTestAdapter(t, r)
	if err := a.startRelay(context.Background(), "apothecary-run1"); err != nil {
		t.Fatal(err)
	}
	relay := find(t, r.calls, "docker", "exec")
	joined := strings.Join(relay.args, " ")
	for _, want := range []string{
		"-d cid1 /opt/apothecary/shim relay",
		"8080:/opt/apothecary/engine.sock",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("relay start missing %q: %v", want, relay.args)
		}
	}
	// Egress is external now — no proxy socket is relayed.
	if strings.Contains(joined, "proxy.sock") {
		t.Errorf("proxy.sock must not be relayed: %v", relay.args)
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

// No gateway, no cache flags. The build cache lives in the project gateway's
// writable registry, so a provisioner running without one must degrade to the
// old behaviour rather than pass buildkit a reference to nothing.
func TestProvisionOmitsCacheFlagsWithoutGateway(t *testing.T) {
	r := &scriptRunner{responses: append(
		[]response{{code: 0}}, // git clone
		sandboxResp([]response{
			{stdout: upOK},
			{code: 0},
			{stdout: "cid1\n"},
			{code: 0},
			{code: 0},
			{code: 0},
		})...)}
	a := newTestAdapter(t, r) // no GatewayImage ⇒ a.gw == nil
	armSandbox(t, a, "apothecary-run1")
	seedDevcontainer(t, a, "apothecary-run1")
	if _, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "RUN1", RepoURL: "https://g.local/grp/repo.git", Project: "acme",
	}); err != nil {
		t.Fatal(err)
	}
	up := find(t, r.calls, "devcontainer", "up")
	joined := strings.Join(up.args, " ")
	if strings.Contains(joined, "--cache-to") || strings.Contains(joined, "--cache-from") {
		t.Errorf("cache flags without a registry to hold the cache: %v", up.args)
	}
}

// "Rebuild without cache" has to actually bypass the cache, not merely skip
// writing it: the point is a floating base tag that moved upstream, which a
// cache key cannot see. The CLI drops its own --cache-from when this is set,
// and --no-cache beats --cache-from in buildkit (verified directly: 1s warm
// vs 32s with --no-cache against the same populated registry).
func TestRebuildWithoutCachePassesNoCache(t *testing.T) {
	r := &scriptRunner{responses: append(
		[]response{{code: 0}}, // git clone
		sandboxResp([]response{
			{stdout: upOK},
			{code: 0},
			{stdout: "cid1\n"},
			{code: 0},
			{code: 0},
			{code: 0},
		})...)}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	seedDevcontainer(t, a, "apothecary-run1")
	if _, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "RUN1", RepoURL: "https://g.local/grp/repo.git", NoCache: true,
	}); err != nil {
		t.Fatal(err)
	}
	up := find(t, r.calls, "devcontainer", "up")
	if !strings.Contains(strings.Join(up.args, " "), "--build-no-cache") {
		t.Errorf("no-cache rebuild did not reach the build: %v", up.args)
	}
}

// ...and the default stays cached. A rebuild that silently threw the cache
// away would cost 77s every time someone recreated a container.
func TestRebuildKeepsTheCacheByDefault(t *testing.T) {
	r := &scriptRunner{responses: append(
		[]response{{code: 0}},
		sandboxResp([]response{
			{stdout: upOK},
			{code: 0},
			{stdout: "cid1\n"},
			{code: 0},
			{code: 0},
			{code: 0},
		})...)}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	seedDevcontainer(t, a, "apothecary-run1")
	if _, err := a.Provision(context.Background(), ports.EnvSpec{
		RunID: "RUN1", RepoURL: "https://g.local/grp/repo.git",
	}); err != nil {
		t.Fatal(err)
	}
	up := find(t, r.calls, "devcontainer", "up")
	if strings.Contains(strings.Join(up.args, " "), "--build-no-cache") {
		t.Errorf("a plain rebuild must keep the cache: %v", up.args)
	}
}

// ListFiles parses the in-container listing into sorted, dir-joined entries
// and reads exit 44 as a directory that is not there.
func TestListFiles(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: "5 ./b.png\n12 ./nested/a.png\nnot a line\n"},
		{code: 44},
	}}
	a := newTestAdapter(t, r)
	files, err := a.ListFiles(context.Background(), "ws", "artifacts/shots/")
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.FileEntry{{Path: "artifacts/shots/b.png", Size: 5}, {Path: "artifacts/shots/nested/a.png", Size: 12}}
	if !slices.Equal(files, want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	if script := string(r.calls[0].stdin); !strings.Contains(script, `"$HOME"/repo/'artifacts/shots'`) || !strings.Contains(script, "find . -type f") {
		t.Errorf("script = %q", script)
	}
	if _, err := a.ListFiles(context.Background(), "ws", "missing/"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want fs.ErrNotExist, got %v", err)
	}
}
