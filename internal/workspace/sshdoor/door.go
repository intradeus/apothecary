// Package sshdoor is the engine's SSH door (TODO 46): what lets a person into
// a run's workspace over SSH — `ssh <workspace>.apothecary`, or VS Code
// Remote-SSH, which is the same thing — as the user their key belongs to, if
// they own the run.
//
// A person reaches it through the engine's HTTPS address: the apothecary
// CLI's ssh-proxy opens a tunnel that names the workspace, and the API hands
// the stream to ServeConn. Nothing else is published. For the dev loop the
// door can also listen on a TCP port of its own (Serve), where the SSH
// username names the workspace.
//
// The door terminates the person's SSH connection (their key, against the
// keys registered on their account; the instance's one stable host key) and
// opens a second one to the SSH server in the workspace's shim sidecar,
// over the engine's own path into the workspace (a unix socket on docker,
// the pod mux on Kubernetes — no container IP is ever dialed). Between the
// two it proxies channels and requests one for one and understands none of
// them: sessions, PTYs, exec, port forwards and exit codes are the
// sidecar's business, as sshd's would be. The upstream trusts the door by
// the door's own key, which the engine writes into the workspace before it
// dials.
package sshdoor

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"apothecary/internal/account/auth"
	"apothecary/internal/platform/logs"
)

// KeyAuth resolves an offered public key to the account it belongs to
// (auth.Service).
type KeyAuth interface {
	AuthenticateSSHKey(ctx context.Context, key ssh.PublicKey) (auth.User, auth.SSHKey, error)
	TouchSSHKey(ctx context.Context, id string, at time.Time) error
}

// Target is the workspace a username names: whose run it is, and how to
// reach its sidecar's SSH server.
type Target struct {
	RunID   string
	OwnerID string
	Dial    func(ctx context.Context) (net.Conn, error)
}

// Targets looks a workspace up by the name a person logs in as.
type Targets interface {
	Lookup(ctx context.Context, workspace string) (Target, error)
}

// Config is what a door needs.
type Config struct {
	// HostKey is the door's own host key — stable across restarts, so a
	// person verifies one fingerprint for every run. DoorKey is the key the
	// door authenticates to the sidecars with; its public half is what the
	// engine writes into each workspace.
	HostKey, DoorKey ssh.Signer
	Keys             KeyAuth
	Targets          Targets
	// Advertise is the host:port people connect to when the door listens
	// on a TCP port of its own (Serve): what the run page prints and links
	// to. Empty when the door is reached only through the HTTPS tunnel.
	Advertise string
	Log       *slog.Logger
}

const (
	// handshakeTimeout bounds the key exchange and authentication.
	handshakeTimeout = 30 * time.Second
	// upstreamTimeout bounds reaching the sidecar: it covers the sidecar's
	// lazy start and, on Kubernetes, a port-forward.
	upstreamTimeout = 45 * time.Second
	// maxAuthTries is OpenSSH's default: an agent may offer several keys
	// before the right one.
	maxAuthTries = 6
	// failWindow and failLimit throttle an address that keeps failing to
	// authenticate: after failLimit failures in failWindow it is refused
	// until the window ends.
	failWindow = 15 * time.Minute
	failLimit  = 8
)

// usernameRe is the shape of a workspace name (a lowercase slug, as the
// engine names them): anything else is refused before any lookup.
var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// errDenied is the one answer every failed authentication gets: an unknown
// key, someone else's run, a workspace that is gone and a disabled account
// read the same from outside.
var errDenied = errors.New("permission denied")

// Door is one running SSH door.
type Door struct {
	cfg      Config
	host     string
	port     int
	failures *failures

	mu     sync.Mutex
	ln     net.Listener
	closed bool
	conns  map[*conn]struct{}
}

