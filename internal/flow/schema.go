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
	bindings := func(desc string) obj {
		return obj{"type": "object", "description": desc, "additionalProperties": obj{"type": "string"}}
	}
	stage := obj{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"id"},
		"properties": obj{
			"id":           withDesc(slug, "the stage's id: lowercase, unique in its chain (or its sequence)"),
			"type":         stringsOf("what the stage is (a sequence stage has none: it names a sequence)", string(StageAgent), string(StageCommand), string(StageHumanGate), string(StageAction), string(StageWait)),
			"sequence":     str("run this sequence in the stage's place (a stage that does has no type)"),
			"description":  str("what the stage is for — documentation only"),
			"when":         str("run only when this holds: tracker, subject, or a bool $parameter — each may be negated, `not x`"),
			"agent":        str("agent: the agent it runs, declared under agents"),
			"model":        str("agent: the model, provider/model, or a $parameter (default: the model parameter)"),
			"harness":      str("agent: the harness, or a $parameter (default: the harness parameter)"),
			"max_turns":    intOrParam("agent: the turn budget (default: the max_turns parameter)"),
			"timeout":      str("how long it may take: a duration (30m) or a $parameter — agent and command (default: the timeout parameter), wait (default: forever)"),
			"input":        bindings("its inputs by name: an agent's declared inputs, a command's files, what a gate shows, an action's inputs"),
			"output":       bindings("its outputs by name: an agent's declared outputs moved elsewhere, a command's files"),
			"run":          str("command: a script, run in the workspace"),
			"run_ref":      str("command: one of the project's commands, or a $parameter naming one"),
			"each_repo":    obj{"type": "boolean", "description": "command: run once per repository of a multi-repo run"},
			"question":     str("human_gate: what the person is asked"),
			"auto_approve": stringsOf("human_gate: a signal that approves it for the person", observed...),
			"auto_reject":  stringsOf("human_gate: a signal that rejects it for the person", observed...),
			"action":       stringsOf("action: the action it performs", actionSlugs...),
			"for":          stringsOf("wait: the signal it waits for", observed...),
			"on_success":   str("where the chain goes once it succeeds: a stage of the same chain, end, done or abort (default: the next stage)"),
			"on_fail":      obj{"$ref": "#/$defs/edge", "description": "where the run goes when it fails — carries $FAILURE"},
			"on_reject":    obj{"$ref": "#/$defs/edge", "description": "agent (with a verdict) or human_gate: where the run goes on a rejection — carries $DECISION"},
			"on_timeout":   obj{"$ref": "#/$defs/edge", "description": "agent, command or wait: where the run goes when it times out (default: on_fail)"},
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
	param := obj{
		"description": "a parameter: its default alone, or {default, description, enum} — read where $name appears",
		"anyOf": []any{
			obj{"type": []string{"string", "number", "boolean"}},
			obj{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"default"},
				"properties": obj{
					"default":     obj{"type": []string{"string", "number", "boolean"}, "description": "its value unless a workshop or a run binds another; its type is the parameter's"},
					"description": str("what the parameter is for"),
					"enum":        obj{"type": "array", "items": obj{"type": "string"}, "description": "the values it may take (a string parameter)"},
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
			"inputs": obj{"type": "array", "description": "what a stage running it binds by name", "items": obj{
				"type": "object", "additionalProperties": false, "required": []string{"name"},
				"properties": obj{
					"name":        str("the input's name"),
					"description": str("what it is — the agent reads this"),
					"optional":    obj{"type": "boolean", "description": "a stage may leave it unbound (required to bind $FAILURE or $DECISION)"},
				},
			}},
			"outputs": obj{"type": "array", "description": "files it must write", "items": obj{
				"type": "object", "additionalProperties": false, "required": []string{"name", "path"},
				"properties": obj{
					"name":        str("the output's name"),
					"description": str("what it is — the agent reads this"),
					"path":        str("where it is written, relative to the workspace (a stage may move it)"),
					"type":        stringsOf("what the file is: a verdict (APPROVE or REJECT, routed by on_reject), a review to post, a review round's replies", agentspec.OutputTypeVerdict, agentspec.OutputTypeReviewThreads, agentspec.OutputTypeReviewReplies),
				},
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
			"parameters": obj{
				"type":                 "object",
				"description":          "the flow's settings: the six the engine reads (harness and model required) and its own, read where $name appears",
				"propertyNames":        obj{"pattern": `^[a-z][a-z0-9_]*$`},
				"properties":           known,
				"additionalProperties": obj{"$ref": "#/$defs/param"},
				"required":             []string{"harness", "model"},
			},
			"agents":    obj{"type": "array", "items": agent, "description": "the agents the flow carries"},
			"sequences": obj{"type": "array", "description": "chains written once, run in place by a stage's sequence:", "items": obj{"type": "object", "additionalProperties": false, "required": []string{"id", "stages"}, "properties": obj{"id": withDesc(slug, "the sequence's id"), "description": str("what it is for"), "stages": obj{"type": "array", "items": obj{"$ref": "#/$defs/stage"}}}}},
			"main":      obj{"type": "array", "items": obj{"$ref": "#/$defs/stage"}, "description": "the chain a run starts with"},
			"watchers":  obj{"type": "array", "items": chain("watcher", false), "description": "chains run while the run is open, on what happens to its merge requests — never ending it"},
			"outcomes":  obj{"type": "array", "items": chain("outcome", true), "description": "what ends the run: each fires once"},
		},
		"$defs": obj{"stage": stage, "edge": edge, "wait": wait, "param": param},
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
	rules := []any{need(StageAgent, "agent"), need(StageAction, "action"), need(StageWait, "for")}
	for _, a := range actionspec.Registry() {
		inputs := obj{}
		var required []string
		for _, in := range a.Inputs {
			p := obj{"type": "string", "description": in.Description}
			if in.Kind == actionspec.KindEnum {
				p["anyOf"] = []any{obj{"enum": slices.Clone(in.Values)}, obj{"pattern": `^\$[a-z][a-z0-9_]*$`}}
			}
			inputs[in.Name] = p
			if in.Required {
				required = append(required, in.Name)
			}
		}
		input := obj{"type": "object", "properties": inputs, "additionalProperties": false}
		if len(required) > 0 {
			input["required"] = required
		}
		rules = append(rules, obj{
			"if":   obj{"properties": obj{"action": obj{"const": a.Slug}}, "required": []string{"action"}},
			"then": obj{"properties": obj{"input": input}},
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
	"harness":         "the harness agent stages run on, unless a stage names its own (required)",
	"model":           "the model agent stages use, provider/model, unless a stage names its own (required)",
	"timeout":         "how long an agent or a command stage may take (default " + knownDefaults["timeout"] + ")",
	"max_turns":       "an agent stage's turn budget (default " + knownDefaults["max_turns"] + ")",
	"max_cost_usd":    "what the whole run may cost, in USD (default " + knownDefaults["max_cost_usd"] + ")",
	"max_total_turns": "the turns the whole run may take (default " + knownDefaults["max_total_turns"] + ")",
}
