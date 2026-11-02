package httpapi

import (
	"errors"
	"net/http"

	"apothecary/internal/account/auth"
	"apothecary/internal/notices"
)

// Notices: problems with apothecary itself the caller must act on.

func (s *Server) listNotices(w http.ResponseWriter, r *http.Request) {
	list, err := s.Notices.List(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []notices.Notice{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) dismissNotice(w http.ResponseWriter, r *http.Request) {
	switch err := s.Notices.Dismiss(r.Context(), r.PathValue("id")); {
	case errors.Is(err, notices.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) dismissAllNotices(w http.ResponseWriter, r *http.Request) {
	if err := s.Notices.DismissAll(r.Context(), auth.UserID(r.Context())); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// noticeRoutes is the caller's notice feed.
func (s *Server) noticeRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/notices", ID: "listNotices", Tag: tagNotices,
			Summary: "One's undismissed notices, newest first",
			Handler: s.listNotices, Returns: []notices.Notice{}, Enabled: haveNotices},
		{Method: "POST", Path: "/api/notices/{id}/dismiss", ID: "dismissNotice", Tag: tagNotices,
			Summary: "Dismiss a notice",
			Handler: s.dismissNotice, Status: http.StatusNoContent, Scope: ScopeNotice, Enabled: haveNotices},
		{Method: "POST", Path: "/api/notices/dismiss", ID: "dismissAllNotices", Tag: tagNotices,
			Summary: "Dismiss all of one's notices",
			Handler: s.dismissAllNotices, Status: http.StatusNoContent, Enabled: haveNotices},
	}
}
