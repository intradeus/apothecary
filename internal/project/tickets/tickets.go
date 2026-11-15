// Package tickets is the ticket factory: the one place a ticket comes into
// existence or leaves it, and what a run starts from.
//
// A workshop has exactly one ticket source: a remote tracker (Jira, GitHub
// Issues, GitLab) or its owner's internal tracker. An internal ticket is the
// issue itself, written in place (Create; its status, type, labels, comments
// and links through package internaltracker). A remote issue is read live
// through Live and never mirrored: Apothecary keeps only a small RECORD of it
// (Record), made the first time something points at it — a run starts, an
// automation fires, someone sets a base branch — holding what was last seen
// of it, the base branch, and the files copied from it. A record belongs to
// no workshop: every workshop on its source sees the same one.
package tickets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

// Sources resolves a ticketsource integration id to its adapter. Implemented
// by integrations.Resolver; nil means no tracker is wired and the remote
// reads report that instead of pretending.
type Sources interface {
	TicketSource(ctx context.Context, integrationID string) (trackerport.TicketSource, error)
	// TicketSourceKind names the integration's vendor ("jira").
	TicketSourceKind(ctx context.Context, integrationID string) (string, error)
}

// ErrNoSources is returned when a remote tracker is read with no
// ticket-source wiring at all (a bare API server).
var ErrNoSources = errors.New("no ticket sources are configured")

type Factory struct {
	Conf        *confstore.Service
	Attachments *filestore.Attachments
	Sources     Sources
	// Live reads the sources; nil reads no remote tracker (ErrNoSources),
	// and a run starts from a record as it was last seen.
	Live *Live
	Log  *slog.Logger
}

func (f *Factory) log() *slog.Logger {
	if f.Log != nil {
		return f.Log
	}
	return slog.Default()
}

// Create writes a ticket on its owner's internal tracker (in.UserID: the
// workshop owner) and claims its uploads for the TICKET rather than any run.
// It lands in the tracker's first status and the type of its change type,
// unless given others (confstore validates them). A base branch not given
// stays empty: each run names it by its own workshop's branch format.
func (f *Factory) Create(ctx context.Context, in confstore.Ticket, attachmentIDs []string) (confstore.Ticket, error) {
	in.ChangeType = normalType(in.ChangeType)
	if in.ChangeType == "" {
		in.ChangeType = trackerport.ChangeFeat
	}
	t, err := f.Conf.CreateTicket(ctx, in)
	if err != nil {
		return confstore.Ticket{}, err
	}
	f.claim(ctx, t, attachmentIDs)
	return t, nil
}

// Update saves an edit and claims any newly-attached uploads (already-claimed
// ids are a no-op, so re-sending the full list is safe). A record takes only
// its base branch.
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

// Record is the record of a remote issue of sourceID, made now when there is
// none (created; automationID names the automation that made it, if one
// did), else rewritten with what issue shows of it — the automation that
// made it stays.
func (f *Factory) Record(ctx context.Context, sourceID string, issue trackerport.SourceTicket, automationID string) (confstore.Ticket, bool, error) {
	return f.Conf.RecordTicket(ctx, sourceID, seen(issue), automationID)
}

// Seen rewrites a record with what issue shows of it now: title, goal, key
// and URL (a moved key follows), type and change type, labels.
func (f *Factory) Seen(ctx context.Context, ticketID string, issue trackerport.SourceTicket) (confstore.Ticket, error) {
	return f.Conf.SeeTicket(ctx, ticketID, seen(issue))
}

// seen is what a record keeps of an issue. A type the adapter reports by
// name only is identified by its name.
func seen(issue trackerport.SourceTicket) confstore.TicketSeen {
	return confstore.TicketSeen{
		RemoteID: issue.RemoteID, Key: issue.Key, URL: issue.URL, Title: issue.Title, Goal: issue.Body,
		Type: issue.Type, TypeRemoteID: remoteIDOr(issue.TypeID, issue.Type), Labels: issue.Labels,
	}
}

// remoteIDOr is the tracker's id for a type, or — from an adapter that
// reports only the name — the name itself, which then stands for it.
func remoteIDOr(id, name string) string {
	if strings.TrimSpace(id) != "" {
		return id
	}
	return strings.TrimSpace(name)
}

// Search serves a source's list and the run dialog's picker: one page of its
// issues, each joined to its record.
func (f *Factory) Search(ctx context.Context, sourceID string, q trackerport.SearchQuery) (LivePage, error) {
	if f.Live == nil {
		return LivePage{}, ErrNoSources
	}
	return f.Live.List(ctx, sourceID, q, false)
}

