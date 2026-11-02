package kube

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Forward is a supervised `kubectl port-forward` to one port of one target
// (pod/<name> or svc/<name>): how an engine OUTSIDE the cluster reaches a
// sandbox's dial-in mux or a gateway's control API. kubectl multiplexes every
// connection over one API stream, and dies whenever the pod restarts — so it
// is respawned, and Addr is read per dial.
type Forward struct {
	c      *Client
	target string
	port   int
	start  func(ctx context.Context, args []string) (*exec.Cmd, *bufio.Scanner, error)

	mu     sync.Mutex
	addr   string
	ready  chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

var forwardingRE = regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+) ->`)

// Forward starts the supervisor and waits (bounded by ctx) for the first
// local port.
func (c *Client) Forward(ctx context.Context, target string, port int) (*Forward, error) {
	sctx, cancel := context.WithCancel(context.Background())
	f := &Forward{c: c, target: target, port: port, ready: make(chan struct{}), cancel: cancel, done: make(chan struct{})}
	f.start = f.startKubectl
	go f.supervise(sctx)
	select {
	case <-f.ready:
		return f, nil
	case <-ctx.Done():
		f.Close()
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		f.Close()
		return nil, fmt.Errorf("kubectl port-forward %s %d: no local port within 30s", target, port)
	}
}

func (f *Forward) startKubectl(ctx context.Context, args []string) (*exec.Cmd, *bufio.Scanner, error) {
	cmd := exec.CommandContext(ctx, f.c.bin(), args...)
	cmd.Stderr = io.Discard
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, bufio.NewScanner(out), nil
}

func (f *Forward) supervise(ctx context.Context) {
	defer close(f.done)
	backoff := 250 * time.Millisecond
	args := append(f.c.Global(), "port-forward", "--address", "127.0.0.1", f.target, "0:"+strconv.Itoa(f.port))
	for ctx.Err() == nil {
		began := time.Now()
		cmd, sc, err := f.start(ctx, args)
		if err == nil {
			for sc.Scan() {
				if m := forwardingRE.FindStringSubmatch(sc.Text()); m != nil {
					f.mu.Lock()
					f.addr = "127.0.0.1:" + m[1]
					select {
					case <-f.ready:
					default:
						close(f.ready)
					}
					f.mu.Unlock()
				}
			}
			if werr := cmd.Wait(); werr != nil {
				err = werr
			}
		}
		if ctx.Err() != nil {
			return
		}
		if time.Since(began) > 30*time.Second {
			backoff = 250 * time.Millisecond
		}
		slog.Debug("kube: port-forward ended; restarting", "target", f.target, "port", f.port, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}

// Addr is the current local address.
func (f *Forward) Addr() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addr
}

// Close stops the forward.
func (f *Forward) Close() {
	f.cancel()
	<-f.done
}
