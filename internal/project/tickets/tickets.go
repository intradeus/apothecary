// Package tickets is the ticket factory: the one place a ticket comes into
// existence or leaves it, whether typed in by hand or imported from an
// external tracker (Jira, GitHub Issues, GitLab). Both creation paths end at
// the same confstore write and the same attachment claim, so downstream —
// runs, flows, workspaces — a ticket is a ticket.
//
// Every ticket has one tracker (docs/ticket-sync-plan.md). A hand-written one
// (and a merge request's) lives on its owner's INTERNAL tracker: its status,
// type, labels, comments and links are the row's own, written in place
// (package internaltracker). An imported one is a MIRROR of its issue: the
// remote is the single source of truth for title, body, status, type, labels
// and comments, the import takes the first snapshot (mirror.go) and the sync
// keeps taking them; writes go to the remote and come back with the next
// snapshot. Attachment bytes are copied into apothecary's own storage at
// import, and again by the sync for each file the issue gains since (keyed
// by the tracker's id for it), so a tracker outage never stalls a run.
package tickets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/branchname"
	"apothecary/internal/project/confstore"
)

// Sources resolves a ticketsource integration id to its adapter. Implemented
// by integrations.Resolver; nil in a Factory means no tracker is wired and the
// import paths report that instead of pretending.
type Sources interface {
	TicketSource(ctx context.Context, integrationID string) (trackerport.TicketSource, error)
	// TicketSourceKind names the integration's vendor ("jira"), recorded on
	// the ticket as provenance that outlives the integration row.
	TicketSourceKind(ctx context.Context, integrationID string) (string, error)
}

// ErrNoSources is returned when an import is attempted with no ticket-source
// wiring at all (a bare API server).
var ErrNoSources = errors.New("no ticket sources are configured")

// ErrDuplicate marks an issue that already has a ticket: one issue of one
// tracker is one ticket per user. *DuplicateError carries it.
var ErrDuplicate = errors.New("already imported")

// DuplicateError names the ticket an issue already is, so the UI can link it
// and an automation can adopt it.
type DuplicateError struct {
	Key    string
	Ticket confstore.Ticket
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("%s is %v as ticket %s (%q)", e.Key, ErrDuplicate, e.Ticket.ID, e.Ticket.Title)
}

func (e *DuplicateError) Is(target error) bool { return target == ErrDuplicate }

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
// uploads for the TICKET rather than any run. It lands on its owner's
// internal tracker — in the status holding the todo step and the type of its
// change type, unless given others (confstore validates them). A base branch
// not given is named by its workshop's branch format (package branchname)
// from its change type (a feature by default) and title; by the default
// when the workshop cannot be read (CreateTicket refuses a missing one).
func (f *Factory) Create(ctx context.Context, in confstore.Ticket, attachmentIDs []string) (confstore.Ticket, error) {
	in.ChangeType = normalType(in.ChangeType)
	if in.ChangeType == "" {
		in.ChangeType = trackerport.ChangeFeat
	}
	if strings.TrimSpace(in.BaseBranch) == "" {
		format := ""
		if p, err := f.Conf.GetProject(ctx, in.ProjectID); err == nil {
			format = p.Repo.BranchFormat
		}
		in.BaseBranch = branchname.Render(format, string(in.ChangeType), "", in.Title)
	}
	t, err := f.Conf.CreateTicket(ctx, in)
	if err != nil {
		return confstore.Ticket{}, err
	}
	f.claim(ctx, t, attachmentIDs)
	return t, nil
}

