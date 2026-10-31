package kinds

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"apothecary/internal/account/providers"
)

// CheckCredential probes one provider credential on its own, from the engine
// and without spending tokens: an API key with its provider's zero-token
// probe (the ones a harness's preflight uses), a ChatGPT login against its
// account's model list. ok with a detail when it could not tell; an error
// when the provider refused. Any other login is verified by refreshing it,
// which is the keeper's (the caller's) to do.
func CheckCredential(ctx context.Context, c providers.Credential) (unverified string, err error) {
	p, ok := providers.Get(c.Provider)
	if !ok {
		return "", fmt.Errorf("provider %q is not one apothecary manages", c.Provider)
	}
	if c.Kind == providers.KindOAuth {
		if p.Flow == providers.FlowChatGPT {
			if _, err := chatgptModels(ctx, c.Access, c.Get(providers.ExtraAccountID)); err != nil {
				if _, refused := err.(*loginRefused); refused {
					return "", err
				}
				return err.Error(), nil
			}
		}
		return "", nil
	}
	catalog, err := fetchCatalog(ctx)
	if err != nil {
		return err.Error(), nil
	}
	cp := catalog[p.Catalog]
	ids := make([]string, 0, len(cp.Models))
	for id := range cp.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	model := ""
	if len(ids) > 0 {
		model = ids[0]
	}
	pr, probeable := providerAuthProbe(p.Catalog, cp, c.Key, model)
	if !probeable {
		return "no zero-token probe for this provider", nil
	}
	rejected, reason, err := pr.run(ctx, c.Key)
	switch {
	case err != nil:
		return err.Error(), nil
	case rejected && strings.HasPrefix(reason, "model"):
		// The gateway checked the borrowed model before the key.
		return "probe model " + model + " " + reason, nil
	case rejected:
		return "", fmt.Errorf("%s: %s", p.Name, reason)
	}
	return "", nil
}
