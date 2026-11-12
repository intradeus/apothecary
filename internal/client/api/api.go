// Package api is the terminal client's one way to the engine: its HTTP API
// and its tunnels, as a person (a personal token). The commands
// (internal/client/cli) and, later, the TUI are built on it and speak HTTP
// nowhere else.
//
// The wire types here are the client's own, holding only the fields it
// reads: the engine's packages are not the client's to import (a test holds
// them to the engine's JSON).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/platform/wstunnel"
)

// Client talks to one engine as one person.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// New is a client of the engine at base (its UI's address) with a personal
// token ("" before login).
func New(base, token string) (*Client, error) {
	u, err := ParseBase(base)
	if err != nil {
		return nil, err
	}
	return &Client{base: u, token: token, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// ParseBase reads an engine address: http(s) only, no query, no trailing
// slash ("apothecary.example.com" means https).
func ParseBase(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("no engine address")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("engine address %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("engine address %q: want http(s)://host", raw)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	return u, nil
}

// Base is the engine's address as the client uses it.
func (c *Client) Base() string { return c.base.String() }

// Error is the engine refusing a request: its status and its words.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("the engine answered %d %s", e.Status, http.StatusText(e.Status))
	}
	return e.Message
}

// IsUnauthorized reports an expired, revoked or missing token.
func IsUnauthorized(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusUnauthorized
}

func (c *Client) do(ctx context.Context, hc *http.Client, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return &Error{Status: resp.StatusCode, Message: errorText(data)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s %s: unexpected answer: %w", method, path, err)
	}
	return nil
}

// errorText is the engine's {"error": "..."} message, else the body.
func errorText(data []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(data))
}

// User is who a token belongs to.
type User struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
	Email  string `json:"email"`
}

// Me is who the client is signed in as.
func (c *Client) Me(ctx context.Context) (User, error) {
	var me struct {
		User User `json:"user"`
	}
	err := c.do(ctx, c.http, "GET", "/api/me", nil, &me)
	return me.User, err
}

// EngineStatus is what the engine says of itself before anyone signs in.
type EngineStatus struct {
	// Version is the engine's release ("v3.4.0"), or "dev" for a build from
	// source.
	Version string `json:"version"`
}

// Status asks the engine its version (public).
func (c *Client) Status(ctx context.Context) (EngineStatus, error) {
	var st EngineStatus
	err := c.do(ctx, c.http, "GET", "/api/auth/status", nil, &st)
	return st, err
}

// DeviceLogin is a sign-in waiting for its person to approve it in a
// browser (the device authorization grant): the code they check, where they
// approve it, and the secret this client polls with.
type DeviceLogin struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	Interval        int    `json:"interval"`   // seconds between polls
	ExpiresIn       int    `json:"expires_in"` // seconds
}

// StartLogin opens a sign-in for a client named name.
func (c *Client) StartLogin(ctx context.Context, name string) (DeviceLogin, error) {
	var l DeviceLogin
	err := c.do(ctx, c.http, "POST", "/api/cli/logins", map[string]string{"name": name}, &l)
	if err == nil && (l.DeviceCode == "" || l.UserCode == "") {
		err = errors.New("the engine opened a sign-in without its codes")
	}
	return l, err
}

// Token is a token this client holds: its id (to revoke it) and its secret.
type Token struct {
	ID     string
	Secret string
	Handle string // whose it is
}

// ErrLoginPending is a sign-in nobody has approved yet: poll again.
var ErrLoginPending = errors.New("waiting for the sign-in to be approved")

// PollLogin asks whether the sign-in was approved: the token once it is,
// ErrLoginPending until then, the engine's words when it was denied or
// expired.
func (c *Client) PollLogin(ctx context.Context, deviceCode string) (Token, error) {
	var got struct {
		Status  string `json:"status"`
		Token   string `json:"token"`
		TokenID string `json:"token_id"`
		Handle  string `json:"handle"`
	}
	if err := c.do(ctx, c.http, "POST", "/api/cli/logins/token", map[string]string{"device_code": deviceCode}, &got); err != nil {
		var e *Error
		if errors.As(err, &e) && e.Status == http.StatusTooManyRequests {
			return Token{}, ErrLoginPending
		}
		return Token{}, err
	}
	if got.Status != "approved" {
		return Token{}, ErrLoginPending
	}
	if got.Token == "" {
		return Token{}, errors.New("the engine approved the sign-in but sent no token")
	}
	return Token{ID: got.TokenID, Secret: got.Token, Handle: got.Handle}, nil
}

