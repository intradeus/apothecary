// Package discord adapts the NotificationChannel port to a Discord channel
// webhook (DESIGN.md §6 port table: one-way fan-out; the CPUI inbox is
// canonical, channels are best-effort mirrors — Send errors are logged by
// the registry, never returned to a run).
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"apothecary/internal/platform/telemetry"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/project/channels"
)

// Discord's embed limits (title 256, description 4096 characters).
const (
	maxTitle       = 256
	maxDescription = 4096
)

type Config struct {
	WebhookURL string // e.g. https://discord.com/api/webhooks/123/xxxx
}

type Channel struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) (*Channel, error) {
	if cfg.WebhookURL == "" {
		return nil, fmt.Errorf("discord channel requires webhook_url")
	}
	return &Channel{
		cfg:  cfg,
		http: &http.Client{Transport: telemetry.SecretURLTransport(http.DefaultTransport), Timeout: 15 * time.Second},
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

// discordEscape backslash-escapes Discord markdown punctuation so a ping's
// title and body render literally (a plan_approval type must not italicise,
// a <@id> must not become a mention).
func discordEscape(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`, "*", `\*`, "_", `\_`, "~", `\~`, "`", "\\`", "|", `\|`,
		"<", `\<`, ">", `\>`, "#", `\#`, "[", `\[`, "]", `\]`, "-", `\-`,
	)
	return r.Replace(s)
}

// truncate cuts s to at most n characters, marking the cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// linkable reports whether Discord accepts u as an embed url: anything but
// an absolute http(s) URL fails the whole message with a 400.
func linkable(u string) bool {
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != ""
}

func (c *Channel) Send(ctx context.Context, n notifyport.Notification) error {
	// One embed: the title links back to the CPUI, the body is its text.
	embed := map[string]any{"title": truncate(discordEscape(n.Title), maxTitle)}
	desc := discordEscape(n.Body)
	if n.URL != "" {
		if linkable(n.URL) {
			embed["url"] = n.URL
		} else {
			desc = strings.TrimLeft(desc+"\n"+n.URL, "\n")
		}
	}
	if desc != "" {
		embed["description"] = truncate(desc, maxDescription)
	}
	payload, err := json.Marshal(map[string]any{
		"embeds": []any{embed},
		// Never ping anyone, whatever the text says.
		"allowed_mentions": map[string]any{"parse": []string{}},
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
		return fmt.Errorf("discord send: %d: %s", resp.StatusCode, data)
	}
	return nil
}

var (
	_ notifyport.NotificationChannel = (*Channel)(nil)
	_ channels.Factory               = Factory
)
