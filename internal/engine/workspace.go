package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
		return "", fmt.Errorf("run %s: %w", runID, ErrRunNotActive)
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

// Workspace returns a client for the run's workspace sidecar (starting it
// when nothing serves yet) and the git ref the run's changes are measured
// against: the branch it forked from.
func (e *Engine) Workspace(ctx context.Context, runID string) (*sidecar.Client, string, error) {
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return nil, "", err
	}
	if len(evs) == 0 {
		return nil, "", fmt.Errorf("run %s not found", runID)
	}
	v := run.Reduce(evs)
	if v.ProjectID == "" || v.EnvID == "" {
		return nil, "", fmt.Errorf("run %s: %w", runID, ErrNoSidecar)
	}
	if v.State == run.Archived {
		return nil, "", fmt.Errorf("run %s is archived; its workspace is gone: %w", runID, ErrNoSidecar)
	}
	res, err := e.resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return nil, "", err
	}
	defer res.release()
	sc, ok := res.Envp.(ports.Sidecar)
	if !ok {
		return nil, "", ErrNoSidecar
	}
	conn, err := sc.Sidecar(ctx, ports.EnvID(e.envNameForRun(v, runID)))
	if err != nil {
		return nil, "", fmt.Errorf("workspace sidecar: %w", err)
	}
	base := v.SourceBranch
	if base == "" {
		base = res.RepoRef.DefaultBranch
	}
	if base != "" {
		base = "origin/" + base
	}
	return sidecar.New(conn), base, nil
}
