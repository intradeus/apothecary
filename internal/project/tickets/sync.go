package tickets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

// The sync (docs/ticket-sync-plan.md § Sync): a ticket with a source mirrors
// its remote issue, and the Syncer re-reads the issue and writes the mirror —
// title, body, status, type, labels (Mirror, confstore.MirrorTicket) and the
// comments (ImportComments). A ticket with no source lives on the internal
// tracker, which writes its row itself: nothing to sync.
//
// Triggers: the daemon's poll (RunOnce: every integration's syncable tickets
// — not in a Done status, not gone), a manual refresh of one ticket or of an
// integration's every ticket, done ones included (Refresh,
// RefreshIntegration), and the engine's refreshes after a ticket stage and at
// run start (Refresh).
//
// Outcomes, recorded on the ticket:
//   - a snapshot older than the one held (Updated before remote_updated_at)
//     is dropped — a lagging search index must not undo a fresher read;
//   - an issue the tracker does not have by its key (ErrTicketNotFound) is
//     fetched again by its stable remote id — a Jira move or a GitHub
//     transfer — and the mirror takes its new key and URL; with no remote id,
//     or not found by it either, the ticket is gone and polling stops;
//   - any other failure (a 401/403, a tracker that is down, an integration
//     that no longer resolves) is sync_state=error with the message, retried
//     at the next pass — the poll backs off from that integration for
//     ErrorBackoff first;
//   - a tracker that refuses for its rate limit (trackerport.ErrRateLimited)
//     records nothing on the tickets: the pass stops reading it, and the poll
//     leaves the integration alone for the RetryAfter it asked (ErrorBackoff
//     when it did not say).
//
// Attachments (Syncer.attachments): each pass copies the files the issue
// gained since the ticket's last sync, matched on the tracker's id for each
// (filestore.Attachment.RemoteID); a file imported before those ids were
// kept is recognised by name and size and given its id instead of a second
// copy (adoptable). One already held is skipped; one the issue no longer
// lists is KEPT, since runs pin attachment ids at start. A file that fails
// to download or store leaves the rest of the sync written — mirror and
// comments — and makes it a *PartialSyncError: sync_state=error naming the
// files, counted Failed, retried at the next pass. One the upload policy
// refuses is only logged, and retried too.
//
// One ticket is synced by one caller at a time (single-flight): a second
// Refresh while one is running waits for it and shares its outcome, and a
// pass skips a ticket another caller is syncing.
type Syncer struct {
	Conf    *confstore.Service
	Sources Sources
	// Attachments is the file store new remote files are copied into; nil
	// syncs no files (the import already warned it had nowhere to put them).
	Attachments *filestore.Attachments
	Log         *slog.Logger
	// ErrorBackoff is how long the poll leaves an integration alone after a
	// pass on it failed to reach its tracker; 0 = DefaultErrorBackoff.
	ErrorBackoff time.Duration
	// Now is the clock (nil = time.Now).
	Now func() time.Time
	// Notices hears whether the poll could read each integration's tracker;
	// nil = log only.
	Notices TrackerNotices

	mu       sync.Mutex
	inflight map[string]*flight
	heldTill map[string]time.Time // integration id → when the poll may read it again
}

// DefaultErrorBackoff is how long RunOnce skips an integration whose last
// pass could not read its tracker (bad credentials, tracker down): 15
// minutes, three poll periods, so a revoked token is not hammered every five.
// A manual refresh is never held back.
const DefaultErrorBackoff = 15 * time.Minute

// TrackerNotices hears whether the poll could read an integration's tracker (notices.Service); nil = log only.
type TrackerNotices interface {
	TrackerSync(ctx context.Context, userID, integrationID, name string, err error)
}

// SyncCounts is what a pass over an integration did.
type SyncCounts struct {
	// Tickets is how many tickets the pass covered.
	Tickets int `json:"tickets"`
	// Synced were re-read (a snapshot older than the mirror's is dropped,
	// and still counts: the mirror is current).
	Synced int `json:"synced"`
	// Gone no longer exist on the tracker (polling them stops).
	Gone int `json:"gone"`
	// Failed could not be synced (sync_error says why).
	Failed int `json:"failed"`
	// Busy were being synced by another caller and left to it.
	Busy int `json:"busy"`
	// RateLimited were not read: the tracker refused for its rate limit, and
	// the poll comes back after the wait it asked for.
	RateLimited int `json:"rate_limited"`
}

