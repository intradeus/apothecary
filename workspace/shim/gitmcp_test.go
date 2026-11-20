package main

// The git tool: it runs git's read commands in the session's repositories,
// and refuses everything that writes, fetches, runs another program or
// leaves the repository — whatever the model sends.

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo is a repository with two commits to ci.yml.
func gitRepo(t *testing.T) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(gitPath, append([]string{"-C", dir, "-c", "user.email=a@b", "-c", "user.name=a"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "ci.yml"), []byte("test: go test ./...\n"), 0o644)
	run("add", ".")
	run("commit", "-qm", "add the test job")
	os.WriteFile(filepath.Join(dir, "ci.yml"), []byte("test: task check\n"), 0o644)
	run("commit", "-qam", "run task check")
	return dir
}

func newGitServer(t *testing.T) (*gitServer, string) {
	t.Helper()
	repo := gitRepo(t)
	gitPath, _ := exec.LookPath("git")
	return &gitServer{repos: map[string]string{"api": repo}, state: t.TempDir(), git: gitPath}, repo
}

func gitCall(s *gitServer, repo string, args ...string) (string, error) {
	raw, _ := json.Marshal(map[string]any{"repo": repo, "args": args})
	return s.call(raw)
}

func TestGitToolReadsHistory(t *testing.T) {
	s, _ := newGitServer(t)
	out, err := gitCall(s, "api", "log", "--format=%s", "--", "ci.yml")
	if err != nil || out != "run task check\nadd the test job\n" {
		t.Errorf("log = %q, %v", out, err)
	}
	out, err = gitCall(s, "api", "show", "HEAD~1:ci.yml")
	if err != nil || out != "test: go test ./...\n" {
		t.Errorf("show = %q, %v", out, err)
	}
	if out, err := gitCall(s, "api", "blame", "--porcelain", "ci.yml"); err != nil || !strings.Contains(out, "run task check") {
		t.Errorf("blame = %q, %v", out, err)
	}
	if out, err := gitCall(s, "api", "grep", "-n", "task"); err != nil || !strings.Contains(out, "ci.yml:1:test: task check") {
		t.Errorf("grep = %q, %v", out, err)
	}
}

