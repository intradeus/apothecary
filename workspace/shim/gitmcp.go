package main

// git-mcp: the assistant session's one way to read history
// (docs/assistant-plan.md, "The git tool"). An MCP server on stdio with one
// tool, git(repo, args), that the session's harness starts below itself —
// so it runs under the session's Landlock rules — and that executes git
// itself, never a shell:
//
//	shim git-mcp --state <dir> --repo <name>=<path> [--repo …]
//
// What it lets through: a read subcommand (gitReadCommands), with none of
// the options that write a file, run a program or reach outside the
// repository (gitRefusedOptions). What it turns off whatever the arguments:
// every transport, the user's and the system's configuration, hooks,
// fsmonitor, pagers, external diff and text conversion drivers (switched
// off on the command line of every subcommand that renders a diff). A long
// answer goes to a file in the session's state folder and its path comes
// back, as journal_read's does.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// gitReadCommands are the subcommands the tool runs: each reads the
// repository and writes nothing (status refreshes no index: optional locks
// are off and the checkout is read-only).
var gitReadCommands = []string{
	"log", "show", "blame", "diff", "grep", "ls-files", "ls-tree", "rev-parse",
	"rev-list", "describe", "shortlog", "cat-file", "for-each-ref", "name-rev",
	"merge-base", "status", "whatchanged",
}

// gitRefusedOptions are refused wherever they appear, as `--opt` or
// `--opt=value`: they write a file (--output), run a program (the pager of
// grep -O, external diffs, text conversion and filters), or read outside the
// repository (--no-index).
var gitRefusedOptions = []string{
	"--output", "--open-files-in-pager", "-O", "--ext-diff", "--textconv",
	"--filters", "--no-index", "--exec", "--upload-pack", "--receive-pack",
	"--git-dir", "--work-tree", "--namespace", "--super-prefix", "--config-env",
}

// gitForced are the -c settings every call runs with, ahead of the
// subcommand: no transport, no hook, no external program, no pager.
var gitForced = []string{
	"-c", "protocol.allow=never",
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=false",
	"-c", "core.pager=cat",
	"-c", "core.sshCommand=false",
	"-c", "credential.helper=",
	"-c", "safe.directory=*",
	"-c", "color.ui=false",
}

// gitMaxInline is how much output comes back in the tool result; past it
// the whole output is written to the state folder.
const gitMaxInline = 48 << 10

// gitTimeout bounds one call: a log of a large history is fast, a runaway
// pickaxe search is not.
const gitTimeout = 60 * time.Second

type gitServer struct {
	repos map[string]string // name → absolute path
	state string            // the session's state folder: long outputs
	git   string            // the git binary
	// seq numbers the output files.
	mu  sync.Mutex
	seq int
}

type repoFlags map[string]string

func (r repoFlags) String() string { return "" }

func (r repoFlags) Set(v string) error {
	name, path, ok := strings.Cut(v, "=")
	if !ok || name == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("--repo wants <name>=<absolute path>, got %q", v)
	}
	r[name] = filepath.Clean(path)
	return nil
}

func runGitMCP(args []string) {
	fs := flag.NewFlagSet("git-mcp", flag.ExitOnError)
	repos := repoFlags{}
	fs.Var(repos, "repo", "a repository the session reads: <name>=<path> (repeatable)")
	state := fs.String("state", "", "the session's state folder, for long outputs")
	gitBin := fs.String("git", "/usr/bin/git", "the git binary")
	_ = fs.Parse(args)
	if *state == "" || !filepath.IsAbs(*state) {
		fmt.Fprintln(os.Stderr, "git-mcp: --state <absolute dir> is required")
		os.Exit(2)
	}
	s := &gitServer{repos: repos, state: *state, git: *gitBin}
	s.serve(os.Stdin, os.Stdout)
}

// serve answers line-delimited JSON-RPC until in ends.
func (s *gitServer) serve(in io.Reader, out io.Writer) {
	var wmu sync.Mutex
	send := func(v any) {
		data, _ := json.Marshal(v)
		wmu.Lock()
		defer wmu.Unlock()
		_, _ = out.Write(append(data, '\n'))
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var wg sync.WaitGroup
	for sc.Scan() {
		var msg rpcMsg
		if json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &msg) != nil || len(msg.ID) == 0 || string(msg.ID) == "null" {
			continue
		}
		id := msg.ID
		ok := func(v any) { send(map[string]any{"jsonrpc": "2.0", "id": id, "result": v}) }
		switch msg.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2024-11-05"
			}
			ok(map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "apothecary-git", "version": "1"},
			})
		case "ping":
			ok(map[string]any{})
		case "tools/list":
			ok(map[string]any{"tools": []any{s.toolDef()}})
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.Name != "git" {
				send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": "unknown tool " + p.Name}})
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				text, err := s.call(p.Arguments)
				if err != nil {
					ok(map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true})
					return
				}
				ok(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
			}()
		default:
			send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "method " + msg.Method + " not found"}})
		}
	}
	wg.Wait()
}

func (s *gitServer) toolDef() map[string]any {
	names := make([]string, 0, len(s.repos))
	for n := range s.repos {
		names = append(names, n)
	}
	slices.Sort(names)
	return map[string]any{
		"name": "git",
		"description": "Run a read-only git command in one of your repositories: its history, blame, diffs and files at any commit. " +
			"args are git's own arguments after `git`, one per element, starting with the subcommand " +
			`(for example ["log", "--oneline", "-n", "20", "--", "ci.yml"] or ["show", "HEAD~3:go.mod"]). ` +
			"Subcommands: " + strings.Join(gitReadCommands, ", ") + ". Nothing that writes, fetches or runs another program.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo": map[string]any{"type": "string", "enum": names, "description": "The repository, by name."},
				"args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1,
					"description": "git's arguments, the subcommand first."},
			},
			"required":             []string{"repo", "args"},
			"additionalProperties": false,
		},
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
	}
}

