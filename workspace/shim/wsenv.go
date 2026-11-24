package main

// The workspace's environment, as the engine tells it (PUT /env): its size
// hints (APO_CPUS, APO_MEMORY_MIB) and the project's variables. The engine
// starts the sidecar without them and tells it the current set each time it
// reaches the sidecar, so what the sidecar starts — the harness, a terminal,
// an SSH session — gets the set as it is then, over the sidecar's own
// environment (the repository's containerEnv and remoteEnv), and a variable
// the project dropped is not left behind in it.

import (
	"maps"
	"net/http"
	"slices"
	"sync"
)

type workspaceEnv struct {
	mu   sync.Mutex
	vars map[string]string
}

// engineEnv is the set the engine last told this sidecar.
var engineEnv workspaceEnv

func (e *workspaceEnv) set(vars map[string]string) {
	e.mu.Lock()
	e.vars = maps.Clone(vars)
	e.mu.Unlock()
}

// over is base with the set laid over it: appended, sorted by name, so each
// variable's value is the set's (exec takes a name's last value).
func (e *workspaceEnv) over(base []string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := slices.Clip(base)
	for _, k := range slices.Sorted(maps.Keys(e.vars)) {
		out = append(out, k+"="+e.vars[k])
	}
	return out
}

// putEnv is PUT /env: {"env": {"NAME": "value", …}} replaces the set.
func putEnv(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Env map[string]string `json:"env"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	for k := range req.Env {
		if !validEnvName(k) {
			writeErrResp(w, http.StatusBadRequest, "not a variable name: "+k)
			return
		}
	}
	engineEnv.set(req.Env)
	w.WriteHeader(http.StatusNoContent)
}

// validEnvName: [A-Za-z_][A-Za-z0-9_]*, what the engine lets a project name.
func validEnvName(k string) bool {
	if k == "" {
		return false
	}
	for i, c := range k {
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