func TestGitToolRefuses(t *testing.T) {
	s, repo := newGitServer(t)
	outside := filepath.Join(t.TempDir(), "x")
	os.WriteFile(outside, []byte("secret\n"), 0o644)
	for name, args := range map[string][]string{
		"a write":             {"commit", "-m", "x"},
		"a fetch":             {"fetch", "origin"},
		"a remote":            {"ls-remote", "https://example.com/r.git"},
		"a config change":     {"config", "user.name", "x"},
		"an option first":     {"-c", "core.pager=sh", "log"},
		"a file written":      {"diff", "--output=/tmp/x", "HEAD~1"},
		"a file written, two": {"log", "--output", "/tmp/x"},
		"a pager program":     {"grep", "-O", "task"},
		"a pager, joined":     {"grep", "-Ovi", "task"},
		"an external diff":    {"diff", "--ext-diff", "HEAD~1"},
		"a text conversion":   {"log", "-p", "--textconv"},
		"a filter":            {"cat-file", "--filters", "HEAD:ci.yml"},
		"outside, no index":   {"diff", "--no-index", outside, filepath.Join(repo, "ci.yml")},
		"another git dir":     {"log", "--git-dir=/etc"},
		// git's parser bundles short options and takes long ones by any
		// unambiguous prefix: each is refused all the same.
		"a pager in a bundle":      {"grep", "-nOecho", "a"},
		"a pager in a list bundle": {"grep", "-lOecho", "a"},
		"a pager, abbreviated":     {"grep", "--open-files=echo", "a"},
		"a pager, shorter":         {"grep", "--open=echo", "a"},
		"an external diff, short":  {"diff", "--ext", "HEAD~1"},
		"a textconv, short":        {"log", "-p", "--textc"},
		"no index, short":          {"diff", "--no-inde", "/etc/passwd", "ci.yml"},
		"an output, short":         {"log", "--out=/tmp/x"},
		"nothing":                  {},
	} {
		if out, err := gitCall(s, "api", args...); err == nil {
			t.Errorf("%s: ran, %q", name, out)
		}
	}
	if _, err := gitCall(s, "other", "log"); err == nil || !strings.Contains(err.Error(), "api") {
		t.Errorf("an unknown repository = %v", err)
	}
	// A path outside the repository is git's own refusal.
	if _, err := gitCall(s, "api", "show", "HEAD:../"+filepath.Base(outside)); err == nil {
		t.Error("show of a path outside the repository ran")
	}
	// Real options that look like the start of a refused one still run.
	for _, args := range [][]string{{"grep", "--text", "task"}, {"log", "--oneline"}, {"grep", "-n", "-e", "task"}, {"log", "--no-ext-diff", "-p", "-n", "1"}} {
		if _, err := gitCall(s, "api", args...); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	// After --, arguments are paths: an option's spelling there is a path.
	if _, err := gitCall(s, "api", "log", "--", "--output=x"); err != nil {
		t.Errorf("a path that looks like an option: %v", err)
	}
}

// No configuration reaches git but the forced settings: a repository whose
// own config names a pager, an external diff or a hook runs none of them.
func TestGitToolIgnoresARepositorysPrograms(t *testing.T) {
	s, repo := newGitServer(t)
	marker := filepath.Join(t.TempDir(), "ran")
	cfg := "[core]\n\tpager = touch " + marker + "\n\tfsmonitor = touch " + marker + "\n\thooksPath = /tmp\n[diff]\n\texternal = touch " + marker + "\n" +
		"[diff \"x\"]\n\ttextconv = touch " + marker + "\n\tcommand = touch " + marker + "\n"
	os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.yml diff=x\n"), 0o644)
	f, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(cfg)
	f.Close()
	for _, args := range [][]string{{"log", "-p"}, {"diff", "HEAD~1"}, {"show", "HEAD"}, {"blame", "ci.yml"}, {"status"}} {
		if _, err := gitCall(s, "api", args...); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a program named in the repository's config ran")
	}
}

func TestGitToolSpillsALongAnswer(t *testing.T) {
	s, repo := newGitServer(t)
	big := strings.Repeat("a line of the file\n", 6000)
	os.WriteFile(filepath.Join(repo, "big.txt"), []byte(big), 0o644)
	if out, err := exec.Command(s.git, "-C", repo, "add", "-N", "big.txt").CombinedOutput(); err != nil {
		t.Fatalf("add -N: %v %s", err, out)
	}
	out, err := gitCall(s, "api", "diff")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(out, "all of it is in ")
	if i < 0 || len(out) > gitMaxInline+300 {
		t.Fatalf("no spill: %d bytes", len(out))
	}
	path := strings.Fields(out[i+len("all of it is in "):])[0]
	path = strings.TrimSuffix(path, ":")
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(path, s.state) || !strings.Contains(string(data), "a line of the file") {
		t.Errorf("spill %s: %v", path, err)
	}
}

// The MCP framing: initialize, one tool listed with the session's
// repositories, a call answered, an unknown tool an error.
func TestGitMCPServes(t *testing.T) {
	s, _ := newGitServer(t)
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"git","arguments":{"repo":"api","args":["log","--format=%s","-n","1"]}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"git","arguments":{"repo":"api","args":["push"]}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"shell"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"resources/list"}`,
	}, "\n")
	var out strings.Builder
	s.serve(strings.NewReader(in), &out)
	got := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var m struct {
			ID json.RawMessage `json:"id"`
		}
		json.Unmarshal(sc.Bytes(), &m)
		got[string(m.ID)] = sc.Text()
	}
	for id, want := range map[string]string{
		"1": `"protocolVersion":"2025-06-18"`,
		"2": `"enum":["api"]`,
		"3": `"text":"run task check\n"`,
		"4": `"isError":true`,
		"5": `"code":-32602`,
		"6": `"code":-32601`,
	} {
		if !strings.Contains(got[id], want) {
			t.Errorf("reply %s = %s, want %s", id, got[id], want)
		}
	}
	if len(got) != 6 {
		t.Errorf("%d replies for 6 requests and a notification", len(got))
	}
}

func TestGitMCPFlags(t *testing.T) {
	r := shim(t, "", "git-mcp", "--repo", "api=/x")
	if r.code != 2 || !strings.Contains(r.stderr, "--state") {
		t.Errorf("no state = %+v", r)
	}
	if err := (repoFlags{}).Set("relative=x"); err == nil {
		t.Error("a relative repository path was taken")
	}
}
