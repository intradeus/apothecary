package kinds

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	gitlabadapter "apothecary/internal/adapters/gitlab"
	"apothecary/internal/ports/repoport"
)

// GitLab is the repository host/gitlab kind (DESIGN2 §4.1): base_url + PAT,
// numeric project id as RepoRef.Locator.
type GitLab struct{}

func (GitLab) Descriptor() Descriptor {
	return Descriptor{
		Port: "repository", Kind: "gitlab", Label: "GitLab",
		Fields: []Field{
			{Name: "base_url", Label: "Base URL", Default: "https://gitlab.com", Required: true},
			{Name: "token", Label: "Personal access token (api scope, Developer+)", Secret: true, Required: true},
			{Name: "git_name", Label: "Commit author name", Placeholder: "Apothecary"},
			{Name: "git_email", Label: "Commit author email", Placeholder: "apothecary@local"},
		},
	}
}

type gitlabAccount struct {
	acc      *gitlabadapter.Account
	baseURL  string
	token    string
	gitName  string
	gitEmail string
}

func (GitLab) Account(cfg Config) (RepoAccount, error) {
	base := cfg.Str("base_url", "https://gitlab.com")
	return &gitlabAccount{
		acc:      gitlabadapter.NewAccount(base, cfg.Secrets["token"]),
		baseURL:  base,
		token:    cfg.Secrets["token"],
		gitName:  cfg.Str("git_name", ""),
		gitEmail: cfg.Str("git_email", ""),
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

func (a *gitlabAccount) ListBranches(ctx context.Context, locator string) ([]BranchInfo, error) {
	branches, err := a.acc.ListBranches(ctx, locator)
	if err != nil {
		return nil, err
	}
	out := make([]BranchInfo, 0, len(branches))
	for _, b := range branches {
		out = append(out, BranchInfo{Name: b.Name, Default: b.Default})
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

// GitIdentity: the operator-configured commit author (empty ⇒ bootstrap default).
func (a *gitlabAccount) GitIdentity() (string, string) { return a.gitName, a.gitEmail }

func (a *gitlabAccount) EgressDomains() []string {
	if u, err := url.Parse(a.baseURL); err == nil && u.Host != "" {
		return []string{u.Hostname()}
	}
	return nil
}

func (GitLab) RepoHost(cfg Config, repo repoport.RepoRef) (repoport.RepoHost, error) {
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

var _ RepoHostKind = GitLab{}
