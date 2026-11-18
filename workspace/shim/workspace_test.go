package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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
	st, _ := os.Stat(filepath.Join(root, "big"))
	if r.Mtime == "" || r.Mtime != strconv.FormatInt(st.ModTime().UnixNano(), 10) {
		t.Errorf("big mtime = %q, want %d", r.Mtime, st.ModTime().UnixNano())
	}
	if _, err := readRepoFile(root, "."); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("a directory: %v", err)
	}
	if _, err := readRepoFile(root, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}

// Anything but a regular file is refused before it is opened: a FIFO's open
// would block for a writer that never comes.
func TestReadFileRefusesWhatIsNotAFile(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		_, err := readRepoFile(root, "fifo")
		done <- err
		_, err = readAbsFile("/dev/null")
		done <- err
	}()
	for _, what := range []string{"fifo", "/dev/null"} {
		select {
		case err := <-done:
			if !errors.Is(err, errNotRegular) {
				t.Errorf("%s: %v", what, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the read blocked", what)
		}
	}
}

func TestReadFileUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secret"), []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := readRepoFile(root, "secret"); !errors.Is(err, os.ErrPermission) {
		t.Errorf("mode 000: %v", err)
	}
}

// /proc files report size 0; the capped read reads them for real.
func TestReadFileProc(t *testing.T) {
	r, err := readAbsFile("/proc/self/status")
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("no /proc")
	}
	if err != nil || r.Binary || !strings.Contains(r.Content, "Name:") || r.Size != int64(len(r.Content)) || r.Truncated {
		t.Errorf("/proc/self/status = %+v, %v", r, err)
	}
	// A file that opens but fails its read (/proc/self/mem at offset 0) is
	// refused as unreadable, never a sidecar failure.
	if _, err := readAbsFile("/proc/self/mem"); !errors.Is(err, errUnreadable) {
		t.Errorf("/proc/self/mem: %v", err)
	}
}

func TestAbsPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for in, want := range map[string]string{
		"~":           home,
		"~/":          home,
		" ~/.bashrc":  filepath.Join(home, ".bashrc"),
		"/a/./b/../c": "/a/c",
		"/":           "/",
	} {
		if got, err := absPath(in); err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "a/b", "./x", "~user/x", "~x"} {
		if _, err := absPath(in); !errors.Is(err, errRelative) {
			t.Errorf("%q: %v", in, err)
		}
	}
	t.Setenv("HOME", "")
	if _, err := absPath("~/x"); err == nil || errors.Is(err, errRelative) {
		t.Errorf("no home: %v", err)
	}
	if _, err := readAbsFile("x"); !errors.Is(err, errRelative) {
		t.Errorf("relative read: %v", err)
	}
	if _, err := listAbsDir("x"); !errors.Is(err, errRelative) {
		t.Errorf("relative listing: %v", err)
	}
}

func TestReadAbsFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("alias x=y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r, err := readAbsFile("~/.bashrc"); err != nil || r.Path != filepath.Join(home, ".bashrc") || r.Content != "alias x=y\n" || r.Mtime == "" {
		t.Errorf("~/.bashrc = %+v, %v", r, err)
	}
	if _, err := os.Stat("/etc/hosts"); err == nil {
		if r, err := readAbsFile("/etc/../etc/hosts"); err != nil || r.Path != "/etc/hosts" || r.Binary {
			t.Errorf("/etc/hosts = %+v, %v", r, err)
		}
	}
	if _, err := readAbsFile("~"); !errors.Is(err, errIsDir) {
		t.Errorf("a directory: %v", err)
	}
	if _, err := readAbsFile("~/missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}

// An absolute listing: absolute paths, links shown as what they lead to
// (wherever that is), nothing hidden.
func TestListAbsDir(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"sub", ".git"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.Symlink(other, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "a.txt"), filepath.Join(dir, "filelink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nowhere", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	ents, err := listAbsDir(dir + "/sub/..")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range ents {
		if e.Path != filepath.Join(dir, e.Name) || e.Mtime == "" {
			t.Errorf("entry = %+v", e)
		}
		s := e.Name
		if e.Dir {
			s += "/"
		}
		got = append(got, s+":"+strconv.FormatInt(e.Size, 10))
	}
	if strings.Join(got, " ") != ".git/:0 link/:0 sub/:0 a.txt:3 dangling:0 filelink:3" {
		t.Errorf("entries = %v", got)
	}
	if _, err := listAbsDir(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}

func TestStatPath(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, []byte("abcd"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := statPath(f, nil); !r.Exists || r.Dir || r.Size != 4 || r.Mtime == "" || r.Error != "" {
		t.Errorf("file = %+v", r)
	}
	if r := statPath(dir, nil); !r.Exists || !r.Dir || r.Size != 0 {
		t.Errorf("dir = %+v", r)
	}
	for _, p := range []string{filepath.Join(dir, "gone"), filepath.Join(f, "under-a-file")} {
		if r := statPath(p, nil); r != (statResult{}) {
			t.Errorf("%s = %+v", p, r)
		}
	}
	if r := statPath("", os.ErrNotExist); r != (statResult{}) {
		t.Errorf("did not resolve, missing = %+v", r)
	}
	if r := statPath("", errOutsideRepo); r.Exists || r.Error != errOutsideRepo.Error() {
		t.Errorf("outside = %+v", r)
	}
}

// resolveFixture: a primary and two extra checkouts, where "shared.go" is in
// the primary and in "b", and "only-b.go" in "b" alone.
func resolveFixture(t *testing.T) []checkout {
	t.Helper()
	primary, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(dir, name string) {
		t.Helper()
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(primary, "shared.go")
	write(primary, "internal/run/run.go")
	write(b, "shared.go")
	write(b, "only-b.go")
	return []checkout{{dir: primary}, {name: "a", dir: a}, {name: "b", dir: b}}
}

func TestResolvePath(t *testing.T) {
	cos := resolveFixture(t)
	primary, b := cos[0].dir, cos[2].dir
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// A link into a checkout from elsewhere resolves to the checkout's file.
	if err := os.Symlink(b, filepath.Join(home, "b-link")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		repo, path string
		want       resolved
	}{
		// The named checkout wins over another with the same path.
		{"", "shared.go", resolved{Path: "shared.go"}},
		{"repo", "./shared.go", resolved{Path: "shared.go"}},
		{"b", "shared.go", resolved{Repo: "b", Path: "shared.go"}},
		// Otherwise the other checkouts, in order.
		{"", "only-b.go", resolved{Repo: "b", Path: "only-b.go"}},
		{"a", "internal/run/run.go", resolved{Path: "internal/run/run.go"}},
		{"", "internal/run", resolved{Path: "internal/run", Dir: true}},
		{"", ".", resolved{Dir: true}},
		// An absolute path inside a checkout maps back to it.
		{"", filepath.Join(b, "only-b.go"), resolved{Repo: "b", Path: "only-b.go"}},
		{"", filepath.Join(primary, "internal/../shared.go"), resolved{Path: "shared.go"}},
		{"", "~/b-link/shared.go", resolved{Repo: "b", Path: "shared.go"}},
		{"b", b, resolved{Repo: "b", Dir: true}},
		// Anything else that exists is absolute.
		{"", "~/notes.txt", resolved{Abs: filepath.Join(home, "notes.txt")}},
		{"", "~", resolved{Abs: home, Dir: true}},
	}
	for _, c := range cases {
		if got, err := resolvePath(cos, c.repo, c.path); err != nil || got != c.want {
			t.Errorf("(%q, %q) = %+v, %v; want %+v", c.repo, c.path, got, err, c.want)
		}
	}
	for _, p := range []string{"missing.go", "../shared.go", "/no/such/file", "~/missing"} {
		if got, err := resolvePath(cos, "", p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%q = %+v, %v; want not found", p, got, err)
		}
	}
	// A checkout whose directory is gone is skipped.
	gone := append([]checkout{{name: "gone", dir: filepath.Join(home, "gone")}}, cos...)
	if got, err := resolvePath(gone, "gone", filepath.Join(b, "only-b.go")); err != nil || got.Repo != "b" {
		t.Errorf("past a gone checkout = %+v, %v", got, err)
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
