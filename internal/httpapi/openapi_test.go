package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The document has to be a document: an invalid spec generates a broken
// client, and the failure would surface as TypeScript nonsense rather than as
// this test.
func TestSpecValidates(t *testing.T) {
	doc, err := Spec()
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("generated spec is invalid: %v", err)
	}
	if doc.Paths.Len() == 0 {
		t.Fatal("no paths")
	}
}

// The table is the contract for BOTH the mux and the spec, so its own
// invariants are worth asserting once: every route has the metadata the
// generated client needs, and no two share an operationId (which would
// silently collapse two functions into one).
func TestRouteTableIsWellFormed(t *testing.T) {
	seen := map[string]string{}
	for _, rt := range (&Server{}).routes() {
		where := rt.Method + " " + rt.Path
		switch {
		case rt.ID == "":
			t.Errorf("%s: no operation id", where)
		case rt.Summary == "":
			t.Errorf("%s: no summary", where)
		case rt.Tag == "":
			t.Errorf("%s: no tag", where)
		case rt.Handler == nil:
			t.Errorf("%s: no handler", where)
		}
		if prev, dup := seen[rt.ID]; dup {
			t.Errorf("operation id %q used by both %s and %s", rt.ID, prev, where)
		}
		seen[rt.ID] = where
		// The probes keep the paths every orchestrator and load balancer
		// expects; everything else is the API.
		if !strings.HasPrefix(rt.Path, "/api/") && rt.Path != "/healthz" && rt.Path != "/readyz" {
			t.Errorf("%s: every endpoint lives under /api/", where)
		}
	}
}

// Every route reaches the document, and the document invents none.
func TestSpecAndRoutesAgree(t *testing.T) {
	doc, err := Spec()
	if err != nil {
		t.Fatal(err)
	}
	inSpec := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			inSpec[method+" "+path] = true
			if op.OperationID == "" {
				t.Errorf("%s %s: no operationId (the client's function name)", method, path)
			}
		}
	}
	for _, rt := range (&Server{}).routes() {
		key := rt.Method + " " + rt.Path
		if !inSpec[key] {
			t.Errorf("%s is served but not documented", key)
		}
		delete(inSpec, key)
	}
	for key := range inSpec {
		t.Errorf("%s is documented but not served", key)
	}
}

// The committed api/openapi.yaml is what the frontend client is generated
// from, so a stale file means a client that describes an API the engine no
// longer has. Regenerate with `task api:spec`.
func TestCommittedSpecIsCurrent(t *testing.T) {
	path := filepath.Join("..", "..", "api", "openapi.yaml")
	have, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v — run `task api:spec`", err)
	}
	doc, err := Spec()
	if err != nil {
		t.Fatal(err)
	}
	want, err := SpecYAML(doc)
	if err != nil {
		t.Fatal(err)
	}
	if string(have) != string(want) {
		t.Error("api/openapi.yaml is out of date with the route table — run `task api:spec`")
	}
}