// Update saves an edit and claims any newly-attached uploads (already-claimed
// ids are a no-op, so re-sending the full list is safe).
func (f *Factory) Update(ctx context.Context, id string, upd confstore.Ticket, attachmentIDs []string) (confstore.Ticket, error) {
	t, err := f.Conf.UpdateTicket(ctx, id, upd)
	if err != nil {
		return confstore.Ticket{}, err
	}
	f.claim(ctx, t, attachmentIDs)
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

// CreateFromSource imports an external issue as a ticket that mirrors it.
// What the tracker owns — title, goal (its body), status, type and so change
// type, labels, comments — is the issue's (mirror.go), whatever `in` says:
// the sync would overwrite it anyway. `in` gives what Apothecary owns: the
// project, the base branch (named by the workshop's branch format from the
// change type, the key and the title when not given), the automation that
// imported it.
//
// One issue is one ticket per user: a second import is *DuplicateError,
// checked before anything is fetched. Attachments are best-effort: one that
// is too large or of a type the upload policy refuses is skipped and named in
// the returned warnings, since losing one screenshot is no reason to lose the
// whole import; so are comments.
func (f *Factory) CreateFromSource(ctx context.Context, sourceID, key string, in confstore.Ticket, attachmentIDs []string) (confstore.Ticket, []string, error) {
	src, kind, err := f.source(ctx, sourceID)
	if err != nil {
		return confstore.Ticket{}, nil, err
	}
	// The copied files and the ticket belong to the project's owner; its
	// branch format names the base branch.
	owner, format := in.UserID, ""
	if p, err := f.Conf.GetProject(ctx, in.ProjectID); err == nil {
		owner, format = p.UserID, p.Repo.BranchFormat
	}
	if err := f.checkDuplicate(ctx, owner, sourceID, key); err != nil {
		return confstore.Ticket{}, nil, err
	}
	remote, err := src.Fetch(ctx, key)
	if err != nil {
		return confstore.Ticket{}, nil, fmt.Errorf("fetch %s: %w", key, err)
	}
	if remote.Key == "" {
		remote.Key = key
	}
	if !strings.EqualFold(remote.Key, key) {
		if err := f.checkDuplicate(ctx, owner, sourceID, remote.Key); err != nil {
			return confstore.Ticket{}, nil, err
		}
	}
	m, ct, err := Mirror(ctx, f.Conf, sourceID, src, remote)
	if err != nil {
		return confstore.Ticket{}, nil, err
	}

	t := in
	t.Title, t.Goal, t.ChangeType = m.Title, m.Goal, ct
	t.StatusID, t.TypeID, t.Labels = m.StatusID, m.TypeID, m.Labels
	t.SourceID, t.SourceKind = sourceID, kind
	t.SourceKey, t.SourceURL, t.SourceType = m.SourceKey, m.SourceURL, m.SourceType
	t.RemoteID = m.RemoteID
	if !m.RemoteUpdated.IsZero() {
		upd := m.RemoteUpdated.UTC()
		t.RemoteUpdatedAt = &upd
	}
	synced := time.Now().UTC()
	t.SyncedAt, t.SyncState, t.SyncError = &synced, confstore.SyncOK, ""
	t.StatusChangedAt, t.StatusChangedBy = nil, ""
	if t.StatusID != "" {
		t.StatusChangedBy = confstore.ChangedBySync
	}
	if strings.TrimSpace(t.BaseBranch) == "" {
		t.BaseBranch = branchname.Render(format, string(t.ChangeType), remote.Key, t.Title)
	}

	imported, warnings := f.importAttachments(ctx, owner, src, remote)

	created, err := f.Conf.CreateTicket(ctx, t)
	if errors.Is(err, confstore.ErrConflict) {
		// Lost a race with another import of the same issue.
		if derr := f.checkDuplicate(ctx, owner, sourceID, remote.Key); derr != nil {
			err = derr
		}
	}
	if err != nil {
		return confstore.Ticket{}, nil, err
	}
	f.claim(ctx, created, append(append([]string{}, attachmentIDs...), imported...))
	if err := ImportComments(ctx, f.Conf, created.ID, src, remote.Key); err != nil {
		warnings = append(warnings, fmt.Sprintf("comments: %v (the next sync retries)", err))
		f.log().Warn("import comments", "ticket", created.ID, "key", remote.Key, "err", err)
	}
	f.log().Info("imported ticket from source",
		"ticket", created.ID, "key", remote.Key, "kind", kind,
		"attachments_reported", len(remote.Attachments),
		"attachments_copied", len(imported), "skipped", len(warnings))
	return created, warnings, nil
}

// Search serves the import picker.
func (f *Factory) Search(ctx context.Context, sourceID, query string, limit int) ([]trackerport.SourceTicket, error) {
	src, _, err := f.source(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	return src.Search(ctx, query, limit)
}

// Fetch serves the picker's preview: one issue with its attachment list, so
// the form can prefill and say what will be copied.
func (f *Factory) Fetch(ctx context.Context, sourceID, key string) (trackerport.SourceTicket, error) {
	src, _, err := f.source(ctx, sourceID)
	if err != nil {
		return trackerport.SourceTicket{}, err
	}
	return src.Fetch(ctx, key)
}

func (f *Factory) source(ctx context.Context, id string) (trackerport.TicketSource, string, error) {
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

// checkDuplicate refuses a second ticket for an issue the user already has
// one for — nearly always a mis-click, and two tickets mirroring one issue
// would only ever say the same thing. An indexed lookup (tickets_source_key_idx).
func (f *Factory) checkDuplicate(ctx context.Context, userID, sourceID, key string) error {
	if key == "" {
		return nil
	}
	t, found, err := f.Conf.TicketBySource(ctx, userID, sourceID, key)
	if err != nil {
		return err
	}
	if found {
		return &DuplicateError{Key: key, Ticket: t}
	}
	return nil
}

// importAttachments pulls each of the source's files through the upload
// policy, returning the ids that landed and a warning per file that did not.
func (f *Factory) importAttachments(ctx context.Context, owner string, src trackerport.TicketSource, remote trackerport.SourceTicket) ([]string, []string) {
	if len(remote.Attachments) == 0 {
		return nil, nil
	}
	if f.Attachments == nil {
		return nil, []string{fmt.Sprintf("%d attachment(s) skipped: this server has no file store configured", len(remote.Attachments))}
	}
	ids, skips := copyAttachments(ctx, f.Attachments, f.log(), owner, src, remote.Key, remote.Attachments)
	var warnings []string
	for _, s := range skips {
		warnings = append(warnings, s.warning())
	}
	return ids, warnings
}

// attachmentSkip is a remote file that did not land, and why.
type attachmentSkip struct {
	file, mime string
	err        error
}

// refused reports a file the upload policy turned away (as opposed to one
// that failed to download or store).
func (s attachmentSkip) refused() bool {
	return errors.Is(s.err, filestore.ErrTooLarge) || errors.Is(s.err, filestore.ErrMimeType)
}

func (s attachmentSkip) warning() string {
	switch {
	case errors.Is(s.err, filestore.ErrTooLarge):
		return fmt.Sprintf("%s: too large for this server's upload limit — attach it by hand if you need it", s.file)
	case errors.Is(s.err, filestore.ErrMimeType):
		return fmt.Sprintf("%s: file type %q is not on the upload allowlist", s.file, s.mime)
	}
	return fmt.Sprintf("%s: %v", s.file, s.err)
}

// copyAttachments downloads remote files (OpenAttachment) into owner's
// unclaimed uploads through the upload policy — size cap, MIME allowlist —
// each recording the tracker's id for it (SaveImported), and returns the ids
// that landed for the caller to claim. A file listed twice under one remote
// id is copied once. Shared by the import and the sync, so a file the sync
// picks up later is held to exactly what the import would have held it to.
func copyAttachments(ctx context.Context, store *filestore.Attachments, log *slog.Logger, owner string, src trackerport.TicketSource, key string, list []trackerport.SourceAttachment) ([]string, []attachmentSkip) {
	var ids []string
	var skips []attachmentSkip
	seen := map[string]bool{}
	for _, a := range list {
		if a.ID != "" {
			if seen[a.ID] {
				continue
			}
			seen[a.ID] = true
		}
		id, err := copyAttachment(ctx, store, owner, src, a)
		if err != nil {
			skip := attachmentSkip{file: a.Filename, mime: a.Mime, err: err}
			if !skip.refused() {
				log.Warn("import attachment", "file", a.Filename, "ticket", key, "err", err)
			}
			skips = append(skips, skip)
			continue
		}
		ids = append(ids, id)
	}
	return ids, skips
}

func copyAttachment(ctx context.Context, store *filestore.Attachments, owner string, src trackerport.TicketSource, a trackerport.SourceAttachment) (string, error) {
	rc, err := src.OpenAttachment(ctx, a)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	att, err := store.SaveImported(ctx, owner, a.ID, a.Filename, a.Mime, io.Reader(rc))
	if err != nil {
		return "", err
	}
	return att.ID, nil
}

// claim binds the ticket owner's uploads to the TICKET: shared by every run of
// it, out of the unclaimed state so the GC can't sweep them. One-shot per
// attachment, so re-sending a full list on update is safe.
func (f *Factory) claim(ctx context.Context, t confstore.Ticket, ids []string) {
	if f.Attachments == nil || len(ids) == 0 {
		return
	}
	if err := f.Attachments.ClaimForTicket(ctx, t.UserID, ids, t.ID); err != nil {
		f.log().Warn("claim ticket attachments", "ticket", t.ID, "err", err)
	}
}

// FindByKey is the project's ticket whose source key is key (compared
// without case), if there is one — an indexed lookup.
func (f *Factory) FindByKey(ctx context.Context, projectID, key string) (confstore.Ticket, bool, error) {
	return f.Conf.TicketByProjectKey(ctx, projectID, key)
}
