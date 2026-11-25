package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/egressgw/policyfile"
	"apothecary/internal/adapters/kube"
	"apothecary/internal/ports/assistantport"
)

var testCfg = Config{
	Image: "apo/assistant:v1", ShimImage: "apo/shim:v1", ToolsImage: "apo/tools:v1", TunnelImage: "apo/egress-gw:v1",
	EnginePort: 8080, SessionIdle: 5 * time.Minute,
}

var testTunnel = tunnel{GatewayIP: "10.200.0.1", Endpoint: "10.43.0.9:51820", EndpointIP: "10.43.0.9", Secret: "apo-wg-assistant-usr1", EngineIP: "10.43.0.2"}

// Decision 2, field by field: a user namespace of its own, non-root, every
// capability dropped but the tunnel's NET_ADMIN, no escalation, the
// runtime's seccomp, a read-only root, no service account token; the agent
// reads the repositories and never sees a credential.
func TestPodManifest(t *testing.T) {
	pod := testCfg.podManifest("usr1", testTunnel)
	sp := pod.Spec
	if sp.HostUsers == nil || *sp.HostUsers {
		t.Error("the pod shares the host's user namespace")
	}
	if sp.AutomountServiceAccountToken == nil || *sp.AutomountServiceAccountToken {
		t.Error("the pod mounts a service account token")
	}
	if sp.RuntimeClassName != "" {
		t.Errorf("runtime class %q: the plain runtime is the plan's", sp.RuntimeClassName)
	}
	if sp.SecurityContext == nil || sp.SecurityContext.RunAsNonRoot == nil || !*sp.SecurityContext.RunAsNonRoot ||
		sp.SecurityContext.SeccompProfile == nil || sp.SecurityContext.SeccompProfile.Type != "RuntimeDefault" {
		t.Errorf("pod security context = %+v", sp.SecurityContext)
	}
	all := append(slices.Clone(sp.InitContainers), sp.Containers...)
	if len(all) != 6 {
		t.Fatalf("%d containers", len(all))
	}
	for _, c := range all {
		sc := c.SecurityContext
		if sc == nil || sc.Privileged != nil && *sc.Privileged {
			t.Errorf("%s: privileged or no security context", c.Name)
			continue
		}
		if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
			t.Errorf("%s may escalate", c.Name)
		}
		if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
			t.Errorf("%s has a writable root", c.Name)
		}
		if sc.SeccompProfile == nil || sc.SeccompProfile.Type != "RuntimeDefault" {
			t.Errorf("%s: seccomp %+v", c.Name, sc.SeccompProfile)
		}
		if sc.Capabilities == nil || !slices.Equal(sc.Capabilities.Drop, []string{"ALL"}) {
			t.Errorf("%s keeps capabilities: %+v", c.Name, sc.Capabilities)
		}
		if c.Name == tunnelContainer {
			if !slices.Equal(sc.Capabilities.Add, []string{"NET_ADMIN"}) {
				t.Errorf("the tunnel adds %v", sc.Capabilities.Add)
			}
			continue
		}
		if len(sc.Capabilities.Add) > 0 {
			t.Errorf("%s adds %v", c.Name, sc.Capabilities.Add)
		}
		if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot || sc.RunAsUser == nil || *sc.RunAsUser == 0 {
			t.Errorf("%s may run as root", c.Name)
		}
	}
	byName := map[string]kube.Container{}
	for _, c := range all {
		byName[c.Name] = c
	}
	mount := func(c, path string) *kube.VolumeMount {
		for _, m := range byName[c].VolumeMounts {
			if m.MountPath == path {
				return &m
			}
		}
		return nil
	}
	if m := mount(agentContainer, reposRoot); m == nil || !m.ReadOnly {
		t.Errorf("the agent's repositories = %+v, want read-only", m)
	}
	if m := mount(gitContainer, reposRoot); m == nil || m.ReadOnly {
		t.Errorf("the git container's repositories = %+v, want read-write", m)
	}
	if mount(gitContainer, stateRoot) != nil {
		t.Error("the git container sees the sessions' state")
	}
	for _, c := range []string{agentContainer, gitContainer} {
		if m := mount(c, shimDir); m == nil || !m.ReadOnly {
			t.Errorf("%s's shim = %+v, want read-only", c, m)
		}
	}
	if m := mount(agentContainer, toolsDir); m == nil || !m.ReadOnly || m.SubPath != "seed/tools" {
		t.Errorf("the agent's harnesses = %+v", m)
	}
	if !strings.Contains(toolsSeed, "TOOLS_HASH") || strings.Contains(toolsSeed, "omp") || strings.Contains(toolsSeed, "codex") {
		t.Error("the tools seed copies what the assistant does not run, or every time")
	}
	// No secret reaches the agent: not its env, not a mount.
	secretVols := map[string]bool{}
	for _, v := range sp.Volumes {
		if v.Secret != nil {
			secretVols[v.Name] = true
		}
	}
	for _, c := range []string{agentContainer, gitContainer} {
		for _, m := range byName[c].VolumeMounts {
			if secretVols[m.Name] && !(c == agentContainer && m.Name == "relay-secret") {
				t.Errorf("%s mounts the secret volume %s", c, m.Name)
			}
		}
		if len(byName[c].Env) > 1 || (len(byName[c].Env) == 1 && byName[c].Env[0].Name != "HOME") {
			t.Errorf("%s env = %+v", c, byName[c].Env)
		}
	}
	if c := byName[agentContainer]; !slices.Contains(c.Command, "--write-root") || !slices.Contains(c.Command, stateRoot) || !slices.Contains(c.Command, "--agent-idle") ||
		!slices.Contains(c.Command, "--confined-only") || !slices.Contains(c.Command, agentTokenDir+"/relay-token") {
		t.Errorf("agent command = %v", c.Command)
	}
	// The agent host's token is the one secret the agent container mounts,
	// beside no path a session's rules read.
	if m := mount(agentContainer, agentTokenDir); m == nil || !m.ReadOnly || m.Name != "relay-secret" {
		t.Errorf("the agent host's token = %+v", m)
	}
	for _, r := range systemRead {
		if strings.HasPrefix(agentTokenDir, r) {
			t.Errorf("sessions read %s, which holds the agent host's token", r)
		}
	}
	if strings.Contains(tunnelScript, "sysctl") {
		t.Error("the tunnel writes sysctls a non-privileged container cannot")
	}
	if sp.DNSConfig == nil || sp.DNSConfig.Nameservers[0] != testTunnel.GatewayIP {
		t.Errorf("DNS = %+v, want the gateway", sp.DNSConfig)
	}
	if pod.Metadata.Labels["apothecary.io/project"] != "assistant-usr1" || pod.Metadata.Labels[egressgw.LabelGatewayPeer] != "true" || pod.Metadata.Labels["apothecary.io/sandbox"] != "" {
		t.Errorf("the gateway would not admit the tunnel, or takes the pod for a sandbox: %v", pod.Metadata.Labels)
	}
	if pod.Metadata.Name != "apo-assistant-usr1" || pod.Metadata.Labels[labelAssistant] != "usr1" {
		t.Errorf("metadata = %+v", pod.Metadata)
	}
}