// New builds a door; Serve runs it.
func New(cfg Config) (*Door, error) {
	switch {
	case cfg.HostKey == nil || cfg.DoorKey == nil:
		return nil, errors.New("sshdoor: a host key and a door key are required")
	case cfg.Keys == nil || cfg.Targets == nil:
		return nil, errors.New("sshdoor: key authentication and targets are required")
	}
	var host string
	var port int
	if cfg.Advertise != "" {
		h, portStr, err := net.SplitHostPort(cfg.Advertise)
		if err != nil {
			return nil, fmt.Errorf("sshdoor: advertise %q is not host:port: %v", cfg.Advertise, err)
		}
		if port, err = strconv.Atoi(portStr); err != nil || port <= 0 {
			return nil, fmt.Errorf("sshdoor: advertise %q has no port", cfg.Advertise)
		}
		host = h
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Door{cfg: cfg, host: host, port: port, failures: newFailures(), conns: map[*conn]struct{}{}}, nil
}

// KeyFromSeed is an ed25519 signer from 32 secret bytes (an instance
// secret): the same seed gives the same key on every boot.
func KeyFromSeed(seed []byte) (ssh.Signer, error) {
	if len(seed) < ed25519.SeedSize {
		return nil, fmt.Errorf("sshdoor: a key seed is %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	return ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize]))
}

// Address is the advertised host and port; ("", 0) when the door has no
// listener of its own and is reached only through the HTTPS tunnel.
func (d *Door) Address() (string, int) { return d.host, d.port }

// Fingerprint is the host key's SHA256 fingerprint, as ssh prints it.
func (d *Door) Fingerprint() string { return ssh.FingerprintSHA256(d.cfg.HostKey.PublicKey()) }

// HostPublicKey is the host key as a known_hosts entry's key part.
func (d *Door) HostPublicKey() string {
	return string(ssh.MarshalAuthorizedKey(d.cfg.HostKey.PublicKey()))
}

// DoorPublicKey is the door's client key as an authorized_keys line: what
// a sidecar must hold to let the door in.
func (d *Door) DoorPublicKey() string {
	return string(ssh.MarshalAuthorizedKey(d.cfg.DoorKey.PublicKey()))
}

// Serve accepts connections until the listener is closed (Close).
func (d *Door) Serve(ln net.Listener) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		ln.Close()
		return errors.New("sshdoor: closed")
	}
	d.ln = ln
	d.mu.Unlock()
	for {
		nc, err := ln.Accept()
		if err != nil {
			d.mu.Lock()
			closed := d.closed
			d.mu.Unlock()
			if closed {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go d.handle(nc, "", hostOf(nc.RemoteAddr()))
	}
}

// ServeConn runs one connection that arrived through the HTTPS tunnel and
// returns when it ends. workspace is the workspace it was opened for (the
// SSH username is then not read); who is the caller as the API
// authenticated them, which the failure throttle counts by instead of an
// address every tunnel shares (the ingress's).
func (d *Door) ServeConn(nc net.Conn, workspace, who string) {
	d.handle(nc, workspace, who)
}

// Close stops accepting and ends every live connection.
func (d *Door) Close() error {
	d.mu.Lock()
	d.closed = true
	ln := d.ln
	conns := make([]*conn, 0, len(d.conns))
	for c := range d.conns {
		conns = append(conns, c)
	}
	d.mu.Unlock()
	var err error
	if ln != nil {
		err = ln.Close()
	}
	for _, c := range conns {
		c.close()
	}
	return err
}

// DropUser ends the user's live connections: an account disabled is out at
// once, not at its next connection.
func (d *Door) DropUser(userID string) {
	d.mu.Lock()
	var drop []*conn
	for c := range d.conns {
		if c.userID == userID {
			drop = append(drop, c)
		}
	}
	d.mu.Unlock()
	for _, c := range drop {
		c.close()
	}
}

// conn is one person's connection through the door.
type conn struct {
	door   *Door
	raw    net.Conn
	remote string
	// What authentication established.
	userID, keyID string
	key           ssh.PublicKey
	target        Target
	// workspace, when set, is the workspace a tunnelled connection was
	// opened for, which the username would otherwise name.
	workspace string
	verdicts  map[string]verdict // per offered key: the callback runs twice for one key
	closeOnce sync.Once
}

type verdict struct {
	perms *ssh.Permissions
	err   error
}

func (c *conn) close() { c.closeOnce.Do(func() { _ = c.raw.Close() }) }

// handle runs one connection: the handshake under a deadline and the
// throttle, the upstream, then the proxy until either side ends. remote is
// what the throttle counts failures by.
func (d *Door) handle(nc net.Conn, workspace, remote string) {
	c := &conn{door: d, raw: nc, remote: remote, workspace: workspace, verdicts: map[string]verdict{}}
	defer c.close()
	if d.failures.blocked(c.remote) {
		return
	}
	if tc, ok := nc.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(30 * time.Second)
	}
	_ = nc.SetDeadline(time.Now().Add(handshakeTimeout))
	sconn, chans, reqs, err := ssh.NewServerConn(nc, d.serverConfig(c))
	if err != nil {
		d.failures.fail(c.remote)
		d.cfg.Log.Debug("ssh door: handshake failed", "remote", c.remote, "err", err)
		return
	}
	_ = nc.SetDeadline(time.Time{})
	defer sconn.Close()
	d.failures.reset(c.remote)
	// Known from here on — the person's ssh is connected, whatever the
	// workspace does next — so DropUser and Close reach it.
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.conns[c] = struct{}{}
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.conns, c)
		d.mu.Unlock()
	}()
	started := time.Now()
	log := d.cfg.Log.With("remote", c.remote, logs.UserID, c.userID, logs.RunID, c.target.RunID)

	// An account disabled since the handshake found nothing to drop.
	// DropUser follows the disabling, so with the connection known first and
	// this look after it, one of the two sees the other. A look that fails
	// keeps the door shut.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, _, err = d.cfg.Keys.AuthenticateSSHKey(ctx, c.key)
	cancel()
	if err != nil {
		log.Info("ssh door: account no longer allowed in", "err", err)
		return
	}

	ctx, cancel = context.WithTimeout(context.Background(), upstreamTimeout)
	uconn, uchans, ureqs, err := d.upstream(ctx, c)
	cancel()
	if err != nil {
		log.Warn("ssh door: workspace unreachable", "err", err)
		// The person's ssh prints the reason its first channel was refused;
		// then the connection ends.
		go ssh.DiscardRequests(reqs)
		if nc, ok := <-chans; ok {
			_ = nc.Reject(ssh.ConnectionFailed, "workspace unavailable: "+err.Error())
		}
		return
	}
	defer uconn.Close()
	_ = d.cfg.Keys.TouchSSHKey(context.Background(), c.keyID, time.Now().UTC())
	log.Info("ssh door: session opened")
	proxy(sconn, chans, reqs, uconn, uchans, ureqs)
	log.Info("ssh door: session closed", "duration", time.Since(started).Round(time.Second))
}

