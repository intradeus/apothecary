package tickets

// The mirror: how a remote tracker's snapshot of an issue becomes the
// internal ticket's columns. One place for the import (CreateFromSource) and
// the sync, so both read a tracker the same way:
//
//   - status and type are resolved by the tracker's own ids against the
//     integration's lists (confstore TrackerStatus / TrackerType), so a
//     renamed status still matches;
//   - one the lists lack (a Jira workflow edited since the last fetch — plan
//     G7) is added to them: from the tracker's metadata when it serves it
//     (TicketMetadata, which also says each status's category), else from the
//     snapshot alone — either way auto-suggested by confstore
//     (EnsureRemoteStatuses / EnsureRemoteTypes): a done category is Done and
//     the done step, a new one the todo step, each step only while free; a
//     type's change type is GuessChangeType's reading of its name;
//   - the change type is the type's, as the user mapped it.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

// SyncLists refreshes an integration's status and type lists from its
// tracker's metadata: new ones are added (auto-suggested), listed ones take
// the tracker's current names, and the user's columns are kept. A tracker
// without the capability is left as it is — its lists grow from the tickets
// it returns (Mirror). The first refresh of a Jira integration that still
// has the legacy status_map / type_map applies them and drops them
// (legacymaps.go); what of them could not be applied is the warnings.
func SyncLists(ctx context.Context, conf *confstore.Service, integrationID string, src trackerport.TicketSource) ([]string, error) {
	md, ok := src.(trackerport.TicketMetadata)
	if !ok {
		return nil, nil
	}
	statuses, err := md.Statuses(ctx)
	if err != nil {
		return nil, fmt.Errorf("statuses: %w", err)
	}
	if _, err := conf.EnsureRemoteStatuses(ctx, integrationID, statuses); err != nil {
		return nil, err
	}
	types, err := md.Types(ctx)
	if err != nil {
		return nil, fmt.Errorf("types: %w", err)
	}
	if _, err := conf.EnsureRemoteTypes(ctx, integrationID, types); err != nil {
		return nil, err
	}
	warnings, err := applyLegacyMaps(ctx, conf, integrationID)
	if err != nil {
		return nil, fmt.Errorf("legacy status/type maps: %w", err)
	}
	for _, w := range warnings {
		slog.Warn("ticket tracker lists: legacy map", "integration", integrationID, "warning", w)
	}
	return warnings, nil
}

// Mirror maps a tracker's snapshot of one issue onto what the internal
// ticket mirrors (confstore.TicketMirror, written by MirrorTicket or the
// import's CreateTicket), and the change type its type stands for. A status
// or type the integration does not list yet is added first (see above); a
// snapshot that names neither leaves the mirror's as it is.
func Mirror(ctx context.Context, conf *confstore.Service, integrationID string, src trackerport.TicketSource, remote trackerport.SourceTicket) (confstore.TicketMirror, trackerport.ChangeType, error) {
	m := confstore.TicketMirror{
		Title: remote.Title, Goal: remote.Body,
		SourceKey: remote.Key, SourceURL: remote.URL, SourceType: remote.Type,
		RemoteID: remote.RemoteID, Labels: remote.Labels, RemoteUpdated: remote.Updated,
	}
	l := lists{conf: conf, id: integrationID, src: src}
	st, err := l.status(ctx, remote)
	if err != nil {
		return confstore.TicketMirror{}, "", fmt.Errorf("status of %s: %w", remote.Key, err)
	}
	m.StatusID = st.ID
	ty, err := l.typ(ctx, remote)
	if err != nil {
		return confstore.TicketMirror{}, "", fmt.Errorf("type of %s: %w", remote.Key, err)
	}
	m.TypeID = ty.ID
	ct := ty.ChangeType
	if ct == "" {
		ct = trackerport.GuessChangeType(remote.Type)
	}
	return m, ct, nil
}

// lists resolves against one integration's lists, refreshing them from the
// tracker's metadata at most once.
type lists struct {
	conf      *confstore.Service
	id        string
	src       trackerport.TicketSource
	refreshed bool
}

func (l *lists) refresh(ctx context.Context) (bool, error) {
	if l.refreshed {
		return false, nil
	}
	l.refreshed = true
	if _, ok := l.src.(trackerport.TicketMetadata); !ok {
		return false, nil
	}
	_, err := SyncLists(ctx, l.conf, l.id, l.src)
	return true, err
}

// remoteIDOr is the tracker's id for a status or type, or — from an adapter
// that reports only the name — the name itself, which then stands for it.
func remoteIDOr(id, name string) string {
	if strings.TrimSpace(id) != "" {
		return id
	}
	return strings.TrimSpace(name)
}

func (l *lists) status(ctx context.Context, remote trackerport.SourceTicket) (confstore.TrackerStatus, error) {
	rid := remoteIDOr(remote.StatusID, remote.Status)
	if rid == "" {
		return confstore.TrackerStatus{}, nil
	}
	find := func() (confstore.TrackerStatus, bool, error) {
		list, err := l.conf.TrackerStatuses(ctx, l.id)
		for _, s := range list {
			if s.RemoteID == rid {
				return s, true, err
			}
		}
		return confstore.TrackerStatus{}, false, err
	}
	s, ok, err := find()
	if err != nil || ok {
		return s, err
	}
	if again, err := l.refresh(ctx); err != nil {
		return s, err
	} else if again {
		if s, ok, err = find(); err != nil || ok {
			return s, err
		}
	}
	name := remote.Status
	if name == "" {
		name = rid
	}
	list, err := l.conf.EnsureRemoteStatuses(ctx, l.id, []trackerport.RemoteStatus{{ID: rid, Name: name}})
	for _, s := range list {
		if s.RemoteID == rid {
			return s, err
		}
	}
	return confstore.TrackerStatus{}, err
}

func (l *lists) typ(ctx context.Context, remote trackerport.SourceTicket) (confstore.TrackerType, error) {
	rid := remoteIDOr(remote.TypeID, remote.Type)
	if rid == "" {
		return confstore.TrackerType{}, nil
	}
	find := func() (confstore.TrackerType, bool, error) {
		list, err := l.conf.TrackerTypes(ctx, l.id)
		for _, t := range list {
			if t.RemoteID == rid {
				return t, true, err
			}
		}
		return confstore.TrackerType{}, false, err
	}
	t, ok, err := find()
	if err != nil || ok {
		return t, err
	}
	if again, err := l.refresh(ctx); err != nil {
		return t, err
	} else if again {
		if t, ok, err = find(); err != nil || ok {
			return t, err
		}
	}
	name := remote.Type
	if name == "" {
		name = rid
	}
	list, err := l.conf.EnsureRemoteTypes(ctx, l.id, []trackerport.RemoteType{{ID: rid, Name: name}})
	for _, t := range list {
		if t.RemoteID == rid {
			return t, err
		}
	}
	return confstore.TrackerType{}, err
}

// ImportComments mirrors the issue's comments onto the ticket (insert, update,
// and drop the ones the tracker no longer has), when the tracker lists them.
func ImportComments(ctx context.Context, conf *confstore.Service, ticketID string, src trackerport.TicketSource, key string) error {
	cl, ok := src.(trackerport.CommentLister)
	if !ok {
		return nil
	}
	list, err := cl.ListComments(ctx, key)
	if err != nil {
		return err
	}
	return conf.ReconcileRemoteComments(ctx, ticketID, list)
}
