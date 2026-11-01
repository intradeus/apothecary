package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// With no token configured, nothing but liveness answers: an unset secret must
// not mean an open API (and "" must not match a missing header).
func TestControlWithoutATokenConfiguredRefusesEveryone(t *testing.T) {
	g := newTestGateway(t)
	h := g.controlHandler("")
	if rec := do(t, h, "GET", "/healthz", "", ""); rec.Code != 200 || rec.Body.String() != "ok\n" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/policy", ""},
		{"PUT", "/policy", `{"mode":"deny"}`},
		{"PUT", "/runs/r", `{"subnet":"10.0.0.0/24"}`},
		{"DELETE", "/runs/r", ""},
		{"GET", "/denials", ""},
		{"GET", "/audit", ""},
		{"DELETE", "/buildcache", ""},
	} {
		if rec := do(t, h, c.method, c.path, "", c.body); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", c.method, c.path, rec.Code)
		}
	}
	if g.pol.Load() != nil {
		t.Fatal("an unauthorized PUT took effect")
	}
}

// Every write needs the token, not only reads.
func TestControlRefusesUnauthorizedWrites(t *testing.T) {
	g := newTestGateway(t)
	h := g.controlHandler("s3cret")
	if rec := do(t, h, "PUT", "/policy", "wrong", `{"mode":"deny"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("PUT /policy = %d", rec.Code)
	}
	if g.pol.Load() != nil {
		t.Fatal("policy applied without the token")
	}
	if _, err := os.Stat(g.policyPath); !os.IsNotExist(err) {
		t.Fatalf("policy file written without the token: %v", err)
	}
	if rec := do(t, h, "PUT", "/runs/r", "", `{"subnet":"10.0.0.0/24"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("PUT /runs = %d", rec.Code)
	}
	if g.attributeIP(net.ParseIP("10.0.0.1")) != "" {
		t.Fatal("route attached without the token")
	}
}

func TestControlPolicyEdges(t *testing.T) {
	t.Run("before any policy it reports deny-all", func(t *testing.T) {
		g := newTestGateway(t)
		rec := do(t, g.controlHandler("t"), "GET", "/policy", "t", "")
		var f File
		if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
			t.Fatal(err)
		}
		if f.Mode != ModeAllow || len(f.Domains) != 0 {
			t.Fatalf("policy = %+v", f)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("content type = %q", ct)
		}
	})
	t.Run("a malformed body changes nothing", func(t *testing.T) {
		g := newTestGateway(t)
		rec := do(t, g.controlHandler("t"), "PUT", "/policy", "t", `{"mode":`)
		if rec.Code != http.StatusBadRequest || !strings.HasPrefix(rec.Body.String(), "body: ") {
			t.Fatalf("PUT = %d %q", rec.Code, rec.Body.String())
		}
		if g.pol.Load() != nil {
			t.Fatal("a rejected body must not install a policy")
		}
	})
	t.Run("an oversized body is refused", func(t *testing.T) {
		g := newTestGateway(t)
		big := `{"domains":["` + strings.Repeat("a", maxControlBody) + `"]}`
		if rec := do(t, g.controlHandler("t"), "PUT", "/policy", "t", big); rec.Code != http.StatusBadRequest {
			t.Fatalf("PUT = %d", rec.Code)
		}
		if g.pol.Load() != nil {
			t.Fatal("an oversized body must not install a policy")
		}
	})
	t.Run("no mode means allow mode", func(t *testing.T) {
		g := newTestGateway(t)
		if rec := do(t, g.controlHandler("t"), "PUT", "/policy", "t", `{"domains":["a.example"]}`); rec.Code != http.StatusNoContent {
			t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
		}
		var onDisk File
		if err := readJSON(g.policyPath, &onDisk); err != nil {
			t.Fatal(err)
		}
		if onDisk.Mode != ModeAllow {
			t.Fatalf("persisted mode = %q", onDisk.Mode)
		}
		if g.pol.Load().verdict("b.example", nil) {
			t.Fatal("a mode-less policy must fail closed")
		}
	})
	t.Run("without a state dir it lives in memory", func(t *testing.T) {
		g := newTestGateway(t)
		g.policyPath = ""
		h := g.controlHandler("t")
		if rec := do(t, h, "PUT", "/policy", "t", `{"mode":"deny","domains":["a.example"]}`); rec.Code != http.StatusNoContent {
			t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
		}
		if g.pol.Load().verdict("a.example", nil) || !g.pol.Load().verdict("b.example", nil) {
			t.Fatal("the in-memory policy is not the one PUT")
		}
		var f File
		if err := json.Unmarshal(do(t, h, "GET", "/policy", "t", "").Body.Bytes(), &f); err != nil {
			t.Fatal(err)
		}
		if f.Mode != ModeDeny || len(f.Domains) != 1 {
			t.Fatalf("GET after PUT = %+v", f)
		}
	})
	t.Run("a policy nft refuses answers 500", func(t *testing.T) {
		g := newTestGateway(t)
		applyNFT = func(string) error { return errors.New("nft broke") }
		rec := do(t, g.controlHandler("t"), "PUT", "/policy", "t", `{"mode":"allow"}`)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "nft broke") {
			t.Fatalf("PUT = %d %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("an unwritable state dir answers 500", func(t *testing.T) {
		g := newTestGateway(t)
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		g.policyPath = filepath.Join(blocker, "policy.json")
		rec := do(t, g.controlHandler("t"), "PUT", "/policy", "t", `{"mode":"allow"}`)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("PUT = %d", rec.Code)
		}
		if g.pol.Load() != nil {
			t.Fatal("a policy that could not be persisted must not go live")
		}
	})
}

