package fake

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strings"
	"testing"

	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/egressport"
	"apothecary/internal/ports/envport"
)

func provisioned(t *testing.T) (*EnvProvisioner, envport.EnvID) {
	t.Helper()
	p := NewEnvProvisioner()
	id, err := p.Provision(context.Background(), envport.EnvSpec{RunID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	return p, id
}

func TestSSHNeedsItsHook(t *testing.T) {
	ctx := context.Background()
	p, id := provisioned(t)
	if _, err := p.SSH(ctx, id); err == nil {
		t.Error("ssh served with no hook")
	}
	boom := errors.New("refused")
	var dialed envport.EnvID
	p.SSHDial = func(env envport.EnvID) (net.Conn, error) { dialed = env; return nil, boom }
	conn, err := p.SSH(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Dial(ctx); !errors.Is(err, boom) || dialed != id {
		t.Errorf("dial = %v, env %q", err, dialed)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
}

func TestConfigDirtyFollowsItsSeed(t *testing.T) {
	ctx := context.Background()
	p, id := provisioned(t)
	if dirty, err := p.ConfigDirty(ctx, id); dirty || err != nil {
		t.Errorf("clean = %v, %v", dirty, err)
	}
	p.Dirty = map[envport.EnvID]bool{id: true}
	if dirty, _ := p.ConfigDirty(ctx, id); !dirty {
		t.Error("seeded dirty read clean")
	}
}

func TestUnknownEnvFailsEveryOperation(t *testing.T) {
	ctx := context.Background()
	p := NewEnvProvisioner()
	ops := map[string]func() error{
		"exec":  func() error { _, err := p.Exec(ctx, "ghost", envport.Command{}); return err },
		"read":  func() error { _, err := p.ReadFile(ctx, "ghost", "f"); return err },
		"list":  func() error { _, err := p.ListFiles(ctx, "ghost", "d"); return err },
		"write": func() error { return p.WriteFile(ctx, "ghost", "f", nil, 0) },
		"tail":  func() error { _, err := p.TailExecLog(ctx, "ghost", 0); return err },
		"tree":  func() error { return p.WriteTree(ctx, "ghost", "/d", nil) },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s = %v", name, err)
		}
	}
	if st, err := p.Status(ctx, "ghost"); err != nil || st != envport.EnvAbsent {
		t.Errorf("status = %s, %v", st, err)
	}
}

func TestExecScriptingAndLog(t *testing.T) {
	ctx := context.Background()
	p, id := provisioned(t)
	boom := errors.New("exec failed")
	p.ExecFunc = func(e *Env, cmd envport.Command) (envport.ExecResult, error) {
		switch cmd.Script {
		case "fail":
			return envport.ExecResult{Output: "not logged"}, boom
		case "seen":
			e.Files["touched"] = []byte("x") // the func may use the env's files
		}
		return envport.ExecResult{ExitCode: 3, Output: "out-" + cmd.Script}, nil
	}
	res, err := p.Exec(ctx, id, envport.Command{Script: "seen", Dir: "repo"})
	if err != nil || res.ExitCode != 3 || res.Output != "out-seen" {
		t.Fatalf("exec = %+v, %v", res, err)
	}
	if p.Env(id).Files["touched"] == nil {
		t.Error("ExecFunc's write lost")
	}
	if _, err := p.Exec(ctx, id, envport.Command{Script: "fail"}); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	log, err := p.TailExecLog(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--- [dir=repo]\nseen\n", "out-seen\n", "fail\n"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log missing %q: %q", want, log)
		}
	}
	if strings.Contains(string(log), "not logged") {
		t.Errorf("a failed exec's output was logged: %q", log)
	}
	tail, _ := p.TailExecLog(ctx, id, 5)
	if len(tail) != 5 || !strings.HasSuffix(string(log), string(tail)) {
		t.Errorf("tail = %q", tail)
	}
	if got := p.Execs(); len(got) != 2 || got[1].Cmd.Script != "fail" {
		t.Errorf("execs = %+v", got)
	}
}

// A remove really removes: a test about a scrub can assert the file is gone.
func TestRemoveScripts(t *testing.T) {
	ctx := context.Background()
	p, id := provisioned(t)
	seed := func() {
		for _, f := range []string{"a", "it's", "dir/x", "dir/sub/y", "dirty", "keep"} {
			p.WriteFile(ctx, id, f, []byte("1"), 0o644)
		}
	}
	has := func(f string) bool { _, ok := p.Env(id).Files[f]; return ok }
	seed()
	if _, err := p.Exec(ctx, id, envport.Command{Script: `rm -f a 'it'\''s' && echo done`}); err != nil {
		t.Fatal(err)
	}
	if has("a") || has("it's") || !has("keep") {
		t.Errorf("files = %v", p.Env(id).Files)
	}
	// -f takes the file only; -rf the directory's too, not its prefix siblings.
	if _, err := p.Exec(ctx, id, envport.Command{Script: "rm -f dir"}); err != nil {
		t.Fatal(err)
	}
	if !has("dir/x") {
		t.Error("rm -f removed a directory's files")
	}
	if _, err := p.Exec(ctx, id, envport.Command{Script: "rm -rf dir/"}); err != nil {
		t.Fatal(err)
	}
	if has("dir/x") || has("dir/sub/y") || !has("dirty") {
		t.Errorf("files = %v", p.Env(id).Files)
	}
	// Removes never reach the scripted exec.
	called := false
	p.ExecFunc = func(*Env, envport.Command) (envport.ExecResult, error) {
		called = true
		return envport.ExecResult{}, nil
	}
	p.Exec(ctx, id, envport.Command{Script: "rm -f keep"})
	if called {
		t.Error("a remove reached ExecFunc")
	}
	for _, script := range []string{"rm -f", "rm a b", "echo rm -f a", ""} {
		if _, _, ok := parseRemove(script); ok {
			t.Errorf("parseRemove(%q) matched", script)
		}
	}
}

func TestListFiles(t *testing.T) {
	ctx := context.Background()
	p, id := provisioned(t)
	if _, err := p.ListFiles(ctx, id, "d"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("empty dir = %v", err)
	}
	p.WriteFile(ctx, id, "d/b", []byte("22"), 0)
	p.WriteFile(ctx, id, "d/a", []byte("1"), 0)
	p.WriteFile(ctx, id, "dx/c", []byte("3"), 0)
	got, err := p.ListFiles(ctx, id, "d/")
	if err != nil || len(got) != 2 || got[0] != (envport.FileEntry{Path: "d/a", Size: 1}) || got[1].Path != "d/b" {
		t.Errorf("list = %+v, %v", got, err)
	}
	for i := range envport.ListFilesLimit + 5 {
		p.WriteFile(ctx, id, fmt.Sprintf("big/%05d", i), nil, 0)
	}
	if got, _ := p.ListFiles(ctx, id, "big"); len(got) != envport.ListFilesLimit {
		t.Errorf("big = %d", len(got))
	}
}

func TestFilesAreCopiedInAndOut(t *testing.T) {
	ctx := context.Background()
	p, id := provisioned(t)
	in := []byte("abc")
	p.WriteFile(ctx, id, "f", in, 0)
	in[0] = 'X'
	out, _ := p.ReadFile(ctx, id, "f")
	if string(out) != "abc" {
		t.Errorf("stored = %q", out)
	}
	out[0] = 'Y'
	if again, _ := p.ReadFile(ctx, id, "f"); string(again) != "abc" {
		t.Errorf("read shares storage: %q", again)
	}
}

func TestWriteTreeReplacesTheDirectory(t *testing.T) {
	ctx := context.Background()
	p, id := provisioned(t)
	p.WriteFile(ctx, id, "/s/old", []byte("x"), 0)
	p.WriteFile(ctx, id, "/other/keep", []byte("x"), 0)
	files := []envport.TreeFile{{Path: "a/b", Data: []byte("1"), Mode: 0o755}, {Path: "c", Data: []byte("2")}}
	if err := p.WriteTree(ctx, id, "/s/", files); err != nil {
		t.Fatal(err)
	}
	e := p.Env(id)
	if _, ok := e.Files["/s/old"]; ok {
		t.Error("the old tree survived")
	}
	if string(e.Files["/s/a/b"]) != "1" || e.Modes["/s/a/b"] != 0o755 || e.Modes["/s/c"] != 0o644 || e.Files["/other/keep"] == nil {
		t.Errorf("files = %v, modes = %v", e.Files, e.Modes)
	}
	if len(p.Trees) != 1 || p.Trees[0] != (TreeCall{Env: id, Dir: "/s/", Count: 2}) {
		t.Errorf("trees = %+v", p.Trees)
	}
	if err := p.WriteTree(ctx, id, "rel", nil); err == nil {
		t.Error("a relative dir accepted")
	}
	for _, bad := range []string{"", "/abs", "a/../b", "a//b", ".", "..", `a\b`, "nul\x00"} {
		if err := p.WriteTree(ctx, id, "/s", []envport.TreeFile{{Path: bad}}); err == nil {
			t.Errorf("path %q accepted", bad)
		}
	}
	if string(e.Files["/s/c"]) != "2" {
		t.Error("a refused tree touched the directory")
	}
}

func TestEgressFolding(t *testing.T) {
	ctx := context.Background()
	p, id := provisioned(t)
	p.Denied = map[envport.EnvID][]egressport.EgressDenial{id: {{Host: "evil.io", Count: 2}}}
	if d := p.EgressDenials(ctx, id); len(d) != 1 || d[0].Host != "evil.io" {
		t.Errorf("denials = %+v", d)
	}
	p.AllowEgress(ctx, id, "a.io")
	p.AllowEgress(ctx, id, "b.io")
	p.SetEgressRule(ctx, id, "a.io", false) // drop from the allowlist, block
	p.SetEgressRule(ctx, id, "a.io", false) // no double block
	p.SetEgressRule(ctx, id, "c.io", true)
	p.SetEgressRule(ctx, id, "b.io", true) // already allowed: once
	pol, err := p.EgressPolicy(ctx, id)
	if err != nil || pol.Mode != "allow" || strings.Join(pol.Domains, ",") != "b.io,c.io" || strings.Join(pol.BlockedDomains, ",") != "a.io" {
		t.Errorf("policy = %+v, %v", pol, err)
	}
	p.SetEgressRule(ctx, id, "a.io", true) // unblock
	pol, _ = p.EgressPolicy(ctx, id)
	if len(pol.BlockedDomains) != 0 || !containsStr(pol.Domains, "a.io") {
		t.Errorf("after unblock = %+v", pol)
	}
}

func TestAgentRunnerRecords(t *testing.T) {
	r := &AgentRunner{}
	specs := []*agentspec.Spec{{Name: "a"}}
	r.RecordPrepare(specs)
	r.RecordInstallSkills([]string{"pdf"})
	if got := r.SnapshotPreparedSpecs(); len(got) != 1 || got[0][0].Name != "a" {
		t.Errorf("prepared = %+v", got)
	}
	if len(r.InstalledSkills) != 1 || r.InstalledSkills[0][0] != "pdf" {
		t.Errorf("skills = %+v", r.InstalledSkills)
	}
	r.Run(context.Background(), "e", agentport.AgentSpec{Agent: "x"}, agentport.AgentInput{})
	if got := r.SnapshotRunCalls(); len(got) != 1 || got[0].Spec.Agent != "x" {
		t.Errorf("runs = %+v", got)
	}
}

func TestAgentRunnerKeepsScriptedSessions(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("harness down")
	r := &AgentRunner{RunFunc: func(_ context.Context, c RunCall) (agentport.AgentResult, error) {
		if c.Spec.Agent == "boom" {
			return agentport.AgentResult{}, boom
		}
		return agentport.AgentResult{Session: "mine"}, nil
	}}
	if res, _ := r.Run(ctx, "e", agentport.AgentSpec{}, agentport.AgentInput{}); res.Session != "mine" {
		t.Errorf("session = %q", res.Session)
	}
	if res, err := r.Run(ctx, "e", agentport.AgentSpec{Agent: "boom"}, agentport.AgentInput{}); !errors.Is(err, boom) || res.Session != "" {
		t.Errorf("failure = %+v, %v", res, err)
	}
}

func TestResumeScripting(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("gone")
	t.Run("plain resume", func(t *testing.T) {
		r := &AgentRunner{}
		res, err := r.Resume(ctx, "e", "s1", agentport.AgentInput{Prompt: "reply"})
		if err != nil || res.Session != "s1" || res.TurnsUsed != 1 {
			t.Errorf("res = %+v, %v", res, err)
		}
		if r.ResumeCalls[0].Reply != "reply" || len(r.RunCalls) != 0 {
			t.Errorf("calls = %+v / %+v", r.ResumeCalls, r.RunCalls)
		}
	})
	t.Run("scripted resume", func(t *testing.T) {
		r := &AgentRunner{ResumeFunc: func(_ context.Context, c ResumeCall) (agentport.AgentResult, error) {
			if c.Reply == "boom" {
				return agentport.AgentResult{}, boom
			}
			return agentport.AgentResult{Output: c.Reply}, nil
		}}
		if res, _ := r.Resume(ctx, "e", "s1", agentport.AgentInput{Prompt: "x"}); res.Session != "s1" || res.Output != "x" {
			t.Errorf("res = %+v", res)
		}
		if _, err := r.Resume(ctx, "e", "s1", agentport.AgentInput{Prompt: "boom"}); !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a new attempt counts as a run", func(t *testing.T) {
		spec := agentport.AgentSpec{Agent: "planner"}
		r := &AgentRunner{}
		res, err := r.Resume(ctx, "e", "s1", agentport.AgentInput{Attempt: &spec})
		if err != nil || res.Session != "s1" || len(r.RunCalls) != 1 || r.RunCalls[0].Spec.Agent != "planner" {
			t.Errorf("unscripted = %+v, %v, %+v", res, err, r.RunCalls)
		}
		r = &AgentRunner{RunFunc: func(_ context.Context, c RunCall) (agentport.AgentResult, error) {
			if c.Spec.Agent == "boom" {
				return agentport.AgentResult{}, boom
			}
			return agentport.AgentResult{Session: "ignored", Output: "ran"}, nil
		}}
		if res, _ := r.Resume(ctx, "e", "s1", agentport.AgentInput{Attempt: &spec}); res.Session != "s1" || res.Output != "ran" {
			t.Errorf("scripted = %+v (the session is the resumed one)", res)
		}
		bad := agentport.AgentSpec{Agent: "boom"}
		if _, err := r.Resume(ctx, "e", "s1", agentport.AgentInput{Attempt: &bad}); !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
	})
}
