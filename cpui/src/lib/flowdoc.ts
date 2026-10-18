// The flow document model the graph editor edits (DESIGN3 §2.3): the raw
// YAML schema shape, mirrored from the Go side's flow.Doc (ParseDoc hands
// it out via /api/flows/validate). The editor mutates this object; docToYaml
// re-emits canonical YAML through the same save/validate/version pipeline —
// the graph is a view, never a second format.
import { stringify } from 'yaml';

export const DONE = 'done';
// ABORT is the reserved edge target that deliberately stops the run
// (projected as abandoned) — the explicit twin of DONE.
export const ABORT = 'abort';
// START is the entry marker's layout/link key. The entry itself is the
// first stage in the document (DESIGN3 §2.1) — START is never serialized.
export const START = 'start';
export const EDGE_KINDS = ['on_fail', 'on_reject', 'on_timeout'] as const;
export type EdgeKind = (typeof EDGE_KINDS)[number];

export type StageKind = 'agent' | 'command' | 'human_gate' | 'action' | 'wait';

// CURRENT_VERSION is the grammar this build writes; every document declares
// it, so a future break is a refusal rather than a mis-parse (FLOWLANG §1).
export const CURRENT_VERSION = 3;

// An edge is a chain: when a link's max is exhausted the next failure
// falls through to `then` (retry-then-escalate). A link with to: abort
// stops the run and carries no max/then.
//
// An edge does not say what it carries: its kind decides that ($DECISION for a
// rejection, $FAILURE for a failure), and the TARGET stage binds it as an input.
export interface DocEdge {
	to: string;
	max?: number;
	then?: DocEdge;
}

// chainLinks flattens an edge chain for editing/rendering (index = depth).
export function chainLinks(e: DocEdge | undefined): DocEdge[] {
	const out: DocEdge[] = [];
	for (let cur = e; cur; cur = cur.then) out.push(cur);
	return out;
}

// Which edge kinds a stage type may declare (mirror of the Go registry).
export function edgeKindsFor(type: StageKind): EdgeKind[] {
	const out: EdgeKind[] = [];
	if (type === 'agent' || type === 'command' || type === 'wait') out.push('on_fail', 'on_timeout');
	if (type === 'agent' || type === 'human_gate') out.push('on_reject');
	// An action fails when its outcome is wrong in a fixable way (a merge
	// that conflicts, a push the remote refuses).
	if (type === 'action') out.push('on_fail');
	return out;
}

/** Which sentinel an edge kind's carried content fills (mirror of Go). */
export function feedSentinelFor(kind: EdgeKind): string {
	return kind === 'on_reject' ? SENTINEL_DECISION : SENTINEL_FAILURE;
}

// Sentinels are the engine-provided input values (FLOWLANG §4). The rule is
// lexical: a $NAME input is resolved by the engine, anything else is a
// repo-relative artifact path.
export const SENTINEL_CHANGED = '$CHANGED';
export const SENTINEL_MR_URL = '$MR_URL';
export const SENTINEL_WORKSPACE = '$WORKSPACE';
// $DECISION and $FAILURE carry what the edge that re-entered a stage brought:
// a rejection's rationale, and a failing stage's output. Bound like any other
// input, so the retry channel is declared rather than arriving invisibly.
export const SENTINEL_DECISION = '$DECISION';
export const SENTINEL_FAILURE = '$FAILURE';
export const isSentinel = (v: string) => v.startsWith('$');
export const isFeedSentinel = (v: string) => v === SENTINEL_DECISION || v === SENTINEL_FAILURE;

/** Which sentinels each stage type may bind (mirror of the Go value rules). */
export function sentinelsFor(type: StageKind): string[] {
	if (type === 'agent') return [SENTINEL_CHANGED, SENTINEL_DECISION, SENTINEL_FAILURE];
	if (type === 'human_gate') return [SENTINEL_WORKSPACE, SENTINEL_MR_URL];
	return [];
}