// The network policy: the engine in, to the relay; out, the gateway's
// WireGuard and the engine. Nothing else.
func TestNetworkPolicy(t *testing.T) {
	np := testCfg.networkPolicy("usr1")
	if !slices.Equal(np.Spec.PolicyTypes, []string{"Ingress", "Egress"}) {
		t.Errorf("policy types = %v", np.Spec.PolicyTypes)
	}
	if len(np.Spec.Ingress) != 1 || np.Spec.Ingress[0].Ports[0].Port != relayPort {
		t.Errorf("ingress = %+v", np.Spec.Ingress)
	}
	if len(np.Spec.Egress) != 2 {
		t.Fatalf("egress = %+v", np.Spec.Egress)
	}
	gw := np.Spec.Egress[0]
	if gw.Ports[0].Protocol != "UDP" || gw.Ports[0].Port != 51820 || gw.To[0].PodSelector.MatchLabels["apothecary.io/gateway"] != "assistant-usr1" {
		t.Errorf("the gateway rule = %+v", gw)
	}
	cidr := testCfg
	cidr.EngineCIDR = "192.168.1.0/24"
	if np := cidr.networkPolicy("usr1"); np.Spec.Egress[1].To[0].IPBlock == nil {
		t.Error("an outside engine is not reached by its CIDR")
	}
	if v := testCfg.volumeManifest("usr1", time.Unix(0, 0)); v.Spec.Resources.Requests["storage"] != "10Gi" || v.Metadata.Annotations[annoLastUsed] == "" {
		t.Errorf("volume = %+v", v)
	}
	sc := testCfg
	sc.StorageClass, sc.VolumeSize = "fast", "20Gi"
	if v := sc.volumeManifest("usr1", time.Now()); *v.Spec.StorageClassName != "fast" || v.Spec.Resources.Requests["storage"] != "20Gi" {
		t.Errorf("volume = %+v", v.Spec)
	}
}

