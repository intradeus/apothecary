package httpapi

import (
	"errors"
	"net/http"

	"apothecary/internal/engine"
)

// journalRoutes is the run journal as its agents read it (FLOWLANG §9
// journal_read): the shim's tool calls these with the run's own token, so
// a run reads its own journal and no other. Read-only.
func (s *Server) journalRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/runs/{id}/journal", ID: "readRunJournal", Tag: tagRuns,
			Summary: "Every record of the run's journal (journal_read with no id)",
			Doc:     "Every owner decision whole and every pass's record with its verdict, no logs: one file for the tool to write.",
			Handler: s.getJournal, Returns: engine.JournalEntry{},
			Auth: AccessRunToken},
		{Method: "GET", Path: "/api/runs/{id}/journal/{entry}", ID: "readRunJournalEntry", Tag: tagRuns,
			Summary: "One entry of the run's journal: a pass whole, a decision or a note",
			Doc:     "A pass (<stage>-<n>) is its record, its outputs and its full log, as one file; a decision (decision-<n>) or a note (note-<n>) is inline unless past 8 KiB, then a file. 404 for an id the run has not.",
			Handler: s.getJournal, Returns: engine.JournalEntry{},
			Auth: AccessRunToken},
	}
}

func (s *Server) getJournal(w http.ResponseWriter, r *http.Request) {
	e, err := s.Runs.JournalRead(r.Context(), r.PathValue("id"), r.PathValue("entry"))
	switch {
	case errors.Is(err, engine.ErrJournalEntryNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, e)
	}
}
