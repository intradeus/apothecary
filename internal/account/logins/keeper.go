package logins

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"apothecary/internal/account/providers"
	"apothecary/internal/platform/telemetry"
	"apothecary/internal/project/confstore"
)

// Store is what the keeper reads and writes credentials through
// (confstore.Service).
type Store interface {
	ListCredentials(ctx context.Context, userID string) ([]confstore.ProviderCredential, error)
	OpenCredential(ctx context.Context, id string) (providers.Credential, confstore.ProviderCredential, error)
	SaveRefreshed(ctx context.Context, c providers.Credential) (confstore.ProviderCredential, error)
	DisableCredential(ctx context.Context, id, reason string) error
}

// Defaults: refresh a login when its access token has under Margin left, and
// a ChatGPT login whose token carries no expiry once it is StaleAfter old —
// well inside codex's own eight days, so no workspace ever decides to refresh
// it itself. Every sweeps the store.
const (
	DefaultMargin     = 30 * time.Minute
	DefaultStaleAfter = 5 * 24 * time.Hour
	DefaultEvery      = time.Minute
)

// Keeper is the one party that redeems refresh tokens.
type Keeper struct {
	store  Store
	client *http.Client
	log    *slog.Logger
	now    func() time.Time

	Margin, StaleAfter time.Duration
	// OnRefresh is told every credential the keeper refreshed, after it is
	// persisted: the engine rewrites the live workspaces that hold it.
	OnRefresh func(ctx context.Context, credentialID string)

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New builds a keeper over store.
func New(store Store, log *slog.Logger) *Keeper {
	if log == nil {
		log = slog.Default()
	}
	return &Keeper{
		store: store, client: &http.Client{Transport: telemetry.HTTPTransport(http.DefaultTransport)}, log: log, now: time.Now,
		Margin: DefaultMargin, StaleAfter: DefaultStaleAfter,
		locks: map[string]*sync.Mutex{},
	}
}

func (k *Keeper) lock(id string) func() {
	k.mu.Lock()
	l, ok := k.locks[id]
	if !ok {
		l = &sync.Mutex{}
		k.locks[id] = l
	}
	k.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// due reports whether a login needs refreshing now.
func (k *Keeper) due(c providers.Credential, meta confstore.ProviderCredential) bool {
	if c.Kind != providers.KindOAuth {
		return false
	}
	p, _ := providers.Get(c.Provider)
	if p.Flow == providers.FlowCopilot {
		return false // the GitHub token is the bearer, and does not expire
	}
	now := k.now()
	if !c.Expires.IsZero() {
		return c.Expires.Sub(now) < k.Margin
	}
	if p.Flow == providers.FlowChatGPT {
		return meta.Meta.LastRefresh.IsZero() || now.Sub(meta.Meta.LastRefresh) > k.StaleAfter
	}
	return false
}

// DisabledError is a credential the keeper gave up on: its owner must sign in
// again.
type DisabledError struct {
	Name, Reason string
}

func (e *DisabledError) Error() string {
	return fmt.Sprintf("credential %q is disabled: %s", e.Name, e.Reason)
}

// Fresh returns the credential with an access token that will outlive the
// margin, refreshing it first when it would not. What a harness renders into
// a workspace is always read through here. Single-flight: a second caller
// for the same credential waits and gets the first's result rather than
// redeeming the (now spent) refresh token again.
func (k *Keeper) Fresh(ctx context.Context, id string) (providers.Credential, error) {
	c, meta, err := k.store.OpenCredential(ctx, id)
	if err != nil {
		return c, err
	}
	if meta.Meta.Status == confstore.CredentialDisabled {
		return c, &DisabledError{Name: meta.Name, Reason: meta.Meta.Error}
	}
	if !k.due(c, meta) {
		return c, nil
	}
	unlock := k.lock(id)
	defer unlock()
	// Re-read under the lock: whoever held it may have just refreshed.
	c, meta, err = k.store.OpenCredential(ctx, id)
	if err != nil {
		return c, err
	}
	if meta.Meta.Status == confstore.CredentialDisabled {
		return c, &DisabledError{Name: meta.Name, Reason: meta.Meta.Error}
	}
	if !k.due(c, meta) {
		return c, nil
	}
	refreshed, err := Refresh(ctx, k.client, c, k.now())
	if err != nil {
		if IsPermanent(err) {
			k.log.Warn("login refresh refused for good; disabling the credential",
				"credential", id, "provider", c.Provider, "reason", err.Error())
			if derr := k.store.DisableCredential(ctx, id, err.Error()); derr != nil {
				k.log.Error("could not record a dead login", "credential", id, "err", derr)
			}
			return c, &DisabledError{Name: meta.Name, Reason: err.Error()}
		}
		// Transient: the current token still works until it expires, so a
		// caller with time left proceeds on it.
		k.log.Warn("login refresh failed; will retry", "credential", id, "provider", c.Provider, "err", err)
		if !c.Expires.IsZero() && c.Expires.After(k.now()) {
			return c, nil
		}
		return c, fmt.Errorf("credential %q: refresh failed and the token has expired: %w", meta.Name, err)
	}
	refreshed.ID = id
	if _, err := k.store.SaveRefreshed(ctx, refreshed); err != nil {
		// The provider spent the old refresh token; failing to keep the new
		// one loses the login. Say so loudly.
		k.log.Error("refreshed a login but could not store it; it will need a new sign-in",
			"credential", id, "provider", c.Provider, "err", err)
		return c, fmt.Errorf("credential %q: could not store the refreshed login: %w", meta.Name, err)
	}
	k.log.Info("login refreshed", "credential", id, "provider", c.Provider, "expires", refreshed.Expires)
	if k.OnRefresh != nil {
		k.OnRefresh(ctx, id)
	}
	return refreshed, nil
}

// Sweep refreshes every login that is due, across all users.
func (k *Keeper) Sweep(ctx context.Context) {
	all, err := k.store.ListCredentials(ctx, "")
	if err != nil {
		k.log.Warn("login sweep: cannot list credentials", "err", err)
		return
	}
	for _, pc := range all {
		if pc.Kind != providers.KindOAuth || pc.Meta.Status == confstore.CredentialDisabled {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if _, err := k.Fresh(ctx, pc.ID); err != nil {
			k.log.Warn("login sweep", "credential", pc.ID, "err", err)
		}
	}
}

// Run sweeps every interval until ctx ends — once immediately, so a login
// that went stale while the engine was down is renewed first.
func (k *Keeper) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = DefaultEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		k.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
