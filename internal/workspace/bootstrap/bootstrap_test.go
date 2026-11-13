package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
	"apothecary/internal/skills"
)

type fixture struct {
	envp *fake.EnvProvisioner
	id   envport.EnvID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	envp := fake.NewEnvProvisioner()
	id, err := envp.Provision(context.Background(), envport.EnvSpec{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{envp: envp, id: id}
}

func (f *fixture) file(t *testing.T, path string) string {
	t.Helper()
	b, err := f.envp.ReadFile(context.Background(), f.id, path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *fixture) scripts() []string {
	var out []string
	for _, c := range f.envp.Execs() {
		out = append(out, c.Cmd.Script)
	}
	return out
}

func baseSpec() BootstrapSpec {
	return BootstrapSpec{
		RunID: "r1", Token: "tok", EngineURL: "http://engine",
		RepoURL: "https://git.example/acme/app.git", SourceBranch: "main", Branch: "feat/x",
		AgentsMDPath: "AGENTS.md",
	}
}

// failOn makes the step whose script contains substr fail.
func failOn(f *fixture, substr string, res envport.ExecResult, err error) {
	f.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, substr) {
			return res, err
		}
		return envport.ExecResult{}, nil
	}
}

func TestRunBootstrapOrderAndFiles(t *testing.T) {
	f := newFixture(t)
	spec := baseSpec()
	spec.GitUser, spec.GitPass = "x-access-token", "gh pass/word"
	spec.ProviderEnv = map[string]string{"Z_KEY": "plain", "A_KEY": "with $dollar 'quote'"}
	spec.GitName, spec.GitEmail = "Ada", "ada@example.com"
	if err := RunBootstrap(context.Background(), f.envp, f.id, spec); err != nil {
		t.Fatal(err)
	}

	if got, want := f.file(t, "/apothecary/run.env"),
		"APOTHECARY_RUN_ID=r1\nAPOTHECARY_RUN_TOKEN=tok\nAPOTHECARY_ENGINE_URL=http://engine\n"; got != want {
		t.Errorf("run.env = %q", got)
	}
	// Sorted, and quoted so the file survives being sourced.
	if got, want := f.file(t, "/apothecary/provider.env"),
		`A_KEY='with $dollar '\''quote'\'''`+"\nZ_KEY='plain'\n"; got != want {
		t.Errorf("provider.env = %q, want %q", got, want)
	}
	// The token lives in the credential store, URL-escaped, and never in a script.
	if got, want := f.file(t, "/apothecary/git-credentials"),
		"https://x-access-token:gh+pass%2Fword@git.example\n"; got != want {
		t.Errorf("git-credentials = %q, want %q", got, want)
	}
	scripts := f.scripts()
	for _, s := range scripts {
		if strings.Contains(s, "gh pass") {
			t.Errorf("a script carries the token: %s", s)
		}
	}

	want := []string{"credential.helper", "git clone", "git switch", "mkdir -p artifacts", "git config user.name"}
	if len(scripts) < len(want) {
		t.Fatalf("scripts = %q", scripts)
	}
	for i, w := range want {
		if !strings.Contains(scripts[i], w) {
			t.Errorf("step %d = %q, want it to contain %q", i, scripts[i], w)
		}
	}
	// An existing clone is kept: it may hold agent commits.
	if !strings.Contains(scripts[1], "[ -e repo/.git ] ||") {
		t.Errorf("clone is not idempotent: %s", scripts[1])
	}
	calls := f.envp.Execs()
	if calls[2].Cmd.Dir != "repo" || calls[1].Cmd.Dir != "" {
		t.Errorf("step dirs: clone %q, branch %q", calls[1].Cmd.Dir, calls[2].Cmd.Dir)
	}
}

func TestRunBootstrapPublicRepoWritesNoCredentials(t *testing.T) {
	f := newFixture(t)
	if err := RunBootstrap(context.Background(), f.envp, f.id, baseSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.envp.ReadFile(context.Background(), f.id, "/apothecary/git-credentials"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("credentials file: %v", err)
	}
	if s := f.scripts(); strings.Contains(strings.Join(s, "\n"), "credential.helper") {
		t.Errorf("a helper was configured for a public repo: %q", s)
	}
}

func TestRunBootstrapExtras(t *testing.T) {
	f := newFixture(t)
	spec := baseSpec()
	spec.GitPass = "p"
	spec.Extras = []ExtraCheckout{
		{Name: "lib", Dir: "repos/lib", URL: "https://git.example/acme/lib.git", Branch: "feat/x", Source: "develop"},
		{Name: "other", Dir: "repos/other", URL: "https://other.example/o.git", Branch: "feat/x", Source: "main", Fetch: "refs/merge-requests/4/head"},
	}
	if err := RunBootstrap(context.Background(), f.envp, f.id, spec); err != nil {
		t.Fatal(err)
	}
	// One entry per distinct host.
	creds := f.file(t, "/apothecary/git-credentials")
	if got := strings.Count(creds, "\n"); got != 2 {
		t.Errorf("credentials = %q, want one line per host", creds)
	}
	if !strings.Contains(creds, "@other.example") || !strings.Contains(creds, "oauth2:p@git.example") {
		t.Errorf("credentials = %q", creds)
	}
	all := strings.Join(f.scripts(), "\n")
	for _, want := range []string{
		"mkdir -p 'repos' && { [ -e 'repos/lib'/.git ] || git clone \"https://git.example/acme/lib.git\" 'repos/lib'; }",
		`git switch -c 'feat/x' 'origin/develop'`,
		"git fetch origin 'refs/merge-requests/4/head' && git switch -c 'feat/x' FETCH_HEAD",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("scripts lack %q:\n%s", want, all)
		}
	}
	var dirs []string
	for _, c := range f.envp.Execs() {
		dirs = append(dirs, c.Cmd.Dir)
	}
	if !strings.Contains(strings.Join(dirs, ","), "repos/other") {
		t.Errorf("no step ran inside the extra checkout: %v", dirs)
	}
}

func TestRunBootstrapHooksRunInOrder(t *testing.T) {
	f := newFixture(t)
	var order []string
	specs := []*agentspec.Spec{{}}
	spec := baseSpec()
	spec.Agents = specs
	spec.Skills = []skills.Tree{{Name: "one"}, {Name: "two"}}
	spec.Prepare = func(_ context.Context, id envport.EnvID, got []*agentspec.Spec) error {
		if id != f.id || len(got) != 1 {
			t.Errorf("Prepare(%s, %d specs)", id, len(got))
		}
		order = append(order, "prepare")
		return nil
	}
	spec.InstallSkills = func(_ context.Context, _ envport.EnvID, names []string) error {
		if strings.Join(names, ",") != "one,two" {
			t.Errorf("InstallSkills(%v)", names)
		}
		order = append(order, "install")
		return nil
	}
	if err := RunBootstrap(context.Background(), f.envp, f.id, spec); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "prepare,install" {
		t.Fatalf("order = %v", order)
	}
}

func TestRunBootstrapFailures(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name    string
		mutate  func(*BootstrapSpec)
		fail    func(*fixture)
		wantErr []string
	}{
		{"clone exits non-zero", nil, func(f *fixture) {
			failOn(f, "git clone", envport.ExecResult{ExitCode: 128, Output: "fatal: repository not found"}, nil)
		}, []string{"bootstrap clone", "exit 128", "repository not found"}},
		{"exec itself fails", nil, func(f *fixture) {
			failOn(f, "git switch", envport.ExecResult{}, boom)
		}, []string{"bootstrap branch", "boom"}},
		{"git config", nil, func(f *fixture) {
			failOn(f, "git config user.name", envport.ExecResult{ExitCode: 1, Output: "no"}, nil)
		}, []string{"bootstrap git-config"}},
		{"seed auth", func(s *BootstrapSpec) {
			s.SeedAuth = func(context.Context, envport.EnvID, func(string)) error { return boom }
		}, nil, []string{"seed harness auth", "boom"}},
		{"unparseable repo url", func(s *BootstrapSpec) {
			s.GitPass, s.RepoURL = "p", "::not a url"
		}, nil, []string{"git credentials"}},
		{"prepare", func(s *BootstrapSpec) {
			s.Prepare = func(context.Context, envport.EnvID, []*agentspec.Spec) error { return boom }
		}, nil, []string{"bootstrap harness-config", "boom"}},
		{"install skills", func(s *BootstrapSpec) {
			s.Skills = []skills.Tree{{Name: "one"}}
			s.InstallSkills = func(context.Context, envport.EnvID, []string) error { return boom }
		}, nil, []string{"bootstrap skills", "boom"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			spec := baseSpec()
			if c.mutate != nil {
				c.mutate(&spec)
			}
			if c.fail != nil {
				c.fail(f)
			}
			err := RunBootstrap(context.Background(), f.envp, f.id, spec)
			if c.wantErr == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("RunBootstrap succeeded")
			}
			for _, want := range c.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want %q in it", err, want)
				}
			}
		})
	}
}

