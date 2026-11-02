package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/egressgw/policyfile"
	"apothecary/internal/ports/envport"
)

func TestNewFillsDefaultsAndPicksTheHost(t *testing.T) {
	a := New(Config{})
	c := a.cfg
	if c.DevcontainerBin != "devcontainer" || c.DockerBin != "docker" || c.ProvisionTimeout != 15*time.Minute ||
		c.EnginePort != 8080 || c.DinDImage != "docker:29-dind" {
		t.Errorf("defaults = %+v", c)
	}
	if _, ok := a.host.(dockerHost); !ok {
		t.Errorf("host = %T", a.host)
	}
	if a.gw != nil {
		t.Error("a gateway with no image configured")
	}
	img := func(context.Context) (string, error) { return "gw:1", nil }
	if New(Config{GatewayImage: img}).gw == nil {
		t.Error("an image configured but no gateway")
	}
	if New(Config{GatewayImage: img, GatewayOff: true}).gw != nil {
		t.Error("the firewall=off escape hatch was ignored")
	}
	kept := New(Config{DevcontainerBin: "dc", DockerBin: "dk", ProvisionTimeout: time.Second, EnginePort: 9, DinDImage: "d"})
	if kept.cfg.DevcontainerBin != "dc" || kept.cfg.DockerBin != "dk" || kept.cfg.EnginePort != 9 || kept.cfg.DinDImage != "d" {
		t.Errorf("configured values overwritten: %+v", kept.cfg)
	}
}

// cliRunner's contract: a nonzero exit is a result, and only a process that
// never ran is an error. sh stands in for the CLIs.
func TestCliRunnerContract(t *testing.T) {
	r := &cliRunner{}
	ctx := context.Background()
	var tee bytes.Buffer
	out, errb, code, err := r.Run(ctx, "sh", []string{"-c", `cat; echo "$GIT_TERMINAL_PROMPT$APO_X"; echo oops >&2; exit 3`}, []byte("in\n"), &tee, "APO_X=y")
	if err != nil || code != 3 || string(out) != "in\n0y\n" || string(errb) != "oops\n" {
		t.Errorf("run = %q %q %d %v", out, errb, code, err)
	}
	if !strings.Contains(tee.String(), "oops") {
		t.Errorf("tee = %q", tee.String())
	}
	if _, _, code, err := r.Run(ctx, "sh", []string{"-c", "true"}, nil, nil); err != nil || code != 0 {
		t.Errorf("clean = %d, %v", code, err)
	}
	if _, _, _, err := r.Run(ctx, filepath.Join(t.TempDir(), "absent"), nil, nil, nil); err == nil {
		t.Error("a process that never ran must be an error")
	}
}

func TestRunGivesUpWithItsContext(t *testing.T) {
	boom := errors.New("spawn failed")
	t.Run("already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		r := &ruleRunner{rules: []rule{{match: "x", resp: response{err: boom}, fn: func([]string, []byte) *response {
			cancel()
			return nil
		}}}}
		a := newTestAdapter(t, r)
		if _, _, _, err := a.run(ctx, "x", nil, nil, nil); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
		if n := len(r.snapshot()); n != 1 {
			t.Errorf("retried %d times after cancel", n)
		}
	})
	t.Run("cancelled during the backoff", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := &ruleRunner{rules: []rule{{match: "x", resp: response{err: boom}, fn: func([]string, []byte) *response {
			time.AfterFunc(20*time.Millisecond, cancel)
			return nil
		}}}}
		a := newTestAdapter(t, r)
		a.retryDelay = time.Hour
		if _, _, _, err := a.run(ctx, "x", nil, nil, nil); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a binary that is not installed gets no second try", func(t *testing.T) {
		missing := fmt.Errorf("x: %w", exec.ErrNotFound)
		r := &ruleRunner{rules: []rule{{match: "x", resp: response{err: missing}}}}
		a := newTestAdapter(t, r)
		a.retryDelay = time.Hour
		_, _, _, err := a.run(context.Background(), "x", []string{"one"}, nil, nil)
		if !errors.Is(err, exec.ErrNotFound) || !strings.Contains(err.Error(), "x one:") {
			t.Errorf("err = %v", err)
		}
		if n := len(r.snapshot()); n != 1 {
			t.Errorf("attempts = %d", n)
		}
	})
	t.Run("three strikes name the command", func(t *testing.T) {
		r := &ruleRunner{rules: []rule{{match: "x", resp: response{err: boom}}}}
		a := newTestAdapter(t, r)
		_, _, _, err := a.run(context.Background(), "x", []string{"one", "two", "three"}, nil, nil)
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "x one two:") {
			t.Errorf("err = %v", err)
		}
		if n := len(r.snapshot()); n != 3 {
			t.Errorf("attempts = %d", n)
		}
	})
}

func TestFolderWithoutRunsDirIsTheTempDir(t *testing.T) {
	a := New(Config{})
	if got, want := a.folderFor("w"), filepath.Join(os.TempDir(), "apothecary-devcontainer", "w"); got != want {
		t.Errorf("folder = %q", got)
	}
}

