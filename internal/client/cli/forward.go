package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/client/api"
)

const forwardUsage = `usage: apothecary forward <run> [port[:local]]... [--same]
       (no port: every port the run listens on now; --same: each on its own
       number, a port:local the exception)
       apothecary forward status | logs
       apothecary forward stop <run> [port]... | --all`

// cmdForward hands the forwards to the background daemon (starting it when
// none runs) and says where the ports landed; it returns at once, and the
// forwards run until `apothecary forward stop`. Refused, it forwards
// nothing and says the command to run instead.
func cmdForward(ctx context.Context, args []string, env Env, p *printer) error {
	if len(args) == 0 {
		return errUsage(forwardUsage)
	}
	switch args[0] {
	case "status":
		return forwardStatus(ctx, env, p)
	case "stop":
		return forwardStop(ctx, args[1:], env, p)
	case "logs":
		return forwardLogs(env, p)
	}
	req, err := parseForward(args)
	if err != nil {
		return err
	}
	if _, _, err := signedIn(env); err != nil {
		return err
	}
	hc, err := ensureDaemon(ctx, env)
	if err != nil {
		return err
	}
	var st runState
	if err := daemonCall(ctx, hc, "POST", "/forward", req, &st); err != nil {
		return err
	}
	if len(st.Notes) > 0 {
		for _, n := range st.Notes {
			p.fail("%s", n)
		}
		if len(st.Taken) > 0 {
			p.say("These local ports are free now; to forward there instead:\n  %s", retryCommand(args, st.Taken))
		}
		return errors.New("nothing was forwarded")
	}
	printRun(p, st)
	p.say("Forwarding in the background. apothecary forward status lists it; apothecary forward stop %s ends it.", runRef(st.Workspace, st.ID))
	return nil
}

// parseForward reads `<run> [port[:local]]... [--same]`, a port at most
// once.
func parseForward(args []string) (forwardRequest, error) {
	var req forwardRequest
	seen := map[int]bool{}
	for _, a := range args {
		switch {
		case a == "--same" || a == "-same":
			req.Same = true
		case strings.HasPrefix(a, "-"):
			return req, errUsage(fmt.Sprintf("unknown flag %s\n%s", a, forwardUsage))
		case req.Run == "":
			req.Run = a
		default:
			m, err := parseMapping(a)
			if err != nil {
				return req, err
			}
			if seen[m.Remote] {
				return req, errUsage(fmt.Sprintf("port %d is named twice", m.Remote))
			}
			seen[m.Remote] = true
			req.Ports = append(req.Ports, m)
		}
	}
	if req.Run == "" {
		return req, errUsage(forwardUsage)
	}
	return req, nil
}

func parseMapping(a string) (mapping, error) {
	remote, local, found := strings.Cut(a, ":")
	r, err := strconv.Atoi(remote)
	l := 0
	if found {
		l, err = strconv.Atoi(local)
		if l < 1 {
			err = errors.New("bad local port")
		}
	}
	if err != nil || r < 1 || r > 65535 || l < 0 || l > 65535 {
		return mapping{}, errUsage(fmt.Sprintf("%q is not a port or port:local (1-65535)", a))
	}
	return mapping{Remote: r, Local: l}, nil
}

// retryCommand is the command typed (args, after `forward`) with each
// taken port on its offered local port: a port typed is rewritten in
// place, one --same chose is added. 0 offered (this machine has no free
// port at all): the bare port.
func retryCommand(args []string, taken []mapping) string {
	offer := map[int]int{}
	for _, m := range taken {
		offer[m.Remote] = m.Local
	}
	out := []string{"apothecary", "forward"}
	run := false
	for _, a := range args {
		if m, err := parseMapping(a); err == nil && run && !strings.HasPrefix(a, "-") {
			if local, ok := offer[m.Remote]; ok {
				a = portArg(m.Remote, local)
				delete(offer, m.Remote)
			}
		} else if !strings.HasPrefix(a, "-") {
			run = true
		}
		out = append(out, a)
	}
	for _, m := range taken {
		if local, ok := offer[m.Remote]; ok {
			out = append(out, portArg(m.Remote, local))
		}
	}
	return strings.Join(out, " ")
}

func portArg(remote, local int) string {
	if local == 0 {
		return strconv.Itoa(remote)
	}
	return fmt.Sprintf("%d:%d", remote, local)
}

func printRun(p *printer, st runState) {
	head := st.Label
	if st.Workspace != "" {
		head += "   " + st.Workspace
	}
	p.say("%s   (run %s)", head, shortID(st.ID))
	for _, port := range st.Ports {
		activity := "idle"
		if port.Active == 1 {
			activity = "1 connection"
		} else if port.Active > 1 {
			activity = fmt.Sprintf("%d connections", port.Active)
		}
		p.say("  %-5d → localhost:%-5d   %-14s since %s", port.Remote, port.Local, activity, port.Since.Local().Format("15:04"))
	}
}

