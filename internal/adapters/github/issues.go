package github

// Issues adapts the ticketsource port — read side for the import picker, write
// side (trackerport.TicketWriter) for a flow's tracker actions — to GitHub Issues,
// reusing the repository host adapter's authenticated client. An issue's key
// is "owner/repo#123": GitHub has no site-wide key, so the repository is part
// of the identity. GitHub issues have two states and no custom fields; what
// the write port cannot express is refused by name (Projects v2 columns are
// not reachable through the issue itself).

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
)

type IssuesConfig struct {
	BaseURL string
	Token   string
	// Repos narrows search to these "owner/repo" repositories (the Jira
	// adapter's JQL scope); empty searches everything the token sees. A
	// bare "#123" key resolves against the one repository listed.
	Repos []string
}

type Issues struct {
	a     *Adapter
	repos []string
}

func NewIssues(cfg IssuesConfig) *Issues {
	var repos []string
	for _, r := range cfg.Repos {
		if r = strings.TrimSuffix(strings.TrimSpace(r), ".git"); r != "" {
			repos = append(repos, r)
		}
	}
	return &Issues{a: New(Config{BaseURL: cfg.BaseURL, Token: cfg.Token}), repos: repos}
}

var issueKeyRe = regexp.MustCompile(`^(?:([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+))?#?(\d+)$`)

// parseKey splits "owner/repo#123" (or "#123" / "123" with one configured
// repo) into its repository and number.
func (s *Issues) parseKey(key string) (repo string, n int, err error) {
	m := issueKeyRe.FindStringSubmatch(strings.TrimSpace(key))
	if m == nil {
		return "", 0, fmt.Errorf("github: issue key %q is not owner/repo#number", key)
	}
	repo = m[1]
	if repo == "" {
		if len(s.repos) != 1 {
			return "", 0, fmt.Errorf("github: issue key %q names no repository (use owner/repo#%s)", key, m[2])
		}
		repo = s.repos[0]
	}
	n, _ = strconv.Atoi(m[2])
	return repo, n, nil
}

func issuePath(repo string, n int) string { return fmt.Sprintf("/repos/%s/issues/%d", repo, n) }

// Test verifies the token authenticates (GET /user).
func (s *Issues) Test(ctx context.Context) error {
	if s.a.cfg.Token == "" {
		return fmt.Errorf("github: no token configured")
	}
	var user struct {
		Login string `json:"login"`
	}
	_, err := s.a.api(ctx, http.MethodGet, "/user", nil, &user)
	return err
}

type issueJSON struct {
	Number        int    `json:"number"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	HTMLURL       string `json:"html_url"`
	State         string `json:"state"`
	UpdatedAt     string `json:"updated_at"`
	RepositoryURL string `json:"repository_url"`
	PullRequest   *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
	Assignee *struct {
		Login string `json:"login"`
	} `json:"assignee"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Milestone *struct {
		Title  string `json:"title"`
		Number int    `json:"number"`
	} `json:"milestone"`
}

// repoOf recovers "owner/repo" from the issue's repository_url
// ("https://api.github.com/repos/owner/repo").
func repoOf(in issueJSON, fallback string) string {
	if i := strings.Index(in.RepositoryURL, "/repos/"); i >= 0 {
		return strings.Trim(in.RepositoryURL[i+len("/repos/"):], "/")
	}
	return fallback
}

func toTicket(in issueJSON, repo string) trackerport.SourceTicket {
	t := trackerport.SourceTicket{
		Key:    fmt.Sprintf("%s#%d", repo, in.Number),
		Title:  in.Title,
		Body:   strings.TrimSpace(in.Body),
		URL:    in.HTMLURL,
		Status: in.State,
		Type:   "issue",
	}
	if in.Assignee != nil {
		t.Assignee = in.Assignee.Login
	}
	if ts, err := time.Parse(time.RFC3339, in.UpdatedAt); err == nil {
		t.Updated = ts.UTC()
	}
	return t
}

