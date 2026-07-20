package github

import (
	"context"
	"fmt"
	"net/http"
)

// Account is the account-level GitHub client (DESIGN2 §4 ForgeAccount):
// token probe and repo browsing for the project wizard.
type Account struct {
	a *Adapter
}

func NewAccount(baseURL, token string) *Account {
	return &Account{a: New(Config{BaseURL: baseURL, Token: token})}
}

// Test verifies the token authenticates (§12: GET /user).
func (ac *Account) Test(ctx context.Context) error {
	if ac.a.cfg.Token == "" {
		return fmt.Errorf("github: no token configured")
	}
	var user struct {
		Login string `json:"login"`
	}
	if _, err := ac.a.api(ctx, http.MethodGet, "/user", nil, &user); err != nil {
		return err
	}
	return nil
}

// Repo is one pickable repository (kinds.RepoInfo source).
type Repo struct {
	Name          string
	URL           string
	Locator       string // "owner/repo" (RepoRef.Locator)
	DefaultBranch string
}

// ListRepos returns repos the token can reach, most recently pushed first,
// paginated to exhaustion (§12).
func (ac *Account) ListRepos(ctx context.Context) ([]Repo, error) {
	var out []Repo
	for page := 1; page > 0 && page < 100; {
		var rows []struct {
			FullName      string `json:"full_name"`
			CloneURL      string `json:"clone_url"`
			DefaultBranch string `json:"default_branch"`
		}
		p := fmt.Sprintf("/user/repos?per_page=100&sort=pushed&affiliation=owner,collaborator,organization_member&page=%d", page)
		if _, err := ac.a.api(ctx, http.MethodGet, p, nil, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, Repo{
				Name:          r.FullName,
				URL:           r.CloneURL,
				Locator:       r.FullName,
				DefaultBranch: r.DefaultBranch,
			})
		}
		if len(rows) < 100 {
			break
		}
		page++
	}
	return out, nil
}
