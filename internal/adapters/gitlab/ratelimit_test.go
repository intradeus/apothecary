package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"apothecary/internal/ports/trackerport"
)

// A 429 is typed with how long to wait: Retry-After, else RateLimit-Reset.
func TestIssuesRateLimited(t *testing.T) {
	header := map[string]string{"Retry-After": "45"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`Retry later`))
	}))
	t.Cleanup(srv.Close)
	s := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok"})
	ctx := context.Background()

	var rl *trackerport.ErrRateLimited
	if _, err := s.Fetch(ctx, "lu/greeter#1"); !errors.As(err, &rl) || rl.RetryAfter != 45*time.Second {
		t.Fatalf("Retry-After = %v", err)
	}
	header = map[string]string{"RateLimit-Reset": strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)}
	if _, err := s.FetchMany(ctx, []string{"lu/greeter#1", "lu/greeter#2"}); !errors.As(err, &rl) || rl.RetryAfter < 50*time.Second || rl.RetryAfter > time.Minute {
		t.Fatalf("RateLimit-Reset = %v (%+v)", err, rl)
	}
}
