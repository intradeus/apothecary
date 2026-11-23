package kube

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"
)

// ExecConn is one connection carried by `kubectl exec -i <pod> -c
// <container> -- <argv>`: what the engine writes is argv's stdin, what argv
// prints is read back. argv pipes its stdio to a socket in the pod (the
// shim's `relay --stdio`). It is how an engine OUTSIDE the cluster reaches a
// pod that port-forward cannot: under a VM runtime the pod's listeners are in
// the guest, while containerd dials the forwarded port in the node's network
// namespace. One kubectl process per connection, so it suits a dev engine's
// handful of connections, not a fleet's.
func (c *Client) ExecConn(ctx context.Context, pod, container string, argv []string) (net.Conn, error) {
	args := append(c.Global(), "exec", "-i", pod, "-c", container, "--")
	args = append(args, argv...)
	// Not CommandContext: the connection outlives the dial's context.
	cmd := exec.Command(c.bin(), args...)
	// Pipes of our own, not StdinPipe/StdoutPipe: Wait closes those when
	// kubectl exits, losing what the reader has not read yet, and an
	// *os.File pipe takes deadlines (net.Conn's).
	stdout, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	pr, stdin, err := os.Pipe()
	if err != nil {
		stdout.Close()
		pw.Close()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pr, pw, io.Discard
	if err := ctx.Err(); err != nil {
		for _, f := range []*os.File{stdout, pw, pr, stdin} {
			f.Close()
		}
		return nil, err
	}
	err = cmd.Start()
	pw.Close() // kubectl holds the write end; EOF once it exits
	pr.Close() // and the read end of its stdin
	if err != nil {
		stdout.Close()
		stdin.Close()
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return &execConn{cmd: cmd, w: stdin, r: stdout, done: done, pod: pod}, nil
}

type execConn struct {
	cmd  *exec.Cmd
	w    *os.File
	r    *os.File
	done chan struct{}
	pod  string

	once sync.Once
}

func (e *execConn) Read(p []byte) (int, error)  { return e.r.Read(p) }
func (e *execConn) Write(p []byte) (int, error) { return e.w.Write(p) }

// CloseWrite half-closes: argv sees end of input and passes it on.
func (e *execConn) CloseWrite() error { return e.w.Close() }

// closeGrace is how long Close lets kubectl carry the last bytes out before
// ending it: a server that keeps its side open after the end of input (an
// HTTP server does) would otherwise hold every Close.
const closeGrace = 200 * time.Millisecond

// Close ends the connection: stdin first, then kubectl itself if it lingers.
func (e *execConn) Close() error {
	e.once.Do(func() {
		_ = e.w.Close()
		select {
		case <-e.done:
		case <-time.After(closeGrace):
			if e.cmd.Process != nil {
				_ = e.cmd.Process.Kill()
			}
			<-e.done
		}
		_ = e.r.Close()
	})
	return nil
}

func (e *execConn) LocalAddr() net.Addr  { return execAddr("engine") }
func (e *execConn) RemoteAddr() net.Addr { return execAddr("pod/" + e.pod) }

// Deadlines are the pipes': a read or write past one fails with an error
// that is os.ErrDeadlineExceeded (a net.Error whose Timeout is true).
func (e *execConn) SetDeadline(t time.Time) error {
	return errors.Join(e.r.SetReadDeadline(t), e.w.SetWriteDeadline(t))
}
func (e *execConn) SetReadDeadline(t time.Time) error  { return e.r.SetReadDeadline(t) }
func (e *execConn) SetWriteDeadline(t time.Time) error { return e.w.SetWriteDeadline(t) }

type execAddr string

func (a execAddr) Network() string { return "kubectl-exec" }
func (a execAddr) String() string  { return string(a) }
