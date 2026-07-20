package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	opencodeadapter "factory/internal/adapters/opencode"
	"factory/internal/ports"
)

// OpencodeVersion is the pinned harness version (kept in lockstep with the
// M2 image pin; the harness layer installs exactly this).
const OpencodeVersion = "1.17.8"

// Opencode is the harness/opencode kind (DESIGN2 §4.1).
type Opencode struct{}

func (Opencode) Descriptor() Descriptor {
	return Descriptor{
		Port: "harness", Kind: "opencode", Label: "opencode",
		Fields: []Field{
			{Name: "version", Label: "Version", Default: OpencodeVersion},
			{Name: "auth_json", Label: "auth.json (subscription/router auth, optional)", Secret: true},
		},
	}
}

func (Opencode) New(cfg Config, envp ports.EnvProvisioner) (Harness, error) {
	return &opencodeHarness{
		adapter:  opencodeadapter.New(envp, opencodeadapter.Config{}),
		version:  cfg.Str("version", OpencodeVersion),
		authJSON: cfg.Secrets["auth_json"],
	}, nil
}

type opencodeHarness struct {
	adapter  *opencodeadapter.Adapter
	version  string
	authJSON string
}

func (h *opencodeHarness) Run(ctx context.Context, env ports.EnvID, spec ports.AgentSpec, in ports.AgentInput) (ports.AgentResult, error) {
	return h.adapter.Run(ctx, env, spec, in)
}

func (h *opencodeHarness) Resume(ctx context.Context, env ports.EnvID, s ports.SessionID, reply string) (ports.AgentResult, error) {
	return h.adapter.Resume(ctx, env, s, reply)
}

// Test statically validates the auth material (§4: no remote to probe).
func (h *opencodeHarness) Test(_ context.Context) error {
	if h.authJSON != "" && !json.Valid([]byte(h.authJSON)) {
		return fmt.Errorf("opencode: auth_json is not valid JSON")
	}
	return nil
}

func (h *opencodeHarness) Binary() string { return "opencode" }

func (h *opencodeHarness) InstallLayer() []string {
	return []string{fmt.Sprintf("RUN npm install -g opencode-ai@%s", h.version)}
}

func (h *opencodeHarness) EgressDomains() []string {
	// The openrouter lesson (IMPLEMENTATION_NOTES M3): router auth needs
	// its domain allowed. Provider integrations contribute their own
	// domains; this covers auth.json-only router setups.
	if strings.Contains(h.authJSON, "openrouter") {
		return []string{"openrouter.ai"}
	}
	return nil
}

// SeedAuth composes opencode's auth.json (bootstrap step 2): the stored
// auth_json secret is the base, and openrouter provider integrations are
// overlaid in opencode's native shape. Anthropic/OpenAI keys need no
// auth.json — opencode reads their standard env vars from provider.env.
func (h *opencodeHarness) SeedAuth(ctx context.Context, env ports.EnvID, providers []ProviderCreds) error {
	auth := map[string]any{}
	if h.authJSON != "" {
		if err := json.Unmarshal([]byte(h.authJSON), &auth); err != nil {
			return fmt.Errorf("opencode: auth_json: %w", err)
		}
	}
	for _, p := range providers {
		if p.Kind == "openrouter" {
			if key := p.Secrets["api_key"]; key != "" {
				auth["openrouter"] = map[string]any{"type": "api", "key": key}
			}
		}
	}
	if len(auth) == 0 {
		return nil
	}
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	return h.adapter.SeedAuthJSON(ctx, env, data)
}

func (h *opencodeHarness) Prepare(ctx context.Context, env ports.EnvID) error {
	return h.adapter.PrepareEnv(ctx, env)
}

var _ HarnessKind = Opencode{}
