package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"apothecary/internal/client/api"
	"apothecary/internal/platform/wstunnel"
)

// The forwards live in one background process per person, the daemon, so
// they outlive the terminal that asked for them: closing it, or killing it,
// leaves them running until `apothecary forward stop`. The commands reach
// the daemon over a unix socket in the person's own config directory (0700),
// with plain HTTP+JSON on it. The daemon exits once nothing is forwarded.

// daemonCommand is the hidden command the daemon runs as.
const daemonCommand = "__forward-daemon"

// daemonIdle is how long a daemon with nothing forwarded stays up: the
// command that started it is about to ask for something.
var daemonIdle = 30 * time.Second

// daemonShutdownGrace bounds how long a daemon told to stop waits for the
// requests in flight (the stop's own answer among them) before it closes.
const daemonShutdownGrace = 5 * time.Second

// listenLocal opens a port on this machine's loopback, never on its other
// interfaces: what a run serves stays the person's. Port 0 is a free one;
// a named port that something on this machine already answers on is
// taken, even when the bind alone would succeed. A var for tests.
var listenLocal = func(port int) (net.Listener, error) {
	if port > 0 && localAnswers(port) {
		return nil, fmt.Errorf("localhost:%d already answers", port)
	}
	return net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

// localProbe is how long a loopback dial may take: a listener accepts at
// once, and a closed port refuses at once everywhere but Windows, which
// retries for seconds.
const localProbe = 200 * time.Millisecond

// localAnswers says whether something accepts connections on port at either
// loopback address. The bind cannot tell: macOS and Windows let 127.0.0.1:p
// be bound beside another process's 0.0.0.0:p or [::]:p, and that process
// still answers [::1]:p — where a client resolving localhost to IPv6 lands.
func localAnswers(port int) bool {
	for _, host := range []string{"127.0.0.1", "::1"} {
		if c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), localProbe); err == nil {
			c.Close()
			return true
		}
	}
	return false
}

