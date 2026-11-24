package confstore

import (
	"regexp"
	"sort"
	"strings"

	"apothecary/internal/account/providers"
)

// Bounds of a project's workspace environment (WorkspaceConfig.Env): it
// rides every exec's argv, so it stays small.
const (
	MaxEnvVars     = 64
	MaxEnvValueLen = 4 << 10
)

// envNameRe is a variable name a shell can export.
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedEnvPrefixes are the engine's own namespaces: the run's variables
// (APOTHECARY_RUN_ID, APOTHECARY_CMD, …) and the size hints (APO_CPUS, …).
var reservedEnvPrefixes = []string{"APOTHECARY_", "APO_"}

// reservedEnvNames are variables the engine sets in a workspace itself:
// what a project value would silently break (PATH, HOME, the terminal's),
// the harness's launch and broker variables, and every provider key
// provider.env carries (reservedEnv adds those).
var reservedEnvNames = []string{
	"PATH", "HOME", "TERM", "COLORTERM",
	"DEFAULT_AUTH_REQUEST", "OMP_AUTH_BROKER_URL", "OMP_AUTH_BROKER_TOKEN",
}

// reservedEnv is reservedEnvNames plus each provider's key variable.
func reservedEnv() map[string]bool {
	out := map[string]bool{}
	for _, n := range reservedEnvNames {
		out[n] = true
	}
	for _, p := range providers.All() {
		for _, n := range p.Harnesses {
			if n.Env != "" {
				out[n.Env] = true
			}
		}
	}
	return out
}

// validateEnv checks a project's workspace environment: names a shell can
// export, none of the engine's own, within the bounds. Names are trimmed;
// an empty map is stored as none.
func validateEnv(w *WorkspaceConfig) error {
	if len(w.Env) == 0 {
		w.Env = nil
		return nil
	}
	if len(w.Env) > MaxEnvVars {
		return invalidf("project: workspace.env: at most %d variables, got %d", MaxEnvVars, len(w.Env))
	}
	reserved := reservedEnv()
	clean := make(map[string]string, len(w.Env))
	names := make([]string, 0, len(w.Env))
	for name := range w.Env {
		names = append(names, name)
	}
	sort.Strings(names) // the first offender is the same on every save
	for _, raw := range names {
		name, value := strings.TrimSpace(raw), w.Env[raw]
		if !envNameRe.MatchString(name) {
			return invalidf("project: workspace.env: %q is not a variable name (letters, digits and _, not starting with a digit)", name)
		}
		for _, p := range reservedEnvPrefixes {
			if strings.HasPrefix(strings.ToUpper(name), p) {
				return invalidf("project: workspace.env: %s: names starting with %s are the engine's own", name, p)
			}
		}
		if reserved[name] {
			return invalidf("project: workspace.env: %s is set by the engine in every workspace and cannot be replaced", name)
		}
		if len(value) > MaxEnvValueLen {
			return invalidf("project: workspace.env: %s: a value is at most %d bytes, got %d", name, MaxEnvValueLen, len(value))
		}
		if strings.ContainsRune(value, 0) {
			return invalidf("project: workspace.env: %s: a value cannot hold a NUL byte", name)
		}
		if _, dup := clean[name]; dup {
			return invalidf("project: workspace.env: %s is set twice", name)
		}
		clean[name] = value
	}
	w.Env = clean
	return nil
}
