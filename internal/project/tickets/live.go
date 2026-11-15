package tickets

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"apothecary/internal/platform/livecache"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

// Live reads a ticket source as it is now: a workshop's ticket list, a
// ticket page, the status and type lists, a prompt's comments, a run's start.
// A remote tracker is read through one in-memory cache (package livecache),
// one group per integration, so every workshop sharing a source shares its
// copies and its rate-limit hold. The internal tracker is read from the
// config store directly: it is local, and its writes are the rows themselves.
//
// A copy younger than its TTL is served as it is, an older one while one
// background read replaces it. A tracker that refuses for its rate limit
// (*trackerport.ErrRateLimited) holds its integration for the wait it asked
// (RateLimitBackoff when it did not say): copies are served at any age, and
// a read with no copy answers the error. A bypassing read (refresh, a run's
// start) that fails serves the copy held, when there is one.
//
// A single daemon is assumed: a second replica would hold its own copies,
// stale for up to a TTL after a write through the other.
type Live struct {
	conf    *confstore.Service
	sources Sources
	cache   *livecache.Cache
	log     *slog.Logger
	listTTL time.Duration
	itemTTL time.Duration

	kinds sync.Map // integration id → internal (bool): a kind never changes
}

// TTLs and the rate-limit backoff.
const (
	// DefaultListTTL is how long a list page is served without a reread.
	DefaultListTTL = time.Minute
	// DefaultItemTTL is how long an issue, its comments, a status or a type
	// list is served without a reread.
	DefaultItemTTL = 5 * time.Minute
	// RateLimitBackoff holds an integration whose tracker refused for its
	// rate limit without saying for how long.
	RateLimitBackoff = time.Minute
	// internalPageSize is an internal tracker page when none is asked.
	internalPageSize = 50
)

// LiveOption tunes NewLive.
type LiveOption func(*liveConfig)

type liveConfig struct {
	now              func() time.Time
	log              *slog.Logger
	listTTL, itemTTL time.Duration
}

// WithClock sets the cache's clock (tests).
func WithClock(now func() time.Time) LiveOption { return func(c *liveConfig) { c.now = now } }

// WithTTLs sets the list-page and item TTLs.
func WithTTLs(list, item time.Duration) LiveOption {
	return func(c *liveConfig) { c.listTTL, c.itemTTL = list, item }
}

// WithLog sets where served-stale reads are logged.
func WithLog(log *slog.Logger) LiveOption { return func(c *liveConfig) { c.log = log } }

// NewLive reads the sources' trackers, and conf for the internal tracker and
// the records joined to a page.
func NewLive(conf *confstore.Service, sources Sources, opts ...LiveOption) *Live {
	cfg := liveConfig{listTTL: DefaultListTTL, itemTTL: DefaultItemTTL, log: slog.Default()}
	for _, o := range opts {
		o(&cfg)
	}
	return &Live{
		conf: conf, sources: sources, log: cfg.log,
		listTTL: cfg.listTTL, itemTTL: cfg.itemTTL,
		cache: livecache.New(livecache.Config{Now: cfg.now, Hold: rateLimitHold}),
	}
}

// rateLimitHold is the cache's error classifier: a rate limit holds the
// integration for the wait the tracker asked.
func rateLimitHold(err error) (time.Duration, bool) {
	var rl *trackerport.ErrRateLimited
	if !errors.As(err, &rl) {
		return 0, false
	}
	if rl.RetryAfter > 0 {
		return rl.RetryAfter, true
	}
	return RateLimitBackoff, true
}

// LiveRow is one issue of a list, and its record when Apothecary has one.
type LiveRow struct {
	Issue trackerport.SourceTicket
	// Record is nil while nothing needed one (no run, no automation, no base
	// branch). On the internal tracker it is the ticket itself.
	Record *confstore.Ticket
}

// LivePage is one page of a source's list.
type LivePage struct {
	Rows []LiveRow
	// Next is the cursor of the following page; "" on the last one.
	Next string
}

// Cache keys, one namespace per kind of value.
const (
	keyList     = "list/"
	keyIssue    = "issue/"
	keyComments = "comments/"
	keyStatuses = "statuses"
	keyTypes    = "types"
)

func listKey(q trackerport.SearchQuery) string {
	return fmt.Sprintf("%s%s/%d/%q/%q", keyList, q.Done, q.Limit, q.Cursor, q.Text)
}