// A stage's input/output binding: an ordered mapping of name to value
// (FLOWLANG §4). One form for every stage type — the name is checked against a
// contract where one exists (an agent's decls, an action's signature) and is a
// free label otherwise. Object insertion order is preserved on the wire (the Go
// side emits ordered JSON), so bindings never reorder across a round-trip.
export type StageIO = Record<string, string>;

export const ioEntries = (v?: StageIO): [string, string][] => (v ? Object.entries(v) : []);

export interface DocStage {
	id: string;
	type: StageKind;
	/** Optional human note; documentation only, reaches no prompt. */
	description?: string;
	agent?: string;
	model?: string;
	harness?: string;
	max_turns?: number;
	timeout?: string;
	input?: StageIO;
	output?: StageIO;
	run?: string;
	run_ref?: string;
	/** command: run once per checkout of a multi-repo run instead of once in
	 *  the primary (what git commit / push want). */
	each_repo?: boolean;
	/** human_gate: the free text the human is asked. */
	question?: string;
	/** human_gate: signals that answer the gate on the human's behalf. */
	auto_approve?: string;
	auto_reject?: string;
	/** action: a slug from the compiled-in registry (GET /api/actions). */
	action?: string;
	/** wait: a signal slug to block on (GET /api/signals). */
	for?: string;
	on_success?: string;
	on_fail?: DocEdge;
	on_reject?: DocEdge;
	on_timeout?: DocEdge;
	/** Editor-only: the phase this stage belongs to, in a phased flow. The
	 *  YAML nests stages under their phase; the editor keeps one flat list. */
	phase?: string;
}

/** A phase's `on:` (PLAN6): `ticket` for the entry, else a forge signal. */
export interface DocTrigger {
	signal: string;
	quiet_for?: string;
	confirm?: boolean;
	max?: number;
}

/** A phase header; its stages are the flat list's stages tagged with it. */
export interface DocPhase {
	id: string;
	description?: string;
	on: DocTrigger;
}

export const TRIGGER_TICKET = 'ticket';

// One declared input of an agent: what the stage binds by name.
export interface DocAgentInput {
	name: string;
	description?: string;
	optional?: boolean;
}

// One declared output: a file the agent must write. `type: verdict` is the
// structured APPROVE/REJECT the engine routes on_reject from.
export interface DocAgentOutput {
	name: string;
	description?: string;
	path: string;
	type?: string;
}

// An agent definition carried BY the flow (FLOWLANG §1, v3): the document is
// the single source, so contracts are checked at parse time and prompts are
// pinned with the rest of the flow.
export interface DocAgent {
	name: string;
	description?: string;
	read_only?: boolean;
	allow_bash?: boolean;
	prompt: string;
	inputs?: DocAgentInput[];
	outputs?: DocAgentOutput[];
}

export interface FlowDoc {
	name: string;
	version?: number;
	description?: string;
	defaults: { timeout?: string; harness?: string; model: string; max_turns?: number };
	budget: { max_cost_usd?: number; max_total_turns?: number };
	agents?: DocAgent[];
	stages: DocStage[];
	/** Phased flows (PLAN6): the phase headers, the entry first. Absent for a
	 *  flow written with top-level stages. */
	phases?: DocPhase[];
}

/** The wire document, where a phased flow nests its stages. */
type WireDoc = Omit<FlowDoc, 'stages' | 'phases'> & {
	stages?: DocStage[];
	phases?: (DocPhase & { stages?: DocStage[] })[];
};

// normalizeDoc turns the server's document into the editor's: a phased
// flow's stages flattened in phase order, each tagged with its phase.
export function normalizeDoc(raw: unknown): FlowDoc {
	const w = raw as WireDoc;
	if (!w.phases?.length) return { ...(w as FlowDoc), stages: w.stages ?? [] };
	const stages: DocStage[] = [];
	const phases: DocPhase[] = [];
	for (const ph of w.phases) {
		const { stages: inner, ...head } = ph;
		phases.push(head);
		for (const st of inner ?? []) stages.push({ ...st, phase: ph.id });
	}
	return { ...(w as FlowDoc), stages, phases };
}