func TestFolders(t *testing.T) {
	r := assistantport.Repo{Workshop: "01HXYZABCDEF", WorkshopName: "Billing API", Name: "api"}
	if got := Folder(r); !strings.HasPrefix(got, "/workshops/billing-api-") || !strings.Contains(got, "/api-") || path.Dir(got) != WorkshopFolder(r) {
		t.Errorf("Folder = %s, workshop %s", got, WorkshopFolder(r))
	}
	// Names that read the same in a folder never share one.
	for _, pair := range [][2]assistantport.Repo{
		{{Workshop: "w1", WorkshopName: "acme", Name: "Foo.Bar"}, {Workshop: "w1", WorkshopName: "acme", Name: "foo-bar"}},
		{{Workshop: "01AAAAAAAAAAAAzzzzzz", WorkshopName: "acme", Name: "api"}, {Workshop: "01BBBBBBBBBBBBzzzzzz", WorkshopName: "acme", Name: "api"}},
		{{Workshop: "w1", WorkshopName: "acme", Name: strings.Repeat("a", 60) + "1"}, {Workshop: "w1", WorkshopName: "acme", Name: strings.Repeat("a", 60) + "2"}},
	} {
		if Folder(pair[0]) == Folder(pair[1]) {
			t.Errorf("%q and %q share %s", pair[0].Name, pair[1].Name, Folder(pair[0]))
		}
	}
	for _, evil := range []string{"../../etc", "a/../../b", "..", "/"} {
		if got := Folder(assistantport.Repo{Workshop: "w", WorkshopName: evil, Name: evil}); !strings.HasPrefix(got, "/workshops/") || strings.Contains(got, "/..") || strings.Count(got, "/") != 3 {
			t.Errorf("Folder(%q) = %s", evil, got)
		}
	}
	if Home("s-1") != "/state/s-1" {
		t.Errorf("Home = %s", Home("s-1"))
	}
}

// --- the manager -----------------------------------------------------------

// fakeHost is a cluster in memory; its agent host is an HTTP server.
type fakeHost struct {
	mu       sync.Mutex
	pods     map[string]string // person → image
	running  map[string]bool
	volumes  map[string]time.Time
	ensured  []string // "person replace=…"
	stopped  []string
	deleted  []string
	execs    [][]string
	stdin    [][]byte
	landlock int // confine --check's exit code
	fetchOut string
	fetchErr error
	agent    *httptest.Server
	files    map[string]string
	// hold, when set, keeps Ensure waiting until it closes or its context
	// ends; ensureCtx is the context the last Ensure ran on.
	hold      chan struct{}
	ensureCtx context.Context
	ensureE   error
}

