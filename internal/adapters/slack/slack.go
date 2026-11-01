// Package slack adapts the NotificationChannel port to a Slack incoming
// webhook (DESIGN.md §6 port table: one-way fan-out; the CPUI inbox is
// canonical, channels are best-effort mirrors — Send errors are logged by
// the registry, never returned to a run).
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"apothecary/internal/ports/notifyport"
	"apothecary/internal/project/channels"
)

type Config struct {
	WebhookURL string // e.g. https://hooks.slack.com/services/T00/B00/xxxx
}

type Channel struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) (*Channel, error) {
	if cfg.WebhookURL == "" {
		return nil, fmt.Errorf("slack channel requires webhook_url")
	}
	return &Channel{
		cfg:  cfg,
		http: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// Factory builds a Channel from the §8 CRUD config shape {webhook_url} for
// the channels registry.
func Factory(config map[string]any) (notifyport.NotificationChannel, error) {
	str := func(k string) string {
		v, _ := config[k].(string)
		return v
	}
	return New(Config{WebhookURL: str("webhook_url")})
}

// slackEscape escapes the three characters Slack treats specially in mrkdwn
// text (& < >), per the Slack "formatting text" reference.
func slackEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func (c *Channel) Send(ctx context.Context, n notifyport.Notification) error {
	// mrkdwn: *bold* title, plain body, and a <url|label> link back to the CPUI.
	text := "*" + slackEscape(n.Title) + "*"
	if n.Body != "" {
		text += "\n" + slackEscape(n.Body)
	}
	if n.URL != "" {
		text += fmt.Sprintf("\n<%s|open in CPUI>", slackEscape(n.URL))
	}
	payload, err := json.Marshal(map[string]any{
		"text":   text,
		"mrkdwn": true,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return channels.ScrubURL(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("slack send: %d: %s", resp.StatusCode, data)
	}
	return nil
}

var (
	_ notifyport.NotificationChannel = (*Channel)(nil)
	_ channels.Factory               = Factory
)
