package acpagent

import (
	"fmt"
	"strings"

	"apothecary/internal/ports/agentport"
)

// askHumanTool is the MCP tool's own name — what the shim registers it as
// (workspace/shim/askhuman.go) and the server it rides under, so a harness
// that namespaces MCP tools by server lists it as ask_human_ask_human.
const askHumanTool = "ask_human"

// AskHumanServer is ask_human as a session MCP server (Profile.MCPServers),
// for a harness whose sessions are the surest way to reach it (omp reads no
// MCP config in ACP mode; Claude Code's bridge forwards session servers
// whatever settings sources it loads). The agent marker is set explicitly:
// ask-human offers its tool only below an agent, and this does not hang on
// the harness passing its own environment down (session-only, so no human's
// harness sees it).
func AskHumanServer() agentport.MCPServer {
	return agentport.MCPServer{
		Name:    askHumanTool,
		Command: []string{"/opt/apothecary/shim", "ask-human"},
		Env:     map[string]string{"APOTHECARY_AGENT": "1"},
	}
}

// AskHumanRules is the standing section every agent's role ends with
// (FLOWLANG §9): how asking the human works, so a flow's prompt states
// only WHEN its agent should ask. It is platform mechanics, not flow
// content — the tool's own description already says most of it, but the
// one thing a description cannot say is the name the harness lists the
// tool under, and that is exactly what made a model conclude the tool was
// missing (docs/notes, the opencode namespacing incident).
//
// listedAs is the name the harness lists the tool under, when the renderer
// knows it ("" when it does not: the text then says it may be prefixed).
func AskHumanRules(listedAs string) string {
	name := fmt.Sprintf("Your tools include %s; the harness may list it under a prefixed name such as %s_%s.",
		askHumanTool, askHumanTool, askHumanTool)
	if listedAs != "" && listedAs != askHumanTool {
		name = fmt.Sprintf("Your tools include %s, listed as %s.", askHumanTool, listedAs)
	} else if listedAs == askHumanTool {
		name = fmt.Sprintf("Your tools include %s.", askHumanTool)
	}
	var b strings.Builder
	b.WriteString("# ASKING THE HUMAN\n")
	b.WriteString(name)
	b.WriteString(" Call it under the name your tool list shows, and never conclude it is missing because the bare name failed.\n")
	b.WriteString("Use it when you are blocked: a requirement you cannot settle from your prompt and the repository (type=clarification), an obstacle you cannot resolve within the task's intent (type=blocker), or an action that needs approval (type=permission).\n")
	b.WriteString("It waits for the human however long that takes and returns their answer: a guess is worse than a wait, so never work around a slow answer or carry on as if you already had one.\n")
	return b.String()
}