// Each stage of a docker provision that fails stops it there, and says so.
func TestProvisionFailuresStopAtTheirStage(t *testing.T) {
	cases := []struct {
		name    string
		rules   []rule
		want    string
		stopped string // a later call that must not happen
	}{
		{"clone exit", []rule{fail("clone --", 128, "repository not found")}, "clone https://git.test/r.git: exit 128: repository not found", "run -d"},
		{"clone spawn", []rule{spawnFail("clone --")}, "spawn failed", "run -d"},
		{"sandbox list exit", []rule{fail("ps -aq --filter label=apothecary.env=w", 1, "denied")}, "docker ps: exit 1", "run -d"},
		{"sandbox list spawn", []rule{spawnFail("ps -aq --filter label=apothecary.env=w")}, "spawn failed", "run -d"},
		{"volume exit", []rule{fail("volume create", 1, "no space")}, "docker volume create w-docker: exit 1: no space", "run -d"},
		{"volume spawn", []rule{spawnFail("volume create")}, "spawn failed", "run -d"},
		{"sandbox run exit", []rule{fail("run -d --name w", 125, "conflict")}, "sandbox run w: exit 125: conflict", "up --workspace-folder"},
		{"sandbox run spawn", []rule{spawnFail("run -d --name w")}, "spawn failed", "up --workspace-folder"},
		{"up spawn", []rule{spawnFail("up --workspace-folder")}, "spawn failed", "exec -u root"},
		{"root init exit", []rule{fail("exec -u root", 2, "read-only fs")}, "workspace init: exit 2: read-only fs", "shim relay"},
		{"root init spawn", []rule{spawnFail("exec -u root")}, "spawn failed", "shim relay"},
		{"no container", []rule{{match: "ps -q --filter label=apothecary.env", resp: response{stdout: "\n"}}}, "no running container", "shim relay"},
		{"container query spawn", []rule{spawnFail("ps -q --filter label=apothecary.env")}, "spawn failed", "shim relay"},
		{"relay exit", []rule{fail("/opt/apothecary/shim relay", 1, "busy")}, "start relay in w: exit 1: busy", "rename"},
		{"relay spawn", []rule{spawnFail("/opt/apothecary/shim relay")}, "spawn failed", "rename"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, r := armedAdapter(t, c.rules...)
			_, err := a.Provision(context.Background(), envport.EnvSpec{Name: "w", RepoURL: "https://git.test/r.git"})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
			if r.saw(c.stopped) {
				t.Errorf("provision went on to %q:\n%s", c.stopped, strings.Join(r.lines(), "\n"))
			}
		})
	}
}

func TestProvisionBuilderFailures(t *testing.T) {
	boom := errors.New("builder down")
	cases := []struct {
		name string
		arm  func(a *Adapter)
		want string
	}{
		{"no shim builder", func(a *Adapter) { a.cfg.ShimBin = nil }, "no shim builder configured"},
		{"shim builder fails", func(a *Adapter) {
			a.cfg.ShimBin = func(context.Context) (string, error) { return "", boom }
		}, "build shim binary"},
		{"shim binary missing", func(a *Adapter) {
			a.cfg.ShimBin = func(context.Context) (string, error) { return "/nonexistent/shim", nil }
		}, "no such file"},
		{"no tools builder", func(a *Adapter) { a.cfg.ToolsDir = nil }, "no tools builder configured"},
		{"tools builder fails", func(a *Adapter) {
			a.cfg.ToolsDir = func(context.Context) (string, error) { return "", boom }
		}, "build tools bundle"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, r := armedAdapter(t)
			c.arm(a)
			_, err := a.Provision(context.Background(), envport.EnvSpec{Name: "w", RepoURL: "https://git.test/r.git"})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v", err)
			}
			if r.saw("run -d") {
				t.Error("a sandbox came up for a workspace that cannot be equipped")
			}
		})
	}
}

// A workspace that never answers fails at the deadline; it does not hang the
// run or report ready.
func TestProvisionWaitReadyDeadline(t *testing.T) {
	a, r := armedAdapter(t, fail("/dev/tcp/", 1, ""))
	a.cfg.ProvisionTimeout = 150 * time.Millisecond
	_, err := a.Provision(context.Background(), envport.EnvSpec{Name: "w", RepoURL: "https://git.test/r.git"})
	if err == nil || !strings.Contains(err.Error(), "exec/relay not ready before deadline") {
		t.Fatalf("err = %v", err)
	}
	if r.saw("rename") {
		t.Error("a workspace that never answered got its final name")
	}
}

func TestProvisionBridgeFailure(t *testing.T) {
	a, r := armedAdapter(t)
	a.listen = func(string, string) (net.Listener, error) { return nil, errors.New("address in use") }
	_, err := a.Provision(context.Background(), envport.EnvSpec{Name: "w", RepoURL: "https://git.test/r.git"})
	if err == nil || !strings.Contains(err.Error(), "bridge socket engine.sock") {
		t.Fatalf("err = %v", err)
	}
	if r.saw("/opt/apothecary/shim relay") {
		t.Error("a relay started with no engine-side bridge behind it")
	}
}

// The credential lives in a 0600 file for exactly as long as the clone, never
// in argv.
func TestCloneCredentialIsEphemeral(t *testing.T) {
	a := newTestAdapter(t, &scriptRunner{})
	credPath := filepath.Join(a.folderFor("w"), "git-credentials")
	var mode fs.FileMode
	var content string
	r := &ruleRunner{rules: []rule{{match: "git ", fn: func(args []string, _ []byte) *response {
		info, err := os.Stat(credPath)
		if err != nil {
			t.Errorf("no credential file during the clone: %v", err)
			return nil
		}
		mode = info.Mode().Perm()
		b, _ := os.ReadFile(credPath)
		content = string(b)
		return nil
	}}}}
	a.runner = r
	if _, err := a.ensureSrc(context.Background(), "w", envport.EnvSpec{RepoURL: "https://git.test/r.git", GitUser: "u", GitPass: "s3cret/x"}); err != nil {
		t.Fatal(err)
	}
	if mode != 0o600 || content != "https://u:s3cret%2Fx@git.test\n" {
		t.Errorf("credential file mode=%v content=%q", mode, content)
	}
	mustNotExist(t, credPath)
	for _, l := range r.lines() {
		if strings.Contains(l, "s3cret") {
			t.Errorf("credential in argv: %s", l)
		}
	}
	if !r.saw("credential.helper=store --file=" + credPath) {
		t.Errorf("clone did not use the store: %v", r.lines())
	}
}

