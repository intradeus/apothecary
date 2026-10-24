// Package github adapts the RepoHost port to the GitHub REST API v3
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

// MRStatus maps the PR: merged_at wins over state (a merged PR reports
// state "closed"); mergeable is null while GitHub is still computing it,
// which is unknown, never a conflict.
func (a *Adapter) MRStatus(ctx context.Context, mr ports.MRRef) (ports.MRStatus, error) {
	var pr struct {
		State          string  `json:"state"`
		MergedAt       *string `json:"merged_at"`
		Mergeable      *bool   `json:"mergeable"`
		MergeableState string  `json:"mergeable_state"`
		Draft          bool    `json:"draft"`
		Head           struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if _, err := a.api(ctx, http.MethodGet,
		fmt.Sprintf("%s/pulls/%d", a.repoPath(), mr.IID), nil, &pr); err != nil {
		return ports.MRStatus{}, err
	}
	st := ports.MRStatus{State: ports.MROpen, HeadSHA: pr.Head.SHA, Target: pr.Base.Ref, Mergeable: ports.MergeClean}
	switch {
	case pr.MergedAt != nil && *pr.MergedAt != "":
		st.State = ports.MRMerged
	case pr.State == "closed":
		st.State = ports.MRClosed
	}
	switch {
	case pr.Mergeable == nil || pr.MergeableState == "unknown":
		st.Mergeable = ports.MergeUnknown
	case pr.MergeableState == "dirty":
		st.Mergeable = ports.MergeConflicts
	}
	// clean (and has_hooks, clean with pre-receive hooks) is GitHub saying
	// every requirement is met; unstable merges too, but with checks red or
	// running, which is not "ready".
	st.Draft = pr.Draft
	st.MergeableNow = st.State == ports.MROpen && !pr.Draft && pr.Mergeable != nil && *pr.Mergeable &&
		(pr.MergeableState == "clean" || pr.MergeableState == "has_hooks")
	if st.Mergeable == ports.MergeConflicts && pr.Base.Ref != "" {
		var br struct {
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/branches/%s", a.repoPath(), url.PathEscape(pr.Base.Ref)), nil, &br); err != nil {
			return ports.MRStatus{}, err
		}
		st.TargetSHA = br.Commit.SHA
	}
	return st, nil
}

// MRChecks reads both kinds of CI result GitHub keeps on a commit: check
// runs (Actions and apps) and commit statuses (older integrations). A check
// re-run keeps its name; the newest run of a name is the one that counts.
func (a *Adapter) MRChecks(ctx context.Context, mr ports.MRRef, head string) (ports.Checks, error) {
	out := ports.Checks{Head: head}
	if head == "" {
		return out, nil
	}
	var runs struct {
		CheckRuns []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"check_runs"`
	}
	if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/commits/%s/check-runs?per_page=100", a.repoPath(), url.PathEscape(head)), nil, &runs); err != nil {
		return out, err
	}
	newest := map[string]int{}
	for _, r := range runs.CheckRuns {
		state := ports.CheckPending
		if r.Status == "completed" {
			switch r.Conclusion {
			case "success":
				state = ports.CheckSuccess
			case "failure", "timed_out", "action_required":
				state = ports.CheckFailure
			default: // neutral, cancelled, skipped, stale
				state = ports.CheckNeutral
			}
		}
		c := ports.Check{Name: r.Name, State: state, URL: r.HTMLURL}
		if i, seen := newest[r.Name]; seen {
			if r.ID > runs.CheckRuns[i].ID {
				out.Items[i] = c
			}
			continue
		}
		newest[r.Name] = len(out.Items)
		out.Items = append(out.Items, c)
	}
	var combined struct {
		Statuses []struct {
			Context   string `json:"context"`
			State     string `json:"state"`
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/commits/%s/status", a.repoPath(), url.PathEscape(head)), nil, &combined); err != nil {
		return out, err
	}
	for _, s := range combined.Statuses {
		state := ports.CheckPending
		switch s.State {
		case "success":
			state = ports.CheckSuccess
		case "failure", "error":
			state = ports.CheckFailure
		}
		out.Items = append(out.Items, ports.Check{Name: s.Context, State: state, URL: s.TargetURL})
	}
	out.State = ports.AggregateChecks(out.Items)
	return out, nil
}

// MRApproval reads the pull request's reviews: each reviewer's latest
// decision counts (a comment decides nothing, a dismissal undoes one). It is
// approved when someone approves and nobody still requests changes.
func (a *Adapter) MRApproval(ctx context.Context, mr ports.MRRef) (ports.Approval, error) {
	latest := map[string]string{}
	var order []string
	for page := 1; page <= 10; page++ {
		var reviews []struct {
			State string `json:"state"`
			User  struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d/reviews?per_page=100&page=%d", a.repoPath(), mr.IID, page), nil, &reviews); err != nil {
			return ports.Approval{}, err
		}
		for _, r := range reviews {
			switch r.State {
			case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
				if _, seen := latest[r.User.Login]; !seen {
					order = append(order, r.User.Login)
				}
				latest[r.User.Login] = r.State
			}
		}
		if len(reviews) < 100 {
			break
		}
	}
	var out ports.Approval
	blocked := false
	for _, who := range order {
		switch latest[who] {
		case "APPROVED":
			out.By = append(out.By, who)
		case "CHANGES_REQUESTED":
			blocked = true
		}
	}
	out.Approved = len(out.By) > 0 && !blocked
	return out, nil
}

// graphqlURL is the GraphQL endpoint beside the REST base: api.github.com →
// api.github.com/graphql; GHE https://host/api/v3 → https://host/api/graphql.
func (a *Adapter) graphqlURL() string {
	if base, ok := strings.CutSuffix(a.cfg.BaseURL, "/api/v3"); ok {
		return base + "/api/graphql"
	}
	return a.cfg.BaseURL + "/graphql"
}

// graphql runs one GraphQL request; out receives the "data" member.
func (a *Adapter) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	data, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.graphqlURL(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("github graphql: %d: %s", resp.StatusCode, tail(string(body), 300))
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("github graphql: decode: %w", err)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("github graphql: %s", env.Errors[0].Message)
	}
	if out != nil {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

const threadsQuery = `query($owner:String!,$name:String!,$n:Int!,$after:String){
  repository(owner:$owner,name:$name){pullRequest(number:$n){
    reviewThreads(first:50,after:$after){
      pageInfo{hasNextPage endCursor}
      nodes{id isResolved path line originalLine
        comments(first:100){nodes{databaseId body createdAt diffHunk author{login}}}}}}}}`

// reviewThreadPrefix and issueThreadPrefix mark threads with no place to
// reply in: a review's top-level body, and a PR conversation comment. Their
// replies are conversation comments carrying the thread's marker.
const (
	reviewThreadPrefix = "review:"
	issueThreadPrefix  = "issue:"
)

// conversationThread reports whether a thread id is answered with a
// conversation comment rather than in a review thread.
func conversationThread(id string) bool {
	return strings.HasPrefix(id, reviewThreadPrefix) || strings.HasPrefix(id, issueThreadPrefix)
}

// MRThreads lists every comment on the PR as threads: the review threads
// (GraphQL: REST exposes neither the grouping nor isResolved), each
// review's top-level body, and each conversation comment — the last two with
// the factory's replies to them attached. Comments from bot accounts (CI,
// dependency bots) are not review comments and are left out.
func (a *Adapter) MRThreads(ctx context.Context, mr ports.MRRef) ([]ports.ReviewThread, error) {
	owner, name, _ := strings.Cut(a.cfg.Locator, "/")
	var out []ports.ReviewThread
	var after any
	for page := 0; page < 20; page++ {
		var res struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []struct {
							ID           string `json:"id"`
							IsResolved   bool   `json:"isResolved"`
							Path         string `json:"path"`
							Line         *int   `json:"line"`
							OriginalLine *int   `json:"originalLine"`
							Comments     struct {
								Nodes []struct {
									DatabaseID int       `json:"databaseId"`
									Body       string    `json:"body"`
									CreatedAt  time.Time `json:"createdAt"`
									DiffHunk   string    `json:"diffHunk"`
									Author     *struct {
										Login string `json:"login"`
									} `json:"author"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		if err := a.graphql(ctx, threadsQuery, map[string]any{"owner": owner, "name": name, "n": mr.IID, "after": after}, &res); err != nil {
			return nil, err
		}
		rt := res.Repository.PullRequest.ReviewThreads
		for _, n := range rt.Nodes {
			th := ports.ReviewThread{ID: n.ID, Path: n.Path, Resolved: n.IsResolved}
			if n.Line != nil {
				th.Line = *n.Line
			} else if n.OriginalLine != nil {
				th.Line = *n.OriginalLine
			}
			for _, c := range n.Comments.Nodes {
				if th.DiffHunk == "" {
					th.DiffHunk = c.DiffHunk
				}
				author := ""
				if c.Author != nil {
					author = c.Author.Login
				}
				th.Comments = append(th.Comments, ports.ReviewComment{ID: fmt.Sprint(c.DatabaseID), Author: author, Body: c.Body, CreatedAt: c.CreatedAt})
			}
			if len(th.Comments) > 0 {
				out = append(out, th)
			}
		}
		if !rt.PageInfo.HasNextPage {
			break
		}
		after = rt.PageInfo.EndCursor
	}

	// Review bodies, and the conversation comments that answer them.
	type user struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	}
	var reviews []struct {
		ID          int       `json:"id"`
		Body        string    `json:"body"`
		State       string    `json:"state"`
		User        user      `json:"user"`
		SubmittedAt time.Time `json:"submitted_at"`
	}
	if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d/reviews?per_page=100", a.repoPath(), mr.IID), nil, &reviews); err != nil {
		return nil, err
	}
	var comments []struct {
		ID        int       `json:"id"`
		Body      string    `json:"body"`
		User      user      `json:"user"`
		CreatedAt time.Time `json:"created_at"`
	}
	if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/issues/%d/comments?per_page=100", a.repoPath(), mr.IID), nil, &comments); err != nil {
		return nil, err
	}
	// Factory replies to conversation threads, by the thread they answer.
	replies := map[string][]ports.ReviewComment{}
	for _, c := range comments {
		if id := ports.MarkerThread(c.Body); id != "" {
			replies[id] = append(replies[id], ports.ReviewComment{ID: fmt.Sprint(c.ID), Author: c.User.Login, Body: c.Body, CreatedAt: c.CreatedAt})
		}
	}
	for _, r := range reviews {
		if strings.TrimSpace(r.Body) == "" || r.User.Type == "Bot" || (r.State != "CHANGES_REQUESTED" && r.State != "COMMENTED") {
			continue
		}
		id := reviewThreadPrefix + fmt.Sprint(r.ID)
		th := ports.ReviewThread{ID: id, Comments: []ports.ReviewComment{{ID: fmt.Sprint(r.ID), Author: r.User.Login, Body: r.Body, CreatedAt: r.SubmittedAt}}}
		th.Comments = append(th.Comments, replies[id]...)
		out = append(out, th)
	}
	for _, c := range comments {
		if ports.IsFactoryReply(c.Body) || c.User.Type == "Bot" || strings.TrimSpace(c.Body) == "" {
			continue
		}
		id := issueThreadPrefix + fmt.Sprint(c.ID)
		th := ports.ReviewThread{ID: id, Comments: []ports.ReviewComment{{ID: fmt.Sprint(c.ID), Author: c.User.Login, Body: c.Body, CreatedAt: c.CreatedAt}}}
		th.Comments = append(th.Comments, replies[id]...)
		out = append(out, th)
	}
	return out, nil
}

