# ask_human mechanics ride the role, not the flow (2026-10-28)

The builder template's planner and builder prompts each carried a paragraph on
how `ask_human` works: that the harness may list it under a prefixed name, that
it waits for the human, that a guess is worse than a wait. Two things were
wrong with that. It is platform mechanics in flow content — every agent stage
can ask (FLOWLANG §9), so every flow author would have to paste the same
paragraph, and a flow that forgot it would re-run the namespacing incident
above. And it was mostly a copy of the tool's own description, which the model
already reads (`workspace/shim/askhuman.go`); the one fact a tool description
cannot carry is *the name the harness lists it under*, and that is a harness
fact, not a flow fact.

So the two renderers that turn a spec's prompt into what the agent reads now
append a standing section after it, `acpagent.AskHumanRules(listedAs)`:
opencode's agent definition (`agentMarkdown`), which knows the exact name
(`ask_human_ask_human`, server_tool), and the in-band `# ROLE` preamble codex
and omp get (`acpagent.RoleText`), which does not and says it may be prefixed.
The section names the three types and that the call waits; form and decline
semantics stay in the tool description, where they were. The Ask pane's agent
is skipped — it has no ask_human (opencode denies `ask_human_*`, a read-only
session gets no profile MCP server) and its prompt says never to ask.

The flows now state only their policy: "ask the human (type=clarification)
BEFORE writing the plan rather than guessing", "ask the human (type=blocker)
instead of improvising a redesign". The agent editor's "what a stage adds"
preview lists the section, so what the YAML shows and what the agent reads
stay the same thing. `agentspec.Hash` is unchanged: it addresses the authored
prompt, as it did for the READ-ONLY lines the role already appended.
