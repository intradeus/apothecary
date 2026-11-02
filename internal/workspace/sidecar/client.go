// Package sidecar is the engine's client for the per-workspace runtime the
// shim serves (`shim sidecar`, workspace/shim/sidecar.go): the long-lived ACP
// agent host, the workspace's files and git state, and terminals. The wire
// types mirror the shim's — the shim builds standalone from its own source
// tree and cannot share a package with the engine, so the two are kept in
// step by hand and pinned by the integration test in this package.
package sidecar

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/platform/telemetry"
)

// Dialer opens one connection to the sidecar (envport.PortConn satisfies it).
type Dialer interface {
	Dial(ctx context.Context) (net.Conn, error)
}

type Client struct {
	d    Dialer
	http *http.Client
	// stream is the same transport with no overall timeout, for the event
	// stream and the terminal, which live as long as their caller wants.
	stream *http.Client
}

func New(d Dialer) *Client {
	var tr http.RoundTripper = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.Dial(ctx)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}
	tr = telemetry.HTTPTransport(tr)
	return &Client{
		d:      d,
		http:   &http.Client{Transport: tr, Timeout: 5 * time.Minute},
		stream: &http.Client{Transport: tr},
	}
}

// base is a placeholder host: the transport ignores it and dials the socket.
const base = "http://sidecar"

// Error is a non-2xx answer from the sidecar.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return fmt.Sprintf("sidecar %d: %s", e.Status, e.Msg) }

// IsStatus reports whether err is a sidecar answer with the given status.
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		data, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		if json.Unmarshal(data, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		return &Error{Status: res.StatusCode, Msg: e.Error}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// Health is what the sidecar says about itself: its boot id, the checkout
// it serves (Repo) and the directory that resolves to (Workspace) — the
// devcontainer's workspace folder, which $HOME/repo is a symlink onto, and
// what an editor opens.
type Health struct {
	Boot      string `json:"boot"`
	Repo      string `json:"repo"`
	Workspace string `json:"workspace"`
}

func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	err := c.do(ctx, http.MethodGet, "/health", nil, &h)
	return h, err
}

// --- agent ---------------------------------------------------------------------

type StartRequest struct {
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env,omitempty"`
	Cwd  string            `json:"cwd,omitempty"`
}

type StartResponse struct {
	Boot string          `json:"boot"`
	Init json.RawMessage `json:"init"`
	// Proc names the agent process started (or reused): what a session
	// request addresses, since a workspace may run several harnesses.
	Proc string `json:"proc"`
}

func (c *Client) Start(ctx context.Context, req StartRequest) (StartResponse, error) {
	var out StartResponse
	err := c.do(ctx, http.MethodPost, "/agent/start", req, &out)
	return out, err
}

