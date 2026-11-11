package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/egressgw/policyfile"
	"apothecary/internal/ports/envport"
)

// The adapter's host-blind paths — what it does around and inside the
// workspace whichever host the sandbox runs on — driven through testHost.

func TestNewFillsDefaults(t *testing.T) {
	a := New(Config{})
	c := a.cfg
	if c.DevcontainerBin != "devcontainer" || c.DockerBin != "docker" || c.ProvisionTimeout != 15*time.Minute || c.EnginePort != 8080 {
		t.Errorf("defaults = %+v", c)
	}
	// No cluster configured: no host and no gateway (only tests build one).
	if a.host != nil || a.gw != nil {
		t.Errorf("host = %T, gw = %v", a.host, a.gw)
	}
	kept := New(Config{DevcontainerBin: "dc", DockerBin: "dk", ProvisionTimeout: time.Second, EnginePort: 9})
	if kept.cfg.DevcontainerBin != "dc" || kept.cfg.DockerBin != "dk" || kept.cfg.ProvisionTimeout != time.Second || kept.cfg.EnginePort != 9 {
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

// Each stage of a provision that fails stops it there, says so, and hands
// back the name of what it may have left.
func TestProvisionFailuresStopAtTheirStage(t *testing.T) {
	cases := []struct {
		name    string
		rules   []rule
		arm     func(h *testHost)
		want    string
		stopped string // a later call that must not happen
	}{
		{name: "up spawn", rules: []rule{spawnFail("up --workspace-folder")}, want: "spawn failed", stopped: "exec -u root"},
		{name: "owner lookup exit", rules: []rule{fail("ps -aq --filter id=", 1, "denied")}, want: "look up container cid1: exit 1: denied", stopped: "exec -u root"},
		{name: "owner lookup spawn", rules: []rule{spawnFail("ps -aq --filter id=")}, want: "spawn failed", stopped: "exec -u root"},
		{name: "root init exit", rules: []rule{fail("exec -u root", 2, "read-only fs")}, want: "workspace init: exit 2: read-only fs", stopped: "run-user-commands"},
		{name: "root init spawn", rules: []rule{spawnFail("exec -u root")}, want: "spawn failed", stopped: "run-user-commands"},
		{name: "hooks exit", rules: []rule{fail("run-user-commands", 1, "npm ERR!")}, want: "devcontainer lifecycle hooks w: exit 1", stopped: "ps -q --filter label=apothecary.env"},
		{name: "bridge", arm: func(h *testHost) { h.bridgeErr = errors.New("address in use") }, want: "address in use", stopped: "ps -q --filter label=apothecary.env"},
		{name: "no container", rules: []rule{{match: "ps -q --filter label=apothecary.env", resp: response{stdout: "\n"}}}, want: "no running container", stopped: "portscan"},
		{name: "container query spawn", rules: []rule{spawnFail("ps -q --filter label=apothecary.env")}, want: "spawn failed", stopped: "portscan"},
		{name: "relay", arm: func(h *testHost) { h.relayErr = errors.New("relay busy") }, want: "relay busy", stopped: "/dev/tcp/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, r := armedAdapter(t, c.rules...)
			if c.arm != nil {
				c.arm(hostOf(t, a))
			}
			id, err := a.Provision(context.Background(), envport.EnvSpec{Name: "w", RepoURL: "https://git.test/r.git"})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
			if id != "w" {
				t.Errorf("failed provision returned %q, want the workspace name", id)
			}
			if r.saw(c.stopped) {
				t.Errorf("provision went on to %q:\n%s", c.stopped, strings.Join(r.lines(), "\n"))
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
	// root already owns the engine's checkout and may write the shim dir:
	// nothing to hand over.
	if strings.Contains(script, "chown -R") || strings.Contains(script, "chgrp") {
		t.Errorf("no ownership change for root: %s", script)
	}
}

// A non-root remote user gets the checkout (the engine cloned it as root),
// quoted like the folder, before anything else refers to it.
func TestRootInitOwnsTheCheckoutForANonRootUser(t *testing.T) {
	r := &ruleRunner{}
	a := newTestAdapter(t, r)
	if err := a.rootInit(context.Background(), "w", Workspace{ContainerID: "c", RemoteUser: "dev", WorkspaceFolder: "/workspaces/it's"}); err != nil {
		t.Fatal(err)
	}
	script := r.snapshot()[0].args[len(r.snapshot()[0].args)-1]
	own := `chown -R 'dev': '/workspaces/it'\''s'`
	if !strings.Contains(script, own) {
		t.Errorf("script = %s", script)
	}
	if strings.Index(script, own) > strings.Index(script, `"$H/repo"`) {
		t.Errorf("ownership must precede the repo link: %s", script)
	}
	// The sidecar (run as this user) creates its sockets in the shim dir:
	// the user's group may write it, the owner stays, nothing recursive.
	for _, want := range []string{`chgrp "$(id -g 'dev')" /opt/apothecary` + "\n", "chmod g+w /opt/apothecary\n"} {
		if !strings.Contains(script, want) {
			t.Errorf("shim dir not writable for the user (%q): %s", want, script)
		}
	}
	if strings.Contains(script, "chown 'dev' /opt/apothecary") || strings.Contains(script, "-R /opt/apothecary") {
		t.Errorf("the shim dir keeps its owner, non-recursively: %s", script)
	}
}

// Without a workspace folder there is no checkout to hand over, but the
// sidecar still needs the shim dir.
func TestRootInitShimDirWithoutAFolder(t *testing.T) {
	r := &ruleRunner{}
	a := newTestAdapter(t, r)
	if err := a.rootInit(context.Background(), "w", Workspace{ContainerID: "c", RemoteUser: "node"}); err != nil {
		t.Fatal(err)
	}
	script := r.snapshot()[0].args[len(r.snapshot()[0].args)-1]
	if !strings.Contains(script, `chgrp "$(id -g 'node')" /opt/apothecary`) || strings.Contains(script, "chown -R") {
		t.Errorf("script = %s", script)
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
	// A directory at the log's path is not a log.
	os.MkdirAll(filepath.Join(a.folderFor("d"), "exec.log"), 0o755)
	if _, err := a.TailExecLog(ctx, "d", 10); err == nil {
		t.Error("read a directory as a log")
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

func TestStatusInnerFailures(t *testing.T) {
	ctx := context.Background()
	a := newTestAdapter(t, &ruleRunner{rules: []rule{fail("ps -a --filter label=apothecary.env=w --format", 1, "daemon down")}})
	armSandbox(t, a, "w")
	if st, err := a.Status(ctx, "w"); err == nil || !strings.Contains(err.Error(), "daemon down") || st != "" {
		t.Errorf("exit = %q, %v (must stay unknown, not absent)", st, err)
	}
	a = newTestAdapter(t, &ruleRunner{rules: []rule{spawnFail("ps -a --filter label=apothecary.env=w --format")}})
	armSandbox(t, a, "w")
	if _, err := a.Status(ctx, "w"); err == nil {
		t.Error("spawn failure read as a state")
	}
}

// A destroy the host could not finish keeps the run folder (and the egress
// route), so it can be retried.
func TestDestroyFailureKeepsTheFolder(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{})
	g := withGateway(t, a)
	h := hostOf(t, a)
	h.destroyErr = errors.New("sandbox busy")
	os.MkdirAll(a.srcDir("w"), 0o755)
	if err := a.Destroy(context.Background(), "w"); !errors.Is(err, h.destroyErr) {
		t.Fatalf("err = %v", err)
	}
	mustExist(t, a.folderFor("w"))
	if len(g.detached) != 0 {
		t.Errorf("egress dropped for a run whose sandbox is still there: %v", g.detached)
	}
}

// foreignUp answers `devcontainer up` with a container the CLI did not create
// (cid0, as when the repository's initializeCommand started the service)
// unless --remove-existing-container is passed, or always when stubborn.
func foreignUp(stubborn bool) []rule {
	res := func(cid string) *response {
		return &response{stdout: `{"outcome":"success","containerId":"` + cid + `","remoteUser":"vscode","remoteWorkspaceFolder":"/workspaces/w"}` + "\n"}
	}
	return []rule{
		{match: "up --workspace-folder", fn: func(args []string, _ []byte) *response {
			if !stubborn && slices.Contains(args, "--remove-existing-container") {
				return res("cid1")
			}
			return res("cid0")
		}},
		{match: "ps -aq --filter id=cid0", resp: response{stdout: "\n"}}, // not ours
	}
}

func upCalls(r *ruleRunner) []string {
	var ups []string
	for _, l := range r.lines() {
		if strings.Contains(l, "up --workspace-folder") {
			ups = append(ups, l)
		}
	}
	return ups
}

// A compose project whose initializeCommand starts the devcontainer's own
// service hands the CLI a container it then reuses without our mounts and
// labels. One more `up` with --remove-existing-container replaces it, and
// the rest of the provision runs on the container the CLI created.
func TestProvisionReplacesAContainerTheCLIDidNotCreate(t *testing.T) {
	a, r := armedAdapter(t, foreignUp(false)...)
	if _, err := a.Provision(context.Background(), envport.EnvSpec{Name: "w", RepoURL: "https://git.test/r.git"}); err != nil {
		t.Fatalf("provision: %v\n%s", err, strings.Join(r.lines(), "\n"))
	}
	ups := upCalls(r)
	if len(ups) != 2 || strings.Contains(ups[0], "--remove-existing-container") || !strings.Contains(ups[1], "--remove-existing-container") {
		t.Fatalf("ups = %q, want a plain one, then one replacing the container", ups)
	}
	if r.saw("exec -u root cid0") || !r.saw("exec -u root cid1") {
		t.Errorf("root init must run in the CLI's own container:\n%s", strings.Join(r.lines(), "\n"))
	}
	logged, _ := os.ReadFile(filepath.Join(a.folderFor("w"), "exec.log"))
	if !strings.Contains(string(logged), "reused a container it did not create") {
		t.Errorf("exec.log does not say why the container was replaced:\n%s", logged)
	}
}

// When even the replacing `up` hands back a foreign container, the provision
// fails with what happened, before anything runs in it.
func TestProvisionRefusesAContainerTheCLIDidNotCreate(t *testing.T) {
	a, r := armedAdapter(t, foreignUp(true)...)
	id, err := a.Provision(context.Background(), envport.EnvSpec{Name: "w", RepoURL: "https://git.test/r.git"})
	if err == nil || !strings.Contains(err.Error(), "reused container cid0, which it did not create") || !strings.Contains(err.Error(), "initializeCommand") {
		t.Fatalf("err = %v", err)
	}
	if id != "w" {
		t.Errorf("id = %q, want the workspace name", id)
	}
	if n := len(upCalls(r)); n != 2 {
		t.Errorf("%d ups, want 2", n)
	}
	if r.saw("exec -u root") {
		t.Error("root init ran in a foreign container")
	}
}

// A recreate already passes --remove-existing-container: a foreign container
// after it is an error at once, not a second recreate.
func TestRecreateDoesNotRetryAForeignContainer(t *testing.T) {
	a, r := armedAdapter(t, foreignUp(true)...)
	_, err := a.provision(context.Background(), envport.EnvSpec{Name: "w", RepoURL: "https://git.test/r.git"}, true, nil)
	if err == nil || !strings.Contains(err.Error(), "did not create") {
		t.Fatalf("err = %v", err)
	}
	if ups := upCalls(r); len(ups) != 1 || !strings.Contains(ups[0], "--remove-existing-container") {
		t.Errorf("ups = %q", ups)
	}
}
