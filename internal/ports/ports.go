// Package ports holds the engine-shaped vendor boundaries (DESIGN.md §4).
// Ports are shaped by what the engine needs, never by a vendor's API; no
// vendor types cross this boundary. The fakes in internal/adapters/fake are
// the test substrate for every §16 scenario.
package ports

import (
	"context"
	"io"
	"net"
)

// Browser is the project browser (internal/browser): one container per
// project, a Chromium per agent session, driven through Playwright's MCP
// server — what a flow's builtin `browser` MCP server is. The browser has no
// network of its own: it reaches the run's services at localhost, through
// Dial, and nothing else.
type Browser interface {
	// Session serves one MCP session on stdio (newline-delimited JSON-RPC,
	// as an MCP client speaks to a stdio server) until either side closes,
	// bringing the project's browser up first. One session is one Chromium.
	Session(ctx context.Context, s BrowserSession, stdio io.ReadWriter) error
	// Warm brings the project's browser up (building its image on first
	// use) in the background, so a session does not wait on it. project is
	// the project's id (what names the container, via ProjectHostKey);
	// projectName is a docker label only.
	Warm(project, projectName string)
	// Status is the project's browser as the settings page shows it, by
	// project id.
	Status(ctx context.Context, project string) (BrowserStatus, error)
}

// BrowserSession is who a browser session is for.
type BrowserSession struct {
	// Project is the project's id: what names its container (ProjectHostKey).
	Project string
	// ProjectName is the human name, a docker label on the container only.
	ProjectName string
	Run         string
	// Dial reaches a TCP port the run's workspace serves: where the
	// browser's http://localhost:<port> goes.
	Dial func(ctx context.Context, port int) (net.Conn, error)
}

// BrowserStatus is a project browser's state.
type BrowserStatus struct {
	Container string `json:"container"`
	// State is the container's: absent (never started, or removed),
	// running, or stopped (idle — the next session starts it again).
	State string `json:"state"`
	// Image is what the container runs; Current what this build would run
	// — they differ until the browser is next idle and gets recreated.
	Image   string `json:"image,omitempty"`
	Current string `json:"current,omitempty"`
	// Sessions are the agent sessions using it now, each its own Chromium.
	Sessions int `json:"sessions"`
}

// Notification is the one-way fan-out payload; the CPUI inbox is canonical,
// channels are best-effort mirrors.
type Notification struct {
	// UserID is whose channels the notification goes to: the run's owner.
	// Empty is an instance-wide notice (every channel).
	UserID string
	RunID  string
	PingID string
	Type   string // ping type
	Title  string
	Body   string
	URL    string // deep link into the CPUI
}

type NotificationChannel interface {
	// Send errors are logged, never fail the run.
	Send(ctx context.Context, n Notification) error
}
