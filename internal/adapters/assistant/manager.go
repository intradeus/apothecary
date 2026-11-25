package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/egressgw/policyfile"
	"apothecary/internal/ports/assistantport"
)

// Gateway is the person's egress gateway, as the manager needs it: up with
// a standing allow list, and that list widened for a while.
type Gateway interface {
	// Attach brings the person's gateway up with hosts as its allow list and
	// returns the pod's tunnel to it.
	Attach(ctx context.Context, person string, hosts []string) (tunnel, error)
	// Allow replaces the allow list.
	Allow(ctx context.Context, person string, hosts []string) error
}

// Manager is assistantport.Pods: the policy over a Host.
type Manager struct {
	Host    Host
	Gateway Gateway
	// Image is the assistant image a pod should run: one on another image
	// is recreated when nobody uses it.
	Image string
	// IdleAfter stops a pod nobody has used for this long; VolumeAfter
	// deletes a volume unused this long (0: never).
	IdleAfter, VolumeAfter time.Duration
	// StartTimeout bounds one start (0: DefaultStartTimeout).
	StartTimeout time.Duration
	// Protocols are the transports a fetch may use (https; tests add file).
	Protocols []string
	Log       *slog.Logger
	// Now is the clock (tests).
	Now func() time.Time

	mu       sync.Mutex
	persons  map[string]*personState
	pmu      map[string]*sync.Mutex
	pmuGuard sync.Mutex
}

type personState struct {
	used     time.Time
	standing []string // the gateway's allow list outside a fetch
	busy     int      // fetches and turns in flight
	err      string   // why the last start failed
	tries    int      // starts ended: a health check clears only the err it saw
	confined bool     // the node's kernel confines: checked once per pod
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

func (m *Manager) state(person string) *personState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.persons == nil {
		m.persons = map[string]*personState{}
	}
	st := m.persons[person]
	if st == nil {
		st = &personState{}
		m.persons[person] = st
	}
	return st
}

func (m *Manager) lock(person string) func() {
	m.pmuGuard.Lock()
	if m.pmu == nil {
		m.pmu = map[string]*sync.Mutex{}
	}
	mu := m.pmu[person]
	if mu == nil {
		mu = &sync.Mutex{}
		m.pmu[person] = mu
	}
	m.pmuGuard.Unlock()
	mu.Lock()
	return mu.Unlock
}

// DefaultStartTimeout bounds one start: the gateway (its rollout, up to 3
// minutes, then its control API, up to 1), the pod (Ready within 5), then
// the kernel check and the agent host's answer.
const DefaultStartTimeout = 12 * time.Minute

func (m *Manager) startTimeout() time.Duration {
	if m.StartTimeout > 0 {
		return m.StartTimeout
	}
	return DefaultStartTimeout
}

