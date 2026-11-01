package gitlab

// Issues adapts the ticketsource port — read side for the import picker and
// the sync, write side (trackerport.TicketWriter) for a flow's tracker
// actions — to GitLab issues, reusing the repository host adapter's
// authenticated client. GitLab is moving issues onto its "work items"
// framework, whose own API is GraphQL; the REST issues API still serves
// every issue-type work item (issues, incidents, tasks, test cases — the
// integration's type list) and is what the rest of this package speaks, so
// it is what this speaks too. An issue's key is GitLab's own full reference,
// "group/project#123": an iid is only unique within its project. An issue is
// opened or closed (the integration's whole status list) and has no custom
// fields; what the write port cannot express is refused by name.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
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
	// ID is the issue's instance-wide id; IID its number in the project.
	ID          int      `json:"id"`
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

func (s *Issues) toTicket(in issueJSON, project string) trackerport.SourceTicket {
	typ := issueTypeOf(in.IssueType)
	t := trackerport.SourceTicket{
		Key:      fmt.Sprintf("%s#%d", s.projectOf(in, project), in.IID),
		Title:    in.Title,
		Body:     strings.TrimSpace(in.Description),
		URL:      in.WebURL,
		Status:   in.State,
		StatusID: in.State,
		Type:     typ.Name,
		TypeID:   typ.ID,
		Labels:   in.Labels,
		Updated:  parseTime(in.UpdatedAt),
	}
	if in.ID != 0 {
		t.RemoteID = strconv.Itoa(in.ID)
	}
	if in.Assignee != nil {
		t.Assignee = in.Assignee.Username
	}
	if in.ProjectID != 0 {
		t.Body, t.Attachments = s.uploads(t.Body, in.ProjectID)
	}
	return t
}

// parseTime reads GitLab's RFC 3339 timestamps; unparseable is zero.
func parseTime(s string) time.Time {
	if ts, err := time.Parse(time.RFC3339, s); err == nil {
		return ts.UTC()
	}
	return time.Time{}
}

// issueTypes are the work item types the REST issues API serves, by their
// issue_type value — the integration's type list.
var issueTypes = []trackerport.RemoteType{
	{ID: "issue", Name: "Issue"},
	{ID: "incident", Name: "Incident"},
	{ID: "task", Name: "Task"},
	{ID: "test_case", Name: "Test case"},
}

// issueTypeOf names an issue_type: one of issueTypes, a type GitLab added
// since under its own id, and an issue when GitLab does not say.
func issueTypeOf(id string) trackerport.RemoteType {
	if id == "" {
		return issueTypes[0]
	}
	for _, t := range issueTypes {
		if t.ID == id {
			return t
		}
	}
	return trackerport.RemoteType{ID: id, Name: id}
}