// Search runs GitHub's issue search over the configured repositories: an
// issue key looks the issue up directly, empty means "open and assigned to
// me", anything else is a text search. Pull requests (which the issues API
// also returns) are dropped.
func (s *Issues) Search(ctx context.Context, query string, limit int) ([]trackerport.SourceTicket, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	query = strings.TrimSpace(query)
	if issueKeyRe.MatchString(query) {
		if t, err := s.Fetch(ctx, query); err == nil {
			return []trackerport.SourceTicket{t}, nil
		}
	}
	q := "is:issue"
	for _, r := range s.repos {
		q += " repo:" + r
	}
	if query == "" {
		q += " state:open assignee:@me"
	} else {
		q += " " + query
	}
	var out struct {
		Items []issueJSON `json:"items"`
	}
	p := fmt.Sprintf("/search/issues?q=%s&per_page=%d&sort=updated&order=desc", url.QueryEscape(q), limit)
	if _, err := s.a.api(ctx, http.MethodGet, p, nil, &out); err != nil {
		return nil, err
	}
	var tickets []trackerport.SourceTicket
	for _, it := range out.Items {
		if it.PullRequest != nil {
			continue
		}
		tickets = append(tickets, toTicket(it, repoOf(it, "")))
	}
	return tickets, nil
}

// Fetch returns one issue; a pull request behind the same number is refused.
func (s *Issues) Fetch(ctx context.Context, key string) (trackerport.SourceTicket, error) {
	repo, n, err := s.parseKey(key)
	if err != nil {
		return trackerport.SourceTicket{}, err
	}
	var it issueJSON
	if _, err := s.a.api(ctx, http.MethodGet, issuePath(repo, n), nil, &it); err != nil {
		return trackerport.SourceTicket{}, err
	}
	if it.PullRequest != nil {
		return trackerport.SourceTicket{}, fmt.Errorf("github: %s#%d is a pull request, not an issue", repo, n)
	}
	return toTicket(it, repoOf(it, repo)), nil
}

// OpenAttachment: GitHub issues carry no attachments as such (files in a
// body are plain URLs the agent can read), so there is never one to open.
func (s *Issues) OpenAttachment(context.Context, trackerport.SourceAttachment) (io.ReadCloser, error) {
	return nil, fmt.Errorf("github: issues have no attachments")
}

// --- write side (trackerport.TicketWriter) ------------------------------------------

var issueStates = []string{"open", "closed"}

// issueStatusMap is what the canonical statuses mean for an issue: todo is
// open, done is closed, and nothing else has a word (Projects v2 columns are
// not reachable through the issue itself).
var issueStatusMap = map[trackerport.TicketStatus]string{trackerport.StatusTodo: "open", trackerport.StatusDone: "closed"}

// TransitionTo reopens (todo) or closes (done) the issue; every other
// canonical status is unsupported — the engine skips the stage.
func (s *Issues) TransitionTo(ctx context.Context, key string, to trackerport.TicketStatus) error {
	state, ok := issueStatusMap[to]
	if !ok {
		return &trackerport.ErrStatusUnsupported{Key: strings.TrimSpace(key), Status: to, Kind: "GitHub Issues",
			Supported: []trackerport.TicketStatus{trackerport.StatusTodo, trackerport.StatusDone}}
	}
	return s.Transition(ctx, key, state)
}

// Transition: an issue is open or closed, nothing else. Anything else a flow
// asks for is unreachable, and the error says what is.
func (s *Issues) Transition(ctx context.Context, key, status string) error {
	repo, n, err := s.parseKey(key)
	if err != nil {
		return err
	}
	want := strings.ToLower(strings.TrimSpace(status))
	var it issueJSON
	if _, err := s.a.api(ctx, http.MethodGet, issuePath(repo, n), nil, &it); err != nil {
		return err
	}
	if want != "open" && want != "closed" {
		return &trackerport.ErrStatusUnreachable{Key: key, From: it.State, To: status, Reachable: issueStates}
	}
	if it.State == want {
		return nil
	}
	body := map[string]any{"state": want}
	if want == "closed" {
		body["state_reason"] = "completed"
	}
	_, err = s.a.api(ctx, http.MethodPatch, issuePath(repo, n), body, nil)
	return err
}

func (s *Issues) Comment(ctx context.Context, key, body string) error {
	repo, n, err := s.parseKey(key)
	if err != nil {
		return err
	}
	_, err = s.a.api(ctx, http.MethodPost, issuePath(repo, n)+"/comments", map[string]any{"body": body}, nil)
	return err
}

func (s *Issues) Comments(ctx context.Context, key string) ([]string, error) {
	repo, n, err := s.parseKey(key)
	if err != nil {
		return nil, err
	}
	var out []string
	for page := 1; page < 100; page++ {
		var rows []struct {
			Body string `json:"body"`
		}
		p := fmt.Sprintf("%s/comments?per_page=100&page=%d", issuePath(repo, n), page)
		if _, err := s.a.api(ctx, http.MethodGet, p, nil, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, r.Body)
		}
		if len(rows) < 100 {
			break
		}
	}
	return out, nil
}

