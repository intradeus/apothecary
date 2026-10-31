package flow

import (
	"encoding/json"
	"slices"

	"apothecary/internal/actionspec"
	"apothecary/internal/agentspec"
	"apothecary/internal/signalspec"
)

// Schema is the JSON Schema of a flow document as it is written (FLOWLANG
// §1) — the YAML with its shorthand: `on: mr_conflicts`, `on_fail: debug`, a
// parameter's bare default. Editors validate, complete and document from it
// as a person types; the parser stays the authority (a schema cannot say that
// a parameter's enum fits a field, or that a route stays in its chain). Its
// lists — stage types, signals, actions and their inputs, start policies —
// come from the registries, so it never names what the parser refuses.
func Schema() json.RawMessage {
	b, err := json.Marshal(schema())
	if err != nil {
		panic(err) // a literal of maps and slices always marshals
	}
	return b
}

type obj = map[string]any

func schema() obj {
	slug := obj{"type": "string", "pattern": `^[a-z][a-z0-9_-]*$`}
	paramRef := obj{"type": "string", "pattern": `^\$[a-z][a-z0-9_]*$`, "description": "a parameter, declared under parameters"}
	str := func(desc string) obj { return obj{"type": "string", "description": desc} }
	intOrParam := func(desc string) obj {
		return obj{"description": desc, "anyOf": []any{obj{"type": "integer"}, paramRef}}
	}
	stringsOf := func(desc string, values ...string) obj {
		return obj{"type": "string", "enum": values, "description": desc}
	}

	var observed, signalSlugs []string
	for _, s := range signalspec.Registry() {
		signalSlugs = append(signalSlugs, s.Slug)
		if s.Observed() {
			observed = append(observed, s.Slug)
		}
	}
	var actionSlugs []string
	for _, a := range actionspec.Registry() {
		actionSlugs = append(actionSlugs, a.Slug)
	}
	starts := []string{string(StartAuto), string(StartConfirm), string(StartManual)}
	terminals := []string{TerminalEnd, TerminalDone, TerminalAbort, ReservedDebug, ReservedFail}

	// A route: a bare target (`on_fail: debug`) or a chain of links.
	edge := obj{
		"description": "where the run goes: a stage of the same chain (with max), or end, done, abort, debug — fail inside a sequence",
		"anyOf": []any{
			obj{"type": "string", "examples": terminals},
			obj{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"to"},
				"properties": obj{
					"to":   str("a stage of the same chain, or end, done, abort, debug — fail inside a sequence"),
					"max":  obj{"type": "integer", "description": "how many times this link may send the run there (-1: no limit, where a person decides each time)"},
					"then": obj{"$ref": "#/$defs/edge", "description": "where the run goes once this link is spent"},
				},
			},
		},
	}
	allTypes := append(slices.Clone(agentspec.FileTypes), agentspec.ValueTypes...)
	typeOrSchema := []any{obj{"required": []string{"type"}}, obj{"required": []string{"schema"}}}
	ports := func(desc, ref string) obj {
		return obj{"type": "object", "description": desc, "additionalProperties": obj{"$ref": "#/$defs/" + ref}}
	}
	portIn := obj{
		"type": "object", "additionalProperties": false, "required": []string{"from"}, "anyOf": typeOrSchema,
		"properties": obj{
			"from":        obj{"type": "string", "description": "where it comes from: a path some stage writes, repo:<path> (a file of the repository), a value the engine provides, or run.service.<name> (a running service's address — the name may be a $parameter)", "examples": append(slices.Clone(EngineSources), ServiceSourcePrefix+"$app")},
			"type":        stringsOf("what it is: a file type, or a value the engine provides", allTypes...),
			"schema":      str("a schema declared under schemas, which the file holds"),
			"description": str("what it is for — documentation"),
			"optional":    obj{"type": "boolean", "description": "it may be empty: an edge source, or a file not written on every route here"},
		},
	}
	portOut := obj{
		"type": "object", "additionalProperties": false, "required": []string{"to"}, "anyOf": typeOrSchema,
		"properties": obj{
			"to":          str("the path it is written to, relative to the workspace"),
			"type":        stringsOf("what the file is", agentspec.FileTypes...),
			"schema":      str("a schema declared under schemas, which the file holds"),
			"description": str("what it is for — documentation"),
		},
	}
	stage := obj{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"id"},
		"properties": obj{
			"id":          withDesc(slug, "the stage's id: lowercase, unique in its chain (or its sequence)"),
			"type":        stringsOf("what the stage is (a sequence stage has none: it names a sequence)", string(StageAgent), string(StageCommand), string(StageHumanGate), string(StageAction), string(StageWait), string(StageService)),
			"sequence":    str("run this sequence in the stage's place (a stage that does has no type)"),
			"description": str("what the stage is for — documentation only"),
			"run_if":      str("run only when this holds: run.ticket.tracker, run.subject or a bool $parameter — each may be negated, `not x` — or run.ticket.change_type == (or !=) feat, fix, hotfix, perf, docs or chore"),
			"agent":       str("agent: the agent it runs, declared under agents"),
			"model":       str("agent: required — the model, provider/model, or a $parameter ($model); stages sharing a model parameter share a harness"),
			"harness":     str("agent: required — the harness kind, or a $parameter ($harness)"),
			"max_turns":   intOrParam("agent: the turn budget (default: the max_turns parameter)"),
			"timeout":     str("how long it may take: a duration (30m) or a $parameter — agent and command (default: the timeout parameter), wait (default: forever), service (how long a start may take to be healthy; default: the service's own)"),
			"input":       ports("what it reads, each port with where it comes from and what it is (a human_gate: the files and folders the person reviews, and at most one link)", "port_in"),
			"output":      ports("what it writes, each port with where it goes and what it is", "port_out"),
			"with":        obj{"type": "object", "description": "action: its settings — literals, or $parameters", "additionalProperties": obj{"type": "string"}},
			"run":         str("command: a script, run in the workspace"),
			"run_ref":     str("command: one of the project's commands, or a $parameter naming one"),
			"each_repo":   obj{"type": "boolean", "description": "command: run once per repository of a multi-repo run"},
			"question":    str("human_gate: what the person is asked"),
			"action":      stringsOf("action: the action it performs", actionSlugs...),
			"for":         stringsOf("wait: the signal it waits for", observed...),
			"service":     str("service: one of the project's services, or a $parameter naming one (service: $app)"),
			"do":          stringsOf("service: what to do with it (default: start — and wait until it is healthy)", ServiceDos...),
			"on_success":  str("where the chain goes once it succeeds: a stage of the same chain, end, done or abort (default: the next stage); a gate's or a verdict's approval carries its comments there as edge.verdict"),
			"on_fail":     obj{"$ref": "#/$defs/edge", "description": "where the run goes when it fails — carries edge.failure"},
			"on_reject":   obj{"$ref": "#/$defs/edge", "description": "agent (with a verdict) or human_gate: where the run goes on a rejection — carries edge.verdict"},
			"on_timeout":  obj{"$ref": "#/$defs/edge", "description": "agent, command, wait or service: where the run goes when it times out (default: on_fail)"},
		},
		"allOf": stageRules(),
	}
	wait := obj{
		"description": "what opens the chain: manual (a person, from the run page), or a signal observed on the run's merge requests",
		"anyOf": []any{
			obj{"type": "string", "enum": signalSlugs},
			obj{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"signal"},
				"properties": obj{
					"signal":    obj{"type": "string", "enum": signalSlugs, "description": "the signal it opens on"},
					"start":     obj{"description": "when the signal holds: start by itself (auto), ask first (confirm), or only surface it (manual) — or an enum $parameter", "anyOf": []any{obj{"type": "string", "enum": starts}, paramRef}},
					"quiet_for": str("a signal that fires on activity: start once nothing new happened for this long (at least 1m), or a $parameter"),
					"max":       intOrParam("a watcher's runs per merge request (default: no limit)"),
				},
			},
		},
	}
	chain := func(kind string, then bool) obj {
		props := obj{
			"id":          withDesc(slug, "the "+kind+"'s id: unique among the watchers and outcomes"),
			"description": str("what it is for — documentation only"),
			"on":          obj{"$ref": "#/$defs/wait"},
			"sequence":    str("run this sequence as its chain (or main, the main chain again)"),
			"stages":      obj{"type": "array", "items": obj{"$ref": "#/$defs/stage"}, "description": "its chain of stages"},
		}
		if then {
			props["then"] = stringsOf("how the run ends once its chain is through (default: done)", TerminalDone, TerminalAbort)
		}
		return obj{"type": "object", "additionalProperties": false, "required": []string{"id", "on"}, "properties": props}
	}
	known := obj{}
	for _, name := range KnownParamOrder {
		known[name] = obj{"description": knownParamDocs[name]}
	}
	types := make([]string, len(ParamTypes))
	for i, t := range ParamTypes {
		types[i] = string(t)
	}
	scalar := obj{"type": []string{"string", "number", "boolean"}}
	// A flow's own parameter declares what it holds; the engine's six take
	// a bare default (their entries under properties).
	param := obj{
		"description": "a parameter of the flow's own: {type, list, default, description, enum} — read where $name appears",
		"anyOf": []any{
			obj{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"type"},
				"properties": obj{
					"type":        stringsOf("what it holds", types...),
					"list":        obj{"type": "boolean", "description": "any number of values of its type (default: one)"},
					"default":     obj{"anyOf": []any{scalar, obj{"type": "array", "items": scalar}}, "description": "its value unless a workshop or a run binds another — read as its type; a list's is a list"},
					"description": str("what the parameter is for"),
					"enum":        obj{"type": "array", "items": obj{"type": "string"}, "description": "the values it may take (a string parameter, or each item of a list of strings)"},
				},
			},
		},
	}
	agent := obj{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"name", "prompt"},
		"properties": obj{
			"name":        obj{"type": "string", "pattern": `^[a-z0-9][a-z0-9._-]*$`, "description": "the agent's name, which an agent stage runs"},
			"description": str("what the agent does"),
			"read_only":   obj{"type": "boolean", "description": "deny the harness its edit and write tools"},
			"allow_bash":  obj{"type": "boolean", "description": "let it run shell commands"},
			"prompt":      str("its instructions; the inputs a stage binds and the outputs it must write are added to them"),
			"mcp":         obj{"type": "array", "items": slug, "uniqueItems": true, "description": "the MCP servers it may use, declared under mcp_servers"},
			"inputs": obj{"type": "array", "description": "what a stage running it binds by name", "items": obj{
				"type": "object", "additionalProperties": false, "required": []string{"name"},
				"properties": obj{
					"name":        str("the input's name"),
					"description": str("what it is — the agent reads this"),
					"optional":    obj{"type": "boolean", "description": "a stage running it may leave it out"},
					"type":        stringsOf("what it reads", allTypes...),
					"schema":      str("a schema declared under schemas, which the file holds"),
				},
				"anyOf": typeOrSchema,
			}},
			"outputs": obj{"type": "array", "description": "files it must write", "items": obj{
				"type": "object", "additionalProperties": false, "required": []string{"name"},
				"properties": obj{
					"name":        str("the output's name"),
					"description": str("what it is — the agent reads this"),
					"type":        stringsOf("what the file is: any file, or a structure something built in reads — a verdict (routed by on_reject), a review to post, a review round's replies, a commit message", agentspec.FileTypes...),
					"schema":      str("a schema declared under schemas, which the file holds (beside a type it narrows it)"),
				},
				"anyOf": typeOrSchema,
			}},
		},
	}
	return obj{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"$id":                  "https://apothecary.local/schemas/flow.json",
		"title":                "apothecary flow",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"name", "version", "parameters"},
		"properties": obj{
			"name":        withDesc(slug, "the flow's name"),
			"version":     obj{"const": CurrentVersion, "description": "the grammar this document is written in"},
			"description": str("what the flow does"),
			"operates_on": stringsOf("what each run works on — picked by whoever starts it (default: ticket)", OperatesOnTicket, OperatesOnMR),
			"ask": obj{
				"type":                 "object",
				"description":          "the Ask pane: a read-only session beside the run that answers questions about the code; its model is picked in the pane",
				"additionalProperties": false,
				"required":             []string{"harness"},
				"properties": obj{
					"harness": str("the harness kind it runs on, or a $parameter"),
				},
			},
			"parameters": obj{
				"type":                 "object",
				"description":          "the flow's settings: the six the engine reads and its own, read where $name appears; harness and model may have no default — the project binds them",
				"propertyNames":        obj{"pattern": `^[a-z][a-z0-9_]*$`},
				"properties":           known,
				"additionalProperties": obj{"$ref": "#/$defs/param"},
			},
			"schemas": obj{"type": "array", "description": "the structures the flow's own files hold, named by the agents' inputs and outputs", "items": obj{
				"type": "object", "additionalProperties": false, "required": []string{"name", "json_schema"},
				"properties": obj{
					"name":        obj{"type": "string", "pattern": `^[a-z0-9][a-z0-9._-]*$`, "description": "the schema's name, which an input's or an output's schema: names"},
					"description": str("what the structure is"),
					"json_schema": obj{"type": []string{"object", "boolean"}, "description": "a JSON Schema (draft 2020-12), written in YAML; $ref resolves inside it only"},
				},
			}},
			"mcp_servers": obj{"type": "array", "description": "MCP servers the flow's agents may use, each agent listing its own under mcp", "items": obj{
				"type": "object", "additionalProperties": false, "required": []string{"name"},
				"oneOf": []any{obj{"required": []string{"builtin"}}, obj{"required": []string{"command"}}, obj{"required": []string{"url"}}},
				"properties": obj{
					"name":        withDesc(slug, "the server's name, which an agent's mcp lists"),
					"description": str("what it offers"),
					"builtin":     stringsOf("one apothecary runs: browser — the project's browser, reaching the run's services at localhost", MCPBuiltins...),
					"command":     obj{"type": "array", "items": obj{"type": "string"}, "minItems": 1, "description": "a program speaking MCP on stdio, run in the workspace: [program, args…]"},
					"url":         str("a remote MCP server (streamable HTTP)"),
					"env":         obj{"type": "object", "additionalProperties": obj{"type": "string"}, "description": "command: its environment — ${secrets.<name>} reads a project secret"},
					"headers":     obj{"type": "object", "additionalProperties": obj{"type": "string"}, "description": "url: request headers — ${secrets.<name>} reads a project secret"},
				},
			}},
			"agents":    obj{"type": "array", "items": agent, "description": "the agents the flow carries"},
			"sequences": obj{"type": "array", "description": "chains written once, run in place by a stage's sequence:", "items": obj{"type": "object", "additionalProperties": false, "required": []string{"id", "stages"}, "properties": obj{"id": withDesc(slug, "the sequence's id"), "description": str("what it is for"), "stages": obj{"type": "array", "items": obj{"$ref": "#/$defs/stage"}}}}},
			"main":      obj{"type": "array", "items": obj{"$ref": "#/$defs/stage"}, "description": "the chain a run starts with"},
			"watchers":  obj{"type": "array", "items": chain("watcher", false), "description": "chains run while the run is open, on what happens to its merge requests — never ending it"},
			"outcomes":  obj{"type": "array", "items": chain("outcome", true), "description": "what ends the run: each fires once"},
		},
		"$defs": obj{"stage": stage, "edge": edge, "wait": wait, "param": param, "port_in": portIn, "port_out": portOut},
	}
}

