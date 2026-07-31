package httpapi

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"apothecary/internal/artifacts"
	"apothecary/internal/engine"
)

// postArtifacts stores one or more uploaded files (multipart) as orphan
// artifacts and returns their ids; the ticket/answer request then attaches
// them by id. The repo's only multipart handler — bodies are streamed part
// by part (no temp-file spill), each capped by the artifact store.
func (s *Server) postArtifacts(w http.ResponseWriter, r *http.Request) {
	if s.Artifacts == nil {
		writeErr(w, http.StatusServiceUnavailable, "artifact uploads are not configured")
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	out := []map[string]any{}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "malformed upload: "+err.Error())
			return
		}
		name := part.FileName()
		if name == "" { // non-file field
			part.Close()
			continue
		}
		if len(out) >= 8 {
			part.Close()
			writeErr(w, http.StatusBadRequest, "at most 8 files per upload")
			return
		}
		m, err := s.Artifacts.Save(r.Context(), name, part.Header.Get("Content-Type"), part)
		part.Close()
		switch {
		case errors.Is(err, artifacts.ErrTooLarge):
			writeErr(w, http.StatusRequestEntityTooLarge, "file too large: "+name)
			return
		case errors.Is(err, artifacts.ErrMimeType):
			writeErr(w, http.StatusUnsupportedMediaType, "file type not allowed: "+name)
			return
		case err != nil:
			writeErr(w, http.StatusInternalServerError, "upload failed: "+err.Error())
			return
		}
		out = append(out, map[string]any{"id": m.ID, "filename": m.Filename, "mime": m.Mime, "size": m.Size})
	}
	if len(out) == 0 {
		writeErr(w, http.StatusBadRequest, "no files in upload")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// getArtifact streams a stored artifact's bytes (the API's only binary route)
// with its content type — inline for preview, attachment on ?download=1.
func (s *Server) getArtifact(w http.ResponseWriter, r *http.Request) {
	if s.Artifacts == nil {
		writeErr(w, http.StatusServiceUnavailable, "artifacts are not configured")
		return
	}
	data, m, err := s.Artifacts.Bytes(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "artifact not found")
		return
	}
	w.Header().Set("Content-Type", m.Mime)
	disp := "inline"
	if r.URL.Query().Get("download") != "" {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", disp+`; filename="`+m.Filename+`"`)
	http.ServeContent(w, r, m.Filename, m.CreatedAt, bytes.NewReader(data))
}

// getRunArtifacts lists a run's attachments grouped as {goal, answers:{ping_id}}
// for the run-page and answered-ping chips.
func (s *Server) getRunArtifacts(w http.ResponseWriter, r *http.Request) {
	goal := []map[string]any{}
	answers := map[string][]map[string]any{}
	if s.Artifacts != nil {
		metas, err := s.Artifacts.ForRun(r.Context(), r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, m := range metas {
			item := map[string]any{
				"id": m.ID, "filename": m.Filename, "mime": m.Mime, "size": m.Size,
				"scope": m.Scope, "created_at": m.CreatedAt,
			}
			switch m.Scope {
			case artifacts.ScopeGoal:
				goal = append(goal, item)
			case artifacts.ScopePingAnswer:
				answers[m.PingID] = append(answers[m.PingID], item)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"goal": goal, "answers": answers})
}

// getRunAttempts serves the run's per-attempt snapshots (PLAN5 M27): what
// each stage attempt produced and printed, grouped by stage and iteration, so
// a finished run's timeline can render "plan (attempt 1) → [plan.md] · [log]"
// long after the workspace is gone. Bytes come from the ordinary artifact
// route; this lists the references.
func (s *Server) getRunAttempts(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	if s.Artifacts != nil {
		metas, err := s.Artifacts.ForStage(r.Context(), r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, m := range metas {
			out = append(out, map[string]any{
				"id": m.ID, "stage_id": m.StageID, "iteration": m.Iteration,
				"scope": m.Scope, "name": m.OutputName, "filename": m.Filename,
				"mime": m.Mime, "size": m.Size, "sha256": m.SHA256,
				"created_at": m.CreatedAt,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"attempts": out})
}

// getPingArtifacts serves a ping's AGENT-attached workspace files (multi
// ask_human). With ?path=<ref> it streams that one file (validated against
// the ping's refs); without, it lists them. The primary reviewed doc keeps
// its own rich-review route (GET .../artifact).
func (s *Server) getPingArtifacts(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	if ref := r.URL.Query().Get("path"); ref != "" {
		data, err := s.Engine.PingArtifactAt(r.Context(), pid, ref)
		switch {
		case err == nil:
			w.Header().Set("Content-Type", mimeByExt(ref))
			http.ServeContent(w, r, filepath.Base(ref), time.Time{}, bytes.NewReader(data))
		case errors.Is(err, engine.ErrPingNotFound):
			writeErr(w, http.StatusNotFound, err.Error())
		case errors.Is(err, engine.ErrArtifactIsURL):
			writeErr(w, http.StatusConflict, err.Error())
		default:
			writeErr(w, http.StatusBadGateway, "artifact read failed: "+err.Error())
		}
		return
	}
	files := []map[string]any{}
	for _, ref := range s.Engine.PingRefs(pid) {
		if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
			continue // a URL payload (mr_url), not a file
		}
		files = append(files, map[string]any{"path": ref, "mime": mimeByExt(ref)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func mimeByExt(name string) string {
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		return strings.TrimSpace(strings.Split(ct, ";")[0])
	}
	return "application/octet-stream"
}
