package httpapi

import (
	"errors"
	"net/http"

	"apothecary/internal/engine"
	"apothecary/internal/run/ref"
)

// journalRoutes is the run journal as its agents read it (FLOWLANG §9
// journal_read): the shim's tool calls these with the run's own token, so
// a run reads its own journal and no other. Read-only.
func (s *Server) journalRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/runs/{id}/journal", ID: "readRunJournal", Tag: tagRuns,
			Summary: "Every record of the run's journal (journal_read with no reference)",
			Doc:     "Every decision and note whole and every attempt's record with its verdict, no logs: one file for the tool to write (run.md).",
			Handler: s.getJournal, Returns: engine.JournalEntry{},
			Auth: AccessRunToken},
		{Method: "GET", Path: "/api/runs/{id}/journal/{ref...}", ID: "readRunJournalEntry", Tag: tagRuns,
			Summary: "One entry of the run's journal, by its reference: an attempt whole, a decision or a note",
			Doc: "The reference is " + ref.Grammar + ", its # sent as %23. An attempt (review#5) is its record, its outputs and its full log, as one file (review#5.md); " +
				"a decision (build#3/decision#1) or a note (build#3/note#2) is inline unless past 8 KiB, then a file at its reference (build#3/decision#1.md); run is every record. " +
				"An attempt's outputs that are not text are listed in beside; ?file=<name> gives one's bytes, as application/octet-stream, for the tool to write beside the attempt's file. " +
				"400 for what is not a reference, 404 for one the run has not, 410 for an output whose bytes are no longer kept.",
			Query:   []Param{{Name: "file", Doc: "an output the attempt lists in beside, by its name: its bytes instead of the entry"}},
			Handler: s.getJournal, Returns: engine.JournalEntry{},
			Auth: AccessRunToken},
	}
}

func (s *Server) getJournal(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("file") {
		s.getJournalBeside(w, r)
		return
	}
	e, err := s.Runs.JournalRead(r.Context(), r.PathValue("id"), r.PathValue("ref"))
	switch {
	case errors.Is(err, ref.ErrMalformed):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, engine.ErrJournalEntryNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, e)
	}
}

// getJournalBeside streams an output written beside an attempt's file:
// workspace-authored bytes, served as bytes (serveFile's headers).
func (s *Server) getJournalBeside(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("file")
	id, err := s.Runs.JournalBeside(r.Context(), r.PathValue("id"), r.PathValue("ref"), name)
	switch {
	case errors.Is(err, ref.ErrMalformed):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, engine.ErrJournalEntryNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	case s.Snapshots == nil:
		writeErr(w, http.StatusGone, "this file's content is no longer stored (expired or lost); its record remains")
		return
	}
	f, _, err := s.Snapshots.Open(r.Context(), id)
	serveFile(w, r, f, name, "application/octet-stream", err)
}
