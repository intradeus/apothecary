package kinds

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Model-provider kinds (DESIGN2 §4.1): api_key secret, standard env var,
// API domain, cheap authenticated list-models probe.

type providerSpec struct {
	kind, label string
	envVar      string
	domains     []string
	// probe builds the authenticated test request.
	probe func(key string) (*http.Request, error)
}

var providerSpecs = []providerSpec{
	{
		kind: "anthropic", label: "Anthropic", envVar: "ANTHROPIC_API_KEY",
		domains: []string{"api.anthropic.com"},
		probe: func(key string) (*http.Request, error) {
			req, err := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/models", nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
			return req, nil
		},
	},
	{
		kind: "openai", label: "OpenAI", envVar: "OPENAI_API_KEY",
		domains: []string{"api.openai.com"},
		probe: func(key string) (*http.Request, error) {
			req, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/models", nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+key)
			return req, nil
		},
	},
	{
		kind: "openrouter", label: "OpenRouter", envVar: "OPENROUTER_API_KEY",
		domains: []string{"openrouter.ai"},
		probe: func(key string) (*http.Request, error) {
			req, err := http.NewRequest(http.MethodGet, "https://openrouter.ai/api/v1/key", nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+key)
			return req, nil
		},
	},
}

// ProviderKinds returns one kind per known provider (registered at wiring).
func ProviderKinds() []ProviderKind {
	out := make([]ProviderKind, 0, len(providerSpecs))
	for _, s := range providerSpecs {
		out = append(out, apiProviderKind{s})
	}
	return out
}

type apiProviderKind struct{ spec providerSpec }

func (k apiProviderKind) Descriptor() Descriptor {
	return Descriptor{
		Port: "provider", Kind: k.spec.kind, Label: k.spec.label,
		Fields: []Field{
			{Name: "api_key", Label: "API key", Secret: true, Required: true},
		},
	}
}

func (k apiProviderKind) New(cfg Config) (Provider, error) {
	return &apiProvider{spec: k.spec, key: cfg.Secrets["api_key"]}, nil
}

type apiProvider struct {
	spec providerSpec
	key  string
}

func (p *apiProvider) Test(ctx context.Context) error {
	if p.key == "" {
		return fmt.Errorf("%s: no API key configured", p.spec.kind)
	}
	req, err := p.spec.probe(p.key)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("%s unreachable: %w", p.spec.kind, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s auth check: %d: %s", p.spec.kind, resp.StatusCode, string(body))
	}
	return nil
}

func (p *apiProvider) EnvVars() map[string]string {
	if p.key == "" {
		return map[string]string{}
	}
	return map[string]string{p.spec.envVar: p.key}
}

func (p *apiProvider) EgressDomains() []string { return p.spec.domains }