// serverConfig is the per-connection server configuration: the host key,
// public-key authentication only, and the verdict cache — x/crypto asks
// the callback once to see whether a key would do and once more with the
// signature, and the lookups behind it should run once.
func (d *Door) serverConfig(c *conn) *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		MaxAuthTries:  maxAuthTries,
		ServerVersion: "SSH-2.0-apothecary",
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			fp := ssh.FingerprintSHA256(key)
			if v, ok := c.verdicts[fp]; ok {
				return v.perms, v.err
			}
			perms, err := d.authenticate(c, meta.User(), key)
			c.verdicts[fp] = verdict{perms, err}
			return perms, err
		},
		AuthLogCallback: func(meta ssh.ConnMetadata, method string, err error) {
			if err != nil && method != "none" {
				d.cfg.Log.Debug("ssh door: auth refused", "remote", c.remote, "username", meta.User(), "method", method)
			}
		},
	}
	cfg.AddHostKey(d.cfg.HostKey)
	return cfg
}

// authenticate is the door's whole decision: the workspace (the tunnel's,
// else the username) is one, the key is a registered one, the key's account
// is not disabled, the workspace belongs to a run of that account's, and the
// workspace is there to be reached. Any miss is errDenied, with the reason in
// the log only.
func (d *Door) authenticate(c *conn, username string, key ssh.PublicKey) (*ssh.Permissions, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if c.workspace != "" {
		username = c.workspace
	}
	if !usernameRe.MatchString(username) {
		return nil, errDenied
	}
	user, k, err := d.cfg.Keys.AuthenticateSSHKey(ctx, key)
	if err != nil {
		return nil, errDenied
	}
	target, err := d.cfg.Targets.Lookup(ctx, username)
	if err != nil {
		d.cfg.Log.Debug("ssh door: no workspace for username", "remote", c.remote, "username", username, logs.UserID, user.ID, "err", err)
		return nil, errDenied
	}
	if target.OwnerID == "" || target.OwnerID != user.ID {
		d.cfg.Log.Debug("ssh door: not the owner", "remote", c.remote, "username", username, logs.UserID, user.ID, logs.RunID, target.RunID)
		return nil, errDenied
	}
	c.userID, c.keyID, c.key, c.target = user.ID, k.ID, key, target
	return &ssh.Permissions{Extensions: map[string]string{
		"apothecary-user": user.ID, "apothecary-key": k.ID, "apothecary-run": target.RunID,
	}}, nil
}

