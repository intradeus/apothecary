package main

// The sidecar's workspace views: a file tree, file contents, and the run's
// git changes. Read-only by design — the agent owns the checkout while a run
// is live, and an editor writing underneath it would race its edits. The
// human edits through code-server when they need to (envport.EditorStarter).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxFileBytes = 1 << 20

var errOutsideRepo = errors.New("path is outside the repository")

// repoPath resolves a repo-relative path to an absolute one inside root,
// following symlinks: a checkout is repo-controlled content, and a link to
// /apothecary/run.env must not become a way to read the run token.
func repoPath(root, rel string) (string, error) {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || rel == "." {
		return filepath.EvalSymlinks(root)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errOutsideRepo
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	full, err := filepath.EvalSymlinks(filepath.Join(realRoot, clean))
	if err != nil {
		return "", err
	}
	if full != realRoot && !strings.HasPrefix(full, realRoot+string(filepath.Separator)) {
		return "", errOutsideRepo
	}
	return full, nil
}

type treeEntry struct {
	Name string `json:"name"`
	Path string `json:"path"` // repo-relative, slash-separated
	Dir  bool   `json:"dir"`
	Size int64  `json:"size,omitempty"`
	// Ignored: matched by .gitignore (shown dimmed — node_modules is real,
	// but rarely what a reviewer is after).
	Ignored bool `json:"ignored,omitempty"`
}

// listDir lists one directory level. .git is hidden; everything else shows,
// ignored entries flagged rather than dropped.
func listDir(root, rel string) ([]treeEntry, error) {
	dir, err := repoPath(root, rel)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	base := strings.Trim(filepath.ToSlash(filepath.Clean("/"+rel)), "/")
	out := make([]treeEntry, 0, len(ents))
	var names []string
	for _, e := range ents {
		if e.Name() == ".git" {
			continue
		}
		p := e.Name()
		if base != "" {
			p = base + "/" + e.Name()
		}
		te := treeEntry{Name: e.Name(), Path: p, Dir: e.IsDir()}
		if info, err := e.Info(); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				// Show where a link leads only if it stays in the repo.
				if target, err := repoPath(root, p); err == nil {
					if st, err := os.Stat(target); err == nil {
						te.Dir = st.IsDir()
					}
				}
			} else if !e.IsDir() {
				te.Size = info.Size()
			}
		}
		out = append(out, te)
		names = append(names, p)
	}
	ignored := gitIgnored(root, names)
	for i := range out {
		out[i].Ignored = ignored[out[i].Path]
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func gitIgnored(root string, paths []string) map[string]bool {
	out := map[string]bool{}
	if len(paths) == 0 {
		return out
	}
	stdout, _, _ := runGit(root, []byte(strings.Join(paths, "\x00")), "check-ignore", "-z", "--stdin")
	for _, p := range strings.Split(stdout, "\x00") {
		if p != "" {
			out[p] = true
		}
	}
	return out
}

type fileResp struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
}

func readRepoFile(root, rel string) (fileResp, error) {
	full, err := repoPath(root, rel)
	if err != nil {
		return fileResp{}, err
	}
	st, err := os.Stat(full)
	if err != nil {
		return fileResp{}, err
	}
	if st.IsDir() {
		return fileResp{}, fmt.Errorf("%s is a directory", rel)
	}
	f, err := os.Open(full)
	if err != nil {
		return fileResp{}, err
	}
	defer f.Close()
	buf := make([]byte, min(st.Size(), maxFileBytes))
	n, _ := f.Read(buf)
	buf = buf[:n]
	r := fileResp{Path: rel, Size: st.Size(), Truncated: st.Size() > maxFileBytes}
	if bytes.IndexByte(buf, 0) >= 0 || !utf8.Valid(trimPartialRune(buf)) {
		r.Binary = true
		return r, nil
	}
	r.Content = string(buf)
	return r, nil
}