/** The stages of one phase, in document order ('' = an unphased flow). */
export function stagesOf(doc: FlowDoc, phase: string | undefined): DocStage[] {
	return doc.stages.filter((s) => (s.phase ?? '') === (phase ?? ''));
}

/** A phase's first stage: where its trigger enters. */
export function phaseEntry(doc: FlowDoc, phase: string | undefined): DocStage | undefined {
	return doc.stages.find((s) => (s.phase ?? '') === (phase ?? ''));
}

/** "on: mr_conflicts (confirm)" — a trigger in a few words. */
export function triggerLabel(t: DocTrigger): string {
	const bits: string[] = [];
	if (t.quiet_for) bits.push(`quiet ${t.quiet_for}`);
	if (t.confirm) bits.push('confirm');
	if (t.max != null) bits.push(`max ${t.max}`);
	return bits.length ? `${t.signal} (${bits.join(', ')})` : t.signal;
}

/** The flow's definition of an agent by name, or undefined. */
export function agentOf(doc: FlowDoc, name: string): DocAgent | undefined {
	return doc.agents?.find((a) => a.name === name);
}

// effectiveSuccess resolves the YAML sugar for display: explicit target, or
// next stage in document order, or done.
// In a phased flow, "next" never crosses into another phase: a phase's last
// stage ends it.
export function effectiveSuccess(doc: FlowDoc, i: number): string {
	const s = doc.stages[i];
	if (s.on_success) return s.on_success;
	const next = doc.stages[i + 1];
	return !next || (next.phase ?? '') !== (s.phase ?? '') ? DONE : next.id;
}

export function edgeOf(s: DocStage, kind: EdgeKind): DocEdge | undefined {
	return kind === 'on_fail' ? s.on_fail : kind === 'on_reject' ? s.on_reject : s.on_timeout;
}

export function setEdge(s: DocStage, kind: EdgeKind, e: DocEdge | undefined) {
	if (kind === 'on_fail') s.on_fail = e;
	else if (kind === 'on_reject') s.on_reject = e;
	else s.on_timeout = e;
}

// setEntry makes the stage with the given id the flow's entry by moving it
// to the front of the document (the entry IS stages[0]); docToYaml already
// serializes entry-first. Returns true when the entry actually changed.
// In a phased flow the stage becomes the entry of ITS phase (moved to the
// front of that phase's block).
export function setEntry(doc: FlowDoc, id: string): boolean {
	const i = doc.stages.findIndex((s) => s.id === id);
	if (i < 0) return false;
	const first = doc.stages.findIndex((s) => (s.phase ?? '') === (doc.stages[i].phase ?? ''));
	if (first === i) return false;
	const [s] = doc.stages.splice(i, 1);
	doc.stages.splice(first, 0, s);
	return true;
}

// movePhase reassigns a stage to another phase, at the end of its block.
export function movePhase(doc: FlowDoc, id: string, phase: string) {
	const i = doc.stages.findIndex((s) => s.id === id);
	if (i < 0 || doc.stages[i].phase === phase) return;
	const [s] = doc.stages.splice(i, 1);
	s.phase = phase;
	let at = doc.stages.length;
	for (let j = doc.stages.length - 1; j >= 0; j--) {
		if (doc.stages[j].phase === phase) {
			at = j + 1;
			break;
		}
	}
	if (at === doc.stages.length) {
		// An empty phase: place its block where the phase order says.
		const order = (doc.phases ?? []).map((p) => p.id);
		const rank = order.indexOf(phase);
		at = doc.stages.findIndex((x) => order.indexOf(x.phase ?? '') > rank);
		if (at < 0) at = doc.stages.length;
	}
	doc.stages.splice(at, 0, s);
}

