package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// The provisioner is the instance's: everyone reads it, an admin changes and
// tests it, and no one makes one of their own over the API.
func TestInstanceProvisionerRoutes(t *testing.T) {
	x := newConfEnv(t)
	bob := x.addUser(t, "bob", false)

	res, data := x.do(t, "GET", "/api/provisioner", nil, bob)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get = %d %s", res.StatusCode, data)
	}
	if got := decode[integrationJSON](t, data); got.Port != "provisioner" || got.Name != "workspaces" {
		t.Errorf("provisioner = %+v", got)
	}

	body := map[string]any{"name": "Cluster", "config": map[string]any{"namespace": "ws"}, "secrets": map[string]string{"token": "s3cret-planted"}}
	if res, data := x.do(t, "PUT", "/api/provisioner", body, bob); res.StatusCode != http.StatusForbidden {
		t.Errorf("a member's change = %d %s, want 403", res.StatusCode, data)
	}
	res, data = x.do(t, "PUT", "/api/provisioner", body, nil)
	if res.StatusCode != http.StatusOK || strings.Contains(string(data), "s3cret-planted") {
		t.Fatalf("admin's change = %d %s", res.StatusCode, data)
	}
	if got := decode[integrationJSON](t, data); got.Name != "Cluster" || got.Config["namespace"] != "ws" || got.Secrets["token"] == "" {
		t.Errorf("changed = %+v", got)
	}

	if res, data := x.do(t, "POST", "/api/provisioner/test", nil, bob); res.StatusCode != http.StatusForbidden {
		t.Errorf("a member's test = %d %s, want 403", res.StatusCode, data)
	}
	res, data = x.do(t, "POST", "/api/provisioner/test", nil, nil)
	if res.StatusCode != http.StatusOK || !decode[TestResult](t, data).OK {
		t.Errorf("admin's test = %d %s", res.StatusCode, data)
	}

	res, data = x.do(t, "POST", "/api/integrations", map[string]any{"port": "provisioner", "kind": "fake", "name": "mine"}, bob)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "System → Provisioner") {
		t.Errorf("one's own provisioner = %d %s, want 400", res.StatusCode, data)
	}
	// Nor does it reach the person's routes, or their list.
	if res, _ := x.do(t, "GET", "/api/integrations/"+decode[integrationJSON](t, data).ID, nil, bob); res.StatusCode != http.StatusNotFound {
		t.Errorf("by id = %d", res.StatusCode)
	}
	res, data = x.do(t, "GET", "/api/integrations", nil, nil)
	if res.StatusCode != http.StatusOK || strings.Contains(string(data), `"provisioner"`) {
		t.Errorf("list = %d %s", res.StatusCode, data)
	}
}

// Without one (a deployment that names no cluster), reading it is 404.
func TestInstanceProvisionerMissing(t *testing.T) {
	x := newConfEnv(t)
	cur, err := x.conf.InstanceProvisioner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := x.conf.DeleteIntegration(t.Context(), cur.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path string }{{"GET", "/api/provisioner"}, {"PUT", "/api/provisioner"}, {"POST", "/api/provisioner/test"}} {
		var body any
		if c.method == "PUT" {
			body = map[string]any{"name": "x"}
		}
		if res, data := x.do(t, c.method, c.path, body, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s = %d %s, want 404", c.method, c.path, res.StatusCode, data)
		}
	}
}
