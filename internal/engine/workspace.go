package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

var (
	// ErrNoAgentRunning: the run is alive but no agent invocation is
	// running to take a message (a command stage, a gate, between stages).
	ErrNoAgentRunning = errors.New("no agent is running in this run right now")
)

const maxMessageLen = 32 << 10

// SendMessage hands a message to the agent a person took over: nobody talks
// to an agent any other way (ErrNotHeld). It is delivered when the agent
// finishes its turn, and recorded (message_queued) before this returns.
// attachmentIDs are userID's uploads, claimed into the run's take-over chat
// once the message is accepted, all or nothing: a message with one that is
// not theirs to send is refused, and records nothing. A message may be
// files alone.
func (e *Engine) SendMessage(runID, userID, text string, attachmentIDs []string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" && len(attachmentIDs) == 0 {
		return "", errors.New("message is empty")
	}
	if len(text) > maxMessageLen {
		return "", fmt.Errorf("message is longer than %d bytes", maxMessageLen)
	}
	if len(attachmentIDs) > filestore.MaxUploadFiles {
		return "", fmt.Errorf("a message carries at most %d files", filestore.MaxUploadFiles)
	}
	if len(attachmentIDs) > 0 && e.attachments == nil {
		return "", errAttachmentsOff
	}
	if h := e.liveLoop(runID); h != nil {
		h.opening.Lock()
		live := h.agentLive.Load()
		h.opening.Unlock()
		if !live {
			return "", ErrNoAgentRunning
		}
		req := msgReq{msg: agentport.UserMessage{ID: e.newID(), Text: text}, userID: userID,
			attachmentIDs: attachmentIDs, ack: make(chan error, 1)}
		if err := e.messageLive(h, req); !errors.Is(err, errNoLoop) {
			return req.msg.ID, err
		}
	}
	// No loop: a run parked on the holder of a take-over is woken by their
	// next message, as a run parked on a question is by its answer. Its
	// files reach the workspace with the run's others when it resumes.
	id := e.newID()
	err := e.wakeHeld(context.Background(), runID, func(run.View, []event.Event) ([]event.Payload, error) {
		if len(attachmentIDs) > 0 {
			if err := e.attachments.ClaimForChat(context.Background(), userID, attachmentIDs, runID, filestore.ChatTakeOver); err != nil {
				return nil, err
			}
		}
		return []event.Payload{&event.MessageQueued{MessageID: id, Text: text, AttachmentIDs: attachmentIDs}}, nil
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
