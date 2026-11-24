package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func putEnvReq(t *testing.T, ts *testSidecar, body any) int {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPut, ts.srv.URL+"/env", bytes.NewReader(data))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// What the engine tells the sidecar reaches the next terminal, over the
// sidecar's own environment; a new set replaces the old one whole.
func TestSidecarWorkspaceEnv(t *testing.T) {
	t.Cleanup(func() { engineEnv.set(nil) })
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("JEST_MAX_WORKERS", "from-the-repo")
	t.Setenv("DROPPED", "from-the-repo")
	ts := newTestSidecar(t, t.TempDir())
	if code := putEnvReq(t, ts, map[string]any{"env": map[string]string{"JEST_MAX_WORKERS": "2", "APO_CPUS": "4", "DROPPED": "x"}}); code != http.StatusNoContent {
		t.Fatalf("PUT /env = %d", code)
	}
	if code := putEnvReq(t, ts, map[string]any{"env": map[string]string{"JEST_MAX_WORKERS": "2", "APO_CPUS": "4"}}); code != http.StatusNoContent {
		t.Fatalf("PUT /env again = %d", code)
	}
	ws := dialWS(t, strings.TrimPrefix(ts.srv.URL, "http://"), "/term")
	defer ws.c.Close()
	ws.send(wsOpBinary, []byte("echo \"got-$JEST_MAX_WORKERS-$APO_CPUS-$DROPPED-end\"\n"))
	// The repo's DROPPED stays: only what the engine set is replaced.
	ws.readUntil(t, "got-2-4-from-the-repo-end")

	for _, bad := range []any{
		map[string]any{"env": map[string]string{"1X": "a"}},
		map[string]any{"env": map[string]string{"": "a"}},
		map[string]any{"env": map[string]string{"A-B": "a"}},
		"not an object",
	} {
		if code := putEnvReq(t, ts, bad); code != http.StatusBadRequest {
			t.Errorf("PUT /env %v = %d, want 400", bad, code)
		}
	}
}

func TestWorkspaceEnvOver(t *testing.T) {
	var e workspaceEnv
	base := []string{"A=1", "B=2"}
	if got := e.over(base); !slices.Equal(got, base) {
		t.Errorf("nothing told: %q", got)
	}
	e.set(map[string]string{"B": "3", "C": ""})
	got := e.over(base)
	if !slices.Equal(got, []string{"A=1", "B=2", "B=3", "C="}) {
		t.Errorf("over = %q", got)
	}
	if !slices.Equal(base, []string{"A=1", "B=2"}) {
		t.Errorf("base changed: %q", base)
	}
	for _, ok := range []string{"A", "_", "a_1", "_9"} {
		if !validEnvName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
}