func TestEnsureSrcEdges(t *testing.T) {
	ctx := context.Background()
	t.Run("an existing checkout is kept", func(t *testing.T) {
		r := &ruleRunner{}
		a := newTestAdapter(t, r)
		if err := os.MkdirAll(filepath.Join(a.srcDir("w"), ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := a.ensureSrc(ctx, "w", envport.EnvSpec{RepoURL: "https://git.test/r.git"}); err != nil {
			t.Fatal(err)
		}
		if len(r.snapshot()) != 0 {
			t.Errorf("re-cloned over a checkout that may hold agent commits: %v", r.lines())
		}
	})
	t.Run("no password, no credential helper", func(t *testing.T) {
		r := &ruleRunner{}
		a := newTestAdapter(t, r)
		if _, err := a.ensureSrc(ctx, "w", envport.EnvSpec{RepoURL: "https://git.test/r.git"}); err != nil {
			t.Fatal(err)
		}
		if r.saw("credential.helper") {
			t.Errorf("calls = %v", r.lines())
		}
	})
	t.Run("an unusable repo URL never reaches git", func(t *testing.T) {
		r := &ruleRunner{}
		a := newTestAdapter(t, r)
		if _, err := a.ensureSrc(ctx, "w", envport.EnvSpec{RepoURL: "no-host", GitPass: "x"}); err == nil || !strings.Contains(err.Error(), "cannot parse the repository URL") {
			t.Errorf("err = %v", err)
		}
		if len(r.snapshot()) != 0 {
			t.Errorf("calls = %v", r.lines())
		}
	})
	t.Run("run folder cannot be made", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{})
		block := filepath.Join(a.cfg.RunsDir, "w")
		if err := os.WriteFile(block, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := a.ensureSrc(ctx, "w", envport.EnvSpec{}); err == nil {
			t.Error("a checkout under a file succeeded")
		}
	})
	t.Run("credential file cannot be written", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{})
		if err := os.MkdirAll(filepath.Join(a.folderFor("w"), "git-credentials"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := a.ensureSrc(ctx, "w", envport.EnvSpec{RepoURL: "https://git.test/r.git", GitPass: "x"}); err == nil {
			t.Error("a clone went ahead without its credential")
		}
	})
}

func TestPolicyFor(t *testing.T) {
	allow := policyFor(envport.EnvSpec{EgressDomains: []string{"a.io"}, EgressDenylist: []string{"never"}, EgressBlocked: []string{"b.io"},
		EgressAllowedCIDRs: []string{"1.2.3.0/24"}, EgressAllowedPorts: []int{5432}})
	if allow.Mode != policyfile.ModeAllow || allow.Domains[0] != "a.io" || allow.BlockedDomains[0] != "b.io" ||
		allow.AllowedCIDRs[0] != "1.2.3.0/24" || allow.AllowedPorts[0] != 5432 {
		t.Errorf("allow = %+v", allow)
	}
	deny := policyFor(envport.EnvSpec{EgressMode: "deny", EgressDomains: []string{"never"}, EgressDenylist: []string{"evil.io"}, EgressBlocked: []string{"b.io"},
		EgressAllowedCIDRs: []string{"1.2.3.0/24"}})
	if deny.Mode != policyfile.ModeDeny || deny.Domains[0] != "evil.io" || deny.BlockedDomains[0] != "b.io" || deny.AllowedCIDRs != nil {
		t.Errorf("deny = %+v", deny)
	}
	// Anything unrecognised is the secure posture.
	if got := policyFor(envport.EnvSpec{EgressMode: "open"}); got.Mode != policyfile.ModeAllow {
		t.Errorf("unknown mode = %+v", got)
	}
}

func TestGatewayMarkers(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{})
	if a.projectFor("w") != "" || a.gatewayIPFor("w") != "" {
		t.Error("markers read from nothing")
	}
	a.recordGateway("w", "P1", "10.0.0.2")
	if a.projectFor("w") != "P1" || a.gatewayIPFor("w") != "10.0.0.2" {
		t.Errorf("markers = %q, %q", a.projectFor("w"), a.gatewayIPFor("w"))
	}
	// A run re-provisioned without a gateway must not keep pointing at one.
	a.recordGateway("w", "P1", "")
	if a.gatewayIPFor("w") != "" || a.projectFor("w") != "P1" {
		t.Errorf("after off = %q, %q", a.projectFor("w"), a.gatewayIPFor("w"))
	}
}