// MCPServer is one MCP server for a session: Command (+Args, Env) for a
// local stdio server, URL (+Headers) for a remote one. The sidecar hands it
// to the agent in ACP's shape.
type MCPServer struct {
	Name    string   `json:"name"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Env     []EnvVar `json:"env,omitempty"`
	URL     string   `json:"url,omitempty"`
	Headers []EnvVar `json:"headers,omitempty"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type SessionRequest struct {
	SessionID string `json:"session_id,omitempty"`
	// Proc is the agent process (StartResponse.Proc) the session runs in;
	// empty = the one process running.
	Proc       string      `json:"proc,omitempty"`
	Cwd        string      `json:"cwd,omitempty"`
	Mode       string      `json:"mode,omitempty"`
	Model      string      `json:"model,omitempty"`
	MCPServers []MCPServer `json:"mcp_servers,omitempty"`
	// ReadOnly: the session must not change the workspace; the sidecar
	// refuses a permission request for a mutating tool (the harness is
	// configured not to make one in the first place).
	ReadOnly bool `json:"read_only,omitempty"`
}

type SessionResponse struct {
	SessionID string   `json:"session_id"`
	Loaded    bool     `json:"loaded"`
	CostUSD   float64  `json:"cost_usd"`
	Model     string   `json:"model,omitempty"`
	Models    []string `json:"models,omitempty"`
	Modes     []string `json:"modes,omitempty"`
}

func (c *Client) Session(ctx context.Context, req SessionRequest) (SessionResponse, error) {
	var out SessionResponse
	err := c.do(ctx, http.MethodPost, "/agent/session", req, &out)
	return out, err
}

type PromptRequest struct {
	SessionID string `json:"session_id"`
	Turn      string `json:"turn"`
	Text      string `json:"text"`
}

func (c *Client) Prompt(ctx context.Context, req PromptRequest) error {
	return c.do(ctx, http.MethodPost, "/agent/prompt", req, nil)
}

func (c *Client) Cancel(ctx context.Context, sessionID string) error {
	return c.do(ctx, http.MethodPost, "/agent/cancel", map[string]string{"session_id": sessionID}, nil)
}

func (c *Client) Stop(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/agent/stop", struct{}{}, nil)
}

type Status struct {
	Boot     string             `json:"boot"`
	Running  bool               `json:"running"`
	LastSeq  int64              `json:"last_seq"`
	Sessions map[string]float64 `json:"sessions"`
	// Turns are the turns in flight, at most one per session, oldest first.
	Turns []TurnStatus `json:"turns,omitempty"`
	// Procs are the live agent processes (StartResponse.Proc ids).
	Procs []string `json:"procs,omitempty"`
}

// Turn is the turn in flight in a session, nil when none.
func (s Status) Turn(session string) *TurnStatus {
	for i := range s.Turns {
		if s.Turns[i].Session == session {
			return &s.Turns[i]
		}
	}
	return nil
}

type TurnStatus struct {
	ID      string    `json:"id"`
	Session string    `json:"session"`
	Since   time.Time `json:"since"`
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var out Status
	err := c.do(ctx, http.MethodGet, "/agent/status", nil, &out)
	return out, err
}

// Event mirrors the shim's sideEvent.
type Event struct {
	Seq         int64      `json:"seq"`
	Boot        string     `json:"boot"`
	TS          time.Time  `json:"ts"`
	Session     string     `json:"session,omitempty"`
	Turn        string     `json:"turn,omitempty"`
	Proc        string     `json:"proc,omitempty"` // agent_exit: which process
	Type        string     `json:"type"`
	Text        string     `json:"text,omitempty"`
	Tool        *Tool      `json:"tool,omitempty"`
	Plan        []Plan     `json:"plan,omitempty"`
	CostUSD     *float64   `json:"cost_usd,omitempty"`
	ContextUsed int64      `json:"context_used,omitempty"`
	ContextSize int64      `json:"context_size,omitempty"`
	StopReason  string     `json:"stop_reason,omitempty"`
	Error       string     `json:"error,omitempty"`
	Usage       *TurnUsage `json:"usage,omitempty"`
}

type Tool struct {
	ID        string   `json:"id"`
	Title     string   `json:"title,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Status    string   `json:"status,omitempty"`
	Input     string   `json:"input,omitempty"`
	Output    string   `json:"output,omitempty"`
	Locations []string `json:"locations,omitempty"`
	Diffs     []Diff   `json:"diffs,omitempty"`
}

type Diff struct {
	Path    string `json:"path"`
	OldText string `json:"old_text,omitempty"`
	NewText string `json:"new_text"`
}

type Plan struct {
	Content  string `json:"content"`
	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
}

type TurnUsage struct {
	InputTokens       int64 `json:"input_tokens,omitempty"`
	OutputTokens      int64 `json:"output_tokens,omitempty"`
	ThoughtTokens     int64 `json:"thought_tokens,omitempty"`
	CachedReadTokens  int64 `json:"cached_read_tokens,omitempty"`
	CachedWriteTokens int64 `json:"cached_write_tokens,omitempty"`
}

// Cursor encodes a stream position (boot + seq) as one opaque string.
func Cursor(boot string, seq int64) string { return boot + ":" + strconv.FormatInt(seq, 10) }

// ParseCursor is Cursor's inverse; ok=false for "" or garbage.
func ParseCursor(c string) (boot string, seq int64, ok bool) {
	b, s, found := strings.Cut(c, ":")
	if !found || b == "" {
		return "", 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return "", 0, false
	}
	return b, n, true
}

// Events follows the event stream from after (exclusive) until ctx ends or
// the connection drops. boot, when set, is the sidecar instance after was
// read from: a different instance answers with a "gap" event first.
func (c *Client) Events(ctx context.Context, boot string, after int64) (<-chan Event, <-chan error, error) {
	q := url.Values{"after": {strconv.FormatInt(after, 10)}}
	if boot != "" {
		q.Set("boot", boot)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/agent/events?"+q.Encode(), nil)
	if err != nil {
		return nil, nil, err
	}
	res, err := c.stream.Do(req)
	if err != nil {
		return nil, nil, err
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		return nil, nil, &Error{Status: res.StatusCode, Msg: "event stream refused"}
	}
	out := make(chan Event, 64)
	errc := make(chan error, 1)
	go func() {
		defer close(out)
		defer res.Body.Close()
		br := bufio.NewReaderSize(res.Body, 64<<10)
		for {
			line, err := br.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				var ev Event
				if json.Unmarshal(line, &ev) == nil {
					select {
					case out <- ev:
					case <-ctx.Done():
						errc <- ctx.Err()
						return
					}
				}
			}
			if err != nil {
				if ctx.Err() != nil {
					err = ctx.Err()
				}
				errc <- err
				return
			}
		}
	}()
	return out, errc, nil
}

// Buffered returns the events the sidecar holds after (exclusive), without
// following: a look-back, e.g. at what happened while the engine was down.
func (c *Client) Buffered(ctx context.Context, boot string, after int64) ([]Event, error) {
	q := url.Values{"after": {strconv.FormatInt(after, 10)}, "follow": {"0"}}
	if boot != "" {
		q.Set("boot", boot)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/agent/events?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, &Error{Status: res.StatusCode, Msg: "event look-back refused"}
	}
	var out []Event
	dec := json.NewDecoder(res.Body)
	for {
		var ev Event
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		out = append(out, ev)
	}
}

// --- workspace -------------------------------------------------------------------

type TreeEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Dir     bool   `json:"dir"`
	Size    int64  `json:"size,omitempty"`
	Ignored bool   `json:"ignored,omitempty"`
}

// Workspace calls take repo: the checkout name of a multi-repo run ("" or
// "repo" = the primary).
func (c *Client) Tree(ctx context.Context, repo, path string) ([]TreeEntry, error) {
	var out []TreeEntry
	err := c.do(ctx, http.MethodGet, "/fs/tree?"+url.Values{"repo": {repo}, "path": {path}}.Encode(), nil, &out)
	return out, err
}

type File struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
}

func (c *Client) File(ctx context.Context, repo, path string) (File, error) {
	var out File
	err := c.do(ctx, http.MethodGet, "/fs/file?"+url.Values{"repo": {repo}, "path": {path}}.Encode(), nil, &out)
	return out, err
}

type ChangedFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	OldPath   string `json:"old_path,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
	Committed bool   `json:"committed"`
}

type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

type Changes struct {
	Branch    string        `json:"branch"`
	Head      string        `json:"head"`
	Base      string        `json:"base"`
	MergeBase string        `json:"merge_base"`
	Commits   []Commit      `json:"commits"`
	Files     []ChangedFile `json:"files"`
}

func (c *Client) Changes(ctx context.Context, repo, base string) (Changes, error) {
	var out Changes
	err := c.do(ctx, http.MethodGet, "/git/changes?"+url.Values{"repo": {repo}, "base": {base}}.Encode(), nil, &out)
	return out, err
}

type FileDiff struct {
	Path      string `json:"path"`
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
}

func (c *Client) Diff(ctx context.Context, repo, base, path string) (FileDiff, error) {
	var out FileDiff
	err := c.do(ctx, http.MethodGet, "/git/diff?"+url.Values{"repo": {repo}, "base": {base}, "path": {path}}.Encode(), nil, &out)
	return out, err
}

// TerminalProxy returns a handler that forwards a browser's WebSocket upgrade
// to the sidecar's /term (query string preserved). httputil.ReverseProxy
// passes a 101 Switching Protocols through and splices the two connections.
func (c *Client) TerminalProxy(repo string) http.Handler {
	target, _ := url.Parse(base)
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/term"
			q := pr.In.URL.Query()
			q.Set("repo", repo)
			pr.Out.URL.RawQuery = q.Encode()
			pr.Out.Host = "sidecar"
		},
		Transport: c.stream.Transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "terminal: "+err.Error(), http.StatusBadGateway)
		},
	}
}
