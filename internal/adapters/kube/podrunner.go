package kube

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// PodRunner runs commands in one container of one pod through `kubectl exec`,
// with the same contract as running them locally: stdin, separate
// stdout/stderr, live tee, and the REMOTE exit code as the result.
//
// kubectl reports a remote command's nonzero exit by exiting with the same
// code and printing "command terminated with exit code N" on stderr. That
// line is stripped (from stderr and from the tee): it is kubectl talking, not
// the command. kubectl failing on its own (no such pod, the API unreachable,
// the stream not upgrading) has no such line; those are returned as a
// TransportError — the "the process never ran" class callers retry — and
// any other markerless failure is a result, because the command may have run.
type PodRunner struct {
	C         *Client
	Pod       string
	Container string
}

// TransportError is kubectl failing to run the command at all.
type TransportError struct {
	Code   int
	Stderr string
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("kubectl exec: exit %d: %s", e.Code, strings.TrimSpace(e.Stderr))
}

var exitMarkerRE = regexp.MustCompile(`(?m)^command terminated with exit code (\d+)\r?\n?$`)

// transportSigns are kubectl's own failures to reach the container.
var transportSigns = []string{
	"Error from server (NotFound)",
	"Error from server (BadRequest)", // container not running / waiting to start
	"unable to upgrade connection",
	"Unable to connect to the server",
	"error dialing backend",
	"connection refused",
	"i/o timeout",
	"TLS handshake timeout",
	"timed out waiting for the condition",
	"Internal error occurred",
}

// ExecArgs is the kubectl argv that runs argv in the container (Global flags
// excluded). env becomes an `env K=V…` prefix: kubectl exec has no --env.
func (r PodRunner) ExecArgs(stdin bool, env []string, argv []string) []string {
	args := []string{"exec"}
	if stdin {
		args = append(args, "-i")
	}
	args = append(args, "--pod-running-timeout=5s", r.Pod)
	if r.Container != "" {
		args = append(args, "-c", r.Container)
	}
	args = append(args, "--")
	if len(env) > 0 {
		args = append(append(args, "env"), env...)
	}
	return append(args, argv...)
}

func (r PodRunner) Run(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer, env ...string) ([]byte, []byte, int, error) {
	argv := append([]string{bin}, args...)
	full := append(r.C.Global(), r.ExecArgs(stdin != nil, append([]string{"GIT_TERMINAL_PROMPT=0"}, env...), argv)...)
	var ft *filterTee
	if tee != nil {
		ft = &filterTee{w: tee}
		tee = ft
	}
	out, errb, code, err := r.C.runner().Run(ctx, r.C.bin(), full, stdin, tee)
	if ft != nil {
		ft.flush()
	}
	if err != nil {
		return nil, nil, 0, err
	}
	if code == 0 {
		return out, errb, 0, nil
	}
	if m := exitMarkerRE.FindSubmatchIndex(errb); m != nil {
		n, _ := strconv.Atoi(string(errb[m[2]:m[3]]))
		clean := append(append([]byte{}, errb[:m[0]]...), errb[m[1]:]...)
		return out, clean, n, nil
	}
	for _, sign := range transportSigns {
		if bytes.Contains(errb, []byte(sign)) {
			return nil, nil, 0, &TransportError{Code: code, Stderr: string(errb)}
		}
	}
	return out, errb, code, nil
}

// filterTee forwards whole lines, dropping kubectl's exit marker.
type filterTee struct {
	mu  sync.Mutex
	w   io.Writer
	buf []byte
}

func (f *filterTee) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buf = append(f.buf, p...)
	for {
		i := bytes.IndexByte(f.buf, '\n')
		if i < 0 {
			break
		}
		line := f.buf[:i+1]
		if !exitMarkerRE.Match(line) {
			_, _ = f.w.Write(line)
		}
		f.buf = f.buf[i+1:]
	}
	return len(p), nil
}

func (f *filterTee) flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.buf) > 0 && !exitMarkerRE.Match(f.buf) {
		_, _ = f.w.Write(f.buf)
	}
	f.buf = nil
}