func TestEnsureShimAliases(t *testing.T) {
	ctx := context.Background()
	a := newTestAdapter(t, &ruleRunner{})
	dir, err := a.ensureShim(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, shimAlias)
	if got, err := os.Readlink(alias); err != nil || got != "shim" {
		t.Fatalf("alias = %q, %v", got, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "shim")); string(b) != "#!fake-shim" {
		t.Errorf("shim = %q", b)
	}
	mustNotExist(t, filepath.Join(dir, "shim.tmp"))
	// Idempotent, and a wrong alias is put right.
	if _, err := a.ensureShim(ctx, "w"); err != nil {
		t.Fatal(err)
	}
	os.Remove(alias)
	os.Symlink("elsewhere", alias)
	if _, err := a.ensureShim(ctx, "w"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(alias); got != "shim" {
		t.Errorf("alias not repaired: %q", got)
	}
	// A workspace's shim dir under a file.
	block := newTestAdapter(t, &ruleRunner{})
	os.WriteFile(filepath.Join(block.cfg.RunsDir, "w"), nil, 0o644)
	if _, err := block.ensureShim(ctx, "w"); err == nil {
		t.Error("a shim dir under a file succeeded")
	}
	// The temp file's path taken by a directory.
	held := newTestAdapter(t, &ruleRunner{})
	os.MkdirAll(filepath.Join(held.folderFor("w"), "shim", "shim.tmp"), 0o755)
	if _, err := held.ensureShim(ctx, "w"); err == nil {
		t.Error("write over a directory succeeded")
	}
	// The final name taken by a directory.
	held2 := newTestAdapter(t, &ruleRunner{})
	os.MkdirAll(filepath.Join(held2.folderFor("w"), "shim", "shim", "x"), 0o755)
	if _, err := held2.ensureShim(ctx, "w"); err == nil {
		t.Error("rename over a directory succeeded")
	}
}

func TestRootInitDefaultsToRoot(t *testing.T) {
	r := &ruleRunner{}
	a := newTestAdapter(t, r)
	if err := a.rootInit(context.Background(), "w", Workspace{ContainerID: "c", WorkspaceFolder: "/workspaces/it's"}); err != nil {
		t.Fatal(err)
	}
	script := r.snapshot()[0].args[len(r.snapshot()[0].args)-1]
	if !strings.Contains(script, "chown 'root' /apothecary") {
		t.Errorf("script = %s", script)
	}
	if !strings.Contains(script, `ln -sfn '/workspaces/it'\''s' "$H/repo"`) {
		t.Errorf("a repo-derived folder must stay quoted: %s", script)
	}
}

func TestRenameIsBestEffort(t *testing.T) {
	r := &ruleRunner{rules: []rule{fail("rename", 1, "name in use")}}
	a := newTestAdapter(t, r)
	a.renameContainer(context.Background(), "", "w")
	if len(r.snapshot()) != 0 {
		t.Error("renamed nothing")
	}
	a.renameContainer(context.Background(), "cid", "w") // must not panic or fail the provision
	if !r.saw("rename cid w") {
		t.Errorf("calls = %v", r.lines())
	}
	// A rejected rename does not fail a provision.
	a2, r2 := armedAdapter(t, fail("rename", 1, "already named"))
	if _, err := a2.Provision(context.Background(), envport.EnvSpec{Name: "w", RepoURL: "https://git.test/r.git"}); err != nil {
		t.Errorf("provision = %v", err)
	}
	if !r2.saw("rename cid1 w") {
		t.Error("rename never tried")
	}
}

type otherDef struct{ devcontainerDef }

func (otherDef) Name() string { return "other" }

// An env keeps the definition that built it, across a restart.
func TestDefForReadsTheRecord(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{})
	a.defs = append(a.defs, otherDef{})
	os.MkdirAll(a.folderFor("w"), 0o755)
	os.WriteFile(filepath.Join(a.folderFor("w"), definitionFile), []byte("other\n"), 0o644)
	if got := a.defFor("w").Name(); got != "other" {
		t.Errorf("defFor = %q", got)
	}
	// An unknown record falls back to the first definition.
	os.MkdirAll(a.folderFor("x"), 0o755)
	os.WriteFile(filepath.Join(a.folderFor("x"), definitionFile), []byte("gone\n"), 0o644)
	if got := a.defFor("x").Name(); got != "devcontainer" {
		t.Errorf("unknown record = %q", got)
	}
	// Remembered: the file is not consulted again.
	os.Remove(filepath.Join(a.folderFor("w"), definitionFile))
	if got := a.defFor("w").Name(); got != "other" {
		t.Errorf("remembered = %q", got)
	}
}

