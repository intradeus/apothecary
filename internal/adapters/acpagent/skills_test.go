package acpagent

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports"
)

func TestInstallSkillsIsOneCopyingExec(t *testing.T) {
	envp := fake.NewEnvProvisioner()
	id, _ := envp.Provision(context.Background(), ports.EnvSpec{RunID: "r"})
	if err := InstallSkills(context.Background(), envp, id, "/home/u/.agents/skills", []string{"pdf", "code-review"}); err != nil {
		t.Fatal(err)
	}
	calls := envp.Execs()
	if len(calls) != 1 {
		t.Fatalf("execs = %d, want 1", len(calls))
	}
	want := "mkdir -p '/home/u/.agents/skills'" +
		" && rm -rf '/home/u/.agents/skills/pdf' && cp -R '/apothecary/skills/pdf' '/home/u/.agents/skills/pdf'" +
		" && rm -rf '/home/u/.agents/skills/code-review' && cp -R '/apothecary/skills/code-review' '/home/u/.agents/skills/code-review'"
	if calls[0].Cmd.Script != want {
		t.Errorf("script =\n%s\nwant\n%s", calls[0].Cmd.Script, want)
	}
}

func TestInstallSkillsGuards(t *testing.T) {
	envp := fake.NewEnvProvisioner()
	id, _ := envp.Provision(context.Background(), ports.EnvSpec{RunID: "r"})
	if err := InstallSkills(context.Background(), envp, id, "/x", nil); err != nil || len(envp.Execs()) != 0 {
		t.Errorf("zero names must be a silent no-op: %v / %d execs", err, len(envp.Execs()))
	}
	if err := InstallSkills(context.Background(), envp, id, "/x", []string{"../etc"}); err == nil || len(envp.Execs()) != 0 {
		t.Error("a non-slug name must be refused before any exec")
	}
	if err := InstallSkills(context.Background(), envp, id, "relative", []string{"a"}); err == nil {
		t.Error("a relative dir must be refused")
	}
	envp.ExecFunc = func(*fake.Env, ports.Command) (ports.ExecResult, error) {
		return ports.ExecResult{ExitCode: 1, Output: "cp: cannot stat"}, nil
	}
	if err := InstallSkills(context.Background(), envp, id, "/x", []string{"a"}); err == nil || !strings.Contains(err.Error(), "cp: cannot stat") {
		t.Errorf("exit code not reported: %v", err)
	}
}
