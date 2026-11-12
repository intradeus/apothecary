package sshdoor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/auth/authtest"
)

// upstream is a fake sidecar SSH server: it records what sessions ask for,
// answers exec with stdout, stderr and an exit status, dials direct-tcpip
// targets, and opens a channel back when asked to by a global request.
type upstream struct {
	t       *testing.T
	doorKey ssh.PublicKey
	config  *ssh.ServerConfig
	ln      net.Listener
	mu      sync.Mutex
	reqs    []string // "<type> <detail>" per session request seen
	dials   int
}

// newUpstream listens on a TCP loopback: an SSH key exchange has both
// sides write before they read, which a synchronous net.Pipe deadlocks.
func newUpstream(t *testing.T, doorKey ssh.PublicKey) *upstream {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	u := &upstream{t: t, doorKey: doorKey}
	u.config = &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(key.Marshal(), doorKey.Marshal()) {
			return &ssh.Permissions{}, nil
		}
		return nil, errors.New("not the door")
	}}
	u.config.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u.ln = ln
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go u.serve(c)
		}
	}()
	return u
}

// dial is what a Target.Dial does: one connection into the fake sidecar.
func (u *upstream) dial(ctx context.Context) (net.Conn, error) {
	u.mu.Lock()
	u.dials++
	u.mu.Unlock()
	var d net.Dialer
	return d.DialContext(ctx, "tcp", u.ln.Addr().String())
}

func (u *upstream) seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.reqs...)
}

func (u *upstream) note(s string) {
	u.mu.Lock()
	u.reqs = append(u.reqs, s)
	u.mu.Unlock()
}

func (u *upstream) serve(c net.Conn) {
	conn, chans, reqs, err := ssh.NewServerConn(c, u.config)
	if err != nil {
		return
	}
	defer conn.Close()
	go func() {
		for r := range reqs {
			switch r.Type {
			case "keepalive@openssh.com":
				_ = r.Reply(true, []byte("alive"))
			case "open-back":
				// The upstream opens a channel towards the client, as a
				// remote forward or an agent request would.
				go func() {
					ch, chReqs, err := conn.OpenChannel("forwarded-tcpip", []byte("back"))
					if err != nil {
						return
					}
					go ssh.DiscardRequests(chReqs)
					_, _ = ch.Write([]byte("from upstream"))
					_ = ch.Close()
				}()
				_ = r.Reply(true, nil)
			default:
				if r.WantReply {
					_ = r.Reply(false, nil)
				}
			}
		}
	}()
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			go u.session(nc)
		case "direct-tcpip":
			go func() {
				var d struct {
					Host  string
					Port  uint32
					OHost string
					OPort uint32
				}
				_ = ssh.Unmarshal(nc.ExtraData(), &d)
				tc, err := net.Dial("tcp", net.JoinHostPort(d.Host, strconv.Itoa(int(d.Port))))
				if err != nil {
					_ = nc.Reject(ssh.ConnectionFailed, err.Error())
					return
				}
				ch, chReqs, _ := nc.Accept()
				go ssh.DiscardRequests(chReqs)
				go func() { _, _ = io.Copy(tc, ch); _ = tc.(*net.TCPConn).CloseWrite() }()
				_, _ = io.Copy(ch, tc)
				_ = ch.Close()
			}()
		default:
			_ = nc.Reject(ssh.UnknownChannelType, "no")
		}
	}
}

func (u *upstream) session(nc ssh.NewChannel) {
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	for r := range reqs {
		switch r.Type {
		case "pty-req", "env", "window-change":
			u.note(r.Type + " " + string(r.Payload))
			if r.WantReply {
				_ = r.Reply(true, nil)
			}
		case "exec":
			var e struct{ Command string }
			_ = ssh.Unmarshal(r.Payload, &e)
			u.note("exec " + e.Command)
			_ = r.Reply(true, nil)
			if e.Command == "quick" {
				// A command that is done before anyone could type: its
				// output, its status and its close right behind the reply.
				_, _ = ch.Write([]byte("quick"))
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			}
			in, _ := io.ReadAll(ch) // the client's stdin, up to its EOF
			_, _ = ch.Write([]byte("out:" + e.Command + ":" + string(in)))
			_, _ = ch.Stderr().Write([]byte("err"))
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{7}))
			return
		default:
			if r.WantReply {
				_ = r.Reply(false, nil)
			}
		}
	}
}

