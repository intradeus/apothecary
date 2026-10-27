package gitlab

// Issues adapts the ticketsource port — read side for the import picker, write
// side (ports.TicketWriter) for a flow's tracker actions — to GitLab issues,
// reusing the repository host adapter's authenticated client. GitLab is moving
// issues onto its "work items" framework, whose own API is GraphQL; the REST
// issues API still serves every issue-type work item (issues, incidents,
// tasks) and is what the rest of this package speaks, so it is what this
// speaks too. An issue's key is GitLab's own full reference,
// "group/project#123": an iid is only unique within its project. An issue is
// opened or closed and has no custom fields; what the write port cannot
// express is refused by name.

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

	"apothecary/internal/ports"
)

type IssuesConfig struct {
	BaseURL string
	Token   string
	// Projects and Groups narrow search to these paths ("group/project",
	// "group/sub"); both empty searches every issue the token sees. A bare
	// "#123" key resolves against the one project listed.
	Projects []string
	Groups   []string
}

type Issues struct {
	a        *Adapter
	projects []string
	groups   []string
}

func NewIssues(cfg IssuesConfig) *Issues {
	return &Issues{
		a:        New(Config{BaseURL: cfg.BaseURL, Token: cfg.Token}),
		projects: cleanPaths(cfg.Projects),
		groups:   cleanPaths(cfg.Groups),
	}
}