func TestControlRunsEdges(t *testing.T) {
	t.Run("a bad subnet is refused and attaches nothing", func(t *testing.T) {
		g := newTestGateway(t)
		rec := do(t, g.controlHandler("t"), "PUT", "/runs/r", "t", `{"subnet":"10.0.0.1"}`)
		if rec.Code != http.StatusBadRequest || !strings.HasPrefix(rec.Body.String(), "subnet: ") {
			t.Fatalf("PUT = %d %q", rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(g.routesPath); !os.IsNotExist(err) {
			t.Fatalf("routes written: %v", err)
		}
	})
	t.Run("a malformed body is refused", func(t *testing.T) {
		g := newTestGateway(t)
		if rec := do(t, g.controlHandler("t"), "PUT", "/runs/r", "t", `nope`); rec.Code != http.StatusBadRequest {
			t.Fatalf("PUT = %d", rec.Code)
		}
	})
	t.Run("re-attaching without a peer drops the old one", func(t *testing.T) {
		g := newTestGateway(t)
		h := g.controlHandler("t")
		body := `{"subnet":"10.0.0.0/24","peer":{"public_key":"pk","ip":"10.90.0.2"}}`
		if rec := do(t, h, "PUT", "/runs/r", "t", body); rec.Code != http.StatusNoContent {
			t.Fatalf("PUT = %d", rec.Code)
		}
		if rec := do(t, h, "PUT", "/runs/r", "t", `{"subnet":"10.0.0.0/24"}`); rec.Code != http.StatusNoContent {
			t.Fatalf("PUT = %d", rec.Code)
		}
		var rt Routes
		if err := json.Unmarshal(do(t, h, "GET", "/routes", "t", "").Body.Bytes(), &rt); err != nil {
			t.Fatal(err)
		}
		if _, ok := rt.Peers["r"]; ok {
			t.Fatalf("peers = %+v", rt.Peers)
		}
		if g.attributeIP(net.ParseIP("10.90.0.2")) != "" {
			t.Fatal("a dropped peer's address still attributes")
		}
	})
	t.Run("without a state dir routes live in memory", func(t *testing.T) {
		g := newTestGateway(t)
		g.routesPath = ""
		h := g.controlHandler("t")
		if rec := do(t, h, "PUT", "/runs/r", "t", `{"subnet":"10.0.0.0/24"}`); rec.Code != http.StatusNoContent {
			t.Fatalf("PUT = %d", rec.Code)
		}
		var rt Routes
		if err := json.Unmarshal(do(t, h, "GET", "/routes", "t", "").Body.Bytes(), &rt); err != nil {
			t.Fatal(err)
		}
		if rt.RunSubnets["r"] != "10.0.0.0/24" {
			t.Fatalf("routes = %+v", rt)
		}
		if rec := do(t, h, "DELETE", "/runs/r", "t", ""); rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE = %d", rec.Code)
		}
		if got := g.currentRoutes(); len(got.RunSubnets) != 0 {
			t.Fatalf("routes after DELETE = %+v", got)
		}
	})
	t.Run("an unwritable state dir answers 500 and keeps the old table", func(t *testing.T) {
		g := newTestGateway(t)
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		g.routesPath = filepath.Join(blocker, "routes.json")
		rec := do(t, g.controlHandler("t"), "PUT", "/runs/r", "t", `{"subnet":"10.0.0.0/24"}`)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("PUT = %d", rec.Code)
		}
		if g.attributeIP(net.ParseIP("10.0.0.1")) != "" {
			t.Fatal("a route that could not be persisted must not be live")
		}
	})
}