func TestExecFailures(t *testing.T) {
	t.Run("spawn failure", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{rules: []rule{spawnFail("exec --id-label")}})
		if _, err := a.Exec(context.Background(), "w", envport.Command{Script: "true"}); err == nil || !strings.Contains(err.Error(), "spawn failed") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("timeout reads as the context's error", func(t *testing.T) {
		r := &ruleRunner{rules: []rule{{match: "exec --id-label", resp: response{err: errors.New("killed")}, fn: func([]string, []byte) *response {
			time.Sleep(30 * time.Millisecond) // the command outlives its timeout
			return nil
		}}}}
		a := newTestAdapter(t, r)
		a.retryDelay = time.Hour
		_, err := a.Exec(context.Background(), "w", envport.Command{Script: "true", Timeout: 5 * time.Millisecond})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("dir is entered or the script exits", func(t *testing.T) {
		r := &ruleRunner{}
		a := newTestAdapter(t, r)
		if _, err := a.Exec(context.Background(), "w", envport.Command{Script: "make", Dir: "repo/sub"}); err != nil {
			t.Fatal(err)
		}
		stdin := string(r.snapshot()[0].stdin)
		if !strings.Contains(stdin, `cd "$HOME/repo/sub" || exit 90`) || !strings.HasPrefix(stdin, shimPathPrelude) {
			t.Errorf("script = %q", stdin)
		}
	})
}

func TestFileOpsFailures(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		call func(a *Adapter) error
		resp response
		want string
	}{
		{"read spawn", func(a *Adapter) error { _, err := a.ReadFile(ctx, "w", "f"); return err }, response{err: errors.New("x")}, "x"},
		{"read escape", func(a *Adapter) error { _, err := a.ReadFile(ctx, "w", "f"); return err }, response{code: escapeExit}, "symlink escape"},
		{"read other", func(a *Adapter) error { _, err := a.ReadFile(ctx, "w", "f"); return err }, response{code: 2, stderr: "denied"}, "read f: exit 2: denied"},
		{"read garbage", func(a *Adapter) error { _, err := a.ReadFile(ctx, "w", "f"); return err }, response{stdout: "%%%"}, "decode"},
		{"list spawn", func(a *Adapter) error { _, err := a.ListFiles(ctx, "w", "d"); return err }, response{err: errors.New("x")}, "x"},
		{"list escape", func(a *Adapter) error { _, err := a.ListFiles(ctx, "w", "d"); return err }, response{code: escapeExit}, "symlink escape"},
		{"list other", func(a *Adapter) error { _, err := a.ListFiles(ctx, "w", "d"); return err }, response{code: 2, stderr: "denied"}, "list d: exit 2: denied"},
		{"write spawn", func(a *Adapter) error { return a.WriteFile(ctx, "w", "f", nil, 0o644) }, response{err: errors.New("x")}, "x"},
		{"write escape", func(a *Adapter) error { return a.WriteFile(ctx, "w", "f", nil, 0o644) }, response{code: escapeExit}, "symlink escape"},
		{"write other", func(a *Adapter) error { return a.WriteFile(ctx, "w", "f", nil, 0o644) }, response{code: 1, stderr: "full"}, "write f: exit 1: full"},
		{"tree spawn", func(a *Adapter) error {
			return a.WriteTree(ctx, "w", "/x", []envport.TreeFile{{Path: "a", Data: []byte("1")}})
		}, response{err: errors.New("x")}, "x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newTestAdapter(t, &ruleRunner{rules: []rule{{match: "bash", resp: c.resp}}})
			if err := c.call(a); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// An escape is refused by the guard script that ships with the call, and an
// absolute engine path carries none.
func TestContainmentGuardIsInTheScript(t *testing.T) {
	r := &ruleRunner{rules: []rule{{match: "bash", resp: response{stdout: "eA==\n"}}}}
	a := newTestAdapter(t, r)
	ctx := context.Background()
	a.ReadFile(ctx, "w", "artifacts/plan.md")
	a.ReadFile(ctx, "w", "/apothecary/run.env")
	a.WriteFile(ctx, "w", "../evil", []byte("x"), 0o600)
	calls := r.snapshot()
	if !strings.Contains(string(calls[0].stdin), "readlink -f") {
		t.Errorf("relative read unguarded: %s", calls[0].stdin)
	}
	if strings.Contains(string(calls[1].stdin), "readlink -f") {
		t.Errorf("absolute read guarded: %s", calls[1].stdin)
	}
	if !strings.Contains(string(calls[2].stdin), "readlink -f") || !strings.Contains(string(calls[2].stdin), "chmod 600") {
		t.Errorf("write = %s", calls[2].stdin)
	}
}

func TestListFilesMissingDir(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{rules: []rule{{match: "bash", resp: response{code: 44}}}})
	if _, err := a.ListFiles(context.Background(), "w", "d/"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v", err)
	}
}

func TestStatusInnerFailures(t *testing.T) {
	ctx := context.Background()
	running := rule{match: "apothecary.sandbox=true", resp: response{stdout: "running\n"}}
	a := newTestAdapter(t, &ruleRunner{rules: []rule{running, fail("ps -a --filter label=apothecary.env=w --format", 1, "daemon down")}})
	if st, err := a.Status(ctx, "w"); err == nil || !strings.Contains(err.Error(), "daemon down") || st != "" {
		t.Errorf("exit = %q, %v (must stay unknown, not absent)", st, err)
	}
	a = newTestAdapter(t, &ruleRunner{rules: []rule{running, spawnFail("ps -a --filter label=apothecary.env=w --format")}})
	if _, err := a.Status(ctx, "w"); err == nil {
		t.Error("spawn failure read as a state")
	}
	a = newTestAdapter(t, &ruleRunner{rules: []rule{spawnFail("apothecary.sandbox=true")}})
	if _, err := a.Status(ctx, "w"); err == nil {
		t.Error("host failure read as a state")
	}
	a = newTestAdapter(t, &ruleRunner{rules: []rule{fail("apothecary.sandbox=true", 1, "")}})
	if _, err := a.Status(ctx, "w"); err == nil {
		t.Error("host exit read as a state")
	}
}

func TestDestroyFailuresKeepTheFolder(t *testing.T) {
	sandbox := rule{match: "ps -aq --filter label=apothecary.env=w --filter label=apothecary.sandbox=true", resp: response{stdout: "sbx1\n"}}
	cases := []struct {
		name  string
		rules []rule
		want  string
	}{
		{"listing fails", []rule{spawnFail("apothecary.sandbox=true")}, "spawn failed"},
		{"listing exits", []rule{fail("apothecary.sandbox=true", 1, "x")}, "docker ps: exit 1"},
		{"rm spawn", []rule{sandbox, spawnFail("rm -f sbx1")}, "spawn failed"},
		{"rm exit", []rule{sandbox, fail("rm -f sbx1", 1, "busy")}, "docker rm w sandbox: exit 1: busy"},
		{"volume spawn", []rule{sandbox, spawnFail("volume rm")}, "spawn failed"},
		{"volume exit", []rule{sandbox, fail("volume rm", 1, "in use")}, "docker volume rm w-docker: exit 1: in use"},
		{"legacy list spawn", []rule{spawnFail("ps -aq --filter label=apothecary.env=w")}, "spawn failed"},
		{"legacy list exit", []rule{{match: "ps -aq --filter label=apothecary.env=w --filter", resp: response{}}, fail("ps -aq --filter label=apothecary.env=w", 1, "x")}, "docker ps: exit 1"},
		{"legacy rm spawn", []rule{{match: "ps -aq --filter label=apothecary.env=w --filter", resp: response{}},
			{match: "ps -aq --filter label=apothecary.env=w", resp: response{stdout: "wid\n"}}, spawnFail("rm -f wid")}, "spawn failed"},
		{"legacy rm exit", []rule{{match: "ps -aq --filter label=apothecary.env=w --filter", resp: response{}},
			{match: "ps -aq --filter label=apothecary.env=w", resp: response{stdout: "wid\n"}}, fail("rm -f wid", 1, "busy")}, "docker rm w: exit 1: busy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newTestAdapter(t, &ruleRunner{rules: c.rules})
			os.MkdirAll(a.srcDir("w"), 0o755)
			err := a.Destroy(context.Background(), "w")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v", err)
			}
			mustExist(t, a.folderFor("w")) // a failed destroy can be retried
		})
	}
}

func TestReattach(t *testing.T) {
	ctx := context.Background()
	running := []rule{
		{match: "apothecary.sandbox=true --format", resp: response{stdout: "running\n"}},
		{match: "--format {{.State}}", resp: response{stdout: "running\n"}},
		{match: "ps -q --filter label=apothecary.env", resp: response{stdout: "cid1\n"}},
	}
	t.Run("rebuilds what the old engine took with it", func(t *testing.T) {
		r := &ruleRunner{rules: running}
		a := newTestAdapter(t, r)
		defer a.Close()
		if err := a.Reattach(ctx, "w"); err != nil {
			t.Fatal(err)
		}
		mustExist(t, filepath.Join(a.folderFor("w"), "shim", "shim"))
		if _, ok := a.bridges["w"]; !ok {
			t.Error("no bridge")
		}
		relay, ready := r.index("/opt/apothecary/shim relay"), r.index("/dev/tcp/")
		if relay < 0 || ready < relay {
			t.Errorf("relay %d, probe %d: %v", relay, ready, r.lines())
		}
	})
	t.Run("idempotent over a live bridge", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{rules: running})
		defer a.Close()
		for range 2 {
			if err := a.Reattach(ctx, "w"); err != nil {
				t.Fatal(err)
			}
		}
		if n := len(a.bridges["w"]); n != 1 {
			t.Errorf("listeners = %d", n)
		}
	})
	t.Run("only a running workspace", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{rules: []rule{{match: "apothecary.sandbox=true --format", resp: response{stdout: "exited\n"}}}})
		if err := a.Reattach(ctx, "w"); err == nil || !strings.Contains(err.Error(), "is stopped, not running") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("unknown status is not running", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{rules: []rule{spawnFail("apothecary.sandbox=true")}})
		if err := a.Reattach(ctx, "w"); err == nil {
			t.Error("reattached without knowing the state")
		}
	})
	t.Run("shim refresh failure", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{rules: running})
		a.cfg.ShimBin = nil
		if err := a.Reattach(ctx, "w"); err == nil || !strings.Contains(err.Error(), "no shim builder") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("bridge failure", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{rules: running})
		a.listen = func(string, string) (net.Listener, error) { return nil, errors.New("busy") }
		if err := a.Reattach(ctx, "w"); err == nil {
			t.Error("reattached with no bridge")
		}
	})
	t.Run("relay failure", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{rules: append([]rule{fail("/opt/apothecary/shim relay", 1, "x")}, running...)})
		defer a.Close()
		if err := a.Reattach(ctx, "w"); err == nil {
			t.Error("reattached with no relay")
		}
	})
}