// trimPartialRune drops a multi-byte rune cut in half by the size cap, so a
// truncated UTF-8 file is not misread as binary.
func trimPartialRune(b []byte) []byte {
	for i := 0; i < 3 && len(b) > 0; i++ {
		if utf8.Valid(b) {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}

// --- git ------------------------------------------------------------------------

func runGit(dir string, stdin []byte, args ...string) (string, string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotepath=off"}, args...)...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
	}
	return out.String(), errb.String(), code
}

type changedFile struct {
	Path string `json:"path"`
	// Status: A added, M modified, D deleted, R renamed, ? untracked.
	Status    string `json:"status"`
	OldPath   string `json:"old_path,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
	// Committed: the change is in a commit on the run's branch (as opposed
	// to only in the working tree).
	Committed bool `json:"committed"`
}

type commitInfo struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

type changesResp struct {
	Branch    string        `json:"branch"`
	Head      string        `json:"head"`
	Base      string        `json:"base"`       // the ref asked for
	MergeBase string        `json:"merge_base"` // what the diff is against
	Commits   []commitInfo  `json:"commits"`
	Files     []changedFile `json:"files"`
}

// mergeBase is what "the run's changes" are measured against: the point the
// branch forked from base. Without a base (or when it is unknown here) the
// diff falls back to HEAD — uncommitted work only.
func mergeBase(root, base string) string {
	if base != "" {
		if out, _, code := runGit(root, nil, "merge-base", "HEAD", base); code == 0 {
			return strings.TrimSpace(out)
		}
	}
	if out, _, code := runGit(root, nil, "rev-parse", "HEAD"); code == 0 {
		return strings.TrimSpace(out)
	}
	return ""
}

func gitChanges(root, base string) (changesResp, error) {
	if _, errs, code := runGit(root, nil, "rev-parse", "--git-dir"); code != 0 {
		return changesResp{}, fmt.Errorf("not a git checkout: %s", strings.TrimSpace(errs))
	}
	r := changesResp{Base: base, Commits: []commitInfo{}, Files: []changedFile{}}
	if out, _, code := runGit(root, nil, "rev-parse", "--abbrev-ref", "HEAD"); code == 0 {
		r.Branch = strings.TrimSpace(out)
	}
	if out, _, code := runGit(root, nil, "rev-parse", "HEAD"); code == 0 {
		r.Head = strings.TrimSpace(out)
	}
	mb := mergeBase(root, base)
	r.MergeBase = mb
	if mb == "" {
		// A repository with no commit yet: everything is untracked.
		r.Files = append(r.Files, untracked(root)...)
		return r, nil
	}
	if out, _, code := runGit(root, nil, "log", "--format=%H%x1f%s", mb+"..HEAD"); code == 0 {
		for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
			if sha, subj, ok := strings.Cut(line, "\x1f"); ok {
				r.Commits = append(r.Commits, commitInfo{SHA: sha, Subject: subj})
			}
		}
	}
	committed := map[string]bool{}
	if r.Head != "" && r.Head != mb {
		if out, _, code := runGit(root, nil, "diff", "--name-only", "-z", mb, "HEAD"); code == 0 {
			for p := range strings.SplitSeq(out, "\x00") {
				if p != "" {
					committed[p] = true
				}
			}
		}
	}
	// Working tree against the merge base: committed + staged + unstaged in
	// one comparison, which is what a reviewer reads.
	byPath := map[string]*changedFile{}
	var order []string
	if out, _, code := runGit(root, nil, "diff", "--name-status", "-z", "-M", mb); code == 0 {
		f := strings.Split(out, "\x00")
		for i := 0; i < len(f); i++ {
			st := f[i]
			if st == "" {
				continue
			}
			cf := changedFile{Status: st[:1]}
			if (cf.Status == "R" || cf.Status == "C") && i+2 < len(f) {
				cf.OldPath, cf.Path = f[i+1], f[i+2]
				i += 2
			} else if i+1 < len(f) {
				cf.Path = f[i+1]
				i++
			}
			cf.Committed = committed[cf.Path]
			byPath[cf.Path] = &cf
			order = append(order, cf.Path)
		}
	}
	if out, _, code := runGit(root, nil, "diff", "--numstat", "-z", "-M", mb); code == 0 {
		applyNumstat(out, byPath)
	}
	for _, p := range order {
		r.Files = append(r.Files, *byPath[p])
	}
	r.Files = append(r.Files, untracked(root)...)
	return r, nil
}

// applyNumstat folds `git diff --numstat -z` counts into the file list. With
// -z a rename is "adds\tdels\t\0old\0new\0"; otherwise "adds\tdels\tpath\0".
func applyNumstat(out string, byPath map[string]*changedFile) {
	f := strings.Split(out, "\x00")
	for i := 0; i < len(f); i++ {
		rec := f[i]
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		path := parts[2]
		if path == "" && i+2 < len(f) {
			path = f[i+2]
			i += 2
		}
		cf := byPath[path]
		if cf == nil {
			continue
		}
		if parts[0] == "-" {
			cf.Binary = true
			continue
		}
		cf.Additions, _ = strconv.Atoi(parts[0])
		cf.Deletions, _ = strconv.Atoi(parts[1])
	}
}

func untracked(root string) []changedFile {
	out, _, code := runGit(root, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if code != 0 {
		return nil
	}
	var files []changedFile
	for p := range strings.SplitSeq(out, "\x00") {
		if p == "" {
			continue
		}
		cf := changedFile{Path: p, Status: "?"}
		if full, err := repoPath(root, p); err == nil {
			if data, err := os.ReadFile(full); err == nil {
				if bytes.IndexByte(data, 0) >= 0 {
					cf.Binary = true
				} else if len(data) > 0 {
					cf.Additions = bytes.Count(data, []byte("\n"))
					if data[len(data)-1] != '\n' {
						cf.Additions++
					}
				}
			}
		}
		files = append(files, cf)
	}
	return files
}

type diffResp struct {
	Path      string `json:"path"`
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
}

const maxDiffBytes = 2 << 20

// gitDiff is one file's unified diff against the merge base (or, for an
// untracked file, against nothing).
func gitDiff(root, base, rel string) (diffResp, error) {
	// git gets the cleaned path, never rel: --no-index takes an absolute path
	// at its word and reads whatever is there.
	clean, err := repoPathLexical(rel)
	if err != nil {
		return diffResp{}, err
	}
	mb := mergeBase(root, base)
	var out string
	var code int
	var errs string
	if mb != "" {
		out, errs, code = runGit(root, nil, "diff", "-M", "--no-color", mb, "--", clean)
	}
	if strings.TrimSpace(out) == "" && !isTracked(root, clean) {
		// Untracked (or no commits yet): diff against the empty file. Exit 1
		// is "differences found", the normal case.
		if _, err := repoPath(root, clean); err == nil {
			out, errs, code = runGit(root, nil, "diff", "--no-index", "--no-color", "--", os.DevNull, clean)
			if code == 1 {
				code = 0
			}
		}
	}
	if code != 0 && out == "" {
		return diffResp{}, fmt.Errorf("git diff: %s", strings.TrimSpace(errs))
	}
	r := diffResp{Path: rel, Diff: out}
	if len(out) > maxDiffBytes {
		r.Diff, r.Truncated = out[:maxDiffBytes], true
	}
	return r, nil
}

func isTracked(root, rel string) bool {
	_, _, code := runGit(root, nil, "ls-files", "--error-unmatch", "--", rel)
	return code == 0
}

// repoPathLexical validates a repo-relative path without touching the disk
// (a deleted file has nothing to resolve, but its diff is still wanted).
func repoPathLexical(rel string) (string, error) {
	rel = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(rel)), "/")
	clean := filepath.Clean(filepath.FromSlash(rel))
	if rel == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errOutsideRepo
	}
	return clean, nil
}
