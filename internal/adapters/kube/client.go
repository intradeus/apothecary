// Package kube drives a Kubernetes cluster through the kubectl CLI — the same
// way the docker side of apothecary drives docker: one process per call, through
// a swappable Runner, no client library. kubectl brings authentication
// (kubeconfig or the pod's service account), exec and port-forward streams,
// and server-side apply; tests script it like they script docker.
package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Runner executes one CLI invocation. tee, when non-nil, receives stdout and
// stderr as they arrive. The method set matches the workspace adapter's
// CmdRunner, so one scripted runner can play both docker and kubectl in tests.
type Runner interface {
	Run(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer, env ...string) (stdout, stderr []byte, exitCode int, err error)
}

// ExecRunner runs real processes.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer, env ...string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	if tee != nil {
		safe := &syncWriter{w: tee}
		cmd.Stdout = io.MultiWriter(&out, safe)
		cmd.Stderr = io.MultiWriter(&errb, safe)
	} else {
		cmd.Stdout, cmd.Stderr = &out, &errb
	}
	err := cmd.Run()
	code := 0
	if ee, ok := err.(interface{ ExitCode() int }); ok {
		code = ee.ExitCode()
		err = nil // it ran; a nonzero exit is a result
	}
	return out.Bytes(), errb.Bytes(), code, err
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// Client is kubectl aimed at one namespace of one cluster.
type Client struct {
	Bin        string // default "kubectl"
	Kubeconfig string // "" = kubectl's default (in-cluster service account, ~/.kube/config)
	Context    string
	Namespace  string
	R          Runner // default ExecRunner
}

func (c *Client) bin() string {
	if c.Bin == "" {
		return "kubectl"
	}
	return c.Bin
}

func (c *Client) runner() Runner {
	if c.R == nil {
		return ExecRunner{}
	}
	return c.R
}

// Global is the flags every invocation carries: cluster, credentials and
// namespace.
func (c *Client) Global() []string {
	var a []string
	if c.Kubeconfig != "" {
		a = append(a, "--kubeconfig", c.Kubeconfig)
	}
	if c.Context != "" {
		a = append(a, "--context", c.Context)
	}
	if c.Namespace != "" {
		a = append(a, "--namespace", c.Namespace)
	}
	return a
}

// requestTimeout bounds every API call that is not a stream.
const requestTimeout = "--request-timeout=30s"

// Error is kubectl failing (not a command it ran in a pod).
type Error struct {
	Args   []string
	Code   int
	Stderr string
}

func (e *Error) Error() string {
	verb := strings.Join(e.Args[:min(2, len(e.Args))], " ")
	return fmt.Sprintf("kubectl %s: exit %d: %s", verb, e.Code, strings.TrimSpace(e.Stderr))
}

// NotFound reports a kubectl "not found" answer.
func NotFound(err error) bool {
	var ke *Error
	return errors.As(err, &ke) && strings.Contains(ke.Stderr, "NotFound")
}

// Kubectl runs one kubectl invocation (Global flags first); a nonzero exit is
// an *Error carrying stderr.
func (c *Client) Kubectl(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
	full := append(c.Global(), args...)
	out, errb, code, err := c.runner().Run(ctx, c.bin(), full, stdin, nil)
	if err != nil {
		return nil, fmt.Errorf("kubectl %s: %w", strings.Join(args[:min(2, len(args))], " "), err)
	}
	if code != 0 {
		return nil, &Error{Args: args, Code: code, Stderr: string(errb)}
	}
	return out, nil
}

// Apply server-side-applies obj (any JSON-marshallable manifest). Mutable
// objects only: apothecary owns every field it sets (field manager
// "apothecary"), so conflicts are forced.
func (c *Client) Apply(ctx context.Context, obj any) error {
	b, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	_, err = c.Kubectl(ctx, []string{"apply", requestTimeout, "--server-side", "--field-manager=apothecary", "--force-conflicts", "-f", "-"}, b)
	return err
}

