// Package gitlab adapts the RepoHost port to the GitLab REST API v4
// (DESIGN.md §0: REST, not glab). The engine holds one repo-scoped token;
// no GitLab types cross the port boundary.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
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
// project (DESIGN2 §4: RepoHostKind.RepoHost(cfg, repo) parses
// RepoRef.Locator), so the RepoRef arguments carry branch/URL context only.
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

// AddMRLabels adds labels with add_labels, which keeps the merge request's
// others; GitLab creates a project label it does not have yet.
func (a *Adapter) AddMRLabels(ctx context.Context, mr ports.MRRef, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	_, err := a.api(ctx, http.MethodPut, fmt.Sprintf("%s/merge_requests/%d", a.path(), mr.IID),
		map[string]any{"add_labels": strings.Join(labels, ",")}, nil)
	return err
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

// BranchURL reads the branch's web_url; a 404 is a branch not pushed yet.
func (a *Adapter) BranchURL(ctx context.Context, _ ports.RepoRef, branch string) (string, bool, error) {
	var br struct {
		WebURL string `json:"web_url"`
	}
	status, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/repository/branches/%s", a.path(), url.PathEscape(branch)), nil, &br)
	if status == http.StatusNotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return br.WebURL, br.WebURL != "", nil
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

// MRStatus maps GitLab's merge request to the port's status. The MRRef
// alone must suffice (§4), so the project id rides in via RepoRef at
// construction — the adapter is bound to one project.
func (a *Adapter) MRStatus(ctx context.Context, mr ports.MRRef) (ports.MRStatus, error) {
	var out struct {
		State               string `json:"state"`
		HasConflicts        bool   `json:"has_conflicts"`
		DetailedMergeStatus string `json:"detailed_merge_status"`
		SHA                 string `json:"sha"`
		TargetBranch        string `json:"target_branch"`
		Draft               bool   `json:"draft"`
	}
	if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/merge_requests/%d", a.path(), mr.IID), nil, &out); err != nil {
		return ports.MRStatus{}, err
	}
	st := ports.MRStatus{State: ports.MROpen, HeadSHA: out.SHA, Target: out.TargetBranch, Mergeable: mergeableOf(out.DetailedMergeStatus, out.HasConflicts)}
	switch out.State {
	case "merged":
		st.State = ports.MRMerged
	case "closed":
		st.State = ports.MRClosed
	}
	// "mergeable" is GitLab saying every check it applies passed: approvals,
	// pipeline, resolved discussions, not a draft, no conflict.
	st.Draft = out.Draft
	st.MergeableNow = st.State == ports.MROpen && out.DetailedMergeStatus == "mergeable"
	if st.Mergeable == ports.MergeConflicts && out.TargetBranch != "" {
		var br struct {
			Commit struct {
				ID string `json:"id"`
			} `json:"commit"`
		}
		if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/repository/branches/%s", a.path(), url.PathEscape(out.TargetBranch)), nil, &br); err != nil {
			return ports.MRStatus{}, err
		}
		st.TargetSHA = br.Commit.ID
	}
	return st, nil
}

// MRChecks reads the merge request's head pipeline — the one GitLab runs for
// its head commit — and, when it failed, which jobs did (a job allowed to
// fail is not a failure). A head pipeline for an older commit means the new
// head's has not been reported yet: pending.
func (a *Adapter) MRChecks(ctx context.Context, mr ports.MRRef, head string) (ports.Checks, error) {
	out := ports.Checks{Head: head}
	var m struct {
		HeadPipeline *struct {
			ID     int    `json:"id"`
			SHA    string `json:"sha"`
			Status string `json:"status"`
			WebURL string `json:"web_url"`
		} `json:"head_pipeline"`
	}
	if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/merge_requests/%d", a.path(), mr.IID), nil, &m); err != nil {
		return out, err
	}
	p := m.HeadPipeline
	if p == nil {
		return out, nil
	}
	pipe := ports.Check{Name: "pipeline", State: pipelineState(p.Status), URL: p.WebURL}
	if head != "" && p.SHA != head {
		pipe.State = ports.CheckPending
	}
	out.Items = []ports.Check{pipe}
	if pipe.State == ports.CheckFailure {
		var jobs []struct {
			Name         string `json:"name"`
			WebURL       string `json:"web_url"`
			AllowFailure bool   `json:"allow_failure"`
		}
		if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/pipelines/%d/jobs?scope[]=failed&per_page=100", a.path(), p.ID), nil, &jobs); err != nil {
			return out, err
		}
		var failed []ports.Check
		for _, j := range jobs {
			if !j.AllowFailure {
				failed = append(failed, ports.Check{Name: j.Name, State: ports.CheckFailure, URL: j.WebURL})
			}
		}
		if len(failed) > 0 {
			out.Items = failed
		}
	}
	out.State = ports.AggregateChecks(out.Items)
	return out, nil
}

