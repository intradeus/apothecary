package main

// shim service — the workspace's supervisor for the project's services (its
// app, a database: confstore ServiceConfig), driven by the engine through
// Exec and read back through the sidecar:
//
//	service start [--restart] [--wait]   request JSON on stdin; prints the state
//	service stop                         request JSON on stdin; prints the state
//	service status [<name>...]           a JSON array of states
//	service logs [--tail N] <name>       the last N lines of its output
//	service supervise <name>             the detached supervisor (internal)
//
// start detaches one supervisor per service — its own session, so neither the
// exec that started it nor an engine restart takes it down; it lives as long
// as the service does, and the container. The supervisor runs the start
// script in the checkout with the exec's environment, writes its output to a
// log, probes health and records everything in a state file. A start that
// keeps running is the service; one that exits 0 started something in the
// background (`docker compose up -d`) and health alone says whether it is up.
// stop asks the supervisor, which runs the stop script and then signals what
// is left of the start's process group, TERM then KILL.
//
// Everything lives in servicesDir/<name>/: spec.json (what was started),
// state.json (written by the supervisor while it lives, else by the command
// that found it gone), output.log, and lock (start and stop of one service
// never interleave).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Service phases, as state.json records them.
const (
	svcStarting  = "starting"
	svcHealthy   = "healthy"
	svcUnhealthy = "unhealthy"
	svcExited    = "exited" // the start failed, or the supervisor is gone
	svcStopping  = "stopping"
	svcStopped   = "stopped"
)