// pruneEdgeTarget drops chain links that target a deleted stage id,
// splicing their fallbacks up; returns the surviving chain head.
export function pruneEdgeTarget(e: DocEdge | undefined, id: string): DocEdge | undefined {
	if (!e) return undefined;
	const rest = pruneEdgeTarget(e.then, id);
	if (e.to === id) return rest;
	e.then = rest;
	return e;
}

// successOrder returns stages ordered by the success chain from the entry
// (doc.stages[0]) followed by the remaining stages in document order — the
// serialization order (DESIGN3 §2.3). Entry stays first by construction.
export function successOrder(doc: FlowDoc): DocStage[] {
	if (doc.phases?.length) return doc.phases.flatMap((ph) => phaseOrder(doc, ph.id));
	return phaseOrder(doc, undefined);
}

// phaseOrder is successOrder within one phase: the chain from the phase's
// entry, then its remaining stages in document order.
function phaseOrder(doc: FlowDoc, phase: string | undefined): DocStage[] {
	const mine = stagesOf(doc, phase);
	if (mine.length === 0) return [];
	const byId = new Map(mine.map((s) => [s.id, s]));
	const seen = new Set<string>();
	const out: DocStage[] = [];
	let cur: DocStage | undefined = mine[0];
	while (cur && !seen.has(cur.id)) {
		seen.add(cur.id);
		out.push(cur);
		const next = effectiveSuccess(doc, doc.stages.indexOf(cur));
		if (next === DONE) break;
		cur = byId.get(next);
	}
	for (const s of mine) if (!seen.has(s.id)) out.push(s);
	return out;
}

// docToYaml emits canonical YAML: stages in success order with a fully
// explicit on_success on every stage (the editor never relies on sugar).
export function docToYaml(doc: FlowDoc): string {
	const ordered = successOrder(doc);
	const idx = new Map(doc.stages.map((s, i) => [s.id, i]));
	const obj: Record<string, unknown> = { name: doc.name, version: doc.version ?? CURRENT_VERSION };
	if (doc.description) obj.description = doc.description;
	const defaults: Record<string, unknown> = {};
	if (doc.defaults.timeout) defaults.timeout = doc.defaults.timeout;
	if (doc.defaults.harness) defaults.harness = doc.defaults.harness;
	defaults.model = doc.defaults.model;
	if (doc.defaults.max_turns != null) defaults.max_turns = doc.defaults.max_turns;
	obj.defaults = defaults;
	const budget: Record<string, unknown> = {};
	if (doc.budget?.max_cost_usd != null) budget.max_cost_usd = doc.budget.max_cost_usd;
	if (doc.budget?.max_total_turns != null) budget.max_total_turns = doc.budget.max_total_turns;
	if (Object.keys(budget).length > 0) obj.budget = budget;
	// Agents before stages: a reader meets the contracts before the wiring
	// that binds them. Order within the list is the author's.
	if (doc.agents?.length) {
		obj.agents = doc.agents.map((a) => {
			const o: Record<string, unknown> = { name: a.name };
			if (a.description) o.description = a.description;
			if (a.read_only) o.read_only = a.read_only;
			if (a.allow_bash) o.allow_bash = a.allow_bash;
			if (a.inputs?.length) {
				o.inputs = a.inputs.map((i) => {
					const io: Record<string, unknown> = { name: i.name };
					if (i.description) io.description = i.description;
					if (i.optional) io.optional = i.optional;
					return io;
				});
			}
			if (a.outputs?.length) {
				o.outputs = a.outputs.map((out) => {
					const oo: Record<string, unknown> = { name: out.name };
					if (out.description) oo.description = out.description;
					oo.path = out.path;
					if (out.type) oo.type = out.type;
					return oo;
				});
			}
			// Prompt last: it is the long block, so the structural fields stay
			// readable at the top of each entry.
			o.prompt = a.prompt;
			return o;
		});
	}
	const stageYaml = (s: DocStage) => {
		const o: Record<string, unknown> = { id: s.id, type: s.type };
		if (s.description) o.description = s.description;
		if (s.agent) o.agent = s.agent;
		if (s.model) o.model = s.model;
		if (s.harness) o.harness = s.harness;
		if (s.max_turns != null) o.max_turns = s.max_turns;
		if (s.timeout) o.timeout = s.timeout;
		// input/output are three-way unions; emit only a non-empty one. Guarding
		// on `.length` alone would silently drop the named form (objects have no
		// length), losing every binding on save.
		if (nonEmptyIO(s.input)) o.input = s.input;
		if (nonEmptyIO(s.output)) o.output = s.output;
		if (s.run) o.run = s.run;
		if (s.run_ref) o.run_ref = s.run_ref;
		if (s.each_repo) o.each_repo = true;
		if (s.question) o.question = s.question;
		if (s.auto_approve) o.auto_approve = s.auto_approve;
		if (s.auto_reject) o.auto_reject = s.auto_reject;
		if (s.action) o.action = s.action;
		if (s.for) o.for = s.for;
		o.on_success = s.on_success ?? effectiveSuccess(doc, idx.get(s.id)!);
		for (const kind of EDGE_KINDS) {
			const e = edgeOf(s, kind);
			if (e) o[kind] = edgeToYamlValue(e);
		}
		return o;
	};
	if (doc.phases?.length) {
		obj.phases = doc.phases.map((ph) => {
			const o: Record<string, unknown> = { id: ph.id };
			if (ph.description) o.description = ph.description;
			o.on = triggerYaml(ph.on);
			o.stages = ordered.filter((s) => s.phase === ph.id).map(stageYaml);
			return o;
		});
	} else {
		obj.stages = ordered.map(stageYaml);
	}
	return stringify(obj, { lineWidth: 0 });
}