// ReplyThread replies in a review thread (GraphQL), or — for a review body,
// which has no thread to reply in — posts a conversation comment.
func (a *Adapter) ReplyThread(ctx context.Context, mr ports.MRRef, threadID, body string) error {
	if conversationThread(threadID) {
		_, err := a.api(ctx, http.MethodPost, fmt.Sprintf("%s/issues/%d/comments", a.repoPath(), mr.IID), map[string]any{"body": body}, nil)
		return err
	}
	return a.graphql(ctx, `mutation($id:ID!,$body:String!){addPullRequestReviewThreadReply(input:{pullRequestReviewThreadId:$id,body:$body}){comment{id}}}`,
		map[string]any{"id": threadID, "body": body}, nil)
}

// ResolveThread resolves a review thread (GraphQL only). A review body has
// nothing to resolve.
func (a *Adapter) ResolveThread(ctx context.Context, mr ports.MRRef, threadID string) error {
	if conversationThread(threadID) {
		return nil // a conversation comment has nothing to resolve
	}
	return a.graphql(ctx, `mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{id}}}`, map[string]any{"id": threadID}, nil)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

var _ ports.RepoHost = (*Adapter)(nil)

// ghPull is the pulls API's shape, as far as MRInfo needs it.
type ghPull struct {
	Number   int     `json:"number"`
	HTMLURL  string  `json:"html_url"`
	Title    string  `json:"title"`
	Body     string  `json:"body"`
	State    string  `json:"state"`
	MergedAt *string `json:"merged_at"`
	Draft    bool    `json:"draft"`
	User     struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

func (p *ghPull) info() ports.MRInfo {
	st := ports.MROpen
	switch {
	case p.MergedAt != nil && *p.MergedAt != "":
		st = ports.MRMerged
	case p.State == "closed":
		st = ports.MRClosed
	}
	return ports.MRInfo{Ref: ports.MRRef{IID: p.Number, URL: p.HTMLURL}, Title: p.Title, Body: p.Body, Author: p.User.Login,
		State: st, Draft: p.Draft, Source: p.Head.Ref, Target: p.Base.Ref, Head: p.Head.SHA,
		HeadRef: fmt.Sprintf("refs/pull/%d/head", p.Number),
		Fork:    p.Head.Repo == nil || p.Head.Repo.FullName != p.Base.Repo.FullName}
}

// GetMR reads one pull request.
func (a *Adapter) GetMR(ctx context.Context, iid int) (ports.MRInfo, error) {
	var p ghPull
	if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d", a.repoPath(), iid), nil, &p); err != nil {
		return ports.MRInfo{}, err
	}
	return p.info(), nil
}

// ListMRs lists the open pull requests, newest first, up to 300.
func (a *Adapter) ListMRs(ctx context.Context) ([]ports.MRInfo, error) {
	var out []ports.MRInfo
	for page := 1; page <= 3; page++ {
		var ps []ghPull
		if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/pulls?state=open&sort=created&direction=desc&per_page=100&page=%d", a.repoPath(), page), nil, &ps); err != nil {
			return nil, err
		}
		for i := range ps {
			out = append(out, ps[i].info())
		}
		if len(ps) < 100 {
			break
		}
	}
	return out, nil
}