// CIFailures lists the failed jobs of head's pipeline — a job allowed to fail
// is not a failure — each with its stage and the CI file that declares it
// (the project's ci_config_path, .gitlab-ci.yml by default; a job from an
// included file is still named there, by its job name).
func (a *Adapter) CIFailures(ctx context.Context, mr ports.MRRef, head string) ([]ports.CIFailure, error) {
	var m struct {
		HeadPipeline *struct {
			ID     int    `json:"id"`
			SHA    string `json:"sha"`
			Status string `json:"status"`
			WebURL string `json:"web_url"`
		} `json:"head_pipeline"`
	}
	if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/merge_requests/%d", a.path(), mr.IID), nil, &m); err != nil {
		return nil, err
	}
	p := m.HeadPipeline
	if p == nil || (head != "" && p.SHA != head) || p.Status != "failed" {
		return nil, nil
	}
	var proj struct {
		CIConfigPath string `json:"ci_config_path"`
	}
	if _, err := a.api(ctx, http.MethodGet, a.path(), nil, &proj); err != nil {
		return nil, err
	}
	file := proj.CIConfigPath
	if file == "" {
		file = ".gitlab-ci.yml"
	}
	var jobs []struct {
		Name          string `json:"name"`
		Stage         string `json:"stage"`
		WebURL        string `json:"web_url"`
		AllowFailure  bool   `json:"allow_failure"`
		FailureReason string `json:"failure_reason"`
	}
	if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/pipelines/%d/jobs?scope[]=failed&per_page=100", a.path(), p.ID), nil, &jobs); err != nil {
		return nil, err
	}
	var out []ports.CIFailure
	for _, j := range jobs {
		if j.AllowFailure {
			continue
		}
		out = append(out, ports.CIFailure{Name: j.Name, URL: j.WebURL, File: file, Job: j.Name, Stage: j.Stage,
			Summary: strings.ReplaceAll(j.FailureReason, "_", " ")})
	}
	if len(out) == 0 {
		// Failed with no job of its own failing: a configuration error, say.
		out = append(out, ports.CIFailure{Name: "pipeline", URL: p.WebURL, File: file})
	}
	return out, nil
}

// pipelineState maps a GitLab pipeline status onto a check state.
func pipelineState(status string) ports.CheckState {
	switch status {
	case "success":
		return ports.CheckSuccess
	case "failed":
		return ports.CheckFailure
	case "canceled", "skipped", "manual":
		return ports.CheckNeutral
	}
	return ports.CheckPending // created, waiting_for_resource, preparing, pending, running, scheduled
}

// MRApproval reads the merge request's approval state. A project without
// approval rules reports approved with nobody's approval; that is not an
// approval, so at least one person must have given one.
func (a *Adapter) MRApproval(ctx context.Context, mr ports.MRRef) (ports.Approval, error) {
	var out struct {
		Approved   bool `json:"approved"`
		ApprovedBy []struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		} `json:"approved_by"`
	}
	if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/merge_requests/%d/approvals", a.path(), mr.IID), nil, &out); err != nil {
		return ports.Approval{}, err
	}
	var ap ports.Approval
	for _, b := range out.ApprovedBy {
		ap.By = append(ap.By, b.User.Username)
	}
	ap.Approved = out.Approved && len(ap.By) > 0
	return ap, nil
}

// mergeableOf reads GitLab's merge status. has_conflicts is only meaningful
// once GitLab has checked: while it is checking it may still describe the
// previous head, so those states are unknown rather than a guess.
func mergeableOf(detailed string, hasConflicts bool) ports.Mergeable {
	switch detailed {
	case "checking", "unchecked", "preparing", "approvals_syncing":
		return ports.MergeUnknown
	case "conflict":
		return ports.MergeConflicts
	}
	if hasConflicts {
		return ports.MergeConflicts
	}
	return ports.MergeClean
}

