package analytics

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

// The built-in dashboards ship as JSON — the same shape a person saves —
// so editing one is editing a document, and duplicating one is a copy.
//
//go:embed builtin/*.json
var builtinFS embed.FS

// builtinOrder is the order the built-ins list in (by file name).
var builtinOrder = []string{"overview", "compare-flows", "compare-skillsets", "stages", "humans", "tickets"}

const builtinPrefix = "builtin-"

func isBuiltin(id string) bool { return strings.HasPrefix(id, builtinPrefix) }

var builtinDashboards = mustLoadBuiltins()

func mustLoadBuiltins() []Dashboard {
	out := make([]Dashboard, 0, len(builtinOrder))
	for _, name := range builtinOrder {
		raw, err := builtinFS.ReadFile("builtin/" + name + ".json")
		if err != nil {
			panic(err)
		}
		var d Dashboard
		if err := json.Unmarshal(raw, &d); err != nil {
			panic(fmt.Sprintf("builtin/%s.json: %v", name, err))
		}
		if d.ID != builtinPrefix+name {
			panic(fmt.Sprintf("builtin/%s.json: id %q, want %q", name, d.ID, builtinPrefix+name))
		}
		d.Builtin = true
		out = append(out, d)
	}
	return out
}

func builtinDashboard(id string) (Dashboard, bool) {
	for _, d := range builtinDashboards {
		if d.ID == id {
			return cloneDashboard(d), true
		}
	}
	return Dashboard{}, false
}

func builtinScorecard(id string) (Scorecard, bool) {
	for _, s := range builtinScorecards {
		if s.ID == id {
			s.Terms = append([]Term(nil), s.Terms...)
			return s, true
		}
	}
	return Scorecard{}, false
}

// cloneDashboard copies the slices a caller might change, so a built-in
// handed out is never the shared one.
func cloneDashboard(d Dashboard) Dashboard {
	raw, err := json.Marshal(d)
	if err != nil {
		panic(err)
	}
	var out Dashboard
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}
