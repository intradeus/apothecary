package main

// fetch: bring a person's repositories up to date in the assistant pod's
// `git` container (docs/assistant-plan.md, "A fetch"). The engine runs
//
//	shim fetch [--protocols https]   < {"repos": [{dir, url, user, pass}…]}
//
// once per Refresh or pod wake, while the person's gateway lets the
// repository host through. Every repository at once: a missing one is cloned
// (the default branch, its whole history), a present one fetched and reset
// hard to the remote head — the `agent` container only ever reads them.
//
// Each repository's credential arrives on stdin and goes to its git alone,
// as an extra HTTP header scoped to its URL's scheme and host through git's
// environment (GIT_CONFIG_COUNT): never argv, never a URL, never a file
// under the volume, never another host. Nothing in the `agent` container can
// read it, during the fetch or after. The answer is one JSON object per
// repository on stdout.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type fetchRequest struct {
	Repos []fetchRepo `json:"repos"`
}

type fetchRepo struct {
	Dir  string `json:"dir"`
	URL  string `json:"url"`
	User string `json:"user,omitempty"`
	Pass string `json:"pass,omitempty"`
}

type fetchResult struct {
	Dir    string `json:"dir"`
	OK     bool   `json:"ok"`
	Cloned bool   `json:"cloned,omitempty"`
	Head   string `json:"head,omitempty"`
	Error  string `json:"error,omitempty"`
}

// fetchTimeout bounds one repository's clone or fetch.
var fetchTimeout = 10 * time.Minute

// fetchParallel bounds how many run at once.
const fetchParallel = 8

func runFetch(args []string) {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	protocols := fs.String("protocols", "https", "the transports git may use, comma-separated")
	gitBin := fs.String("git", "/usr/bin/git", "the git binary")
	_ = fs.Parse(args)
	var req fetchRequest
	dec := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		fmt.Fprintln(os.Stderr, "fetch: request:", err)
		os.Exit(2)
	}
	results, err := fetchAll(context.Background(), *gitBin, strings.Split(*protocols, ","), req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fetch:", err)
		os.Exit(2)
	}
	enc := json.NewEncoder(os.Stdout)
	failed := false
	for _, r := range results {
		_ = enc.Encode(r)
		failed = failed || !r.OK
	}
	if failed {
		os.Exit(1)
	}
}

// fetchAll brings every repository up to date, in parallel, and answers in
// the request's order.
func fetchAll(ctx context.Context, git string, protocols []string, req fetchRequest) ([]fetchResult, error) {
	seen := map[string]bool{}
	for _, r := range req.Repos {
		if !filepath.IsAbs(r.Dir) || filepath.Clean(r.Dir) != r.Dir || seen[r.Dir] {
			return nil, fmt.Errorf("repository folder %q is not a clean absolute path, or is named twice", r.Dir)
		}
		seen[r.Dir] = true
		if r.URL == "" || strings.HasPrefix(r.URL, "-") {
			return nil, fmt.Errorf("repository %s has no usable URL", r.Dir)
		}
	}
	out := make([]fetchResult, len(req.Repos))
	sem := make(chan struct{}, fetchParallel)
	var wg sync.WaitGroup
	for i, r := range req.Repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			env, err := fetchEnv(r)
			if err != nil {
				out[i] = fetchResult{Dir: r.Dir, Error: err.Error()}
				return
			}
			res := fetchOne(ctx, git, protocols, env, r)
			if res.Error != "" && r.Pass != "" {
				res.Error = strings.ReplaceAll(res.Error, r.Pass, "***")
			}
			out[i] = res
		}()
	}
	wg.Wait()
	return out, nil
}

// fetchEnv is one repository's git environment: nothing of the user's or
// the system's configuration, no prompt, and its credential as an HTTP
// header for its URL's scheme and host only.
func fetchEnv(r fetchRepo) ([]string, error) {
	user, pass := r.User, r.Pass
	env := []string{
		"HOME=" + os.TempDir(),
		"PATH=/usr/bin:/bin",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"LC_ALL=C.UTF-8",
	}
	if pass == "" {
		return append(env, "GIT_CONFIG_COUNT=0"), nil
	}
	u, err := url.Parse(r.URL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("repository %s: a credential needs a URL with a scheme and a host", r.Dir)
	}
	if user == "" {
		user = "x-access-token"
	}
	basic := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	return append(env,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http."+u.Scheme+"://"+u.Host+"/.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic,
	), nil
}

func fetchOne(ctx context.Context, git string, protocols, env []string, r fetchRepo) fetchResult {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	res := fetchResult{Dir: r.Dir}
	safe := []string{"-c", "protocol.allow=never", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"-c", "credential.helper=", "-c", "safe.directory=*"}
	for _, p := range protocols {
		if p = strings.TrimSpace(p); p != "" {
			safe = append(safe, "-c", "protocol."+p+".allow=always")
		}
	}
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, git, append(append([]string{}, safe...), args...)...)
		cmd.Env = env
		var errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = io.Discard, &limitWriter{w: &errb, n: 8 << 10}
		if err := cmd.Run(); err != nil {
			if msg := strings.TrimSpace(errb.String()); msg != "" {
				return errors.New(msg)
			}
			return err
		}
		return nil
	}
	if _, err := os.Stat(filepath.Join(r.Dir, ".git")); err != nil {
		// A folder left half-made by an interrupted clone is cleared first.
		_ = os.RemoveAll(r.Dir)
		if err := os.MkdirAll(filepath.Dir(r.Dir), 0o755); err != nil {
			res.Error = err.Error()
			return res
		}
		if err := run("clone", "--quiet", "--single-branch", "--no-tags", "--", r.URL, r.Dir); err != nil {
			res.Error = "clone: " + err.Error()
			return res
		}
		res.Cloned = true
	} else {
		if err := run("-C", r.Dir, "remote", "set-url", "origin", "--", r.URL); err != nil {
			res.Error = "remote: " + err.Error()
			return res
		}
		if err := run("-C", r.Dir, "fetch", "--quiet", "--prune", "--no-tags", "origin"); err != nil {
			res.Error = "fetch: " + err.Error()
			return res
		}
		if err := run("-C", r.Dir, "reset", "--quiet", "--hard", "FETCH_HEAD"); err != nil {
			res.Error = "reset: " + err.Error()
			return res
		}
	}
	head, err := exec.CommandContext(ctx, git, "-c", "safe.directory=*", "-C", r.Dir, "rev-parse", "HEAD").Output()
	if err != nil {
		res.Error = "rev-parse: " + err.Error()
		return res
	}
	res.OK, res.Head = true, strings.TrimSpace(string(head))
	return res
}

// runIdle waits for its container to be stopped: the assistant pod's git
// container, which only ever runs what the engine execs into it, and has no
// shell to wait with.
func runIdle() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
}
