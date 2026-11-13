package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/engine/ask"
)

// The Ask pane: a run's read-only sessions beside its stages, answering
// questions about the code (FLOWLANG §1 ask). A run holds any number of
// them; the API calls each a thread.

// AskRequest is one question for one of the run's Ask threads.
type AskRequest struct {
	Text string `json:"text"`
	// ThreadID names the thread; empty: the run's latest, opened when the
	// run has none.
	ThreadID string `json:"thread_id,omitempty"`
}

// AskModelRequest picks the model an Ask thread answers with from here on.
type AskModelRequest struct {
	Model string `json:"model"`
	// ThreadID names the thread; empty: the run's latest, opened when the
	// run has none.
	ThreadID string `json:"thread_id,omitempty"`
}

func (s *Server) getAsk(w http.ResponseWriter, r *http.Request) {
	st, err := s.Ask.State(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, askStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) postAsk(w http.ResponseWriter, r *http.Request) {
	var body AskRequest
	if !readJSON(w, r, &body) {
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	m, err := s.Ask.Ask(r.Context(), r.PathValue("id"), body.ThreadID, p.User.ID, body.Text)
	if err != nil {
		writeErr(w, askStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, m)
}

func (s *Server) postAskThread(w http.ResponseWriter, r *http.Request) {
	th, err := s.Ask.NewThread(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, askStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, th)
}

func (s *Server) putAskModel(w http.ResponseWriter, r *http.Request) {
	var body AskModelRequest
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.Ask.SetModel(r.Context(), r.PathValue("id"), body.ThreadID, body.Model); err != nil {
		writeErr(w, askStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func askStatus(err error) int {
	switch {
	case errors.Is(err, ask.ErrOff), errors.Is(err, ask.ErrBusy):
		return http.StatusConflict
	case strings.Contains(err.Error(), "not found"):
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

// streamAsk serves the Ask conversations as server-sent events: the
// messages of every thread after after_seq (id = seq, so a reconnect
// resumes), then live — an answer's text as it is written (delta frames),
// each message as it lands, a thread's state (busy, model) as it changes,
// and threads as they open; every frame names its thread_id.
func (s *Server) streamAsk(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var after int64
	if q := r.URL.Query().Get("after_seq"); q != "" {
		v, err := strconv.ParseInt(q, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid after_seq")
			return
		}
		after = v
	}
	if h := r.Header.Get("Last-Event-ID"); h != "" {
		if v, err := strconv.ParseInt(h, 10, 64); err == nil && v > after {
			after = v
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	// Subscribe before the replay so nothing lands in between unseen; a
	// message replayed and streamed both is harmless (same seq).
	frames, stop := s.Ask.Watch(id)
	defer stop()
	msgs, err := s.Ask.Messages(r.Context(), id, after)
	if err != nil {
		writeErr(w, askStatus(err), err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	send := func(f ask.AskFrame) {
		data, err := json.Marshal(f)
		if err != nil {
			return
		}
		if f.Kind == "message" && f.Message != nil {
			fmt.Fprintf(w, "id: %d\n", f.Message.Seq)
		} else {
			fmt.Fprintf(w, "event: %s\n", f.Kind)
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	for i := range msgs {
		m := msgs[i]
		send(ask.AskFrame{Kind: "message", Message: &m})
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case f := <-frames:
			send(f)
		case <-heartbeat.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		case <-s.draining():
			return
		}
	}
}

// askMessageDoc is the schema the generated client sees for a message.
var _ = ask.Message{}