// triggerYaml uses the scalar sugar when the trigger has no options.
function triggerYaml(t: DocTrigger): unknown {
	if (!t.quiet_for && !t.confirm && t.max == null) return t.signal;
	const o: Record<string, unknown> = { signal: t.signal };
	if (t.quiet_for) o.quiet_for = t.quiet_for;
	if (t.confirm) o.confirm = true;
	if (t.max != null) o.max = t.max;
	return o;
}

// nonEmptyIO reports whether a binding has anything to serialize.
function nonEmptyIO(v?: StageIO): boolean {
	return v != null && Object.keys(v).length > 0;
}

// edgeToYamlValue serializes one chain link (recursing into then). An
// abort link uses the parser's scalar sugar: `on_reject: abort`.
function edgeToYamlValue(e: DocEdge): unknown {
	if (e.to === ABORT) return ABORT;
	const eo: Record<string, unknown> = { to: e.to, max: e.max };
	if (e.then) eo.then = edgeToYamlValue(e.then);
	return eo;
}

// autoLayout places nodes left-to-right along the success chain, dropping
// off-chain stages into lanes below (used when no stored layout exists).
// A phased flow gets one band per phase, top to bottom.
export function autoLayout(doc: FlowDoc): Record<string, { x: number; y: number }> {
	const pos: Record<string, { x: number; y: number }> = {};
	const bands = doc.phases?.length ? doc.phases.map((p) => p.id) : [undefined];
	const byId = new Map(doc.stages.map((s, i) => [s.id, i]));
	bands.forEach((phase, band) => {
		const y0 = band * 340;
		const chain = new Set<string>();
		let cur = phaseEntry(doc, phase)?.id;
		while (cur && cur !== DONE && !chain.has(cur)) {
			chain.add(cur);
			cur = effectiveSuccess(doc, byId.get(cur)!);
		}
		let cx = 0;
		let ox = 0;
		for (const s of phaseOrder(doc, phase)) {
			if (chain.has(s.id)) {
				pos[s.id] = { x: cx * 240, y: y0 };
				cx++;
			} else {
				pos[s.id] = { x: ox * 240 + 120, y: y0 + 160 };
				ox++;
			}
		}
	});
	return pos;
}

