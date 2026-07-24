package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"apothecary/internal/ports"
)

func TestSendPostsWebhookMessage(t *testing.T) {
	var method, ctype string
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, ctype = r.Method, r.Header.Get("Content-Type")
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	ch, err := New(Config{WebhookURL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = ch.Send(context.Background(), ports.Notification{
		Title: "plan_approval: Add login",
		Body:  "artifacts/plan.md",
		URL:   "http://localhost:8080/runs/r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost {
		t.Errorf("method = %s", method)
	}
	if ctype != "application/json" {
		t.Errorf("content-type = %q", ctype)
	}
	text, _ := body["text"].(string)
	if !strings.Contains(text, "*plan_approval: Add login*") {
		t.Errorf("missing bold title: %q", text)
	}
	if !strings.Contains(text, "artifacts/plan.md") {
		t.Errorf("missing body: %q", text)
	}
	if !strings.Contains(text, "<http://localhost:8080/runs/r1|open in CPUI>") {
		t.Errorf("missing link: %q", text)
	}
}

func TestSendEscapesSpecialChars(t *testing.T) {
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	ch, _ := New(Config{WebhookURL: ts.URL})
	if err := ch.Send(context.Background(), ports.Notification{Title: "a < b & c > d"}); err != nil {
		t.Fatal(err)
	}
	text, _ := body["text"].(string)
	if !strings.Contains(text, "a &lt; b &amp; c &gt; d") {
		t.Errorf("special chars not escaped: %q", text)
	}
}

func TestSendErrorSurfaces(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte("no_service"))
	}))
	defer ts.Close()
	ch, _ := New(Config{WebhookURL: ts.URL})
	if err := ch.Send(context.Background(), ports.Notification{Title: "t"}); err == nil || !strings.Contains(err.Error(), "no_service") {
		t.Errorf("err = %v", err)
	}
}

func TestFactoryValidation(t *testing.T) {
	if _, err := Factory(map[string]any{"webhook_url": "https://hooks.slack.com/services/T/B/x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Factory(map[string]any{}); err == nil {
		t.Error("missing webhook_url must fail")
	}
}
