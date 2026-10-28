package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/ask"
	"apothecary/internal/auth"
	"apothecary/internal/engine"
)

// The Ask pane: a run's read-only session beside its stages, answering
// questions about the code (FLOWLANG §1 ask).

// AskRequest is one question for the run's Ask session.
type AskRequest struct {
	Text string `json:"text"`
}

// AskModelRequest picks the model the Ask session answers with from here on.
type AskModelRequest struct {
	Model string `json:"model"`
}

func (s *Server) getAsk(w http.ResponseWriter, r *http.Request) {
	st, err := s.Engine.AskState(r.Context(), r.PathValue("id"))
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
	m, err := s.Engine.Ask(r.Context(), r.PathValue("id"), p.User.ID, body.Text)
	if err != nil {
		writeErr(w, askStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, m)
}

func (s *Server) putAskModel(w http.ResponseWriter, r *http.Request) {
	var body AskModelRequest
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.Engine.AskSetModel(r.Context(), r.PathValue("id"), body.Model); err != nil {
		writeErr(w, askStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func askStatus(err error) int {
	switch {
	case errors.Is(err, engine.ErrAskOff), errors.Is(err, engine.ErrAskBusy):
		return http.StatusConflict
	case strings.Contains(err.Error(), "not found"):
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

// streamAsk serves the Ask conversation as server-sent events: the messages
// after after_seq (id = seq, so a reconnect resumes), then live — an
// answer's text as it is written (delta frames), each message as it lands,
// and the pane's state (busy, model) as it changes.
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
	frames, stop := s.Engine.AskWatch(id)
	defer stop()
	msgs, err := s.Engine.AskMessages(r.Context(), id, after)
	if err != nil {
		writeErr(w, askStatus(err), err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	send := func(f engine.AskFrame) {
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
		send(engine.AskFrame{Kind: "message", Message: &m})
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
		}
	}
}

// askMessageDoc is the schema the generated client sees for a message.
var _ = ask.Message{}
