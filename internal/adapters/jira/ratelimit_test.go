package jira

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"apothecary/internal/ports/trackerport"
)

// A 429 is typed with its Retry-After — seconds or an HTTP date — so the
// sync waits instead of recording the tickets in error.
func TestRateLimitedRetryAfter(t *testing.T) {
	retryAfter := "30"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", retryAfter)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errorMessages":["Rate limit exceeded."]}`))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL, Email: "you@acme.com", Token: "tok"})
	ctx := context.Background()

	var rl *trackerport.ErrRateLimited
	if _, err := c.Fetch(ctx, "PROJ-1"); !errors.As(err, &rl) || rl.RetryAfter != 30*time.Second {
		t.Fatalf("seconds = %v", err)
	}
	retryAfter = time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)
	if _, err := c.FetchMany(ctx, []string{"PROJ-1", "PROJ-2"}); !errors.As(err, &rl) || rl.RetryAfter < 110*time.Second || rl.RetryAfter > 2*time.Minute {
		t.Fatalf("HTTP date = %v (%+v)", err, rl)
	}
	retryAfter = ""
	if _, err := c.Statuses(ctx); !errors.As(err, &rl) || rl.RetryAfter != 0 {
		t.Fatalf("no header = %v", err)
	}
}