func TestLogsWithoutARunFolder(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{})
	os.WriteFile(filepath.Join(a.cfg.RunsDir, "w"), nil, 0o644) // a file where the folder goes
	if f := a.openProvisionLog("w"); f != nil {
		t.Error("provision log opened under a file")
	}
	if f := a.openExecLog("w", envport.Command{}); f != nil {
		t.Error("exec log opened under a file")
	}
	// Taken by a directory: the folder exists, the log cannot.
	os.MkdirAll(filepath.Join(a.folderFor("x"), "exec.log"), 0o755)
	if f := a.openProvisionLog("x"); f != nil {
		t.Error("provision log opened over a directory")
	}
	if f := a.openExecLog("x", envport.Command{}); f != nil {
		t.Error("exec log opened over a directory")
	}
	if f := New(Config{}).openExecLog("w", envport.Command{}); f != nil {
		t.Error("exec log with no runs dir")
	}
}

func TestTailExecLog(t *testing.T) {
	ctx := context.Background()
	if _, err := New(Config{}).TailExecLog(ctx, "w", 10); err == nil || !strings.Contains(err.Error(), "runs dir is not configured") {
		t.Errorf("no runs dir = %v", err)
	}
	a := newTestAdapter(t, &ruleRunner{})
	if _, err := a.TailExecLog(ctx, "w", 10); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("no log = %v", err)
	}
	os.MkdirAll(a.folderFor("w"), 0o755)
	os.WriteFile(filepath.Join(a.folderFor("w"), "exec.log"), []byte("0123456789"), 0o644)
	cases := map[int]string{4: "6789", 10: "0123456789", 99: "0123456789", 0: "0123456789", -1: "0123456789"}
	for n, want := range cases {
		if got, err := a.TailExecLog(ctx, "w", n); err != nil || string(got) != want {
			t.Errorf("tail %d = %q, %v", n, got, err)
		}
	}
	// A directory at the log's path: opens, cannot be read.
	os.MkdirAll(filepath.Join(a.folderFor("d"), "exec.log"), 0o755)
	if _, err := a.TailExecLog(ctx, "d", 10); err == nil {
		t.Error("read a directory as a log")
	}
}

