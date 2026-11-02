// Package notifyport is the notification port: a channel (Matrix, Slack,
// Discord) a user's run events are announced on.
package notifyport

import "context"

// Notification is the one-way fan-out payload; the CPUI inbox is canonical,
// channels are best-effort mirrors.
type Notification struct {
	// UserID is whose channels the notification goes to: the run's owner, or
	// a notice's recipient.
	// Empty is an instance-wide notice (every channel).
	UserID string
	RunID  string
	PingID string
	Type   string // ping type, or a notice kind (internal/notices)
	Title  string
	Body   string
	URL    string // deep link into the CPUI
}

type NotificationChannel interface {
	// Send errors are logged, never fail the run.
	Send(ctx context.Context, n Notification) error
}