// Internal reports whether the source is an internal tracker.
func (l *Live) Internal(ctx context.Context, sourceID string) (bool, error) {
	if v, ok := l.kinds.Load(sourceID); ok {
		return v.(bool), nil
	}
	in, err := l.conf.DB().GetIntegration(ctx, sourceID)
	if err != nil {
		return false, fmt.Errorf("ticket source %s: %w", sourceID, err)
	}
	if in.Port != confstore.PortTicketSource {
		return false, fmt.Errorf("%w: integration %s is a %s integration, not a ticket source", confstore.ErrInvalid, in.Name, in.Port)
	}
	internal := in.Kind == confstore.KindInternal
	l.kinds.Store(sourceID, internal)
	return internal, nil
}

func (l *Live) source(ctx context.Context, sourceID string) (trackerport.TicketSource, error) {
	if l.sources == nil {
		return nil, ErrNoSources
	}
	return l.sources.TicketSource(ctx, sourceID)
}

// read serves key of the source's group: from the cache, or — bypass — read
// now, falling back to the copy held when the read fails.
func read[V any](ctx context.Context, l *Live, sourceID, key string, ttl time.Duration, bypass bool, load func(context.Context) (V, error)) (V, error) {
	if !bypass {
		return livecache.Get(ctx, l.cache, sourceID, key, ttl, load)
	}
	v, err := livecache.Bypass(ctx, l.cache, sourceID, key, load)
	if stale, ok := errors.AsType[*livecache.StaleError](err); ok {
		l.log.Warn("ticket source: serving a cached copy", "integration", sourceID, "key", key, "loaded", stale.Loaded, "err", stale.Err)
		return v, nil
	}
	return v, err
}

// List is one page of the source's issues matching q, each joined to its
// record. refresh rereads the page now.
func (l *Live) List(ctx context.Context, sourceID string, q trackerport.SearchQuery, refresh bool) (LivePage, error) {
	internal, err := l.Internal(ctx, sourceID)
	if err != nil {
		return LivePage{}, err
	}
	if internal {
		return l.internalList(ctx, sourceID, q)
	}
	page, err := read(ctx, l, sourceID, listKey(q), l.listTTL, refresh, func(ctx context.Context) (trackerport.SearchPage, error) {
		src, err := l.source(ctx, sourceID)
		if err != nil {
			return trackerport.SearchPage{}, err
		}
		return src.Search(ctx, q)
	})
	if err != nil {
		return LivePage{}, err
	}
	ids := make([]string, 0, len(page.Tickets))
	for _, t := range page.Tickets {
		if t.RemoteID != "" {
			ids = append(ids, t.RemoteID)
		}
	}
	records, err := l.conf.TicketsByRemote(ctx, sourceID, ids)
	if err != nil {
		return LivePage{}, err
	}
	out := LivePage{Rows: make([]LiveRow, len(page.Tickets)), Next: page.Next}
	for i, t := range page.Tickets {
		out.Rows[i].Issue = t
		if rec, ok := records[t.RemoteID]; ok && t.RemoteID != "" {
			out.Rows[i].Record = &rec
		}
	}
	return out, nil
}

// Issue is one issue by its key, with its attachments. bypass reads it now.
func (l *Live) Issue(ctx context.Context, sourceID, key string, bypass bool) (trackerport.SourceTicket, error) {
	return l.issue(ctx, sourceID, key, "", bypass)
}

// IssueOf is the issue a ticket stands for: the internal ticket itself, or a
// record's issue by its last-seen key — retried by its stable remote id when
// the tracker no longer has that key (a Jira project move, a GitHub
// transfer), so the moved issue is still found.
func (l *Live) IssueOf(ctx context.Context, t confstore.Ticket, bypass bool) (trackerport.SourceTicket, error) {
	return l.issue(ctx, t.SourceID, t.TrackerKey(), t.RemoteID, bypass)
}

func (l *Live) issue(ctx context.Context, sourceID, key, remoteID string, bypass bool) (trackerport.SourceTicket, error) {
	internal, err := l.Internal(ctx, sourceID)
	if err != nil {
		return trackerport.SourceTicket{}, err
	}
	if internal {
		return l.internalIssue(ctx, sourceID, key)
	}
	return read(ctx, l, sourceID, keyIssue+key, l.itemTTL, bypass, func(ctx context.Context) (trackerport.SourceTicket, error) {
		src, err := l.source(ctx, sourceID)
		if err != nil {
			return trackerport.SourceTicket{}, err
		}
		t, err := src.Fetch(ctx, key)
		if errors.Is(err, trackerport.ErrTicketNotFound) && remoteID != "" && !strings.EqualFold(remoteID, key) {
			t, err = src.Fetch(ctx, remoteID)
		}
		if err == nil && t.Key == "" {
			t.Key = key
		}
		return t, err
	})
}

