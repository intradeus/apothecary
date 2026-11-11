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

var (
	// followEvery is how often a followed run's ports are listed.
	followEvery = 3 * time.Second
	// daemonIdle is how long a daemon with nothing forwarded stays up: the
	// command that started it is about to ask for something.
	daemonIdle = 30 * time.Second
)

// listenLocal opens a port on this machine's loopback, never on its other
// interfaces: what a run serves stays the person's. Port 0 is a free one.
// A var for tests.
var listenLocal = func(port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
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
	forwardRequest struct {
		Run   string    `json:"run"`
		Ports []mapping `json:"ports,omitempty"` // none: every port the run listens on, kept in step
		Same  bool      `json:"same,omitempty"`  // local port = the run's
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
		Follow    bool        `json:"follow,omitempty"`
		Same      bool        `json:"same,omitempty"`
		Ports     []portState `json:"ports"`
		// Notes are what the request could not do as asked (a port taken,
		// a port the run does not listen on).
		Notes []string `json:"notes,omitempty"`
	}
)

// daemon holds the forwards.
type daemon struct {
	env   Env
	log   *log.Logger
	base  context.Context
	every time.Duration // followEvery, as it was when the daemon started

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
	follow bool
	same   bool
	ports  map[int]*dport
	cancel context.CancelFunc // stops following
	ctx    context.Context
}

type dport struct {
	remote, local int
	ln            net.Listener
	active        atomic.Int32
	since         time.Time
}

func newDaemon(ctx context.Context, env Env, logger *log.Logger, assign *assignments) *daemon {
	d := &daemon{env: env, log: logger, base: ctx, every: followEvery, runs: map[string]*drun{}, assign: assign, quit: make(chan struct{})}
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

// forward adds what req asks for and says where the run's ports landed.
func (d *daemon) forward(ctx context.Context, req forwardRequest) (runState, error) {
	c, err := d.client()
	if err != nil {
		return runState{}, err
	}
	info, err := c.FindRun(ctx, req.Run)
	if err != nil {
		return runState{}, err
	}
	d.mu.Lock()
	r := d.runs[info.ID]
	if r == nil {
		rctx, cancel := context.WithCancel(d.base)
		r = &drun{info: info, ports: map[int]*dport{}, ctx: rctx, cancel: cancel}
		d.runs[info.ID] = r
	}
	r.info = info
	if req.Same {
		r.same = true
	}
	var notes []string
	if len(req.Ports) > 0 {
		for _, m := range req.Ports {
			if r.ports[m.Remote] != nil {
				continue
			}
			if err := d.open(r, m.Remote, m.Local, req.Same, false); err != nil {
				notes = append(notes, err.Error())
			}
		}
	}
	startFollow := len(req.Ports) == 0 && !r.follow
	if startFollow {
		r.follow = true
	}
	d.mu.Unlock()
	if startFollow {
		// The first listing before answering, so the answer has the ports.
		if err := d.sync(r); err != nil {
			d.drop(info.ID, err.Error())
			return runState{}, err
		}
		go d.followRun(r)
	}
	st := d.snapshot(info.ID)
	if len(st) == 0 {
		d.drop(info.ID, "nothing forwarded")
		return runState{ID: info.ID, Label: info.Label(), Workspace: info.Workspace, Notes: notes}, errors.New(joinNotes(notes))
	}
	st[0].Notes = notes
	d.noteBusy()
	return st[0], nil
}

func joinNotes(notes []string) string {
	if len(notes) == 0 {
		return "nothing forwarded"
	}
	out := notes[0]
	for _, n := range notes[1:] {
		out += "; " + n
	}
	return out
}

// open listens locally for one of the run's ports: on local when it is
// named, on the run's own port with same, else on a free port — the one
// this run's port had last time when it is free. Called with d.mu held.
func (d *daemon) open(r *drun, remote, local int, same, follow bool) error {
	var ln net.Listener
	var err error
	switch {
	case local > 0:
		if ln, err = listenLocal(local); err != nil {
			return fmt.Errorf("port %d: localhost:%d is taken", remote, local)
		}
	case same:
		if ln, err = listenLocal(remote); err != nil {
			if !follow {
				return fmt.Errorf("port %d: localhost:%d is taken (forward it without --same, or name a local port: %d:<local>)", remote, remote, remote)
			}
			d.log.Printf("run %s: localhost:%d is taken; port %d goes to a free port", r.info.ID, remote, remote)
			ln, err = listenLocal(0)
		}
	default:
		if prev := d.assign.get(r.info.ID, remote); prev > 0 {
			ln, err = listenLocal(prev)
		}
		if ln == nil {
			ln, err = listenLocal(0)
		}
	}
	if err != nil {
		return fmt.Errorf("port %d: %w", remote, err)
	}
	p := &dport{remote: remote, local: ln.Addr().(*net.TCPAddr).Port, ln: ln, since: time.Now()}
	r.ports[remote] = p
	if local == 0 && !same {
		d.assign.set(r.info.ID, remote, p.local)
	}
	d.log.Printf("run %s: port %d → localhost:%d", r.info.ID, remote, p.local)
	go d.accept(r, p)
	return nil
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

// followRun keeps a run's forwards in step with what it listens on until
// it is stopped, or the run or the sign-in is gone.
func (d *daemon) followRun(r *drun) {
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(d.every):
		}
		if err := d.sync(r); err != nil {
			if api.IsUnauthorized(err) || isNotFound(err) {
				d.drop(r.info.ID, err.Error())
				return
			}
			d.log.Printf("run %s: listing its ports: %v (retrying)", r.info.ID, err)
		}
	}
}

// sync opens what the run listens on and closes what it stopped listening on.
func (d *daemon) sync(r *drun) error {
	c, err := d.client()
	if err != nil {
		return err
	}
	ports, err := c.Ports(r.ctx, r.info.ID)
	if err != nil {
		return err
	}
	want := map[int]bool{}
	for _, p := range ports.Listening() {
		want[p.Port] = true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runs[r.info.ID] != r {
		return nil // stopped meanwhile
	}
	for remote, p := range r.ports {
		if !want[remote] {
			p.ln.Close()
			delete(r.ports, remote)
			d.log.Printf("run %s: port %d stopped listening; localhost:%d closed", r.info.ID, remote, p.local)
		}
	}
	added := make([]int, 0, len(want))
	for remote := range want {
		if r.ports[remote] == nil {
			added = append(added, remote)
		}
	}
	sort.Ints(added)
	for _, remote := range added {
		if err := d.open(r, remote, 0, r.same, true); err != nil {
			d.log.Printf("run %s: %v", r.info.ID, err)
		}
	}
	return nil
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
		// The ports named were all it had, and it is not following: done.
		if len(r.ports) == 0 && !r.follow {
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

// snapshot is every run's forwards (or one run's), by label.
func (d *daemon) snapshot(runID string) []runState {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []runState{}
	for id, r := range d.runs {
		if runID != "" && id != runID {
			continue
		}
		st := runState{ID: id, Label: r.info.Label(), Workspace: r.info.Workspace, Follow: r.follow, Same: r.same, Ports: []portState{}}
		for _, p := range r.ports {
			st.Ports = append(st.Ports, portState{Remote: p.remote, Local: p.local, Active: int(p.active.Load()), Since: p.since})
		}
		sort.Slice(st.Ports, func(i, j int) bool { return st.Ports[i].Remote < st.Ports[j].Remote })
		if len(st.Ports) == 0 && !st.Follow {
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