// issueFields are the only writable "fields" an issue has.
var issueFields = []string{"labels", "assignees", "milestone"}

// SetFields writes labels (replacing the set), assignees or the milestone
// (by title). The engine's append mode merges before calling, so replacing
// is the right primitive here.
func (s *Issues) SetFields(ctx context.Context, key string, fields map[string]string) error {
	repo, n, err := s.parseKey(key)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	patch := map[string]any{}
	for _, name := range names {
		items := splitItems(fields[name])
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "labels":
			if items == nil {
				items = []string{}
			}
			patch["labels"] = items
		case "assignees":
			if items == nil {
				items = []string{}
			}
			patch["assignees"] = items
		case "milestone":
			title := strings.TrimSpace(fields[name])
			if title == "" {
				patch["milestone"] = nil
				continue
			}
			num, err := s.milestone(ctx, repo, title)
			if err != nil {
				return err
			}
			patch["milestone"] = num
		default:
			return &trackerport.ErrFieldUnsupported{Key: key, Field: name, Supported: issueFields}
		}
	}
	if len(patch) == 0 {
		return nil
	}
	_, err = s.a.api(ctx, http.MethodPatch, issuePath(repo, n), patch, nil)
	return err
}

// milestone resolves a milestone title to its number.
func (s *Issues) milestone(ctx context.Context, repo, title string) (int, error) {
	var rows []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	}
	if _, err := s.a.api(ctx, http.MethodGet, "/repos/"+repo+"/milestones?state=all&per_page=100", nil, &rows); err != nil {
		return 0, err
	}
	var titles []string
	for _, m := range rows {
		if strings.EqualFold(m.Title, title) {
			return m.Number, nil
		}
		titles = append(titles, m.Title)
	}
	return 0, &trackerport.ErrFieldUnknown{Key: repo, Field: "milestone " + title, Candidates: titles}
}

func (s *Issues) Fields(ctx context.Context, key string, names []string) (map[string]string, error) {
	repo, n, err := s.parseKey(key)
	if err != nil {
		return nil, err
	}
	var it issueJSON
	if _, err := s.a.api(ctx, http.MethodGet, issuePath(repo, n), nil, &it); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, name := range names {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "labels":
			var ls []string
			for _, l := range it.Labels {
				ls = append(ls, l.Name)
			}
			out[name] = strings.Join(ls, ", ")
		case "assignees":
			var as []string
			for _, a := range it.Assignees {
				as = append(as, a.Login)
			}
			out[name] = strings.Join(as, ", ")
		case "milestone":
			if it.Milestone != nil {
				out[name] = it.Milestone.Title
			} else {
				out[name] = ""
			}
		default:
			return nil, &trackerport.ErrFieldUnsupported{Key: key, Field: name, Supported: issueFields}
		}
	}
	return out, nil
}

// linkMarker is the invisible tag a link comment carries, so a re-entry
// finds the link it already posted instead of posting it again.
func linkMarker(u string) string { return "<!-- apothecary link=" + u + " -->" }

// AddLink posts the link as a comment (GitHub autolinks pull requests), once:
// an existing comment carrying the marker is the link already attached.
func (s *Issues) AddLink(ctx context.Context, key string, link trackerport.TicketLink) error {
	if strings.TrimSpace(link.URL) == "" {
		return fmt.Errorf("github: a link needs a URL")
	}
	existing, err := s.Comments(ctx, key)
	if err != nil {
		return err
	}
	marker := linkMarker(link.URL)
	for _, c := range existing {
		if strings.Contains(c, marker) {
			return nil
		}
	}
	title := strings.TrimSpace(link.Title)
	if title == "" {
		title = "Link"
	}
	return s.Comment(ctx, key, fmt.Sprintf("%s: %s\n\n%s", title, link.URL, marker))
}

// Attach uploads a file for a comment on the issue: GitHub keeps no files
// on an issue, so it goes where a pasted screenshot does — user-attachments,
// for the issue's repository (Adapter.uploadAttachment).
func (s *Issues) Attach(ctx context.Context, key, name, contentType string, data []byte) (repoport.Attachment, error) {
	repo, _, err := s.parseKey(key)
	if err != nil {
		return repoport.Attachment{}, err
	}
	return s.a.uploadAttachment(ctx, repo, name, contentType, data)
}

func splitItems(value string) []string {
	var out []string
	for part := range strings.SplitSeq(value, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var (
	_ trackerport.TicketSource   = (*Issues)(nil)
	_ trackerport.TicketWriter   = (*Issues)(nil)
	_ trackerport.TicketAttacher = (*Issues)(nil)
)