// Create creates obj; one that already exists is left as it is (pods and
// claims are immutable — a changed one must be deleted first, on purpose).
func (c *Client) Create(ctx context.Context, obj any) error {
	b, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	_, err = c.Kubectl(ctx, []string{"create", requestTimeout, "-f", "-"}, b)
	var ke *Error
	if errors.As(err, &ke) && strings.Contains(ke.Stderr, "AlreadyExists") {
		return nil
	}
	return err
}

// Get reads one object into out; found=false (and no error) when it does not
// exist. Any other failure is an error — never read as "absent".
func (c *Client) Get(ctx context.Context, kind, name string, out any) (bool, error) {
	b, err := c.Kubectl(ctx, []string{"get", requestTimeout, kind, name, "-o", "json", "--ignore-not-found"}, nil)
	if err != nil {
		return false, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return false, nil
	}
	return true, json.Unmarshal(b, out)
}

// List reads the objects of kind matching selector ("" = all) into out (a
// List-shaped struct, {"items": [...]}).
func (c *Client) List(ctx context.Context, kind, selector string, out any) error {
	args := []string{"get", requestTimeout, kind, "-o", "json"}
	if selector != "" {
		args = append(args, "-l", selector)
	}
	b, err := c.Kubectl(ctx, args, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// Delete removes one object; absent is fine. wait blocks until it is gone
// (a claim cannot be deleted while a pod still mounts it).
func (c *Client) Delete(ctx context.Context, kind, name string, wait bool) error {
	args := []string{"delete", kind, name, "--ignore-not-found", fmt.Sprintf("--wait=%t", wait)}
	if wait {
		args = append(args, "--timeout=120s")
	} else {
		args = append(args, requestTimeout)
	}
	_, err := c.Kubectl(ctx, args, nil)
	return err
}

// DeleteSelected removes every object of the kinds matching selector.
func (c *Client) DeleteSelected(ctx context.Context, kinds, selector string) error {
	_, err := c.Kubectl(ctx, []string{"delete", kinds, "-l", selector, "--ignore-not-found", "--wait=false", requestTimeout}, nil)
	return err
}

// WaitReady blocks until the pod reports Ready.
func (c *Client) WaitReady(ctx context.Context, pod string, timeout time.Duration) error {
	_, err := c.Kubectl(ctx, []string{"wait", "--for=condition=Ready", "pod/" + pod, fmt.Sprintf("--timeout=%ds", int(timeout.Seconds()))}, nil)
	return err
}

// RolloutStatus blocks until a deployment's rollout completes.
func (c *Client) RolloutStatus(ctx context.Context, deployment string, timeout time.Duration) error {
	_, err := c.Kubectl(ctx, []string{"rollout", "status", "deployment/" + deployment, fmt.Sprintf("--timeout=%ds", int(timeout.Seconds()))}, nil)
	return err
}

// CanI reports whether the configured identity may do verb on resource.
// kubectl answers "yes" (exit 0) or "no" (exit 1) on stdout; anything on
// stderr is a real failure.
func (c *Client) CanI(ctx context.Context, verb, resource string) (bool, error) {
	args := append(c.Global(), "auth", "can-i", verb, resource)
	out, errb, code, err := c.runner().Run(ctx, c.bin(), args, nil, nil)
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(string(out)) {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	}
	return false, &Error{Args: []string{"auth", "can-i"}, Code: code, Stderr: string(errb)}
}

// Version is the server's version (a reachability probe).
func (c *Client) Version(ctx context.Context) (string, error) {
	out, err := c.Kubectl(ctx, []string{"version", requestTimeout, "-o", "json"}, nil)
	if err != nil {
		return "", err
	}
	var v struct {
		ServerVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", err
	}
	if v.ServerVersion.GitVersion == "" {
		return "", errors.New("kubectl version: no server answered")
	}
	return v.ServerVersion.GitVersion, nil
}
