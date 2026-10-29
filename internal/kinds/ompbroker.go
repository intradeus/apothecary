package kinds

import (
	"context"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/providers"
)

// The omp auth-broker protocol, served by the engine (docs/model-providers-
// plan.md). omp in broker mode (OMP_AUTH_BROKER_URL / _TOKEN) keeps no login
// of its own: it reads a snapshot of access tokens and asks the broker to
// refresh one that expires. The engine is that broker, per run: the snapshot
// holds the run's harness integration's logins and nothing else, every
// refresh token is the protocol's sentinel, and a "refresh" is the keeper's
// single-flight Fresh — a workspace can never redeem anything itself.
//
// The wire shapes are omp 18.13.1's packages/ai/src/auth-broker/
// wire-schemas.ts, which are strict ("+": "reject") and unversioned: a field
// too many or too few and omp refuses the snapshot. TestOmpBrokerWire holds
// them; a bump of OmpVersion must re-check them.

// AuthBrokered is an OPTIONAL Harness capability: the harness reads its
// logins from the engine's auth broker rather than from a file.
type AuthBrokered interface {
	HasLogins() bool
	// Broker serves the protocol for one run. register is told every token
	// before it is served (the run's redactor).
	Broker(register func(string)) http.Handler
}

// remoteRefreshSentinel is omp's REMOTE_REFRESH_SENTINEL: what stands in a
// snapshot for a refresh token the broker keeps.
const remoteRefreshSentinel = "__remote__"

// brokerRefreshMs / brokerSkewMs describe the keeper's schedule to omp
// (its refresher block): it sweeps every minute, ahead of expiry.
const (
	brokerIntervalMs = 60_000
	brokerSkewMs     = 300_000
	// brokerFarFutureMs stands in for "never expires" (a GitHub token), as
	// omp itself does: its schema wants a number.
	brokerFarFuture = 10 * 365 * 24 * time.Hour
	// brokerMaxWait bounds a snapshot long-poll.
	brokerMaxWait = 25 * time.Second
)

// Broker serves the auth-broker protocol over this harness's logins.
func (h *ompHarness) Broker(register func(string)) http.Handler {
	return &ompBroker{creds: h.creds, register: register, now: time.Now}
}

type ompBroker struct {
	creds    Credentials
	register func(string)
	now      func() time.Time
}

var (
	brokerRefreshRoute = regexp.MustCompile(`^/v1/credential/(\d+)/refresh$`)
	brokerAckRoute     = regexp.MustCompile(`^/v1/credential/(\d+)/(disable|block|blocks)$`)
)

// brokerID is a login's numeric id on the wire: stable for the credential.
func brokerID(credentialID string) int64 {
	return int64(crc32.ChecksumIEEE([]byte(credentialID)) & 0x7fffffff)
}

type brokerCredential struct {
	Type          string `json:"type"`
	Refresh       string `json:"refresh"`
	Access        string `json:"access"`
	Expires       int64  `json:"expires"`
	AccountID     string `json:"accountId,omitempty"`
	Email         string `json:"email,omitempty"`
	ProjectID     string `json:"projectId,omitempty"`
	EnterpriseURL string `json:"enterpriseUrl,omitempty"`
	APIEndpoint   string `json:"apiEndpoint,omitempty"`
}

// brokerEntry is a snapshotEntrySchema row (strict: exactly these keys).
type brokerEntry struct {
	ID          int64            `json:"id"`
	Provider    string           `json:"provider"`
	Credential  brokerCredential `json:"credential"`
	IdentityKey *string          `json:"identityKey"`
	RotatesInMs *int64           `json:"rotatesInMs"`
}

// brokerRefreshEntry is a credentialSnapshotEntrySchema row: no rotatesInMs.
type brokerRefreshEntry struct {
	ID          int64            `json:"id"`
	Provider    string           `json:"provider"`
	Credential  brokerCredential `json:"credential"`
	IdentityKey *string          `json:"identityKey"`
}

type brokerRefresher struct {
	Enabled       bool  `json:"enabled"`
	IntervalMs    int64 `json:"intervalMs"`
	SkewMs        int64 `json:"skewMs"`
	NextSweepInMs int64 `json:"nextSweepInMs"`
}

type brokerSnapshot struct {
	Generation  int64           `json:"generation"`
	GeneratedAt int64           `json:"generatedAt"`
	ServerNowMs int64           `json:"serverNowMs"`
	Refresher   brokerRefresher `json:"refresher"`
	Credentials []brokerEntry   `json:"credentials"`
}

// entries renders the run's logins; each token is registered first.
func (b *ompBroker) entries(ctx context.Context) ([]brokerEntry, error) {
	creds, err := b.creds.current(ctx)
	if err != nil {
		return nil, err
	}
	now := b.now()
	var out []brokerEntry
	for _, bc := range bind(providers.Omp, creds) {
		if bc.Kind != providers.KindOAuth {
			continue // keys ride provider.env
		}
		for _, s := range bc.SecretValues() {
			b.register(s)
		}
		exp := bc.Expires
		if exp.IsZero() {
			exp = now.Add(brokerFarFuture)
		}
		var identity *string
		if a := bc.Get(providers.ExtraAccountID); a != "" {
			identity = &a
		} else if e := bc.Label(); e != "" {
			identity = &e
		}
		out = append(out, brokerEntry{
			ID: brokerID(bc.ID), Provider: bc.native.ID, IdentityKey: identity,
			Credential: brokerCredential{
				Type: "oauth", Refresh: remoteRefreshSentinel, Access: bc.Access, Expires: exp.UnixMilli(),
				AccountID: bc.Get(providers.ExtraAccountID), Email: bc.Label(), ProjectID: bc.Get(providers.ExtraProjectID),
				EnterpriseURL: bc.Get(providers.ExtraEnterprise), APIEndpoint: bc.Get(providers.ExtraAPIEndpoint),
			},
		})
	}
	return out, nil
}

