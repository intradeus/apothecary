// Package github adapts the Forge port to the GitHub REST API v3
// (DESIGN2 §12): Bearer PAT auth, no vendor SDK, no GitHub types across
// the port boundary. RepoRef.Locator is "owner/repo". "Draft MR" maps to a
// native draft pull request — no title prefix (deviation from the GitLab
// adapter, recorded).
package github

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
	// BaseURL is the API endpoint (default https://api.github.com;
	// GitHub Enterprise would use https://<host>/api/v3).
	BaseURL string
	Token   string
	// Locator is the bound repo's "owner/repo" (§12: the adapter is
	// constructed per project from RepoRef.Locator).
	Locator string
}

type Adapter struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Adapter {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.github.com"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Adapter{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
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
	req, err := http.NewRequestWithContext(ctx, method, a.cfg.BaseURL+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-Ratelimit-Remaining") == "0" {
		// Rate-limit 403s read like permission errors otherwise; name them.
		return resp.StatusCode, fmt.Errorf("github %s %s: rate limit exhausted (resets at unix %s) — not a permissions problem",
			method, path, resp.Header.Get("X-Ratelimit-Reset"))
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("github %s %s: %d: %s", method, path, resp.StatusCode, tail(string(data), 300))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("github %s %s: decode: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

func (a *Adapter) repoPath() string { return "/repos/" + a.cfg.Locator }

// CheckAccess verifies the token authenticates and can push to the repo
// (§1 preflight): classic PAT `repo` scope, or fine-grained with
// Contents:RW + Pull requests:RW.
func (a *Adapter) CheckAccess(ctx context.Context, _ ports.RepoRef) error {
	if a.cfg.Token == "" {
		return fmt.Errorf("github: no token configured")
	}
	var repo struct {
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if _, err := a.api(ctx, http.MethodGet, a.repoPath(), nil, &repo); err != nil {
		return fmt.Errorf("repo %s unreachable: %w", a.cfg.Locator, err)
	}
	if !repo.Permissions.Push {
		return fmt.Errorf("token cannot push to %s (needs `repo` scope or Contents:RW + Pull requests:RW)", a.cfg.Locator)
	}
	return nil
}

// FileExists checks a file — or, with a trailing slash, a non-empty
// directory — at the default branch (§1 preflight). The contents endpoint
// returns an array for directories.
func (a *Adapter) FileExists(ctx context.Context, repo ports.RepoRef, path string) (bool, error) {
	ref := repo.DefaultBranch
	if ref == "" {
		ref = "main"
	}
	// Escape per segment: GitHub's contents API takes a real path, and an
	// encoded slash (%2F) is GitLab's convention, not GitHub's — proxies may
	// reject or normalize it.
	segs := strings.Split(strings.TrimSuffix(path, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	p := fmt.Sprintf("%s/contents/%s?ref=%s",
		a.repoPath(), strings.Join(segs, "/"), url.QueryEscape(ref))
	var raw json.RawMessage
	status, err := a.api(ctx, http.MethodGet, p, nil, &raw)
	if status == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if strings.HasSuffix(path, "/") {
		var entries []json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return false, nil // a file where a directory was expected
		}
		return len(entries) > 0, nil
	}
	return true, nil
}

// baseOr is the PR base: the target asked for, else the repo's default.
func baseOr(target string, repo ports.RepoRef) string {
	if target != "" {
		return target
	}
	if repo.DefaultBranch != "" {
		return repo.DefaultBranch
	}
	return "main"
}

// CreateDraftMR opens a native draft pull request onto target (a stacked
// change chains bases layer onto layer).
func (a *Adapter) CreateDraftMR(ctx context.Context, repo ports.RepoRef, branch, target, title, body string) (ports.MRRef, error) {
	base := baseOr(target, repo)
	var pr struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	status, err := a.api(ctx, http.MethodPost, a.repoPath()+"/pulls", map[string]any{
		"title": title,
		"head":  branch,
		"base":  base,
		"body":  body,
		"draft": true,
	}, &pr)
	if err != nil && status == http.StatusUnprocessableEntity && strings.Contains(err.Error(), "raft") {
		// Plans without draft-PR support (private repos on Free) 422 here;
		// degrade to a regular PR with the GitLab-style title prefix instead
		// of failing the stage outright.
		status, err = a.api(ctx, http.MethodPost, a.repoPath()+"/pulls", map[string]any{
			"title": "Draft: " + title,
			"head":  branch,
			"base":  base,
			"body":  body,
		}, &pr)
	}
	if err != nil && status == http.StatusUnprocessableEntity && strings.Contains(err.Error(), "already exists") {
		// A PR for this head branch already exists (422). Treat it as success —
		// the stage is idempotent — by returning the existing one.
		if existing, lookErr := a.findOpenPR(ctx, branch, base); lookErr == nil && existing.IID != 0 {
			return existing, nil
		}
	}
	if err != nil {
		return ports.MRRef{}, err
	}
	return ports.MRRef{IID: pr.Number, URL: pr.HTMLURL}, nil
}

// FindMR implements the port's reconciliation lookup: the open PR whose head
// is this branch, onto target.
func (a *Adapter) FindMR(ctx context.Context, repo ports.RepoRef, branch, target string) (ports.MRRef, bool, error) {
	ref, err := a.findOpenPR(ctx, branch, baseOr(target, repo))
	if err != nil {
		return ports.MRRef{}, false, err
	}
	return ref, ref.IID != 0, nil
}

// findOpenPR returns the open PR from branch onto base, or a zero MRRef if
// none is found. The head filter is scoped to the bound repo's owner.
func (a *Adapter) findOpenPR(ctx context.Context, branch, base string) (ports.MRRef, error) {
	head := branch
	if owner, _, ok := strings.Cut(a.cfg.Locator, "/"); ok {
		head = owner + ":" + branch
	}
	var prs []struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	p := fmt.Sprintf("%s/pulls?state=open&head=%s&base=%s",
		a.repoPath(), url.QueryEscape(head), url.QueryEscape(base))
	if _, err := a.api(ctx, http.MethodGet, p, nil, &prs); err != nil {
		return ports.MRRef{}, err
	}
	if len(prs) == 0 {
		return ports.MRRef{}, nil
	}
	return ports.MRRef{IID: prs[0].Number, URL: prs[0].HTMLURL}, nil
}

// MRState maps the PR state to open|merged|closed: merged_at wins over
// state (a merged PR reports state "closed").
func (a *Adapter) MRState(ctx context.Context, mr ports.MRRef) (ports.MRState, error) {
	var pr struct {
		State    string  `json:"state"`
		MergedAt *string `json:"merged_at"`
	}
	if _, err := a.api(ctx, http.MethodGet,
		fmt.Sprintf("%s/pulls/%d", a.repoPath(), mr.IID), nil, &pr); err != nil {
		return "", err
	}
	switch {
	case pr.MergedAt != nil && *pr.MergedAt != "":
		return ports.MRMerged, nil
	case pr.State == "closed":
		return ports.MRClosed, nil
	default:
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
