package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as the shim: with shimEnv set, TestMain runs main()
// instead of the tests. A subcommand that exits, reads stdin or serves until
// killed runs as the process it is, with no go build per test, and the paths
// and intervals a release pins are pointed at fixtures through the variables
// asShim reads.
const (
	shimEnv         = "SHIM_TEST_AS_SHIM"
	shimRunEnv      = "SHIM_TEST_RUN_ENV"
	shimProcNet     = "SHIM_TEST_PROC_NET"
	shimSingleton   = "SHIM_TEST_SINGLETON"
	shimFastSupport = "SHIM_TEST_FAST"
)

func asShim() {
	if v := os.Getenv(shimRunEnv); v != "" {
		runEnvPath = v
	}
	if v := os.Getenv(shimProcNet); v != "" {
		procTCPPaths, procRoot = []string{v}, filepath.Dir(v)
	}
	if v := os.Getenv(shimSingleton); v != "" {
		singletonAddr = v
	}
	if os.Getenv(shimFastSupport) != "" {
		scanInterval, heartbeatInterval = time.Millisecond, 30*time.Millisecond
		backoffBase, backoffCap = time.Millisecond, 5*time.Millisecond
		startingProbeEvery, noProbeGrace = 5*time.Millisecond, 5*time.Millisecond
		stopGrace = 2 * time.Second
	}
	// As coverflush.go does: a clean exit, so a killed shim's coverage counts.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGUSR2)
	go func() {
		<-stop
		os.Exit(0)
	}()
	main()
}

// shimCmd is the shim, ready to run with args.
func shimCmd(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	// Under -race a process otherwise sleeps a second on its way out.
	cmd.Env = append(os.Environ(), shimEnv+"=1", "GORACE=atexit_sleep_ms=0")
	// `task go:test`: what this process runs counts too. Any other -cover run
	// still needs a directory, or the process warns about it on stderr; a
	// shared one, as a detached supervisor can outlive the test.
	if dir := os.Getenv("APOTHECARY_COVERDIR"); dir != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+dir)
	} else if testing.CoverMode() != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+coverScratch())
	}
	return cmd
}

var coverScratch = sync.OnceValue(func() string {
	dir, err := os.MkdirTemp("", "shim-cover")
	if err != nil {
		panic(err)
	}
	return dir
})

// shimResult is what a finished shim left.
type shimResult struct {
	stdout, stderr string
	code           int
}

func runShim(t *testing.T, stdin string, cmd *exec.Cmd) shimResult {
	t.Helper()
	var out, errb bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(stdin), &out, &errb
	if err := cmd.Start(); err != nil {
		t.Fatalf("run %v: %v", cmd.Args, err)
	}
	// One that hangs fails here, rather than outliving a timed-out test run.
	limit := time.AfterFunc(30*time.Second, func() { _ = cmd.Process.Kill() })
	defer limit.Stop()
	err := cmd.Wait()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatalf("run %v: %v", cmd.Args, err)
	}
	return shimResult{out.String(), errb.String(), cmd.ProcessState.ExitCode()}
}

// shim runs one subcommand to its end.
func shim(t *testing.T, stdin string, args ...string) shimResult {
	t.Helper()
	return runShim(t, stdin, shimCmd(t, args...))
}

// startShim leaves a long-lived subcommand running for the test's length.
func startShim(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopShim(cmd) })
}

func stopShim(cmd *exec.Cmd) {
	_ = cmd.Process.Signal(syscall.SIGUSR2)
	_ = cmd.Wait()
}

func TestMainUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}} {
		r := shim(t, "", args...)
		if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, "usage: shim ask-human") {
			t.Errorf("shim %v = %+v", args, r)
		}
	}
}

func TestMainVersionPrintsTheSourceHash(t *testing.T) {
	if r := shim(t, "", "version"); r.code != 0 || r.stdout != sourceHash+"\n" {
		t.Errorf("version = %+v", r)
	}
}
