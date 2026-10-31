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
	// answering[x] is held while a request x sent is forwarded and its
	// reply carried back to x, and taken before x is closed: a command that
	// exits the moment it starts closes its channel right behind its reply
	// to exec, and the person must get that reply before the close.
	var nearAnswering, farAnswering sync.Mutex
	go pump(far, farReqs, &farAnswering, near, &nearAnswering)
	pump(near, nearReqs, &nearAnswering, far, &farAnswering)
}

// pump carries one direction of a bridged channel: src's data and stderr
// into dst, then the half-close — only once BOTH streams are drained, since
// an SSH EOF ends the receiver's stderr too — then src's requests until src
// closes, then dst's close, once no reply owed to dst is still in flight
// (dstAnswering). So an exit-status always lands after the bytes before it
// and before the close that follows it, and so does the reply to the
// request that started the command.
func pump(src ssh.Channel, srcReqs <-chan *ssh.Request, srcAnswering *sync.Mutex, dst ssh.Channel, dstAnswering *sync.Mutex) {
	var data sync.WaitGroup
	data.Add(2)
	go func() { defer data.Done(); _, _ = io.Copy(dst, src) }()
	go func() { defer data.Done(); _, _ = io.Copy(dst.Stderr(), src.Stderr()) }()
	go func() {
		data.Wait()
		_ = dst.CloseWrite()
	}()
	forwardRequests(srcReqs, dst, srcAnswering)
	data.Wait()
	dstAnswering.Lock()
	_ = dst.Close()
	dstAnswering.Unlock()
}

// forwardRequests sends each request of a channel on to the other side
// and answers with what it said, holding answering until it has. The other
// side's reply cannot hang the closer waiting on answering: by then that
// side has closed, and SendRequest returns with it.
func forwardRequests(from <-chan *ssh.Request, to ssh.Channel, answering *sync.Mutex) {
	for r := range from {
		answering.Lock()
		ok, err := to.SendRequest(r.Type, r.WantReply, r.Payload)
		if r.WantReply {
			_ = r.Reply(ok && err == nil, nil)
		}
		answering.Unlock()
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