// Whoami is the token's login.
func (a *Adapter) Whoami(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if _, err := a.api(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

// MergeMR merges through the pulls API, pinned to the head when asked. GitHub
// says 405 for a pull request it will not merge (a draft, required checks or
// reviews missing, a conflict), 409 for a head that moved, 422 for a method
// the repository does not allow. Deleting the branch is a second call, and
// one already gone (the repository deletes merged branches) is fine.
func (a *Adapter) MergeMR(ctx context.Context, mr ports.MRRef, opts ports.MergeOptions) error {
	method := opts.Method
	if method == "" {
		method = ports.MergeMethodMerge
	}
	body := map[string]any{"merge_method": method}
	if opts.Head != "" {
		body["sha"] = opts.Head
	}
	code, err := a.api(ctx, http.MethodPut, fmt.Sprintf("%s/pulls/%d/merge", a.repoPath(), mr.IID), body, nil)
	if err != nil {
		switch code {
		case http.StatusMethodNotAllowed, http.StatusConflict, http.StatusUnprocessableEntity:
			return &ports.ErrMergeRefused{Reason: err.Error()}
		}
		return err
	}
	if !opts.DeleteBranch {
		return nil
	}
	var pr struct {
		Head struct {
			Ref  string `json:"ref"`
			Repo *struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	}
	if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d", a.repoPath(), mr.IID), nil, &pr); err != nil {
		return err
	}
	if pr.Head.Repo == nil || pr.Head.Repo.FullName != pr.Base.Repo.FullName || pr.Head.Ref == "" {
		return nil // a fork's branch is not this repository's to delete
	}
	code, err = a.api(ctx, http.MethodDelete, fmt.Sprintf("%s/git/refs/heads/%s", a.repoPath(), pr.Head.Ref), nil, nil)
	if err != nil && code != http.StatusNotFound && code != http.StatusUnprocessableEntity {
		return err
	}
	return nil
}

// PostReview posts one review — GitHub's batch of a body and line comments,
// all or nothing — after checking the marker is not already on one of the
// pull request's reviews. A note goes on its line only when that line is in
// the diff (GitHub refuses the whole review otherwise); the rest join the
// body. Always a COMMENT review: approving is a person's call.
func (a *Adapter) PostReview(ctx context.Context, mr ports.MRRef, rev ports.Review) (ports.ReviewPosted, error) {
	marker := ports.ReviewMarker(rev.ID, "review")
	for page := 1; page <= 10; page++ {
		var reviews []struct {
			Body string `json:"body"`
		}
		if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d/reviews?per_page=100&page=%d", a.repoPath(), mr.IID, page), nil, &reviews); err != nil {
			return ports.ReviewPosted{}, err
		}
		for _, r := range reviews {
			if strings.Contains(r.Body, marker) {
				return ports.ReviewPosted{Already: true}, nil
			}
		}
		if len(reviews) < 100 {
			break
		}
	}
	files := map[string]map[int]int{}
	for page := 1; page <= 30; page++ {
		var fs []struct {
			Filename string `json:"filename"`
			Patch    string `json:"patch"`
		}
		if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d/files?per_page=100&page=%d", a.repoPath(), mr.IID, page), nil, &fs); err != nil {
			return ports.ReviewPosted{}, err
		}
		for _, f := range fs {
			files[f.Filename] = ports.DiffLines(f.Patch)
		}
		if len(fs) < 100 {
			break
		}
	}
	comments := []map[string]any{}
	var folded []ports.ReviewNote
	for i, n := range rev.Notes {
		if _, ok := files[n.Path][n.Line]; !ok {
			folded = append(folded, n)
			continue
		}
		comments = append(comments, map[string]any{"path": n.Path, "line": n.Line, "side": "RIGHT",
			"body": strings.TrimSpace(n.Body) + "\n\n" + ports.ReviewMarker(rev.ID, fmt.Sprintf("note-%d", i))})
	}
	var parts []string
	for _, p := range []string{strings.TrimSpace(rev.Summary), ports.FoldedNotes(folded)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	body := map[string]any{"body": strings.Join(append(parts, marker), "\n\n"), "event": "COMMENT", "comments": comments}
	if rev.Head != "" {
		body["commit_id"] = rev.Head
	}
	if _, err := a.api(ctx, http.MethodPost, fmt.Sprintf("%s/pulls/%d/reviews", a.repoPath(), mr.IID), body, nil); err != nil {
		return ports.ReviewPosted{}, err
	}
	return ports.ReviewPosted{Inline: len(comments), Folded: len(folded)}, nil
}
