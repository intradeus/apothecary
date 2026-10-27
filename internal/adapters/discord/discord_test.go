package discord

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"apothecary/internal/ports"
)

type payload struct {
	Embeds []struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		URL         string `json:"url"`
	} `json:"embeds"`
	AllowedMentions struct {
		Parse []string `json:"parse"`
	} `json:"allowed_mentions"`
}

// capture serves a webhook that records the last payload.
func capture(t *testing.T) (*httptest.Server, *payload, *http.Request) {
	t.Helper()
	var body payload
	var req http.Request
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req = *r
		data, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(data, &body); err != nil {
			t.Errorf("payload: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(ts.Close)
	return ts, &body, &req
}

func TestSendPostsEmbed(t *testing.T) {
	ts, body, req := capture(t)
	ch, err := New(Config{WebhookURL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = ch.Send(context.Background(), ports.Notification{
		Title: "Plan approval: Add login",
		Body:  "artifacts/plan.md",
		URL:   "http://localhost:8080/runs/r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodPost {
		t.Errorf("method = %s", req.Method)
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	if len(body.Embeds) != 1 {
		t.Fatalf("embeds = %+v", body.Embeds)
	}
	e := body.Embeds[0]
	if e.Title != "Plan approval: Add login" || e.Description != "artifacts/plan.md" || e.URL != "http://localhost:8080/runs/r1" {
		t.Errorf("embed = %+v", e)
	}
	if body.AllowedMentions.Parse == nil || len(body.AllowedMentions.Parse) != 0 {
		t.Errorf("allowed_mentions.parse = %v, want []", body.AllowedMentions.Parse)
	}
}

func TestSendEscapesMarkdown(t *testing.T) {
	ts, body, _ := capture(t)
	ch, _ := New(Config{WebhookURL: ts.URL})
	if err := ch.Send(context.Background(), ports.Notification{Title: "plan_approval", Body: "**hi** <@123>"}); err != nil {
		t.Fatal(err)
	}
	e := body.Embeds[0]
	if e.Title != `plan\_approval` {
		t.Errorf("title = %q", e.Title)
	}
	if e.Description != `\*\*hi\*\* \<@123\>` {
		t.Errorf("description = %q", e.Description)
	}
}

func TestSendNonHTTPURLGoesInDescription(t *testing.T) {
	ts, body, _ := capture(t)
	ch, _ := New(Config{WebhookURL: ts.URL})
	if err := ch.Send(context.Background(), ports.Notification{Title: "t", URL: "/runs/r1"}); err != nil {
		t.Fatal(err)
	}
	e := body.Embeds[0]
	if e.URL != "" || e.Description != "/runs/r1" {
		t.Errorf("embed = %+v", e)
	}
}

func TestSendTruncatesToEmbedLimits(t *testing.T) {
	ts, body, _ := capture(t)
	ch, _ := New(Config{WebhookURL: ts.URL})
	err := ch.Send(context.Background(), ports.Notification{
		Title: strings.Repeat("é", 300),
		Body:  strings.Repeat("a", 5000),
	})
	if err != nil {
		t.Fatal(err)
	}
	e := body.Embeds[0]
	if n := utf8.RuneCountInString(e.Title); n != maxTitle || !strings.HasSuffix(e.Title, "…") {
		t.Errorf("title runes = %d", n)
	}
	if n := utf8.RuneCountInString(e.Description); n != maxDescription {
		t.Errorf("description runes = %d", n)
	}
}

func TestSendErrorSurfaces(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message": "Unknown Webhook", "code": 10015}`))
	}))
	defer ts.Close()
	ch, _ := New(Config{WebhookURL: ts.URL})
	if err := ch.Send(context.Background(), ports.Notification{Title: "t"}); err == nil || !strings.Contains(err.Error(), "Unknown Webhook") {
		t.Errorf("err = %v", err)
	}
}

func TestFactoryValidation(t *testing.T) {
	if _, err := Factory(map[string]any{"webhook_url": "https://discord.com/api/webhooks/1/x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Factory(map[string]any{}); err == nil {
		t.Error("missing webhook_url must fail")
	}
}