// uploads lists the files a description links and makes those links
// absolute, since the body is read outside GitLab once imported. Each file
// downloads through the project uploads API (OpenAttachment).
func (s *Issues) uploads(body string, projectID int) (string, []trackerport.SourceAttachment) {
	var out []trackerport.SourceAttachment
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
		out = append(out, trackerport.SourceAttachment{
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
	var tickets []trackerport.SourceTicket
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

// get reads one issue; a 404 wraps trackerport.ErrTicketNotFound.
func (s *Issues) get(ctx context.Context, project string, iid int) (issueJSON, error) {
	var it issueJSON
	status, err := s.a.api(ctx, http.MethodGet, issuePath(project, iid), nil, &it)
	if status == http.StatusNotFound {
		err = fmt.Errorf("%w: %w", trackerport.ErrTicketNotFound, err)
	}
	return it, err
}

// Fetch returns one issue with the files its description links. No such
// issue (or project) ⇒ an error wrapping trackerport.ErrTicketNotFound.
func (s *Issues) Fetch(ctx context.Context, key string) (trackerport.SourceTicket, error) {
	project, iid, err := s.parseKey(key)
	if err != nil {
		return trackerport.SourceTicket{}, err
	}
	it, err := s.get(ctx, project, iid)
	if err != nil {
		return trackerport.SourceTicket{}, err
	}
	return s.toTicket(it, project), nil
}

// fetchBatch is how many iids one FetchMany request names: a full page.
const fetchBatch = 100

// FetchMany re-reads issues one request per project and fetchBatch iids
// (GET /projects/:id/issues?iids[]=…). An issue — or a whole project — GitLab
// no longer has is left out of the result.
func (s *Issues) FetchMany(ctx context.Context, keys []string) ([]trackerport.SourceTicket, error) {
	var order []string
	byProject := map[string][]int{}
	for _, k := range keys {
		project, iid, err := s.parseKey(k)
		if err != nil {
			return nil, err
		}
		if _, ok := byProject[project]; !ok {
			order = append(order, project)
		}
		byProject[project] = append(byProject[project], iid)
	}
	var out []trackerport.SourceTicket
	for _, project := range order {
		iids := byProject[project]
		for start := 0; start < len(iids); start += fetchBatch {
			v := url.Values{"per_page": {strconv.Itoa(fetchBatch)}, "scope": {"all"}, "state": {"all"}}
			for _, iid := range iids[start:min(start+fetchBatch, len(iids))] {
				v.Add("iids[]", strconv.Itoa(iid))
			}
			var rows []issueJSON
			status, err := s.a.api(ctx, http.MethodGet, projectPath(project)+"/issues?"+v.Encode(), nil, &rows)
			if status == http.StatusNotFound {
				break
			}
			if err != nil {
				return nil, err
			}
			for _, it := range rows {
				out = append(out, s.toTicket(it, project))
			}
		}
	}
	return out, nil
}

// Statuses: an issue is opened or closed — that is the whole list.
func (s *Issues) Statuses(context.Context) ([]trackerport.RemoteStatus, error) {
	return []trackerport.RemoteStatus{
		{ID: "opened", Name: "Open", Category: trackerport.CategoryNew},
		{ID: "closed", Name: "Closed", Category: trackerport.CategoryDone},
	}, nil
}

// Types lists the work item types the issues API serves (issueTypes).
func (s *Issues) Types(context.Context) ([]trackerport.RemoteType, error) {
	return slices.Clone(issueTypes), nil
}

// OpenAttachment downloads one uploaded file (GET
// /projects/:id/uploads/:secret/:filename). The URL is re-checked against the
// configured host before the token goes with it.
func (s *Issues) OpenAttachment(ctx context.Context, att trackerport.SourceAttachment) (io.ReadCloser, error) {
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
		return nil, fmt.Errorf("gitlab: attachment %s: %d: %s", att.Filename, resp.StatusCode, shell.Tail(string(data), 300))
	}
	return resp.Body, nil
}

// --- write side (trackerport.TicketWriter) ------------------------------------------

var issueStates = []string{"opened", "closed"}

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
		return &trackerport.ErrStatusUnreachable{Key: key, From: it.State, To: status, Reachable: issueStates}
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

// ListComments lists the issue's notes oldest first, leaving out GitLab's
// own system notes ("changed the description", "closed").
func (s *Issues) ListComments(ctx context.Context, key string) ([]trackerport.Comment, error) {
	project, iid, err := s.parseKey(key)
	if err != nil {
		return nil, err
	}
	var out []trackerport.Comment
	for page := 1; page < 100; page++ {
		var rows []struct {
			ID     int `json:"id"`
			Author *struct {
				Username string `json:"username"`
			} `json:"author"`
			Body      string `json:"body"`
			System    bool   `json:"system"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
		}
		p := fmt.Sprintf("%s/notes?sort=asc&order_by=created_at&per_page=100&page=%d", issuePath(project, iid), page)
		if _, err := s.a.api(ctx, http.MethodGet, p, nil, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.System {
				continue
			}
			c := trackerport.Comment{ID: strconv.Itoa(r.ID), Body: r.Body,
				Created: parseTime(r.CreatedAt), Updated: parseTime(r.UpdatedAt)}
			if r.Author != nil {
				c.Author = r.Author.Username
			}
			if c.Updated.IsZero() {
				c.Updated = c.Created
			}
			out = append(out, c)
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
			return &trackerport.ErrFieldUnsupported{Key: key, Field: name, Supported: issueFields}
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
		return 0, &trackerport.ErrFieldUnknown{Key: key, Field: "assignee " + username}
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
	return 0, &trackerport.ErrFieldUnknown{Key: project, Field: "milestone " + title, Candidates: titles}
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
			return nil, &trackerport.ErrFieldUnsupported{Key: key, Field: name, Supported: issueFields}
		}
	}
	return out, nil
}

// linkMarker is the invisible tag a link note carries, so a re-entry finds
// the link it already posted instead of posting it again.
func linkMarker(u string) string { return "<!-- apothecary link=" + u + " -->" }

// AddLink posts the link as a note (GitLab autolinks merge requests), once:
// an existing note carrying the marker is the link already attached.
func (s *Issues) AddLink(ctx context.Context, key string, link trackerport.TicketLink) error {
	if strings.TrimSpace(link.URL) == "" {
		return fmt.Errorf("gitlab: a link needs a URL")
	}
	existing, err := s.ListComments(ctx, key)
	if err != nil {
		return err
	}
	marker := linkMarker(link.URL)
	for _, c := range existing {
		if strings.Contains(c.Body, marker) {
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
func (s *Issues) Attach(ctx context.Context, key, name, contentType string, data []byte) (repoport.Attachment, error) {
	project, iid, err := s.parseKey(key)
	if err != nil {
		return repoport.Attachment{}, err
	}
	it, err := s.get(ctx, project, iid)
	if err != nil {
		return repoport.Attachment{}, err
	}
	up := New(Config{BaseURL: s.a.cfg.BaseURL, Token: s.a.cfg.Token, ProjectID: it.ProjectID})
	return up.UploadAttachment(ctx, repoport.RepoRef{}, name, contentType, data)
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
	_ trackerport.TicketMetadata = (*Issues)(nil)
	_ trackerport.TicketSyncer   = (*Issues)(nil)
)
