package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRepoPath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(root)
	for _, rel := range []string{"", ".", " . ", "/"} {
		if got, err := repoPath(root, rel); err != nil || got != real {
			t.Errorf("%q = %q, %v", rel, got, err)
		}
	}
	if got, err := repoPath(root, "/src"); err != nil || got != filepath.Join(real, "src") {
		t.Errorf("a leading slash stays in the repo: %q, %v", got, err)
	}
	for _, rel := range []string{"..", "../x", "src/../../x"} {
		if _, err := repoPath(root, rel); !errors.Is(err, errOutsideRepo) {
			t.Errorf("%q: %v", rel, err)
		}
	}
	if _, err := repoPath(root, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if _, err := repoPath(filepath.Join(root, "gone"), "src"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a root that does not exist: %v", err)
	}
}

func TestRepoPathLexical(t *testing.T) {
	if got, err := repoPathLexical("/a/./b"); err != nil || got != "a/b" {
		t.Errorf("got %q, %v", got, err)
	}
	for _, rel := range []string{"", ".", "/", "..", "../x", "a/../.."} {
		if _, err := repoPathLexical(rel); !errors.Is(err, errOutsideRepo) {
			t.Errorf("%q: %v", rel, err)
		}
	}
}

// A link shows as what it leads to while that stays in the repo; one leading
// out shows, but as nothing more than its name.
func TestListDirSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src", filepath.Join(root, "inside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	ents, err := listDir(root, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]treeEntry{}
	for _, e := range ents {
		got[e.Name] = e
	}
	if !got["inside"].Dir || got["outside"].Dir || len(ents) != 3 {
		t.Errorf("entries = %+v", ents)
	}
	if ents, err := listDir(root, "src"); err != nil || len(ents) != 0 {
		t.Errorf("an empty directory = %+v, %v", ents, err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ents, err := listDir(root, "/src/"); err != nil || len(ents) != 1 || ents[0].Path != "src/a.go" || ents[0].Size != 10 {
		t.Errorf("a nested directory = %+v, %v", ents, err)
	}
	if _, err := listDir(root, "../.."); !errors.Is(err, errOutsideRepo) {
		t.Errorf("escape = %v", err)
	}
	if _, err := listDir(filepath.Join(root, "src", "..", "inside"), "missing"); err == nil {
		t.Error("a missing directory listed")
	}
	f := filepath.Join(root, "file")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := listDir(root, "file"); err == nil {
		t.Error("a file listed as a directory")
	}
}

func TestReadRepoFile(t *testing.T) {
	root := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("nul", []byte("a\x00b"))
	write("latin1", []byte("caf\xe9 au lait"))
	// A multi-byte rune cut in half by the cap is text, not binary.
	cut := append(make([]byte, 0, maxFileBytes+4), []byte(strings.Repeat("a", maxFileBytes-1))...)
	cut = append(cut, []byte("€€")...)
	write("big", cut)

	if r, err := readRepoFile(root, "nul"); err != nil || !r.Binary || r.Content != "" {
		t.Errorf("nul = %+v, %v", r, err)
	}
	if r, err := readRepoFile(root, "latin1"); err != nil || !r.Binary {
		t.Errorf("latin1 = %+v, %v", r, err)
	}
	r, err := readRepoFile(root, "big")
	if err != nil || r.Binary || !r.Truncated || r.Size != int64(len(cut)) || len(r.Content) != maxFileBytes {
		t.Errorf("big = binary %v truncated %v size %d, %d bytes of content, %v", r.Binary, r.Truncated, r.Size, len(r.Content), err)
	}
	if _, err := readRepoFile(root, "."); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("a directory: %v", err)
	}
	if _, err := readRepoFile(root, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}

func TestTrimPartialRune(t *testing.T) {
	euro := []byte("€") // three bytes
	if got := trimPartialRune(append([]byte("a"), euro[:2]...)); string(got) != "a" {
		t.Errorf("cut rune = %q", got)
	}
	if got := trimPartialRune(nil); len(got) != 0 {
		t.Errorf("nil = %q", got)
	}
	// Invalid well before the end is not a cut rune: what comes back stays
	// invalid, and the file reads as binary.
	if got := trimPartialRune([]byte{0xff, 'a', 'b', 'c', 'd'}); utf8.Valid(got) {
		t.Errorf("garbage trimmed to valid %q", got)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// Renames keep both names and their counts, committed or not, binary files
// have no counts, and untracked ones count their own lines.
func TestGitChangesRenamesAndBinaries(t *testing.T) {
	repo := gitInit(t)
	lines := strings.Repeat("the same line\n", 20)
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "blob.bin"), []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "keep.txt", "blob.bin")
	git(t, repo, "commit", "-qm", "add")
	git(t, repo, "branch", "base")
	git(t, repo, "mv", "keep.txt", "moved.txt")
	if err := os.WriteFile(filepath.Join(repo, "moved.txt"), []byte(lines+"one more\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "blob.bin"), []byte{0, 3, 4}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.bin"), []byte{0, 1}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "empty"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "open-ended"), []byte("a\nb"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "moved.txt")

	c, err := gitChanges(repo, "base")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]changedFile{}
	for _, f := range c.Files {
		got[f.Path] = f
	}
	if f := got["moved.txt"]; f.Status != "R" || f.OldPath != "keep.txt" || f.Additions != 1 || f.Committed {
		t.Errorf("rename = %+v", f)
	}
	if f := got["blob.bin"]; f.Status != "M" || !f.Binary || f.Additions != 0 {
		t.Errorf("binary = %+v", f)
	}
	if f := got["new.bin"]; f.Status != "?" || !f.Binary {
		t.Errorf("untracked binary = %+v", f)
	}
	if f := got["empty"]; f.Status != "?" || f.Additions != 0 || f.Binary {
		t.Errorf("empty = %+v", f)
	}
	if f := got["open-ended"]; f.Additions != 2 {
		t.Errorf("no trailing newline = %+v", f)
	}
}

