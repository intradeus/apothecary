// Package jira adapts the TicketSource port to Jira's REST API (Cloud v3 and
// Data Center v2), with no vendor SDK and no Jira types across the port
// boundary. Auth is Basic email+API-token (Cloud) or Bearer PAT (Data Center,
// signalled by leaving the email empty).
//
// Two Jira-shaped details are absorbed here rather than leaking outward:
// descriptions arrive as ADF (Atlassian Document Format) JSON on v3 and as
// wiki markup on v2 — both become markdown (adf.go) — and the search endpoint
// moved from /search to /search/jql on Cloud, so search tries the new route
// and falls back to the old one for older deployments.
package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"apothecary/internal/ports"
)

type Config struct {
	// BaseURL is the site root ("https://acme.atlassian.net"), NOT the API
	// path — attachment download URLs the API hands back are absolute under
	// this host and are checked against it.
	BaseURL string
	// Email + Token = Cloud Basic auth. Email empty ⇒ Token is a Data Center
	// personal access token, sent as Bearer.
	Email string
	Token string
	// JQL narrows every search to the operator's scope ("project = PROJ");
	// empty searches the whole site the token can see.
	JQL string
	// APIVersion is "3" (Cloud, ADF descriptions) or "2" (Data Center).
	APIVersion string
	// StatusMap is the workflow status (or transition name) each canonical
	// status means on this site — what TransitionTo moves to. A canonical
	// status missing from the map is one this Jira has no word for
	// (ErrStatusUnsupported). Nil = DefaultStatusMap.
	StatusMap map[ports.TicketStatus]string
	// TypeMap is the change type each of this site's issue types is, keyed
	// by the lowercased type name ("bug" → fix) — what an imported ticket
	// becomes. A type missing from it is left for the importer to guess
	// (ports.GuessChangeType).
	TypeMap map[string]ports.ChangeType
}

type Client struct {
	cfg  Config
	http *http.Client

	// projects caches the keys a bare issue number is tried against; nil =
	// not looked up yet, empty = looked up and there is nothing to try.
	projMu   sync.Mutex
	projects []string

	// fieldList caches GET /field (fields.go) for the client's life.
	fieldMu   sync.Mutex
	fieldList []fieldJSON
}

func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Email = strings.TrimSpace(cfg.Email)
	// A token pasted from a browser easily carries a trailing newline or a
	// stray space; both produce an unexplained 401.
	cfg.Token = strings.TrimSpace(cfg.Token)
	if cfg.APIVersion == "" {
		cfg.APIVersion = "3"
	}
	if cfg.StatusMap == nil {
		cfg.StatusMap = DefaultStatusMap()
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
}

// isCloud reports an Atlassian-hosted site. Cloud REJECTS a bearer API token
// ("Client must be authenticated to access this resource"), which is the same
// 401 body a wrong password gives — so the deployment is detected from the
// host and the mismatch named before it can masquerade as a bad credential.
func (c *Client) isCloud() bool {
	host := c.cfg.BaseURL
	if u, err := url.Parse(c.cfg.BaseURL); err == nil && u.Host != "" {
		host = u.Host
	}
	host = strings.ToLower(host)
	return strings.HasSuffix(host, ".atlassian.net") || strings.HasSuffix(host, ".jira.com")
}

// credentialMismatch names a configuration that CANNOT authenticate, before a
// request is spent on it. Only the certain case lives here: an *.atlassian.net
// host with no email. Everything else is a guess (Cloud supports custom
// domains, so a non-atlassian.net host proves nothing) and belongs in the
// 401 hint instead.
func (c *Client) credentialMismatch() string {
	if c.isCloud() && c.cfg.Email == "" {
		return "jira: this is a Jira Cloud site, which needs the ACCOUNT EMAIL alongside the API token — the two are sent as basic auth. " +
			"With the email empty the token goes out as a bearer token, which only Data Center accepts, and Cloud answers " +
			`401 "Client must be authenticated to access this resource". Fill in the email of the account that created the token.`
	}
	return ""
}