// call checks the arguments and runs git.
func (s *gitServer) call(raw json.RawMessage) (string, error) {
	var in struct {
		Repo string   `json:"repo"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	dir, ok := s.repos[in.Repo]
	if !ok {
		return "", fmt.Errorf("no repository %q here; yours: %s", in.Repo, strings.Join(s.repoNames(), ", "))
	}
	if err := checkGitArgs(in.Args); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	argv := append(append([]string{"-C", dir, "--no-pager"}, gitForced...), gitArgs(in.Args)...)
	cmd := exec.CommandContext(ctx, s.git, argv...)
	cmd.Dir = dir
	cmd.Env = gitEnv(s.state)
	cmd.Stdin = nil
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limitWriter{w: &out, n: 32 << 20}, &limitWriter{w: &errb, n: 64 << 10}
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("git %s took longer than %s: narrow it", in.Args[0], gitTimeout)
	}
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", in.Args[0], msg)
	}
	text := out.String()
	if text == "" {
		text = strings.TrimSpace(errb.String())
		if text == "" {
			text = "(no output)"
		}
	}
	if len(text) <= gitMaxInline {
		return text, nil
	}
	path, werr := s.spill(text)
	if werr != nil {
		return text[:gitMaxInline] + "\n[cut: " + werr.Error() + "]", nil
	}
	return fmt.Sprintf("%s\n[the output is %d bytes; the first %d are above, all of it is in %s: read it there]", text[:runeCut(text, gitMaxInline)], len(text), gitMaxInline, path), nil
}

func (s *gitServer) repoNames() []string {
	out := make([]string, 0, len(s.repos))
	for n := range s.repos {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// spill writes a long output into the state folder.
func (s *gitServer) spill(text string) (string, error) {
	s.mu.Lock()
	s.seq++
	n := s.seq
	s.mu.Unlock()
	dir := filepath.Join(s.state, "git-output")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%d-%d.txt", os.Getpid(), n))
	return path, os.WriteFile(path, []byte(text), 0o600)
}

// checkGitArgs refuses a subcommand off the list and an option that writes,
// runs or reaches out.
func checkGitArgs(args []string) error {
	if len(args) == 0 {
		return errors.New("args needs at least the subcommand")
	}
	if !slices.Contains(gitReadCommands, args[0]) {
		return fmt.Errorf("git %s is not one of the read commands this tool runs: %s", args[0], strings.Join(gitReadCommands, ", "))
	}
	for _, a := range args[1:] {
		if strings.ContainsRune(a, 0) {
			return errors.New("an argument holds a NUL byte")
		}
		if a == "--" {
			break // what follows is paths
		}
		if bad := refusedOption(args[0], a); bad != "" {
			return fmt.Errorf("git option %s is refused here: it writes a file, runs a program or reads outside the repository", bad)
		}
	}
	return nil
}

// gitExactOptions are real options that are prefixes of refused ones: git
// takes them as themselves, never as an abbreviation.
var gitExactOptions = []string{"--text"}

// refusedOption is the refused option arg spells for subcommand cmd, or "".
// git's parser takes a long option by any unambiguous prefix (--open,
// --ext, --no-inde) and short options bundled (-nOprog), so a refused long
// option is refused by every prefix of it, and grep's -O in any bundle.
func refusedOption(cmd, arg string) string {
	switch {
	case strings.HasPrefix(arg, "--"):
		name, _, _ := strings.Cut(arg, "=")
		if slices.Contains(gitExactOptions, name) {
			return ""
		}
		for _, bad := range gitRefusedOptions {
			if !strings.HasPrefix(bad, "--") {
				continue
			}
			if name == bad || (len(name) >= 3 && strings.HasPrefix(bad, name)) {
				return bad
			}
		}
	case strings.HasPrefix(arg, "-") && len(arg) > 1:
		// A bundle of short options: grep's -O runs a program on its
		// matches, wherever it sits in the bundle.
		if cmd == "grep" && strings.ContainsRune(arg[1:], 'O') {
			return "-O"
		}
		if arg == "-O" || strings.HasPrefix(arg, "-O") {
			return "-O"
		}
	}
	return ""
}

// gitNoPrograms are the subcommands that render diffs: each runs with
// external diff drivers and text conversion off, whatever a configuration
// or an attributes file names.
var gitNoPrograms = []string{"log", "show", "diff", "whatchanged", "blame"}

// gitArgs is args with the program switches added after the subcommand.
func gitArgs(args []string) []string {
	if !slices.Contains(gitNoPrograms, args[0]) {
		return args
	}
	out := []string{args[0], "--no-textconv"}
	if args[0] != "blame" {
		out = append(out, "--no-ext-diff")
	}
	return append(out, args[1:]...)
}

// gitEnv is git's whole environment: no user or system configuration, no
// terminal prompt, no pager, no optional locks, no lazy fetch.
func gitEnv(state string) []string {
	return []string{
		"HOME=" + state,
		"PATH=/usr/bin:/bin",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_ALLOW_PROTOCOL=",
		"LC_ALL=C.UTF-8",
	}
}

// runeCut is n, moved back to the start of a UTF-8 character.
func runeCut(s string, n int) int {
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return n
}

// limitWriter keeps the first n bytes and drops the rest.
type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	total := len(p)
	if l.n <= 0 {
		return total, nil
	}
	if len(p) > l.n {
		p = p[:l.n]
	}
	l.n -= len(p)
	if _, err := l.w.Write(p); err != nil {
		return 0, err
	}
	return total, nil
}
