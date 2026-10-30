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
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"apothecary/internal/auth"
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
	svc := auth.New(auth.NewMem())
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
	door, err := New(Config{HostKey: hostKey, DoorKey: doorKey, Keys: keys.svc, Targets: ts, Advertise: "door.example:2222"})
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
