// Package gitlab adapts the Forge port to the GitLab REST API v4
// (DESIGN.md §0: REST, not glab). The engine holds one repo-scoped token;
// no GitLab types cross the port boundary.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"apothecary/internal/ports"
)

type Config struct {
	BaseURL string // e.g. https://gitlab.com or http://127.0.0.1:8929
	Token   string
	// ProjectID binds the adapter to the one v1 project (§13
	// gitlab.project_id) — MRState receives only an MRRef (§4).
	ProjectID int
}

type Adapter struct {
	cfg       Config
	projectID int
	http      *http.Client
}

func New(cfg Config) *Adapter {
	return &Adapter{
		cfg:       Config{BaseURL: strings.TrimRight(cfg.BaseURL, "/"), Token: cfg.Token, ProjectID: cfg.ProjectID},
		projectID: cfg.ProjectID,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

// api performs one authenticated call; out may be nil.
func (a *Adapter) api(ctx context.Context, method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.cfg.BaseURL+"/api/v4"+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("PRIVATE-TOKEN", a.cfg.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("gitlab %s %s: %d: %s", method, path, resp.StatusCode, tail(string(data), 300))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("gitlab %s %s: decode: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// path is the bound project's API prefix. The adapter is constructed per
// project (DESIGN2 §4: ForgeKind.Forge(cfg, repo) parses RepoRef.Locator),
// so the RepoRef arguments carry branch/URL context only.
func (a *Adapter) path() string {
	return fmt.Sprintf("/projects/%d", a.projectID)
}

// CheckAccess verifies the token is valid, the project reachable, and the
// access level sufficient to push branches and open MRs (§1 preflight).
func (a *Adapter) CheckAccess(ctx context.Context, repo ports.RepoRef) error {
	if a.cfg.Token == "" {
		return fmt.Errorf("gitlab: no token configured")
	}
	var proj struct {
		Permissions struct {
			ProjectAccess *struct {
				AccessLevel int `json:"access_level"`
			} `json:"project_access"`
			GroupAccess *struct {
				AccessLevel int `json:"access_level"`
			} `json:"group_access"`
		} `json:"permissions"`
	}
	if _, err := a.api(ctx, http.MethodGet, a.path(), nil, &proj); err != nil {
		return fmt.Errorf("project %d unreachable: %w", a.projectID, err)
	}
	const developer = 30
	level := 0
	if p := proj.Permissions.ProjectAccess; p != nil && p.AccessLevel > level {
		level = p.AccessLevel
	}
	if g := proj.Permissions.GroupAccess; g != nil && g.AccessLevel > level {
		level = g.AccessLevel
	}
	if level < developer {
		return fmt.Errorf("token has access level %d on project %d; needs >= %d (Developer) to push and open MRs", level, a.projectID, developer)
	}
	return nil
}

// FileExists checks a file — or, with a trailing slash, a non-empty
// directory — at the default branch (§1 preflight).
func (a *Adapter) FileExists(ctx context.Context, repo ports.RepoRef, path string) (bool, error) {
	ref := repo.DefaultBranch
	if ref == "" {
		ref = "main"
	}
	if strings.HasSuffix(path, "/") {
		var entries []struct {
			Name string `json:"name"`
		}
		p := fmt.Sprintf("%s/repository/tree?path=%s&ref=%s&per_page=1",
			a.path(), url.QueryEscape(strings.TrimSuffix(path, "/")), url.QueryEscape(ref))
		status, err := a.api(ctx, http.MethodGet, p, nil, &entries)
		if status == http.StatusNotFound {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return len(entries) > 0, nil
	}
	p := fmt.Sprintf("%s/repository/files/%s?ref=%s",
		a.path(), url.PathEscape(path), url.QueryEscape(ref))
	status, err := a.api(ctx, http.MethodHead, p, nil, nil)
	if status == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// targetOr is the MR target: the one asked for, else the repo's default.
func targetOr(target string, repo ports.RepoRef) string {
	if target != "" {
		return target
	}
	if repo.DefaultBranch != "" {
		return repo.DefaultBranch
	}
	return "main"
}

// CreateDraftMR opens a draft merge request from branch onto target (§3.1 mr
// stage); a stacked change chains targets layer onto layer.
func (a *Adapter) CreateDraftMR(ctx context.Context, repo ports.RepoRef, branch, target, title, body string) (ports.MRRef, error) {
	target = targetOr(target, repo)
	if !strings.HasPrefix(title, "Draft:") {
		title = "Draft: " + title
	}
	var mr struct {
		IID    int    `json:"iid"`
		WebURL string `json:"web_url"`
	}
	status, err := a.api(ctx, http.MethodPost, a.path()+"/merge_requests", map[string]any{
		"source_branch":        branch,
		"target_branch":        target,
		"title":                title,
		"description":          body,
		"remove_source_branch": true,
	}, &mr)
	if err != nil && status == http.StatusConflict {
		// An open MR for this source branch already exists (409). Treat it as
		// success — the stage is idempotent — by returning the existing one.
		if existing, lookErr := a.findOpenMR(ctx, branch, target); lookErr == nil && existing.IID != 0 {
			return existing, nil
		}
	}
	if err != nil {
		return ports.MRRef{}, err
	}
	return ports.MRRef{IID: mr.IID, URL: mr.WebURL}, nil
}

// FindMR implements the port's reconciliation lookup: the open MR from this
// source branch onto target.
func (a *Adapter) FindMR(ctx context.Context, repo ports.RepoRef, branch, target string) (ports.MRRef, bool, error) {
	ref, err := a.findOpenMR(ctx, branch, targetOr(target, repo))
	if err != nil {
		return ports.MRRef{}, false, err
	}
	return ref, ref.IID != 0, nil
}

// findOpenMR returns the open MR from branch onto target, or a zero MRRef if
// none is found.
func (a *Adapter) findOpenMR(ctx context.Context, branch, target string) (ports.MRRef, error) {
	var mrs []struct {
		IID    int    `json:"iid"`
		WebURL string `json:"web_url"`
	}
	p := fmt.Sprintf("%s/merge_requests?state=opened&source_branch=%s&target_branch=%s",
		a.path(), url.QueryEscape(branch), url.QueryEscape(target))
	if _, err := a.api(ctx, http.MethodGet, p, nil, &mrs); err != nil {
		return ports.MRRef{}, err
	}
	if len(mrs) == 0 {
		return ports.MRRef{}, nil
	}
	return ports.MRRef{IID: mrs[0].IID, URL: mrs[0].WebURL}, nil
}

// MRState maps GitLab's state to the port's open|merged|closed. The MRRef
// alone must suffice (§4), so the project id rides in via RepoRef at
// construction — the adapter is bound to the one v1 project.
func (a *Adapter) MRState(ctx context.Context, mr ports.MRRef) (ports.MRState, error) {
	var out struct {
		State string `json:"state"`
	}
	if _, err := a.api(ctx, http.MethodGet,
		fmt.Sprintf("/projects/%d/merge_requests/%d", a.projectID, mr.IID), nil, &out); err != nil {
		return "", err
	}
	switch out.State {
	case "merged":
		return ports.MRMerged, nil
	case "closed":
		return ports.MRClosed, nil
	default: // opened, locked
		return ports.MROpen, nil
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

var _ ports.Forge = (*Adapter)(nil)
