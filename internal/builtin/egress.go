package builtin

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed egress-defaults.txt
var egressDefaultsRaw string

// EgressDefaults is the compiled-in "sensible defaults" allowlist a project
// can OPT INTO (the New Project / edit form's "Add sensible defaults" button).
// Unlike the old behaviour these are NOT seeded into every project's allowlist
// — a project that wants them copies them into its own ExtraDomains, where they
// become visible, editable rows it can curate. The list is read-only mirrors
// (registries, OS package repos, dev-tool CDNs); see egress-defaults.txt.
//
// Blank lines and `#` comments in the source file are ignored; order is
// preserved (grouped by category) and duplicates are dropped.
var EgressDefaults = sync.OnceValue(func() []string {
	var out []string
	seen := map[string]bool{}
	for line := range strings.SplitSeq(egressDefaultsRaw, "\n") {
		host := strings.TrimSpace(line)
		if host == "" || strings.HasPrefix(host, "#") {
			continue
		}
		if !seen[host] {
			seen[host] = true
			out = append(out, host)
		}
	}
	return out
})