// Fetch serves one issue with its attachment list.
func (f *Factory) Fetch(ctx context.Context, sourceID, key string) (trackerport.SourceTicket, error) {
	if f.Live == nil {
		return trackerport.SourceTicket{}, ErrNoSources
	}
	return f.Live.Issue(ctx, sourceID, key, false)
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

// copyIssueFiles gives a record the issue's files it does not hold yet
// (matched on the tracker's id for each, filestore.Attachment.RemoteID):
// each is copied into the owner's uploads and claimed for the ticket, so a
// run reads them without the tracker. A file with no remote id cannot be
// told from one already held and is not copied. A file the ticket got before
// remote ids were kept is recognised and given its id instead of a second
// copy (adoptable). One the issue no longer lists is KEPT: runs pin
// attachment ids at start. Best-effort: what fails is logged, and the next
// run start tries again.
func (f *Factory) copyIssueFiles(ctx context.Context, src trackerport.TicketSource, t confstore.Ticket, issue trackerport.SourceTicket) {
	if f.Attachments == nil || len(issue.Attachments) == 0 {
		return
	}
	log := f.log().With("ticket", t.ID, "key", issue.Key)
	stored, err := f.Attachments.ForTicket(ctx, t.ID)
	if err != nil {
		log.Warn("ticket files: list the held ones", "err", err)
		return
	}
	held := make(map[string]bool, len(stored))
	var legacy []filestore.Attachment
	for _, a := range stored {
		if a.RemoteID == "" {
			legacy = append(legacy, a)
		}
		held[a.RemoteID] = true
	}
	var fresh []trackerport.SourceAttachment
	names := map[string]int{}
	for _, a := range issue.Attachments {
		if a.ID != "" && !held[a.ID] {
			held[a.ID] = true // listed twice: once is enough
			fresh = append(fresh, a)
			names[filestore.SafeName(a.Filename)]++
		}
	}
	if len(fresh) == 0 {
		return
	}
	var download []trackerport.SourceAttachment
	used := map[string]bool{}
	adopted := 0
	for _, a := range fresh {
		id := adoptable(a, legacy, used, names)
		if id == "" {
			download = append(download, a)
			continue
		}
		ok, err := f.Attachments.AdoptRemote(ctx, t.ID, id, a.ID)
		switch {
		case err != nil:
			log.Warn("ticket files: adopt", "file", a.Filename, "err", err)
		case ok:
			used[id] = true
			adopted++
		default: // gone or adopted meanwhile: copy it as any new file
			download = append(download, a)
		}
	}
	if adopted > 0 {
		log.Info("ticket files: recognised files copied before remote ids", "count", adopted)
	}
	ids, skips := copyAttachments(ctx, f.Attachments, f.log(), t.UserID, src, issue.Key, download)
	for _, sk := range skips {
		if sk.refused() {
			log.Info("ticket files: refused by the upload policy", "warning", sk.warning())
		}
	}
	if len(ids) == 0 {
		return
	}
	if err := f.Attachments.ClaimForTicket(ctx, t.UserID, ids, t.ID); err != nil {
		log.Warn("ticket files: claim", "err", err)
		return
	}
	log.Info("ticket files: copied", "count", len(ids))
}

// adoptable picks the ticket's file without a remote id that is remote file
// a, copied before remote ids were kept — "" when none is, or when it can't
// be told. The stored name is the copy's (filestore.SafeName), compared
// case-sensitively. With a size from the tracker, the oldest unused file of
// that name and size is it; without one, the name must be unambiguous: one
// unused file of that name, and one new remote file of it (names counts
// them). A hand upload of the same name and size is taken for the copy: the
// same bytes, as far as can be told without downloading them.
func adoptable(a trackerport.SourceAttachment, legacy []filestore.Attachment, used map[string]bool, names map[string]int) string {
	name := filestore.SafeName(a.Filename)
	var match []string
	for _, x := range legacy {
		if !used[x.ID] && x.Filename == name && (a.Size <= 0 || x.Size == a.Size) {
			match = append(match, x.ID)
		}
	}
	switch {
	case len(match) == 0:
		return ""
	case a.Size > 0:
		return match[0]
	case len(match) == 1 && names[name] == 1:
		return match[0]
	}
	return ""
}

// copyAttachments downloads remote files (OpenAttachment) into owner's
// unclaimed uploads through the upload policy — size cap, MIME allowlist —
// each recording the tracker's id for it (SaveImported), and returns the ids
// that landed for the caller to claim. A file listed twice under one remote
// id is copied once.
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
				log.Warn("copy attachment", "file", a.Filename, "ticket", key, "err", err)
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