// Run is a run as the client names it.
type Run struct {
	ID          string `json:"run_id"`
	State       string `json:"state"`
	Workspace   string `json:"env_id"`
	TicketKey   string `json:"ticket_key"`
	TicketTitle string `json:"ticket_title"`
}

// Label is how a person recognises the run: its ticket's key and title.
func (r Run) Label() string {
	switch {
	case r.TicketKey != "" && r.TicketTitle != "":
		return r.TicketKey + " · " + r.TicketTitle
	case r.TicketTitle != "":
		return r.TicketTitle
	case r.TicketKey != "":
		return r.TicketKey
	}
	return "run " + r.ID
}

// FindRun is the run ref names: a run id, else a workspace name.
func (c *Client) FindRun(ctx context.Context, ref string) (Run, error) {
	var detail struct {
		Run Run `json:"run"`
	}
	err := c.do(ctx, c.http, "GET", "/api/runs/"+url.PathEscape(ref), nil, &detail)
	if err == nil {
		return detail.Run, nil
	}
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusNotFound {
		return Run{}, err
	}
	var runs []Run
	if err := c.do(ctx, c.http, "GET", "/api/runs", nil, &runs); err != nil {
		return Run{}, err
	}
	for _, r := range runs {
		if r.Workspace != "" && r.Workspace == ref {
			return r, nil
		}
	}
	return Run{}, &Error{Status: http.StatusNotFound, Message: fmt.Sprintf("no run or workspace named %q among your runs", ref)}
}

// RevokeToken deletes one of the signed-in person's tokens.
func (c *Client) RevokeToken(ctx context.Context, id string) error {
	return c.do(ctx, c.http, "DELETE", "/api/me/tokens/"+url.PathEscape(id), nil, nil)
}

// Port is one port of a run's workspace: published (with a browser URL) or
// seen listening.
type Port struct {
	Port    int    `json:"port"`
	URL     string `json:"url,omitempty"`
	Process string `json:"process,omitempty"`
	Service string `json:"service,omitempty"`
}

// RunPorts are a run's ports, as the engine lists them.
type RunPorts struct {
	Ports    []Port `json:"ports"`
	Observed []Port `json:"observed"`
	Links    bool   `json:"links"`
}

// Listening is every port the run's workspace has, published or seen, once
// each, in order.
func (p RunPorts) Listening() []Port {
	seen := map[int]bool{}
	var out []Port
	for _, list := range [][]Port{p.Ports, p.Observed} {
		for _, x := range list {
			if x.Port > 0 && !seen[x.Port] {
				seen[x.Port] = true
				out = append(out, x)
			}
		}
	}
	return out
}

// Ports lists a run's ports.
func (c *Client) Ports(ctx context.Context, runID string) (RunPorts, error) {
	var p RunPorts
	err := c.do(ctx, c.http, "GET", "/api/runs/"+url.PathEscape(runID)+"/ports", nil, &p)
	return p, err
}

// DialPort opens one connection to a port of a run's workspace.
func (c *Client) DialPort(ctx context.Context, runID string, port int) (net.Conn, error) {
	return c.tunnel(ctx, "/api/runs/"+url.PathEscape(runID)+"/ports/"+strconv.Itoa(port)+"/tunnel")
}

// DialSSH opens one connection to the engine's SSH door for a workspace.
func (c *Client) DialSSH(ctx context.Context, workspace string) (net.Conn, error) {
	return c.tunnel(ctx, "/api/ssh/tunnel?workspace="+url.QueryEscape(workspace))
}

func (c *Client) tunnel(ctx context.Context, path string) (net.Conn, error) {
	u := *c.base
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	hdr := http.Header{}
	if c.token != "" {
		hdr.Set("Authorization", "Bearer "+c.token)
	}
	conn, err := wstunnel.Dial(ctx, u.String()+path, hdr)
	var he *wstunnel.HTTPError
	if errors.As(err, &he) {
		return nil, &Error{Status: he.Status, Message: errorText([]byte(he.Body))}
	}
	return conn, err
}