// forwardDir holds the daemon's socket, its log and the remembered local
// ports.
func forwardDir(env Env) (string, error) {
	if env.ConfigDir == "" {
		return "", errors.New("no configuration directory on this machine")
	}
	dir := filepath.Join(env.ConfigDir, "apothecary", "forwards")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// The wire between the commands and the daemon.
type (
	// mapping is one port asked for: the run's port, and a local one
	// (0: a free one).
	mapping struct {
		Remote int `json:"remote"`
		Local  int `json:"local,omitempty"`
	}
	// forwardRequest is one forward, done whole or not at all. Ports with
	// no local port pick which of the run's ports are forwarded; none
	// picked, every port it listens on now. With Same each lands on its own
	// number, except a port:local, which is an exception to Same rather
	// than a pick (`--same 5000:5001`: every port, 5000 on 5001).
	forwardRequest struct {
		Run   string    `json:"run"`
		Ports []mapping `json:"ports,omitempty"`
		Same  bool      `json:"same,omitempty"`
	}
	stopRequest struct {
		Run   string `json:"run,omitempty"`
		Ports []int  `json:"ports,omitempty"`
		All   bool   `json:"all,omitempty"`
	}
	portState struct {
		Remote int       `json:"remote"`
		Local  int       `json:"local"`
		Active int       `json:"active"`
		Since  time.Time `json:"since"`
	}
	runState struct {
		ID        string      `json:"id"`
		Label     string      `json:"label"`
		Workspace string      `json:"workspace,omitempty"`
		Ports     []portState `json:"ports"`
		// Notes say why a forward was refused (a port taken, a port already
		// forwarded elsewhere): one refused port refuses the whole request,
		// and nothing of it is forwarded.
		Notes []string `json:"notes,omitempty"`
		// Taken are the refused ports whose local port was taken, each with
		// a local port free when the daemon answered (0: none found): what
		// to ask for instead.
		Taken []mapping `json:"taken,omitempty"`
	}
)

// takenError is a port refused because the local port it was to land on —
// named, or its own with --same — is taken.
type takenError struct{ remote, local int }

func (e *takenError) Error() string {
	return fmt.Sprintf("port %d: localhost:%d is taken", e.remote, e.local)
}

// daemon holds the forwards.
type daemon struct {
	env  Env
	log  *log.Logger
	base context.Context

	mu     sync.Mutex
	runs   map[string]*drun
	assign *assignments
	idle   *time.Timer
	quit   chan struct{}
	once   sync.Once
}

// drun is one run's forwards.
type drun struct {
	info   api.Run
	ports  map[int]*dport
	cancel context.CancelFunc // ends its connections' dials
	ctx    context.Context
}

type dport struct {
	remote, local int
	ln            net.Listener
	free          bool // the daemon chose the local port: remembered
	active        atomic.Int32
	since         time.Time
}

func newDaemon(ctx context.Context, env Env, logger *log.Logger, assign *assignments) *daemon {
	d := &daemon{env: env, log: logger, base: ctx, runs: map[string]*drun{}, assign: assign, quit: make(chan struct{})}
	d.idle = time.AfterFunc(daemonIdle, d.exitIfIdle)
	return d
}

// runDaemon is the daemon's process: it serves the socket until nothing is
// forwarded, or it is told to stop.
func runDaemon(ctx context.Context, env Env) error {
	dir, err := forwardDir(env)
	if err != nil {
		return err
	}
	// Its own log file: what `forward logs` shows. A crash's output lands
	// there too, the spawn having pointed stdout and stderr at it.
	logf, err := os.OpenFile(filepath.Join(dir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	logger := log.New(logf, "", log.LstdFlags)
	sock := filepath.Join(dir, "daemon.sock")
	// Another daemon answering: this one is not needed.
	if _, err := daemonStatus(ctx, sock); err == nil {
		return nil
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", sock, err)
	}
	_ = os.Chmod(sock, 0o600)
	d := newDaemon(ctx, env, logger, loadAssignments(filepath.Join(dir, "ports.json")))
	srv := &http.Server{Handler: d.handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		select {
		case <-d.quit:
			// Told to stop by a request (the last forward stopped): let that
			// request's answer out before the server goes.
			sctx, cancel := context.WithTimeout(ctx, daemonShutdownGrace)
			defer cancel()
			_ = srv.Shutdown(sctx)
		case <-ctx.Done():
		}
		_ = srv.Close()
	}()
	logger.Printf("forward daemon %s started (pid %d)", env.Version, os.Getpid())
	err = srv.Serve(ln)
	d.closeAll()
	_ = os.Remove(sock)
	logger.Printf("forward daemon stopped")
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (d *daemon) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONTo(w, http.StatusOK, d.snapshot(""))
	})
	mux.HandleFunc("POST /forward", func(w http.ResponseWriter, r *http.Request) {
		var req forwardRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Run == "" {
			http.Error(w, "a run is required", http.StatusBadRequest)
			return
		}
		st, err := d.forward(r.Context(), req)
		if err != nil {
			status := http.StatusBadGateway
			var ae *api.Error
			if errors.As(err, &ae) {
				status = ae.Status
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSONTo(w, http.StatusOK, st)
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		var req stopRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		left, err := d.stop(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSONTo(w, http.StatusOK, left)
	})
	return mux
}

func writeJSONTo(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// client is the engine as the person is signed in now: the config is read
// again each time, so a new login reaches running forwards.
func (d *daemon) client() (*api.Client, error) {
	c, _, err := signedIn(d.env)
	return c, err
}

// forward does what req asks for, whole or not at all, and says where the
// run's ports landed — or, refused, why, with free ports to ask for instead.
func (d *daemon) forward(ctx context.Context, req forwardRequest) (runState, error) {
	c, err := d.client()
	if err != nil {
		return runState{}, err
	}
	info, err := c.FindRun(ctx, req.Run)
	if err != nil {
		return runState{}, err
	}
	want := req.Ports
	if !picks(req.Ports) {
		ports, err := c.Ports(ctx, info.ID)
		if err != nil {
			return runState{}, err
		}
		if want = everyPort(ports.Listening(), req.Ports); len(want) == 0 {
			return runState{}, fmt.Errorf("%s listens on no port yet: forward it once it does", info.Label())
		}
	}
	d.mu.Lock()
	r := d.runs[info.ID]
	if r == nil {
		rctx, cancel := context.WithCancel(d.base)
		r = &drun{info: info, ports: map[int]*dport{}, ctx: rctx, cancel: cancel}
		d.runs[info.ID] = r
	}
	r.info = info
	bound, notes, taken := d.bindAll(r, want, req.Same)
	if len(notes) == 0 {
		for _, p := range bound {
			d.commit(r, p)
		}
		st := r.state()
		d.mu.Unlock()
		d.noteBusy()
		return st, nil
	}
	// Refused: nothing of it stays. The ports it did bind are kept from
	// the offer, the command offered binding them again.
	avoid := map[int]bool{}
	for _, p := range bound {
		p.ln.Close()
		avoid[p.local] = true
	}
	empty := len(r.ports) == 0
	d.mu.Unlock()
	if empty {
		d.drop(info.ID, "nothing forwarded")
	}
	// Outside the lock: a probe may take a while.
	return runState{ID: info.ID, Label: info.Label(), Workspace: info.Workspace, Ports: []portState{}, Notes: notes, Taken: freeInstead(taken, avoid)}, nil
}

// picks says whether ports pick which of the run's ports are forwarded: one
// of them has no local port. Only port:local ones are exceptions to --same.
func picks(ports []mapping) bool {
	for _, m := range ports {
		if m.Local == 0 {
			return true
		}
	}
	return false
}

// everyPort is each port the run listens on, by number, with the
// exceptions asked for (their local port; one the run does not listen on
// is still forwarded: it was named).
func everyPort(listening []api.Port, exceptions []mapping) []mapping {
	local := map[int]int{}
	for _, m := range exceptions {
		local[m.Remote] = m.Local
	}
	var out []mapping
	for _, p := range listening {
		out = append(out, mapping{Remote: p.Port, Local: local[p.Port]})
		delete(local, p.Port)
	}
	for _, m := range exceptions {
		if _, left := local[m.Remote]; left {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Remote < out[j].Remote })
	return out
}

// bindAll binds a local port for each of want the run does not forward
// yet, every one tried so a refusal names them all: the bound ports, why
// the others were refused, and which of those were refused as taken.
// Called with d.mu held.
func (d *daemon) bindAll(r *drun, want []mapping, same bool) (bound []*dport, notes []string, taken []mapping) {
	for _, m := range want {
		asked := m.Local
		if asked == 0 && same {
			asked = m.Remote
		}
		if p := r.ports[m.Remote]; p != nil {
			if asked > 0 && p.local != asked {
				notes = append(notes, fmt.Sprintf("port %d is forwarded to localhost:%d already (apothecary forward stop %s %d first)", m.Remote, p.local, runRef(r.info.Workspace, r.info.ID), m.Remote))
			}
			continue
		}
		p, err := d.bind(r, m.Remote, asked)
		if err != nil {
			notes = append(notes, err.Error())
			var te *takenError
			if errors.As(err, &te) {
				taken = append(taken, mapping{Remote: te.remote, Local: te.local})
			}
			continue
		}
		bound = append(bound, p)
	}
	return bound, notes, taken
}

// runRef is how a person names a run: its workspace, else its id.
func runRef(workspace, id string) string {
	if workspace != "" {
		return workspace
	}
	return id
}

// suggestSpan is how many ports above a taken one are tried before any
// free port is offered.
const suggestSpan = 20

// freeInstead gives each taken port a local port free now: the nearest
// above the one asked for (5432 offers 5433), so the number still reads as
// the service's, else any free one. None is offered twice, nor one in
// avoid. Each is probed and let go: a suggestion, which something may take
// before it is asked for.
func freeInstead(taken []mapping, avoid map[int]bool) []mapping {
	if len(taken) == 0 {
		return nil
	}
	used := map[int]bool{}
	for p := range avoid {
		used[p] = true
	}
	out := make([]mapping, 0, len(taken))
	for _, m := range taken {
		out = append(out, mapping{Remote: m.Remote, Local: freeNear(m.Local, used)})
	}
	return out
}

// freeNear is a free local port near from, not in used (and now in it);
// 0 when even the system has none to give.
func freeNear(from int, used map[int]bool) int {
	try := func(port int) int {
		ln, err := listenLocal(port)
		if err != nil {
			return 0
		}
		got := port
		if got == 0 {
			got = ln.Addr().(*net.TCPAddr).Port
		}
		ln.Close()
		used[got] = true
		return got
	}
	for port := from + 1; port <= min(from+suggestSpan, 65535); port++ {
		if used[port] {
			continue
		}
		if got := try(port); got > 0 {
			return got
		}
	}
	return try(0)
}

// bind listens locally for one of the run's ports: on local when it is
// named (taken: refused), else on a free port — the one this run's port
// had last time when it is free. Called with d.mu held.
func (d *daemon) bind(r *drun, remote, local int) (*dport, error) {
	var ln net.Listener
	var err error
	if local > 0 {
		if ln, err = listenLocal(local); err != nil {
			return nil, &takenError{remote: remote, local: local}
		}
	} else {
		if prev := d.assign.get(r.info.ID, remote); prev > 0 {
			ln, _ = listenLocal(prev)
		}
		if ln == nil {
			if ln, err = listenLocal(0); err != nil {
				return nil, fmt.Errorf("port %d: %w", remote, err)
			}
		}
	}
	return &dport{remote: remote, local: ln.Addr().(*net.TCPAddr).Port, ln: ln, free: local == 0}, nil
}

// commit starts forwarding a bound port. Called with d.mu held.
func (d *daemon) commit(r *drun, p *dport) {
	p.since = time.Now()
	r.ports[p.remote] = p
	if p.free {
		d.assign.set(r.info.ID, p.remote, p.local)
	}
	d.log.Printf("run %s: port %d → localhost:%d", r.info.ID, p.remote, p.local)
	go d.accept(r, p)
}

func (d *daemon) accept(r *drun, p *dport) {
	for {
		local, err := p.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer local.Close()
			c, err := d.client()
			if err == nil {
				var conn net.Conn
				if conn, err = c.DialPort(r.ctx, r.info.ID, p.remote); err == nil {
					p.active.Add(1)
					defer p.active.Add(-1)
					wstunnel.Pipe(local, conn)
					return
				}
			}
			d.log.Printf("run %s: port %d: %v", r.info.ID, p.remote, err)
			if api.IsUnauthorized(err) || isNotFound(err) {
				d.drop(r.info.ID, err.Error())
			}
		}()
	}
}

// drop ends a run's forwards, saying why in the log.
func (d *daemon) drop(runID, why string) {
	d.mu.Lock()
	r := d.runs[runID]
	if r != nil {
		d.closeRun(r)
		delete(d.runs, runID)
		d.log.Printf("run %s: stopped (%s)", runID, why)
	}
	empty := len(d.runs) == 0
	d.mu.Unlock()
	if empty {
		d.exit()
	}
}

func (d *daemon) closeRun(r *drun) {
	r.cancel()
	for _, p := range r.ports {
		p.ln.Close()
	}
}

// stop ends what req names and says what is left.
func (d *daemon) stop(req stopRequest) ([]runState, error) {
	d.mu.Lock()
	switch {
	case req.All:
		for id, r := range d.runs {
			d.closeRun(r)
			delete(d.runs, id)
		}
	default:
		r := d.find(req.Run)
		if r == nil {
			d.mu.Unlock()
			return nil, fmt.Errorf("%q is not forwarded (apothecary forward status)", req.Run)
		}
		if len(req.Ports) == 0 {
			d.closeRun(r)
			delete(d.runs, r.info.ID)
			break
		}
		for _, remote := range req.Ports {
			p := r.ports[remote]
			if p == nil {
				d.mu.Unlock()
				return nil, fmt.Errorf("port %d of %s is not forwarded", remote, req.Run)
			}
			p.ln.Close()
			delete(r.ports, remote)
		}
		// The ports named were all it had: done.
		if len(r.ports) == 0 {
			d.closeRun(r)
			delete(d.runs, r.info.ID)
		}
	}
	empty := len(d.runs) == 0
	d.mu.Unlock()
	left := d.snapshot("")
	if empty {
		d.exit()
	}
	return left, nil
}

// find is the forwarded run ref names: its id or its workspace.
func (d *daemon) find(ref string) *drun {
	if r := d.runs[ref]; r != nil {
		return r
	}
	for _, r := range d.runs {
		if r.info.Workspace == ref {
			return r
		}
	}
	return nil
}

// state is the run's forwards, by port. Called with d.mu held.
func (r *drun) state() runState {
	st := runState{ID: r.info.ID, Label: r.info.Label(), Workspace: r.info.Workspace, Ports: []portState{}}
	for _, p := range r.ports {
		st.Ports = append(st.Ports, portState{Remote: p.remote, Local: p.local, Active: int(p.active.Load()), Since: p.since})
	}
	sort.Slice(st.Ports, func(i, j int) bool { return st.Ports[i].Remote < st.Ports[j].Remote })
	return st
}

// snapshot is every run's forwards (or one run's), by label.
func (d *daemon) snapshot(runID string) []runState {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []runState{}
	for id, r := range d.runs {
		if runID != "" && id != runID {
			continue
		}
		st := r.state()
		if len(st.Ports) == 0 {
			continue
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

func (d *daemon) closeAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, r := range d.runs {
		d.closeRun(r)
		delete(d.runs, id)
	}
}

// noteBusy cancels the idle exit of a daemon that now forwards something.
func (d *daemon) noteBusy() { d.idle.Stop() }

func (d *daemon) exitIfIdle() {
	d.mu.Lock()
	empty := len(d.runs) == 0
	d.mu.Unlock()
	if empty {
		d.exit()
	}
}

func (d *daemon) exit() { d.once.Do(func() { close(d.quit) }) }

// assignments remember which local port each run's port got, so forwarding
// it again lands on the same one when it is free: bookmarks and database
// tools keep working across a stop or a reboot.
type assignments struct {
	path string
	mu   sync.Mutex
	by   map[string]map[int]int
}

func loadAssignments(path string) *assignments {
	a := &assignments{path: path, by: map[string]map[int]int{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &a.by)
	}
	return a
}

// maxAssigned bounds the remembered runs: the oldest are forgotten.
const maxAssigned = 200

func (a *assignments) get(run string, remote int) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.by[run][remote]
}

func (a *assignments) set(run string, remote, local int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.by[run] == nil {
		if len(a.by) >= maxAssigned {
			for k := range a.by { // any one: a map has no order to keep
				delete(a.by, k)
				break
			}
		}
		a.by[run] = map[int]int{}
	}
	a.by[run][remote] = local
	if a.path == "" {
		return
	}
	if data, err := json.Marshal(a.by); err == nil {
		_ = os.WriteFile(a.path, data, 0o600)
	}
}