// freshStage builds a new stage of the given type with a unique id.
export function freshStage(doc: FlowDoc, type: StageKind): DocStage {
	const base = {
		agent: 'agent',
		command: 'cmd',
		human_gate: 'gate',
		action: 'action',
		wait: 'wait'
	}[type];
	let n = 1;
	let id = `${base}_${n}`;
	const ids = new Set(doc.stages.map((s) => s.id));
	while (ids.has(id)) id = `${base}_${++n}`;
	const s: DocStage = { id, type, on_success: DONE };
	if (doc.phases?.length) s.phase = doc.phases[0].id;
	if (type === 'agent') s.agent = '';
	if (type === 'command') s.run_ref = '';
	if (type === 'human_gate') s.question = '';
	if (type === 'action') s.action = '';
	if (type === 'wait') s.for = '';
	return s;
}

// --- dataflow (FLOWLANG §4) -------------------------------------------------
//
// Every stage declares what it consumes and produces, so the wiring between
// stages is derivable rather than drawn: two stages are connected when one's
// output path is another's input path. These helpers are the single source for
// the graph's io dots and its derived data edges.

/** One io port on a stage: a dot in the graph, a row in the inspector. */
export interface IOPort {
	/** Binding name, or the path itself for a command's unnamed entries. */
	name: string;
	/** The bound artifact path, or a sentinel. */
	value: string;
	/** True when value is engine-provided ($CHANGED, $MR_URL, $WORKSPACE). */
	sentinel: boolean;
	/** Declared description, when the contract carries one. */
	description?: string;
	/** True for a declared-but-unbound input (nothing to connect yet). */
	unbound?: boolean;
}

/** The agent/action contract shapes the graph needs; a subset of api.ts types. */
export interface IODecl {
	name: string;
	description?: string;
	optional?: boolean;
	path?: string;
	type?: string;
	kind?: string;
	sentinel?: string;
	required?: boolean;
}
export interface AgentContract {
	name: string;
	inputs?: IODecl[];
	outputs?: IODecl[];
}
export interface ActionContract {
	slug: string;
	inputs?: IODecl[];
	outputs?: IODecl[];
}

const port = (name: string, value: string, decl?: IODecl, unbound = false): IOPort => ({
	name,
	value,
	sentinel: isSentinel(value),
	description: decl?.description,
	unbound: unbound || undefined
});

/**
 * stageInputs lists a stage's input ports: named bindings for agents and
 * actions (including any declared-but-unbound ones, so the contract is visible
 * before it is wired), plain paths for commands, and the single reviewed
 * artifact or sentinel for a gate.
 */
export function stageInputs(
	s: DocStage,
	agents: AgentContract[] = [],
	actions: ActionContract[] = []
): IOPort[] {
	switch (s.type) {
		case 'agent':
			return declaredPorts(s.input, agents.find((a) => a.name === s.agent)?.inputs ?? []);
		case 'action':
			return declaredPorts(s.input, actions.find((a) => a.slug === s.action)?.inputs ?? []);
		// A command or gate has no contract, so every binding it wrote is a port.
		case 'command':
		case 'human_gate':
		case 'wait':
			return ioEntries(s.input).map(([name, value]) => port(name, value));
	}
}

/**
 * declaredPorts lists a contract's inputs in declaration order (so the shape is
 * visible before it is wired), followed by any binding the contract does not
 * declare — the validator flags those, but hiding them would make the graph lie
 * about the YAML.
 */
function declaredPorts(io: StageIO | undefined, decls: IODecl[]): IOPort[] {
	const bound = io ?? {};
	const out = decls.map((d) => port(d.name, bound[d.name] ?? '', d, bound[d.name] == null));
	for (const [name, value] of ioEntries(io)) {
		if (!decls.some((d) => d.name === name)) out.push(port(name, value));
	}
	return out;
}