// Comments is the issue's comments, oldest first; none from a tracker that
// does not list them. bypass reads them now.
func (l *Live) Comments(ctx context.Context, sourceID, key string, bypass bool) ([]trackerport.Comment, error) {
	internal, err := l.Internal(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if internal {
		list, err := l.conf.TicketComments(ctx, key)
		if err != nil {
			return nil, err
		}
		out := make([]trackerport.Comment, len(list))
		for i, c := range list {
			out[i] = trackerport.Comment{ID: c.ID, Author: c.Author, Body: c.Body, Created: c.CreatedAt, Updated: c.UpdatedAt}
		}
		return out, nil
	}
	return read(ctx, l, sourceID, keyComments+key, l.itemTTL, bypass, func(ctx context.Context) ([]trackerport.Comment, error) {
		src, err := l.source(ctx, sourceID)
		if err != nil {
			return nil, err
		}
		cl, ok := src.(trackerport.CommentLister)
		if !ok {
			return nil, nil
		}
		return cl.ListComments(ctx, key)
	})
}

// Statuses is the source's status list: the tracker's (none from one that
// does not list them), or the internal tracker's rows, Done in the done
// category and every other in the new one. refresh rereads it now.
func (l *Live) Statuses(ctx context.Context, sourceID string, refresh bool) ([]trackerport.RemoteStatus, error) {
	internal, err := l.Internal(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if internal {
		rows, err := l.conf.TrackerStatuses(ctx, sourceID)
		if err != nil {
			return nil, err
		}
		out := make([]trackerport.RemoteStatus, len(rows))
		for i, r := range rows {
			out[i] = trackerport.RemoteStatus{ID: r.ID, Name: r.Name, Category: trackerport.CategoryNew}
			if r.IsDone {
				out[i].Category = trackerport.CategoryDone
			}
		}
		return out, nil
	}
	return read(ctx, l, sourceID, keyStatuses, l.itemTTL, refresh, func(ctx context.Context) ([]trackerport.RemoteStatus, error) {
		src, err := l.source(ctx, sourceID)
		if err != nil {
			return nil, err
		}
		md, ok := src.(trackerport.TicketMetadata)
		if !ok {
			return nil, nil
		}
		return md.Statuses(ctx)
	})
}

// Types is the source's type list as stored, each with its change type. A
// remote tracker's types are read first, and one not listed yet is added
// with GuessChangeType's reading of its name (confstore.EnsureRemoteTypes);
// a tracker that does not list them keeps the rows its records added.
// refresh rereads the tracker's now.
func (l *Live) Types(ctx context.Context, sourceID string, refresh bool) ([]confstore.TrackerType, error) {
	internal, err := l.Internal(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if !internal {
		_, err := read(ctx, l, sourceID, keyTypes, l.itemTTL, refresh, func(ctx context.Context) ([]trackerport.RemoteType, error) {
			src, err := l.source(ctx, sourceID)
			if err != nil {
				return nil, err
			}
			md, ok := src.(trackerport.TicketMetadata)
			if !ok {
				return nil, nil
			}
			types, err := md.Types(ctx)
			if err != nil {
				return nil, err
			}
			if _, err := l.conf.EnsureRemoteTypes(ctx, sourceID, types); err != nil {
				return nil, err
			}
			return types, nil
		})
		if err != nil {
			return nil, err
		}
	}
	return l.conf.TrackerTypes(ctx, sourceID)
}

// Invalidate drops the source's copies of the issue at key — the issue, its
// comments — and every list page, which may show it: the next read rereads.
func (l *Live) Invalidate(sourceID, key string) {
	l.cache.Invalidate(sourceID, keyIssue+key)
	l.cache.Invalidate(sourceID, keyComments+key)
	l.cache.InvalidatePrefix(sourceID, keyList)
}

// Writer wraps the source's writer so that every write that lands drops
// the issue's copies (Invalidate): the next read shows the write. A writer
// that hosts attachments (trackerport.TicketAttacher) still does.
func (l *Live) Writer(sourceID string, w trackerport.TicketWriter) trackerport.TicketWriter {
	lw := liveWriter{TicketWriter: w, live: l, sourceID: sourceID}
	if a, ok := w.(trackerport.TicketAttacher); ok {
		return liveAttacher{liveWriter: lw, attacher: a}
	}
	return lw
}

type liveWriter struct {
	trackerport.TicketWriter
	live     *Live
	sourceID string
}

func (w liveWriter) wrote(key string, err error) error {
	if err == nil {
		w.live.Invalidate(w.sourceID, key)
	}
	return err
}

func (w liveWriter) Transition(ctx context.Context, key, status string) error {
	return w.wrote(key, w.TicketWriter.Transition(ctx, key, status))
}

func (w liveWriter) Comment(ctx context.Context, key, body string) error {
	return w.wrote(key, w.TicketWriter.Comment(ctx, key, body))
}

func (w liveWriter) SetFields(ctx context.Context, key string, fields map[string]string) error {
	return w.wrote(key, w.TicketWriter.SetFields(ctx, key, fields))
}

func (w liveWriter) AddLink(ctx context.Context, key string, link trackerport.TicketLink) error {
	return w.wrote(key, w.TicketWriter.AddLink(ctx, key, link))
}

type liveAttacher struct {
	liveWriter
	attacher trackerport.TicketAttacher
}

// Attach hosts the file on the issue (Jira lists it among the issue's
// attachments), so the issue's copy goes too.
func (w liveAttacher) Attach(ctx context.Context, key, name, contentType string, data []byte) (repoport.Attachment, error) {
	a, err := w.attacher.Attach(ctx, key, name, contentType, data)
	return a, w.wrote(key, err)
}

// --- the internal tracker ----------------------------------------------------

// internalList is a page of the internal tracker's tickets, newest first,
// after the keyset cursor q.Cursor.
func (l *Live) internalList(ctx context.Context, sourceID string, q trackerport.SearchQuery) (LivePage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = internalPageSize
	}
	iq := confstore.InternalTicketQuery{Text: q.Text, Done: q.Done, Limit: limit + 1}
	if q.Cursor != "" {
		after, err := decodeCursor(q.Cursor)
		if err != nil {
			return LivePage{}, err
		}
		iq.After = &after
	}
	list, err := l.conf.InternalTickets(ctx, sourceID, iq)
	if err != nil {
		return LivePage{}, err
	}
	var page LivePage
	if len(list) > limit {
		list = list[:limit]
		last := list[limit-1]
		page.Next = encodeCursor(confstore.TicketCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	lists, err := l.internalLists(ctx, sourceID)
	if err != nil {
		return LivePage{}, err
	}
	page.Rows = make([]LiveRow, len(list))
	for i := range list {
		page.Rows[i] = LiveRow{Issue: lists.issue(list[i]), Record: &list[i]}
	}
	return page, nil
}

func (l *Live) internalIssue(ctx context.Context, sourceID, key string) (trackerport.SourceTicket, error) {
	t, err := l.conf.GetTicket(ctx, key)
	if errors.Is(err, confstore.ErrNotFound) || (err == nil && t.SourceID != sourceID) {
		return trackerport.SourceTicket{}, fmt.Errorf("ticket %s: %w", key, trackerport.ErrTicketNotFound)
	}
	if err != nil {
		return trackerport.SourceTicket{}, err
	}
	lists, err := l.internalLists(ctx, sourceID)
	if err != nil {
		return trackerport.SourceTicket{}, err
	}
	return lists.issue(t), nil
}

// trackerLists is an internal tracker's statuses and types by id.
type trackerLists struct {
	statuses map[string]confstore.TrackerStatus
	types    map[string]confstore.TrackerType
}

func (l *Live) internalLists(ctx context.Context, sourceID string) (trackerLists, error) {
	sts, err := l.conf.TrackerStatuses(ctx, sourceID)
	if err != nil {
		return trackerLists{}, err
	}
	tys, err := l.conf.TrackerTypes(ctx, sourceID)
	if err != nil {
		return trackerLists{}, err
	}
	out := trackerLists{statuses: make(map[string]confstore.TrackerStatus, len(sts)), types: make(map[string]confstore.TrackerType, len(tys))}
	for _, s := range sts {
		out.statuses[s.ID] = s
	}
	for _, t := range tys {
		out.types[t.ID] = t
	}
	return out, nil
}

// issue is an internal ticket as a tracker would report it: its id is its
// key, its goal its body.
func (tl trackerLists) issue(t confstore.Ticket) trackerport.SourceTicket {
	st, ty := tl.statuses[t.StatusID], tl.types[t.TypeID]
	return trackerport.SourceTicket{
		RemoteID: t.ID, Key: t.ID, Title: t.Title, Body: t.Goal,
		Status: st.Name, StatusID: t.StatusID, Done: st.IsDone,
		Type: ty.Name, TypeID: t.TypeID, Labels: t.Labels, Updated: t.UpdatedAt,
	}
}

// encodeCursor makes an internal list position opaque to the caller.
func encodeCursor(c confstore.TicketCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.CreatedAt.UTC().Format(time.RFC3339Nano) + " " + c.ID))
}

func decodeCursor(s string) (confstore.TicketCursor, error) {
	bad := fmt.Errorf("%w: cursor %q is not one this list gave", confstore.ErrInvalid, s)
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return confstore.TicketCursor{}, bad
	}
	at, id, ok := strings.Cut(string(raw), " ")
	if !ok || id == "" {
		return confstore.TicketCursor{}, bad
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return confstore.TicketCursor{}, bad
	}
	return confstore.TicketCursor{CreatedAt: t, ID: id}, nil
}