func TestControlBuildCacheFaults(t *testing.T) {
	t.Run("a missing cache is empty, and clearing it is fine", func(t *testing.T) {
		g := newTestGateway(t)
		h := g.controlHandler("t")
		var info BuildCacheInfo
		if err := json.Unmarshal(do(t, h, "GET", "/buildcache", "t", "").Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
		if info.Bytes != 0 {
			t.Fatalf("bytes = %d", info.Bytes)
		}
		if rec := do(t, h, "DELETE", "/buildcache", "t", ""); rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE = %d", rec.Code)
		}
	})
	t.Run("the wipe restarts the registry and clears the gallery", func(t *testing.T) {
		g := newTestGateway(t)
		dir := t.TempDir()
		g.buildCacheConfig = filepath.Join(dir, "config.yml")
		if err := os.WriteFile(g.buildCacheConfig, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Hour)
		if err := os.Chtimes(g.buildCacheConfig, old, old); err != nil {
			t.Fatal(err)
		}
		g.gallery = newGallery("http://upstream.invalid", filepath.Join(dir, "gallery"))
		if err := os.MkdirAll(g.gallery.dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(g.gallery.dir, "cached"), []byte("vsix"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(g.buildCacheDir, "docker"), 0o755); err != nil {
			t.Fatal(err)
		}

		if rec := do(t, g.controlHandler("t"), "DELETE", "/buildcache", "t", ""); rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE = %d %s", rec.Code, rec.Body.String())
		}
		if mt, ok := mtime(g.buildCacheConfig); !ok || !mt.After(old) {
			t.Error("the config mtime is what restarts the registry child; it did not move")
		}
		if entries, _ := os.ReadDir(g.gallery.dir); len(entries) != 0 {
			t.Errorf("gallery cache left %d entries", len(entries))
		}
		if entries, _ := os.ReadDir(g.buildCacheDir); len(entries) != 0 {
			t.Errorf("build cache left %d entries", len(entries))
		}
	})
	t.Run("an unreadable cache dir answers 500", func(t *testing.T) {
		g := newTestGateway(t)
		g.buildCacheDir = filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(g.buildCacheDir, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if rec := do(t, g.controlHandler("t"), "DELETE", "/buildcache", "t", ""); rec.Code != http.StatusInternalServerError {
			t.Fatalf("DELETE = %d", rec.Code)
		}
	})
	t.Run("an unreadable gallery cache is reported", func(t *testing.T) {
		g := newTestGateway(t)
		f := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		g.gallery = newGallery("http://upstream.invalid", f)
		if err := g.clearBuildCache(); err == nil {
			t.Fatal("clearBuildCache must report the gallery's failure")
		}
	})
}

func TestControlToken(t *testing.T) {
	t.Setenv("APO_GW_TOKEN", "")
	if got, err := controlToken(""); err == nil {
		t.Fatalf("controlToken = %q, want an error with no source", got)
	}
	t.Setenv("APO_GW_TOKEN", "  from-env\n")
	if got, err := controlToken(""); err != nil || got != "from-env" {
		t.Fatalf("controlToken = %q, %v", got, err)
	}
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(" from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := controlToken(file); err != nil || got != "from-file" {
		t.Fatalf("the file must win: %q, %v", got, err)
	}
	if _, err := controlToken(file + ".missing"); err == nil {
		t.Fatal("an unreadable token file must be an error, not a fallback")
	}
}

func TestDirSizeCountsRegularFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "f"), make([]byte, 7), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "g"), make([]byte, 5), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if got := dirSize(dir); got != 12 {
		t.Fatalf("dirSize = %d, want 12", got)
	}
	if got := dirSize(filepath.Join(dir, "missing")); got != 0 {
		t.Fatalf("dirSize of a missing dir = %d", got)
	}
}

func TestAuditorBounds(t *testing.T) {
	var none *auditor
	none.record("r", "h", "ip", 443, "tls", "allow", 1, 1) // a nil auditor is off
	none.retain(map[string]bool{})
	none.flush()

	a := newAuditor("")
	for i := range 8192 {
		a.record("r", itoa(i), "ip", 443, "tls", "deny", 0, 0)
	}
	a.record("r", "one-too-many", "ip", 443, "tls", "deny", 0, 0)
	if got := len(a.snapshot("")); got != 8192 {
		t.Fatalf("records = %d, want the cap", got)
	}
	// A tracked tuple keeps counting at the cap.
	a.record("r", "0", "ip2", 443, "tls", "deny", 2, 3)
	for _, r := range a.snapshot("r") {
		if r.Host == "0" && (r.Count != 2 || r.BytesOut != 2 || r.BytesIn != 3 || r.DstIP != "ip2") {
			t.Fatalf("record = %+v", r)
		}
	}
	a.flush() // no path: stays in memory
	if got := a.snapshot("other"); len(got) != 0 {
		t.Fatalf("snapshot(other) = %+v", got)
	}

	// An unwritable path loses that flush, not the data.
	u := newAuditor(filepath.Join(t.TempDir(), "no", "dir", "audit.json"))
	u.record("r", "h", "ip", 443, "tls", "deny", 0, 0)
	u.flush()
	if len(u.snapshot("r")) != 1 {
		t.Fatal("the aggregate must survive a failed flush")
	}
}

func TestWriteAtomicAndReadJSONFaults(t *testing.T) {
	dir := t.TempDir()
	if err := writeAtomic(filepath.Join(dir, "bad"), make(chan int)); err == nil {
		t.Error("an unmarshalable value must fail")
	}
	if err := writeAtomic(filepath.Join(dir, "no", "dir", "f"), 1); err == nil {
		t.Error("a missing directory must fail")
	}
	path := filepath.Join(dir, "x.json")
	if err := writeAtomic(path, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
	var m map[string]int
	if err := readJSON(path, &m); err != nil || m["a"] != 1 {
		t.Fatalf("readJSON = %v, %v", m, err)
	}
}