// With a base git does not know, the diff falls back to HEAD: uncommitted
// work only, never an error.
func TestGitChangesUnknownBase(t *testing.T) {
	repo := gitInit(t)
	c, err := gitChanges(repo, "no-such-ref")
	if err != nil {
		t.Fatal(err)
	}
	if c.MergeBase != c.Head || len(c.Commits) != 0 {
		t.Errorf("changes = %+v", c)
	}
	for _, f := range c.Files {
		if f.Path == "README.md" {
			t.Errorf("a committed file counted as uncommitted work: %+v", f)
		}
	}
}

// A repository with no commit yet: everything is untracked, and a file's
// diff is against nothing.
func TestGitWithNoCommits(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := gitChanges(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if c.MergeBase != "" || len(c.Files) != 1 || c.Files[0].Status != "?" || c.Files[0].Additions != 1 {
		t.Errorf("changes = %+v", c)
	}
	d, err := gitDiff(repo, "main", "a.txt")
	if err != nil || !strings.Contains(d.Diff, "+hello") {
		t.Errorf("diff = %+v, %v", d, err)
	}
}

func TestGitChangesOutsideACheckout(t *testing.T) {
	if _, err := gitChanges(t.TempDir(), ""); err == nil || !strings.Contains(err.Error(), "not a git checkout") {
		t.Errorf("err = %v", err)
	}
	if d, err := gitDiff(t.TempDir(), "", "a.txt"); err != nil || d.Diff != "" {
		t.Errorf("diff = %+v, %v", d, err)
	}
}

// git receives the checked path, not the caller's: a leading slash once sent
// --no-index to the real file — the run token, with a repository that merely
// held something at the same relative path.
func TestGitDiffStaysInTheRepo(t *testing.T) {
	repo := gitInit(t)
	secret := filepath.Join(t.TempDir(), "run.env")
	if err := os.WriteFile(secret, []byte("APOTHECARY_RUN_TOKEN=hush\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(repo, strings.TrimPrefix(secret, "/"))
	if err := os.MkdirAll(filepath.Dir(mirror), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mirror, []byte("harmless\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := gitDiff(repo, "main", secret)
	if err != nil || strings.Contains(d.Diff, "hush") || !strings.Contains(d.Diff, "+harmless") {
		t.Errorf("diff = %+v, %v", d, err)
	}
	if _, err := gitDiff(repo, "main", "../secret"); !errors.Is(err, errOutsideRepo) {
		t.Errorf("escape = %v", err)
	}
}

// A diff past the cap is cut and says so.
func TestGitDiffTruncates(t *testing.T) {
	repo := gitInit(t)
	big := strings.Repeat("0123456789abcde\n", maxDiffBytes/16+1)
	if err := os.WriteFile(filepath.Join(repo, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := gitDiff(repo, "main", "big.txt")
	if err != nil || !d.Truncated || len(d.Diff) != maxDiffBytes {
		t.Errorf("truncated %v, %d bytes, %v", d.Truncated, len(d.Diff), err)
	}
}

// numstat records that name nothing in the list are skipped, not panicked on.
func TestApplyNumstatIgnoresStrayRecords(t *testing.T) {
	cf := &changedFile{Path: "a"}
	applyNumstat("not a record\x001\t2\tother\x003\t4\ta\x00", map[string]*changedFile{"a": cf})
	if cf.Additions != 3 || cf.Deletions != 4 {
		t.Errorf("a = %+v", cf)
	}
}
