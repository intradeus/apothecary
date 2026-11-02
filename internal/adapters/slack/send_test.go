package slack

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

func TestSendOmitsWhatIsNotThere(t *testing.T) {
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}))
	defer ts.Close()
	ch, err := New(Config{WebhookURL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(context.Background(), notifyport.Notification{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if body["text"] != "*t*" || body["mrkdwn"] != true {
		t.Errorf("body = %v", body)
	}
}

// A malformed stored webhook URL is the credential: the error that gets
// logged must not quote it.
func TestSendErrorNeverQuotesTheWebhookURL(t *testing.T) {
	ch, err := New(Config{WebhookURL: "https://hooks.slack.com/services/T0/B0/SECRETPART\n"})
	if err != nil {
		t.Fatal(err)
	}
	err = ch.Send(context.Background(), notifyport.Notification{Title: "t"})
	if err == nil {
		t.Fatal("want an error for a malformed webhook URL")
	}
	if strings.Contains(err.Error(), "SECRETPART") {
		t.Errorf("error quotes the webhook URL: %v", err)
	}
}

// A link cannot break out of its <url|label> with a > of its own.
func TestSendEscapesTheURL(t *testing.T) {
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}))
	defer ts.Close()
	ch, _ := New(Config{WebhookURL: ts.URL})
	if err := ch.Send(context.Background(), notifyport.Notification{Title: "t", URL: "http://x/?a=1&b=<2>"}); err != nil {
		t.Fatal(err)
	}
	if text, _ := body["text"].(string); !strings.Contains(text, "<http://x/?a=1&amp;b=&lt;2&gt;|open in CPUI>") {
		t.Errorf("text = %q", text)
	}
}

func TestSendFailures(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate_limited"))
	}))
	defer ts.Close()
	ch, _ := New(Config{WebhookURL: ts.URL})
	err := ch.Send(context.Background(), notifyport.Notification{Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "rate_limited") {
		t.Errorf("429 = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ch.Send(ctx, notifyport.Notification{Title: "t"}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled = %v", err)
	}
}

// The webhook URL is the credential: whoever holds it can post. A send's error
// goes to the log, so it must not repeat the URL.
func TestSendErrorsDoNotCarryTheWebhookSecret(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL + "/services/T00/B00/s3cr3t"
	ts.Close()
	ch, err := New(Config{WebhookURL: url})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(context.Background(), notifyport.Notification{Title: "t"}); err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("err = %v", err)
	}
}

func TestSendBadWebhookURL(t *testing.T) {
	ch, err := New(Config{WebhookURL: "http://bad host/x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(context.Background(), notifyport.Notification{Title: "t"}); err == nil {
		t.Error("an unparsable webhook URL is an error")
	}
}

func TestFactoryIgnoresAWrongType(t *testing.T) {
	if _, err := Factory(map[string]any{"webhook_url": 42}); err == nil {
		t.Error("a webhook_url that is not a string is missing")
	}
}
