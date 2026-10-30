// Package ports holds the engine-shaped vendor boundaries (DESIGN.md §4).
// Ports are shaped by what the engine needs, never by a vendor's API; no
// vendor types cross this boundary. The fakes in internal/adapters/fake are
// the test substrate for every §16 scenario.
package ports

import (
	"context"
)

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
