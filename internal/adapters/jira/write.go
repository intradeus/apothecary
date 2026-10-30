package jira

// The write side (ports.TicketWriter): what a flow's tracker actions call.
// Every call here is one of the "EXACTLY what this integration calls" lines
// in the kind's help text — keep the two in step.

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

	"apothecary/internal/ports"
	"apothecary/internal/ports/repoport"
)

// issuePath is the issue's API path with its key escaped.
func (c *Client) issuePath(key string) string {
	return c.apiBase() + "/issue/" + url.PathEscape(strings.TrimSpace(key))
}

// status reads the issue's current status name.
func (c *Client) status(ctx context.Context, key string) (string, error) {
	var issue struct {
		Fields struct {
			Status struct {
				Name string `json:"name"`
			} `json:"status"`
		} `json:"fields"`
	}
	if _, err := c.api(ctx, http.MethodGet, c.issuePath(key)+"?fields=status", nil, &issue); err != nil {
		return "", err
	}
	return issue.Fields.Status.Name, nil
}

// Transition moves the issue to the named status: the transition whose
// target status (or own name) matches, case-insensitively, among those Jira
// offers from the CURRENT status — a workflow may well not allow the move,
// and then the error names what it does allow. Already there ⇒ no-op.
func (c *Client) Transition(ctx context.Context, key, status string) error {
	key, status = strings.TrimSpace(key), strings.TrimSpace(status)
	if key == "" || status == "" {
		return fmt.Errorf("jira: transition needs an issue key and a status")
	}
	cur, err := c.status(ctx, key)
	if err != nil {
		return err
	}
	if strings.EqualFold(cur, status) {
		return nil
	}
	var out struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	if _, err := c.api(ctx, http.MethodGet, c.issuePath(key)+"/transitions", nil, &out); err != nil {
		return err
	}
	var reachable []string
	id := ""
	for _, t := range out.Transitions {
		if t.To.Name != "" && !containsFold(reachable, t.To.Name) {
			reachable = append(reachable, t.To.Name)
		}
		if id == "" && (strings.EqualFold(t.To.Name, status) || strings.EqualFold(t.Name, status)) {
			id = t.ID
		}
	}
	if id == "" {
		return &ports.ErrStatusUnreachable{Key: key, From: cur, To: status, Reachable: reachable}
	}
	_, err = c.api(ctx, http.MethodPost, c.issuePath(key)+"/transitions",
		map[string]any{"transition": map[string]any{"id": id}}, nil)
	return err
}

// DefaultStatusMap is what a canonical status means on a Jira with the
// stock software workflow: To Do → In Progress → In Review → Done. There is
// no stock "blocked" column, so blocked is unsupported until the
// integration maps it.
func DefaultStatusMap() map[ports.TicketStatus]string {
	return map[ports.TicketStatus]string{
		ports.StatusTodo:       "To Do",
		ports.StatusInProgress: "In Progress",
		ports.StatusInReview:   "In Review",
		ports.StatusDone:       "Done",
	}
}

// ParseStatusMap reads the integration's status map text — canonical=status
// pairs separated by commas or newlines ("done=Ready for prod, blocked=On
// hold") — laid over DefaultStatusMap: a listed status replaces the default
// word for it, and an empty value ("in_review=") removes the default, so a
// flow's `to: in_review` skips on a workflow that has no review column.
// Empty text is the defaults.
func ParseStatusMap(text string) (map[ports.TicketStatus]string, error) {
	m := DefaultStatusMap()
	for part := range strings.SplitSeq(strings.ReplaceAll(text, "\n", ","), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("jira: status map entry %q is not canonical=status", part)
		}
		st, known := ports.ParseTicketStatus(name)
		if !known {
			return nil, fmt.Errorf("jira: status map names %q; the canonical statuses are %s", strings.TrimSpace(name), ports.StatusNames(ports.CanonicalStatuses()))
		}
		if value = strings.TrimSpace(value); value == "" {
			delete(m, st)
		} else {
			m[st] = value
		}
	}
	return m, nil
}

// supportedStatuses lists the canonical statuses the map has a word for,
// in lifecycle order.
func (c *Client) supportedStatuses() []ports.TicketStatus {
	var out []ports.TicketStatus
	for _, st := range ports.CanonicalStatuses() {
		if c.cfg.StatusMap[st] != "" {
			out = append(out, st)
		}
	}
	return out
}

