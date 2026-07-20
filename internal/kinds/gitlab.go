package kinds

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	gitlabadapter "factory/internal/adapters/gitlab"
	"factory/internal/ports"
)

// GitLab is the forge/gitlab kind (DESIGN2 §4.1): base_url + PAT, numeric
// project id as RepoRef.Locator.
type GitLab struct{}

func (GitLab) Descriptor() Descriptor {
	return Descriptor{
		Port: "forge", Kind: "gitlab", Label: "GitLab",
		Fields: []Field{
			{Name: "base_url", Label: "Base URL", Default: "https://gitlab.com", Required: true},
			{Name: "token", Label: "Personal access token (api scope, Developer+)", Secret: true, Required: true},
		},
	}
}

type gitlabAccount struct {
	acc     *gitlabadapter.Account
	baseURL string
	token   string
}

func (GitLab) Account(cfg Config) (ForgeAccount, error) {
	base := cfg.Str("base_url", "https://gitlab.com")
	return &gitlabAccount{
		acc:     gitlabadapter.NewAccount(base, cfg.Secrets["token"]),
		baseURL: base,
		token:   cfg.Secrets["token"],
	}, nil
}

func (a *gitlabAccount) Test(ctx context.Context) error { return a.acc.Test(ctx) }

func (a *gitlabAccount) ListRepos(ctx context.Context) ([]RepoInfo, error) {
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

// GitCredential: GitLab accepts any non-empty user with a PAT password;
// "oauth2" is the v1 convention (§11 step 3).
func (a *gitlabAccount) GitCredential() (string, string) {
	if a.token == "" {
		return "", ""
	}
	return "oauth2", a.token
}

func (a *gitlabAccount) EgressDomains() []string {
	if u, err := url.Parse(a.baseURL); err == nil && u.Host != "" {
		return []string{u.Hostname()}
	}
	return nil
}

func (GitLab) Forge(cfg Config, repo ports.RepoRef) (ports.Forge, error) {
	id, err := strconv.Atoi(repo.Locator)
	if err != nil {
		return nil, fmt.Errorf("gitlab: repo locator must be the numeric project id, got %q", repo.Locator)
	}
	return gitlabadapter.New(gitlabadapter.Config{
		BaseURL:   cfg.Str("base_url", "https://gitlab.com"),
		Token:     cfg.Secrets["token"],
		ProjectID: id,
	}), nil
}

var _ ForgeKind = GitLab{}