// hint401 is what to check when the credential was plausible but Jira refused
// it. Jira's own 401 body is the same generic sentence for every cause, so it
// diagnoses nothing on its own.
func (c *Client) hint401() string {
	var h []string
	if c.cfg.Email == "" {
		h = append(h, "the account email is empty, so the token was sent as a BEARER token (Data Center style) — a Jira Cloud site on a custom domain still needs email + token")
	} else {
		h = append(h, "the email must be the login of the account that created the token (an account id or display name will not do)")
	}
	h = append(h,
		"a token created WITH SCOPES is only accepted on Atlassian's api.atlassian.com gateway, not on your site URL — for this integration create an UNSCOPED API token",
		"the token must be pasted whole and un-expired (Atlassian shows it once; re-create it if unsure)",
		`the site URL must be the root ("https://acme.atlassian.net"), with no /rest/... path`,
	)
	if !c.isCloud() && c.cfg.APIVersion == "3" {
		h = append(h, "self-hosted Jira (Data Center/Server) serves REST API v2, not v3 — set the API version to 2")
	}
	return "check: " + strings.Join(h, "; ") + "."
}

func (c *Client) apiBase() string { return "/rest/api/" + c.cfg.APIVersion }

// authorize applies the configured credential. Basic for Cloud (email + API
// token), Bearer for a Data Center PAT.
func (c *Client) authorize(req *http.Request) {
	if c.cfg.Token == "" {
		return
	}
	if c.cfg.Email != "" {
		enc := base64.StdEncoding.EncodeToString([]byte(c.cfg.Email + ":" + c.cfg.Token))
		req.Header.Set("Authorization", "Basic "+enc)
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
}

// api performs one authenticated JSON call; out may be nil. The status is
// returned alongside the error so callers can branch on it (the search
// endpoint's 404/410 fallback).
func (c *Client) api(ctx context.Context, method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, rd)
	if err != nil {
		return 0, err
	}
	c.authorize(req)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("jira %s %s: %d: %s", method, path, resp.StatusCode, errText(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("jira %s %s: decode: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// Test verifies the credential authenticates (GET /myself).
func (c *Client) Test(ctx context.Context) error {
	if c.cfg.BaseURL == "" {
		return fmt.Errorf("jira: no base URL configured")
	}
	if c.cfg.Token == "" {
		return fmt.Errorf("jira: no API token configured")
	}
	if msg := c.credentialMismatch(); msg != "" {
		return errors.New(msg)
	}
	var me struct {
		DisplayName string `json:"displayName"`
	}
	status, err := c.api(ctx, http.MethodGet, c.apiBase()+"/myself", nil, &me)
	if err != nil {
		if status == http.StatusUnauthorized {
			return fmt.Errorf("%w — %s", err, c.hint401())
		}
		return err
	}
	return nil
}

// issueFields is the field set both search and fetch request. Asking
// explicitly matters: the new /search/jql endpoint returns only the id + key
// unless fields are named.
const issueFields = "summary,description,status,issuetype,assignee,updated"

type issueJSON struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"`
		Updated     string          `json:"updated"`
		Status      struct {
			Name string `json:"name"`
		} `json:"status"`
		IssueType struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Assignee struct {
			DisplayName string `json:"displayName"`
		} `json:"assignee"`
		Attachment []struct {
			// id is a string on Cloud and a number on some Data Center
			// versions; decode it loosely and normalize (itoa).
			ID       any    `json:"id"`
			Filename string `json:"filename"`
			MimeType string `json:"mimeType"`
			Size     int64  `json:"size"`
			Content  string `json:"content"`
		} `json:"attachment"`
	} `json:"fields"`
}

func (c *Client) toTicket(in issueJSON) ports.SourceTicket {
	t := ports.SourceTicket{
		Key:      in.Key,
		Title:    in.Fields.Summary,
		Body:     ToMarkdown(in.Fields.Description),
		URL:      c.cfg.BaseURL + "/browse/" + in.Key,
		Status:   in.Fields.Status.Name,
		Type:     in.Fields.IssueType.Name,
		Assignee: in.Fields.Assignee.DisplayName,
	}
	t.ChangeType = c.cfg.TypeMap[strings.ToLower(strings.TrimSpace(t.Type))]
	// Jira stamps "2026-07-31T10:04:00.000+0200" — RFC3339 with no colon in
	// the offset, so it needs its own layout.
	if ts, err := time.Parse("2006-01-02T15:04:05.999-0700", in.Fields.Updated); err == nil {
		t.Updated = ts.UTC()
	}
	for _, a := range in.Fields.Attachment {
		t.Attachments = append(t.Attachments, ports.SourceAttachment{
			ID: itoa(a.ID), Filename: a.Filename, Mime: a.MimeType, Size: a.Size, URL: a.Content,
		})
	}
	return t
}

// Search runs the JQL derived from the operator's free text (see JQL) and
// returns the matching issues, most recently updated first.
//
// A BARE ISSUE NUMBER ("2624") is resolved first, the way Jira's own quick
// search does it: JQL has no way to match a key by its number half, so the
// candidate keys are built from the project(s) in scope and looked up
// directly. Whatever the text search also finds is appended after.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]ports.SourceTicket, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	var tickets []ports.SourceTicket
	if n := issueNumber(query); n != "" {
		tickets = c.byNumber(ctx, n)
	}
	jql := c.JQL(query)
	var out struct {
		Issues []issueJSON `json:"issues"`
	}
	// Cloud's current endpoint. Older deployments (and Data Center) answer 404
	// or 410 here, so fall back to the classic one rather than reporting a
	// missing route as a search failure.
	status, err := c.api(ctx, http.MethodPost, c.apiBase()+"/search/jql", map[string]any{
		"jql":        jql,
		"maxResults": limit,
		"fields":     strings.Split(issueFields, ","),
	}, &out)
	if err != nil && (status == http.StatusNotFound || status == http.StatusGone ||
		status == http.StatusMethodNotAllowed) {
		p := fmt.Sprintf("%s/search?jql=%s&maxResults=%d&fields=%s",
			c.apiBase(), url.QueryEscape(jql), limit, url.QueryEscape(issueFields))
		_, err = c.api(ctx, http.MethodGet, p, nil, &out)
	}
	if err != nil {
		// A number that already resolved to a real issue is a good answer on
		// its own; don't discard it because the text half failed.
		if len(tickets) > 0 {
			return tickets, nil
		}
		return nil, err
	}
	seen := map[string]bool{}
	for _, t := range tickets {
		seen[t.Key] = true
	}
	for _, i := range out.Issues {
		if seen[i.Key] {
			continue
		}
		tickets = append(tickets, c.toTicket(i))
	}
	return tickets, nil
}

var looseKeyRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*)[\s_-]+([0-9]+)$`)

// normalizeQuery repairs the ways a key gets typed in practice — "proj 2624",
// "PROJ_2624", "PROJ - 2624" all mean PROJ-2624 — so they take the exact-key
// path instead of falling through to a text search that cannot match a key.
func normalizeQuery(q string) string {
	q = strings.TrimSpace(q)
	q = strings.TrimPrefix(q, "#")
	if strings.HasPrefix(q, "jql:") {
		return q
	}
	if m := looseKeyRe.FindStringSubmatch(q); m != nil {
		return m[1] + "-" + m[2]
	}
	return q
}

// issueNumber extracts a query that is nothing but an issue number ("2624",
// "#2624") — the bare-number case Jira's own quick search resolves against
// your projects. "" for anything else, including a full key, which JQL
// already looks up exactly.
func issueNumber(query string) string {
	q := normalizeQuery(query)
	if q == "" || strings.TrimLeft(q, "0123456789") != "" {
		return ""
	}
	return q
}

// byNumber resolves a bare issue number against the projects in scope. JQL
// cannot match a key by its number half, and `key in (…)` errors outright on a
// key that doesn't exist — so each candidate is fetched directly and a 404 is
// simply "not that project". Best-effort: errors yield no hits rather than
// failing the search the operator asked for.
func (c *Client) byNumber(ctx context.Context, number string) []ports.SourceTicket {
	projects := c.scopeProjects(ctx)
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		hits = make([]ports.SourceTicket, len(projects))
		sem  = make(chan struct{}, 4)
	)
	for i, p := range projects {
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if t, err := c.Fetch(ctx, key+"-"+number); err == nil {
				mu.Lock()
				hits[i] = t
				mu.Unlock()
			}
		}(i, p)
	}
	wg.Wait()
	// Keep the candidate order (configured filter first, then most recently
	// active project) rather than whichever request finished first.
	out := make([]ports.SourceTicket, 0, len(hits))
	for _, t := range hits {
		if t.Key != "" {
			out = append(out, t)
		}
	}
	return out
}

// scopeProjects lists the project keys a bare number could belong to: the ones
// named by the configured filter if it names any (no request needed), else the
// most recently active projects the account can see, fetched once and cached.
func (c *Client) scopeProjects(ctx context.Context) []string {
	if keys := projectKeysFromJQL(c.cfg.JQL); len(keys) > 0 {
		return keys
	}
	c.projMu.Lock()
	defer c.projMu.Unlock()
	if c.projects != nil {
		return c.projects
	}
	c.projects = []string{} // negative-cache: don't re-probe on every keystroke
	var out struct {
		Values []struct {
			Key string `json:"key"`
		} `json:"values"`
	}
	// Cloud + recent DC: a paged search ordered by activity. Older DC serves
	// the unpaged /project, which decodes as a bare array.
	p := c.apiBase() + "/project/search?maxResults=" + strconv.Itoa(maxNumberProbes) + "&orderBy=lastIssueUpdatedTime"
	if _, err := c.api(ctx, http.MethodGet, p, nil, &out); err != nil {
		var all []struct {
			Key string `json:"key"`
		}
		if _, err := c.api(ctx, http.MethodGet, c.apiBase()+"/project", nil, &all); err != nil {
			return c.projects
		}
		for _, v := range all {
			out.Values = append(out.Values, v)
		}
	}
	for _, v := range out.Values {
		if v.Key != "" && len(c.projects) < maxNumberProbes {
			c.projects = append(c.projects, v.Key)
		}
	}
	return c.projects
}

// maxNumberProbes caps how many projects a bare number is tried against — one
// GET each, so this is the latency budget of an interactive search box.
const maxNumberProbes = 8

var (
	projectEqRe = regexp.MustCompile(`(?i)\bproject\s*=\s*"?([A-Za-z][A-Za-z0-9_]*)"?`)
	projectInRe = regexp.MustCompile(`(?i)\bproject\s+in\s*\(([^)]*)\)`)
)

// projectKeysFromJQL pulls the project keys out of the configured scope filter
// (`project = PROJ`, `project in (A, B)`), so a site with a filter never needs
// the project-listing request.
func projectKeysFromJQL(jql string) []string {
	var keys []string
	add := func(s string) {
		s = strings.Trim(strings.TrimSpace(s), `"'`)
		if s != "" && !strings.EqualFold(s, "currentUser()") {
			keys = append(keys, strings.ToUpper(s))
		}
	}
	for _, m := range projectEqRe.FindAllStringSubmatch(jql, -1) {
		add(m[1])
	}
	for _, m := range projectInRe.FindAllStringSubmatch(jql, -1) {
		for part := range strings.SplitSeq(m[1], ",") {
			add(part)
		}
	}
	return keys
}