// upstream reaches the workspace's sidecar and opens the door's own SSH
// connection to it. The sidecar's host key is ephemeral and not checked:
// the transport is the engine's private path into the workspace (a unix
// socket only the engine's uid reaches, the token-authenticated pod mux),
// and SSH here carries session semantics, not transport trust.
func (d *Door) upstream(ctx context.Context, c *conn) (ssh.Conn, <-chan ssh.NewChannel, <-chan *ssh.Request, error) {
	raw, err := c.target.Dial(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	deadline, _ := ctx.Deadline()
	_ = raw.SetDeadline(deadline)
	uconn, chans, reqs, err := ssh.NewClientConn(raw, "sidecar", &ssh.ClientConfig{
		User:            "apothecary",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(d.cfg.DoorKey)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		ClientVersion:   "SSH-2.0-apothecary-door",
		Timeout:         upstreamTimeout,
	})
	if err != nil {
		raw.Close()
		return nil, nil, nil, fmt.Errorf("the sidecar refused the door: %w", err)
	}
	_ = raw.SetDeadline(time.Time{})
	return uconn, chans, reqs, nil
}

// hostOf is an address without its port: what the throttle counts by.
func hostOf(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	if h, _, err := net.SplitHostPort(addr.String()); err == nil {
		return h
	}
	return addr.String()
}

// failures throttles addresses that keep failing to authenticate.
type failures struct {
	mu   sync.Mutex
	by   map[string]*failure
	now  func() time.Time
	last time.Time
}

type failure struct {
	count int
	since time.Time
}

func newFailures() *failures { return &failures{by: map[string]*failure{}, now: time.Now} }

func (f *failures) blocked(remote string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweep()
	e := f.by[remote]
	return e != nil && e.count >= failLimit
}

func (f *failures) fail(remote string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.by[remote]
	if e == nil {
		e = &failure{since: f.now()}
		f.by[remote] = e
	}
	e.count++
}

func (f *failures) reset(remote string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.by, remote)
}

// sweep forgets windows that ended; once a minute at most.
func (f *failures) sweep() {
	now := f.now()
	if now.Sub(f.last) < time.Minute {
		return
	}
	f.last = now
	for k, e := range f.by {
		if now.Sub(e.since) > failWindow {
			delete(f.by, k)
		}
	}
}
