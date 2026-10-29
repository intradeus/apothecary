package acpagent

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"apothecary/internal/ports"
	"apothecary/internal/skills"
)

// SkillsDir is where the engine lays a run's skills down, one directory
// per skill (bootstrap writes it before any harness is prepared). Each
// harness then installs them from here into the directory IT reads.
const SkillsDir = skills.WorkspaceDir

// InstallSkills copies SkillsDir/<name> into dir for every name, replacing
// what was there, in one exec. A copy rather than a symlink: not every
// harness follows a symlinked skill directory (omp's discovery is
// undocumented there; a readdir that asks isDirectory() of a symlink gets
// "no"), and skills are kilobytes. Zero names is a no-op: the harness's
// directory is left as the image had it.
func InstallSkills(ctx context.Context, envp ports.EnvProvisioner, env ports.EnvID, dir string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	if !strings.HasPrefix(dir, "/") {
		return fmt.Errorf("install skills: directory %q must be absolute", dir)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "mkdir -p %s", shellQuote(dir))
	for _, n := range names {
		if err := skills.ValidName(n); err != nil {
			return fmt.Errorf("install skills: %w", err)
		}
		src := shellQuote(path.Join(SkillsDir, n))
		dst := shellQuote(path.Join(dir, n))
		fmt.Fprintf(&b, " && rm -rf %s && cp -R %s %s", dst, src, dst)
	}
	res, err := envp.Exec(ctx, env, ports.Command{Script: b.String(), Timeout: 60 * time.Second})
	if err != nil {
		return fmt.Errorf("install skills into %s: %w", dir, err)
	}
	if res.ExitCode != 0 {
		out := res.Output
		if len(out) > 512 {
			out = out[len(out)-512:]
		}
		return fmt.Errorf("install skills into %s: exit %d: %s", dir, res.ExitCode, out)
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
