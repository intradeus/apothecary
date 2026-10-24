package kinds

import (
	"context"
	"fmt"
	"strings"

	githubadapter "apothecary/internal/adapters/github"
	"apothecary/internal/ports"
)

// GitHub is the repository host/github kind (DESIGN2 §12): Bearer PAT, Locator
// is "owner/repo", draft PRs are native (no title prefix).
type GitHub struct{}

func (GitHub) Descriptor() Descriptor {
	return Descriptor{
		Port: "repository", Kind: "github", Label: "GitHub",
		Fields: []Field{
			{Name: "base_url", Label: "API base URL", Default: "https://api.github.com", Required: true},
			{Name: "token", Label: "Personal access token (repo scope, or fine-grained Contents:RW + Pull requests:RW)", Secret: true, Required: true},
			{Name: "git_name", Label: "Commit author name", Placeholder: "Apothecary"},
			{Name: "git_email", Label: "Commit author email", Placeholder: "apothecary@local"},
		},
	}
}

type githubAccount struct {
	acc      *githubadapter.Account
	token    string
	gitName  string
	gitEmail string
}

func (GitHub) Account(cfg Config) (RepoAccount, error) {
	return &githubAccount{
		acc:      githubadapter.NewAccount(cfg.Str("base_url", "https://api.github.com"), cfg.Secrets["token"]),
		token:    cfg.Secrets["token"],
		gitName:  cfg.Str("git_name", ""),
		gitEmail: cfg.Str("git_email", ""),
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

func (a *githubAccount) ListBranches(ctx context.Context, locator string) ([]BranchInfo, error) {
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

// GitCredential: GitHub's PAT-over-HTTPS convention (§12).
func (a *githubAccount) GitCredential() (string, string) {
	if a.token == "" {
		return "", ""
	}
	return "x-access-token", a.token
}

// GitIdentity: the operator-configured commit author (empty ⇒ bootstrap default).
func (a *githubAccount) GitIdentity() (string, string) { return a.gitName, a.gitEmail }

func (a *githubAccount) EgressDomains() []string {
	return []string{"github.com", "api.github.com", "codeload.github.com"}
}

func (GitHub) RepoHost(cfg Config, repo ports.RepoRef) (ports.RepoHost, error) {
	locator := strings.TrimSuffix(strings.TrimSpace(repo.Locator), ".git")
	if strings.Contains(locator, "://") || strings.Count(locator, "/") != 1 ||
		strings.HasPrefix(locator, "/") || strings.HasSuffix(locator, "/") {
		// A URL or nested path here produces garbage API routes and 404s far
		// from the real mistake — refuse it with the shape spelled out.
		return nil, fmt.Errorf(`github: repo locator must be "owner/repo" (not a URL), got %q`, repo.Locator)
	}
	return githubadapter.New(githubadapter.Config{
		BaseURL: cfg.Str("base_url", "https://api.github.com"),
		Token:   cfg.Secrets["token"],
		Locator: locator,
	}), nil
}

var _ RepoHostKind = GitHub{}