func TestDockerProbe(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		rules []rule
		want  string
	}{
		{"cli exits nonzero", []rule{fail("devcontainer --version", 1, "")}, "devcontainer CLI not runnable"},
		{"docker missing", []rule{spawnFail("docker version")}, "docker CLI not runnable"},
		{"daemon down", []rule{fail("docker version", 1, "Cannot connect")}, "docker daemon unreachable: Cannot connect"},
	}
	for _, c := range cases {
		a := newTestAdapter(t, &ruleRunner{rules: c.rules})
		if err := a.Test(ctx); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	a := newTestAdapter(t, &ruleRunner{})
	if err := a.Test(ctx); err != nil {
		t.Errorf("healthy = %v", err)
	}
	// An opted-in runtime is checked by the probe, once it answers.
	r := &ruleRunner{rules: []rule{{match: "info --format", resp: response{stdout: `{"sysbox-runc":{}}`}}}}
	a = newTestAdapter(t, r)
	a.cfg.SandboxRuntime = "sysbox-runc"
	for range 2 {
		if err := a.Test(ctx); err != nil {
			t.Errorf("with runtime = %v", err)
		}
	}
	n := 0
	for _, l := range r.lines() {
		if strings.Contains(l, "info --format") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("runtime checked %d times, success is cached", n)
	}
}

func TestCheckRuntimeFailuresStayRetryable(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		r    rule
		want string
	}{
		{"spawn", spawnFail("info --format"), "docker info"},
		{"exit", fail("info --format", 1, "down"), "exit 1: down"},
		{"garbage", rule{match: "info --format", resp: response{stdout: "nope"}}, "parsing docker info runtimes"},
		{"absent", rule{match: "info --format", resp: response{stdout: `{"runc":{},"crun":{}}`}}, "(it has: crun, runc)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &ruleRunner{rules: []rule{c.r}}
			a := newTestAdapter(t, r)
			a.cfg.SandboxRuntime = "sysbox-runc"
			if err := a.ensureRuntime(ctx); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v", err)
			}
			if a.runtimeOK {
				t.Error("a failure was cached")
			}
			r.mu.Lock()
			r.rules = []rule{{match: "info --format", resp: response{stdout: `{"sysbox-runc":{}}`}}}
			r.mu.Unlock()
			if err := a.ensureRuntime(ctx); err != nil {
				t.Errorf("after recovery = %v", err)
			}
		})
	}
}

func TestSandboxGuardsSocketLength(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{})
	a.noSandboxGuard = false
	a.cfg.RunsDir = "/var/lib/" + strings.Repeat("x", 100)
	err := a.ensureSandbox(context.Background(), "w", envport.EnvSpec{}, attachmentNone(), "/t")
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("err = %v", err)
	}
}

func TestEnsureSandboxExistingPaths(t *testing.T) {
	ctx := context.Background()
	existing := rule{match: "ps -aq --filter label=apothecary.env=w --filter label=apothecary.sandbox=true", resp: response{stdout: "sbx1\n"}}
	t.Run("shape query failure", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{rules: []rule{fail("apothecary.sandbox.shape", 1, "x"), existing}})
		if err := a.ensureSandbox(ctx, "w", envport.EnvSpec{}, attachmentNone(), "/t"); err == nil {
			t.Error("recreated a sandbox without knowing its shape")
		}
	})
	t.Run("old sandbox cannot be removed", func(t *testing.T) {
		for _, bad := range []rule{fail("rm -f sbx1", 1, "busy"), spawnFail("rm -f sbx1")} {
			a := newTestAdapter(t, &ruleRunner{rules: []rule{{match: "apothecary.sandbox.shape", resp: response{}}, existing, bad}})
			if err := a.ensureSandbox(ctx, "w", envport.EnvSpec{}, attachmentNone(), "/t"); err == nil {
				t.Error("old-shape sandbox left in place silently")
			}
		}
	})
	t.Run("start spawn failure", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{rules: []rule{existing, spawnFail("start w")}})
		armSandbox(t, a, "w")
		if err := a.ensureSandbox(ctx, "w", envport.EnvSpec{}, attachmentNone(), "/t"); err == nil {
			t.Error("start failure swallowed")
		}
	})
}

// The inner daemon never answering ends with the caller's context, not a hang.
func TestWaitSandboxReadyEndsWithItsContext(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{rules: []rule{fail("version --format", 1, "")}})
	armSandbox(t, a, "w")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := a.waitSandboxReady(ctx, "w"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	// Probes against the run's own socket, never the host daemon.
	r := a.runner.(*ruleRunner)
	c := r.snapshot()[0]
	if !envHas(c, "DOCKER_HOST=unix://"+a.sockPath("w")) {
		t.Errorf("probe env = %v", c.env)
	}
}

