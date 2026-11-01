package main

// The sidecar's SSH server (TODO 46): what a person's ssh or VS Code
// Remote-SSH session becomes once the engine's SSH door has let it through.
// It listens on a unix socket in the shim dir — the engine dials the same
// file host-side (docker) or through the pod mux (Kubernetes); no container
// networking is involved — and speaks SSH only for its session semantics:
// a shell or a command on a PTY as the remote user, in the repo, and TCP
// forwarding into the workspace (what Remote-SSH needs to reach the server
// it starts). The door authenticates the person; the sidecar authenticates
// the door, by the one public key the engine writes to doorKeyPath before
// it dials, re-read on every attempt so a rebuilt container needs nothing
// but the next connection.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"log"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

// doorKeyPath holds the engine's door public key (an authorized_keys line);
// a var so tests point it at a fixture.
var doorKeyPath = "/apothecary/ssh_door.pub"

const (
	// sshMaxConns bounds the connections one sidecar serves at once: a
	// person's editor opens a handful, never dozens.
	sshMaxConns = 64
	// sshHandshakeTimeout is how long a connection may take to authenticate.
	sshHandshakeTimeout = 30 * time.Second
)

// sshServer is one sidecar's SSH server.
type sshServer struct {
	repo   string
	config *ssh.ServerConfig
	sem    chan struct{}
	// hangupGrace is how long a session's process has to go once its client
	// has hung up, before it is killed.
	hangupGrace time.Duration
}

// startSSH serves SSH on a unix socket until the listener is closed. The
// socket is world-connectable like sidecar.sock: the engine dials it as
// another uid, and the door key — not the file mode — is what admits.
func startSSH(sock, repo string) (net.Listener, error) {
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(sock, 0o666)
	s, err := newSSHServer(repo)
	if err != nil {
		ln.Close()
		return nil, err
	}
	go s.serve(ln)
	return ln, nil
}

// newSSHServer builds the server: an ephemeral host key (the person only
// ever sees the door's; the door trusts the transport, not this key), the
// door key as the one accepted credential, no passwords.
func newSSHServer(repo string) (*sshServer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	s := &sshServer{repo: repo, sem: make(chan struct{}, sshMaxConns), hangupGrace: 5 * time.Second}
	s.config = &ssh.ServerConfig{
		MaxAuthTries:  2,
		ServerVersion: "SSH-2.0-apothecary-sidecar",
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !doorKeyAccepts(key) {
				return nil, errors.New("not the door's key")
			}
			return &ssh.Permissions{}, nil
		},
	}
	s.config.AddHostKey(signer)
	return s, nil
}

// doorKeyAccepts reports whether key is the door key the engine wrote —
// read on every attempt: the engine lays the file down right before it
// dials, and a rebuilt container starts without it.
func doorKeyAccepts(key ssh.PublicKey) bool {
	data, err := os.ReadFile(doorKeyPath)
	if err != nil {
		return false
	}
	want := key.Marshal()
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		pk, _, _, _, err := ssh.ParseAuthorizedKey(line)
		if err == nil && bytes.Equal(pk.Marshal(), want) {
			return true
		}
	}
	return false
}

func (s *sshServer) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return // the listener is closed: the sidecar is going away
		}
		select {
		case s.sem <- struct{}{}:
		default:
			c.Close()
			continue
		}
		go func() {
			defer func() { <-s.sem }()
			s.handle(c)
		}()
	}
}

// handle runs one connection: the handshake under a deadline, then its
// channels — sessions and TCP forwards — for as long as it lasts. Global
// requests (keepalives, remote forwards) get the standard "no".
func (s *sshServer) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(sshHandshakeTimeout))
	conn, chans, reqs, err := ssh.NewServerConn(c, s.config)
	if err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			go s.session(nc)
		case "direct-tcpip":
			go s.directTCPIP(nc)
		default:
			_ = nc.Reject(ssh.UnknownChannelType, "unsupported channel type")
		}
	}
}

// logSSH is the sidecar's one log line per noteworthy SSH event.
func logSSH(format string, args ...any) { log.Printf("sidecar: ssh: "+format, args...) }
