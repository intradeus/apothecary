package assistant

import (
	"maps"
	"path"

	"apothecary/internal/ports/assistantport"
)

// profile is how a harness runs as an assistant session: its ACP command
// (absolute: the session has no PATH lookup through a shell, and the
// musl builds are executed directly), the other programs it runs, its
// environment, its session mode, and the settings that keep it from
// offering the tools the session has no use for (its shell, its writes,
// web fetches). Landlock and the gateway are what hold; this keeps the
// agent from reaching for tools that would only fail.
type profile struct {
	argv  []string
	exec  []string
	envs  map[string]string
	mode  string
	files func(home string) []homeFile
}

type homeFile struct{ path, content string }

func (p profile) env() map[string]string { return maps.Clone(p.envs) }

var profiles = map[string]profile{
	"claude-code": {
		argv: []string{path.Join(assistantport.ToolsMusl, "libexec/claude-agent-acp")},
		exec: []string{path.Join(assistantport.ToolsMusl, "libexec/claude")},
		envs: map[string]string{
			"CLAUDE_CODE_EXECUTABLE":                   path.Join(assistantport.ToolsMusl, "libexec/claude"),
			"IS_SANDBOX":                               "1",
			"DISABLE_AUTOUPDATER":                      "1",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			"ENABLE_TOOL_SEARCH":                       "false",
		},
		// No permission prompts: nothing here asks a person mid-turn, and
		// the tools that would change something are denied below.
		mode: "bypassPermissions",
		files: func(home string) []homeFile {
			return []homeFile{{path.Join(home, ".claude", "settings.json"), `{"permissions":{"deny":["Bash","Edit","Write","NotebookEdit","WebFetch","WebSearch","Task"]}}`}}
		},
	},
	"opencode": {
		argv: []string{path.Join(assistantport.ToolsMusl, "libexec/opencode"), "acp"},
		envs: map[string]string{},
		files: func(home string) []homeFile {
			return []homeFile{{path.Join(home, ".config", "opencode", "opencode.json"),
				`{"tools":{"bash":false,"edit":false,"write":false,"patch":false,"webfetch":false,"task":false},"autoupdate":false}`}}
		},
	},
}

// Runs reports a harness kind the assistant runs on.
func Runs(kind string) bool {
	_, ok := profiles[kind]
	return ok
}
