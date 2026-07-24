package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Account is the account-level GitLab client (DESIGN2 §4 ForgeAccount):
// token probe and repo browsing for the project wizard. Project-bound
// operations live on Adapter.
type Account struct {
	a *Adapter
}

func NewAccount(baseURL, token string) *Account {
	return &Account{a: New(Config{BaseURL: baseURL, Token: token})}
}

// Test verifies the token authenticates (§4.1: GET /user).
func (ac *Account) Test(ctx context.Context) error {
	if ac.a.cfg.Token == "" {
		return fmt.Errorf("gitlab: no token configured")
	}
	var user struct {
		Username string `json:"username"`
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
	Locator       string // numeric project id as string (RepoRef.Locator)
	DefaultBranch string
}

// ListRepos returns repos the token can push to (Developer+), newest
// activity first, paginated to exhaustion.
func (ac *Account) ListRepos(ctx context.Context) ([]Repo, error) {
	var out []Repo
	for page := 1; page > 0 && page < 100; {
		var rows []struct {
			ID                int    `json:"id"`
			PathWithNamespace string `json:"path_with_namespace"`
			HTTPURLToRepo     string `json:"http_url_to_repo"`
			DefaultBranch     string `json:"default_branch"`
		}
		p := fmt.Sprintf("/projects?membership=true&min_access_level=30&order_by=last_activity_at&per_page=100&page=%d", page)
		if _, err := ac.a.api(ctx, http.MethodGet, p, nil, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, Repo{
				Name:          r.PathWithNamespace,
				URL:           r.HTTPURLToRepo,
				Locator:       strconv.Itoa(r.ID),
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

// Branch is one branch of a project (source-branch picker source).
type Branch struct {
	Name    string
	Default bool
}

// ListBranches returns a project's branches (GET
// /projects/:id/repository/branches), paginated to exhaustion. locator is the
// numeric project id (RepoRef.Locator).
func (ac *Account) ListBranches(ctx context.Context, locator string) ([]Branch, error) {
	pid := url.PathEscape(locator)
	var out []Branch
	for page := 1; page > 0 && page < 100; {
		var rows []struct {
			Name    string `json:"name"`
			Default bool   `json:"default"`
		}
		p := fmt.Sprintf("/projects/%s/repository/branches?per_page=100&page=%d", pid, page)
		if _, err := ac.a.api(ctx, http.MethodGet, p, nil, &rows); err != nil {
			return nil, err
		}
		for _, b := range rows {
			out = append(out, Branch{Name: b.Name, Default: b.Default})
		}
		if len(rows) < 100 {
			break
		}
		page++
	}
	return out, nil
}
