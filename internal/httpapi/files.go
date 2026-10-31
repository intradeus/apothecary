package httpapi

// File routes (DESIGN-FILES §8): uploads, the two byte routes, and the
// per-owner lists. Consistency rules: every list response is an object
// envelope; nil store ⇒ 503 for writes, 404 for bytes, empty-200 for lists;
// all bytes stream through ONE helper with the trust-class headers.

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/agentspec"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/reviewfile"
	"apothecary/internal/run"
)

func mimeByExt(name string) string {
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		return strings.TrimSpace(strings.Split(ct, ";")[0])
	}
	return "application/octet-stream"
}

// postAttachments stores one or more uploaded files (multipart) as unclaimed
// attachments and returns their ids; the ticket/run/answer request then
// claims them by id. The repo's only multipart handler — bodies are streamed
// part by part (no temp-file spill), each capped by the upload policy.
func (s *Server) postAttachments(w http.ResponseWriter, r *http.Request) {
	if s.Attachments == nil {
		writeErr(w, http.StatusServiceUnavailable, "file uploads are not configured")
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	out := []filestore.Attachment{}
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
		if len(out) >= filestore.MaxUploadFiles {
			part.Close()
			writeErr(w, http.StatusBadRequest,
				fmt.Sprintf("at most %d files per upload", filestore.MaxUploadFiles))
			return
		}
		a, err := s.Attachments.Save(r.Context(), auth.UserID(r.Context()), name, part.Header.Get("Content-Type"), part)
		part.Close()
		switch {
		case errors.Is(err, filestore.ErrTooLarge):
			writeErr(w, http.StatusRequestEntityTooLarge, "file too large: "+name)
			return
		case errors.Is(err, filestore.ErrMimeType):
			writeErr(w, http.StatusUnsupportedMediaType, "file type not allowed: "+name)
			return
		case err != nil:
			writeErr(w, http.StatusInternalServerError, "upload failed: "+err.Error())
			return
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		writeErr(w, http.StatusBadRequest, "no files in upload")
		return
	}
	writeJSON(w, http.StatusCreated, Uploaded{Attachments: out})
}

// getAttachmentLimits mirrors the upload policy to the browser, so a bad pick
// is rejected inline with the reason instead of after a failed round trip.
func (s *Server) getAttachmentLimits(w http.ResponseWriter, r *http.Request) {
	if s.Attachments == nil {
		writeErr(w, http.StatusServiceUnavailable, "file uploads are not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.Attachments.Limits())
}

// activeContent reports MIME types a browser would execute in the page's
// origin. Both byte routes force these to download: snapshot bytes are
// workspace-authored (untrusted), and the upload allowlist's text/* admits
// text/html — either way, agent- or human-authored HTML must not run on the
// control-plane origin (DESIGN-FILES §8).
func activeContent(mime string) bool {
	m := strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))
	switch m {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "application/xml", "text/xml":
		return true
	}
	return false
}

// serveFile is the ONE byte-serving helper: verified blob content, streamed
// with ranges, nosniff + a no-script CSP, active types forced to download. A
// reference whose bytes are gone answers 410 with the reason — the metadata
// outlives the content by design (§3.1).
func serveFile(w http.ResponseWriter, r *http.Request, f interface {
	io.ReadSeeker
	io.Closer
}, filename, mimeType string, err error) {
	switch {
	case errors.Is(err, filestore.ErrNotFound):
		writeErr(w, http.StatusNotFound, "file not found")
		return
	case errors.Is(err, filestore.ErrContentGone):
		writeErr(w, http.StatusGone, "this file's content is no longer stored (expired or lost); its record remains")
		return
	case errors.Is(err, filestore.ErrContentCorrupt):
		writeErr(w, http.StatusGone, "this file's stored content failed its integrity check and will not be served")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	disp := "inline"
	if r.URL.Query().Get("download") != "" || activeContent(mimeType) {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", disp+`; filename="`+filename+`"`)
	http.ServeContent(w, r, filename, time.Time{}, f)
}

// getAttachment streams one human-given file.
func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request) {
	if s.Attachments == nil {
		writeErr(w, http.StatusNotFound, "file not found")
		return
	}
	f, a, err := s.Attachments.Open(r.Context(), r.PathValue("id"))
	serveFile(w, r, f, a.Filename, a.Mime, err)
}

// getSnapshot streams one engine-captured record (workspace-authored bytes —
// the trust-class headers in serveFile are the point).
func (s *Server) getSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.Snapshots == nil {
		writeErr(w, http.StatusNotFound, "file not found")
		return
	}
	f, sn, err := s.Snapshots.Open(r.Context(), r.PathValue("id"))
	serveFile(w, r, f, sn.Filename, sn.Mime, err)
}

// getRunFiles is the run page's ONE fetch: the run's attachments (goal +
// answers, PingID says which) and its snapshots (outputs + logs; ping files
// render on their ping).
func (s *Server) getRunFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	attachments := []filestore.Attachment{}
	snapshots := []filestore.Snapshot{}
	if s.Attachments != nil {
		atts, err := s.Attachments.ForRun(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		attachments = append(attachments, atts...)
	}
	if s.Snapshots != nil {
		snaps, err := s.Snapshots.ForRun(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, sn := range snaps {
			if sn.Kind == filestore.KindPingFile {
				continue
			}
			snapshots = append(snapshots, sn)
		}
	}
	writeJSON(w, http.StatusOK, RunFiles{Attachments: attachments, Snapshots: snapshots})
}

// pingView resolves one ping from the durable projection (finished runs
// answer too) with an engine-registry fallback for bare test servers.
func (s *Server) pingView(r *http.Request, pid string) (run.PingView, error) {
	if s.Pings != nil {
		return s.Pings.GetPing(r.Context(), pid)
	}
	if p := s.Runs.GetPing(pid); p != nil {
		return run.PingView{
			PingID: p.ID, RunID: p.RunID, Type: p.Type, StageID: p.StageID,
			Question: p.Question, Context: p.Context,
			Files: p.Files, FileTypes: p.FileTypes, Link: p.Link, PayloadRef: p.Primary(),
			Form: p.Form, AnswerKind: p.AnswerKind, Status: "pending", RaisedAt: p.RaisedAt,
		}, nil
	}
	return run.PingView{}, fmt.Errorf("ping %s not found", pid)
}

// getPingFiles lists a ping's attached files: the declared refs (from the
// projection — no in-memory registry, so finished runs answer) joined
// server-side with the snapshots captured at raise time. Primary first
// (files[0] matches the ping's payload_ref). An uncaptured ref lists with
// captured:false — visible, not silently substituted.
func (s *Server) getPingFiles(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	view, err := s.pingView(r, pid)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	snaps := map[string]filestore.Snapshot{}
	if s.Snapshots != nil {
		list, err := s.Snapshots.ForPing(r.Context(), pid)
		if err != nil {
			s.Log.Warn("ping snapshots", "ping", pid, "err", err)
		}
		for _, sn := range list {
			if _, dup := snaps[sn.Path]; !dup {
				snaps[sn.Path] = sn
			}
		}
	}
	files := []PingFile{}
	for _, ref := range view.Files {
		item := PingFile{Path: ref, Mime: mimeByExt(ref), Type: view.FileTypes[ref]}
		if sn, ok := snaps[ref]; ok {
			item.Captured = true
			item.SnapshotID = sn.ID
			item.Mime = sn.Mime
			item.Size = sn.Size
		}
		files = append(files, item)
	}
	writeJSON(w, http.StatusOK, PingFiles{Files: files, Link: view.Link})
}

// getPingCuration decodes a question's review documents as the gate showed
// them — each shown file of a review type from its raise-time snapshot —
// for the curation page: the page never parses a review itself, the one
// parser the engine posts with does. A file not captured, or not holding
// what its type promises, lists with the reason: it can only be approved
// as written.
func (s *Server) getPingCuration(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	view, err := s.pingView(r, pid)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	snaps := map[string]filestore.Snapshot{}
	if s.Snapshots != nil {
		list, err := s.Snapshots.ForPing(r.Context(), pid)
		if err != nil {
			s.Log.Warn("ping snapshots", "ping", pid, "err", err)
		}
		for _, sn := range list {
			if _, dup := snaps[sn.Path]; !dup && sn.Kind == filestore.KindPingFile {
				snaps[sn.Path] = sn
			}
		}
	}
	out := CurationView{Documents: []CurationDocument{}}
	for _, ref := range view.Files {
		typ := view.FileTypes[ref]
		switch typ {
		case agentspec.TypeReviewThreads, agentspec.TypeReviewReplies, agentspec.TypeMRThreads:
		default:
			continue
		}
		doc := CurationDocument{Path: ref, Type: typ}
		sn, ok := snaps[ref]
		if !ok {
			doc.Error = "not captured when the question was raised"
			out.Documents = append(out.Documents, doc)
			continue
		}
		data, _, err := s.Snapshots.Bytes(r.Context(), sn.ID)
		if err != nil {
			doc.Error = err.Error()
			out.Documents = append(out.Documents, doc)
			continue
		}
		doc.Captured = true
		switch typ {
		case agentspec.TypeReviewThreads:
			if rev, err := reviewfile.ParseReview(ref, data); err != nil {
				doc.Error = err.Error()
			} else {
				doc.Review = rev
			}
		case agentspec.TypeReviewReplies:
			if list, err := reviewfile.ParseReplies(ref, data); err != nil {
				doc.Error = err.Error()
			} else if list == nil {
				doc.Replies = []reviewfile.Reply{}
			} else {
				doc.Replies = list
			}
		case agentspec.TypeMRThreads:
			if th, err := reviewfile.ParseThreads(ref, data); err != nil {
				doc.Error = err.Error()
			} else if out.Threads == nil {
				out.Threads = th
			}
		}
		out.Documents = append(out.Documents, doc)
	}
	writeJSON(w, http.StatusOK, out)
}

// fileRoutes is uploads and the captured record.
func (s *Server) fileRoutes() []Route {
	return []Route{
		{Method: "POST", Path: "/api/attachments", ID: "uploadAttachments", Tag: tagFiles,
			Summary: "Upload files to attach to a ticket, a goal or an answer",
			Handler: s.postAttachments, Returns: Uploaded{}, Status: http.StatusCreated,
			Accepts: "multipart/form-data"},
		{Method: "GET", Path: "/api/attachments/limits", ID: "getAttachmentLimits", Tag: tagFiles,
			Summary: "Upload policy: size cap and accepted types",
			Handler: s.getAttachmentLimits, Returns: filestore.UploadLimits{}},
		{Method: "GET", Path: "/api/attachments/{id}", ID: "downloadAttachment", Tag: tagFiles,
			Summary: "Download one uploaded file",
			Handler: s.getAttachment, Produces: "application/octet-stream", Scope: ScopeAttachment},
		{Method: "GET", Path: "/api/snapshots/{id}", ID: "downloadSnapshot", Tag: tagFiles,
			Summary: "Download one engine-captured record",
			Handler: s.getSnapshot, Produces: "application/octet-stream", Scope: ScopeSnapshot},
		{Method: "GET", Path: "/api/runs/{id}/files", ID: "getRunFiles", Tag: tagFiles,
			Summary: "Everything filed under a run: human uploads and engine captures",
			Handler: s.getRunFiles, Returns: RunFiles{}, Scope: ScopeRun},
	}
}