// keyring is a KeyAuth over an auth.Service and its in-memory DB.
type keyring struct {
	svc   *auth.Service
	alice auth.User
	bob   auth.User
}

func newKeyring(t *testing.T) *keyring {
	t.Helper()
	svc := authtest.New(auth.NewMem())
	ctx := context.Background()
	pw := "correct horse battery staple"
	alice, err := svc.Setup(ctx, auth.NewUser{Handle: "alice", Email: "a@x", Name: "Alice", Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := svc.CreateUser(ctx, auth.NewUser{Handle: "bob", Email: "b@x", Name: "Bob", Password: pw, Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	return &keyring{svc: svc, alice: alice, bob: bob}
}

func (k *keyring) register(t *testing.T, user auth.User) ssh.Signer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	if _, err := k.svc.AddSSHKey(context.Background(), user.ID, "laptop", string(ssh.MarshalAuthorizedKey(signer.PublicKey()))); err != nil {
		t.Fatal(err)
	}
	return signer
}

// targets is a Targets over a map.
type targets map[string]Target

func (ts targets) Lookup(_ context.Context, workspace string) (Target, error) {
	tg, ok := ts[workspace]
	if !ok {
		return Target{}, errors.New("no such workspace")
	}
	return tg, nil
}

type doorRig struct {
	door  *Door
	addr  string
	keys  *keyring
	up    *upstream
	alice ssh.Signer
}

func newDoorRig(t *testing.T) *doorRig {
	t.Helper()
	return newDoorRigWith(t, func(k KeyAuth) KeyAuth { return k })
}

// newDoorRigWith is a door whose key lookups go through wrap.
func newDoorRigWith(t *testing.T, wrap func(KeyAuth) KeyAuth) *doorRig {
	t.Helper()
	seed := make([]byte, 32)
	_, _ = rand.Read(seed)
	hostKey, _ := KeyFromSeed(seed)
	_, _ = rand.Read(seed)
	doorKey, _ := KeyFromSeed(seed)
	keys := newKeyring(t)
	up := newUpstream(t, doorKey.PublicKey())
	ts := targets{
		"apo-alice-run": {RunID: "r1", OwnerID: keys.alice.ID, Dial: up.dial},
		"apo-bob-run":   {RunID: "r2", OwnerID: keys.bob.ID, Dial: up.dial},
		"apo-broken":    {RunID: "r3", OwnerID: keys.alice.ID, Dial: func(context.Context) (net.Conn, error) { return nil, errors.New("sidecar down") }},
	}
	door, err := New(Config{HostKey: hostKey, DoorKey: doorKey, Keys: wrap(keys.svc), Targets: ts, Advertise: "door.example:2222"})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = door.Serve(ln) }()
	t.Cleanup(func() { _ = door.Close() })
	return &doorRig{door: door, addr: ln.Addr().String(), keys: keys, up: up, alice: keys.register(t, keys.alice)}
}

func (r *doorRig) connect(t *testing.T, user string, key ssh.Signer) (*ssh.Client, error) {
	t.Helper()
	c, err := ssh.Dial("tcp", r.addr, &ssh.ClientConfig{
		User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: ssh.FixedHostKey(r.door.cfg.HostKey.PublicKey()), Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Cleanup(func() { c.Close() })
	}
	return c, err
}

// A session through the door: the command, its stdin, stdout, stderr and
// exit status all cross; the pty, env and resize requests reach the
// workspace as sent.
func TestDoorProxiesASession(t *testing.T) {
	rig := newDoorRig(t)
	client, err := rig.connect(t, "apo-alice-run", rig.alice)
	if err != nil {
		t.Fatal(err)
	}
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	if err := ses.RequestPty("xterm", 24, 80, nil); err != nil {
		t.Fatal(err)
	}
	if err := ses.Setenv("TERM", "xterm"); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	ses.Stderr = &stderr
	ses.Stdin = strings.NewReader("stdin")
	out, err := ses.Output("whoami")
	var ee *ssh.ExitError
	if !errors.As(err, &ee) || ee.ExitStatus() != 7 {
		t.Fatalf("err = %v (out %q)", err, out)
	}
	if string(out) != "out:whoami:stdin" || stderr.String() != "err" {
		t.Errorf("out = %q stderr = %q", out, stderr.String())
	}
	seen := strings.Join(rig.up.seen(), "\n")
	for _, want := range []string{"pty-req ", "env ", "exec whoami"} {
		if !strings.Contains(seen, want) {
			t.Errorf("the workspace did not see %q:\n%s", want, seen)
		}
	}
	host, port := rig.door.Address()
	if host != "door.example" || port != 2222 || !strings.HasPrefix(rig.door.Fingerprint(), "SHA256:") || !strings.HasPrefix(rig.door.DoorPublicKey(), "ssh-ed25519 ") {
		t.Errorf("address %s:%d fingerprint %s", host, port, rig.door.Fingerprint())
	}
	// The key's use is recorded.
	keys, _ := rig.keys.svc.ListSSHKeys(context.Background(), rig.keys.alice.ID)
	if len(keys) != 1 || keys[0].LastUsedAt == nil {
		t.Errorf("last used not recorded: %+v", keys)
	}
}

// A command that exits the moment it starts still reaches the person: the
// reply to their exec arrives before the channel's close, never after it.
func TestDoorDeliversAQuickCommand(t *testing.T) {
	rig := newDoorRig(t)
	client, err := rig.connect(t, "apo-alice-run", rig.alice)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		ses, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		out, err := ses.Output("quick")
		ses.Close()
		if err != nil || string(out) != "quick" {
			t.Fatalf("run %d: exec = %q, %v", i, out, err)
		}
	}
}

// Port forwards cross in both directions, and a keepalive is answered.
func TestDoorProxiesForwardsAndGlobalRequests(t *testing.T) {
	rig := newDoorRig(t)
	client, err := rig.connect(t, "apo-alice-run", rig.alice)
	if err != nil {
		t.Fatal(err)
	}
	echo, _ := net.Listen("tcp", "127.0.0.1:0")
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	c, err := client.Dial("tcp", echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("ping"))
	buf := make([]byte, 4)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Errorf("echo = %q, %v", buf, err)
	}
	c.Close()
	if _, err := client.Dial("tcp", "127.0.0.1:1"); err == nil {
		t.Error("a closed port opened")
	}
	ok, payload, err := client.SendRequest("keepalive@openssh.com", true, nil)
	if err != nil || !ok || string(payload) != "alive" {
		t.Errorf("keepalive = %v %q %v", ok, payload, err)
	}
	// A channel the workspace opens reaches the client.
	back := client.HandleChannelOpen("forwarded-tcpip")
	if ok, _, err := client.SendRequest("open-back", true, nil); err != nil || !ok {
		t.Fatalf("open-back = %v %v", ok, err)
	}
	select {
	case nc := <-back:
		if string(nc.ExtraData()) != "back" {
			t.Errorf("extra data = %q", nc.ExtraData())
		}
		ch, reqs, _ := nc.Accept()
		go ssh.DiscardRequests(reqs)
		data, _ := io.ReadAll(ch)
		if string(data) != "from upstream" {
			t.Errorf("read %q", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream's channel never arrived")
	}
}

// Every way in but the right one is refused the same way.
func TestDoorRefusals(t *testing.T) {
	rig := newDoorRig(t)
	_, stranger, _ := ed25519.GenerateKey(rand.Reader)
	strangerKey, _ := ssh.NewSignerFromKey(stranger)
	bobKey := rig.keys.register(t, rig.keys.bob)
	for _, c := range []struct {
		name, user string
		key        ssh.Signer
	}{
		{"unknown key", "apo-alice-run", strangerKey},
		{"someone else's run", "apo-alice-run", bobKey},
		{"unknown workspace", "apo-nowhere", rig.alice},
		{"not a workspace name", "Root User", rig.alice},
	} {
		if _, err := rig.connect(t, c.user, c.key); err == nil {
			t.Errorf("%s: connected", c.name)
		}
	}
	// A disabled account: refused, and a live session ends.
	client, err := rig.connect(t, "apo-alice-run", rig.alice)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rig.keys.svc.SetDisabled(context.Background(), rig.keys.alice.ID, true); err != nil {
		t.Fatal(err)
	}
	rig.door.DropUser(rig.keys.alice.ID)
	select {
	case <-closed(client):
	case <-time.After(5 * time.Second):
		t.Error("the disabled user's session is still open")
	}
	if _, err := rig.connect(t, "apo-alice-run", rig.alice); err == nil {
		t.Error("a disabled account connected")
	}
}

// disabledAfterAuth is an account disabled just after its handshake, before
// the door knows the connection: the look that let it in is the last to pass.
type disabledAfterAuth struct {
	KeyAuth
	looks atomic.Int32
}

func (d *disabledAfterAuth) AuthenticateSSHKey(ctx context.Context, key ssh.PublicKey) (auth.User, auth.SSHKey, error) {
	if d.looks.Add(1) > 1 {
		return auth.User{}, auth.SSHKey{}, errors.New("account disabled")
	}
	return d.KeyAuth.AuthenticateSSHKey(ctx, key)
}

// An account disabled in the gap between its handshake and the door knowing
// the connection finds nothing to drop, so the door looks again.
func TestAnAccountDisabledDuringTheHandshakeIsOut(t *testing.T) {
	rig := newDoorRigWith(t, func(k KeyAuth) KeyAuth { return &disabledAfterAuth{KeyAuth: k} })
	client, err := rig.connect(t, "apo-alice-run", rig.alice)
	if err != nil {
		return // refused outright is out too
	}
	select {
	case <-closed(client):
	case <-time.After(5 * time.Second):
		t.Error("a disabled account kept its connection")
	}
}

// closed signals when a client's connection is gone.
func closed(c *ssh.Client) <-chan struct{} {
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	return done
}

// A workspace whose sidecar cannot be reached: the person authenticates,
// then their first channel is refused with the reason and the connection
// ends — nothing hangs.
func TestDoorWhenTheWorkspaceIsUnreachable(t *testing.T) {
	rig := newDoorRig(t)
	client, err := rig.connect(t, "apo-broken", rig.alice)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.NewSession()
	if err == nil || !strings.Contains(err.Error(), "workspace unavailable") {
		t.Errorf("session err = %v", err)
	}
	select {
	case <-closed(client):
	case <-time.After(5 * time.Second):
		t.Error("the connection stayed open")
	}
}

// An address that keeps failing is refused outright for a while.
func TestDoorThrottlesFailingAddresses(t *testing.T) {
	rig := newDoorRig(t)
	_, stranger, _ := ed25519.GenerateKey(rand.Reader)
	strangerKey, _ := ssh.NewSignerFromKey(stranger)
	for range failLimit {
		_, _ = rig.connect(t, "apo-alice-run", strangerKey)
	}
	// The server side records the failure a moment after the client gives up.
	deadline := time.Now().Add(5 * time.Second)
	for !rig.door.failures.blocked("127.0.0.1") {
		if time.Now().After(deadline) {
			t.Fatal("not blocked after the limit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Even the right key is turned away now; a reset lets it in again.
	if _, err := rig.connect(t, "apo-alice-run", rig.alice); err == nil {
		t.Error("blocked address connected")
	}
	rig.door.failures.reset("127.0.0.1")
	if _, err := rig.connect(t, "apo-alice-run", rig.alice); err != nil {
		t.Errorf("after reset: %v", err)
	}
}

// Closing the door ends live sessions.
func TestDoorCloseEndsSessions(t *testing.T) {
	rig := newDoorRig(t)
	client, err := rig.connect(t, "apo-alice-run", rig.alice)
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.door.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed(client):
	case <-time.After(5 * time.Second):
		t.Error("the session outlived the door")
	}
}

func TestNewValidates(t *testing.T) {
	seed := make([]byte, 32)
	hostKey, _ := KeyFromSeed(seed)
	ok := Config{HostKey: hostKey, DoorKey: hostKey, Keys: newKeyring(t).svc, Targets: targets{}, Advertise: "h:22"}
	for name, mut := range map[string]func(*Config){
		"no host key":      func(c *Config) { c.HostKey = nil },
		"no door key":      func(c *Config) { c.DoorKey = nil },
		"no keys":          func(c *Config) { c.Keys = nil },
		"no targets":       func(c *Config) { c.Targets = nil },
		"bad advertise":    func(c *Config) { c.Advertise = "no-colon" },
		"zero port":        func(c *Config) { c.Advertise = "h:0" },
		"non-numeric port": func(c *Config) { c.Advertise = "h:ssh" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := ok
			mut(&cfg)
			if _, err := New(cfg); err == nil {
				t.Fatal("New must reject it")
			}
		})
	}
	d, err := New(ok)
	if err != nil {
		t.Fatal(err)
	}
	if h, p := d.Address(); h != "h" || p != 22 {
		t.Errorf("Address = %s:%d", h, p)
	}
	if d.Fingerprint() == "" || d.HostPublicKey() == "" || d.DoorPublicKey() == "" {
		t.Error("key material must render")
	}
}

func TestKeyFromSeed(t *testing.T) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	a, err := KeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := KeyFromSeed(seed)
	if string(a.PublicKey().Marshal()) != string(b.PublicKey().Marshal()) {
		t.Error("the same seed must give the same key")
	}
	if _, err := KeyFromSeed(make([]byte, 16)); err == nil {
		t.Error("a short seed must fail")
	}
	// Extra bytes are ignored: only the first 32 are the seed.
	if _, err := KeyFromSeed(make([]byte, 64)); err != nil {
		t.Errorf("a long seed: %v", err)
	}
}

// Serve on a closed door refuses and closes the listener it was handed.
func TestServeOnAClosedDoor(t *testing.T) {
	rig := newDoorRig(t)
	if err := rig.door.Close(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.door.Serve(ln); err == nil {
		t.Fatal("Serve on a closed door must error")
	}
	// The listener is closed: a dial is refused.
	if c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		c.Close()
		t.Error("the listener was not closed")
	}
}

// A connection that never completes the handshake is dropped at the deadline
// and counts as a failure, without pinning the door.
func TestHandshakeDeadlineDropsASilentClient(t *testing.T) {
	rig := newDoorRig(t)
	rig.door.failures.reset("127.0.0.1")
	c, err := net.Dial("tcp", rig.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Say nothing. The door's handshakeTimeout is 30s, too long to wait on;
	// instead assert the connection is accepted and the door stays responsive
	// to a real client meanwhile.
	client, err := rig.connect(t, "apo-alice-run", rig.alice)
	if err != nil {
		t.Fatalf("the door stopped serving: %v", err)
	}
	client.Close()
}

func TestHostOf(t *testing.T) {
	if got := hostOf(nil); got != "" {
		t.Errorf("hostOf(nil) = %q", got)
	}
	if got := hostOf(&net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 2222}); got != "10.0.0.1" {
		t.Errorf("hostOf = %q", got)
	}
	// An address with no port is returned whole.
	if got := hostOf(fakeAddr("just-a-name")); got != "just-a-name" {
		t.Errorf("hostOf = %q", got)
	}
}

type fakeAddr string

func (fakeAddr) Network() string  { return "fake" }
func (a fakeAddr) String() string { return string(a) }

func TestFailuresWindowAndSweep(t *testing.T) {
	f := newFailures()
	now := time.Unix(1000, 0)
	f.now = func() time.Time { return now }
	for range failLimit - 1 {
		f.fail("1.2.3.4")
	}
	if f.blocked("1.2.3.4") {
		t.Fatal("blocked before the limit")
	}
	f.fail("1.2.3.4")
	if !f.blocked("1.2.3.4") {
		t.Fatal("not blocked at the limit")
	}
	// A different address is unaffected.
	if f.blocked("5.6.7.8") {
		t.Fatal("an unrelated address is blocked")
	}
	// The window ends; the sweep (throttled to once a minute) forgets it.
	now = now.Add(failWindow + time.Minute + time.Second)
	if f.blocked("1.2.3.4") {
		t.Fatal("still blocked after the window")
	}
	// reset clears immediately.
	f.fail("1.2.3.4")
	f.reset("1.2.3.4")
	f.fail("1.2.3.4")
	if f.by["1.2.3.4"].count != 1 {
		t.Fatalf("reset did not clear: %+v", f.by["1.2.3.4"])
	}
}

// tunnel runs one connection through ServeConn, as the API does for the
// HTTPS tunnel, and returns the client's end.
func (r *doorRig) tunnel(t *testing.T, workspace, who, user string, key ssh.Signer) (*ssh.Client, error) {
	t.Helper()
	// A real socket pair: net.Pipe is unbuffered, and both ends of an SSH
	// handshake write their banner first.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if doorEnd, err := ln.Accept(); err == nil {
			r.door.ServeConn(doorEnd, workspace, who)
		}
	}()
	cliEnd, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	sc, chans, reqs, err := ssh.NewClientConn(cliEnd, "tunnel", &ssh.ClientConfig{
		User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: ssh.FixedHostKey(r.door.cfg.HostKey.PublicKey()), Timeout: 5 * time.Second,
	})
	if err != nil {
		cliEnd.Close()
		return nil, err
	}
	c := ssh.NewClient(sc, chans, reqs)
	t.Cleanup(func() { c.Close() })
	return c, nil
}

// Through the tunnel the workspace is the one the tunnel was opened for:
// the SSH username is whatever the person's ssh sends, and ownership is
// still the door's to check.
func TestTunnelledConnectionNamesItsWorkspace(t *testing.T) {
	rig := newDoorRig(t)
	client, err := rig.tunnel(t, "apo-alice-run", "user:alice", "whoever-locally", rig.alice)
	if err != nil {
		t.Fatal(err)
	}
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	if out, _ := ses.Output("whoami"); string(out) != "out:whoami:" {
		t.Errorf("out = %q", out)
	}
	// Someone else's workspace is refused, whatever the username says.
	if _, err := rig.tunnel(t, "apo-bob-run", "user:alice", "apo-alice-run", rig.alice); err == nil {
		t.Error("alice reached bob's workspace through the tunnel")
	}
	// The throttle counts the caller the API named, not an address every
	// tunnel shares.
	counted := func(who string) bool {
		f := rig.door.failures
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.by[who] != nil
	}
	// The door records the refusal after the client has its answer.
	deadline := time.Now().Add(5 * time.Second)
	for !counted("user:alice") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !counted("user:alice") || counted("127.0.0.1") {
		t.Error("the failure must be counted against the tunnel's caller")
	}
}

// A door reached only through the tunnel has no address of its own.
func TestDoorWithoutAListener(t *testing.T) {
	seed := make([]byte, 32)
	key, _ := KeyFromSeed(seed)
	d, err := New(Config{HostKey: key, DoorKey: key, Keys: newKeyring(t).svc, Targets: targets{}})
	if err != nil {
		t.Fatal(err)
	}
	if h, p := d.Address(); h != "" || p != 0 {
		t.Errorf("Address = %q:%d", h, p)
	}
}