// MRThreads lists the merge request's discussions: inline threads and
// general comments, system notes (pushes, label changes) dropped.
func (a *Adapter) MRThreads(ctx context.Context, mr ports.MRRef) ([]ports.ReviewThread, error) {
	type note struct {
		ID     int    `json:"id"`
		Body   string `json:"body"`
		System bool   `json:"system"`
		Author struct {
			Username string `json:"username"`
		} `json:"author"`
		CreatedAt  time.Time `json:"created_at"`
		Resolvable bool      `json:"resolvable"`
		Resolved   bool      `json:"resolved"`
		Position   *struct {
			NewPath string `json:"new_path"`
			OldPath string `json:"old_path"`
			NewLine int    `json:"new_line"`
			OldLine int    `json:"old_line"`
		} `json:"position"`
	}
	var out []ports.ReviewThread
	for page := 1; page <= 50; page++ {
		var ds []struct {
			ID    string `json:"id"`
			Notes []note `json:"notes"`
		}
		p := fmt.Sprintf("%s/merge_requests/%d/discussions?per_page=100&page=%d", a.path(), mr.IID, page)
		if _, err := a.api(ctx, http.MethodGet, p, nil, &ds); err != nil {
			return nil, err
		}
		for _, d := range ds {
			th := ports.ReviewThread{ID: d.ID}
			resolvable, resolved := false, true
			for _, n := range d.Notes {
				if n.System {
					continue
				}
				if n.Position != nil && th.Path == "" {
					th.Path, th.Line = n.Position.NewPath, n.Position.NewLine
					if th.Path == "" {
						th.Path, th.Line = n.Position.OldPath, n.Position.OldLine
					}
				}
				if n.Resolvable {
					resolvable = true
					resolved = resolved && n.Resolved
				}
				th.Comments = append(th.Comments, ports.ReviewComment{ID: fmt.Sprint(n.ID), Author: n.Author.Username, Body: n.Body, CreatedAt: n.CreatedAt})
			}
			if len(th.Comments) == 0 {
				continue
			}
			th.Resolved = resolvable && resolved
			out = append(out, th)
		}
		if len(ds) < 100 {
			break
		}
	}
	return out, nil
}

// ReplyThread adds a note to a discussion.
func (a *Adapter) ReplyThread(ctx context.Context, mr ports.MRRef, threadID, body string) error {
	_, err := a.api(ctx, http.MethodPost,
		fmt.Sprintf("%s/merge_requests/%d/discussions/%s/notes", a.path(), mr.IID, url.PathEscape(threadID)),
		map[string]any{"body": body}, nil)
	return err
}

// ResolveThread resolves a discussion.
func (a *Adapter) ResolveThread(ctx context.Context, mr ports.MRRef, threadID string) error {
	_, err := a.api(ctx, http.MethodPut,
		fmt.Sprintf("%s/merge_requests/%d/discussions/%s?resolved=true", a.path(), mr.IID, url.PathEscape(threadID)), nil, nil)
	return err
}

