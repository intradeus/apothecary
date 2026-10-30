package main

// TCP forwarding into the workspace (RFC 4254 §7.2, direct-tcpip): what a
// `ssh -L`/`-D` carries, and what VS Code Remote-SSH uses to reach the
// server it starts on the workspace's loopback. The dial happens inside the
// workspace's network namespace, so loopback listeners and compose
// siblings are reachable alike.

import (
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type directTCPIPReq struct {
	DestAddr string
	DestPort uint32
	OrigAddr string
	OrigPort uint32
}

func (s *sshServer) directTCPIP(nc ssh.NewChannel) {
	var r directTCPIPReq
	if err := ssh.Unmarshal(nc.ExtraData(), &r); err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, "malformed direct-tcpip request")
		return
	}
	addr := net.JoinHostPort(r.DestAddr, strconv.Itoa(int(r.DestPort)))
	tc, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		tc.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	splice(ch, tc.(*net.TCPConn))
}

// splice pumps bytes both ways until both directions are done, passing
// each side's EOF to the other as a half-close so protocols that end with
// one (HTTP/1.0, scp) finish cleanly.
func splice(ch ssh.Channel, tc *net.TCPConn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(tc, ch)
		_ = tc.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(ch, tc)
		_ = ch.CloseWrite()
	}()
	wg.Wait()
	_ = tc.Close()
	_ = ch.Close()
}