func newFakeHost(t *testing.T) *fakeHost {
	h := &fakeHost{pods: map[string]string{}, running: map[string]bool{}, volumes: map[string]time.Time{}, files: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	// The agent host takes its token on every request.
	guard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(assistantport.AgentHostTokenHeader) != "t0k" {
				http.Error(w, "no token", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	mux.HandleFunc("POST /fs/write", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
			Data []byte `json:"data"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		h.mu.Lock()
		h.files[req.Path] = string(req.Data)
		h.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /fs/abs/file", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		c, ok := h.files[r.URL.Query().Get("path")]
		h.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"content": c})
	})
	h.agent = httptest.NewServer(guard(mux))
	t.Cleanup(h.agent.Close)
	return h
}

func (h *fakeHost) Ensure(ctx context.Context, person string, tn tunnel, replace bool) error {
	h.mu.Lock()
	h.ensureCtx = ctx
	hold := h.hold
	h.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ensureE != nil {
		return h.ensureE
	}
	h.ensured = append(h.ensured, person+" replace="+map[bool]string{true: "yes", false: "no"}[replace])
	h.pods[person] = testCfg.Image
	h.running[person] = true
	if _, ok := h.volumes[person]; !ok {
		h.volumes[person] = time.Now()
	}
	return nil
}

func (h *fakeHost) Inspect(_ context.Context, person string) (string, bool, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	img, ok := h.pods[person]
	return img, h.running[person], ok, nil
}

func (h *fakeHost) Stop(_ context.Context, person string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.pods, person)
	delete(h.running, person)
	h.stopped = append(h.stopped, person)
	return nil
}

func (h *fakeHost) Running(context.Context) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for p, r := range h.running {
		if r {
			out = append(out, p)
		}
	}
	return out, nil
}

func (h *fakeHost) Volumes(context.Context) (map[string]time.Time, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]time.Time{}
	for k, v := range h.volumes {
		out[k] = v
	}
	return out, nil
}

func (h *fakeHost) Touch(_ context.Context, person string, now time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.volumes[person]; ok {
		h.volumes[person] = now
	}
	return nil
}

func (h *fakeHost) DeleteVolume(_ context.Context, person string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.volumes, person)
	h.deleted = append(h.deleted, person)
	return nil
}

func (h *fakeHost) Exec(_ context.Context, person, container string, argv []string, stdin []byte) ([]byte, []byte, int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.execs = append(h.execs, append([]string{container}, argv...))
	h.stdin = append(h.stdin, stdin)
	if slices.Contains(argv, "--check") {
		return []byte("landlock: abi 6\n"), nil, h.landlock, nil
	}
	if slices.Contains(argv, "fetch") {
		return []byte(h.fetchOut), nil, 0, h.fetchErr
	}
	return nil, nil, 0, nil
}

func (h *fakeHost) Token(context.Context, string) (string, error) { return "t0k", nil }

func (h *fakeHost) Dial(ctx context.Context, person, sock string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", strings.TrimPrefix(h.agent.URL, "http://"))
}

// fakeGateway records the allow lists it was given.
type fakeGateway struct {
	mu       sync.Mutex
	lists    [][]string
	attachE  error
	allowErr error
}

func (g *fakeGateway) Attach(_ context.Context, person string, hosts []string) (tunnel, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.attachE != nil {
		return tunnel{}, g.attachE
	}
	g.lists = append(g.lists, hosts)
	return testTunnel, nil
}

func (g *fakeGateway) Allow(_ context.Context, person string, hosts []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lists = append(g.lists, hosts)
	// An error after the list took effect: the worst case for a widening.
	return g.allowErr
}

func newManager(t *testing.T) (*Manager, *fakeHost, *fakeGateway) {
	h, g := newFakeHost(t), &fakeGateway{}
	return &Manager{Host: h, Gateway: g, Image: testCfg.Image, IdleAfter: 10 * time.Minute, VolumeAfter: 30 * 24 * time.Hour}, h, g
}

func TestEnsureStartsReusesAndRecreates(t *testing.T) {
	m, h, g := newManager(t)
	ctx := context.Background()
	if err := m.Ensure(ctx, "usr1", []string{"API.anthropic.com", " api.anthropic.com"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.ensured, []string{"usr1 replace=no"}) || !slices.Equal(g.lists[0], []string{"api.anthropic.com"}) {
		t.Errorf("first ensure: %v, gateway %v", h.ensured, g.lists)
	}
	if st, _ := m.Status(ctx, "usr1"); st.State != assistantport.PodRunning {
		t.Errorf("status = %+v", st)
	}
	// Running on the right image: reused, the kernel check not repeated.
	execs := len(h.execs)
	if err := m.Ensure(ctx, "usr1", nil); err != nil || len(h.ensured) != 1 || len(h.execs) != execs {
		t.Errorf("reuse: %v, %v, %d execs", err, h.ensured, len(h.execs)-execs)
	}
	// A stale image, nobody busy: recreated.
	h.pods["usr1"] = "apo/assistant:v0"
	if err := m.Ensure(ctx, "usr1", nil); err != nil || h.ensured[len(h.ensured)-1] != "usr1 replace=yes" {
		t.Errorf("stale: %v, %v", err, h.ensured)
	}
	// A stale image while a turn runs: kept.
	h.pods["usr1"] = "apo/assistant:v0"
	done := m.Busy("usr1")
	n := len(h.ensured)
	if err := m.Ensure(ctx, "usr1", nil); err != nil || len(h.ensured) != n {
		t.Errorf("stale but busy: %v, %v", err, h.ensured)
	}
	done()
}

// lockedBuffer is a log the starts write from their own goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// podError waits for the person's pod error to be want.
func podError(t *testing.T, m *Manager, person, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := m.Status(context.Background(), person)
		if err == nil && strings.Contains(st.Error, want) && (want != "" || st.Error == "") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pod error = %q (%v), want %q", st.Error, err, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A start is the engine's: the caller going away ends its wait, not the
// start, which runs on its own deadline and records and logs its failure.
func TestEnsureOutlivesItsCaller(t *testing.T) {
	m, h, _ := newManager(t)
	var logs lockedBuffer
	m.Log = slog.New(slog.NewTextHandler(&logs, nil))
	h.hold = make(chan struct{})
	h.ensureE = errors.New("no room on the node")
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() { ended <- m.Ensure(ctx, "usr1", nil) }()
	for {
		h.mu.Lock()
		started := h.ensureCtx != nil
		h.mu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-ended; !errors.Is(err, context.Canceled) {
		t.Errorf("the caller's wait = %v", err)
	}
	h.mu.Lock()
	sctx := h.ensureCtx
	h.mu.Unlock()
	if sctx.Err() != nil {
		t.Errorf("the start was cancelled with its caller: %v", sctx.Err())
	}
	if d, ok := sctx.Deadline(); !ok || time.Until(d) < DefaultStartTimeout-time.Minute {
		t.Errorf("the start's deadline = %v, %v", d, ok)
	}
	close(h.hold)
	podError(t, m, "usr1", "no room on the node")
	if !strings.Contains(logs.String(), "assistant: start a pod") || !strings.Contains(logs.String(), "no room on the node") {
		t.Errorf("the failed start is not logged: %s", logs.String())
	}

	// The engine's deadline ends a start that takes too long.
	m.StartTimeout = 20 * time.Millisecond
	h.hold = make(chan struct{})
	if err := m.Ensure(context.Background(), "usr2", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a start past its deadline = %v", err)
	}
	if st, _ := m.Status(context.Background(), "usr2"); st.Error == "" {
		t.Errorf("the timed-out start left no error: %+v", st)
	}
}

// A failed start is the pod's error until the next start, a stop, or a
// health check that finds the pod well.
func TestAPodErrorClears(t *testing.T) {
	m, h, g := newManager(t)
	ctx := context.Background()
	if err := m.Ensure(ctx, "usr1", nil); err != nil {
		t.Fatal(err)
	}
	// A later start fails with the pod up and checked: found well, over.
	g.attachE = errors.New("gateway down")
	if err := m.Ensure(ctx, "usr1", nil); err == nil {
		t.Fatal("the gateway's failure was not the start's")
	}
	if st, _ := m.Status(ctx, "usr1"); st.State != assistantport.PodRunning || st.Error != "" {
		t.Errorf("a healthy pod kept its error: %+v", st)
	}
	// One whose pod was never checked keeps it: the next start retries.
	g.attachE = nil
	h.ensureE = errors.New("no room on the node")
	if err := m.Ensure(ctx, "usr2", nil); err == nil {
		t.Fatal("the pod's failure was not the start's")
	}
	podError(t, m, "usr2", "no room on the node")
	h.ensureE = nil
	if err := m.Ensure(ctx, "usr2", nil); err != nil {
		t.Fatal(err)
	}
	podError(t, m, "usr2", "")
	// Stopped idle: its error is over.
	h.landlock = 3
	h.pods["usr3"], h.running["usr3"] = testCfg.Image, true
	if err := m.Ensure(ctx, "usr3", nil); !errors.Is(err, assistantport.ErrNoLandlock) {
		t.Fatalf("no Landlock = %v", err)
	}
	now := time.Now().Add(time.Hour)
	m.Now = func() time.Time { return now }
	m.mu.Lock()
	m.persons["usr3"].used = now.Add(-m.IdleAfter)
	m.mu.Unlock()
	m.Sweep(ctx)
	m.mu.Lock()
	left := m.persons["usr3"].err
	m.mu.Unlock()
	if left != "" || !slices.Contains(h.stopped, "usr3") {
		t.Errorf("stopped idle: err %q, stopped %v", left, h.stopped)
	}
	// A failed start's pod that does not answer keeps its error.
	m.mu.Lock()
	m.persons["usr3"].err, m.persons["usr3"].confined = "slow", true
	m.mu.Unlock()
	h.pods["usr3"], h.running["usr3"] = testCfg.Image, true
	h.agent.Close()
	if st, _ := m.Status(ctx, "usr3"); st.Error != "slow" {
		t.Errorf("an unanswering pod lost its error: %+v", st)
	}
}

func TestEnsureRefusesAnUnconfinedNode(t *testing.T) {
	m, h, _ := newManager(t)
	h.landlock = 3
	if err := m.Ensure(context.Background(), "usr1", nil); !errors.Is(err, assistantport.ErrNoLandlock) {
		t.Errorf("no Landlock = %v", err)
	}
	if st, _ := m.Status(context.Background(), "usr1"); st.Error == "" {
		t.Errorf("status hides the refusal: %+v", st)
	}
	g := &fakeGateway{attachE: errors.New("gateway down")}
	m.Gateway = g
	if err := m.Ensure(context.Background(), "usr2", nil); err == nil || !strings.Contains(err.Error(), "gateway down") {
		t.Errorf("gateway failure = %v", err)
	}
	none := &Manager{}
	if err := none.Ensure(context.Background(), "usr1", nil); !errors.Is(err, assistantport.ErrNoHost) {
		t.Errorf("no host = %v", err)
	}
	if st, _ := none.Status(context.Background(), "usr1"); st.State != assistantport.PodAbsent {
		t.Errorf("no host status = %+v", st)
	}
}

// A session's rules: its repositories and nothing beside, its home and
// nothing else written, its harness and the pod's tools run, never a shell.
func TestStartRequestRules(t *testing.T) {
	m, _, _ := newManager(t)
	api := assistantport.Repo{Workshop: "w1abcdef", WorkshopName: "acme", Name: "api"}
	web := assistantport.Repo{Workshop: "w1abcdef", WorkshopName: "acme", Name: "web"}
	req, err := m.StartRequest("usr1", assistantport.Session{ID: "s1", Repos: []assistantport.Repo{api},
		Argv: []string{toolsMusl + "/libexec/opencode", "acp"}, Env: map[string]string{"ANTHROPIC_API_KEY": "k"}})
	if err != nil {
		t.Fatal(err)
	}
	read, write, exe := req.Confine.Read, req.Confine.Write, req.Confine.Exec
	if !slices.Contains(read, Folder(api)) || slices.Contains(read, Folder(web)) || slices.Contains(read, reposRoot) || slices.Contains(read, stateRoot) {
		t.Errorf("read = %v", read)
	}
	if !slices.Equal(write, []string{"/state/s1"}) {
		t.Errorf("write = %v", write)
	}
	for _, sh := range []string{"/bin/sh", "/bin/busybox", "/bin/bash"} {
		if slices.Contains(exe, sh) {
			t.Errorf("exec lists %s", sh)
		}
	}
	for _, want := range []string{gitBin, rgBin, shimBin, loader, toolsMusl + "/libexec/opencode"} {
		if !slices.Contains(exe, want) {
			t.Errorf("exec lacks %s: %v", want, exe)
		}
	}
	env := req.Env
	if env["HOME"] != "/state/s1" || env["TMPDIR"] != "/state/s1/tmp" || env["ANTHROPIC_API_KEY"] != "k" {
		t.Errorf("env = %v", env)
	}
	if req.Cwd != WorkshopFolder(api) {
		t.Errorf("cwd = %v", req.Cwd)
	}
	// A skill's session with no repository works in its home.
	req, _ = m.StartRequest("usr1", assistantport.Session{ID: "s2", Argv: []string{"/x/h"}})
	if req.Cwd != "/state/s2" {
		t.Errorf("no repositories: cwd = %v", req.Cwd)
	}
	for name, s := range map[string]assistantport.Session{
		"an id that is a path": {ID: "../s1", Argv: []string{"/x/h"}},
		"no id":                {Argv: []string{"/x/h"}},
		"a relative harness":   {ID: "s3", Argv: []string{"opencode"}},
		"a relative program":   {ID: "s3", Argv: []string{"/x/h"}, Exec: []string{"sh"}},
	} {
		if _, err := m.StartRequest("usr1", s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	cmd, args := GitServer("s1", []assistantport.Repo{api, web})
	if cmd != shimBin || !slices.Contains(args, "api="+Folder(api)) || !slices.Contains(args, "/state/s1") {
		t.Errorf("git server = %s %v", cmd, args)
	}
}

// A fetch opens the repository hosts for its length only, and the
// credential rides its stdin.
func TestFetchOpensTheHostsForItsLength(t *testing.T) {
	m, h, g := newManager(t)
	ctx := context.Background()
	if err := m.Ensure(ctx, "usr1", []string{"api.anthropic.com"}); err != nil {
		t.Fatal(err)
	}
	api := assistantport.Repo{Workshop: "w1abcdef", WorkshopName: "acme", Name: "api", URL: "https://GitHub.com/acme/api.git"}
	web := assistantport.Repo{Workshop: "w1abcdef", WorkshopName: "acme", Name: "web", URL: "https://gitlab.example.com/acme/web.git"}
	h.fetchOut = `{"dir":"` + Folder(api) + `","ok":true,"head":"abc"}` + "\n" + `{"dir":"` + Folder(web) + `","ok":false,"error":"clone: 404"}` + "\n"
	states, err := m.Fetch(ctx, "usr1", []assistantport.FetchItem{{Repo: api, Cred: assistantport.Credential{User: "bot", Pass: "s3cret"}}, {Repo: web}})
	if err != nil {
		t.Fatal(err)
	}
	if !states[0].OK || states[0].Head != "abc" || states[1].OK || states[1].Error != "clone: 404" || states[1].Name != "web" {
		t.Errorf("states = %+v", states)
	}
	n := len(g.lists)
	if n < 3 || !slices.Equal(g.lists[n-2], []string{"api.anthropic.com", "github.com", "gitlab.example.com"}) ||
		!slices.Equal(g.lists[n-1], []string{"api.anthropic.com"}) {
		t.Errorf("allow lists = %v", g.lists)
	}
	last := h.execs[len(h.execs)-1]
	// The credential rides api's entry alone: web's host never sees it.
	var sent struct {
		Repos []struct{ URL, Pass string } `json:"repos"`
	}
	json.Unmarshal(h.stdin[len(h.stdin)-1], &sent)
	if len(sent.Repos) != 2 || sent.Repos[0].Pass != "s3cret" || sent.Repos[1].Pass != "" {
		t.Errorf("credentials per repository = %+v", sent.Repos)
	}
	if last[0] != gitContainer || slices.ContainsFunc(last, func(a string) bool { return strings.Contains(a, "s3cret") }) {
		t.Errorf("fetch exec = %v, stdin %s", last, h.stdin[len(h.stdin)-1])
	}
	// Closed again on failure too.
	h.fetchErr = errors.New("exec refused")
	if _, err := m.Fetch(ctx, "usr1", []assistantport.FetchItem{{Repo: api}}); err == nil {
		t.Error("a failed exec succeeded")
	}
	if l := g.lists[len(g.lists)-1]; !slices.Equal(l, []string{"api.anthropic.com"}) {
		t.Errorf("after a failed fetch, the allow list is %v", l)
	}
	// A widening that answers an error is closed again all the same.
	h.fetchErr = nil
	g.allowErr = errors.New("timeout")
	if _, err := m.Fetch(ctx, "usr1", []assistantport.FetchItem{{Repo: api}}); err == nil {
		t.Error("a fetch whose widening failed went on")
	}
	g.allowErr = nil
	if l := g.lists[len(g.lists)-1]; !slices.Equal(l, []string{"api.anthropic.com"}) {
		t.Errorf("after a failed widening, the allow list is %v", l)
	}
	if _, err := m.Fetch(ctx, "usr1", []assistantport.FetchItem{{Repo: assistantport.Repo{Name: "x", URL: "not a url"}}}); err == nil {
		t.Error("a URL with no host was fetched")
	}
}

func TestSweepStopsIdlePodsAndOldVolumes(t *testing.T) {
	m, h, _ := newManager(t)
	ctx := context.Background()
	now := time.Now()
	m.Now = func() time.Time { return now }
	for _, p := range []string{"idle", "busy", "fresh"} {
		if err := m.Ensure(ctx, p, nil); err != nil {
			t.Fatal(err)
		}
	}
	done := m.Busy("busy")
	defer done()
	now = now.Add(11 * time.Minute)
	m.touch("fresh")
	m.Sweep(ctx)
	if !slices.Equal(h.stopped, []string{"idle"}) {
		t.Errorf("stopped = %v", h.stopped)
	}
	// A volume unused for longer than VolumeAfter, its pod gone, is deleted.
	h.volumes["gone"] = now.Add(-31 * 24 * time.Hour)
	h.volumes["recent"] = now.Add(-24 * time.Hour)
	m.Sweep(ctx)
	if !slices.Equal(h.deleted, []string{"gone"}) {
		t.Errorf("deleted = %v", h.deleted)
	}
}

func TestFilesGoThroughTheAgentHost(t *testing.T) {
	m, h, _ := newManager(t)
	ctx := context.Background()
	if err := m.WriteFile(ctx, "usr1", "/state/s1/.claude/settings.json", []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if h.files["/state/s1/.claude/settings.json"] != "{}" {
		t.Errorf("files = %v", h.files)
	}
	if err := m.WriteFile(ctx, "usr1", "/workshops/x", nil, 0o600); err == nil {
		t.Error("a write outside /state was sent")
	}
	if b, err := m.ReadFile(ctx, "usr1", "/state/s1/.claude/settings.json"); err != nil || string(b) != "{}" {
		t.Errorf("read = %q, %v", b, err)
	}
	if _, err := m.ReadFile(ctx, "usr1", "/state/none"); err == nil {
		t.Error("a missing file was read")
	}
	ah, err := m.AgentHost(ctx, "usr1")
	if err != nil || ah.Token != "t0k" {
		t.Fatal(ah, err)
	}
	c, err := ah.Dialer.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	_ = io.EOF
}

// gwHost is an egressgw.Host whose gateway is an HTTP server recording the
// policies it is given.
type gwHost struct {
	srv      *httptest.Server
	policies []map[string]any
	runs     []string
	projects []string
}

func newGWHost(t *testing.T) *gwHost {
	g := &gwHost{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /routes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"peers":{}}`)) })
	mux.HandleFunc("PUT /runs/{run}", func(w http.ResponseWriter, r *http.Request) {
		g.runs = append(g.runs, r.PathValue("run"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /policy", func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		g.policies = append(g.policies, p)
		w.WriteHeader(http.StatusNoContent)
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *gwHost) EnsureGateway(_ context.Context, project, _ string) (egressgw.Control, error) {
	g.projects = append(g.projects, project)
	return egressgw.Control{BaseURL: g.srv.URL}, nil
}
func (g *gwHost) Control(context.Context, string) (egressgw.Control, bool, error) {
	return egressgw.Control{BaseURL: g.srv.URL}, true, nil
}
func (g *gwHost) AttachRun(_ context.Context, _, _, run string, _ map[string]string) (egressgw.Attachment, error) {
	return egressgw.Attachment{GatewayIP: "10.200.0.1", WGEndpoint: "10.43.0.9:51820", WGSecret: egressgw.WGSecretName(run)}, nil
}
func (g *gwHost) DetachRun(context.Context, string, string) error { return nil }
func (g *gwHost) RunProject(context.Context, string) (string, bool, error) {
	return "", false, nil
}

// The person's gateway is its own key, never a workshop's, and its pod is
// its one peer; Allow replaces its allow list.
func TestEgressGateway(t *testing.T) {
	gh := newGWHost(t)
	g := EgressGateway{M: egressgw.NewManager(gh), EngineIP: func(context.Context) (string, error) { return "10.43.0.2", nil },
		Base: policyfile.File{AllowedCIDRs: []string{"10.1.0.0/16"}, AllowedPorts: []int{8443}}}
	ctx := context.Background()
	tn, err := g.Attach(ctx, "USR1", []string{"api.anthropic.com"})
	if err != nil {
		t.Fatal(err)
	}
	if tn.EndpointIP != "10.43.0.9" || tn.EngineIP != "10.43.0.2" || tn.Secret != "apo-wg-assistant-usr1" || tn.GatewayIP != "10.200.0.1" {
		t.Errorf("tunnel = %+v", tn)
	}
	if !slices.Equal(gh.projects, []string{"assistant-usr1"}) || !slices.Equal(gh.runs, []string{"assistant-usr1"}) {
		t.Errorf("gateway %v, peer %v", gh.projects, gh.runs)
	}
	if err := g.Allow(ctx, "USR1", []string{"api.anthropic.com", "github.com"}); err != nil {
		t.Fatal(err)
	}
	if p := gh.policies[len(gh.policies)-1]; p["mode"] != "allow" || len(p["domains"].([]any)) != 2 || len(p["allowed_cidrs"].([]any)) != 1 {
		t.Errorf("policy = %v", p)
	}
	bad := EgressGateway{M: egressgw.NewManager(gh), EngineIP: func(context.Context) (string, error) { return "", errors.New("no engine") }}
	if _, err := bad.Attach(ctx, "usr1", nil); err == nil {
		t.Error("an engine with no address attached")
	}
}

func TestRunSweepsUntilCancelled(t *testing.T) {
	m, h, _ := newManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Ensure(ctx, "idle", nil); err != nil {
		t.Fatal(err)
	}
	m.IdleAfter = time.Nanosecond
	done := make(chan struct{})
	go func() { m.Run(ctx, time.Millisecond); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		n := len(h.stopped)
		h.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if len(h.stopped) == 0 {
		t.Error("Run never stopped the idle pod")
	}
	(&Manager{}).Sweep(ctx) // no host: nothing to do
	if m.Home("s") != Home("s") || m.Folder(assistantport.Repo{Name: "r"}) == "" || m.log() == nil {
		t.Error("helpers")
	}
	if _, err := (&Manager{}).AgentHost(ctx, "x"); !errors.Is(err, assistantport.ErrNoHost) {
		t.Error("no host dials")
	}
	if _, err := (&Manager{}).Fetch(ctx, "x", nil); !errors.Is(err, assistantport.ErrNoHost) {
		t.Error("no host fetches")
	}
	if states, err := m.Fetch(ctx, "idle", nil); err != nil || states != nil {
		t.Error("fetching nothing")
	}
}

// A fetch whose answer is not the shim's is an error, and a repository it
// did not answer for is marked so.
func TestFetchReadsItsAnswerStrictly(t *testing.T) {
	m, h, _ := newManager(t)
	ctx := context.Background()
	m.Protocols = []string{"https", "file"}
	api := assistantport.Repo{Workshop: "w1abcdef", WorkshopName: "acme", Name: "api", URL: "https://h/a.git"}
	h.fetchOut = "not json"
	if _, err := m.Fetch(ctx, "usr1", []assistantport.FetchItem{{Repo: api}}); err == nil {
		t.Error("a garbled answer was read")
	}
	h.fetchOut = ""
	states, err := m.Fetch(ctx, "usr1", []assistantport.FetchItem{{Repo: api}})
	if err != nil || states[0].OK || states[0].Error == "" {
		t.Errorf("no answer = %+v, %v", states, err)
	}
	if last := h.execs[len(h.execs)-1]; !slices.Contains(last, "https,file") {
		t.Errorf("protocols = %v", last)
	}
}

// A sweep decides under the person's lock: a fetch in flight keeps the pod.
func TestSweepWaitsForAFetch(t *testing.T) {
	m, h, _ := newManager(t)
	ctx := context.Background()
	now := time.Now()
	m.Now = func() time.Time { return now }
	if err := m.Ensure(ctx, "usr1", nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	unlock := m.lock("usr1")
	done := make(chan struct{})
	go func() { m.Sweep(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	end := m.Busy("usr1") // a fetch began while the sweep waited
	unlock()
	<-done
	end()
	if len(h.stopped) != 0 {
		t.Errorf("a pod with a fetch in flight was stopped: %v", h.stopped)
	}
}