// UploadAttachment uploads a file to the project (POST /projects/:id/uploads)
// for a description or a note to show. GitLab answers with a
// project-relative url and the markdown for it — what its renderer resolves
// inside that project, and so what the body uses — and, on recent versions,
// a full_path; URL is the absolute address either way.
func (a *Adapter) UploadAttachment(ctx context.Context, _ ports.RepoRef, name, contentType string, data []byte) (ports.Attachment, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, quoteEscaper.Replace(name)))
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err == nil {
		_, err = part.Write(data)
	}
	if err == nil {
		err = mw.Close()
	}
	if err != nil {
		return ports.Attachment{}, err
	}
	path := a.path() + "/uploads"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.BaseURL+"/api/v4"+path, &buf)
	if err != nil {
		return ports.Attachment{}, err
	}
	req.Header.Set("PRIVATE-TOKEN", a.cfg.Token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := a.http.Do(req)
	if err != nil {
		return ports.Attachment{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return ports.Attachment{}, fmt.Errorf("gitlab POST %s: %d: %s", path, resp.StatusCode, tail(string(body), 300))
	}
	var out struct {
		URL      string `json:"url"`
		FullPath string `json:"full_path"`
		Markdown string `json:"markdown"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return ports.Attachment{}, fmt.Errorf("gitlab POST %s: decode: %w", path, err)
	}
	if out.URL == "" {
		return ports.Attachment{}, fmt.Errorf("gitlab POST %s: the upload has no url", path)
	}
	abs := out.FullPath
	if abs == "" {
		abs = fmt.Sprintf("/-/project/%d%s", a.projectID, out.URL)
	}
	return ports.Attachment{URL: a.cfg.BaseURL + abs, Link: out.URL, Markdown: out.Markdown}, nil
}

// quoteEscaper escapes a multipart filename, as mime/multipart does.
var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

var _ ports.RepoHost = (*Adapter)(nil)

// glMR is the merge requests API's shape, as far as MRInfo needs it.
type glMR struct {
	IID             int    `json:"iid"`
	WebURL          string `json:"web_url"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	State           string `json:"state"`
	Draft           bool   `json:"draft"`
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`
	SHA             string `json:"sha"`
	SourceProjectID int    `json:"source_project_id"`
	TargetProjectID int    `json:"target_project_id"`
	Author          struct {
		Username string `json:"username"`
	} `json:"author"`
}

func (m *glMR) info() ports.MRInfo {
	st := ports.MROpen
	switch m.State {
	case "merged":
		st = ports.MRMerged
	case "closed":
		st = ports.MRClosed
	}
	return ports.MRInfo{Ref: ports.MRRef{IID: m.IID, URL: m.WebURL}, Title: m.Title, Body: m.Description, Author: m.Author.Username,
		State: st, Draft: m.Draft, Source: m.SourceBranch, Target: m.TargetBranch, Head: m.SHA,
		HeadRef: fmt.Sprintf("refs/merge-requests/%d/head", m.IID),
		Fork:    m.SourceProjectID != 0 && m.SourceProjectID != m.TargetProjectID}
}

// GetMR reads one merge request.
func (a *Adapter) GetMR(ctx context.Context, iid int) (ports.MRInfo, error) {
	var m glMR
	if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/merge_requests/%d", a.path(), iid), nil, &m); err != nil {
		return ports.MRInfo{}, err
	}
	return m.info(), nil
}

// ListMRs lists the open merge requests, newest first, up to 300.
func (a *Adapter) ListMRs(ctx context.Context) ([]ports.MRInfo, error) {
	var out []ports.MRInfo
	for page := 1; page <= 3; page++ {
		var ms []glMR
		if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/merge_requests?state=opened&order_by=created_at&sort=desc&per_page=100&page=%d", a.path(), page), nil, &ms); err != nil {
			return nil, err
		}
		for i := range ms {
			out = append(out, ms[i].info())
		}
		if len(ms) < 100 {
			break
		}
	}
	return out, nil
}

// Whoami is the token's username.
func (a *Adapter) Whoami(ctx context.Context) (string, error) {
	var u struct {
		Username string `json:"username"`
	}
	if _, err := a.api(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return "", err
	}
	return u.Username, nil
}

// MergeMR merges through the merge requests API, pinned to the head when
// asked. GitLab merges by the project's merge method (merge commit,
// semi-linear, fast-forward): squash is per merge, a rebase is not — a
// project wanting a linear history sets fast-forward, so `rebase` is
// refused here rather than quietly merged another way. 405/406 are GitLab
// declining (a draft, a pipeline or approvals missing, a conflict), 409 a
// head that moved.
func (a *Adapter) MergeMR(ctx context.Context, mr ports.MRRef, opts ports.MergeOptions) error {
	if opts.Method == ports.MergeMethodRebase {
		return &ports.ErrMergeRefused{Reason: "GitLab merges by the project's merge method — set it to fast-forward for a linear history, and merge with method merge or squash"}
	}
	body := map[string]any{"squash": opts.Method == ports.MergeMethodSquash, "should_remove_source_branch": opts.DeleteBranch}
	if opts.Head != "" {
		body["sha"] = opts.Head
	}
	code, err := a.api(ctx, http.MethodPut, fmt.Sprintf("%s/merge_requests/%d/merge", a.path(), mr.IID), body, nil)
	if err != nil {
		switch code {
		case http.StatusMethodNotAllowed, http.StatusNotAcceptable, http.StatusConflict, http.StatusUnprocessableEntity:
			return &ports.ErrMergeRefused{Reason: err.Error()}
		}
		return err
	}
	return nil
}

// PostReview posts each note as a discussion on its line and the summary as
// a note — GitLab has no batch for both — skipping every part whose marker
// is already on the merge request, so a retry posts only what is missing. A
// note whose line is not in the diff, or that GitLab will not anchor, joins
// the summary.
func (a *Adapter) PostReview(ctx context.Context, mr ports.MRRef, rev ports.Review) (ports.ReviewPosted, error) {
	threads, err := a.MRThreads(ctx, mr)
	if err != nil {
		return ports.ReviewPosted{}, err
	}
	var existing []string
	for _, t := range threads {
		for _, c := range t.Comments {
			existing = append(existing, c.Body)
		}
	}
	posted := func(marker string) bool {
		for _, b := range existing {
			if strings.Contains(b, marker) {
				return true
			}
		}
		return false
	}
	var m struct {
		DiffRefs struct {
			BaseSHA  string `json:"base_sha"`
			HeadSHA  string `json:"head_sha"`
			StartSHA string `json:"start_sha"`
		} `json:"diff_refs"`
	}
	if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/merge_requests/%d", a.path(), mr.IID), nil, &m); err != nil {
		return ports.ReviewPosted{}, err
	}
	type diffFile struct {
		oldPath string
		lines   map[int]int
	}
	files := map[string]diffFile{}
	for page := 1; page <= 30; page++ {
		var ds []struct {
			OldPath string `json:"old_path"`
			NewPath string `json:"new_path"`
			Diff    string `json:"diff"`
		}
		if _, err := a.api(ctx, http.MethodGet, fmt.Sprintf("%s/merge_requests/%d/diffs?per_page=100&page=%d", a.path(), mr.IID, page), nil, &ds); err != nil {
			return ports.ReviewPosted{}, err
		}
		for _, d := range ds {
			files[d.NewPath] = diffFile{oldPath: d.OldPath, lines: ports.DiffLines(d.Diff)}
		}
		if len(ds) < 100 {
			break
		}
	}
	var out ports.ReviewPosted
	already := true
	var folded []ports.ReviewNote
	for i, n := range rev.Notes {
		marker := ports.NoteMarker(rev.ID, i, n.Kind)
		f, ok := files[n.Path]
		old, inDiff := f.lines[n.Line]
		if !ok || !inDiff {
			folded = append(folded, n)
			continue
		}
		if posted(marker) {
			out.Inline++
			continue
		}
		already = false
		pos := map[string]any{"position_type": "text", "base_sha": m.DiffRefs.BaseSHA, "start_sha": m.DiffRefs.StartSHA,
			"head_sha": m.DiffRefs.HeadSHA, "new_path": n.Path, "old_path": f.oldPath, "new_line": n.Line}
		if old > 0 {
			pos["old_line"] = old
		}
		code, err := a.api(ctx, http.MethodPost, fmt.Sprintf("%s/merge_requests/%d/discussions", a.path(), mr.IID),
			map[string]any{"body": strings.TrimSpace(n.Body) + "\n\n" + marker, "position": pos}, nil)
		if err != nil {
			if code == http.StatusBadRequest || code == http.StatusUnprocessableEntity {
				folded = append(folded, n) // GitLab would not anchor it there
				continue
			}
			return ports.ReviewPosted{}, err
		}
		out.Inline++
	}
	out.Folded = len(folded)
	var parts []string
	for _, p := range []string{strings.TrimSpace(rev.Summary), ports.FoldedNotes(folded)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if marker := ports.ReviewMarker(rev.ID, "summary"); len(parts) > 0 && !posted(marker) {
		already = false
		if _, err := a.api(ctx, http.MethodPost, fmt.Sprintf("%s/merge_requests/%d/notes", a.path(), mr.IID),
			map[string]any{"body": strings.Join(append(parts, marker), "\n\n")}, nil); err != nil {
			return ports.ReviewPosted{}, err
		}
	}
	out.Already = already
	return out, nil
}
