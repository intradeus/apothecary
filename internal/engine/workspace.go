package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/run"
)

var (
	// ErrNoAgentRunning: the run is alive but no agent invocation is
	// running to take a message (a command stage, a gate, between stages).
	ErrNoAgentRunning = errors.New("no agent is running in this run right now")
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
	if h := e.liveLoop(runID); h != nil {
		if !h.agentLive.Load() {
			return "", ErrNoAgentRunning
		}
		req := msgReq{msg: agentport.UserMessage{ID: e.newID(), Text: text, Interrupt: interrupt}, ack: make(chan error, 1)}
		if err := e.messageLive(h, req); !errors.Is(err, errNoLoop) {
			return req.msg.ID, err
		}
	}
	// No loop: a run parked on the holder of a take-over is woken by their
	// next message, as a run parked on a question is by its answer.
	id := e.newID()
	err := e.wakeHeld(context.Background(), runID, func(run.View) []event.Payload {
		return []event.Payload{&event.MessageQueued{MessageID: id, Text: text, Interrupt: interrupt}}
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// messageLive hands a message to the run's loop and waits to hear it landed.
// errNoLoop: the loop went without taking it.
func (e *Engine) messageLive(h *runHandle, req msgReq) error {
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	select {
	case h.messages <- req:
	case <-h.done:
		return errNoLoop
	case <-t.C:
		// The invocation ended between the check and the send.
		return ErrNoAgentRunning
	}
	select {
	case err := <-req.ack:
		return err
	case <-h.done:
		select {
		case err := <-req.ack:
			return err
		default:
			return errNoLoop
		}
	}
}
