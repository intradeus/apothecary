package main

// The extension gallery: a caching reverse proxy of Open VSX's VS Code
// marketplace surface, served to every run on :5002. A run's code-server is
// pointed at it (EXTENSIONS_GALLERY), so both the editor's own marketplace UI
// and the engine's "install the project's extensions" step go through it —
// inside allow-mode projects too, since the fetch is gateway-originated (the
// same policy carve-out as the registry mirror, registry.go).
//
// What is cached: responses whose path names an extension VERSION — the
// .vsix and its files (/api/{ns}/{name}/{ver}/file/…), /vscode/asset/… and
// /vscode/unpkg/… — immutable once published, so they are kept forever on the
// state volume and served from there first (a project's second run installs
// its extensions with no upstream fetch at all; the cache also survives an
// upstream outage). Queries (extensionquery, …/latest) pass through every
// time: they are what changes when an extension releases.
//
// Upstream answers queries with absolute URLs to itself; those are rewritten
// to this listener (as the client addressed it), so every follow-up request
// comes back through the cache. Storage redirects (open-vsx.org serves files
// from Azure blob storage) are followed here, never handed to the client,
// which could not reach them.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// galleryPort is where the gallery listens; the engine knows it as
// egressgw.GalleryPort.
const galleryPort = "5002"

type gallery struct {
	upstream string // e.g. https://open-vsx.org (no trailing slash)
	dir      string // cache directory; "" = no cache (pure proxy)
	client   *http.Client
}

