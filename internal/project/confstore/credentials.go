package confstore

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"apothecary/internal/account/providers"
	"apothecary/internal/platform/secrets"
)

// Provider credentials (docs/model-providers-plan.md): a user's API keys and
// logins, one row each, referenced by their harness integrations. The secret
// half is sealed like an integration's; the API sees metadata only, and the
// only paths that open one are the keeper (to refresh it) and a harness (to
// render it into a workspace) — both engine-side.

// Credential statuses.
const (
	CredentialOK       = "ok"
	CredentialDisabled = "disabled" // a refresh failed for good: sign in again
)

// CredentialMeta is what is shown about a credential without opening it.
type CredentialMeta struct {
	// Account names the signed-in account (a login's email), when known.
	Account string `json:"account,omitempty"`
	// ExpiresAt is the access token's expiry (a login; zero = never).
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	// LastRefresh is when the keeper last refreshed the login.
	LastRefresh time.Time `json:"last_refresh,omitzero"`
	// Status is ok or disabled; Error says why a refresh failed.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// ProviderCredential is one credential as the API shows it.
type ProviderCredential struct {
	ID        string         `json:"id"`
	UserID    string         `json:"user_id"`
	Provider  string         `json:"provider"`
	Kind      providers.Kind `json:"kind"`
	Name      string         `json:"name"`
	Meta      CredentialMeta `json:"meta"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	// Sealed is the secret half, at rest; never filled past the Service.
	Sealed []byte `json:"-"`
}

// CredentialRefIDs reads a harness integration's credential references.
func CredentialRefIDs(cfg map[string]any) []string {
	raw, _ := cfg[ConfigCredentialIDs].([]any)
	var out []string
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	if ids, ok := cfg[ConfigCredentialIDs].([]string); ok {
		out = append(out, ids...)
	}
	return out
}

// ConfigCredentialIDs is the harness integration config key holding the
// ids of the provider credentials it uses.
const ConfigCredentialIDs = "credential_ids"

// CreateCredential seals and stores a new credential of the user's. The
// provider must be one of the table, and take the credential's kind.
func (s *Service) CreateCredential(ctx context.Context, userID string, c providers.Credential, name string) (ProviderCredential, error) {
	p, ok := providers.Get(c.Provider)
	if !ok {
		return ProviderCredential{}, invalidf("credential: provider %q is not one apothecary manages", c.Provider)
	}
	switch c.Kind {
	case providers.KindAPIKey:
		if !p.Key {
			return ProviderCredential{}, invalidf("credential: %s takes no API key — sign in instead", p.Name)
		}
		if strings.TrimSpace(c.Key) == "" {
			return ProviderCredential{}, invalidf("credential: the API key is required")
		}
		if strings.HasPrefix(c.Key, maskPrefix) {
			return ProviderCredential{}, invalidf("credential: the key is a masked placeholder — send the real value")
		}
	case providers.KindOAuth:
		if p.Flow == "" {
			return ProviderCredential{}, invalidf("credential: %s has no login apothecary can keep — use an API key", p.Name)
		}
		if c.Access == "" || c.Refresh == "" {
			return ProviderCredential{}, invalidf("credential: a login needs an access and a refresh token")
		}
	default:
		return ProviderCredential{}, invalidf("credential: kind %q is not api_key or oauth", c.Kind)
	}
	if strings.TrimSpace(name) == "" {
		name = p.Name
		if l := c.Label(); l != "" {
			name += " · " + l
		}
	}
	now := s.now().UTC()
	pc := ProviderCredential{
		ID: s.newID(), UserID: userID, Provider: p.ID, Kind: c.Kind, Name: strings.TrimSpace(name),
		Meta:      CredentialMeta{Account: c.Label(), ExpiresAt: c.Expires, Status: CredentialOK},
		CreatedAt: now, UpdatedAt: now,
	}
	if c.Kind == providers.KindOAuth {
		pc.Meta.LastRefresh = now
	}
	sealed, err := secrets.Seal(s.key, c.Secrets())
	if err != nil {
		return ProviderCredential{}, err
	}
	pc.Sealed = sealed
	if err := s.db.UpsertCredential(ctx, pc); err != nil {
		return ProviderCredential{}, err
	}
	pc.Sealed = nil
	return pc, nil
}

// GetCredential is one credential's metadata.
func (s *Service) GetCredential(ctx context.Context, id string) (ProviderCredential, error) {
	pc, err := s.db.GetCredential(ctx, id)
	pc.Sealed = nil
	return pc, err
}

// ListCredentials is one user's credentials ("" = everyone's: the keeper's
// sweep), by name.
func (s *Service) ListCredentials(ctx context.Context, userID string) ([]ProviderCredential, error) {
	rows, err := s.db.ListCredentials(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Sealed = nil
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// OpenCredential returns a credential with its secrets: engine-side only.
func (s *Service) OpenCredential(ctx context.Context, id string) (providers.Credential, ProviderCredential, error) {
	pc, err := s.db.GetCredential(ctx, id)
	if err != nil {
		return providers.Credential{}, ProviderCredential{}, err
	}
	m, err := secrets.Open(s.key, pc.Sealed)
	if err != nil {
		return providers.Credential{}, ProviderCredential{}, fmt.Errorf("credential %s: %w", id, err)
	}
	c := providers.Credential{ID: pc.ID, Provider: pc.Provider, Kind: pc.Kind, Expires: pc.Meta.ExpiresAt}
	providers.FromSecrets(&c, m)
	pc.Sealed = nil
	return c, pc, nil
}

// SaveRefreshed stores what a refresh returned — the new access token, the
// rotated refresh token, the new expiry — and clears any earlier failure.
func (s *Service) SaveRefreshed(ctx context.Context, c providers.Credential) (ProviderCredential, error) {
	pc, err := s.db.GetCredential(ctx, c.ID)
	if err != nil {
		return ProviderCredential{}, err
	}
	sealed, err := secrets.Seal(s.key, c.Secrets())
	if err != nil {
		return ProviderCredential{}, err
	}
	now := s.now().UTC()
	pc.Sealed = sealed
	pc.Meta.ExpiresAt = c.Expires
	pc.Meta.LastRefresh = now
	pc.Meta.Status, pc.Meta.Error = CredentialOK, ""
	if l := c.Label(); l != "" {
		pc.Meta.Account = l
	}
	pc.UpdatedAt = now
	if err := s.db.UpsertCredential(ctx, pc); err != nil {
		return ProviderCredential{}, err
	}
	pc.Sealed = nil
	return pc, nil
}

// DisableCredential records a refresh that failed for good: the login is
// dead until its owner signs in again, and runs on it are refused with why.
func (s *Service) DisableCredential(ctx context.Context, id, reason string) error {
	pc, err := s.db.GetCredential(ctx, id)
	if err != nil {
		return err
	}
	pc.Meta.Status, pc.Meta.Error = CredentialDisabled, reason
	pc.UpdatedAt = s.now().UTC()
	return s.db.UpsertCredential(ctx, pc)
}

// RenameCredential changes what the credential is called.
func (s *Service) RenameCredential(ctx context.Context, id, name string) (ProviderCredential, error) {
	if strings.TrimSpace(name) == "" {
		return ProviderCredential{}, invalidf("credential: a name is required")
	}
	pc, err := s.db.GetCredential(ctx, id)
	if err != nil {
		return ProviderCredential{}, err
	}
	pc.Name, pc.UpdatedAt = strings.TrimSpace(name), s.now().UTC()
	if err := s.db.UpsertCredential(ctx, pc); err != nil {
		return ProviderCredential{}, err
	}
	pc.Sealed = nil
	return pc, nil
}

// DeleteCredential removes a credential no harness integration references.
func (s *Service) DeleteCredential(ctx context.Context, id string) error {
	pc, err := s.db.GetCredential(ctx, id)
	if err != nil {
		return err
	}
	ints, err := s.db.ListIntegrations(ctx, pc.UserID)
	if err != nil {
		return err
	}
	var refs []string
	for _, in := range ints {
		for _, ref := range CredentialRefIDs(in.Config) {
			if ref == id {
				refs = append(refs, in.Name)
			}
		}
	}
	if len(refs) > 0 {
		sort.Strings(refs)
		return fmt.Errorf("credential %s is used by harness integration(s) %s: %w", pc.Name, strings.Join(refs, ", "), ErrReferenced)
	}
	return s.db.DeleteCredential(ctx, id)
}

// IntegrationsUsing lists the harness integrations referencing a credential
// (the keeper's push: which runs to rewrite).
func (s *Service) IntegrationsUsing(ctx context.Context, credentialID string) ([]string, error) {
	pc, err := s.db.GetCredential(ctx, credentialID)
	if err != nil {
		return nil, err
	}
	ints, err := s.db.ListIntegrations(ctx, pc.UserID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, in := range ints {
		for _, ref := range CredentialRefIDs(in.Config) {
			if ref == credentialID {
				out = append(out, in.ID)
			}
		}
	}
	return out, nil
}

// validateCredentialRefs checks a harness integration's references: each is
// one of the owner's credentials, of a provider this harness kind takes with
// that credential kind, and no two land in the same place in the harness
// (providers.Sink). Normalises the list in place.
func (s *Service) validateCredentialRefs(ctx context.Context, in *Integration) error {
	if in.Config == nil {
		in.Config = map[string]any{}
	}
	ids := CredentialRefIDs(in.Config)
	seen := map[string]bool{}
	sinks := map[string]string{}
	norm := make([]any, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		pc, err := s.db.GetCredential(ctx, id)
		if errors.Is(err, ErrNotFound) || (err == nil && !sameOwner(pc.UserID, in.UserID)) {
			return invalidf("integration: credential %s: %v", id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		p, ok := providers.Get(pc.Provider)
		if !ok {
			return invalidf("integration: credential %q is for provider %q, which apothecary no longer manages", pc.Name, pc.Provider)
		}
		if _, ok := p.Native(in.Kind, pc.Kind); !ok {
			return invalidf("integration: %s cannot use %q (%s, %s)", in.Kind, pc.Name, p.Name, pc.Kind)
		}
		sink := providers.Sink(in.Kind, p, pc.Kind)
		if other, dup := sinks[sink]; dup {
			return invalidf("integration: %s keeps %q and %q in the same place (%s) — reference one of them", in.Kind, other, pc.Name, sink)
		}
		sinks[sink] = pc.Name
		norm = append(norm, id)
	}
	in.Config[ConfigCredentialIDs] = norm
	return nil
}

// InstanceSecret returns the instance's random secret named name, creating
// it on first use: 32 bytes, sealed with the master key in the instance's
// settings (owner ""), stable across restarts. What a token that must
// survive a restart is derived from (the auth broker's per-run tokens).
func (s *Service) InstanceSecret(ctx context.Context, name string) ([]byte, error) {
	key := "instance.secret." + name
	settings, err := s.db.GetSettings(ctx, "")
	if err != nil {
		return nil, err
	}
	if v, ok := settings[key]; ok {
		// Settings hold JSON values: the sealed bytes are a base64 JSON string.
		var enc string
		if err := json.Unmarshal([]byte(v), &enc); err != nil {
			return nil, fmt.Errorf("instance secret %s: %w", name, err)
		}
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return nil, fmt.Errorf("instance secret %s: %w", name, err)
		}
		m, err := secrets.Open(s.key, raw)
		if err != nil {
			return nil, fmt.Errorf("instance secret %s: %w", name, err)
		}
		b, err := base64.StdEncoding.DecodeString(m[name])
		if err != nil || len(b) == 0 {
			return nil, fmt.Errorf("instance secret %s: unreadable", name)
		}
		return b, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	sealed, err := secrets.Seal(s.key, map[string]string{name: base64.StdEncoding.EncodeToString(b)})
	if err != nil {
		return nil, err
	}
	enc, _ := json.Marshal(base64.StdEncoding.EncodeToString(sealed))
	if err := s.db.PutSetting(ctx, "", key, string(enc)); err != nil {
		return nil, err
	}
	return b, nil
}