type flight struct {
	done chan struct{}
	err  error
}

func (s *Syncer) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Syncer) backoff() time.Duration {
	if s.ErrorBackoff > 0 {
		return s.ErrorBackoff
	}
	return DefaultErrorBackoff
}

// claim makes the caller the ticket's one syncer: ok=false hands back the
// running flight instead.
func (s *Syncer) claim(ticketID string) (*flight, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.inflight[ticketID]; ok {
		return f, false
	}
	if s.inflight == nil {
		s.inflight = map[string]*flight{}
	}
	f := &flight{done: make(chan struct{})}
	s.inflight[ticketID] = f
	return f, true
}

func (s *Syncer) release(ticketID string, f *flight, err error) {
	f.err = err
	s.mu.Lock()
	delete(s.inflight, ticketID)
	s.mu.Unlock()
	close(f.done)
}

// Refresh re-mirrors one ticket from its tracker, done or not, gone or not —
// a manual refresh and the engine's. A ticket with no source is a no-op. The
// outcome is also recorded on the ticket (sync_state, sync_error); an error
// is returned for a ticket that could not be synced (gone is not an error),
// a *PartialSyncError for one mirrored without every new file.
func (s *Syncer) Refresh(ctx context.Context, ticketID string) error {
	f, mine := s.claim(ticketID)
	if !mine {
		select {
		case <-f.done:
			return f.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var err error
	defer func() { s.release(ticketID, f, err) }()
	t, err := s.Conf.GetTicket(ctx, ticketID)
	if err != nil {
		return err
	}
	if t.SourceID == "" {
		return nil
	}
	src, err := s.Sources.TicketSource(ctx, t.SourceID)
	if err != nil {
		err = fmt.Errorf("ticket %s's integration %s: %w", t.ID, t.SourceID, err)
		s.record(ctx, t, confstore.SyncError, err)
		return err
	}
	remote, ferr := src.Fetch(ctx, t.SourceKey)
	_, err = s.settle(ctx, src, t, remote, ferr)
	s.holdIfRateLimited(t.SourceID, err)
	return err
}

// RefreshIntegration re-mirrors every ticket of a ticketsource integration,
// done ones included (a manual refresh), after refreshing its status and type
// lists from the tracker. An integration that does not resolve is an error.
func (s *Syncer) RefreshIntegration(ctx context.Context, integrationID string) (SyncCounts, error) {
	src, err := s.Sources.TicketSource(ctx, integrationID)
	if err != nil {
		return SyncCounts{}, err
	}
	if _, err := SyncLists(ctx, s.Conf, integrationID, src); err != nil {
		s.holdIfRateLimited(integrationID, err)
		return SyncCounts{}, fmt.Errorf("integration %s's lists: %w", integrationID, err)
	}
	list, err := s.Conf.ListTicketsBySource(ctx, integrationID)
	if err != nil {
		return SyncCounts{}, err
	}
	counts, _, limited := s.pass(ctx, src, list)
	if limited != nil {
		s.hold(integrationID, s.rateLimitWait(limited))
	}
	return counts, nil
}

// RunOnce is the poll: for every remote ticketsource integration, its
// syncable tickets (ListSyncable: not in a Done status, not gone). An
// integration whose last pass could not read its tracker is skipped for
// ErrorBackoff, one its tracker rate-limited for the wait asked; one that
// does not resolve marks its tickets in error and backs off too. Only a
// failure to list the integrations fails the pass.
func (s *Syncer) RunOnce(ctx context.Context) error {
	ins, err := s.Conf.ListIntegrations(ctx, "")
	if err != nil {
		return err
	}
	for _, in := range ins {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if in.Port != confstore.PortTicketSource || in.Kind == confstore.KindInternal || s.backingOff(in.ID) {
			continue
		}
		list, err := s.Conf.ListSyncable(ctx, in.ID)
		if err != nil {
			s.log().Error("ticket sync: list", "integration", in.ID, "err", err)
			continue
		}
		if len(list) == 0 {
			continue
		}
		var counts SyncCounts
		var failure error
		var limited *trackerport.ErrRateLimited
		if src, serr := s.Sources.TicketSource(ctx, in.ID); serr != nil {
			failure = fmt.Errorf("integration %s: %w", in.Name, serr)
			for _, t := range list {
				s.record(ctx, t, confstore.SyncError, failure)
			}
			counts.Tickets, counts.Failed = len(list), len(list)
		} else {
			counts, failure, limited = s.pass(ctx, src, list)
		}
		var wait time.Duration
		if failure != nil {
			wait = s.backoff()
		}
		if limited != nil {
			wait = max(wait, s.rateLimitWait(limited))
			s.log().Warn("ticket sync: rate limited", "integration", in.ID, "retry_after", wait, "err", limited)
		}
		s.mu.Lock()
		if s.heldTill == nil {
			s.heldTill = map[string]time.Time{}
		}
		if wait > 0 {
			s.heldTill[in.ID] = s.now().Add(wait)
		} else {
			delete(s.heldTill, in.ID)
		}
		s.mu.Unlock()
		// Only the poll tells notices: a pass that only met rate limits or busy
		// tickets says nothing either way.
		if s.Notices != nil {
			if failure != nil {
				s.Notices.TrackerSync(ctx, in.UserID, in.ID, in.Name, failure)
			} else if counts.Synced > 0 {
				s.Notices.TrackerSync(ctx, in.UserID, in.ID, in.Name, nil)
			}
		}
		if counts.Failed > 0 || counts.Gone > 0 {
			s.log().Warn("ticket sync", "integration", in.ID, "tickets", counts.Tickets,
				"synced", counts.Synced, "gone", counts.Gone, "failed", counts.Failed)
		}
	}
	return nil
}

func (s *Syncer) backingOff(integrationID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.heldTill[integrationID]
	return ok && s.now().Before(until)
}

// rateLimitWait is how long the poll leaves a rate-limited integration: the
// wait the tracker asked for, ErrorBackoff when it did not say.
func (s *Syncer) rateLimitWait(rl *trackerport.ErrRateLimited) time.Duration {
	if rl.RetryAfter > 0 {
		return rl.RetryAfter
	}
	return s.backoff()
}

// hold keeps the poll off an integration for at least d from now (a
// manual refresh that met a rate limit); a longer hold already set stays.
func (s *Syncer) hold(integrationID string, d time.Duration) {
	until := s.now().Add(d)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.heldTill == nil {
		s.heldTill = map[string]time.Time{}
	}
	if until.After(s.heldTill[integrationID]) {
		s.heldTill[integrationID] = until
	}
}

// holdIfRateLimited holds the integration when err is a rate limit.
func (s *Syncer) holdIfRateLimited(integrationID string, err error) {
	var rl *trackerport.ErrRateLimited
	if errors.As(err, &rl) {
		s.hold(integrationID, s.rateLimitWait(rl))
	}
}

// pass syncs a list of one integration's tickets: one FetchMany when the
// tracker has it (a key it leaves out is fetched alone, to tell a moved
// issue from a deleted one), else a Fetch per key. unreachable is the first
// failed read of the tracker (not a 404) — the poll's backoff signal; limited is
// the tracker's rate-limit refusal, after which the pass reads no more and
// the tickets left are counted RateLimited, with nothing recorded on them.
// A batch result carries the attachment metadata Fetch would (the
// TicketSyncer contract, checked for every adapter), so the files need no
// Fetch of their own.
func (s *Syncer) pass(ctx context.Context, src trackerport.TicketSource, list []confstore.Ticket) (counts SyncCounts, unreachable error, limited *trackerport.ErrRateLimited) {
	counts.Tickets = len(list)
	type claimed struct {
		t confstore.Ticket
		f *flight
	}
	var mine []claimed
	for _, t := range list {
		if f, ok := s.claim(t.ID); ok {
			mine = append(mine, claimed{t, f})
		} else {
			counts.Busy++
		}
	}
	var batch map[string]trackerport.SourceTicket
	var batchErr error
	if many, ok := src.(trackerport.TicketSyncer); ok && len(mine) > 0 {
		keys := make([]string, len(mine))
		for i, c := range mine {
			keys[i] = c.t.SourceKey
		}
		var got []trackerport.SourceTicket
		if got, batchErr = many.FetchMany(ctx, keys); batchErr == nil {
			batch = make(map[string]trackerport.SourceTicket, len(got))
			for _, r := range got {
				batch[strings.ToLower(r.Key)] = r
				if r.RemoteID != "" {
					batch["id:"+r.RemoteID] = r
				}
			}
		}
	}
	if batchErr != nil {
		errors.As(batchErr, &limited)
	}
	for _, c := range mine {
		if limited != nil {
			counts.RateLimited++
			s.release(c.t.ID, c.f, limited)
			continue
		}
		var remote trackerport.SourceTicket
		var ferr error
		switch {
		case batchErr != nil:
			ferr = batchErr
		case batch != nil:
			r, ok := batch[strings.ToLower(c.t.SourceKey)]
			if !ok && c.t.RemoteID != "" {
				r, ok = batch["id:"+c.t.RemoteID]
			}
			if ok {
				remote = r
			} else {
				remote, ferr = src.Fetch(ctx, c.t.SourceKey)
			}
		default:
			remote, ferr = src.Fetch(ctx, c.t.SourceKey)
		}
		state, err := s.settle(ctx, src, c.t, remote, ferr)
		s.release(c.t.ID, c.f, err)
		switch {
		case state == stateRateLimited:
			counts.RateLimited++
			errors.As(err, &limited)
		case state == confstore.SyncGone:
			counts.Gone++
		case err != nil:
			counts.Failed++
			if state == stateUnreachable && unreachable == nil {
				unreachable = err
			}
		default:
			counts.Synced++
		}
	}
	return counts, unreachable, limited
}

// stateUnreachable is settle's word for a failed read of the tracker (as
// opposed to a mirror it could not write), stateRateLimited for a read the
// tracker refused for its rate limit (recorded nowhere: the ticket keeps its
// state until the next pass reads it).
const (
	stateUnreachable = "unreachable"
	stateRateLimited = "rate_limited"
)

// settle writes one fetch's outcome onto the ticket (see Syncer). state is
// confstore.SyncOK, SyncGone, stateUnreachable (the tracker read failed),
// stateRateLimited (the tracker refused for its rate limit: nothing is
// recorded) or SyncError (the read worked, the mirror did not).
func (s *Syncer) settle(ctx context.Context, src trackerport.TicketSource, t confstore.Ticket, remote trackerport.SourceTicket, ferr error) (state string, err error) {
	state, err = s.mirror(ctx, src, t, remote, ferr)
	var rl *trackerport.ErrRateLimited
	if errors.As(err, &rl) {
		return stateRateLimited, err
	}
	if err != nil {
		s.record(ctx, t, confstore.SyncError, err)
	}
	return state, err
}

// mirror is settle's work, short of recording a failure on the ticket.
func (s *Syncer) mirror(ctx context.Context, src trackerport.TicketSource, t confstore.Ticket, remote trackerport.SourceTicket, ferr error) (state string, err error) {
	if errors.Is(ferr, trackerport.ErrTicketNotFound) && t.RemoteID != "" && !strings.EqualFold(t.RemoteID, t.SourceKey) {
		// Moved (a Jira project move, a GitHub transfer): the stable id
		// still finds it, and the mirror takes the new key and URL.
		remote, ferr = src.Fetch(ctx, t.RemoteID)
	}
	if errors.Is(ferr, trackerport.ErrTicketNotFound) {
		s.record(ctx, t, confstore.SyncGone, ferr)
		return confstore.SyncGone, nil
	}
	if ferr != nil {
		return stateUnreachable, fmt.Errorf("fetch %s: %w", t.SourceKey, ferr)
	}
	if remote.Key == "" {
		remote.Key = t.SourceKey
	}
	m, _, err := Mirror(ctx, s.Conf, t.SourceID, src, remote)
	if err != nil {
		return confstore.SyncError, err
	}
	applied, err := s.Conf.MirrorTicket(ctx, t.ID, m)
	if err != nil {
		return confstore.SyncError, err
	}
	if !applied {
		s.log().Info("ticket sync: dropped a snapshot older than the mirror's", "ticket", t.ID, "key", remote.Key,
			"snapshot", remote.Updated, "held", t.RemoteUpdatedAt)
	}
	if err := ImportComments(ctx, s.Conf, t.ID, src, remote.Key); err != nil {
		return stateUnreachable, fmt.Errorf("comments of %s: %w", remote.Key, err)
	}
	if err := s.attachments(ctx, src, t, remote); err != nil {
		// The mirror and the comments stand: only files are missing, which
		// settle records and a pass counts Failed.
		return confstore.SyncError, err
	}
	return confstore.SyncOK, nil
}

// PartialSyncError is a sync that wrote the mirror and the comments but not
// every new file: the ticket is sync_state=error naming them, a pass counts
// it Failed, Refresh returns it (the API still answers with the ticket), and
// the next sync retries the files, their ids still not held.
type PartialSyncError struct {
	Key string
	// Failures says, per file (or for the claim or the listing), what failed.
	Failures []string
}

func (e *PartialSyncError) Error() string {
	return fmt.Sprintf("attachments of %s: %s (the next sync retries)", e.Key, strings.Join(e.Failures, "; "))
}

// attachments copies the remote's files the ticket does not hold yet (by
// remote id) and claims them for it. A file with no remote id cannot be told
// from one already held, so only the import copies it. Before downloading,
// a file imported before remote ids were kept is recognised and given its id
// instead (adoptable). A refusal by the upload policy is logged, not
// recorded: the import already warned of it, and a policy change lets the
// next pass take it. Anything else that fails is a *PartialSyncError naming
// each file; the others still land.
func (s *Syncer) attachments(ctx context.Context, src trackerport.TicketSource, t confstore.Ticket, remote trackerport.SourceTicket) error {
	if s.Attachments == nil || len(remote.Attachments) == 0 {
		return nil
	}
	stored, err := s.Attachments.ForTicket(ctx, t.ID)
	if err != nil {
		return &PartialSyncError{Key: remote.Key, Failures: []string{fmt.Sprintf("listing the held files: %v", err)}}
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
	for _, a := range remote.Attachments {
		if a.ID != "" && !held[a.ID] {
			held[a.ID] = true // listed twice: once is enough
			fresh = append(fresh, a)
			names[filestore.SafeName(a.Filename)]++
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	var failed []string
	var download []trackerport.SourceAttachment
	used := map[string]bool{}
	adopted := 0
	for _, a := range fresh {
		id := adoptable(a, legacy, used, names)
		if id == "" {
			download = append(download, a)
			continue
		}
		ok, err := s.Attachments.AdoptRemote(ctx, t.ID, id, a.ID)
		switch {
		case err != nil:
			failed = append(failed, fmt.Sprintf("%s: %v", a.Filename, err))
		case ok:
			used[id] = true
			adopted++
		default: // gone or adopted meanwhile: copy it as any new file
			download = append(download, a)
		}
	}
	if adopted > 0 {
		s.log().Info("ticket sync: recognised attachments imported before remote ids", "ticket", t.ID, "key", remote.Key, "count", adopted)
	}
	ids, skips := copyAttachments(ctx, s.Attachments, s.log(), t.UserID, src, remote.Key, download)
	for _, sk := range skips {
		if sk.refused() {
			s.log().Info("ticket sync: attachment refused by the upload policy", "ticket", t.ID, "key", remote.Key, "file", sk.file, "err", sk.err)
			continue
		}
		failed = append(failed, sk.warning())
	}
	if len(ids) > 0 {
		if err := s.Attachments.ClaimForTicket(ctx, t.UserID, ids, t.ID); err != nil {
			failed = append(failed, fmt.Sprintf("claim: %v", err))
		} else {
			s.log().Info("ticket sync: copied new attachments", "ticket", t.ID, "key", remote.Key, "count", len(ids))
		}
	}
	if len(failed) > 0 {
		return &PartialSyncError{Key: remote.Key, Failures: failed}
	}
	return nil
}

// adoptable picks the ticket's file without a remote id that is remote file
// a, imported before remote ids were kept — "" when none is, or when it can't
// be told. The stored name is the import's (filestore.SafeName), compared
// case-sensitively. With a size from the tracker, the oldest unused file of
// that name and size is it; without one, the name must be unambiguous: one
// unused file of that name, and one new remote file of it (names counts
// them). A hand upload of the same name and size is taken for the import's
// copy: the same bytes, as far as can be told without downloading them.
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

// record writes a sync's failure (or gone) onto the ticket; a ticket deleted
// meanwhile has nothing to record it on.
func (s *Syncer) record(ctx context.Context, t confstore.Ticket, state string, cause error) {
	if err := s.Conf.SetTicketSyncState(ctx, t.ID, state, cause.Error()); err != nil && !errors.Is(err, confstore.ErrNotFound) {
		s.log().Error("ticket sync: record state", "ticket", t.ID, "state", state, "err", err)
	}
}