// stageRules: what each kind of stage must name, and what an action's inputs
// are — so an editor completes them.
func stageRules() []any {
	need := func(typ StageType, field string) obj {
		return obj{
			"if":   obj{"properties": obj{"type": obj{"const": string(typ)}}, "required": []string{"type"}},
			"then": obj{"required": []string{field}},
		}
	}
	rules := []any{need(StageAgent, "agent"), need(StageAction, "action"), need(StageWait, "for"), need(StageService, "service")}
	for _, a := range actionspec.Registry() {
		names := func(ps []actionspec.PortSig, required bool) (obj, []string) {
			props := obj{}
			var req []string
			for _, pt := range ps {
				props[pt.Name] = obj{"description": pt.Description}
				if pt.Required || required {
					req = append(req, pt.Name)
				}
			}
			return props, req
		}
		then := obj{}
		inProps, inReq := names(a.Inputs, false)
		input := obj{"type": "object", "properties": inProps, "additionalProperties": false}
		if len(inReq) > 0 {
			input["required"] = inReq
		}
		then["input"] = input
		outProps, outReq := names(a.Outputs, true) // the action writes them all
		output := obj{"type": "object", "properties": outProps, "additionalProperties": false}
		if len(outReq) > 0 {
			output["required"] = outReq
		}
		then["output"] = output
		settings := obj{}
		var required []string
		for _, st := range a.Settings {
			p := obj{"type": "string", "description": st.Description}
			if st.Kind == actionspec.KindEnum {
				p["anyOf"] = []any{obj{"enum": slices.Clone(st.Values)}, obj{"pattern": `^\$[a-z][a-z0-9_]*$`}}
			}
			settings[st.Name] = p
			if st.Required {
				required = append(required, st.Name)
			}
		}
		with := obj{"type": "object", "properties": settings, "additionalProperties": false}
		if len(required) > 0 {
			with["required"] = required
		}
		then["with"] = with
		rule := obj{"properties": then}
		var needs []string
		if len(inReq) > 0 {
			needs = append(needs, "input")
		}
		if len(outReq) > 0 {
			needs = append(needs, "output")
		}
		if len(needs) > 0 {
			rule["required"] = needs
		}
		rules = append(rules, obj{
			"if":   obj{"properties": obj{"action": obj{"const": a.Slug}}, "required": []string{"action"}},
			"then": rule,
		})
	}
	return rules
}

// withDesc is s with a description.
func withDesc(s obj, desc string) obj {
	out := obj{"description": desc}
	for k, v := range s {
		out[k] = v
	}
	return out
}

// knownParamDocs says what each engine-read parameter is.
var knownParamDocs = map[string]string{
	"harness":         "the harness kind agent stages read as $harness; no default means the project binds it",
	"model":           "the model agent stages read as $model, provider/model; no default means the project binds it (model ids change — leave it to the project)",
	"timeout":         "how long an agent or a command stage may take (default " + knownDefaults["timeout"] + ")",
	"max_turns":       "an agent stage's turn budget (default " + knownDefaults["max_turns"] + ")",
	"max_cost_usd":    "what the whole run may cost, in USD (default " + knownDefaults["max_cost_usd"] + ")",
	"max_total_turns": "the turns the whole run may take (default " + knownDefaults["max_total_turns"] + ")",
}