// Fetch returns one issue with its attachment metadata.
func (c *Client) Fetch(ctx context.Context, key string) (ports.SourceTicket, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return ports.SourceTicket{}, fmt.Errorf("jira: no issue key given")
	}
	var issue issueJSON
	p := fmt.Sprintf("%s/issue/%s?fields=%s,attachment", c.apiBase(), url.PathEscape(key), issueFields)
	if _, err := c.api(ctx, http.MethodGet, p, nil, &issue); err != nil {
		return ports.SourceTicket{}, err
	}
	if issue.Key == "" {
		issue.Key = key
	}
	return c.toTicket(issue), nil
}

// OpenAttachment streams an attachment's bytes. The URL comes from Jira's own
// response, but it is re-checked against the configured site before the
// credential is attached — a tracker must not be able to aim an authenticated
// engine request at an arbitrary host.
func (c *Client) OpenAttachment(ctx context.Context, a ports.SourceAttachment) (io.ReadCloser, error) {
	if a.URL == "" {
		return nil, fmt.Errorf("jira: attachment %s has no content URL", a.Filename)
	}
	u, err := url.Parse(a.URL)
	if err != nil {
		return nil, fmt.Errorf("jira: attachment %s: %w", a.Filename, err)
	}
	base, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if !u.IsAbs() {
		u = base.ResolveReference(u)
	}
	if !strings.EqualFold(u.Host, base.Host) {
		return nil, fmt.Errorf("jira: attachment %s points outside %s (%s) — refusing to send credentials there",
			a.Filename, base.Host, u.Host)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	c.authorize(req)
	// Cloud answers with a redirect to a pre-signed media host; Go drops the
	// Authorization header on the cross-host hop, which is exactly right.
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		return nil, fmt.Errorf("jira: download %s: %d: %s", a.Filename, resp.StatusCode, errText(data))
	}
	return resp.Body, nil
}

var issueKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)

// JQL turns the picker's free text into JQL, scoped by the configured filter:
//   - an issue key ("PROJ-123") looks that issue up directly;
//   - a "jql:" prefix passes the rest through verbatim (the power-user hatch);
//   - anything else is a text search;
//   - empty means "assigned to me and unresolved" — the useful default when
//     the operator opens the picker with nothing typed.
//
// An ORDER BY is appended unless the query already carries one.
func (c *Client) JQL(query string) string {
	query = normalizeQuery(query)
	var clause string
	switch {
	case query == "":
		clause = "assignee = currentUser() AND resolution = Unresolved"
	case issueKeyRe.MatchString(query):
		clause = "key = " + quoteJQL(strings.ToUpper(query))
	default:
		if raw, ok := strings.CutPrefix(query, "jql:"); ok {
			clause = strings.TrimSpace(raw)
			break
		}
		clause = "text ~ " + quoteJQL(query+"*")
	}
	out := clause
	if f := strings.TrimSpace(c.cfg.JQL); f != "" && clause != "" {
		out = "(" + f + ") AND (" + clause + ")"
	} else if f != "" {
		out = f
	}
	if !strings.Contains(strings.ToLower(out), "order by") {
		out += " ORDER BY updated DESC"
	}
	return out
}

