package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	// Vendor-neutral fixtures on purpose (lint:models): the shapes are what
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