// Supervisor knobs — vars so tests can compress time and move the state.
var (
	servicesDir = envOr("APOTHECARY_SERVICES", "/apothecary/services")
	// startingProbeEvery is how often a starting service is probed: often,
	// so "healthy" is noticed within a second of being true.
	startingProbeEvery = time.Second
	// noProbeGrace: a service with no probe is up once its start has run
	// this long without failing (or exited 0).
	noProbeGrace = time.Second
	// unhealthyAfter consecutive failed probes turn a healthy service
	// unhealthy: one slow answer is not an outage.
	unhealthyAfter  = 3
	stopGrace       = 10 * time.Second
	stopTimeout     = 2 * time.Minute
	defaultTimeout  = 5 * time.Minute
	defaultInterval = 10 * time.Second
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var serviceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// serviceRequest is what the engine sends start and stop on stdin: the
// service as the project declares it, and who is asking.
type serviceRequest struct {
	Name   string        `json:"name"`
	Start  string        `json:"start"`
	Stop   string        `json:"stop,omitempty"`
	Dir    string        `json:"dir,omitempty"` // default: the command's cwd
	Health serviceHealth `json:"health"`
	// Timeout is how long this start may take to be healthy; Interval how
	// often a started one is probed. Durations; empty = the defaults.
	Timeout  string `json:"timeout,omitempty"`
	Interval string `json:"interval,omitempty"`
	// By is who started it (stage|human), Owner the
	// chain (phase) whose stage did, StageID that stage.
	By      string `json:"by,omitempty"`
	Owner   string `json:"owner,omitempty"`
	StageID string `json:"stage_id,omitempty"`
}

type serviceHealth struct {
	HTTP    string `json:"http,omitempty"`
	TCP     int    `json:"tcp,omitempty"`
	Command string `json:"command,omitempty"`
}

func (h serviceHealth) none() bool { return h.HTTP == "" && h.TCP == 0 && h.Command == "" }

// serviceState is state.json, and what every subcommand prints.
type serviceState struct {
	Name  string `json:"name"`
	Phase string `json:"phase"`
	// PID is the supervisor's; PGID the start script's process group.
	PID     int    `json:"pid,omitempty"`
	PGID    int    `json:"pgid,omitempty"`
	By      string `json:"by,omitempty"`
	Owner   string `json:"owner,omitempty"`
	StageID string `json:"stage_id,omitempty"`

	StartedAt *time.Time `json:"started_at,omitempty"`
	// Deadline is when a starting service turns unhealthy.
	Deadline  *time.Time `json:"deadline,omitempty"`
	HealthyAt *time.Time `json:"healthy_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	// Daemonized: the start exited 0, leaving the service in the background.
	Daemonized  bool       `json:"daemonized,omitempty"`
	LastProbeAt *time.Time `json:"last_probe_at,omitempty"`
	LastProbeOK bool       `json:"last_probe_ok,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	// Launched is start's answer only: this call started it (false: it was
	// already up, or starting).
	Launched bool `json:"launched,omitempty"`
}

// live reports whether the phase is one a supervisor holds.
func (s *serviceState) live() bool {
	switch s.Phase {
	case svcStarting, svcHealthy, svcUnhealthy, svcStopping:
		return true
	}
	return false
}

func runService(args []string) {
	if len(args) == 0 {
		serviceUsage()
	}
	switch args[0] {
	case "start":
		fs := flag.NewFlagSet("service start", flag.ExitOnError)
		restart := fs.Bool("restart", false, "stop it first when it runs")
		wait := fs.Bool("wait", false, "return once it is healthy, failed, or past its timeout")
		_ = fs.Parse(args[1:])
		req := readServiceRequest()
		st, err := startService(req, *restart, *wait)
		exitService(st, err)
	case "stop":
		req := readServiceRequest()
		st, err := stopService(req)
		exitService(st, err)
	case "status":
		out, err := serviceStatus(args[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "service:", err)
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(out)
	case "logs":
		fs := flag.NewFlagSet("service logs", flag.ExitOnError)
		n := fs.Int("tail", 200, "lines")
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 {
			serviceUsage()
		}
		if err := serviceLogs(os.Stdout, fs.Arg(0), *n); err != nil {
			fmt.Fprintln(os.Stderr, "service:", err)
			os.Exit(1)
		}
	case "supervise":
		if len(args) != 2 {
			serviceUsage()
		}
		os.Exit(supervise(args[1]))
	default:
		serviceUsage()
	}
}

func serviceUsage() {
	fmt.Fprintln(os.Stderr, "usage: shim service start [--restart] [--wait] | stop | status [<name>...] | logs [--tail N] <name>  (start/stop read the service as JSON on stdin)")
	os.Exit(2)
}

func readServiceRequest() serviceRequest {
	var req serviceRequest
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&req); err != nil {
		fmt.Fprintln(os.Stderr, "service: reading the service from stdin:", err)
		os.Exit(2)
	}
	return req
}

func exitService(st serviceState, err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "service:", err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(st)
}

func serviceDir(name string) string { return filepath.Join(servicesDir, name) }

// lockService serializes start and stop of one service.
func lockService(name string) (func(), error) {
	if err := os.MkdirAll(serviceDir(name), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(serviceDir(name), "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func loadServiceState(name string) (serviceState, bool) {
	var st serviceState
	data, err := os.ReadFile(filepath.Join(serviceDir(name), "state.json"))
	if err != nil || json.Unmarshal(data, &st) != nil {
		return serviceState{Name: name, Phase: svcStopped}, false
	}
	return st, true
}

// saveServiceState writes state.json whole (tmp + rename): a reader never
// sees half of one.
func saveServiceState(st serviceState) error {
	st.Launched = false
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	dir := serviceDir(st.Name)
	tmp, err := os.CreateTemp(dir, "state-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	_ = os.Chmod(tmp.Name(), 0o644)
	return os.Rename(tmp.Name(), filepath.Join(dir, "state.json"))
}

// observed is the state as it stands: a live phase whose supervisor is gone
// (killed, or the state outlived a container restart) is over.
func observed(st serviceState) serviceState {
	if st.live() && !supervisorAlive(st) {
		st.Phase = svcExited
		st.LastError = "its supervisor is gone"
	}
	return st
}

// supervisorAlive checks the recorded pid is still this service's
// supervisor — not a zombie, not a recycled pid.
func supervisorAlive(st serviceState) bool {
	if st.PID <= 0 {
		return false
	}
	proc := filepath.Join(procRoot, strconv.Itoa(st.PID))
	if stat, err := os.ReadFile(filepath.Join(proc, "stat")); err == nil {
		if i := strings.LastIndexByte(string(stat), ')'); i >= 0 && i+2 < len(stat) {
			if s := stat[i+2]; s == 'Z' || s == 'X' {
				return false
			}
		}
		if cmdline, err := os.ReadFile(filepath.Join(proc, "cmdline")); err == nil {
			return strings.Contains(string(cmdline), "service\x00supervise\x00"+st.Name+"\x00")
		}
	}
	err := syscall.Kill(st.PID, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func groupAlive(pgid int) bool {
	return pgid > 0 && syscall.Kill(-pgid, 0) == nil
}

func validRequest(req *serviceRequest) error {
	if !serviceNameRe.MatchString(req.Name) {
		return fmt.Errorf("service name %q is not a slug", req.Name)
	}
	if req.Dir == "" {
		req.Dir, _ = os.Getwd()
	}
	for _, d := range []string{req.Timeout, req.Interval} {
		if d == "" {
			continue
		}
		if v, err := time.ParseDuration(d); err != nil || v <= 0 {
			return fmt.Errorf("bad duration %q", d)
		}
	}
	return nil
}

func durationOr(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

// startService starts the service unless it is already up or on its way:
// a healthy or starting service is left alone, an unhealthy one restarted
// (start means "make it up"), and --restart stops whatever runs first.
func startService(req serviceRequest, restart, wait bool) (serviceState, error) {
	if err := validRequest(&req); err != nil {
		return serviceState{}, err
	}
	if strings.TrimSpace(req.Start) == "" {
		return serviceState{}, fmt.Errorf("service %s has no start script", req.Name)
	}
	unlock, err := lockService(req.Name)
	if err != nil {
		return serviceState{}, err
	}
	cur, _ := loadServiceState(req.Name)
	cur = observed(cur)
	launched := false
	if cur.live() && !restart && cur.Phase != svcUnhealthy && cur.Phase != svcStopping {
		unlock()
	} else {
		if cur.live() {
			if _, err := stopLocked(req, cur); err != nil {
				unlock()
				return serviceState{}, err
			}
		}
		err := launch(req)
		unlock()
		if err != nil {
			return serviceState{}, err
		}
		launched = true
	}
	st, _ := loadServiceState(req.Name)
	st = observed(st)
	if wait {
		st = waitSettled(req.Name, durationOr(req.Timeout, defaultTimeout))
	}
	st.Launched = launched
	return st, nil
}

// launch records the start and detaches its supervisor (lock held).
func launch(req serviceRequest) error {
	dir := serviceDir(req.Name)
	spec, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), spec, 0o644); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(dir, "output.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	who := req.By
	if req.StageID != "" {
		who += " " + req.StageID
	}
	fmt.Fprintf(logf, "[apothecary] %s starting %s (%s)\n", time.Now().UTC().Format(time.RFC3339), req.Name, strings.TrimSpace(who))
	deadline := time.Now().Add(durationOr(req.Timeout, defaultTimeout)).UTC()
	if err := saveServiceState(serviceState{Name: req.Name, Phase: svcStarting, By: req.By,
		Owner: req.Owner, StageID: req.StageID, Deadline: &deadline}); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	cmd := exec.Command(self, "service", "supervise", req.Name)
	cmd.Dir = req.Dir
	cmd.Env = append(serviceEnv(req.Name), "APOTHECARY_SERVICES="+servicesDir)
	// The supervisor's own stderr (a panic, say) lands in the log too.
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the supervisor: %w", err)
	}
	_ = cmd.Process.Release()
	// Back once the supervisor has taken over the state, so a status right
	// after this never reads a supervisor-less "starting" as gone.
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if st, ok := loadServiceState(req.Name); ok && (st.PID != 0 || st.Phase != svcStarting) {
			return nil
		}
	}
	return fmt.Errorf("the supervisor of %s did not start (see its log)", req.Name)
}

// waitSettled waits until the service is no longer starting, or for d.
func waitSettled(name string, d time.Duration) serviceState {
	end := time.Now().Add(d)
	for {
		st, _ := loadServiceState(name)
		st = observed(st)
		if st.Phase != svcStarting || !time.Now().Before(end) {
			return st
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// stopService stops it, whoever started it; stopping a stopped service is
// not an error.
func stopService(req serviceRequest) (serviceState, error) {
	if err := validRequest(&req); err != nil {
		return serviceState{}, err
	}
	unlock, err := lockService(req.Name)
	if err != nil {
		return serviceState{}, err
	}
	defer unlock()
	cur, ok := loadServiceState(req.Name)
	if !ok {
		return serviceState{Name: req.Name, Phase: svcStopped}, nil
	}
	return stopLocked(req, cur)
}

// stopLocked: the supervisor stops what it runs; when it is gone, this
// does its part — the stop script for a service that daemonized, then the
// start's group.
func stopLocked(req serviceRequest, cur serviceState) (serviceState, error) {
	if supervisorAlive(cur) {
		_ = syscall.Kill(cur.PID, syscall.SIGTERM)
		end := time.Now().Add(stopTimeout + 2*stopGrace)
		for supervisorAlive(cur) && time.Now().Before(end) {
			time.Sleep(50 * time.Millisecond)
		}
		if supervisorAlive(cur) {
			_ = syscall.Kill(cur.PID, syscall.SIGKILL)
			if st, _ := loadServiceState(req.Name); st.PGID > 0 {
				_ = syscall.Kill(-st.PGID, syscall.SIGKILL)
			}
		}
		st, _ := loadServiceState(req.Name)
		if st.Phase != svcStopped {
			st.Phase, st.LastError = svcStopped, "its supervisor did not stop in time and was killed"
			now := time.Now().UTC()
			st.EndedAt = &now
			if err := saveServiceState(st); err != nil {
				return st, err
			}
		}
		return st, nil
	}
	if cur.Phase == svcStopped {
		return cur, nil
	}
	stop := req.Stop
	if stop == "" {
		if spec, ok := loadSpec(req.Name); ok {
			stop = spec.Stop
		}
	}
	logf, _ := os.OpenFile(filepath.Join(serviceDir(req.Name), "output.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if logf != nil {
		defer logf.Close()
	}
	if stop != "" && cur.StartedAt != nil {
		runStopScript(stop, req.Dir, req.Name, logf)
	}
	killGroup(cur.PGID, nil)
	now := time.Now().UTC()
	cur.Phase, cur.EndedAt = svcStopped, &now
	return cur, saveServiceState(cur)
}

func loadSpec(name string) (serviceRequest, bool) {
	var spec serviceRequest
	data, err := os.ReadFile(filepath.Join(serviceDir(name), "spec.json"))
	if err != nil || json.Unmarshal(data, &spec) != nil {
		return spec, false
	}
	return spec, true
}

// serviceEnv is the exec's environment — the user's, as the workspace
// gives it — without the command-stage marker (a later attempt of the stage
// that started a service must not sweep it away), naming the service.
func serviceEnv(name string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "APOTHECARY_CMD=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "APOTHECARY_SERVICE="+name)
}

// shellCommand runs a script the way command stages do: bash -eo pipefail,
// or sh -e in an image without bash.
func shellCommand(ctx context.Context, script string) *exec.Cmd {
	if _, err := exec.LookPath("bash"); err == nil {
		return exec.CommandContext(ctx, "bash", "-eo", "pipefail", "-c", script)
	}
	return exec.CommandContext(ctx, "sh", "-ec", script)
}

func runStopScript(script, dir, name string, logf io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	cmd := shellCommand(ctx, script)
	cmd.Dir, cmd.Env = dir, serviceEnv(name)
	if logf != nil {
		fmt.Fprintf(logf, "[apothecary] %s stopping %s: running its stop script\n", time.Now().UTC().Format(time.RFC3339), name)
		cmd.Stdout, cmd.Stderr = logf, logf
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	if err := cmd.Run(); err != nil && logf != nil {
		fmt.Fprintf(logf, "[apothecary] the stop script failed: %v\n", err)
	}
}

// killGroup signals what is left of a process group, TERM then KILL after
// stopGrace. exited, when given, is closed once the group's leader is
// reaped (the supervisor's own child), which ends the grace early.
func killGroup(pgid int, exited <-chan struct{}) {
	if !groupAlive(pgid) {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	end := time.Now().Add(stopGrace)
	for groupAlive(pgid) && time.Now().Before(end) {
		select {
		case <-exited:
			exited = nil
		case <-time.After(50 * time.Millisecond):
		}
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// supervise is the detached supervisor: it runs the start script, probes
// its health and keeps state.json, until the start fails or it is told to
// stop (SIGTERM).
func supervise(name string) int {
	spec, ok := loadSpec(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "[apothecary] supervisor: no spec for %s\n", name)
		return 1
	}
	st, _ := loadServiceState(name)
	st.Name, st.PID = name, os.Getpid()
	signal.Ignore(syscall.SIGHUP)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)

	logf, err := os.OpenFile(filepath.Join(serviceDir(name), "output.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		logf, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
	defer logf.Close()
	note := func(format string, args ...any) {
		fmt.Fprintf(logf, "[apothecary] %s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
	}
	save := func() { _ = saveServiceState(st) }

	cmd := shellCommand(context.Background(), spec.Start)
	cmd.Dir, cmd.Env = spec.Dir, serviceEnv(name)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	now := time.Now().UTC()
	st.StartedAt = &now
	if err := cmd.Start(); err != nil {
		st.Phase, st.LastError, st.EndedAt = svcExited, "start: "+err.Error(), &now
		save()
		return 1
	}
	st.PGID = cmd.Process.Pid
	save()
	exitCh := make(chan int, 1)
	reaped := make(chan struct{})
	go func() {
		err := cmd.Wait()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			code = -1
		}
		close(reaped)
		exitCh <- code
	}()

	timeout := durationOr(spec.Timeout, defaultTimeout)
	interval := durationOr(spec.Interval, defaultInterval)
	deadline := now.Add(timeout)
	st.Deadline = &deadline
	save()
	noProbe := spec.Health.none()
	first := startingProbeEvery
	if noProbe {
		first = noProbeGrace
	}
	timer := time.NewTimer(first)
	defer timer.Stop()
	running, fails := true, 0
	healthy := func() {
		t := time.Now().UTC()
		st.Phase, st.HealthyAt, st.LastError = svcHealthy, &t, ""
		note("%s is healthy", name)
	}
	for {
		select {
		case code := <-exitCh:
			running = false
			t := time.Now().UTC()
			st.ExitCode = &code
			if code != 0 {
				st.Phase, st.EndedAt = svcExited, &t
				st.LastError = fmt.Sprintf("its start exited with %d", code)
				note("%s: its start exited with %d", name, code)
				save()
				return 0
			}
			st.Daemonized = true
			note("%s: its start exited 0; the service runs in the background", name)
			if noProbe && st.Phase == svcStarting {
				healthy()
			}
			save()
		case <-timer.C:
			next := interval
			switch {
			case noProbe:
				if st.Phase == svcStarting {
					healthy()
					save()
				}
				continue // nothing to probe: the timer stays stopped
			default:
				ok, perr := probeService(spec)
				t := time.Now().UTC()
				st.LastProbeAt, st.LastProbeOK = &t, ok
				switch st.Phase {
				case svcStarting:
					switch {
					case ok:
						healthy()
					case !t.Before(deadline):
						st.Phase = svcUnhealthy
						st.LastError = fmt.Sprintf("not healthy within %s: %v", timeout, perr)
						note("%s is not healthy within %s: %v", name, timeout, perr)
					default:
						next = startingProbeEvery
						if left := deadline.Sub(t); left < next {
							next = left
						}
					}
				case svcHealthy:
					if ok {
						fails = 0
					} else if fails++; fails >= unhealthyAfter {
						st.Phase, st.LastError = svcUnhealthy, perr.Error()
						note("%s is unhealthy: %v", name, perr)
					}
				case svcUnhealthy:
					if ok {
						fails = 0
						healthy()
					} else {
						st.LastError = perr.Error()
					}
				}
				save()
			}
			timer.Reset(next)
		case <-sigs:
			st.Phase = svcStopping
			save()
			note("stopping %s", name)
			if spec.Stop != "" {
				runStopScript(spec.Stop, spec.Dir, name, logf)
			}
			if running {
				killGroup(st.PGID, reaped)
			} else {
				killGroup(st.PGID, nil)
			}
			t := time.Now().UTC()
			st.Phase, st.EndedAt = svcStopped, &t
			note("%s stopped", name)
			save()
			return 0
		}
	}
}

// probeService runs the service's health probe once.
func probeService(spec serviceRequest) (bool, error) {
	h := spec.Health
	switch {
	case h.HTTP != "":
		c := &http.Client{Timeout: 5 * time.Second,
			// A redirect is an answer: the app is up, wherever it points.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := c.Get(h.HTTP)
		if err != nil {
			return false, err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 400 {
			return false, fmt.Errorf("GET %s: %s", h.HTTP, res.Status)
		}
		return true, nil
	case h.TCP != 0:
		c, err := net.DialTimeout("tcp", net.JoinHostPort("localhost", strconv.Itoa(h.TCP)), 2*time.Second)
		if err != nil {
			return false, err
		}
		c.Close()
		return true, nil
	case h.Command != "":
		d := durationOr(spec.Interval, defaultInterval)
		if d > 30*time.Second {
			d = 30 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		cmd := shellCommand(ctx, h.Command)
		cmd.Dir, cmd.Env = spec.Dir, serviceEnv(spec.Name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if len(msg) > 512 {
				msg = msg[len(msg)-512:]
			}
			if msg != "" {
				return false, fmt.Errorf("health command: %v: %s", err, msg)
			}
			return false, fmt.Errorf("health command: %v", err)
		}
		return true, nil
	}
	return true, nil
}

// serviceStatus is every service's state (or the named ones'), as observed.
func serviceStatus(names []string) ([]serviceState, error) {
	if len(names) == 0 {
		ents, err := os.ReadDir(servicesDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, e := range ents {
			if e.IsDir() && serviceNameRe.MatchString(e.Name()) {
				names = append(names, e.Name())
			}
		}
	}
	out := []serviceState{}
	for _, n := range names {
		if !serviceNameRe.MatchString(n) {
			return nil, fmt.Errorf("service name %q is not a slug", n)
		}
		st, _ := loadServiceState(n)
		out = append(out, observed(st))
	}
	return out, nil
}

// serviceLogs writes the last n lines of the service's output.
func serviceLogs(w io.Writer, name string, n int) error {
	if !serviceNameRe.MatchString(name) {
		return fmt.Errorf("service name %q is not a slug", name)
	}
	f, err := os.Open(filepath.Join(serviceDir(name), "output.log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.WriteString(w, tailLines(f, n))
	return err
}

// tailLines reads the last n lines of a file without reading all of it:
// the last 1 MiB is plenty for any tail worth showing.
func tailLines(f *os.File, n int) string {
	if n <= 0 {
		n = 200
	}
	const window = 1 << 20
	if info, err := f.Stat(); err == nil && info.Size() > window {
		_, _ = f.Seek(info.Size()-window, io.SeekStart)
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// serveServiceStatus is the sidecar's GET /services: what `service status`
// prints, read without an exec — the run page polls it.
func serveServiceStatus(w http.ResponseWriter, r *http.Request) {
	out, err := serviceStatus(nil)
	if err != nil {
		writeErrResp(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONResp(w, http.StatusOK, out)
}

// serveServiceLog is the sidecar's GET /services/{name}/log?tail=N.
func serveServiceLog(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := serviceLogs(w, r.PathValue("name"), n); err != nil {
		writeErrResp(w, http.StatusBadRequest, err.Error())
	}
}
