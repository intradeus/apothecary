// Package tickets is the ticket factory: the one place a ticket comes into
// existence or leaves it, whether typed in by hand or copied from an external
// tracker (Jira). Both creation paths end at the same confstore write and the
// same attachment claim, so an imported ticket is indistinguishable
// downstream from a hand-written one — runs, flows and workspaces know
// nothing about Jira.
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

	"apothecary/internal/confstore"
	"apothecary/internal/filestore"
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
	Conf        *confstore.Service
	Attachments *filestore.Attachments
	Sources     Sources
	Log         *slog.Logger
}

func (f *Factory) log() *slog.Logger {
	if f.Log != nil {
		return f.Log
	}
	return slog.Default()
}

// Create writes a ticket as given (the hand-written path) and claims its
// uploads for the TICKET rather than any run. A base branch not given is
// named from its change type (a feature by default) and title (BranchName).
func (f *Factory) Create(ctx context.Context, in confstore.Ticket, attachmentIDs []string) (confstore.Ticket, error) {
	in.ChangeType = normalType(in.ChangeType)
	if in.ChangeType == "" {
		in.ChangeType = ports.ChangeFeat
	}
	if strings.TrimSpace(in.BaseBranch) == "" {
		in.BaseBranch = BranchName(in.ChangeType, "", in.Title)
	}
	t, err := f.Conf.CreateTicket(ctx, in)
	if err != nil {
		return confstore.Ticket{}, err
	}
	f.claim(ctx, t.ID, attachmentIDs)
	return t, nil
}

// Update saves an edit and claims any newly-attached uploads (already-claimed
// ids are a no-op, so re-sending the full list is safe).
func (f *Factory) Update(ctx context.Context, id string, upd confstore.Ticket, attachmentIDs []string) (confstore.Ticket, error) {
	t, err := f.Conf.UpdateTicket(ctx, id, upd)
	if err != nil {
		return confstore.Ticket{}, err
	}
	f.claim(ctx, t.ID, attachmentIDs)
	return t, nil
}

// Delete removes the ticket AND its attachment rows — the fix for the old
// leak, where DeleteTicket dropped the row and left the files immortal
// (DESIGN-FILES §12 #3). The blob sweep reclaims the bytes once nothing
// references them; runs that pinned these ids at start keep working (F8).
func (f *Factory) Delete(ctx context.Context, id string) error {
	if f.Attachments != nil {
		if err := f.Attachments.DeleteForTicket(ctx, id); err != nil {
			return fmt.Errorf("ticket %s attachments: %w", id, err)
		}
	}
	return f.Conf.DeleteTicket(ctx, id)
}

// CreateFromSource copies an external issue into a ticket. Fields set on
// `in` win over the tracker's (the operator may have edited the prefilled
// form); everything else — and every attachment — comes from the source.
// The issue's type comes across as the ticket's change type (the
// integration's mapping, else a guess), and a base branch not given is named
// from it, the key and the title (BranchName).
//
// Attachments are best-effort: one that is too large or of a type the
// upload policy refuses is skipped and named in the returned warnings, since
// losing one screenshot is no reason to lose the whole import.
func (f *Factory) CreateFromSource(ctx context.Context, sourceID, key string, in confstore.Ticket, attachmentIDs []string) (confstore.Ticket, []string, error) {
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
	t.ChangeType = normalType(t.ChangeType)
	if t.ChangeType == "" {
		t.ChangeType = SourceChangeType(remote)
	}
	if strings.TrimSpace(t.BaseBranch) == "" {
		t.BaseBranch = BranchName(t.ChangeType, remote.Key, t.Title)
	}
	t.SourceID, t.SourceKind = sourceID, kind
	t.SourceKey, t.SourceURL = remote.Key, remote.URL
	t.SourceType = remote.Type

	// The copied files belong to the project's owner, like the ticket.
	owner := t.UserID
	if p, err := f.Conf.GetProject(ctx, t.ProjectID); err == nil {
		owner = p.UserID
	}
	imported, warnings := f.importAttachments(ctx, owner, src, remote)

	created, err := f.Conf.CreateTicket(ctx, t)
	if err != nil {
		return confstore.Ticket{}, nil, err
	}
	f.claim(ctx, created.ID, append(append([]string{}, attachmentIDs...), imported...))
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
	rows, err := f.Conf.ListTickets(ctx, "")
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

// importAttachments pulls each of the source's files through the upload
// policy, returning the ids that landed and a warning per file that did not.
func (f *Factory) importAttachments(ctx context.Context, owner string, src ports.TicketSource, remote ports.SourceTicket) ([]string, []string) {
	if len(remote.Attachments) == 0 {
		return nil, nil
	}
	if f.Attachments == nil {
		return nil, []string{fmt.Sprintf("%d attachment(s) skipped: this server has no file store configured", len(remote.Attachments))}
	}
	var ids, warnings []string
	for _, a := range remote.Attachments {
		id, err := f.importOne(ctx, owner, src, a)
		switch {
		case errors.Is(err, filestore.ErrTooLarge):
			warnings = append(warnings, fmt.Sprintf("%s: too large for this server's upload limit — attach it by hand if you need it", a.Filename))
		case errors.Is(err, filestore.ErrMimeType):
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

func (f *Factory) importOne(ctx context.Context, owner string, src ports.TicketSource, a ports.SourceAttachment) (string, error) {
	rc, err := src.OpenAttachment(ctx, a)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	att, err := f.Attachments.Save(ctx, owner, a.Filename, a.Mime, io.Reader(rc))
	if err != nil {
		return "", err
	}
	return att.ID, nil
}

// claim binds uploads to the TICKET: shared by every run of it, out of the
// unclaimed state so the GC can't sweep them. One-shot per attachment, so
// re-sending a full list on update is safe.
func (f *Factory) claim(ctx context.Context, ticketID string, ids []string) {
	if f.Attachments == nil || len(ids) == 0 {
		return
	}
	if err := f.Attachments.ClaimForTicket(ctx, ids, ticketID); err != nil {
		f.log().Warn("claim ticket attachments", "ticket", ticketID, "err", err)
	}
}
