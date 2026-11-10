package workspace

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"apothecary/internal/ports/envport"
)

// The checkout script runs for real (local bash and git against a file://
// remote): the files it writes keep the modes git records — 0755 for an
// executable, 0644 for the rest — while the credential file is 0600. A
// script-wide `umask 077` (meant for the credential alone) once made the
// whole checkout 0600/0700, and a compose sibling running as a non-root user
// could not read a bind-mounted script from it.
func TestK8sCheckoutKeepsTrackedModes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("GNU stat; the pod is linux")
	}
	for _, bin := range []string{"bash", "git", "stat"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	dir := t.TempDir()
	env := append(os.Environ(), "HOME="+dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	git := func(wd string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
		c.Dir, c.Env = wd, env
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	remote, seed := filepath.Join(dir, "remote.git"), filepath.Join(dir, "seed")
	git(dir, "init", "-q", "--bare", remote)
	git(dir, "init", "-q", seed)
	if err := os.MkdirAll(filepath.Join(seed, "object-storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "object-storage", "entrypoint.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(seed, "add", "-A")
	git(seed, "commit", "-qm", "seed")
	git(seed, "push", "-q", remote, "main")

	// A git on PATH ahead of the real one records the credential file's mode
	// while the script runs (it is gone once the script ends).
	shim := filepath.Join(dir, "bin")
	credMode := filepath.Join(dir, "cred-mode")
	realGit, _ := exec.LookPath("git")
	if err := os.MkdirAll(shim, 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := "#!/bin/sh\n" +
		`f=${GIT_CONFIG_VALUE_0#store --file=}; [ -f "$f" ] && stat -c %a "$f" > ` + credMode + "\n" +
		`exec ` + realGit + ` "$@"` + "\n"
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(dir, "ws", "src")
	k := &kubeFake{exec: func(argv []string, stdin []byte) (string, string, int) {
		// The pod's shell, under a common umask, so the modes are exact.
		c := exec.Command("sh", "-c", `umask 022 && exec "$@"`, "sh")
		c.Args = append(c.Args, argv...)
		c.Stdin = bytes.NewReader(stdin)
		c.Env = append(append([]string{}, env...), "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"))
		var stdout, stderr bytes.Buffer
		c.Stdout, c.Stderr = &stdout, &stderr
		if err := c.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return stdout.String(), stderr.String(), ee.ExitCode()
			}
			t.Fatal(err)
		}
		return stdout.String(), stderr.String(), 0
	}}
	_, h := k8sWith(t, k, false)
	spec := envport.EnvSpec{RepoURL: "file://localhost" + remote, Branch: "feat/x", SourceBranch: "main", GitPass: "tok"}
	if err := h.ensureCheckout(context.Background(), "w", src, spec, nil); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]os.FileMode{
		"object-storage/entrypoint.sh": 0o755,
		"README":                       0o644,
		"object-storage":               0o755,
	} {
		info, err := os.Stat(filepath.Join(src, path))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %#o, want %#o", path, got, want)
		}
	}
	if b, err := os.ReadFile(credMode); err != nil || strings.TrimSpace(string(b)) != "600" {
		t.Errorf("credential file mode during the clone = %q (%v), want 600", b, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(src), "git-credentials")); !os.IsNotExist(err) {
		t.Errorf("credential file left behind: %v", err)
	}
	if out, err := exec.Command("git", "-C", src, "branch", "--show-current").Output(); err != nil || strings.TrimSpace(string(out)) != "feat/x" {
		t.Errorf("branch = %q (%v), want feat/x", out, err)
	}
}
