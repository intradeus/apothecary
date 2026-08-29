package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"apothecary/internal/engine"
)

func TestDocsAndSpecAreServed(t *testing.T) {
	x := newEnv(t, engine.Config{})
	for _, c := range []struct{ path, want string }{
		{"/api/openapi.json", `"openapi"`},
		{"/api/openapi.yaml", "openapi: 3.0.3"},
		{"/docs", "SwaggerUIBundle"},
		{"/docs/swagger-ui-bundle.js", "swagger"},
		{"/docs/swagger-ui.css", "swagger"},
	} {
		req := httptest.NewRequest("GET", c.path, nil)
		rec := httptest.NewRecorder()
		x.srv.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("%s = %d", c.path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s body does not contain %q", c.path, c.want)
		}
	}
}