// Ensure brings the pod up: its gateway first (the pod's DNS and only way
// out), then the pod, recreated on a stale image when no fetch or turn is
// in flight, then the kernel check (no Landlock, no session) and the agent
// host's answer.
//
// The start is the engine's, not the caller's: it runs on ctx's values with
// its own deadline (StartTimeout), so a caller that stops waiting (a
// browser that went away, a turn stopped) returns ctx's error and leaves
// the start to finish, its outcome recorded and logged all the same.
func (m *Manager) Ensure(ctx context.Context, person string, hosts []string) error {
	if m.Host == nil {
		return assistantport.ErrNoHost
	}
	done := make(chan error, 1)
	go func() { done <- m.start(context.WithoutCancel(ctx), person, hosts) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// start is one start under the person's lock, its deadline counted from
// when it holds it. A failed one is the pod's error (Status) until the next
// start, a stop, or a health check that finds the pod well.
func (m *Manager) start(ctx context.Context, person string, hosts []string) error {
	unlock := m.lock(person)
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, m.startTimeout())
	defer cancel()
	st := m.state(person)
	err := m.ensure(ctx, person, st, hosts)
	m.mu.Lock()
	st.used = m.now()
	st.tries++
	st.err = ""
	if err != nil {
		st.err = err.Error()
	}
	m.mu.Unlock()
	if err != nil {
		m.log().Error("assistant: start a pod", "person", person, "err", err)
	}
	return err
}

func (m *Manager) ensure(ctx context.Context, person string, st *personState, hosts []string) error {
	hosts = cleanHosts(hosts)
	tn, err := m.Gateway.Attach(ctx, person, hosts)
	if err != nil {
		return fmt.Errorf("the assistant's gateway: %w", err)
	}
	m.mu.Lock()
	st.standing = hosts
	busy := st.busy > 0
	m.mu.Unlock()
	img, running, found, err := m.Host.Inspect(ctx, person)
	if err != nil {
		return err
	}
	stale := found && img != m.Image
	if !found || !running || (stale && !busy) {
		// Unchecked from here: a start that fails half-way may leave a new
		// pod the next one finds running.
		m.mu.Lock()
		st.confined = false
		m.mu.Unlock()
		if err := m.Host.Ensure(ctx, person, tn, stale && !busy); err != nil {
			return fmt.Errorf("the assistant's pod: %w", err)
		}
	}
	_ = m.Host.Touch(ctx, person, m.now())
	m.mu.Lock()
	confined := st.confined
	m.mu.Unlock()
	if !confined {
		_, stderr, code, err := m.Host.Exec(ctx, person, agentContainer, []string{shimBin, "confine", "--check"}, nil)
		switch {
		case err != nil:
			return fmt.Errorf("the assistant's pod: %w", err)
		case code == 3:
			return assistantport.ErrNoLandlock
		case code != 0:
			return fmt.Errorf("the assistant's pod: confine --check exited %d: %s", code, strings.TrimSpace(string(stderr)))
		}
		m.mu.Lock()
		st.confined = true
		m.mu.Unlock()
	}
	return m.waitAgentHost(ctx, person)
}

// waitAgentHost waits for the sidecar's /health.
func (m *Manager) waitAgentHost(ctx context.Context, person string) error {
	var last error
	for range 100 {
		if _, err := m.call(ctx, person, http.MethodGet, "/health", nil); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("the assistant's agent host does not answer: %w", last)
}

func (m *Manager) Status(ctx context.Context, person string) (assistantport.Status, error) {
	if m.Host == nil {
		return assistantport.Status{State: assistantport.PodAbsent, Error: assistantport.ErrNoHost.Error()}, nil
	}
	_, running, found, err := m.Host.Inspect(ctx, person)
	if err != nil {
		return assistantport.Status{}, err
	}
	st := m.state(person)
	m.mu.Lock()
	failed, confined, tries := st.err, st.confined, st.tries
	m.mu.Unlock()
	switch {
	case !found:
		return assistantport.Status{State: assistantport.PodAbsent, Error: failed}, nil
	case !running:
		return assistantport.Status{State: assistantport.PodStarting, Error: failed}, nil
	}
	// A failed start whose pod came up since (its agent host was slow, the
	// caller's start ran on) is over once the pod is found well: past its
	// kernel check, its agent host answering. One that never passed the
	// check (its node cannot confine a session) keeps its error.
	if failed != "" && confined && m.healthy(ctx, person) {
		m.mu.Lock()
		if st.tries == tries {
			st.err = ""
		}
		failed = st.err
		m.mu.Unlock()
	}
	return assistantport.Status{State: assistantport.PodRunning, Error: failed}, nil
}

// healthy is one quick look at the agent host's /health.
func (m *Manager) healthy(ctx context.Context, person string) bool {
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := m.call(hctx, person, http.MethodGet, "/health", nil)
	return err == nil
}

func (m *Manager) AgentHost(ctx context.Context, person string) (assistantport.AgentHost, error) {
	if m.Host == nil {
		return assistantport.AgentHost{}, assistantport.ErrNoHost
	}
	tok, err := m.Host.Token(ctx, person)
	if err != nil {
		return assistantport.AgentHost{}, err
	}
	m.touch(person)
	return assistantport.AgentHost{Dialer: dialer{m: m, person: person}, Token: tok}, nil
}

type dialer struct {
	m      *Manager
	person string
}

func (d dialer) Dial(ctx context.Context) (net.Conn, error) {
	return d.m.Host.Dial(ctx, d.person, agentSock)
}

func (m *Manager) touch(person string) {
	st := m.state(person)
	m.mu.Lock()
	st.used = m.now()
	m.mu.Unlock()
}

// Busy marks a turn or a fetch in flight on the person's pod: an idle stop
// and a stale-image recreate both wait for it. The returned func ends it.
func (m *Manager) Busy(person string) func() {
	st := m.state(person)
	m.mu.Lock()
	st.busy++
	st.used = m.now()
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		st.busy--
		st.used = m.now()
		m.mu.Unlock()
	}
}

func (m *Manager) Home(sessionID string) string       { return Home(sessionID) }
func (m *Manager) Folder(r assistantport.Repo) string { return Folder(r) }

// systemRead is what every session reads: the image's libraries, its
// configuration and the harnesses.
var systemRead = []string{"/lib", "/usr/lib", "/usr/share", "/etc", toolsMusl}

// sessionDevices are the devices a harness opens.
var sessionDevices = []string{"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/tty"}

// StartRequest is the agent host's start request for a confined session:
// its rules read its repositories (and nothing beside them), write its home
// (which holds its temp folder), and run its harness, git, ripgrep, the
// shim and the loader. No shell.
func (m *Manager) StartRequest(person string, s assistantport.Session) (assistantport.Start, error) {
	if s.ID == "" || slug(s.ID) != s.ID {
		return assistantport.Start{}, fmt.Errorf("session id %q is not a folder name", s.ID)
	}
	if len(s.Argv) == 0 || !path.IsAbs(s.Argv[0]) {
		return assistantport.Start{}, errors.New("a session's harness is named by its absolute path")
	}
	home := Home(s.ID)
	read := slices.Clone(systemRead)
	cwd := home
	for _, r := range s.Repos {
		read = append(read, Folder(r))
		if cwd == home {
			cwd = WorkshopFolder(r)
		}
	}
	exe := []string{s.Argv[0]}
	for _, e := range s.Exec {
		if !path.IsAbs(e) {
			return assistantport.Start{}, fmt.Errorf("program %q is not an absolute path", e)
		}
		exe = append(exe, e)
	}
	exe = append(exe, gitBin, rgBin, shimBin, loader)
	env := map[string]string{
		"HOME": home, "TMPDIR": path.Join(home, "tmp"), "PATH": "/usr/bin:/bin",
		"GIT_CONFIG_NOSYSTEM": "1", "LC_ALL": "C.UTF-8",
	}
	for k, v := range s.Env {
		env[k] = v
	}
	return assistantport.Start{Argv: s.Argv, Env: env, Cwd: cwd, Confine: &assistantport.Rules{
		Read: dedupe(read), Write: []string{home}, Exec: dedupe(exe), Devices: slices.Clone(sessionDevices),
	}}, nil
}

// GitServer is the git tool's MCP server for a session: the shim's
// git-mcp over the session's repositories, started by the harness below
// itself so it runs under the session's rules.
func GitServer(sessionID string, repos []assistantport.Repo) (command string, args []string) {
	args = []string{"git-mcp", "--state", Home(sessionID)}
	for _, r := range repos {
		args = append(args, "--repo", slug(r.Name)+"="+Folder(r))
	}
	return shimBin, args
}

func dedupe(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// Fetch brings repositories up to date: the repository hosts join the
// gateway's allow list for the length of the fetch, the credential rides
// the fetch's stdin into the git container and nowhere else.
func (m *Manager) Fetch(ctx context.Context, person string, items []assistantport.FetchItem) ([]assistantport.RepoState, error) {
	if m.Host == nil {
		return nil, assistantport.ErrNoHost
	}
	if len(items) == 0 {
		return nil, nil
	}
	done := m.Busy(person)
	defer done()
	unlock := m.lock(person)
	defer unlock()
	st := m.state(person)
	m.mu.Lock()
	standing := slices.Clone(st.standing)
	m.mu.Unlock()
	var hosts []string
	type req struct {
		Dir  string `json:"dir"`
		URL  string `json:"url"`
		User string `json:"user,omitempty"`
		Pass string `json:"pass,omitempty"`
	}
	body := struct {
		Repos []req `json:"repos"`
	}{}
	repos := make([]assistantport.Repo, 0, len(items))
	for _, it := range items {
		r := it.Repo
		u, err := url.Parse(r.URL)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("repository %s has no host in its URL", r.Name)
		}
		hosts = append(hosts, u.Hostname())
		repos = append(repos, r)
		body.Repos = append(body.Repos, req{Dir: Folder(r), URL: r.URL, User: it.Cred.User, Pass: it.Cred.Pass})
	}
	// Closed again whatever happened, on a context of its own, registered
	// before the widening: a widening that took effect but answered an error
	// (a timeout), or a fetch cancelled half-way, must not leave them open.
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := m.Gateway.Allow(cctx, person, standing); err != nil {
			m.log().Error("assistant: close the repository hosts", "person", person, "err", err)
		}
	}()
	if err := m.Gateway.Allow(ctx, person, cleanHosts(append(slices.Clone(standing), hosts...))); err != nil {
		return nil, fmt.Errorf("open the repository hosts: %w", err)
	}
	in, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	argv := []string{shimBin, "fetch"}
	if len(m.Protocols) > 0 {
		argv = append(argv, "--protocols", strings.Join(m.Protocols, ","))
	}
	stdout, stderr, code, err := m.Host.Exec(ctx, person, gitContainer, argv, in)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	if code == 2 {
		return nil, fmt.Errorf("fetch: %s", strings.TrimSpace(string(stderr)))
	}
	now := m.now()
	byDir := map[string]assistantport.RepoState{}
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for {
		var r struct {
			Dir   string `json:"dir"`
			OK    bool   `json:"ok"`
			Head  string `json:"head"`
			Error string `json:"error"`
		}
		if err := dec.Decode(&r); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("fetch: its answer: %w", err)
		}
		byDir[r.Dir] = assistantport.RepoState{Folder: r.Dir, OK: r.OK, Head: r.Head, Error: r.Error, At: now}
	}
	out := make([]assistantport.RepoState, 0, len(repos))
	for _, r := range repos {
		s, ok := byDir[Folder(r)]
		if !ok {
			s = assistantport.RepoState{Folder: Folder(r), Error: "no answer for this repository", At: now}
		}
		s.Workshop, s.Name = r.Workshop, r.Name
		out = append(out, s)
	}
	return out, nil
}