// TransitionTo moves the issue to the workflow status the integration maps
// the canonical one to (Config.StatusMap); a canonical status with no word
// on this site is unsupported, which the engine skips rather than fails.
func (c *Client) TransitionTo(ctx context.Context, key string, to ports.TicketStatus) error {
	name := c.cfg.StatusMap[to]
	if name == "" {
		return &ports.ErrStatusUnsupported{Key: strings.TrimSpace(key), Status: to, Kind: "Jira", Supported: c.supportedStatuses()}
	}
	return c.Transition(ctx, key, name)
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// richText renders markdown the way the configured API expects a rich-text
// value: an ADF document on v3 (Cloud), the text itself on v2 (Data Center,
// which takes wiki markup — close enough to read for the prose the engine
// writes).
func (c *Client) richText(md string) any {
	if c.cfg.APIVersion == "2" {
		return md
	}
	return FromMarkdown(md)
}

// Comment posts markdown as a new comment.
func (c *Client) Comment(ctx context.Context, key, body string) error {
	_, err := c.api(ctx, http.MethodPost, c.issuePath(key)+"/comment", map[string]any{"body": c.richText(body)}, nil)
	return err
}

// Comments lists the issue's comments as markdown, oldest first, paging to
// exhaustion (the reconciliation read).
func (c *Client) Comments(ctx context.Context, key string) ([]string, error) {
	var out []string
	for start := 0; ; {
		var page struct {
			Comments []struct {
				Body json.RawMessage `json:"body"`
			} `json:"comments"`
			Total int `json:"total"`
		}
		p := fmt.Sprintf("%s/comment?startAt=%d&maxResults=100&orderBy=created", c.issuePath(key), start)
		if _, err := c.api(ctx, http.MethodGet, p, nil, &page); err != nil {
			return nil, err
		}
		for _, cm := range page.Comments {
			out = append(out, ToMarkdown(cm.Body))
		}
		start += len(page.Comments)
		if len(page.Comments) == 0 || start >= page.Total {
			return out, nil
		}
	}
}

// AddLink attaches a remote issue link. globalId is the URL, so Jira treats
// a second call with the same link as an update of the first — idempotent by
// construction, which is what a re-entered stage needs.
func (c *Client) AddLink(ctx context.Context, key string, link ports.TicketLink) error {
	if strings.TrimSpace(link.URL) == "" {
		return fmt.Errorf("jira: a link needs a URL")
	}
	title := strings.TrimSpace(link.Title)
	if title == "" {
		title = link.URL
	}
	_, err := c.api(ctx, http.MethodPost, c.issuePath(key)+"/remotelink", map[string]any{
		"globalId": "apothecary:" + link.URL,
		"object":   map[string]any{"url": link.URL, "title": title},
	}, nil)
	return err
}

// Attach attaches a file to the issue (POST /issue/{key}/attachments,
// multipart, with the XSRF opt-out Jira asks of API clients) for a comment
// to link. A comment links it rather than embedding it: an embedded image in
// ADF is a media node, which needs the Media API's id, not the attachment's.
// On v2 the link is wiki markup — the image itself (!name!) or an
// attachment link ([^name]) — since that is what a v2 comment body is.
func (c *Client) Attach(ctx context.Context, key, name, contentType string, data []byte) (repoport.Attachment, error) {
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
		return repoport.Attachment{}, err
	}
	path := c.issuePath(key) + "/attachments"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path, &buf)
	if err != nil {
		return repoport.Attachment{}, err
	}
	c.authorize(req)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Atlassian-Token", "no-check")
	resp, err := c.http.Do(req)
	if err != nil {
		return repoport.Attachment{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return repoport.Attachment{}, fmt.Errorf("jira POST %s: %d: %s", path, resp.StatusCode, errText(body))
	}
	var out []struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return repoport.Attachment{}, fmt.Errorf("jira POST %s: decode: %w", path, err)
	}
	if len(out) == 0 || out[0].Content == "" {
		return repoport.Attachment{}, fmt.Errorf("jira POST %s: the attachment has no content URL", path)
	}
	att := repoport.Attachment{URL: out[0].Content, Markdown: "[" + name + "](" + out[0].Content + ")"}
	if c.cfg.APIVersion == "2" {
		att.Markdown = "[^" + out[0].Filename + "]"
		if strings.HasPrefix(contentType, "image/") {
			att.Markdown = "!" + out[0].Filename + "!"
		}
	}
	return att, nil
}

// quoteEscaper escapes a multipart filename, as mime/multipart does.
var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

var (
	_ ports.TicketWriter   = (*Client)(nil)
	_ ports.TicketAttacher = (*Client)(nil)
)
