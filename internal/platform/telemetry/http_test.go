package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// An outbound call's span never shows what the URL carries besides where it
// goes: no userinfo, no query value, and for a credential URL (a webhook) no
// path at all.
func TestOutboundSpansCarryNoURLCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	target := "http://bob:hunter2@" + host + "/services/T0/B0/sekrit-hook?token=sk-live-123&q=a+b#frag"

	for _, c := range []struct {
		name string
		rt   func(http.RoundTripper) http.RoundTripper
		want string
		gone []string
	}{
		{"http", HTTPTransport, "http://" + host + "/services/T0/B0/sekrit-hook?q=REDACTED&token=REDACTED",
			[]string{"hunter2", "sk-live-123", "a+b", "frag"}},
		{"secret url", SecretURLTransport, "http://" + host,
			[]string{"hunter2", "sk-live-123", "sekrit-hook", "frag"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			spans := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
			ctx, parent := tp.Tracer("test").Start(context.Background(), "parent")
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
			res, err := (&http.Client{Transport: c.rt(http.DefaultTransport)}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			parent.End()

			var client *tracetest.SpanStub
			for _, s := range spans.GetSpans() {
				if s.Name == "HTTP POST" {
					client = &s
				}
			}
			if client == nil {
				t.Fatalf("no client span in %d spans", len(spans.GetSpans()))
			}
			for _, a := range client.Attributes {
				v := a.Value.Emit()
				if a.Key == "url.full" && v != c.want {
					t.Errorf("url.full = %q, want %q", v, c.want)
				}
				for _, s := range c.gone {
					if strings.Contains(v, s) {
						t.Errorf("%s = %q shows %q", a.Key, v, s)
					}
				}
			}
		})
	}
}
