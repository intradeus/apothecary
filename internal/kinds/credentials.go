package kinds

import (
	"context"
	"sort"

	"apothecary/internal/providers"
)

// Credentials are the provider credentials a harness integration references,
// opened engine-side (docs/model-providers-plan.md).
type Credentials struct {
	// Snapshot is what they were when the resolution was built: enough for
	// the static parts of a harness (its egress domains, its env vars, its
	// models). Access tokens in it may be stale by the time a workspace is
	// seeded — SeedAuth reads Fresh.
	Snapshot []providers.Credential
	// Problems name credentials that could not be used (a disabled login, a
	// credential deleted since): a run on them is refused with these.
	Problems map[string]string
	// Fresh returns the credentials with access tokens that will outlive the
	// keeper's margin, refreshing them first when needed. nil in tests: the
	// snapshot is used.
	Fresh func(ctx context.Context) ([]providers.Credential, error)
}

// current is what to render now.
func (c Credentials) current(ctx context.Context) ([]providers.Credential, error) {
	if c.Fresh == nil {
		return c.Snapshot, nil
	}
	return c.Fresh(ctx)
}

// bound is one credential as one harness consumes it.
type bound struct {
	providers.Credential
	provider providers.Provider
	native   providers.Native
}

// bind keeps the credentials harness takes, each with its provider and
// native mapping, in a stable order (by native id, keys before logins).
func bind(harness string, creds []providers.Credential) []bound {
	var out []bound
	for _, c := range creds {
		p, ok := providers.Get(c.Provider)
		if !ok {
			continue
		}
		n, ok := p.Native(harness, c.Kind)
		if !ok {
			continue
		}
		out = append(out, bound{Credential: c, provider: p, native: n})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].native.ID != out[j].native.ID {
			return out[i].native.ID < out[j].native.ID
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// domains are the egress hosts of the bound credentials' providers.
func domains(bs []bound) []string {
	var out []string
	for _, b := range bs {
		out = append(out, b.provider.Domains...)
	}
	return out
}

// secretValues are what the redactor must know about the bound credentials.
func secretValues(bs []bound) []string {
	var out []string
	for _, b := range bs {
		out = append(out, b.SecretValues()...)
	}
	return out
}

// credentialsField is the harness form's one input: the owner's provider
// credentials it uses.
func credentialsField(harness string) Field {
	var names []string
	for _, p := range providers.For(harness) {
		names = append(names, p.Name)
	}
	return Field{
		Name: "credential_ids", Label: "Model providers", Type: "credentials", Required: true,
		Help: "The provider credentials this harness uses — API keys and logins from System → Model providers. " +
			"It takes: " + joinNames(names) + ".",
	}
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
