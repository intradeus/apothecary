package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/platform/filestore"
	"apothecary/internal/run/event"
	"apothecary/internal/workspace/wsinfo"
)

// What a run says: its event stream (SSE), the stage logs captured in its
// record, and the live tail of its workspace's exec output.

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.runEvents(w, r, id); !ok {
		return
	}
	after := int64(0)
	if q := r.URL.Query().Get("after_seq"); q != "" {
		v, err := strconv.ParseInt(q, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid after_seq")
			return
		}
		after = v
	}
	// Browser EventSource reconnects carry the last received id here.
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
	ch, err := s.Store.Watch(r.Context(), id, after)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Live agent progress rides the same stream as "delta" frames: no id
	// (they are not in the log, so a reconnect must not resume from one),
	// and superseded by the agent_message / agent_tool event that follows.
	live, stopLive := s.Runs.SubscribeLive(id)
	defer stopLive()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case d := <-live:
			data, err := json.Marshal(d)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: delta\ndata: %s\n\n", data)
			flusher.Flush()
		case ev, open := <-ch:
			if !open {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				s.Log.Error("sse marshal", "err", err)
				continue
			}
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, data)
			flusher.Flush()
			if s.runEndsAt(r.Context(), ev) {
				return
			}
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

// runEndsAt reports whether ev is where the stream of a run stops: an event
// that ended it, with no later retry on the log to bring it back. A browser's
// EventSource reconnects past that event and is left open, so a page on the
// run still sees what comes after (a retry, the workspace's teardown).
func (s *Server) runEndsAt(ctx context.Context, ev event.Event) bool {
	switch ev.Payload.(type) {
	case *event.RunCompleted, *event.RunAbandoned, *event.RunFailed, *event.RunArchived:
	default:
		return false
	}
	later, err := s.Store.Events(ctx, ev.RunID, ev.Seq)
	if err != nil {
		return false
	}
	for _, e := range later {
		if _, retried := e.Payload.(*event.RunRetried); retried {
			return false
		}
	}
	return true
}

// getWorkspaceLog serves the LIVE tail of the run's workspace exec output
// (optional provisioner capability) — feedback while a stage is working.
func (s *Server) getWorkspaceLog(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	data, err := s.Workspaces.LogTail(r.Context(), r.PathValue("id"), n)
	if err != nil {
		status := http.StatusBadGateway
		switch {
		case strings.Contains(err.Error(), "not found"):
			status = http.StatusNotFound
		case errors.Is(err, wsinfo.ErrNoLiveLog), os.IsNotExist(err):
			status = http.StatusNotFound
		}
		writeErr(w, status, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) getLog(w http.ResponseWriter, r *http.Request) {
	// The log SNAPSHOT is the log (DESIGN-FILES §5.4): the runs-dir per-stage
	// files are retired; this serves the latest attempt's captured log. The
	// live tail during a running stage stays on the workspace-log route.
	id, stage := r.PathValue("id"), r.PathValue("stage")
	if s.Snapshots == nil {
		writeErr(w, http.StatusNotFound, "log storage is not configured")
		return
	}
	snaps, err := s.Snapshots.ForRun(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var best *filestore.Snapshot
	for i := range snaps {
		sn := &snaps[i]
		if sn.Kind != filestore.KindLog || sn.StageID != stage {
			continue
		}
		if best == nil || sn.Iteration > best.Iteration {
			best = sn
		}
	}
	if best == nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no log for stage %s", stage))
		return
	}
	data, _, err := s.Snapshots.Bytes(r.Context(), best.ID)
	if err != nil {
		writeErr(w, http.StatusGone, "this log's content is no longer stored")
		return
	}
	n := 64 << 10
	if q := r.URL.Query().Get("n"); q != "" {
		if v, err := strconv.Atoi(q); err == nil && v > 0 {
			n = v
		}
	}
	if len(data) > n {
		data = data[len(data)-n:]
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}
