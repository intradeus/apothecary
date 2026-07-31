// Package tickets is the ticket factory: the one place a ticket comes into
// existence, whether it is typed in by hand or copied from an external tracker
// (Jira). Both paths end at the same confstore write and the same attachment
// claim, so an imported ticket is indistinguishable downstream from a
// hand-written one — runs, flows and workspaces know nothing about Jira.
//
// The import is a COPY, taken once: title, body and attachment bytes are
// pulled into apothecary's own storage. Nothing re-reads the issue later and
// nothing is ever written back, so a tracker outage can only ever fail an
// import — never a run.
package tickets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"apothecary/internal/artifacts"
	"apothecary/internal/confstore"
	"apothecary/internal/ports"
)

// Sources resolves a ticketsource integration id to its adapter. Implemented
// by resolve.Resolver; nil in a Factory means no tracker is wired and the
// import paths report that instead of pretending.
type Sources interface {
	TicketSource(ctx context.Context, integrationID string) (ports.TicketSource, error)
	// TicketSourceKind names the integration's vendor ("jira"), recorded on
	// the ticket as provenance that outlives the integration row.
	TicketSourceKind(ctx context.Context, integrationID string) (string, error)
}

// ErrNoSources is returned when an import is attempted with no ticket-source
// wiring at all (a bare API server).
var ErrNoSources = errors.New("no ticket sources are configured")

// ErrDuplicate marks an issue that is already imported into the project; the
// message names the existing ticket.
var ErrDuplicate = errors.New("already imported")

type Factory struct {
	Conf      *confstore.Service
	Artifacts *artifacts.Store
	Sources   Sources
	Log       *slog.Logger
}

func (f *Factory) log() *slog.Logger {
	if f.Log != nil {
		return f.Log
	}
	return slog.Default()
}

// Create writes a ticket exactly as given (the hand-written path) and claims
// its uploads for the TICKET rather than any run.
func (f *Factory) Create(ctx context.Context, in confstore.Ticket) (confstore.Ticket, error) {
	t, err := f.Conf.CreateTicket(ctx, in)
	if err != nil {
		return confstore.Ticket{}, err
	}
	f.claim(ctx, t)
	return t, nil
}

// Update saves an edit and claims any newly-attached uploads.
func (f *Factory) Update(ctx context.Context, id string, upd confstore.Ticket) (confstore.Ticket, error) {
	t, err := f.Conf.UpdateTicket(ctx, id, upd)
	if err != nil {
		return confstore.Ticket{}, err
	}
	f.claim(ctx, t)
	return t, nil
}

// CreateFromSource copies an external issue into a ticket. Fields set on
// `in` win over the tracker's (the operator may have edited the prefilled
// form); everything else — and every attachment — comes from the source.
//
// Attachments are best-effort: one that is too large or of a type the
// artifact store refuses is skipped and named in the returned warnings, since
// losing one screenshot is no reason to lose the whole import.
func (f *Factory) CreateFromSource(ctx context.Context, sourceID, key string, in confstore.Ticket) (confstore.Ticket, []string, error) {
	src, kind, err := f.source(ctx, sourceID)
	if err != nil {
		return confstore.Ticket{}, nil, err
	}
	remote, err := src.Fetch(ctx, key)
	if err != nil {
		return confstore.Ticket{}, nil, fmt.Errorf("fetch %s: %w", key, err)
	}
	if remote.Key == "" {
		remote.Key = key
	}
	if err := f.checkDuplicate(ctx, in.ProjectID, remote.Key); err != nil {
		return confstore.Ticket{}, nil, err
	}

	t := in
	if strings.TrimSpace(t.Title) == "" {
		t.Title = remote.Title
	}
	if strings.TrimSpace(t.Goal) == "" {
		t.Goal = remote.Body
	}
	t.SourceID, t.SourceKind = sourceID, kind
	t.SourceKey, t.SourceURL = remote.Key, remote.URL

	imported, warnings := f.importAttachments(ctx, src, remote)
	t.ArtifactIDs = append(append([]string{}, in.ArtifactIDs...), imported...)

	created, err := f.Conf.CreateTicket(ctx, t)
	if err != nil {
		return confstore.Ticket{}, nil, err
	}
	f.claim(ctx, created)
	// "The attachments didn't come across" has three very different causes —
	// the tracker reported none, they were skipped, or they landed and the UI
	// didn't show them. One line settles which.
	f.log().Info("imported ticket from source",
		"ticket", created.ID, "key", remote.Key, "kind", kind,
		"attachments_reported", len(remote.Attachments),
		"attachments_copied", len(imported), "skipped", len(warnings))
	return created, warnings, nil
}

