package telemetry

import (
	"net/http"
	"net/url"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
)

// HTTPTransport traces outbound requests through base. otelhttp records the
// whole URL (url.full) less its userinfo, and a query string can carry a
// token or a person's search; the span keeps scheme, host and path, with
// every query value replaced by REDACTED.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(redactURL{base: base, keepPath: true})
}

// SecretURLTransport is HTTPTransport for addresses that are themselves
// credentials (a Slack or Discord webhook's path is its token): the span
// keeps only scheme and host.
func SecretURLTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(redactURL{base: base})
}

// redactURL sits under otelhttp: its span is on the request's context by
// then, with url.full already set, so setting it again replaces it before
// the span can end.
type redactURL struct {
	base     http.RoundTripper
	keepPath bool
}

func (t redactURL) RoundTrip(r *http.Request) (*http.Response, error) {
	trace.SpanFromContext(r.Context()).SetAttributes(semconv.URLFull(redactedURL(r.URL, t.keepPath)))
	return t.base.RoundTrip(r)
}

// redactedURL is u as a span may show it: scheme and host, the path when
// keepPath, query keys with REDACTED values; never userinfo or fragment.
func redactedURL(u *url.URL, keepPath bool) string {
	out := url.URL{Scheme: u.Scheme, Host: u.Host}
	if !keepPath {
		return out.String()
	}
	out.Path, out.RawPath = u.Path, u.RawPath
	if q := u.Query(); len(q) > 0 {
		for k := range q {
			q[k] = []string{"REDACTED"}
		}
		out.RawQuery = q.Encode()
	}
	return out.String()
}