func cleanPaths(in []string) []string {
	var out []string
	for _, p := range in {
		if p = strings.Trim(strings.TrimSuffix(strings.TrimSpace(p), ".git"), "/"); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var issueKeyRe = regexp.MustCompile(`^(?:([A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+))?#?(\d+)$`)

// parseKey splits "group/project#123" (or "#123" / "123" with one configured
// project) into its project path and iid.
func (s *Issues) parseKey(key string) (project string, iid int, err error) {
	m := issueKeyRe.FindStringSubmatch(strings.TrimSpace(key))
	if m == nil {
		return "", 0, fmt.Errorf("gitlab: issue key %q is not group/project#number", key)
	}
	project = m[1]
	if project == "" {
		if len(s.projects) != 1 {
			return "", 0, fmt.Errorf("gitlab: issue key %q names no project (use group/project#%s)", key, m[2])
		}
		project = s.projects[0]
	}
	iid, _ = strconv.Atoi(m[2])
	return project, iid, nil
}

func projectPath(project string) string { return "/projects/" + url.PathEscape(project) }

func issuePath(project string, iid int) string {
	return fmt.Sprintf("%s/issues/%d", projectPath(project), iid)
}

// Test verifies the token authenticates (GET /user).
func (s *Issues) Test(ctx context.Context) error {
	if s.a.cfg.Token == "" {
		return fmt.Errorf("gitlab: no token configured")
	}
	var user struct {
		Username string `json:"username"`
	}
	_, err := s.a.api(ctx, http.MethodGet, "/user", nil, &user)
	return err
}

type issueJSON struct {
	IID         int      `json:"iid"`
	ProjectID   int      `json:"project_id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	WebURL      string   `json:"web_url"`
	State       string   `json:"state"`
	IssueType   string   `json:"issue_type"`
	UpdatedAt   string   `json:"updated_at"`
	Labels      []string `json:"labels"`
	References  struct {
		Full string `json:"full"`
	} `json:"references"`
	Assignee *struct {
		Username string `json:"username"`
	} `json:"assignee"`
	Assignees []struct {
		Username string `json:"username"`
	} `json:"assignees"`
	Milestone *struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
	} `json:"milestone"`
}

// projectOf recovers the issue's project path: from its full reference
// ("group/project#5"), else from its web_url ("…/group/project/-/issues/5").
func (s *Issues) projectOf(in issueJSON, fallback string) string {
	if i := strings.LastIndex(in.References.Full, "#"); i > 0 {
		return in.References.Full[:i]
	}
	if u, err := url.Parse(in.WebURL); err == nil {
		if i := strings.Index(u.Path, "/-/"); i > 0 {
			return strings.Trim(u.Path[:i], "/")
		}
	}
	return fallback
}

// uploadRe finds a file uploaded into a description: GitLab writes it as a
// project-relative markdown target, "/uploads/<secret>/<filename>".
var uploadRe = regexp.MustCompile(`\]\((/uploads/([0-9a-f]{32})/([^)\s]+))\)`)

func (s *Issues) toTicket(in issueJSON, project string) ports.SourceTicket {
	t := ports.SourceTicket{
		Key:    fmt.Sprintf("%s#%d", s.projectOf(in, project), in.IID),
		Title:  in.Title,
		Body:   strings.TrimSpace(in.Description),
		URL:    in.WebURL,
		Status: in.State,
		Type:   in.IssueType,
	}
	if t.Type == "" {
		t.Type = "issue"
	}
	if in.Assignee != nil {
		t.Assignee = in.Assignee.Username
	}
	if ts, err := time.Parse(time.RFC3339, in.UpdatedAt); err == nil {
		t.Updated = ts.UTC()
	}
	if in.ProjectID != 0 {
		t.Body, t.Attachments = s.uploads(t.Body, in.ProjectID)
	}
	return t
}

// uploads lists the files a description links and makes those links
// absolute, since the body is read outside GitLab once imported. Each file
// downloads through the project uploads API (OpenAttachment).
func (s *Issues) uploads(body string, projectID int) (string, []ports.SourceAttachment) {
	var out []ports.SourceAttachment
	seen := map[string]bool{}
	for _, m := range uploadRe.FindAllStringSubmatch(body, -1) {
		secret, file := m[2], m[3]
		if seen[secret+"/"+file] {
			continue
		}
		seen[secret+"/"+file] = true
		name, err := url.PathUnescape(file)
		if err != nil {
			name = file
		}
		out = append(out, ports.SourceAttachment{
			ID:       secret,
			Filename: name,
			URL:      fmt.Sprintf("%s/api/v4/projects/%d/uploads/%s/%s", s.a.cfg.BaseURL, projectID, secret, file),
		})
	}
	body = uploadRe.ReplaceAllString(body, fmt.Sprintf("](%s/-/project/%d$1)", s.a.cfg.BaseURL, projectID))
	return body, out
}

// Search lists issues over the configured projects and groups: an issue key
// looks the issue up directly, empty means "open and assigned to me",
// anything else is a text search with a few GitLab-style filters lifted out
// (searchParams).
func (s *Issues) Search(ctx context.Context, query string, limit int) ([]ports.SourceTicket, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	query = strings.TrimSpace(query)
	if issueKeyRe.MatchString(query) {
		if t, err := s.Fetch(ctx, query); err == nil {
			return []ports.SourceTicket{t}, nil
		}
	}
	params := searchParams(query)
	params.Set("per_page", strconv.Itoa(limit))
	var scopes []string
	for _, p := range s.projects {
		scopes = append(scopes, projectPath(p)+"/issues")
	}
	for _, g := range s.groups {
		scopes = append(scopes, "/groups/"+url.PathEscape(g)+"/issues")
	}
	if len(scopes) == 0 {
		scopes = []string{"/issues"}
	}
	seen := map[string]bool{}
	var tickets []ports.SourceTicket
	for _, scope := range scopes {
		var rows []issueJSON
		if _, err := s.a.api(ctx, http.MethodGet, scope+"?"+params.Encode(), nil, &rows); err != nil {
			return nil, err
		}
		for _, it := range rows {
			t := s.toTicket(it, "")
			if !seen[t.Key] {
				seen[t.Key] = true
				tickets = append(tickets, t)
			}
		}
	}
	sort.SliceStable(tickets, func(i, j int) bool { return tickets[i].Updated.After(tickets[j].Updated) })
	if len(tickets) > limit {
		tickets = tickets[:limit]
	}
	return tickets, nil
}

// searchParams turns the search box into the issues API's query: the words
// GitLab's own filter bar takes — ~label, label:x, state:opened|closed|all,
// is:open|closed, assignee:@me|name, author:@me|name, milestone:x, type:x —
// become their parameters (one word each), the rest is the text search.
func searchParams(query string) url.Values {
	v := url.Values{"order_by": {"updated_at"}, "sort": {"desc"}, "scope": {"all"}}
	if query == "" {
		v.Set("scope", "assigned_to_me")
		v.Set("state", "opened")
		return v
	}
	var labels, text []string
	for _, w := range strings.Fields(query) {
		if l, ok := strings.CutPrefix(w, "~"); ok && l != "" {
			labels = append(labels, l)
			continue
		}
		key, val, ok := strings.Cut(w, ":")
		if !ok || val == "" {
			text = append(text, w)
			continue
		}
		switch strings.ToLower(key) {
		case "label", "labels":
			labels = append(labels, strings.TrimPrefix(val, "~"))
		case "state", "is":
			switch strings.ToLower(val) {
			case "open", "opened":
				v.Set("state", "opened")
			case "closed":
				v.Set("state", "closed")
			default:
				v.Del("state")
			}
		case "assignee":
			if val == "@me" {
				v.Set("scope", "assigned_to_me")
			} else {
				v.Set("assignee_username", strings.TrimPrefix(val, "@"))
			}
		case "author":
			if val == "@me" {
				v.Set("scope", "created_by_me")
			} else {
				v.Set("author_username", strings.TrimPrefix(val, "@"))
			}
		case "milestone":
			v.Set("milestone", strings.TrimPrefix(val, "%"))
		case "type":
			v.Set("issue_type", strings.ToLower(val))
		default:
			text = append(text, w)
		}
	}
	if len(labels) > 0 {
		v.Set("labels", strings.Join(labels, ","))
	}
	if len(text) > 0 {
		v.Set("search", strings.Join(text, " "))
	}
	return v
}

func (s *Issues) get(ctx context.Context, project string, iid int) (issueJSON, error) {
	var it issueJSON
	_, err := s.a.api(ctx, http.MethodGet, issuePath(project, iid), nil, &it)
	return it, err
}

// Fetch returns one issue with the files its description links.
func (s *Issues) Fetch(ctx context.Context, key string) (ports.SourceTicket, error) {
	project, iid, err := s.parseKey(key)
	if err != nil {
		return ports.SourceTicket{}, err
	}
	it, err := s.get(ctx, project, iid)
	if err != nil {
		return ports.SourceTicket{}, err
	}
	return s.toTicket(it, project), nil
}

// OpenAttachment downloads one uploaded file (GET
// /projects/:id/uploads/:secret/:filename). The URL is re-checked against the
// configured host before the token goes with it.
func (s *Issues) OpenAttachment(ctx context.Context, att ports.SourceAttachment) (io.ReadCloser, error) {
	u, err := url.Parse(att.URL)
	if err != nil {
		return nil, fmt.Errorf("gitlab: attachment %s: %w", att.Filename, err)
	}
	base, err := url.Parse(s.a.cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(u.Host, base.Host) {
		return nil, fmt.Errorf("gitlab: attachment %s points outside %s (%s) — refusing to send credentials there",
			att.Filename, base.Host, u.Host)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", s.a.cfg.Token)
	resp, err := s.a.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		resp.Body.Close()
		return nil, fmt.Errorf("gitlab: attachment %s: %d: %s", att.Filename, resp.StatusCode, tail(string(data), 300))
	}
	return resp.Body, nil
}

// --- write side (ports.TicketWriter) ------------------------------------------

var issueStates = []string{"opened", "closed"}

// issueStatusMap is what the canonical statuses mean for an issue: todo is
// opened, done is closed, and nothing else has a word on the REST issue.
var issueStatusMap = map[ports.TicketStatus]string{ports.StatusTodo: "opened", ports.StatusDone: "closed"}

// TransitionTo reopens (todo) or closes (done) the issue; every other
// canonical status is unsupported — the engine skips the stage.
func (s *Issues) TransitionTo(ctx context.Context, key string, to ports.TicketStatus) error {
	state, ok := issueStatusMap[to]
	if !ok {
		return &ports.ErrStatusUnsupported{Key: strings.TrimSpace(key), Status: to, Kind: "GitLab Issues",
			Supported: []ports.TicketStatus{ports.StatusTodo, ports.StatusDone}}
	}
	return s.Transition(ctx, key, state)
}

// Transition: an issue is opened or closed ("open" is taken for opened, so
// a status written for GitHub reads the same here). Anything else a flow
// asks for is unreachable, and the error says what is.
func (s *Issues) Transition(ctx context.Context, key, status string) error {
	project, iid, err := s.parseKey(key)
	if err != nil {
		return err
	}
	it, err := s.get(ctx, project, iid)
	if err != nil {
		return err
	}
	var want, event string
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "opened", "open":
		want, event = "opened", "reopen"
	case "closed":
		want, event = "closed", "close"
	default:
		return &ports.ErrStatusUnreachable{Key: key, From: it.State, To: status, Reachable: issueStates}
	}
	if it.State == want {
		return nil
	}
	_, err = s.a.api(ctx, http.MethodPut, issuePath(project, iid), map[string]any{"state_event": event}, nil)
	return err
}

func (s *Issues) Comment(ctx context.Context, key, body string) error {
	project, iid, err := s.parseKey(key)
	if err != nil {
		return err
	}
	_, err = s.a.api(ctx, http.MethodPost, issuePath(project, iid)+"/notes", map[string]any{"body": body}, nil)
	return err
}

// Comments lists the issue's notes oldest first, leaving out GitLab's own
// system notes ("changed the description", "closed").
func (s *Issues) Comments(ctx context.Context, key string) ([]string, error) {
	project, iid, err := s.parseKey(key)
	if err != nil {
		return nil, err
	}
	var out []string
	for page := 1; page < 100; page++ {
		var rows []struct {
			Body   string `json:"body"`
			System bool   `json:"system"`
		}
		p := fmt.Sprintf("%s/notes?sort=asc&order_by=created_at&per_page=100&page=%d", issuePath(project, iid), page)
		if _, err := s.a.api(ctx, http.MethodGet, p, nil, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			if !r.System {
				out = append(out, r.Body)
			}
		}
		if len(rows) < 100 {
			break
		}
	}
	return out, nil
}

// issueFields are the only writable "fields" an issue has.
var issueFields = []string{"labels", "assignees", "milestone"}

// SetFields writes labels (replacing the set), assignees (by username) or
// the milestone (by title, the project's or an ancestor group's). The
// engine's append mode merges before calling, so replacing is the right
// primitive here.
func (s *Issues) SetFields(ctx context.Context, key string, fields map[string]string) error {
	project, iid, err := s.parseKey(key)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	update := map[string]any{}
	for _, name := range names {
		items := splitItems(fields[name])
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "labels":
			update["labels"] = strings.Join(items, ",")
		case "assignees":
			ids := []int{}
			for _, u := range items {
				id, err := s.userID(ctx, key, u)
				if err != nil {
					return err
				}
				ids = append(ids, id)
			}
			update["assignee_ids"] = ids
		case "milestone":
			title := strings.TrimSpace(fields[name])
			if title == "" {
				update["milestone_id"] = 0
				continue
			}
			id, err := s.milestone(ctx, project, title)
			if err != nil {
				return err
			}
			update["milestone_id"] = id
		default:
			return &ports.ErrFieldUnsupported{Key: key, Field: name, Supported: issueFields}
		}
	}
	if len(update) == 0 {
		return nil
	}
	_, err = s.a.api(ctx, http.MethodPut, issuePath(project, iid), update, nil)
	return err
}

// userID resolves a username to the id assignee_ids takes.
func (s *Issues) userID(ctx context.Context, key, username string) (int, error) {
	username = strings.TrimPrefix(username, "@")
	var rows []struct {
		ID int `json:"id"`
	}
	if _, err := s.a.api(ctx, http.MethodGet, "/users?username="+url.QueryEscape(username), nil, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, &ports.ErrFieldUnknown{Key: key, Field: "assignee " + username}
	}
	return rows[0].ID, nil
}

// milestone resolves a milestone title to its id, among the project's own
// and its groups'.
func (s *Issues) milestone(ctx context.Context, project, title string) (int, error) {
	var rows []struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
	}
	p := projectPath(project) + "/milestones?include_ancestors=true&per_page=100"
	if _, err := s.a.api(ctx, http.MethodGet, p, nil, &rows); err != nil {
		return 0, err
	}
	var titles []string
	for _, m := range rows {
		if strings.EqualFold(m.Title, title) {
			return m.ID, nil
		}
		titles = append(titles, m.Title)
	}
	return 0, &ports.ErrFieldUnknown{Key: project, Field: "milestone " + title, Candidates: titles}
}

func (s *Issues) Fields(ctx context.Context, key string, names []string) (map[string]string, error) {
	project, iid, err := s.parseKey(key)
	if err != nil {
		return nil, err
	}
	it, err := s.get(ctx, project, iid)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, name := range names {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "labels":
			out[name] = strings.Join(it.Labels, ", ")
		case "assignees":
			var as []string
			for _, a := range it.Assignees {
				as = append(as, a.Username)
			}
			out[name] = strings.Join(as, ", ")
		case "milestone":
			if it.Milestone != nil {
				out[name] = it.Milestone.Title
			} else {
				out[name] = ""
			}
		default:
			return nil, &ports.ErrFieldUnsupported{Key: key, Field: name, Supported: issueFields}
		}
	}
	return out, nil
}

// linkMarker is the invisible tag a link note carries, so a re-entry finds
// the link it already posted instead of posting it again.
func linkMarker(u string) string { return "<!-- apothecary link=" + u + " -->" }

// AddLink posts the link as a note (GitLab autolinks merge requests), once:
// an existing note carrying the marker is the link already attached.
func (s *Issues) AddLink(ctx context.Context, key string, link ports.TicketLink) error {
	if strings.TrimSpace(link.URL) == "" {
		return fmt.Errorf("gitlab: a link needs a URL")
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

// Attach uploads a file to the issue's project for a note to show — the
// same project upload a merge request's attachment is (UploadAttachment).
func (s *Issues) Attach(ctx context.Context, key, name, contentType string, data []byte) (ports.Attachment, error) {
	project, iid, err := s.parseKey(key)
	if err != nil {
		return ports.Attachment{}, err
	}
	it, err := s.get(ctx, project, iid)
	if err != nil {
		return ports.Attachment{}, err
	}
	up := New(Config{BaseURL: s.a.cfg.BaseURL, Token: s.a.cfg.Token, ProjectID: it.ProjectID})
	return up.UploadAttachment(ctx, ports.RepoRef{}, name, contentType, data)
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
	_ ports.TicketSource   = (*Issues)(nil)
	_ ports.TicketWriter   = (*Issues)(nil)
	_ ports.TicketAttacher = (*Issues)(nil)
)