func TestSandboxIDsAndStateMarkers(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{rules: []rule{{match: "apothecary.sandbox=true --format", resp: response{stdout: "running\nexited\n"}}}})
	if st, err := a.sandboxState(context.Background(), "w"); err != nil || st != "running" {
		t.Errorf("state = %q, %v", st, err)
	}
}

func TestLegacySandboxStateNeedsNoSocket(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{})
	h := dockerHost{a}
	if st, legacy, err := h.sandboxState(context.Background(), "w"); err != nil || st != "" || !legacy {
		t.Errorf("no socket = %q %v %v", st, legacy, err)
	}
	armSandbox(t, a, "w")
	if st, legacy, _ := h.sandboxState(context.Background(), "w"); st != "" || legacy {
		t.Errorf("socket, no sandbox = %q %v (a DinD env whose sandbox is gone is absent, not legacy)", st, legacy)
	}
}

func TestDockerSampleFailures(t *testing.T) {
	ctx := context.Background()
	a := newTestAdapter(t, &ruleRunner{rules: []rule{spawnFail("stats --no-stream")}})
	if _, err := a.SampleResources(ctx, "w"); err == nil {
		t.Error("spawn failure read as a sample")
	}
	a = newTestAdapter(t, &ruleRunner{rules: []rule{{match: "stats --no-stream", resp: response{stdout: "not json\n"}}}})
	if _, err := a.SampleResources(ctx, "w"); err == nil || !strings.Contains(err.Error(), "unparseable row") {
		t.Errorf("garbage = %v", err)
	}
}

func TestHostCPUsCachesOnlySuccess(t *testing.T) {
	ctx := context.Background()
	var answer atomic.Value
	answer.Store(response{code: 1})
	r := &ruleRunner{rules: []rule{{match: "info --format {{.NCPU}}", fn: func([]string, []byte) *response {
		v := answer.Load().(response)
		return &v
	}}}}
	a := newTestAdapter(t, r)
	if n := a.hostCPUs(ctx); n != 0 {
		t.Errorf("failed = %d", n)
	}
	answer.Store(response{stdout: "nope\n"})
	if n := a.hostCPUs(ctx); n != 0 {
		t.Errorf("garbage = %d", n)
	}
	answer.Store(response{stdout: "0\n"})
	if n := a.hostCPUs(ctx); n != 0 {
		t.Errorf("zero = %d", n)
	}
	answer.Store(response{stdout: "8\n"})
	if n := a.hostCPUs(ctx); n != 8 {
		t.Errorf("answered = %d", n)
	}
	answer.Store(response{code: 1})
	if n := a.hostCPUs(ctx); n != 8 {
		t.Errorf("cached = %d", n)
	}
}

func TestParseSizeEdges(t *testing.T) {
	for _, s := range []string{"", "MB", "1.2.3MB", "10XB"} {
		if _, err := parseSize(s); err == nil {
			t.Errorf("parseSize(%q) accepted", s)
		}
	}
	if used, limit := parseSizePair("1.5MiB / nonsense"); used != 1572864 || limit != 0 {
		t.Errorf("pair = %d, %d", used, limit)
	}
}

func TestEngineHostPort(t *testing.T) {
	cases := map[string][2]string{
		"http://10.0.0.1:9000":  {"10.0.0.1", "9000"},
		"http://engine.ns":      {"engine.ns", "80"},
		"https://engine.ns":     {"engine.ns", "443"},
		"http://[fd00::1]:8080": {"fd00::1", "8080"},
		"":                      {"127.0.0.1", "8080"},
		"http://bad host":       {"127.0.0.1", "8080"},
	}
	for in, want := range cases {
		if h, p := engineHostPort(in); h != want[0] || p != want[1] {
			t.Errorf("%q = %s, %s", in, h, p)
		}
	}
}

func TestOSFS(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var f osFS
	sub := filepath.Join(dir, "a", "b")
	if err := f.MkdirAll(ctx, sub); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(sub, "f")
	if err := f.WriteFile(ctx, p, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if b, err := f.ReadFile(ctx, p); err != nil || string(b) != "x" {
		t.Errorf("read = %q, %v", b, err)
	}
	if info, _ := os.Stat(p); info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", info.Mode())
	}
	link := filepath.Join(dir, "link")
	os.Symlink(p, link)
	if !f.Exists(ctx, p) || f.Exists(ctx, p+"x") {
		t.Error("Exists is wrong")
	}
	if !f.IsRegular(ctx, p) || f.IsRegular(ctx, link) || f.IsRegular(ctx, sub) {
		t.Error("IsRegular must not follow a final symlink")
	}
	files, err := f.Walk(ctx, dir)
	if err != nil || len(files) != 1 || files[0] != p {
		t.Errorf("walk = %v, %v (a symlink is not a regular file)", files, err)
	}
	if files, err := f.Walk(ctx, filepath.Join(dir, "absent")); err != nil || files != nil {
		t.Errorf("missing dir = %v, %v", files, err)
	}
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	locked := filepath.Join(dir, "locked")
	os.Mkdir(locked, 0o000)
	defer os.Chmod(locked, 0o755)
	if _, err := f.Walk(ctx, dir); err == nil {
		t.Error("an unreadable directory reads as empty")
	}
}
