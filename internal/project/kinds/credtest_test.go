package kinds

import (
	"time"

	"apothecary/internal/account/providers"
)

// withCreds is a harness Config over these credentials (the snapshot; no
// keeper, so Fresh is the snapshot too).
func withCreds(creds ...providers.Credential) Config {
	return Config{Credentials: Credentials{Snapshot: creds}}
}

func apiKey(provider, key string) providers.Credential {
	return providers.Credential{ID: provider + "-key", Provider: provider, Kind: providers.KindAPIKey, Key: key}
}

func login(provider, access string, extra map[string]string) providers.Credential {
	c := providers.Credential{ID: provider + "-login", Provider: provider, Kind: providers.KindOAuth,
		Access: access, Refresh: "refresh-" + access, Expires: time.Now().Add(time.Hour), Extra: extra}
	if provider == "github-copilot" {
		c.Refresh, c.Expires = access, time.Time{}
	}
	return c
}
