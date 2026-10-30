package sshdoor

// The proxy: two SSH connections joined channel for channel, request for
// request, in both directions, with no idea what any of it means. A
// channel one side opens is opened on the other with the same type and
// extra data (a session, a direct-tcpip forward, a forwarded-tcpip or
// agent channel coming back); its data and stderr streams are copied, EOF
// as a half-close; every channel request (pty-req, env, exec, exit-status,
// window-change, …) and global request (keepalives, remote forwards) is
// sent on and its reply carried back. Each side's close reaches the other
// once everything that side sent — the exit status last — has been
// delivered.

import (
	"errors"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
)

// proxy joins a and b until both are closed.
func proxy(a ssh.Conn, achans <-chan ssh.NewChannel, areqs <-chan *ssh.Request, b ssh.Conn, bchans <-chan ssh.NewChannel, breqs <-chan *ssh.Request) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for nc := range achans {
			go bridgeChannel(nc, b)
		}
	}()
	go func() {
		defer wg.Done()
		for nc := range bchans {
			go bridgeChannel(nc, a)
		}
	}()
	go forwardGlobal(areqs, b)
	go forwardGlobal(breqs, a)
	// One side going away ends the other: a workspace destroyed under a
	// session, a person closing their editor.
	go func() { _ = a.Wait(); _ = b.Close() }()
	go func() { _ = b.Wait(); _ = a.Close() }()
	wg.Wait()
}

// bridgeChannel opens the channel nc asks for on the other connection and
// pipes the two together. A refusal there is a refusal here, with its
// reason.
func bridgeChannel(nc ssh.NewChannel, other ssh.Conn) {
	far, farReqs, err := other.OpenChannel(nc.ChannelType(), nc.ExtraData())
	if err != nil {
		var oce *ssh.OpenChannelError
		if errors.As(err, &oce) {
			_ = nc.Reject(oce.Reason, oce.Message)
		} else {
			_ = nc.Reject(ssh.ConnectionFailed, "workspace unavailable")
		}
		return
	}
	near, nearReqs, err := nc.Accept()
	if err != nil {
		_ = far.Close()
		return
	}
	go pump(far, farReqs, near)
	pump(near, nearReqs, far)
}

// pump carries one direction of a bridged channel: src's data and stderr
// into dst, then the half-close — only once BOTH streams are drained, since
// an SSH EOF ends the receiver's stderr too — then src's requests until src
// closes, then dst's close. So an exit-status always lands after the bytes
// before it and before the close that follows it.
func pump(src ssh.Channel, srcReqs <-chan *ssh.Request, dst ssh.Channel) {
	var data sync.WaitGroup
	data.Add(2)
	go func() { defer data.Done(); _, _ = io.Copy(dst, src) }()
	go func() { defer data.Done(); _, _ = io.Copy(dst.Stderr(), src.Stderr()) }()
	go func() {
		data.Wait()
		_ = dst.CloseWrite()
	}()
	forwardRequests(srcReqs, dst)
	data.Wait()
	_ = dst.Close()
}

// forwardRequests sends each request of a channel on to the other side
// and answers with what it said.
func forwardRequests(from <-chan *ssh.Request, to ssh.Channel) {
	for r := range from {
		ok, err := to.SendRequest(r.Type, r.WantReply, r.Payload)
		if r.WantReply {
			_ = r.Reply(ok && err == nil, nil)
		}
	}
}

// forwardGlobal does the same for connection-level requests.
func forwardGlobal(from <-chan *ssh.Request, to ssh.Conn) {
	for r := range from {
		ok, payload, err := to.SendRequest(r.Type, r.WantReply, r.Payload)
		if r.WantReply {
			if err != nil {
				ok, payload = false, nil
			}
			_ = r.Reply(ok, payload)
		}
	}
}