// WriteFile writes a file under /state through the agent host.
func (m *Manager) WriteFile(ctx context.Context, person, p string, data []byte, mode uint32) error {
	if !strings.HasPrefix(p, stateRoot+"/") {
		return fmt.Errorf("%s is not under %s", p, stateRoot)
	}
	_, err := m.call(ctx, person, http.MethodPost, "/fs/write", map[string]any{"path": p, "data": data, "mode": mode})
	return err
}

// ReadFile reads a file in the agent container through the agent host.
func (m *Manager) ReadFile(ctx context.Context, person, p string) ([]byte, error) {
	raw, err := m.call(ctx, person, http.MethodGet, "/fs/abs/file?path="+url.QueryEscape(p), nil)
	if err != nil {
		return nil, err
	}
	var f struct {
		Content string `json:"content"`
		Binary  bool   `json:"binary"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	if f.Binary {
		return nil, fmt.Errorf("%s is not text", p)
	}
	return []byte(f.Content), nil
}

// call is one request to the agent host.
func (m *Manager) call(ctx context.Context, person, method, p string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	tok, err := m.Host.Token(ctx, person)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return m.Host.Dial(ctx, person, agentSock) },
	}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, method, "http://agent-host"+p, rd)
	if err != nil {
		return nil, err
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(assistantport.AgentHostTokenHeader, tok)
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("agent host %s %s: %d %s", method, p, res.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// Run stops idle pods and reaps old volumes until ctx ends.
func (m *Manager) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.Sweep(ctx)
	}
}

// Sweep is one pass: a running pod nobody has used for IdleAfter is
// stopped (never one with a fetch or turn in flight); a volume whose pod is
// gone and that nobody has used for VolumeAfter is deleted.
func (m *Manager) Sweep(ctx context.Context) {
	if m.Host == nil {
		return
	}
	now := m.now()
	running, err := m.Host.Running(ctx)
	if err != nil {
		m.log().Warn("assistant: list pods", "err", err)
		return
	}
	for _, person := range running {
		m.stopIfIdle(ctx, person, now)
	}
	if m.VolumeAfter <= 0 {
		return
	}
	vols, err := m.Host.Volumes(ctx)
	if err != nil {
		m.log().Warn("assistant: list volumes", "err", err)
		return
	}
	for person, used := range vols {
		if slices.Contains(running, person) || used.IsZero() || now.Sub(used) < m.VolumeAfter {
			continue
		}
		unlock := m.lock(person)
		if _, _, found, err := m.Host.Inspect(ctx, person); err == nil && !found {
			if err := m.Host.DeleteVolume(ctx, person); err != nil {
				m.log().Warn("assistant: delete an old volume", "person", person, "err", err)
			}
		}
		unlock()
	}
}

// stopIfIdle stops a person's pod when it is idle, deciding under the
// person's lock: an Ensure or a fetch that began after a look without it
// would have its pod deleted under it.
func (m *Manager) stopIfIdle(ctx context.Context, person string, now time.Time) {
	if m.IdleAfter <= 0 {
		return
	}
	unlock := m.lock(person)
	defer unlock()
	st := m.state(person)
	m.mu.Lock()
	if st.used.IsZero() {
		st.used = now // first seen since this engine started
	}
	idle := st.busy == 0 && now.Sub(st.used) >= m.IdleAfter
	m.mu.Unlock()
	if !idle {
		return
	}
	if err := m.Host.Stop(ctx, person); err != nil {
		m.log().Warn("assistant: stop an idle pod", "person", person, "err", err)
		return
	}
	// A stopped pod starts afresh: its last start's error is over.
	m.mu.Lock()
	st.err = ""
	m.mu.Unlock()
	_ = m.Host.Touch(ctx, person, now)
}

// cleanHosts lowercases, trims and dedupes host names, sorted.
func cleanHosts(hosts []string) []string {
	out := []string{}
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	slices.Sort(out)
	return out
}

// EgressGateway is Gateway over the egress gateway manager: the person's
// gateway is its own "project" (gatewayKey), the pod its one peer.
type EgressGateway struct {
	M *egressgw.Manager
	// EngineIP is the engine's address, routed outside the tunnel.
	EngineIP func(ctx context.Context) (string, error)
	// Base is the raw egress every allow list keeps (AllowedCIDRs,
	// AllowedPorts): a model endpoint on a private address and port.
	Base policyfile.File
}

// policy is the allow list hosts over the base.
func (g EgressGateway) policy(hosts []string) policyfile.File {
	return policyfile.File{Mode: policyfile.ModeAllow, Domains: hosts,
		AllowedCIDRs: g.Base.AllowedCIDRs, AllowedPorts: g.Base.AllowedPorts}
}

func (g EgressGateway) Attach(ctx context.Context, person string, hosts []string) (tunnel, error) {
	att, err := g.M.EnsureRunEgress(ctx, gatewayKey(person), "assistant", gatewayPeer(person), g.policy(hosts))
	if err != nil {
		return tunnel{}, err
	}
	engineIP := ""
	if g.EngineIP != nil {
		if engineIP, err = g.EngineIP(ctx); err != nil {
			return tunnel{}, err
		}
	}
	host, _, _ := net.SplitHostPort(att.WGEndpoint)
	return tunnel{GatewayIP: att.GatewayIP, Endpoint: att.WGEndpoint, EndpointIP: host, Secret: att.WGSecret, EngineIP: engineIP}, nil
}

func (g EgressGateway) Allow(ctx context.Context, person string, hosts []string) error {
	return g.M.SetPolicy(ctx, gatewayKey(person), g.policy(hosts))
}

var _ assistantport.Pods = (*Manager)(nil)
