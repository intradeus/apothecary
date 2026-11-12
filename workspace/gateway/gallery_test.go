package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// A fake Open VSX: a query answering with absolute URLs to itself, a
// version-addressed file (counted), a moving "latest", an item redirect.
func fakeOpenVSX(t *testing.T, fileHits *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("POST /vscode/gallery/extensionquery", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "golang.go") {
			http.Error(w, "unexpected query "+string(body), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"extensions":[{"versions":[{"files":[{"source":"`+srv.URL+`/api/golang/go/1.0.0/file/golang.go-1.0.0.vsix"}]}]}]}]}`)
	})
	mux.HandleFunc("GET /api/golang/go/1.0.0/file/golang.go-1.0.0.vsix", func(w http.ResponseWriter, r *http.Request) {
		fileHits.Add(1)
		// Storage lives elsewhere: a redirect the client never sees.
		http.Redirect(w, r, srv.URL+"/blob/golang.go-1.0.0.vsix", http.StatusFound)
	})
	mux.HandleFunc("GET /blob/golang.go-1.0.0.vsix", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "VSIX-BYTES")
	})
	mux.HandleFunc("GET /api/golang/go/latest/file/golang.go-latest.vsix", func(w http.ResponseWriter, r *http.Request) {
		fileHits.Add(1)
		_, _ = io.WriteString(w, "MOVING")
	})
	mux.HandleFunc("GET /vscode/item", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/extension/golang/go", http.StatusFound)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func galleryDo(g *gallery, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "10.90.0.1:5002"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	return rec
}

func TestGalleryRewritesQueryURLsToItself(t *testing.T) {
	var hits atomic.Int32
	up := fakeOpenVSX(t, &hits)
	g := newGallery(up.URL, t.TempDir())
	rec := galleryDo(g, http.MethodPost, "/vscode/gallery/extensionquery", `{"filters":[{"criteria":[{"filterType":7,"value":"golang.go"}]}]}`)
	if rec.Code != 200 {
		t.Fatalf("query: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"source":"http://10.90.0.1:5002/api/golang/go/1.0.0/file/golang.go-1.0.0.vsix"`) {
		t.Errorf("file URLs must point back at the gallery: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), up.URL) {
		t.Errorf("upstream URL leaked: %s", rec.Body)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("content-type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestGalleryCachesVersionedFilesAndFollowsStorageRedirects(t *testing.T) {
	var hits atomic.Int32
	up := fakeOpenVSX(t, &hits)
	dir := t.TempDir()
	g := newGallery(up.URL, dir)
	const file = "/api/golang/go/1.0.0/file/golang.go-1.0.0.vsix"
	for i := range 2 {
		rec := galleryDo(g, http.MethodGet, file, "")
		if rec.Code != 200 || rec.Body.String() != "VSIX-BYTES" {
			t.Fatalf("fetch %d: %d %q", i, rec.Code, rec.Body)
		}
		if rec.Header().Get("Content-Type") != "application/octet-stream" {
			t.Errorf("fetch %d content-type = %q", i, rec.Header().Get("Content-Type"))
		}
	}
	if hits.Load() != 1 {
		t.Errorf("upstream hit %d times for one versioned file, want 1 (cached)", hits.Load())
	}
	// The cache outlives upstream.
	up.Close()
	if rec := galleryDo(g, http.MethodGet, file, ""); rec.Code != 200 || rec.Body.String() != "VSIX-BYTES" || rec.Header().Get("X-Apothecary-Cache") != "hit" {
		t.Errorf("offline: %d %q %q", rec.Code, rec.Body, rec.Header().Get("X-Apothecary-Cache"))
	}
	if err := g.clear(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("clear left %v", entries)
	}
}

func TestGalleryCacheablePaths(t *testing.T) {
	g := newGallery("https://open-vsx.org", t.TempDir())
	// Vendor-neutral fixtures on purpose (repo:lint): the shapes are what
	// matter — a plain, a platform-specific (@linux-x64), an alpine build, a
	// universal one, the asset form, and the moving "latest".
	for p, want := range map[string]bool{
		"/api/golang/go/1.0.0/file/golang.go-1.0.0.vsix":                             true,
		"/api/acme/widgets/linux-x64/26.5.1/file/acme.widgets-26.5.1@linux-x64.vsix": true,
		"/api/acme/helper/alpine-arm64/2.1.3/file/acme.helper-2.1.3.vsix":            true,
		"/api/redhat/java/universal/1.2.0/file/redhat.java-1.2.0.vsix":               true,
		"/vscode/asset/golang/go/1.0.0/Microsoft.VisualStudio.Services.VSIXPackage":  true,
		"/api/golang/go/latest/file/golang.go-latest.vsix":                           false,
		"/api/acme/widgets/linux-x64/latest/file/acme.widgets.vsix":                  false,
		"/api/acme/widgets/linux-x64":                                                false,
	} {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		if got := g.cacheable(r); got != want {
			t.Errorf("cacheable(%s) = %v, want %v", p, got, want)
		}
	}
}

func TestGalleryNeverCachesMovingVersions(t *testing.T) {
	var hits atomic.Int32
	up := fakeOpenVSX(t, &hits)
	g := newGallery(up.URL, t.TempDir())
	for range 2 {
		if rec := galleryDo(g, http.MethodGet, "/api/golang/go/latest/file/golang.go-latest.vsix", ""); rec.Code != 200 {
			t.Fatalf("latest: %d", rec.Code)
		}
	}
	if hits.Load() != 2 {
		t.Errorf("'latest' served from cache (%d upstream hits)", hits.Load())
	}
}

func TestGalleryPassesItemRedirectAndRefusesTheRest(t *testing.T) {
	var hits atomic.Int32
	up := fakeOpenVSX(t, &hits)
	g := newGallery(up.URL, "")
	rec := galleryDo(g, http.MethodGet, "/vscode/item?itemName=golang.go", "")
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), up.URL+"/extension/") {
		t.Errorf("item: %d %q (the marketplace website is the browser's to open)", rec.Code, rec.Header().Get("Location"))
	}
	if rec := galleryDo(g, http.MethodGet, "/healthz", ""); rec.Code != 404 {
		t.Errorf("only the marketplace surface is proxied: /healthz → %d", rec.Code)
	}
	if rec := galleryDo(g, http.MethodDelete, "/api/x/y/1/file/z", ""); rec.Code != 405 {
		t.Errorf("DELETE → %d", rec.Code)
	}
}

