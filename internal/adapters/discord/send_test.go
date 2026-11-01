package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"apothecary/internal/ports/notifyport"
)

// A URL Discord would refuse must not take the whole message with it, and with
// no body it stands alone, with no leading blank line.
func TestSendBareBadURLIsTheDescription(t *testing.T) {
	var body struct {
		Embeds          []map[string]any `json:"embeds"`
		AllowedMentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}))
	defer ts.Close()
	ch, err := New(Config{WebhookURL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(context.Background(), notifyport.Notification{Title: "t", URL: "/runs/r1"}); err != nil {
		t.Fatal(err)
	}
	embed := body.Embeds[0]
	if embed["description"] != "/runs/r1" {
		t.Errorf("description = %q", embed["description"])
	}
	if _, has := embed["url"]; has {
		t.Errorf("a relative URL is not an embed url: %v", embed)
	}
	if body.AllowedMentions.Parse == nil || len(body.AllowedMentions.Parse) != 0 {
		t.Errorf("allowed_mentions.parse must be an empty list: %v", body.AllowedMentions.Parse)
	}
}

func TestLinkable(t *testing.T) {
	for u, want := range map[string]bool{
		"https://x.example/a": true,
		"http://x.example":    true,
		"/relative":           false,
		"ftp://x.example":     false,
		"https://":            false,
		"http://[bad":         false,
	} {
		if got := linkable(u); got != want {
			t.Errorf("linkable(%q) = %v", u, got)
		}
	}
}

func TestTruncateCountsCharactersNotBytes(t *testing.T) {
	if got := truncate(strings.Repeat("é", 5), 5); got != strings.Repeat("é", 5) {
		t.Errorf("at the limit = %q", got)
	}
	if got := truncate(strings.Repeat("é", 6), 5); got != strings.Repeat("é", 4)+"…" {
		t.Errorf("past the limit = %q", got)
	}
}

func TestSendFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()
	ch, _ := New(Config{WebhookURL: ts.URL})
	if err := ch.Send(ctx, notifyport.Notification{Title: "t"}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled = %v", err)
	}
	bad, _ := New(Config{WebhookURL: "http://bad host/x"})
	if err := bad.Send(context.Background(), notifyport.Notification{Title: "t"}); err == nil {
		t.Error("an unparsable webhook URL is an error")
	}
}

// The webhook URL is the credential: whoever holds it can post. A send's error
// goes to the log, so it must not repeat the URL.
func TestSendErrorsDoNotCarryTheWebhookSecret(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL + "/api/webhooks/1/s3cr3t"
	ts.Close()
	ch, err := New(Config{WebhookURL: url})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(context.Background(), notifyport.Notification{Title: "t"}); err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("err = %v", err)
	}
}