// generation changes whenever a token does: the latest expiry, in seconds
// (a refresh always moves it forward), plus the entry count.
func generation(es []brokerEntry) int64 {
	var g int64
	for _, e := range es {
		if s := e.Credential.Expires / 1000; s > g {
			g = s
		}
	}
	return g + int64(len(es))
}

func (b *ompBroker) snapshot(ctx context.Context) (brokerSnapshot, error) {
	es, err := b.entries(ctx)
	if err != nil {
		return brokerSnapshot{}, err
	}
	if es == nil {
		es = []brokerEntry{}
	}
	now := b.now().UnixMilli()
	return brokerSnapshot{
		Generation: generation(es), GeneratedAt: now, ServerNowMs: now,
		Refresher:   brokerRefresher{Enabled: true, IntervalMs: brokerIntervalMs, SkewMs: brokerSkewMs, NextSweepInMs: brokerIntervalMs},
		Credentials: es,
	}, nil
}

func brokerJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// parseGeneration reads an ETag / If-None-Match ("N", W/"N" or N).
func parseGeneration(h string) (int64, bool) {
	h = strings.TrimSpace(h)
	h = strings.TrimSpace(strings.TrimPrefix(h, "W/"))
	h = strings.Trim(h, `"`)
	n, err := strconv.ParseInt(h, 10, 64)
	return n, err == nil && n >= 0
}

func (b *ompBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/v1/healthz":
		brokerJSON(w, 200, map[string]any{"ok": true, "version": "apothecary"})
	case r.Method == http.MethodGet && path == "/v1/snapshot":
		b.serveSnapshot(w, r)
	case r.Method == http.MethodGet && path == "/v1/snapshot/stream":
		// Not served: omp falls back to long-polling /v1/snapshot.
		brokerJSON(w, 404, map[string]string{"error": "stream not supported"})
	case r.Method == http.MethodPost && brokerRefreshRoute.MatchString(path):
		b.serveRefresh(w, r, brokerRefreshRoute.FindStringSubmatch(path)[1])
	case r.Method == http.MethodGet && path == "/v1/credentials/disabled":
		brokerJSON(w, 200, map[string]any{"generatedAt": b.now().UnixMilli(), "disabled": []any{}})
	case (r.Method == http.MethodPost || r.Method == http.MethodDelete) && brokerAckRoute.MatchString(path):
		// A workspace may not disable or block an engine credential; the
		// call is acknowledged so omp carries on, and nothing changes.
		brokerJSON(w, 200, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && (path == "/v1/usage/observed" || path == "/v1/usage/stale"):
		brokerJSON(w, 200, map[string]bool{"ok": true})
	case r.Method == http.MethodGet && path == "/v1/usage":
		brokerJSON(w, 200, map[string]any{"generatedAt": b.now().UnixMilli(), "reports": []any{}})
	case r.Method == http.MethodGet && path == "/v1/usage/history":
		brokerJSON(w, 200, map[string]any{"generatedAt": b.now().UnixMilli(), "entries": []any{}})
	case r.Method == http.MethodGet && path == "/v1/usage/clients":
		brokerJSON(w, 200, map[string]any{"generatedAt": b.now().UnixMilli(), "clients": []any{}})
	case r.Method == http.MethodPost && path == "/v1/credential":
		brokerJSON(w, 403, map[string]string{"error": "credentials are managed in apothecary (System → Model providers), not from a workspace"})
	default:
		brokerJSON(w, 404, map[string]string{"error": "not found"})
	}
}

func (b *ompBroker) serveSnapshot(w http.ResponseWriter, r *http.Request) {
	snap, err := b.snapshot(r.Context())
	if err != nil {
		brokerJSON(w, 503, map[string]string{"error": err.Error()})
		return
	}
	have, conditional := parseGeneration(r.Header.Get("If-None-Match"))
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	deadline := b.now().Add(min(time.Duration(wait)*time.Millisecond, brokerMaxWait))
	for conditional && have == snap.Generation && wait > 0 && b.now().Before(deadline) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
		}
		if snap, err = b.snapshot(r.Context()); err != nil {
			brokerJSON(w, 503, map[string]string{"error": err.Error()})
			return
		}
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(snap.Generation, 10)+`"`)
	if conditional && have == snap.Generation {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	brokerJSON(w, 200, snap)
}

func (b *ompBroker) serveRefresh(w http.ResponseWriter, r *http.Request, rawID string) {
	id, _ := strconv.ParseInt(rawID, 10, 64)
	// The keeper's Fresh renews the login if it is due and hands back the
	// current token otherwise; the workspace never chooses when to redeem.
	es, err := b.entries(r.Context())
	if err != nil {
		brokerJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	for _, e := range es {
		if e.ID == id {
			brokerJSON(w, 200, map[string]brokerRefreshEntry{"entry": {ID: e.ID, Provider: e.Provider, Credential: e.Credential, IdentityKey: e.IdentityKey}})
			return
		}
	}
	brokerJSON(w, 404, map[string]string{"error": "No credential with id=" + rawID})
}