// Search serves the import picker.
func (f *Factory) Search(ctx context.Context, sourceID, query string, limit int) ([]ports.SourceTicket, error) {
	src, _, err := f.source(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	return src.Search(ctx, query, limit)
}

// Fetch serves the picker's preview: one issue with its attachment list, so
// the form can prefill and say what will be copied.
func (f *Factory) Fetch(ctx context.Context, sourceID, key string) (ports.SourceTicket, error) {
	src, _, err := f.source(ctx, sourceID)
	if err != nil {
		return ports.SourceTicket{}, err
	}
	return src.Fetch(ctx, key)
}

func (f *Factory) source(ctx context.Context, id string) (ports.TicketSource, string, error) {
	if f.Sources == nil {
		return nil, "", ErrNoSources
	}
	if strings.TrimSpace(id) == "" {
		return nil, "", fmt.Errorf("%w: no source integration given", confstore.ErrInvalid)
	}
	src, err := f.Sources.TicketSource(ctx, id)
	if err != nil {
		return nil, "", err
	}
	kind, err := f.Sources.TicketSourceKind(ctx, id)
	if err != nil {
		return nil, "", err
	}
	return src, kind, nil
}

// checkDuplicate refuses a second copy of the same issue into the same
// project — nearly always a mis-click, and two tickets tracking one issue
// diverge silently. The error names the existing ticket so the UI can link it.
func (f *Factory) checkDuplicate(ctx context.Context, projectID, key string) error {
	if key == "" || projectID == "" {
		return nil
	}
	rows, err := f.Conf.ListTickets(ctx)
	if err != nil {
		return err
	}
	for _, t := range rows {
		if t.ProjectID == projectID && strings.EqualFold(t.SourceKey, key) {
			return fmt.Errorf("%s is %w as ticket %s (%q)", key, ErrDuplicate, t.ID, t.Title)
		}
	}
	return nil
}

// importAttachments pulls each of the source's files through the artifact
// store, returning the ids that landed and a warning per file that did not.
func (f *Factory) importAttachments(ctx context.Context, src ports.TicketSource, remote ports.SourceTicket) ([]string, []string) {
	if len(remote.Attachments) == 0 {
		return nil, nil
	}
	if f.Artifacts == nil {
		return nil, []string{fmt.Sprintf("%d attachment(s) skipped: this server has no artifact store configured", len(remote.Attachments))}
	}
	var ids, warnings []string
	for _, a := range remote.Attachments {
		id, err := f.importOne(ctx, src, a)
		switch {
		case errors.Is(err, artifacts.ErrTooLarge):
			warnings = append(warnings, fmt.Sprintf("%s: too large for this server's artifact limit — attach it by hand if you need it", a.Filename))
		case errors.Is(err, artifacts.ErrMimeType):
			warnings = append(warnings, fmt.Sprintf("%s: file type %q is not on the upload allowlist", a.Filename, a.Mime))
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("%s: %v", a.Filename, err))
			f.log().Warn("import attachment", "file", a.Filename, "ticket", remote.Key, "err", err)
		default:
			ids = append(ids, id)
		}
	}
	return ids, warnings
}

func (f *Factory) importOne(ctx context.Context, src ports.TicketSource, a ports.SourceAttachment) (string, error) {
	rc, err := src.OpenAttachment(ctx, a)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	m, err := f.Artifacts.Save(ctx, a.Filename, a.Mime, io.Reader(rc))
	if err != nil {
		return "", err
	}
	return m.ID, nil
}

// claim binds the ticket's uploads to the TICKET (scope ticket_goal, no run
// id): out of `orphan` so the GC can't sweep them, and shared by every run of
// the ticket. Already-claimed ids are a no-op, so re-sending the full list on
// update is safe.
func (f *Factory) claim(ctx context.Context, t confstore.Ticket) {
	if f.Artifacts == nil || len(t.ArtifactIDs) == 0 {
		return
	}
	if err := f.Artifacts.Associate(ctx, t.ArtifactIDs, "", "", artifacts.ScopeTicketGoal); err != nil {
		f.log().Warn("associate ticket artifacts", "ticket", t.ID, "err", err)
	}
}
