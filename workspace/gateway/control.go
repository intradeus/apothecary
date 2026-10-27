package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The control API: how the engine drives a gateway it cannot share a
// filesystem with (Kubernetes) — and, so there is one protocol, how it drives
// every gateway. Bearer-token auth; plain HTTP on a port only the engine can
// reach (a published loopback port on docker, a network-policy-guarded
// Service on Kubernetes).
//
//	GET  /healthz              liveness, no auth
//	GET  /policy               the policy being enforced
//	PUT  /policy               replace it (applied before the response)
//	GET  /routes               run routes and WireGuard peers
//	PUT  /runs/{run}           attach a run: {subnet, peer?}
//	DELETE /runs/{run}         detach it
//	GET  /denials[?run=]       recorded denials
//	GET  /audit[?run=]         the aggregated egress trail
//	GET  /buildcache           {bytes}
//	DELETE /buildcache         wipe it (the registry child restarts clean)

const maxControlBody = 4 << 20

func controlToken(file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	if t := strings.TrimSpace(os.Getenv("APO_GW_TOKEN")); t != "" {
		return t, nil
	}
	return "", errors.New("no token: set --control-token-file or APO_GW_TOKEN")
}

func (g *gateway) serveControl(addr, token string) {
	srv := &http.Server{Addr: addr, Handler: g.controlHandler(token), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("gateway: control API on %s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("gateway: control API: %v", err)
	}
}

func (g *gateway) controlHandler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /policy", func(w http.ResponseWriter, _ *http.Request) {
		f := File{Mode: ModeAllow}
		if p := g.file.Load(); p != nil {
			f = *p
		}
		f.Mode = f.normMode()
		writeJSON(w, f)
	})
	mux.HandleFunc("PUT /policy", func(w http.ResponseWriter, r *http.Request) {
		var f File
		if !readBody(w, r, &f) {
			return
		}
		if f.Mode == "" {
			f.Mode = ModeAllow
		}
		g.cmu.Lock()
		defer g.cmu.Unlock()
		if err := g.persist(g.policyPath, f); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := g.setPolicy(f); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /routes", func(w http.ResponseWriter, _ *http.Request) {
		g.cmu.Lock()
		defer g.cmu.Unlock()
		writeJSON(w, g.currentRoutes())
	})
	mux.HandleFunc("PUT /runs/{run}", func(w http.ResponseWriter, r *http.Request) {
		var rr RunRoute
		if !readBody(w, r, &rr) {
			return
		}
		if _, _, err := net.ParseCIDR(rr.Subnet); err != nil {
			http.Error(w, "subnet: "+err.Error(), http.StatusBadRequest)
			return
		}
		g.updateRoutes(w, func(rt *Routes) {
			run := r.PathValue("run")
			rt.RunSubnets[run] = rr.Subnet
			if rr.Peer != nil {
				rt.Peers[run] = *rr.Peer
			} else {
				delete(rt.Peers, run)
			}
		})
	})
	mux.HandleFunc("DELETE /runs/{run}", func(w http.ResponseWriter, r *http.Request) {
		g.updateRoutes(w, func(rt *Routes) {
			delete(rt.RunSubnets, r.PathValue("run"))
			delete(rt.Peers, r.PathValue("run"))
		})
	})
	mux.HandleFunc("GET /denials", func(w http.ResponseWriter, r *http.Request) {
		run := r.URL.Query().Get("run")
		g.dmu.Lock()
		out := make([]Denial, 0, len(g.denials))
		for _, d := range g.denials {
			if run == "" || d.Run == run {
				out = append(out, *d)
			}
		}
		g.dmu.Unlock()
		writeJSON(w, DenialsFile{Denials: out})
	})
	mux.HandleFunc("GET /audit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, AuditFile{Records: g.audit.snapshot(r.URL.Query().Get("run"))})
	})
	mux.HandleFunc("GET /buildcache", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, BuildCacheInfo{Bytes: dirSize(g.buildCacheDir)})
	})
	mux.HandleFunc("DELETE /buildcache", func(w http.ResponseWriter, _ *http.Request) {
		if err := g.clearBuildCache(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// setPolicy installs f as the live policy (the same path a file reload takes).
func (g *gateway) setPolicy(f File) error {
	if g.policyPath != "" {
		return g.loadPolicy() // reads back what persist just wrote
	}
	return g.installPolicy(f)
}

// currentRoutes reads the routes as last applied (file when there is one).
func (g *gateway) currentRoutes() Routes {
	var rt Routes
	if g.routesPath != "" {
		_ = readJSON(g.routesPath, &rt)
	} else if p := g.routesMem.Load(); p != nil {
		rt = *p
	}
	if rt.RunSubnets == nil {
		rt.RunSubnets = map[string]string{}
	}
	if rt.Peers == nil {
		rt.Peers = map[string]Peer{}
	}
	return rt
}

func (g *gateway) updateRoutes(w http.ResponseWriter, mutate func(*Routes)) {
	g.cmu.Lock()
	defer g.cmu.Unlock()
	rt := g.currentRoutes()
	mutate(&rt)
	if err := g.persist(g.routesPath, rt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	g.applyRoutes(rt)
	w.WriteHeader(http.StatusNoContent)
}

// persist writes v to path when the gateway keeps state on disk.
func (g *gateway) persist(path string, v any) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeAtomic(path, v)
}

func (g *gateway) clearBuildCache() error {
	entries, err := os.ReadDir(g.buildCacheDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(g.buildCacheDir, e.Name())); err != nil {
			return err
		}
	}
	// Bump the config's mtime: the supervisor restarts the registry child, so
	// it stops serving manifests whose blobs are gone.
	if g.buildCacheConfig != "" {
		now := time.Now()
		_ = os.Chtimes(g.buildCacheConfig, now, now)
	}
	return nil
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func readBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlBody))
	if err := dec.Decode(v); err != nil {
		http.Error(w, "body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
