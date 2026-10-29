package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"

	"apothecary/internal/auth"
	"apothecary/internal/ports"
	"apothecary/internal/skills"
	"apothecary/internal/skillstore"
)

// The skill library (TODO 38): a user's skills and skillsets. Every route
// here is the caller's own: lists and creates stamp the principal, the
// id'd routes are scoped by the guard (ScopeSkill / ScopeSkillset).

// maxSkillBody bounds a skill write: the files' cap plus base64 and JSON
// overhead (readJSON's 1 MiB is for documents, not file sets).
const maxSkillBody = (skills.MaxSkillBytes * 4 / 3) + 1<<20

func readSkillJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxSkillBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Skills.ListSkills(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		confErr(w, err)
		return
	}
	if rows == nil {
		rows = []skillstore.Skill{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// skillInput decodes a SkillRequest's files; a bad base64 is a 400 naming
// the file.
func skillInput(w http.ResponseWriter, body SkillRequest) (skillstore.SkillInput, bool) {
	in := skillstore.SkillInput{Name: body.Name, Files: make([]ports.TreeFile, 0, len(body.Files))}
	for _, f := range body.Files {
		data, err := base64.StdEncoding.DecodeString(f.ContentB64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("%s: content_b64 is not base64", f.Path))
			return in, false
		}
		in.Files = append(in.Files, ports.TreeFile{Path: f.Path, Mode: fs.FileMode(f.Mode), Data: data})
	}
	return in, true
}

func (s *Server) createSkill(w http.ResponseWriter, r *http.Request) {
	var body SkillRequest
	if !readSkillJSON(w, r, &body) {
		return
	}
	in, ok := skillInput(w, body)
	if !ok {
		return
	}
	sk, err := s.Skills.CreateSkill(r.Context(), auth.UserID(r.Context()), in)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sk)
}

func (s *Server) getSkill(w http.ResponseWriter, r *http.Request) {
	sk, err := s.Skills.GetSkill(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	files, err := s.Skills.SkillFiles(r.Context(), sk.ID)
	if err != nil {
		confErr(w, err)
		return
	}
	out := SkillDetail{Skill: sk, Contents: make([]SkillFileRequest, 0, len(files))}
	for _, f := range files {
		out.Contents = append(out.Contents, SkillFileRequest{Path: f.Path, ContentB64: base64.StdEncoding.EncodeToString(f.Data), Mode: uint32(f.Mode)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateSkill(w http.ResponseWriter, r *http.Request) {
	var body SkillRequest
	if !readSkillJSON(w, r, &body) {
		return
	}
	in, ok := skillInput(w, body)
	if !ok {
		return
	}
	sk, err := s.Skills.UpdateSkill(r.Context(), r.PathValue("id"), in)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sk)
}

func (s *Server) deleteSkill(w http.ResponseWriter, r *http.Request) {
	if err := s.Skills.DeleteSkill(r.Context(), r.PathValue("id")); err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) importSkills(w http.ResponseWriter, r *http.Request) {
	if s.SkillImports == nil {
		writeErr(w, http.StatusServiceUnavailable, "importing skills is not configured on this server")
		return
	}
	var body SkillImportRequest
	if !readJSON(w, r, &body) {
		return
	}
	res, err := s.SkillImports.Import(r.Context(), auth.UserID(r.Context()), body.URL, body.Path)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) refreshSkill(w http.ResponseWriter, r *http.Request) {
	if s.SkillImports == nil {
		writeErr(w, http.StatusServiceUnavailable, "importing skills is not configured on this server")
		return
	}
	sk, err := s.SkillImports.RefreshSkill(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sk)
}

// --- skillsets --------------------------------------------------------------

func (s *Server) listSkillsets(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Skills.ListSkillsets(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		confErr(w, err)
		return
	}
	if rows == nil {
		rows = []skillstore.Skillset{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func skillsetInput(body SkillsetRequest) skillstore.SkillsetInput {
	return skillstore.SkillsetInput{Name: body.Name, Description: body.Description, SkillIDs: body.SkillIDs}
}

func (s *Server) createSkillset(w http.ResponseWriter, r *http.Request) {
	var body SkillsetRequest
	if !readJSON(w, r, &body) {
		return
	}
	set, err := s.Skills.CreateSkillset(r.Context(), auth.UserID(r.Context()), skillsetInput(body))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, set)
}

func (s *Server) getSkillset(w http.ResponseWriter, r *http.Request) {
	set, err := s.Skills.GetSkillset(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) updateSkillset(w http.ResponseWriter, r *http.Request) {
	var body SkillsetRequest
	if !readJSON(w, r, &body) {
		return
	}
	set, err := s.Skills.UpdateSkillset(r.Context(), r.PathValue("id"), skillsetInput(body))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) deleteSkillset(w http.ResponseWriter, r *http.Request) {
	if err := s.Skills.DeleteSkillset(r.Context(), r.PathValue("id")); err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listSkillsetVersions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Skills.ListVersions(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	if rows == nil {
		rows = []skillstore.Version{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) getSkillsetVersion(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.Atoi(r.PathValue("seq"))
	if err != nil || seq < 0 {
		writeErr(w, http.StatusBadRequest, "seq must be a version number (0 = latest)")
		return
	}
	v, err := s.Skills.GetVersion(r.Context(), r.PathValue("id"), seq)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) refreshSkillset(w http.ResponseWriter, r *http.Request) {
	if s.SkillImports == nil {
		writeErr(w, http.StatusServiceUnavailable, "importing skills is not configured on this server")
		return
	}
	set, warnings, err := s.SkillImports.RefreshSkillset(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SkillsetRefreshResult{Skillset: set, Warnings: warnings})
}
