package github

import (
	"context"
	"fmt"
	"net/http"
)

// Account is the account-level GitHub client (DESIGN2 §4 RepoAccount):
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

// Branch is one branch of a repo (source-branch picker source).
type Branch struct {
	Name    string
	Default bool
}

// ListBranches returns a repo's branches (GET /repos/:owner/:repo/branches),
// paginated to exhaustion. locator is "owner/repo". A prior repo GET supplies
// the default branch (the branches endpoint doesn't flag it).
func (ac *Account) ListBranches(ctx context.Context, locator string) ([]Branch, error) {
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if _, err := ac.a.api(ctx, http.MethodGet, "/repos/"+locator, nil, &repo); err != nil {
		return nil, err
	}
	var out []Branch
	for page := 1; page > 0 && page < 100; {
		var rows []struct {
			Name string `json:"name"`
		}
		p := fmt.Sprintf("/repos/%s/branches?per_page=100&page=%d", locator, page)
		if _, err := ac.a.api(ctx, http.MethodGet, p, nil, &rows); err != nil {
			return nil, err
		}
		for _, b := range rows {
			out = append(out, Branch{Name: b.Name, Default: b.Name == repo.DefaultBranch})
		}
		if len(rows) < 100 {
			break
		}
		page++
	}
	return out, nil
}