// shortID is a run id a person can read: its head and tail.
func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:4] + "…" + id[len(id)-4:]
}

func forwardStatus(ctx context.Context, env Env, p *printer) error {
	hc, err := runningDaemon(ctx, env)
	if err != nil {
		return err
	}
	var runs []runState
	if hc != nil {
		if err := daemonCall(ctx, hc, "GET", "/status", nil, &runs); err != nil {
			return err
		}
	}
	if len(runs) == 0 {
		p.say("Nothing is forwarded.")
		return nil
	}
	for _, st := range runs {
		printRun(p, st)
	}
	return nil
}

func forwardStop(ctx context.Context, args []string, env Env, p *printer) error {
	var req stopRequest
	for _, a := range args {
		switch {
		case a == "--all" || a == "-all":
			req.All = true
		case strings.HasPrefix(a, "-"):
			return errUsage(forwardUsage)
		case req.Run == "":
			req.Run = a
		default:
			port, err := strconv.Atoi(a)
			if err != nil || port < 1 || port > 65535 {
				return errUsage(fmt.Sprintf("%q is not a port (1-65535)", a))
			}
			req.Ports = append(req.Ports, port)
		}
	}
	if !req.All && req.Run == "" || req.All && req.Run != "" {
		return errUsage(forwardUsage)
	}
	hc, err := runningDaemon(ctx, env)
	if err != nil {
		return err
	}
	if hc == nil {
		p.say("Nothing is forwarded.")
		return nil
	}
	var left []runState
	if err := daemonCall(ctx, hc, "POST", "/stop", req, &left); err != nil {
		return err
	}
	switch {
	case req.All:
		p.say("Stopped every forward.")
	case len(req.Ports) > 0:
		p.say("Stopped forwarding port %s of %s.", joinInts(req.Ports), req.Run)
	default:
		p.say("Stopped forwarding %s.", req.Run)
	}
	if len(left) == 0 {
		p.say("Nothing else is forwarded.")
	}
	return nil
}

func joinInts(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ", ")
}

// forwardLogs prints the end of the daemon's log.
func forwardLogs(env Env, p *printer) error {
	dir, err := forwardDir(env)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, "daemon.log"))
	if errors.Is(err, os.ErrNotExist) {
		p.say("No log yet: nothing was forwarded on this machine.")
		return nil
	}
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > 100 {
		lines = lines[len(lines)-100:]
	}
	p.say("%s", strings.Join(lines, "\n"))
	return nil
}

// daemonHTTP talks HTTP to the daemon over its socket.
func daemonHTTP(sock string) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}},
	}
}

func daemonSocket(env Env) (string, error) {
	dir, err := forwardDir(env)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.sock"), nil
}

// daemonStatus asks the daemon on sock what it forwards: the way to tell
// whether one runs.
func daemonStatus(ctx context.Context, sock string) ([]runState, error) {
	var runs []runState
	err := daemonCall(ctx, daemonHTTP(sock), "GET", "/status", nil, &runs)
	return runs, err
}

// runningDaemon is the daemon when one runs, nil when none does.
func runningDaemon(ctx context.Context, env Env) (*http.Client, error) {
	sock, err := daemonSocket(env)
	if err != nil {
		return nil, err
	}
	if _, err := daemonStatus(ctx, sock); err != nil {
		return nil, nil
	}
	return daemonHTTP(sock), nil
}

// daemonStart is how long a starting daemon may take to answer.
var daemonStart = 5 * time.Second

// ensureDaemon is the running daemon, started when there is none.
func ensureDaemon(ctx context.Context, env Env) (*http.Client, error) {
	if hc, err := runningDaemon(ctx, env); err != nil || hc != nil {
		return hc, err
	}
	if env.SpawnDaemon == nil {
		return nil, errors.New("cannot start the forwarder here")
	}
	if err := env.SpawnDaemon(); err != nil {
		return nil, fmt.Errorf("start the forwarder: %w", err)
	}
	deadline := time.Now().Add(daemonStart)
	for {
		if hc, _ := runningDaemon(ctx, env); hc != nil {
			return hc, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("the forwarder did not start: apothecary forward logs says why")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// daemonCall is one request to the daemon; its refusals come back as
// errors in its words, an engine's status kept so "sign in again" still
// reads right.
func daemonCall(ctx context.Context, hc *http.Client, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://forwarder"+path, rd)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return &api.Error{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// spawnDaemon starts the daemon from this binary, detached from the
// terminal (its own session or process group), its output appended to the
// log.
func spawnDaemon(env Env) error {
	dir, err := forwardDir(env)
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(dir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := daemonCmd(env.Executable)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func isNotFound(err error) bool {
	var e *api.Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}