/**
 * stageOutputs lists what a stage produces: an agent's declared outputs with
 * the stage's rebinds applied (the TS mirror of flow.EffectiveOutputs), a
 * command's declared paths, or an action's value outputs shown by sentinel.
 */
export function stageOutputs(
	s: DocStage,
	agents: AgentContract[] = [],
	actions: ActionContract[] = []
): IOPort[] {
	switch (s.type) {
		case 'agent': {
			// Declared defaults overlaid with the stage's rebinds — the TS mirror of
			// flow.EffectiveOutputs.
			const decls = agents.find((a) => a.name === s.agent)?.outputs ?? [];
			const rebinds = s.output ?? {};
			const out = decls.map((d) => port(d.name, rebinds[d.name] ?? d.path ?? '', d));
			for (const [name, value] of ioEntries(s.output)) {
				if (!decls.some((d) => d.name === name)) out.push(port(name, value));
			}
			return out;
		}
		case 'command':
			return ioEntries(s.output).map(([name, value]) => port(name, value));
		case 'action': {
			// A value output is consumed through its sentinel, not a path.
			const sig = actions.find((a) => a.slug === s.action)?.outputs ?? [];
			return sig.map((d) => port(d.name, d.sentinel ?? d.path ?? '', d));
		}
		case 'human_gate':
		case 'wait':
			return [];
	}
}

/** verdictOutput returns an agent's verdict-typed output decl, if it has one. */
export function verdictOutput(agent?: AgentContract): IODecl | undefined {
	return agent?.outputs?.find((o) => o.type === 'verdict');
}

/** One derived producer→consumer connection. Read-only: paths are the wiring. */
export interface DataLink {
	source: string;
	sourceHandle: string;
	target: string;
	targetHandle: string;
	value: string;
}

/**
 * deriveDataLinks connects every consumer port to the stage that produces its
 * value — by artifact path, or by sentinel for an action's value outputs (a
 * gate's $MR_URL comes from create_mr). Derived, never stored: the YAML's paths
 * are the wiring, and this only draws what they already say.
 */
export function deriveDataLinks(
	doc: FlowDoc,
	agents: AgentContract[] = [],
	actions: ActionContract[] = []
): DataLink[] {
	// value -> producing (stage, port). Later producers win, which keeps the
	// link pointing at the most recent writer in document order.
	const producers = new Map<string, { stage: string; name: string }>();
	for (const s of doc.stages) {
		for (const o of stageOutputs(s, agents, actions)) {
			if (o.value) producers.set(o.value, { stage: s.id, name: o.name });
		}
	}
	const links: DataLink[] = [];
	for (const s of doc.stages) {
		for (const i of stageInputs(s, agents, actions)) {
			// A feed sentinel arrives along a control edge, not from an output — the
			// graph lands those edges on the dot instead (see feedDots).
			if (!i.value || i.unbound || isFeedSentinel(i.value)) continue;
			const p = producers.get(i.value);
			if (!p || p.stage === s.id) continue;
			links.push({
				source: p.stage,
				sourceHandle: `out:${p.name}`,
				target: s.id,
				targetHandle: `in:${i.name}`,
				value: i.value
			});
		}
	}
	return links;
}

/**
 * feedDots maps "<stage id>|<sentinel>" to the input name bound to it, so the
 * graph can land a control edge on the exact dot that receives its content: an
 * on_reject edge ends at whatever the target called its $DECISION input.
 */
export function feedDots(doc: FlowDoc): Map<string, string> {
	const out = new Map<string, string>();
	for (const s of doc.stages) {
		for (const [name, value] of ioEntries(s.input)) {
			if (isFeedSentinel(value)) out.set(`${s.id}|${value}`, name);
		}
	}
	return out;
}
