// Package browserport is the project-browser port: one browser container per
// project, a Chromium per agent session, reaching the run's workspace ports
// and nothing else (internal/browser implements it).
package browserport

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
	// the project's id (what names the container, via envport.ProjectHostKey);
	// projectName is a docker label only.
	Warm(project, projectName string)
	// Status is the project's browser as the settings page shows it, by
	// project id.
	Status(ctx context.Context, project string) (BrowserStatus, error)
}

// BrowserSession is who a browser session is for.
type BrowserSession struct {
	// Project is the project's id: what names its container (envport.ProjectHostKey).
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