// A bootstrap stops at its first failure: nothing after it runs.
func TestRunBootstrapStopsAtTheFirstFailure(t *testing.T) {
	f := newFixture(t)
	failOn(f, "git clone", envport.ExecResult{ExitCode: 1}, nil)
	spec := baseSpec()
	spec.Prepare = func(context.Context, envport.EnvID, []*agentspec.Spec) error {
		t.Error("Prepare ran after a failed clone")
		return nil
	}
	if err := RunBootstrap(context.Background(), f.envp, f.id, spec); err == nil {
		t.Fatal("RunBootstrap succeeded")
	}
	if got := len(f.envp.Execs()); got != 1 {
		t.Fatalf("ran %d steps after the failed one", got-1)
	}
}

// failingEnvp fails the file writes it is told to.
type failingEnvp struct {
	*fake.EnvProvisioner
	writeFail string
}

func (e failingEnvp) WriteFile(ctx context.Context, id envport.EnvID, path string, data []byte, mode fs.FileMode) error {
	if strings.HasSuffix(path, e.writeFail) {
		return errors.New("disk full")
	}
	return e.EnvProvisioner.WriteFile(ctx, id, path, data, mode)
}

func TestWriteFailuresNameTheFile(t *testing.T) {
	for file, want := range map[string]string{
		"run.env":         "write run.env",
		"provider.env":    "write provider.env",
		"git-credentials": "write git-credentials",
	} {
		t.Run(file, func(t *testing.T) {
			f := newFixture(t)
			spec := baseSpec()
			spec.GitPass = "p"
			err := RunBootstrap(context.Background(), failingEnvp{f.envp, file}, f.id, spec)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// Secrets register with the redactor before they are written.
func TestWriteWorkspaceSecretsRegisters(t *testing.T) {
	f := newFixture(t)
	var registered []string
	spec := baseSpec()
	spec.Register = func(s string) { registered = append(registered, s) }
	spec.SeedAuth = func(_ context.Context, id envport.EnvID, register func(string)) error {
		register("oauth-token")
		return f.envp.WriteFile(context.Background(), id, "/root/.auth", []byte("oauth-token"), 0o600)
	}
	if err := WriteWorkspaceSecrets(context.Background(), f.envp, f.id, spec); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 1 || registered[0] != "oauth-token" {
		t.Fatalf("registered = %v", registered)
	}
	// No redactor given: seeding still has a register to call.
	spec.Register = nil
	if err := WriteWorkspaceSecrets(context.Background(), f.envp, f.id, spec); err != nil {
		t.Fatal(err)
	}
}

func TestWriteSkills(t *testing.T) {
	ctx := context.Background()
	t.Run("always starts from an empty directory", func(t *testing.T) {
		f := newFixture(t)
		stale := skills.WorkspaceDir + "/old/SKILL.md"
		if err := f.envp.WriteFile(ctx, f.id, stale, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeSkills(ctx, f.envp, f.id, BootstrapSpec{}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.envp.ReadFile(ctx, f.id, stale); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("a previous selection survived: %v", err)
		}
		if _, err := f.envp.ReadFile(ctx, f.id, skills.WorkspaceDir+"/manifest.json"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("no skills, no manifest: %v", err)
		}
	})
	t.Run("lays down trees and a manifest", func(t *testing.T) {
		f := newFixture(t)
		spec := BootstrapSpec{
			Skills:    []skills.Tree{{Name: "one", Files: []envport.TreeFile{{Path: "SKILL.md", Data: []byte("x")}}}},
			SkillPins: []event.SkillsetPin{{}},
		}
		if err := writeSkills(ctx, f.envp, f.id, spec); err != nil {
			t.Fatal(err)
		}
		if len(f.envp.Trees) != 1 || f.envp.Trees[0].Dir != skills.WorkspaceDir+"/one" {
			t.Fatalf("trees = %+v", f.envp.Trees)
		}
		var m skillsManifest
		if err := json.Unmarshal([]byte(f.file(t, skills.WorkspaceDir+"/manifest.json")), &m); err != nil {
			t.Fatal(err)
		}
		if len(m.Skillsets) != 1 || len(m.Skills) != 1 || m.Skills[0] != "one" {
			t.Fatalf("manifest = %+v", m)
		}
	})
	t.Run("no pins is an empty list, not null", func(t *testing.T) {
		f := newFixture(t)
		if err := writeSkills(ctx, f.envp, f.id, BootstrapSpec{Skills: []skills.Tree{{Name: "one"}}}); err != nil {
			t.Fatal(err)
		}
		if got := f.file(t, skills.WorkspaceDir+"/manifest.json"); !strings.Contains(got, `"skillsets": []`) {
			t.Fatalf("manifest = %s", got)
		}
	})
	t.Run("a refused reset fails", func(t *testing.T) {
		f := newFixture(t)
		bad := resetFailEnvp{f.envp, envport.ExecResult{ExitCode: 1, Output: "read-only file system"}, nil}
		err := writeSkills(ctx, bad, f.id, BootstrapSpec{})
		if err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("err = %v", err)
		}
		bad.err = errors.New("gone")
		if err := writeSkills(ctx, bad, f.id, BootstrapSpec{}); err == nil {
			t.Fatal("an exec failure must surface")
		}
	})
	t.Run("a tree that cannot be written names the skill", func(t *testing.T) {
		f := newFixture(t)
		spec := BootstrapSpec{Skills: []skills.Tree{{Name: "evil", Files: []envport.TreeFile{{Path: "../escape"}}}}}
		err := writeSkills(ctx, f.envp, f.id, spec)
		if err == nil || !strings.Contains(err.Error(), "skill evil") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an unwritable manifest fails", func(t *testing.T) {
		f := newFixture(t)
		err := writeSkills(ctx, failingEnvp{f.envp, "manifest.json"}, f.id, BootstrapSpec{Skills: []skills.Tree{{Name: "one"}}})
		if err == nil || !strings.Contains(err.Error(), "manifest") {
			t.Fatalf("err = %v", err)
		}
	})
}

// resetFailEnvp answers every Exec itself, past the fake's handling of rm.
type resetFailEnvp struct {
	*fake.EnvProvisioner
	res envport.ExecResult
	err error
}

func (e resetFailEnvp) Exec(context.Context, envport.EnvID, envport.Command) (envport.ExecResult, error) {
	return e.res, e.err
}

func TestSourceBranchOr(t *testing.T) {
	if got := SourceBranchOr("dev", "main"); got != "dev" {
		t.Errorf("got %q", got)
	}
	if got := SourceBranchOr("", "main"); got != "main" {
		t.Errorf("got %q", got)
	}
}

func TestAgentConfigScript(t *testing.T) {
	plain := agentConfigScript("AGENTS.md")
	for _, want := range []string{"mkdir -p artifacts .apothecary/attachments", "grep -qx artifacts/ .git/info/exclude", "grep -qx .apothecary/ .git/info/exclude"} {
		if !strings.Contains(plain, want) {
			t.Errorf("script lacks %q: %s", want, plain)
		}
	}
	if strings.Contains(plain, "ln -sf") {
		t.Errorf("AGENTS.md needs no symlink onto itself: %s", plain)
	}
	linked := agentConfigScript("docs/AI.md")
	if !strings.Contains(linked, `ln -sf "docs/AI.md" AGENTS.md`) || !strings.Contains(linked, "echo AGENTS.md >> .git/info/exclude") {
		t.Errorf("script = %s", linked)
	}
	none := agentConfigScript("")
	if strings.Contains(none, "ln -sf") || !strings.Contains(none, "mkdir -p artifacts .apothecary/attachments") ||
		!strings.Contains(none, "grep -qx artifacts/ .git/info/exclude") || !strings.Contains(none, "grep -qx .apothecary/ .git/info/exclude") {
		t.Errorf("no agent guide links nothing and keeps the dirs: %s", none)
	}
}

func TestGitConfigScript(t *testing.T) {
	if got, want := gitConfigScript("", ""), "git config user.name 'Apothecary' && git config user.email 'apothecary@local'"; got != want {
		t.Errorf("defaults = %q", got)
	}
	if got := gitConfigScript("O'Brien", "o@x"); !strings.Contains(got, `'O'\''Brien'`) || !strings.Contains(got, "'o@x'") {
		t.Errorf("quoting = %q", got)
	}
	if got := gitConfigScript("Ada", ""); !strings.Contains(got, "'Ada'") || !strings.Contains(got, "apothecary@local") {
		t.Errorf("one default = %q", got)
	}
}

func TestEncodeEnvFile(t *testing.T) {
	if got, want := encodeEnvFile(map[string]string{"B": "2 3", "A": "$1"}), "A='$1'\nB='2 3'\n"; got != want {
		t.Errorf("encodeEnvFile = %q, want %q", got, want)
	}
	if got := encodeEnvFile(nil); got != "" {
		t.Errorf("encodeEnvFile(nil) = %q", got)
	}
}
