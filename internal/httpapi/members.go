package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/authz"
	"apothecary/internal/project/confstore"
)

// Members: a workshop shared with other accounts, each as viewer, operator
// or maintainer (authz decides what each holds). The owner is never a
// member; what a maintainer may change stops at maintainers, which are the
// owner's to add, change and remove.

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	ms, err := s.Conf.ListMembers(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	out := make([]Member, 0, len(ms))
	for _, m := range ms {
		out = append(out, s.member(r, m))
	}
	writeJSON(w, http.StatusOK, out)
}

// member is a row with its account's handle and name (an account gone from
// the store reads as its id).
func (s *Server) member(r *http.Request, m confstore.Member) Member {
	out := Member{UserID: m.UserID, Handle: m.UserID, Role: m.Role, CreatedAt: m.CreatedAt}
	if u, err := s.Auth.GetUser(r.Context(), m.UserID); err == nil {
		out.Handle, out.Name = u.Handle, u.Name
	}
	return out
}

// mayTouch is the owner-only rule: adding, changing or removing a
// maintainer is the owner's. A maintainer manages viewers and operators.
func (s *Server) mayTouch(w http.ResponseWriter, r *http.Request, roles ...string) bool {
	for _, role := range roles {
		if role != confstore.MemberMaintainer {
			continue
		}
		_, mine, err := s.Conf.ProjectAccess(r.Context(), r.PathValue("id"), auth.UserID(r.Context()))
		if err != nil {
			confErr(w, err)
			return false
		}
		if mine != confstore.RoleOwner {
			writeErr(w, http.StatusForbidden, "only the workshop's owner adds, changes or removes a maintainer")
			return false
		}
	}
	return true
}

func (s *Server) addMember(w http.ResponseWriter, r *http.Request) {
	var body MemberRequest
	if !readJSON(w, r, &body) {
		return
	}
	login := strings.TrimSpace(body.Login)
	if login == "" {
		writeErr(w, http.StatusBadRequest, "login is required: a handle or an email")
		return
	}
	u, err := s.Auth.GetUserByLogin(r.Context(), login)
	if err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "no account "+login)
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	pid := r.PathValue("id")
	roles := []string{body.Role}
	if old, err := s.Conf.GetMember(r.Context(), pid, u.ID); err == nil {
		roles = append(roles, old.Role)
	}
	if !s.mayTouch(w, r, roles...) {
		return
	}
	m, err := s.Conf.PutMember(r.Context(), confstore.Member{ProjectID: pid, UserID: u.ID, Role: body.Role})
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.member(r, m))
}

func (s *Server) updateMember(w http.ResponseWriter, r *http.Request) {
	var body MemberRoleRequest
	if !readJSON(w, r, &body) {
		return
	}
	pid, uid := r.PathValue("id"), r.PathValue("uid")
	old, err := s.Conf.GetMember(r.Context(), pid, uid)
	if err != nil {
		confErr(w, err)
		return
	}
	if !s.mayTouch(w, r, old.Role, body.Role) {
		return
	}
	m, err := s.Conf.PutMember(r.Context(), confstore.Member{ProjectID: pid, UserID: uid, Role: body.Role})
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.member(r, m))
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request) {
	pid, uid := r.PathValue("id"), r.PathValue("uid")
	old, err := s.Conf.GetMember(r.Context(), pid, uid)
	if err != nil {
		confErr(w, err)
		return
	}
	if !s.mayTouch(w, r, old.Role) {
		return
	}
	if err := s.Conf.DeleteMember(r.Context(), pid, uid); err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// leaveProject is a member taking itself off a workshop: reading it is
// enough. The owner cannot leave its own.
func (s *Server) leaveProject(w http.ResponseWriter, r *http.Request) {
	pid, me := r.PathValue("id"), auth.UserID(r.Context())
	if _, err := s.Conf.GetMember(r.Context(), pid, me); err != nil {
		writeErr(w, http.StatusBadRequest, "only a member can leave a workshop; its owner deletes it")
		return
	}
	if err := s.Conf.DeleteMember(r.Context(), pid, me); err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// memberRoutes is sharing a workshop.
func (s *Server) memberRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/projects/{id}/members", ID: "listMembers", Tag: tagProjects,
			Summary: "The accounts a project is shared with, and their roles",
			Handler: s.listMembers, Returns: []Member{}, Enabled: haveConf, Scope: ScopeProject, Action: authz.ProjectRead},
		{Method: "POST", Path: "/api/projects/{id}/members", ID: "addMember", Tag: tagProjects,
			Summary: "Share a project with an account, by handle or email",
			Doc:     "role is viewer, operator or maintainer; sharing with a member again changes its role. Only the owner adds a maintainer or changes one. 404 for an unknown account; 400 for the owner itself.",
			Handler: s.addMember, Body: MemberRequest{}, Returns: Member{}, Status: http.StatusCreated, Enabled: haveConf, Scope: ScopeProject, Action: authz.ProjectMembers},
		{Method: "PUT", Path: "/api/projects/{id}/members/{uid}", ID: "updateMember", Tag: tagProjects,
			Summary: "Change a member's role",
			Doc:     "Only the owner makes or unmakes a maintainer.",
			Handler: s.updateMember, Body: MemberRoleRequest{}, Returns: Member{}, Enabled: haveConf, Scope: ScopeProject, Action: authz.ProjectMembers},
		{Method: "DELETE", Path: "/api/projects/{id}/members/{uid}", ID: "removeMember", Tag: tagProjects,
			Summary: "Stop sharing a project with a member",
			Doc:     "Only the owner removes a maintainer.",
			Handler: s.removeMember, Status: http.StatusNoContent, Enabled: haveConf, Scope: ScopeProject, Action: authz.ProjectMembers},
		{Method: "DELETE", Path: "/api/projects/{id}/members/me", ID: "leaveProject", Tag: tagProjects,
			Summary: "Leave a project shared with one",
			Handler: s.leaveProject, Status: http.StatusNoContent, Enabled: haveConf, Scope: ScopeProject, Action: authz.ProjectRead},
	}
}