func TestGalleryUpstreamOK(t *testing.T) {
	for in, ok := range map[string]bool{
		"https://open-vsx.org": true,
		"http://10.0.0.1:3000": true,
		"":                     false,
		"open-vsx.org":         false,
		"ftp://open-vsx.org":   false,
		"https://":             false,
		"http://bad host":      false,
	} {
		if err := galleryUpstreamOK(in); (err == nil) != ok {
			t.Errorf("galleryUpstreamOK(%q) = %v", in, err)
		}
	}
}

func TestGalleryUpstreamDownIsABadGateway(t *testing.T) {
	up := httptest.NewServer(http.NotFoundHandler())
	url := up.URL
	up.Close()
	g := newGallery(url, t.TempDir())
	rec := galleryDo(g, "GET", "/vscode/asset/ns/name/1.0.0/Microsoft.VisualStudio.Services.VSIXPackage", "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "gallery upstream") {
		t.Fatalf("GET = %d %q", rec.Code, rec.Body.String())
	}
}

// A cached file does not need the upstream at all.
func TestGalleryServesTheCacheWithUpstreamDown(t *testing.T) {
	var hits atomic.Int32
	up := fakeOpenVSX(t, &hits)
	g := newGallery(up.URL, t.TempDir())
	path := "/api/golang/go/1.0.0/file/golang.go-1.0.0.vsix"
	first := galleryDo(g, "GET", path, "")
	if first.Code != 200 {
		t.Fatalf("first GET = %d", first.Code)
	}
	up.Close()
	second := galleryDo(g, "GET", path, "")
	if second.Code != 200 || second.Header().Get("X-Apothecary-Cache") != "hit" {
		t.Fatalf("GET = %d, cache %q", second.Code, second.Header().Get("X-Apothecary-Cache"))
	}
	if second.Body.String() != first.Body.String() {
		t.Fatalf("cached body = %q, want %q", second.Body.String(), first.Body.String())
	}
	if second.Header().Get("Content-Type") != first.Header().Get("Content-Type") {
		t.Errorf("content type = %q, want %q", second.Header().Get("Content-Type"), first.Header().Get("Content-Type"))
	}
}

func upstreamServing(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	return up
}

// Only a clean, complete 200 may enter the immutable cache: anything else would
// be served forever.
func TestGalleryDoesNotCacheFailuresOrPartials(t *testing.T) {
	const path = "/api/ns/name/1.0.0/file/x.vsix"
	cases := map[string]http.HandlerFunc{
		"an upstream error": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		},
		"a truncated transfer": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("short"))
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			g := newGallery(upstreamServing(t, h).URL, dir)
			galleryDo(g, "GET", path, "")
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				t.Errorf("cache holds %s", e.Name())
			}
		})
	}
}

// The cache being unavailable degrades to a pure proxy.
func TestGalleryStreamsWhenTheCacheCannotBeWritten(t *testing.T) {
	up := upstreamServing(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("vsix-bytes")) })

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	g := newGallery(up.URL, filepath.Join(blocker, "cache"))
	rec := galleryDo(g, "GET", "/api/ns/name/1.0.0/file/x.vsix", "")
	if rec.Code != 200 || rec.Body.String() != "vsix-bytes" {
		t.Fatalf("GET = %d %q", rec.Code, rec.Body.String())
	}
}

