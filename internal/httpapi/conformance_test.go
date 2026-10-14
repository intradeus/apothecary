package httpapi

// Does the server actually send what the document promises?
//
// The route table declares each endpoint's response TYPE, and everything
// downstream — the spec, the generated client, the frontend's mappers —
// believes that declaration. A wrong one is invisible: the code compiles, the
// spec validates, and the client is confidently wrong. (It happened while this
// was being written: /api/projects/{id}/branches was declared []string when it
// returns objects.)
//
// So: drive the real handlers and validate the real bytes against the
// generated schema.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"apothecary/internal/engine"
)

// conformance walks these; each is a request the test fixture can really
// serve. Endpoints that need a live workspace, a forge or a running agent are
// covered by their own tests — what is checked here is the SHAPE contract.
type probe struct {
	method string
	path   string
	body   any
	// want is the status the fixture produces; a 4xx/5xx body is validated
	// against the error schema, which is a contract too.
	want int
}

func TestResponsesMatchTheSpec(t *testing.T) {
	doc, err := Spec()
	if err != nil {
		t.Fatal(err)
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}

	x := newEnv(t, engine.Config{})
	// A run to address, and a ticket, so the id-bearing routes answer with
	// content rather than 404.
	runID := x.createTicketFlow(t, "conformance", "loop")
	x.e.Wait(runID)

	probes := []probe{
		{method: "GET", path: "/api/runs", want: 200},
		{method: "GET", path: "/api/runs/" + runID, want: 200},
		{method: "GET", path: "/api/runs/" + runID + "/files", want: 200},
		{method: "GET", path: "/api/runs/" + runID + "/ports", want: 200},
		{method: "GET", path: "/api/runs/" + runID + "/resources", want: 200},
		{method: "GET", path: "/api/runs/" + runID + "/egress-denials", want: 200},
		{method: "GET", path: "/api/egress-defaults", want: 200},
		{method: "GET", path: "/api/pings", want: 200},
		// The fixture wires no file store, and saying so honestly is the
		// contract for a server without one.
		{method: "GET", path: "/api/attachments/limits", want: 503},
		{method: "GET", path: "/api/channels", want: 200},
		{method: "GET", path: "/api/projects", want: 200},
		{method: "GET", path: "/api/tickets", want: 200},
		{method: "GET", path: "/api/integrations", want: 200},
		{method: "GET", path: "/api/flows", want: 200},
		{method: "GET", path: "/api/actions", want: 200},
		{method: "GET", path: "/api/signals", want: 200},
		{method: "GET", path: "/api/settings", want: 200},
		{method: "POST", path: "/api/flows/validate", body: map[string]any{"yaml": loopFlowDoc}, want: 200},
		{method: "POST", path: "/api/projects/" + testProject + "/preflight", want: 200},
		// The fake provisioner hosts no sidecar: an honest 503.
		{method: "GET", path: "/api/runs/" + runID + "/workspace/tree", want: 503},
		{method: "POST", path: "/api/runs/" + runID + "/messages", body: map[string]any{"text": "hi"}, want: 409},
		// An error body is part of the contract as much as a success one.
		{method: "GET", path: "/api/runs/nope", want: 404},
	}

	for _, p := range probes {
		t.Run(p.method+" "+p.path, func(t *testing.T) {
			var rd io.Reader
			if p.body != nil {
				data, err := json.Marshal(p.body)
				if err != nil {
					t.Fatal(err)
				}
				rd = bytes.NewReader(data)
			}
			req := httptest.NewRequest(p.method, p.path, rd)
			if p.body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			x.srv.Handler().ServeHTTP(rec, req)
			res := rec.Result()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != p.want {
				t.Fatalf("status = %d (want %d): %s", res.StatusCode, p.want, tail(body))
			}

			route, pathParams, err := router.FindRoute(req)
			if err != nil {
				t.Fatalf("the document has no route for this request: %v", err)
			}
			err = openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
				RequestValidationInput: &openapi3filter.RequestValidationInput{
					Request: req, PathParams: pathParams, Route: route,
					Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
				},
				Status: res.StatusCode,
				Header: res.Header,
				Body:   io.NopCloser(bytes.NewReader(body)),
				Options: &openapi3filter.Options{
					IncludeResponseStatus: true,
					AuthenticationFunc:    openapi3filter.NoopAuthenticationFunc,
				},
			})
			if err != nil {
				t.Errorf("response does not match the schema the client is generated from:\n%v\nbody: %s", err, tail(body))
			}
		})
	}
}

// Every documented request body has to be something a client can send: an
// object with properties, not an opaque blob a generator would emit as
// `unknown`.
func TestRequestBodiesAreDescribed(t *testing.T) {
	doc, err := Spec()
	if err != nil {
		t.Fatal(err)
	}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op.RequestBody == nil || op.RequestBody.Value == nil {
				continue
			}
			for mime, media := range op.RequestBody.Value.Content {
				if mime != "application/json" {
					continue
				}
				schema := resolved(doc, media.Schema)
				if schema == nil || (len(schema.Properties) == 0 && schema.AdditionalProperties.Has == nil &&
					schema.AdditionalProperties.Schema == nil) {
					t.Errorf("%s %s: request body has no described shape", method, path)
				}
			}
		}
	}
}

func resolved(doc *openapi3.T, ref *openapi3.SchemaRef) *openapi3.Schema {
	if ref == nil {
		return nil
	}
	if ref.Value != nil {
		return ref.Value
	}
	name := strings.TrimPrefix(ref.Ref, "#/components/schemas/")
	if s, ok := doc.Components.Schemas[name]; ok {
		return s.Value
	}
	return nil
}

func tail(b []byte) string {
	const max = 400
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
