package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/run"
	"apothecary/internal/sidecar"
)

var (
	// ErrNoAgentRunning: the run is alive but no agent invocation is
	// running to take a message (a command stage, a gate, between stages).
	ErrNoAgentRunning = errors.New("no agent is running in this run right now")
	// ErrNoSidecar: the run's workspace cannot host the sidecar (provisioner
	// without one, or no workspace yet).
	ErrNoSidecar = errors.New("this run's workspace has no sidecar")
)

const maxMessageLen = 32 << 10

// SendMessage hands a human message to the run's running agent. Interrupt
// asks for the turn in flight to be cancelled so the agent reads it now;
// otherwise it is delivered when the agent finishes its turn. The message is
// recorded (message_queued) before this returns.
func (e *Engine) SendMessage(runID, text string, interrupt bool) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("message is empty")
	}
	if len(text) > maxMessageLen {
		return "", fmt.Errorf("message is longer than %d bytes", maxMessageLen)
	}
	e.mu.Lock()
	h, ok := e.handles[runID]
	e.mu.Unlock()
	if !ok {
		// No loop: a run parked on the holder of a take-over is woken by
		// their next message, as a run parked on a question is by its answer.
		id := e.newID()
		err := e.wakeHeld(context.Background(), runID, func(run.View) []event.Payload {
			return []event.Payload{&event.MessageQueued{MessageID: id, Text: text, Interrupt: interrupt}}
		})
		if err != nil {
			return "", err
		}
		return id, nil
	}
	if !h.agentLive.Load() {
		return "", ErrNoAgentRunning
	}
	req := msgReq{msg: ports.UserMessage{ID: e.newID(), Text: text, Interrupt: interrupt}, ack: make(chan error, 1)}
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	select {
	case h.messages <- req:
	case <-h.done:
		return "", fmt.Errorf("run %s: %w", runID, ErrRunNotActive)
	case <-t.C:
		// The invocation ended between the check and the send.
		return "", ErrNoAgentRunning
	}
	select {
	case err := <-req.ack:
		return req.msg.ID, err
	case <-h.done:
		return "", fmt.Errorf("run %s: %w", runID, ErrRunNotActive)
	}
}

// ErrNoSuchCheckout: the run has no checkout of that name.
var ErrNoSuchCheckout = errors.New("no such checkout in this run")

// Checkout is one of a run's repositories as the workspace views address
// it: the recorded checkout plus the git ref its changes are measured
// against (the branch it forked from).
type Checkout struct {
	event.Checkout
	Base string
}

func (e *Engine) runView(ctx context.Context, runID string) (run.View, error) {
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return run.View{}, err
	}
	if len(evs) == 0 {
		return run.View{}, fmt.Errorf("run %s not found", runID)
	}
	return run.Reduce(evs), nil
}

// Checkouts lists the run's repositories (the primary first).
func (e *Engine) Checkouts(ctx context.Context, runID string) ([]event.Checkout, error) {
	v, err := e.runView(ctx, runID)
	if err != nil {
		return nil, err
	}
	return v.Checkouts, nil
}

// Workspace returns a client for the run's workspace sidecar (starting it
// when nothing serves yet) and the checkout named by repo ("" = the primary).
func (e *Engine) Workspace(ctx context.Context, runID, repo string) (*sidecar.Client, Checkout, error) {
	v, err := e.runView(ctx, runID)
	if err != nil {
		return nil, Checkout{}, err
	}
	if v.ProjectID == "" || v.EnvID == "" {
		return nil, Checkout{}, fmt.Errorf("run %s: %w", runID, ErrNoSidecar)
	}
	if v.State == run.Archived {
		return nil, Checkout{}, fmt.Errorf("run %s is archived; its workspace is gone: %w", runID, ErrNoSidecar)
	}
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
	var co *event.Checkout
	for i := range v.Checkouts {
		if v.Checkouts[i].Name == repo {
			co = &v.Checkouts[i]
		}
	}
	if co == nil {
		return nil, Checkout{}, fmt.Errorf("%w: %q", ErrNoSuchCheckout, repo)
	}
	res, err := e.resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return nil, Checkout{}, err
	}
	defer res.release()
	sc, ok := res.Envp.(ports.Sidecar)
	if !ok {
		return nil, Checkout{}, ErrNoSidecar
	}
	conn, err := sc.Sidecar(ctx, ports.EnvID(e.envNameForRun(v, runID)))
	if err != nil {
		return nil, Checkout{}, fmt.Errorf("workspace sidecar: %w", err)
	}
	base := co.Source
	if base == "" {
		base = res.RepoRef.DefaultBranch
	}
	if base != "" {
		base = "origin/" + base
	}
	return sidecar.New(conn), Checkout{Checkout: *co, Base: base}, nil
}
