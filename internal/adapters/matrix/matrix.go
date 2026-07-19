// Package matrix adapts the NotificationChannel port to the Matrix
// client-server API (DESIGN.md §6 port table: one-way fan-out; the CPUI
// inbox is canonical, channels are best-effort mirrors — Send errors are
// logged by the registry, never returned to a run).
package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"factory/internal/channels"
	"factory/internal/ports"
)

type Config struct {
	Homeserver  string // e.g. https://matrix.org
	AccessToken string
	RoomID      string // e.g. !abc123:matrix.org
}

type Channel struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) (*Channel, error) {
	if cfg.Homeserver == "" || cfg.AccessToken == "" || cfg.RoomID == "" {
		return nil, fmt.Errorf("matrix channel requires homeserver, access_token and room_id")
	}
	return &Channel{
		cfg:  Config{Homeserver: strings.TrimRight(cfg.Homeserver, "/"), AccessToken: cfg.AccessToken, RoomID: cfg.RoomID},
		http: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// Factory builds a Channel from the §8 CRUD config shape
// {homeserver, access_token, room_id} for the channels registry.
func Factory(config map[string]any) (ports.NotificationChannel, error) {
	str := func(k string) string {
		v, _ := config[k].(string)
		return v
	}
	return New(Config{Homeserver: str("homeserver"), AccessToken: str("access_token"), RoomID: str("room_id")})
}

func (c *Channel) Send(ctx context.Context, n ports.Notification) error {
	body := n.Title
	if n.Body != "" {
		body += "\n" + n.Body
	}
	if n.URL != "" {
		body += "\n" + n.URL
	}
	formatted := "<strong>" + html.EscapeString(n.Title) + "</strong>"
	if n.Body != "" {
		formatted += "<br/>" + html.EscapeString(n.Body)
	}
	if n.URL != "" {
		formatted += fmt.Sprintf(`<br/><a href="%s">open in CPUI</a>`, html.EscapeString(n.URL))
	}
	payload, err := json.Marshal(map[string]any{
		"msgtype":        "m.text",
		"body":           body,
		"format":         "org.matrix.custom.html",
		"formatted_body": formatted,
	})
	if err != nil {
		return err
	}
	// PUT with a unique txn id makes delivery idempotent per attempt.
	u := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s",
		c.cfg.Homeserver, url.PathEscape(c.cfg.RoomID), ulid.Make().String())
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("matrix send: %d: %s", resp.StatusCode, data)
	}
	return nil
}

var (
	_ ports.NotificationChannel = (*Channel)(nil)
	_ channels.Factory          = Factory
)