// quoteJQL renders a JQL string literal, escaping the two characters that can
// break out of one.
func quoteJQL(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// errText pulls Jira's own message out of an error body: it answers
// {"errorMessages":[…],"errors":{…}} on most failures and HTML on a few.
func errText(data []byte) string {
	var body struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
		Message       string            `json:"message"`
	}
	if err := json.Unmarshal(data, &body); err == nil {
		var parts []string
		parts = append(parts, body.ErrorMessages...)
		for k, v := range body.Errors {
			parts = append(parts, k+": "+v)
		}
		if body.Message != "" {
			parts = append(parts, body.Message)
		}
		if len(parts) > 0 {
			return truncate(strings.Join(parts, "; "), 300)
		}
	}
	return truncate(strings.TrimSpace(string(data)), 300)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// itoa keeps the attachment id readable when Jira returns it as a number.
func itoa(v any) string {
	switch n := v.(type) {
	case string:
		return n
	case float64:
		return strconv.FormatInt(int64(n), 10)
	}
	return ""
}

var _ ports.TicketSource = (*Client)(nil)

// ParseTypeMap reads the integration's type map text — issue type=change
// type pairs separated by commas or newlines ("Bug=fix, Task=chore, Spike=
// chore"); several types may share a change type. Type names match
// case-insensitively. Empty text is no mapping: every type is guessed.
func ParseTypeMap(text string) (map[string]ports.ChangeType, error) {
	m := map[string]ports.ChangeType{}
	for part := range strings.SplitSeq(strings.ReplaceAll(text, "\n", ","), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		if !ok || name == "" {
			return nil, fmt.Errorf("jira: type map entry %q is not issue type=change type", part)
		}
		ct, known := ports.ParseChangeType(value)
		if !known {
			return nil, fmt.Errorf("jira: type map gives %q the change type %q; the change types are feat, fix, hotfix, perf, docs, chore", strings.TrimSpace(name), strings.TrimSpace(value))
		}
		m[name] = ct
	}
	return m, nil
}