func newGallery(upstream, dir string) *gallery {
	return &gallery{
		upstream: strings.TrimRight(upstream, "/"),
		dir:      dir,
		client: &http.Client{
			// Extensions run to hundreds of MB; bound the whole transfer, not
			// the headers alone.
			Timeout: 10 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// /vscode/item answers 302 to the marketplace WEBSITE, for the
				// client's own browser: pass it through. Everything else (file
				// storage) is followed here.
				if strings.HasPrefix(via[0].URL.Path, "/vscode/item") {
					return http.ErrUseLastResponse
				}
				if len(via) >= 10 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
	}
}

// cacheablePath matches a version-addressed (immutable) resource. The
// version segment must be a concrete one: "latest" and other aliases move. A
// platform-specific extension (openai.chatgpt, anthropic.claude-code — the
// big ones, with native binaries) names its target before the version:
// /api/{ns}/{name}/linux-x64/{ver}/file/….
var cacheablePath = regexp.MustCompile(`^/(?:api/[^/]+/[^/]+/(?:(?:win32|linux|alpine|darwin)-[a-z0-9]+/|web/|universal/)?([^/]+)/file/|vscode/(?:asset|unpkg)/[^/]+/[^/]+/([^/]+)/)`)

func (g *gallery) cacheable(r *http.Request) bool {
	if g.dir == "" || r.Method != http.MethodGet {
		return false
	}
	m := cacheablePath.FindStringSubmatch(r.URL.Path)
	if m == nil {
		return false
	}
	ver := m[1] + m[2] // exactly one group matched
	switch strings.ToLower(ver) {
	case "", "latest", "pre-release", "prerelease":
		return false
	}
	return true
}

// cachePath is the file a request's response is kept in (and the
// content-type beside it, ".ct").
func (g *gallery) cachePath(r *http.Request) string {
	h := sha256.Sum256([]byte(r.URL.Path + "?" + r.URL.RawQuery))
	return filepath.Join(g.dir, hex.EncodeToString(h[:]))
}

// rewritesBody reports a response whose JSON carries absolute upstream URLs.
func rewritesBody(r *http.Request) bool {
	p := r.URL.Path
	return strings.HasPrefix(p, "/vscode/gallery/") || strings.HasPrefix(p, "/vscode/gallery")
}

func (g *gallery) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/vscode/") && !strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost:
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cacheable := g.cacheable(r)
	if cacheable && g.serveCached(w, r) {
		return
	}

	up, err := http.NewRequestWithContext(r.Context(), r.Method, g.upstream+r.URL.RequestURI(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, vs := range r.Header {
		switch strings.ToLower(k) {
		case "host", "connection", "accept-encoding", "content-length":
			continue
		}
		up.Header[k] = vs
	}
	// Plain bodies: the JSON rewrite below needs to read them, and the cache
	// stores what was served.
	up.Header.Set("Accept-Encoding", "identity")
	resp, err := g.client.Do(up)
	if err != nil {
		slog.Warn("gallery", "method", r.Method, "path", r.URL.Path, "err", err)
		http.Error(w, "gallery upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for _, k := range []string{"Content-Type", "Cache-Control", "ETag", "Last-Modified", "Location", "Content-Disposition"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	if rewritesBody(r) && resp.StatusCode == http.StatusOK {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			http.Error(w, "gallery upstream: "+err.Error(), http.StatusBadGateway)
			return
		}
		body = bytes.ReplaceAll(body, []byte(g.upstream+"/"), []byte(g.self(r)+"/"))
		w.Header().Set("Content-Length", itoa(len(body)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}
	if v := resp.Header.Get("Content-Length"); v != "" {
		w.Header().Set("Content-Length", v)
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	if cacheable && resp.StatusCode == http.StatusOK {
		g.streamAndCache(w, r, resp)
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

// self is this listener as the client addressed it (http://<gw>:5002).
func (g *gallery) self(r *http.Request) string {
	host := r.Host
	if host == "" {
		host = net.JoinHostPort("127.0.0.1", galleryPort)
	}
	return "http://" + host
}

// serveCached answers from the cache; false when the file is not there.
func (g *gallery) serveCached(w http.ResponseWriter, r *http.Request) bool {
	p := g.cachePath(r)
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false
	}
	if ct, err := os.ReadFile(p + ".ct"); err == nil && len(ct) > 0 {
		w.Header().Set("Content-Type", strings.TrimSpace(string(ct)))
	}
	w.Header().Set("Content-Length", itoa(int(st.Size())))
	w.Header().Set("X-Apothecary-Cache", "hit")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, f)
	}
	return true
}

// streamAndCache sends the body to the client while writing it to a temp
// file, published (renamed) only once complete: a client that disconnects
// halfway leaves no partial cache entry.
func (g *gallery) streamAndCache(w http.ResponseWriter, r *http.Request, resp *http.Response) {
	if err := os.MkdirAll(g.dir, 0o755); err != nil {
		_, _ = io.Copy(w, resp.Body)
		return
	}
	p := g.cachePath(r)
	tmp, err := os.CreateTemp(g.dir, ".partial-*")
	if err != nil {
		_, _ = io.Copy(w, resp.Body)
		return
	}
	defer os.Remove(tmp.Name())
	n, copyErr := io.Copy(io.MultiWriter(w, tmp), resp.Body)
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		return
	}
	if cl := resp.ContentLength; cl >= 0 && n != cl {
		return // short read: not the whole file
	}
	_ = os.WriteFile(p+".ct", []byte(resp.Header.Get("Content-Type")), 0o644)
	_ = os.Rename(tmp.Name(), p)
}

// clear empties the cache (the build-cache wipe clears it too).
func (g *gallery) clear() error {
	if g.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(g.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(g.dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// serveGallery listens on :5002 (run links reach it; the input chain drops
// only the control port) and serves the proxy until the process ends.
func serveGallery(g *gallery) {
	srv := &http.Server{Addr: ":" + galleryPort, Handler: g, ReadHeaderTimeout: 30 * time.Second}
	slog.Info("gateway: extension gallery", "port", galleryPort, "upstream", g.upstream, "cache", g.dir)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("gateway: gallery", "err", err)
	}
}

// galleryUpstreamOK validates the --gallery-upstream flag.
func galleryUpstreamOK(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an http(s) URL")
	}
	return nil
}
