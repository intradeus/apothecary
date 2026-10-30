//go:build live

package workspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"apothecary/internal/ports/envport"
	"apothecary/internal/shell"
)

// TestLiveWriteTreeScript runs WriteTree's exact stdin (one script line +
// base64 tar) and InstallSkills' copy script inside throwaway containers
// with a GNU userland (debian) and a busybox one (the bash image), since
// a repo's devcontainer may be either. Needs docker and network for the
// pulls:  go test -tags live -run TestLiveWriteTree ./internal/adapters/workspace/
func TestLiveWriteTreeScript(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	files := []envport.TreeFile{
		{Path: "SKILL.md", Data: []byte("---\nname: x\ndescription: d\n---\nbody\n")},
		{Path: "scripts/run.sh", Mode: 0o755, Data: []byte("#!/bin/sh\necho ok\n")},
		{Path: "references/deep/a.md", Data: []byte("a")},
	}
	archive, err := buildTar(files)
	if err != nil {
		t.Fatal(err)
	}
	q := shell.Quote("/apothecary/skills/x")
	stdin := fmt.Sprintf(`rm -rf %s && mkdir -p %s && base64 -d | tar -xf - -C %s`, q, q, q) + "\n" + base64.StdEncoding.EncodeToString(archive) + "\n"
	install := "mkdir -p '/home/u/.agents/skills' && rm -rf '/home/u/.agents/skills/x' && cp -R '/apothecary/skills/x' '/home/u/.agents/skills/x'"

	for _, image := range []string{"debian:stable-slim", "bash:5.2"} {
		t.Run(image, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			name := "apo-treetest-" + strings.NewReplacer(":", "-", ".", "-").Replace(image)
			exec.Command("docker", "rm", "-f", name).Run()
			if out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--name", name, image, "sleep", "300").CombinedOutput(); err != nil {
				t.Fatalf("docker run: %v: %s", err, out)
			}
			defer exec.Command("docker", "rm", "-f", name).Run()

			cmd := exec.CommandContext(ctx, "docker", "exec", "-i", name, "bash", "-eo", "pipefail", "-s")
			cmd.Stdin = bytes.NewReader([]byte(stdin))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("write tree: %v: %s", err, out)
			}
			check := `cd /apothecary/skills/x && stat -c '%n %a' SKILL.md scripts/run.sh references/deep/a.md && cat SKILL.md && ./scripts/run.sh`
			out, err := exec.CommandContext(ctx, "docker", "exec", name, "bash", "-c", check).CombinedOutput()
			if err != nil {
				t.Fatalf("check: %v: %s", err, out)
			}
			for _, want := range []string{"SKILL.md 644", "scripts/run.sh 755", "references/deep/a.md 644", "name: x", "ok"} {
				if !strings.Contains(string(out), want) {
					t.Errorf("missing %q in:\n%s", want, out)
				}
			}
			if out, err := exec.CommandContext(ctx, "docker", "exec", name, "bash", "-eo", "pipefail", "-c", install).CombinedOutput(); err != nil {
				t.Fatalf("install: %v: %s", err, out)
			}
			out, err = exec.CommandContext(ctx, "docker", "exec", name, "bash", "-c", `stat -c '%n %a' /home/u/.agents/skills/x/scripts/run.sh && cat /home/u/.agents/skills/x/SKILL.md`).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "run.sh 755") || !strings.Contains(string(out), "name: x") {
				t.Errorf("installed copy: %v\n%s", err, out)
			}
			// Idempotent: a second write replaces, never accumulates.
			cmd = exec.CommandContext(ctx, "docker", "exec", "-i", name, "bash", "-eo", "pipefail", "-s")
			cmd.Stdin = bytes.NewReader([]byte(stdin))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("second write: %v: %s", err, out)
			}
		})
	}
}
