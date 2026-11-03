// Package commitmsg holds the commit convention's two encodings together: the
// dependency-free check every commit and pull request title goes through
// (.lefthook/commit-msg/conventional.sh) and commitizen's schema_pattern
// (.cz.toml), what the release reads the history against.
package commitmsg

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const root = "../../.."

// czPattern is .cz.toml's schema_pattern, anchored at the start as
// commitizen's re.match is.
func czPattern(t *testing.T) *regexp.Regexp {
	t.Helper()
	f, err := os.Open(filepath.Join(root, ".cz.toml"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if p, ok := strings.CutPrefix(sc.Text(), "schema_pattern = '"); ok {
			return regexp.MustCompile("^" + strings.TrimSuffix(p, "'"))
		}
	}
	t.Fatal(".cz.toml has no schema_pattern")
	return nil
}

// hook runs the check on msg as git hands it over: in a file, newline-ended.
func hook(t *testing.T, msg string) bool {
	t.Helper()
	file := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	if err := os.WriteFile(file, []byte(msg+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := exec.Command("sh", filepath.Join(root, ".lefthook/commit-msg/conventional.sh"), file).Run()
	if _, exit := err.(*exec.ExitError); err != nil && !exit {
		t.Fatal(err)
	}
	return err == nil
}

// Whatever the hook lets through, the release reads as a convention commit,
// and the other way round.
func TestHookAgreesWithCommitizen(t *testing.T) {
	cz := czPattern(t)
	for _, msg := range []string{
		"feat: x", "fix(flow): x", "feat(flow)!: a run has one cursor", "refactor!: x",
		"build: x", "chore: x", "ci: x", "docs: x", "perf: x", "revert: x", "style: x", "test: x",
		"feat: x\n\nbody\n\nmore", "feat: x\n\n", "fix(a.b/c): x",
		"update stuff", "feat:x", "feat : x", "Feat: x", "feature: x", "feat(): x", "feat(a b): x",
		"feat: ", "(flow): x", "wip feat: x", "feat: x\nbody", "feat!(flow): x",
	} {
		if got, want := hook(t, msg), cz.MatchString(msg); got != want {
			t.Errorf("%q: hook %v, commitizen %v", msg, got, want)
		}
	}
}

// What git hands the hook beyond the message itself.
func TestHookReadsTheMessageAsGitWritesIt(t *testing.T) {
	for msg, want := range map[string]bool{
		"":                                  true, // git aborts an empty message itself
		"# Please enter the commit message": true,
		"feat: x\n# Please enter the commit message\n# On branch main":                        true,
		"# a comment first\nfix: x":                                                           true,
		"feat: x\n# ------------------------ >8 ------------------------\ndiff --git a/x b/x": true,
		"Merge branch 'main' into feat/x":                                                     true,
		"Revert \"feat: x\"":                                                                  true,
		"fixup! feat: x":                                                                      true,
		"feat: x\r":                                                                           true,
		"# a comment first\nupdate stuff":                                                     false,
	} {
		if got := hook(t, msg); got != want {
			t.Errorf("%q: hook %v, want %v", msg, got, want)
		}
	}
}
