package kinds

import (
	"context"
	"fmt"
	"strings"

	githubadapter "factory/internal/adapters/github"
	"factory/internal/ports"
)

// GitHub is the forge/github kind (DESIGN2 §12): Bearer PAT, Locator is
// "owner/repo", draft PRs are native (no title prefix).
type GitHub struct{}

func (GitHub) Descriptor() Descriptor {
	return Descriptor{
		Port: "forge", Kind: "github", Label: "GitHub",
		Fields: []Field{
			{Name: "base_url", Label: "API base URL", Default: "https://api.github.com", Required: true},
			{Name: "token", Label: "Personal access token (repo scope, or fine-grained Contents:RW + Pull requests:RW)", Secret: true, Required: true},
		},
	}
}

type githubAccount struct {
	acc   *githubadapter.Account
	token string
}

func (GitHub) Account(cfg Config) (ForgeAccount, error) {
	return &githubAccount{
		acc:   githubadapter.NewAccount(cfg.Str("base_url", "https://api.github.com"), cfg.Secrets["token"]),
		token: cfg.Secrets["token"],
	}, nil
}

func (a *githubAccount) Test(ctx context.Context) error { return a.acc.Test(ctx) }

func (a *githubAccount) ListRepos(ctx context.Context) ([]RepoInfo, error) {
	repos, err := a.acc.ListRepos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RepoInfo, 0, len(repos))
	for _, r := range repos {
		out = append(out, RepoInfo{Name: r.Name, URL: r.URL, Locator: r.Locator, DefaultBranch: r.DefaultBranch})
	}
	return out, nil
}

// GitCredential: GitHub's PAT-over-HTTPS convention (§12).
func (a *githubAccount) GitCredential() (string, string) {
	if a.token == "" {
		return "", ""
	}
	return "x-access-token", a.token
}

func (a *githubAccount) EgressDomains() []string {
	return []string{"github.com", "api.github.com", "codeload.github.com"}
}

func (GitHub) Forge(cfg Config, repo ports.RepoRef) (ports.Forge, error) {
	locator := strings.TrimSpace(repo.Locator)
	if !strings.Contains(locator, "/") {
		return nil, fmt.Errorf(`github: repo locator must be "owner/repo", got %q`, repo.Locator)
	}
	return githubadapter.New(githubadapter.Config{
		BaseURL: cfg.Str("base_url", "https://api.github.com"),
		Token:   cfg.Secrets["token"],
		Locator: locator,
	}), nil
}

var _ ForgeKind = GitHub{}