func TestGalleryPassesMethodsAndHeaders(t *testing.T) {
	var got http.Header
	var method, body string
	up := upstreamServing(t, func(w http.ResponseWriter, r *http.Request) {
		got, method = r.Header.Clone(), r.Method
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("X-Internal", "no")
		_, _ = w.Write([]byte("answer"))
	})
	g := newGallery(up.URL, "")

	req := httptest.NewRequest("POST", "/api/-/query?x=1", strings.NewReader("payload"))
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Client", "code-server")
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "answer" {
		t.Fatalf("POST = %d %q", rec.Code, rec.Body.String())
	}
	if method != "POST" || body != "payload" {
		t.Errorf("upstream saw %s %q", method, body)
	}
	if got.Get("X-Client") != "code-server" {
		t.Errorf("client header lost: %v", got)
	}
	if got.Get("Accept-Encoding") != "identity" {
		t.Errorf("Accept-Encoding = %q: compressed bodies cannot be rewritten or cached", got.Get("Accept-Encoding"))
	}
	if rec.Header().Get("ETag") != `"abc"` || rec.Header().Get("X-Internal") != "" {
		t.Errorf("response headers = %v", rec.Header())
	}

	head := galleryDo(g, "HEAD", "/vscode/gallery/items", "")
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Errorf("HEAD = %d, %d bytes", head.Code, head.Body.Len())
	}
	if rec := galleryDo(g, "DELETE", "/api/x", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE = %d", rec.Code)
	}
	if rec := galleryDo(g, "GET", "/etc/passwd", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET outside the surface = %d", rec.Code)
	}
}

// Whatever the client asks for lands on the configured upstream, path and query
// intact.
func TestGalleryStaysOnItsUpstream(t *testing.T) {
	var seen atomic.Pointer[string]
	up := upstreamServing(t, func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.RequestURI()
		seen.Store(&p)
	})
	g := newGallery(up.URL+"/", "")
	if g.upstream != up.URL {
		t.Fatalf("upstream = %q, want the slash trimmed", g.upstream)
	}
	galleryDo(g, "GET", "/vscode/x?y=1", "")
	if p := seen.Load(); p == nil || *p != "/vscode/x?y=1" {
		t.Fatalf("upstream saw %v", p)
	}
}

func TestGalleryBadUpstreamURLIsABadGateway(t *testing.T) {
	g := newGallery("http://bad\x7fhost", "")
	if rec := galleryDo(g, "GET", "/api/x", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("GET = %d", rec.Code)
	}
}

func TestGallerySelfDefaultsToItsOwnPort(t *testing.T) {
	g := newGallery("http://u", "")
	r := httptest.NewRequest("GET", "/x", nil)
	r.Host = ""
	if got := g.self(r); got != "http://127.0.0.1:"+galleryPort {
		t.Fatalf("self = %q", got)
	}
	r.Host = "gw:5002"
	if got := g.self(r); got != "http://gw:5002" {
		t.Fatalf("self = %q", got)
	}
}

func TestGalleryClear(t *testing.T) {
	if err := newGallery("http://u", "").clear(); err != nil {
		t.Errorf("no cache: %v", err)
	}
	if err := newGallery("http://u", filepath.Join(t.TempDir(), "missing")).clear(); err != nil {
		t.Errorf("missing cache: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := newGallery("http://u", dir).clear(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left %d entries", len(entries))
	}
}

func TestGalleryRedirectLimit(t *testing.T) {
	var n atomic.Int32
	up := upstreamServing(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Redirect(w, r, "/api/loop", http.StatusFound)
	})
	g := newGallery(up.URL, "")
	if rec := galleryDo(g, "GET", "/api/loop", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("GET = %d", rec.Code)
	}
	if got := n.Load(); got != 10 {
		t.Fatalf("followed %d redirects, want the cap", got)
	}
}

func TestGalleryServeCached(t *testing.T) {
	dir := t.TempDir()
	up := upstreamServing(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("fresh")) })
	g := newGallery(up.URL, dir)
	r := httptest.NewRequest("GET", "/api/ns/name/1.0.0/file/x.vsix", nil)
	if err := os.WriteFile(g.cachePath(r), []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No .ct beside it: still served, just without a type.
	rec := httptest.NewRecorder()
	if !g.serveCached(rec, r) {
		t.Fatal("a cached file must be served")
	}
	if rec.Body.String() != "cached" || rec.Header().Get("Content-Length") != "6" {
		t.Fatalf("served %q, headers %v", rec.Body.String(), rec.Header())
	}
	if g.serveCached(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/ns/name/2.0.0/file/x.vsix", nil)) {
		t.Fatal("an absent entry must miss")
	}
}
