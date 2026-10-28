// The flow document model the graph editor edits (DESIGN3 §2.3): the raw
// YAML schema shape, mirrored from the Go side's flow.Doc (ParseDoc hands
// it out via /api/flows/validate). The editor mutates this object; docToYaml
// re-emits canonical YAML through the same save/validate/version pipeline —
// the graph is a view, never a second format.
import { stringify } from 'yaml';

// The two ends (FLOWLANG §3). END finishes a PHASE: its chain is over and the
// run goes back to waiting — a phase's last stage ends this way, and a
// watcher, which may run any number of times, always does. DONE finishes the
// RUN, now, whatever else is parked or armed: said where the work is truly
// finished (an outcome says it once the merge request merged).
export const END = 'end';
export const DONE = 'done';
// ABORT is the reserved edge target that deliberately stops the run
// (projected as abandoned) — the explicit twin of DONE.
export const ABORT = 'abort';
// DEBUG is the reserved edge target that parks the run for a human with the
// workspace and comes BACK to the stage that took it — a leaf on the canvas,
// not a terminal. Like abort it carries no max/then.
export const DEBUG = 'debug';
// START is the main chain's marker layout/link key (the canvas's solid
// beginning marker); it is never serialized.
export const START = 'start';
// FAIL exists only inside a sequence: an edge to it is the sequence stage's
// on_fail (FLOWLANG §1 Sequences), spliced in where it is said.
export const FAIL = 'fail';

/** Whether a route target is reserved rather than a stage: the two ends, the
 *  deliberate stop, the debug leaf — and, inside a sequence, `fail`. None of
 *  them takes max or then. */
export const isReservedTarget = (to: string) =>
	to === END || to === DONE || to === ABORT || to === DEBUG || to === FAIL;
export const EDGE_KINDS = ['on_fail', 'on_reject', 'on_timeout'] as const;
export type EdgeKind = (typeof EDGE_KINDS)[number];

/** 'sequence' is the editor's name for a stage that runs a sequence in its
 *  place: the YAML gives it `sequence:` and no type. */
export type StageKind =
	'agent' | 'command' | 'human_gate' | 'action' | 'wait' | 'service' | 'sequence';

// CURRENT_VERSION is the grammar this build writes; every document declares
// it, so a future break is a refusal rather than a mis-parse (FLOWLANG §1).
export const CURRENT_VERSION = 5;

// An edge is a chain: when a link's max is exhausted the next failure
// falls through to `then` (retry-then-escalate). A link with to: abort
// stops the run, one with to: debug parks it for a human; both carry no
// max/then.
//
// An edge does not say what it carries: its kind decides that (edge.verdict
// for a rejection, edge.failure for a failure), and the TARGET stage binds it
// as an input.
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
	// A service fails when it does not come up healthy, and times out when it
	// takes longer than its timeout to.
	if (type === 'agent' || type === 'command' || type === 'wait' || type === 'service')
		out.push('on_fail', 'on_timeout');
	if (type === 'agent' || type === 'human_gate') out.push('on_reject');
	// An action fails when its outcome is wrong in a fixable way (a merge
	// that conflicts, a push the remote refuses).
	if (type === 'action' || type === 'sequence') out.push('on_fail');
	return out;
}

/** Which edge source an edge kind's carried content fills (mirror of Go). */
export function feedSourceFor(kind: EdgeKind): string {
	return kind === 'on_reject' ? SOURCE_VERDICT : SOURCE_FAILURE;
}

// Sources are where an input comes from (FLOWLANG §4): a path some stage of
// the flow writes, a file of the repository (repo:path), or a value the
// engine provides — a fact about the run (run.*) or what the edge that
// entered the stage carried (edge.*). Every engine source has one type.
export const REPO_PREFIX = 'repo:';
export const SOURCE_TICKET = 'run.ticket';
export const SOURCE_STATUS = 'run.git.status';
export const SOURCE_DIFF_TARGET = 'run.git.diff_target';
export const SOURCE_DIFF_PHASE = 'run.git.diff_phase';
export const SOURCE_MR_URL = 'run.mr.url';
export const SOURCE_WORKSPACE = 'run.workspace';
// What failed in CI on the merge request a ci_failed watcher opened on: each
// check's CI file, job and failed steps (never a log).
export const SOURCE_CI_FAILURE = 'run.ci.failure';
// What the edge that entered a stage carried: a verdict (a rejection's
// reasons; an approval's, on a gate's or a verdict's success), a failing
// stage's output. Empty otherwise, so a port reading one is optional.
export const SOURCE_VERDICT = 'edge.verdict';
export const SOURCE_FAILURE = 'edge.failure';
export const ENGINE_SOURCES: Record<string, string> = {
	[SOURCE_TICKET]: 'ticket',
	[SOURCE_STATUS]: 'changeset',
	[SOURCE_DIFF_TARGET]: 'changeset',
	[SOURCE_DIFF_PHASE]: 'changeset',
	[SOURCE_MR_URL]: 'link',
	[SOURCE_WORKSPACE]: 'link',
	[SOURCE_CI_FAILURE]: 'failure',
	[SOURCE_FAILURE]: 'failure',
	[SOURCE_VERDICT]: 'verdict'
};
// A running service's address is a family of sources, one per service:
// run.service.<name>, the name a project service's or a $parameter naming
// one (run.service.$app). Every one of them is a link.
export const SERVICE_SOURCE_PREFIX = 'run.service.';
export const serviceSource = (name: string) => SERVICE_SOURCE_PREFIX + name;
/** The service a source names ("" when it names none): a slug, or a $param. */
export function sourceService(v: string): string {
	if (!v.startsWith(SERVICE_SOURCE_PREFIX)) return '';
	const name = v.slice(SERVICE_SOURCE_PREFIX.length);
	return /^\$?[a-z][a-z0-9_-]*$/.test(name) ? name : '';
}
/** The type of a value the engine provides, or undefined for a file. */
export const sourceType = (v: string): string | undefined =>
	ENGINE_SOURCES[v] ?? (sourceService(v) ? 'link' : undefined);
export const isEngineSource = (v: string) => sourceType(v) != null;
export const isEdgeSource = (v: string) => v === SOURCE_VERDICT || v === SOURCE_FAILURE;

/** Port types (mirror of agentspec): what a file holds, what the engine
 *  provides. `files` is a directory of files — its path ends in "/". */
export const FILE_TYPES = [
	'file',
	'verdict',
	'review_threads',
	'review_replies',
	'commit_message',
	'files'
];
/** A verdict is also what edge.verdict gives: the one file type an engine
 *  source has too. */
export const VALUE_TYPES = ['ticket', 'changeset', 'failure', 'link'];
/** What a gate shows the person: files, and folders of them (a link is
 *  typed by its source). */
export const GATE_TYPES = ['file', 'files'];
/** The port types a schema never goes with: a plain file says nothing of
 *  its shape, a directory holds many, a value is the engine's. */
export const takesSchema = (type?: string) =>
	type !== 'file' && type !== 'files' && !VALUE_TYPES.includes(type ?? '');

/** Which engine sources each stage type may read (mirror of the Go rules):
 *  an agent any — run.ci.failure only in a ci_failed watcher (`ciFailed`) —
 *  a gate a link; the services' addresses among them, one per service the
 *  flow's stages name (`services`). */
export function sourcesFor(type: StageKind, services: string[] = [], ciFailed = false): string[] {
	const running = services.map(serviceSource);
	if (type === 'agent')
		return [
			...Object.keys(ENGINE_SOURCES).filter((v) => ciFailed || v !== SOURCE_CI_FAILURE),
			...running
		];
	if (type === 'human_gate') return [SOURCE_WORKSPACE, SOURCE_MR_URL, ...running];
	return [];
}

/** What a service stage does to its service; start (the default) waits until
 *  it is healthy. */
export const SERVICE_DOS = ['start', 'restart', 'stop'] as const;
export type ServiceDo = (typeof SERVICE_DOS)[number];

/** The services the flow names — a service stage's service, a source's —
 *  as written (`$app` stays `$app`), in document order. */
export function flowServices(doc: FlowDoc): string[] {
	const out = new Set<string>();
	for (const s of allStages(doc)) {
		if (s.type === 'service' && s.service) out.add(s.service);
		for (const p of Object.values(s.input ?? {})) {
			const name = sourceService(p.from ?? '');
			if (name) out.add(name);
		}
	}
	return [...out];
}

/** One port as the document writes it: an input's source, an output's
 *  destination, and what it is. */
export interface DocPort {
	from?: string;
	to?: string;
	type?: string;
	schema?: string;
	description?: string;
	optional?: boolean;
}

// A stage's input or output: an ordered mapping of port name to port
// (FLOWLANG §4). One form for every stage type — the name is checked against a
// contract where one exists (an agent's decls, an action's signature) and is a
// free label otherwise. Object insertion order is preserved on the wire (the Go
// side emits ordered JSON), so ports never reorder across a round-trip.
export type StagePorts = Record<string, DocPort>;

export const portEntries = (v?: StagePorts): [string, DocPort][] => (v ? Object.entries(v) : []);

/** A port's source (an input) or destination (an output). */
export const portValue = (p?: DocPort) => p?.from ?? p?.to ?? '';

export interface DocStage {
	id: string;
	type: StageKind;
	/** sequence: the sequence this stage runs in its place. */
	sequence?: string;
	/** Optional human note; documentation only, reaches no prompt. */
	description?: string;
	agent?: string;
	model?: string;
	harness?: string;
	max_turns?: number | string;
	timeout?: string;
	input?: StagePorts;
	output?: StagePorts;
	/** action: its settings — literals, or $parameters. */
	with?: Record<string, string | string[]>;
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
	/** A condition (FLOWLANG §2): `tracker`, `subject`, `$bool_param`, each
	 *  optionally negated as `not x`. A stage whose condition does not hold
	 *  completes as skipped. */
	run_if?: string;
	/** action: a slug from the compiled-in registry (GET /api/actions). */
	action?: string;
	/** wait: a signal slug to block on (GET /api/signals). */
	for?: string;
	/** service: a project service, or a $parameter naming one (`$app`). */
	service?: string;
	/** service: what it does to it; absent = start. */
	do?: ServiceDo;
	on_success?: string;
	on_fail?: DocEdge;
	on_reject?: DocEdge;
	on_timeout?: DocEdge;
	/** Editor-only: the chain this stage belongs to — `main`, or a watcher's
	 *  or an outcome's id. The YAML writes stages under their section; the
	 *  editor keeps one flat list. */
	phase?: string;
}

/** A phase's `on:` — the wait point that opens it (FLOWLANG §1): nothing
 *  (signal '') for the main chain, which runs when a person or an automation
 *  starts the run; manual for a watcher a person starts from the run page;
 *  or an observed signal with
 *  its start policy (auto | confirm | manual, or a $param). `max` may read
 *  an int parameter ("$name"). */
export interface DocWait {
	signal: string;
	start?: string;
	quiet_for?: string;
	max?: number | string;
}

/** Which section a phase is written in (FLOWLANG §1): the main chain a run
 *  starts with, a watcher that runs its chain on each new occurrence of its
 *  signal, or an outcome that fires once and ends the run. */
export type PhaseKind = 'main' | 'watcher' | 'outcome';
export const PHASE_KINDS: PhaseKind[] = ['main', 'watcher', 'outcome'];

/** A phase header: what opens it (`on`), its section and, tagged with its
 *  id, the flat list's stages it runs. */
export interface DocPhase {
	id: string;
	/** Editor-only: the section it is written in. */
	kind: PhaseKind;
	description?: string;
	on: DocWait;
	/** An OUTCOME's end of the run, done (the default) or abort: its last
	 *  stage's success, or all it does when it has no chain. */
	then?: string;
}

/** The main chain's id — and the sequence other chains run it by. */
export const MAIN = 'main';
// MANUAL opens a watcher from the run page only. Every other signal is
// observed on the run's merge requests, and takes a start policy. The main
// chain has none: it runs when the run is started.
export const MANUAL = 'manual';
export const START_POLICIES = ['auto', 'confirm', 'manual'] as const;
export type StartPolicy = (typeof START_POLICIES)[number];
/** What a flow's runs are started on (`operates_on:`): a ticket, the default,
 *  or a merge request someone opened. */
export const OPERATES_ON = ['ticket', 'merge_request'] as const;
export type OperatesOn = (typeof OPERATES_ON)[number];

/** One parameter of the flow's `parameters:` block (FLOWLANG §1): what it
 *  holds — its declared type, and whether it is a list (the engine's six
 *  take theirs from the engine) — its default, and optionally a description
 *  and an enum. Values come from outside the document (the workshop's
 *  bindings, a run's overrides). */
export interface DocParam {
	type?: 'bool' | 'int' | 'float' | 'string' | 'duration';
	list?: boolean;
	default: boolean | number | string | (boolean | number | string)[];
	description?: string;
	enum?: string[];
}

/** The six parameters the engine reads itself. harness and model are
 *  required; the rest exist at the engine's defaults when omitted. */
export const KNOWN_PARAMS = [
	'harness',
	'model',
	'timeout',
	'max_turns',
	'max_cost_usd',
	'max_total_turns'
] as const;

// One declared input of an agent: what the stage binds by name.
export interface DocAgentInput {
	name: string;
	description?: string;
	optional?: boolean;
	/** What it reads: a port type, a schema under `schemas:`, or both. */
	type?: string;
	schema?: string;
}

// One declared output: a file the agent must write — what, never where (a
// stage running it says). A `type` is `file` or a structure something built
// in reads (`verdict`: the APPROVE/REJECT on_reject routes from;
// `commit_message`: what the commit action commits with); a `schema` names
// one of the flow's own structures, alone or narrowing the type.
export interface DocAgentOutput {
	name: string;
	description?: string;
	type?: string;
	schema?: string;
}

/** One of the flow's structures (FLOWLANG §4): a JSON Schema, named by the
 *  agents' inputs and outputs that read and write it. */
export interface DocSchema {
	name: string;
	description?: string;
	json_schema: unknown;
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
	/** The flow's MCP servers it may call, by name. */
	mcp?: string[];
	inputs?: DocAgentInput[];
	outputs?: DocAgentOutput[];
}

/** One of the flow's MCP servers (FLOWLANG §1 mcp_servers): exactly one of a
 *  server apothecary runs (builtin), a program speaking MCP on stdio
 *  (command: the program, then its arguments) or a remote url. A command
 *  takes env, a url headers; their values may read a project secret as
 *  ${secrets.<name>} — nowhere else may. */
export interface DocMCPServer {
	name: string;
	description?: string;
	builtin?: string;
	command?: string[];
	url?: string;
	env?: Record<string, string>;
	headers?: Record<string, string>;
}

/** The servers apothecary provides (mirror of flow.MCPBuiltins): the
 *  project's browser, one Chromium per agent session. */
export const MCP_BUILTINS = ['browser'];

/** How a server is reached — which of its three fields it sets. A server
 *  that sets none yet reads as a command, the one most are. */
export type MCPKind = 'builtin' | 'command' | 'url';
export const MCP_KINDS: MCPKind[] = ['builtin', 'command', 'url'];
export const mcpKind = (m: DocMCPServer): MCPKind =>
	m.builtin != null ? 'builtin' : m.url != null ? 'url' : 'command';

/** A sequence (FLOWLANG §1 Sequences): a chain of stages written once and run
 *  by every stage that names it. Its stages stay as written — their ids are
 *  the sequence's own, `fail` is the sequence stage's on_fail, and a last
 *  stage without on_success leaves by the sequence stage's success. */
export interface DocSequence {
	id: string;
	description?: string;
	stages: DocStage[];
}

export interface FlowDoc {
	name: string;
	version?: number;
	description?: string;
	/** What the flow's runs are started on; absent = a ticket. */
	operates_on?: OperatesOn;
	/** The Ask pane: a read-only session beside each run, on this harness
	 *  (a kind or a $parameter); absent = no pane. */
	ask?: { harness: string };
	/** The flow's one settings block, in document order. */
	parameters: Record<string, DocParam>;
	/** The flow's structures, in document order. */
	schemas?: DocSchema[];
	/** The MCP servers its agents may use, in document order. */
	mcp_servers?: DocMCPServer[];
	agents?: DocAgent[];
	/** Sequences, in document order; the canvas shows the stages that run
	 *  them. */
	sequences?: DocSequence[];
	/** Every chain's stages, in section order, each tagged with its phase. */
	stages: DocStage[];
	/** The chains' headers, in section order: the main chain (a flow that
	 *  only watches has none), the watchers, the outcomes. Absent for a
	 *  sequence drawn on its own. */
	phases?: DocPhase[];
}

/** A watcher or an outcome as the document writes it: `on` may be the bare
 *  signal, and `sequence: X` stands for a chain of one stage running X. */
interface WireWatcher {
	id: string;
	description?: string;
	on: DocWait | string;
	sequence?: string;
	stages?: DocStage[];
	then?: string;
}

/** The wire document: a section per kind of chain, and sequence stages with
 *  no type. */
type WireDoc = Omit<FlowDoc, 'stages' | 'phases'> & {
	main?: DocStage[];
	watchers?: WireWatcher[];
	outcomes?: WireWatcher[];
};

/** An edge as the editor holds it. The server's document spells every edge
 *  as a mapping; YAML parsed here keeps the scalar sugar (`on_fail: debug`,
 *  `then: fail`), which this unfolds. */
function wireEdge(e: unknown): DocEdge | undefined {
	if (e == null) return undefined;
	if (typeof e === 'string') return { to: e };
	const m = e as DocEdge;
	return { ...m, then: wireEdge(m.then) };
}

/** A wire stage as the editor holds it: a sequence stage gets its kind,
 *  edges their mapping form. */
function tagStage(s: DocStage): DocStage {
	const out: DocStage = { ...s, type: s.sequence ? 'sequence' : s.type };
	for (const kind of EDGE_KINDS) if (s[kind] != null) out[kind] = wireEdge(s[kind]);
	return out;
}

// normalizeDoc turns the server's document — or a pinned one parsed here —
// into the editor's: every chain's stages flattened in section order, each
// tagged with its phase, and `sequence: X` read the way the parser reads it,
// as a chain of one stage that runs X, named after its watcher.
//
// It also makes every stage's success target explicit. The YAML may leave
// `on_success` out ("next stage in document order"); the graph resolves that
// sugar when it draws, but a form bound to the raw field read the absence as
// "done" — the canvas and the sidebar disagreed. docToYaml writes it explicitly
// anyway, so materializing it here changes no saved document.
export function normalizeDoc(raw: unknown): FlowDoc {
	const { main, watchers, outcomes, ...w } = raw as WireDoc;
	const stages: DocStage[] = [];
	const phases: DocPhase[] = [];
	if (main?.length) {
		phases.push({ id: MAIN, kind: 'main', on: { signal: '' } });
		for (const st of main) stages.push({ ...tagStage(st), phase: MAIN });
	}
	const section = (list: WireWatcher[] | undefined, kind: PhaseKind) => {
		for (const { stages: inner, sequence, on, ...head } of list ?? []) {
			phases.push({ ...head, kind, on: typeof on === 'string' ? { signal: on } : { ...on } });
			const chain = sequence ? [{ id: head.id, sequence } as DocStage] : (inner ?? []);
			for (const st of chain) stages.push({ ...tagStage(st), phase: head.id });
		}
	};
	section(watchers, 'watcher');
	section(outcomes, 'outcome');
	const doc: FlowDoc = { ...(w as FlowDoc), stages, phases };
	doc.stages.forEach((s, i) => (s.on_success = effectiveSuccess(doc, i)));
	// A sequence's stages keep their sugar: its last stage's success is
	// whichever sequence stage runs it.
	if (doc.sequences)
		doc.sequences = doc.sequences.map((q) => ({ ...q, stages: q.stages.map(tagStage) }));
	// A parameter may be written as its bare default (`harness: opencode`):
	// the editor always holds the long form.
	doc.parameters = Object.fromEntries(
		Object.entries(doc.parameters ?? {}).map(([name, p]) => [
			name,
			p !== null && typeof p === 'object' ? p : { default: p as DocParam['default'] }
		])
	);
	return doc;
}

/** expandDoc writes every sequence stage out the way the parser does
 *  (FLOWLANG §1 Sequences): ids take its path (make.ship.test), a sequence's
 *  routes resolve to its expanded stages, its last stage leaves by the
 *  sequence stage's success, and `fail` becomes the sequence stage's
 *  on_fail. For what shows a run,
 *  whose events name the expanded stages; the editor edits the unexpanded
 *  document. The document is one the server parsed, so nothing is checked. */
export function expandDoc(doc: FlowDoc): FlowDoc {
	if (!doc.sequences?.length) return doc;
	const sequences = new Map(doc.sequences.map((q) => [q.id, q]));
	const entryOf = (name: string, prefix: string): string => {
		const first = sequences.get(name)?.stages[0];
		if (!first) return prefix;
		return first.sequence ? entryOf(first.sequence, `${prefix}${first.id}.`) : prefix + first.id;
	};
	const top = new Map(
		doc.stages.filter((s) => s.sequence).map((s) => [s.id, entryOf(s.sequence!, `${s.id}.`)])
	);
	interface Scope {
		prefix: string;
		local?: Map<string, string>;
		onSuccess?: string;
		onFail?: DocEdge;
	}
	const target = (sc: Scope, to: string) =>
		isReservedTarget(to) ? to : ((sc.local ? sc.local.get(to) : top.get(to)) ?? to);
	const edgeIn = (sc: Scope, e?: DocEdge): DocEdge | undefined => {
		if (!e) return undefined;
		if (e.to === FAIL) return sc.onFail && structuredClone(sc.onFail);
		return { ...e, to: target(sc, e.to), then: edgeIn(sc, e.then) };
	};
	const expand = (stages: DocStage[], sc: Scope, phase?: string): DocStage[] =>
		stages.flatMap((s, i) => {
			let succ = s.on_success ? target(sc, s.on_success) : undefined;
			if (!succ && i === stages.length - 1) succ = sc.onSuccess;
			const onFail = edgeIn(sc, s.on_fail);
			if (s.type !== 'sequence') {
				return [
					{
						...s,
						id: sc.prefix + s.id,
						phase,
						on_success: succ,
						on_fail: onFail,
						on_reject: edgeIn(sc, s.on_reject),
						on_timeout: edgeIn(sc, s.on_timeout)
					}
				];
			}
			const seq = sequences.get(s.sequence ?? '');
			if (!seq) return [];
			const prefix = `${sc.prefix}${s.id}.`;
			const local = new Map(
				seq.stages.map((q) => [
					q.id,
					q.sequence ? entryOf(q.sequence, `${prefix}${q.id}.`) : prefix + q.id
				])
			);
			return expand(seq.stages, { prefix, local, onSuccess: succ, onFail }, phase);
		});
	const phaseIds = doc.phases?.length ? doc.phases.map((p) => p.id) : [undefined];
	const stages = phaseIds.flatMap((ph) =>
		expand(
			doc.stages.filter((s) => s.phase === ph),
			{ prefix: '' },
			ph
		)
	);
	const out: FlowDoc = { ...doc, sequences: undefined, stages };
	out.stages.forEach((s, i) => (s.on_success ??= effectiveSuccess(out, i)));
	return out;
}

/** The stages a stage may route to: its own phase's (a phase's routes stay
 *  inside it — FLOWLANG §1; every stage in an unphased flow). */
export function routableFrom(doc: FlowDoc, s: DocStage): DocStage[] {
	return stagesOf(doc, s.phase);
}

// pruneCrossPhase removes every route that leaves its stage's phase — what a
// stage moved to another phase leaves behind, both its own routes out and the
// routes into it. A cut success route ends the phase (end); a cut chain link
// drops out with its fallbacks spliced up (pruneEdgeTarget).
export function pruneCrossPhase(doc: FlowDoc) {
	const phaseOf = new Map(doc.stages.map((s) => [s.id, s.phase ?? '']));
	const away = (s: DocStage, to: string) =>
		!isReservedTarget(to) && phaseOf.get(to) !== (s.phase ?? '');
	for (const s of doc.stages) {
		if (s.on_success && away(s, s.on_success)) s.on_success = END;
		for (const kind of EDGE_KINDS) {
			let e = edgeOf(s, kind);
			for (const l of chainLinks(e)) if (away(s, l.to)) e = pruneEdgeTarget(e, l.to);
			setEdge(s, kind, e);
		}
	}
}

/** The stages of one phase, in document order ('' = an unphased flow). */
export function stagesOf(doc: FlowDoc, phase: string | undefined): DocStage[] {
	return doc.stages.filter((s) => (s.phase ?? '') === (phase ?? ''));
}

/** A phase's first stage: where its trigger enters. */
export function phaseEntry(doc: FlowDoc, phase: string | undefined): DocStage | undefined {
	return doc.stages.find((s) => (s.phase ?? '') === (phase ?? ''));
}

/** The phase a stage belongs to. */
export function phaseOf(doc: FlowDoc, s: DocStage): DocPhase | undefined {
	return doc.phases?.find((p) => p.id === s.phase);
}

/** "mr_conflicts (confirm)" — a phase's wait point in a few words. */
export function triggerLabel(w: DocWait): string {
	if (!fires(w)) return w.signal;
	const bits: string[] = [];
	if (w.start && w.start !== 'auto') bits.push(w.start);
	if (w.quiet_for) bits.push(`quiet ${w.quiet_for}`);
	if (w.max != null) bits.push(`max ${w.max}`);
	return bits.length ? `${w.signal} (${bits.join(', ')})` : w.signal;
}

/** Whether a phase's signal fires on its own — and so takes a start policy.
 *  The main chain (no signal) and manual are a person's act. */
export function fires(w: DocWait): boolean {
	return !!w.signal && w.signal !== MANUAL;
}

/** A parameter's default as text, or undefined when the document omits it
 *  (a known one then runs at the engine's default). */
export function paramDefault(doc: FlowDoc, name: string): string | undefined {
	const p = doc.parameters?.[name];
	if (p == null) return undefined;
	return Array.isArray(p.default) ? p.default.join(', ') : String(p.default);
}

/** The flow's definition of an agent by name, or undefined. */
export function agentOf(doc: FlowDoc, name: string): DocAgent | undefined {
	return doc.agents?.find((a) => a.name === name);
}

// effectiveSuccess resolves the YAML sugar for display: explicit target, or
// next stage in document order, or the end of the chain. "Next" never crosses
// into another chain. A chain's last stage ends its phase — unless nothing
// could follow: a main chain with no watcher or outcome is done, and an
// outcome ends the run the way its then says.
export function effectiveSuccess(doc: FlowDoc, i: number): string {
	const s = doc.stages[i];
	if (s.on_success) return s.on_success;
	const next = doc.stages[i + 1];
	if (next && (next.phase ?? '') === (s.phase ?? '')) return next.id;
	if (!doc.phases?.length) return DONE;
	const ph = phaseOf(doc, s);
	if (ph?.kind === 'outcome') return ph.then ?? DONE;
	return ph?.kind === 'main' && doc.phases.length === 1 ? DONE : END;
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
// front of that phase's stages).
export function setEntry(doc: FlowDoc, id: string): boolean {
	const i = doc.stages.findIndex((s) => s.id === id);
	if (i < 0) return false;
	const first = doc.stages.findIndex((s) => (s.phase ?? '') === (doc.stages[i].phase ?? ''));
	if (first === i) return false;
	const [s] = doc.stages.splice(i, 1);
	doc.stages.splice(first, 0, s);
	return true;
}

// movePhase reassigns a stage to another phase, at the end of its stages, and
// cuts the routes that would now cross a phase boundary (a parse error).
export function movePhase(doc: FlowDoc, id: string, phase: string) {
	placeInPhase(doc, id, phase);
	pruneCrossPhase(doc);
}

function placeInPhase(doc: FlowDoc, id: string, phase: string) {
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
		// An empty chain: place its stages where the section order says.
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
		if (next === END || next === DONE) break;
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
	if (doc.operates_on && doc.operates_on !== 'ticket') obj.operates_on = doc.operates_on;
	if (doc.ask?.harness) obj.ask = { harness: doc.ask.harness };
	// The one settings block: the six the engine reads, in their order (the
	// order the editor shows them in), then the flow's own in document order
	// — a bare default when that is all a parameter has.
	const params: Record<string, unknown> = {};
	const known = KNOWN_PARAMS.filter((k) => doc.parameters?.[k] != null);
	const own = Object.keys(doc.parameters ?? {}).filter(
		(k) => !(KNOWN_PARAMS as readonly string[]).includes(k)
	);
	for (const name of [...known, ...own]) {
		const p = doc.parameters[name];
		const isKnown = (KNOWN_PARAMS as readonly string[]).includes(name);
		// The engine's own take a bare default; the flow's declare what they hold.
		if (isKnown && !p.description && !p.enum?.length) params[name] = p.default;
		else {
			const o: Record<string, unknown> = {};
			if (!isKnown && p.type) o.type = p.type;
			if (!isKnown && p.list) o.list = true;
			o.default = p.default;
			if (p.description) o.description = p.description;
			if (p.enum?.length) o.enum = p.enum;
			params[name] = o;
		}
	}
	obj.parameters = params;
	// Schemas before the agents that name them.
	if (doc.schemas?.length) {
		obj.schemas = doc.schemas.map((q) => {
			const o: Record<string, unknown> = { name: q.name };
			if (q.description) o.description = q.description;
			o.json_schema = q.json_schema;
			return o;
		});
	}
	// MCP servers before the agents that list them: name first, then what
	// reaches it, then what it is given.
	if (doc.mcp_servers?.length) {
		obj.mcp_servers = doc.mcp_servers.map((m) => {
			const o: Record<string, unknown> = { name: m.name };
			if (m.description) o.description = m.description;
			const kind = mcpKind(m);
			if (kind === 'builtin') o.builtin = m.builtin;
			else if (kind === 'url') o.url = m.url;
			else o.command = m.command ?? [];
			if (kind === 'command' && m.env && Object.keys(m.env).length) o.env = m.env;
			if (kind === 'url' && m.headers && Object.keys(m.headers).length) o.headers = m.headers;
			return o;
		});
	}
	// Agents before stages: a reader meets the contracts before the wiring
	// that binds them. Order within the list is the author's.
	if (doc.agents?.length) {
		obj.agents = doc.agents.map((a) => {
			const o: Record<string, unknown> = { name: a.name };
			if (a.description) o.description = a.description;
			if (a.read_only) o.read_only = a.read_only;
			if (a.allow_bash) o.allow_bash = a.allow_bash;
			if (a.mcp?.length) o.mcp = a.mcp;
			if (a.inputs?.length) {
				o.inputs = a.inputs.map((i) => {
					const io: Record<string, unknown> = { name: i.name };
					if (i.description) io.description = i.description;
					if (i.optional) io.optional = i.optional;
					if (i.type) io.type = i.type;
					if (i.schema) io.schema = i.schema;
					return io;
				});
			}
			if (a.outputs?.length) {
				o.outputs = a.outputs.map((out) => {
					const oo: Record<string, unknown> = { name: out.name };
					if (out.description) oo.description = out.description;
					if (out.type) oo.type = out.type;
					if (out.schema) oo.schema = out.schema;
					return oo;
				});
			}
			// Prompt last: it is the long block, so the structural fields stay
			// readable at the top of each entry.
			o.prompt = a.prompt;
			return o;
		});
	}
	// A sequence's stages are written as they were (explicit=false): their
	// success sugar depends on the sequence stage that runs them.
	const stageYaml = (s: DocStage, explicit = true) => {
		if (s.type === 'sequence') {
			const seq: Record<string, unknown> = { id: s.id, sequence: s.sequence ?? '' };
			if (s.description) seq.description = s.description;
			const succ = explicit
				? (s.on_success ?? effectiveSuccess(doc, idx.get(s.id)!))
				: s.on_success;
			if (succ) seq.on_success = succ;
			if (s.on_fail) seq.on_fail = edgeToYamlValue(s.on_fail);
			return seq;
		}
		const o: Record<string, unknown> = { id: s.id, type: s.type };
		if (s.description) o.description = s.description;
		if (s.run_if) o.run_if = s.run_if;
		if (s.agent) o.agent = s.agent;
		// A service stage's what before its how long.
		if (s.service) o.service = s.service;
		if (s.do) o.do = s.do;
		if (s.model) o.model = s.model;
		if (s.harness) o.harness = s.harness;
		if (s.max_turns != null) o.max_turns = s.max_turns;
		if (s.timeout) o.timeout = s.timeout;
		// input/output are three-way unions; emit only a non-empty one. Guarding
		// on `.length` alone would silently drop the named form (objects have no
		// length), losing every binding on save.
		if (nonEmptyIO(s.input)) o.input = portsYaml(s.input!);
		if (nonEmptyIO(s.output)) o.output = portsYaml(s.output!);
		if (s.with && Object.keys(s.with).length) o.with = s.with;
		if (s.run) o.run = s.run;
		if (s.run_ref) o.run_ref = s.run_ref;
		if (s.each_repo) o.each_repo = true;
		if (s.question) o.question = s.question;
		if (s.auto_approve) o.auto_approve = s.auto_approve;
		if (s.auto_reject) o.auto_reject = s.auto_reject;
		if (s.action) o.action = s.action;
		if (s.for) o.for = s.for;
		if (explicit) o.on_success = s.on_success ?? effectiveSuccess(doc, idx.get(s.id)!);
		else if (s.on_success) o.on_success = s.on_success;
		for (const kind of EDGE_KINDS) {
			const e = edgeOf(s, kind);
			if (e) o[kind] = edgeToYamlValue(e);
		}
		return o;
	};
	// Sequences before the chains that run them, as written.
	if (doc.sequences?.length) {
		obj.sequences = doc.sequences.map((b) => {
			const o: Record<string, unknown> = { id: b.id };
			if (b.description) o.description = b.description;
			o.stages = b.stages.map((s) => stageYaml(s, false));
			return o;
		});
	}
	// The sections: the main chain, then the watchers and the outcomes —
	// each opened by its `on`, an outcome ending the run by its `then`.
	const chainOf = (ph: DocPhase) => ordered.filter((s) => s.phase === ph.id);
	const main = doc.phases?.find((p) => p.kind === 'main');
	if (main) obj.main = chainOf(main).map((s) => stageYaml(s));
	const section = (kind: PhaseKind) =>
		(doc.phases ?? [])
			.filter((p) => p.kind === kind)
			.map((ph) => {
				const o: Record<string, unknown> = { id: ph.id };
				if (ph.description) o.description = ph.description;
				o.on = triggerYaml(ph.on);
				const own = chainOf(ph);
				// An outcome with no chain is nothing but its `then`.
				if (kind === 'outcome' && (ph.then || !own.length)) o.then = ph.then ?? DONE;
				const sequence = sequenceOf(ph, own);
				if (sequence) o.sequence = sequence;
				else if (own.length) o.stages = own.map((s) => stageYaml(s));
				return o;
			});
	const watchers = section('watcher');
	if (watchers.length) obj.watchers = watchers;
	const outcomes = section('outcome');
	if (outcomes.length) obj.outcomes = outcomes;
	return stringify(obj, { lineWidth: 0 });
}

// sequenceOf is the sequence a chain is written as `sequence: X` for: a
// chain of one sequence stage named after its phase, leaving the way the
// chain ends on its own — exactly what the parser reads `sequence: X` as.
function sequenceOf(ph: DocPhase, own: DocStage[]): string | undefined {
	if (own.length !== 1) return undefined;
	const [s] = own;
	const ends = ph.kind === 'outcome' ? (ph.then ?? DONE) : END;
	if (
		s.type !== 'sequence' ||
		s.id !== ph.id ||
		s.on_fail ||
		s.description ||
		s.on_success !== ends
	)
		return undefined;
	return s.sequence;
}

// triggerYaml uses the scalar sugar when the wait point has no options
// (`on: manual`, `on: mr_conflicts`).
function triggerYaml(w: DocWait): unknown {
	const start = w.start && w.start !== 'auto' ? w.start : undefined;
	if (!start && !w.quiet_for && w.max == null) return w.signal;
	const o: Record<string, unknown> = { signal: w.signal };
	if (start) o.start = start;
	if (w.quiet_for) o.quiet_for = w.quiet_for;
	if (w.max != null) o.max = w.max;
	return o;
}

// nonEmptyIO reports whether a port map has anything to serialize.
function nonEmptyIO(v?: StagePorts): boolean {
	return v != null && Object.keys(v).length > 0;
}

// portsYaml writes ports in the canonical key order, empty fields left out.
function portsYaml(ports: StagePorts): Record<string, unknown> {
	const out: Record<string, unknown> = {};
	for (const [name, p] of Object.entries(ports)) {
		const o: Record<string, unknown> = {};
		if (p.from) o.from = p.from;
		if (p.to) o.to = p.to;
		if (p.type) o.type = p.type;
		if (p.schema) o.schema = p.schema;
		if (p.description) o.description = p.description;
		if (p.optional) o.optional = true;
		out[name] = o;
	}
	return out;
}

// edgeToYamlValue serializes one chain link (recursing into then). A link to
// a reserved target uses the parser's scalar sugar: `on_fail: end`,
// `on_reject: abort`, `then: debug`.
function edgeToYamlValue(e: DocEdge): unknown {
	if (isReservedTarget(e.to)) return e.to;
	const eo: Record<string, unknown> = { to: e.to, max: e.max };
	if (e.then) eo.then = edgeToYamlValue(e.then);
	return eo;
}

/** A node's rendered size, as Svelte Flow measured it. FlowGraph reports them
 *  keyed the way `layout` is: stage ids, START/END/DONE/ABORT, HUB,
 *  `trigger:<phase>`, `end|done|abort:<phase>`, `section:<kind>` and
 *  `debug:<stage>`. */
export interface NodeSize {
	width: number;
	height: number;
}

// Layout metrics: the gaps between nodes, and the size assumed for a node
// nobody has measured yet (a first render, a stage just typed into the YAML).
const GAP_X = 56;
const GAP_Y = 72;
const BAND_GAP = 140;
const LANE_INDENT = 120;
const GUESS: NodeSize = { width: 220, height: 120 };
const GUESS_TERMINAL: NodeSize = { width: 110, height: 44 };
const GUESS_SECTION: NodeSize = { width: 90, height: 18 };
const GUESS_HUB: NodeSize = { width: 150, height: 52 };
const SECTION_GAP = 14;
const LEAF_GAP = 14;

/** The layout key of a phase's marker: START for the main chain, every
 *  other phase its own. */
export const markerKey = (ph: DocPhase | undefined) =>
	!ph || ph.kind === 'main' ? START : `trigger:${ph.id}`;
/** The layout key of the hub: where the run waits once the main chain is
 *  over, and every watcher and outcome branches from. */
export const HUB = 'hub';
/** Whether the canvas draws the hub: the flow has watchers or outcomes —
 *  something the run waits for. */
export const hasHub = (doc: FlowDoc) => !!doc.phases?.some((p) => p.kind !== 'main');

/** The ends a route may reach. */
export type Terminal = typeof END | typeof DONE | typeof ABORT;
/** The layout key of a terminal a chain reaches: the main chain's are the
 *  flow's END / DONE / ABORT (its end is the hub, once there is one); a
 *  watcher or an outcome has its own, at the end of its row. */
export const terminalKey = (to: Terminal, ph: DocPhase | undefined) =>
	!ph || ph.kind === 'main' ? to : `${to}:${ph.id}`;

/** The ends a chain's routes reach, in END / DONE / ABORT order — the
 *  terminals its row draws. An outcome with no chain reaches its then. */
export function chainEnds(doc: FlowDoc, ph: DocPhase): Terminal[] {
	const reached = new Set<string>();
	doc.stages.forEach((s, i) => {
		if (s.phase !== ph.id) return;
		reached.add(effectiveSuccess(doc, i));
		for (const k of EDGE_KINDS) for (const l of chainLinks(edgeOf(s, k))) reached.add(l.to);
	});
	if (!phaseEntry(doc, ph.id) && ph.kind === 'outcome') reached.add(ph.then ?? DONE);
	return ([END, DONE, ABORT] as Terminal[]).filter((x) => reached.has(x));
}

/** The layout key of a section's label on the canvas. */
export const sectionKey = (kind: PhaseKind) => `section:${kind}`;
/** Whether the canvas labels its sections: once a flow has more than its
 *  one chain. */
export const showsSections = (doc: FlowDoc) => (doc.phases?.length ?? 0) > 1;

/** One labelled control link leaving a stage: every link of every edge
 *  chain, in edge-kind then chain order. Its index is the label's row under
 *  the stage — FlowGraph draws, autoLayout reserves, from this one list. */
export function edgeLinks(s: DocStage): { kind: EdgeKind; link: DocEdge; depth: number }[] {
	return EDGE_KINDS.flatMap((kind) =>
		chainLinks(edgeOf(s, kind)).map((link, depth) => ({ kind, link, depth }))
	);
}

/** Edge-label rows under a stage (FlowLabeledEdge): the gap above the first
 *  row and each row's height. */
export const LABEL_PAD = 6;
export const LABEL_ROW = 20;

/** The layout key of a stage's debug leaf (one per stage that escalates to
 *  debug: the leaf is that stage's pause, not a shared terminal). */
export const debugKey = (stage: string) => `debug:${stage}`;

/** parksOnDebug reports whether any of the stage's edge chains reaches the
 *  debug leaf — the stage then draws its own leaf. */
export function parksOnDebug(s: DocStage): boolean {
	return EDGE_KINDS.some((k) => chainLinks(edgeOf(s, k)).some((l) => l.to === DEBUG));
}

// autoLayout places nodes left-to-right along the success chain, dropping
// off-chain stages into a lane below (used when no stored layout exists, and
// by the auto-arrange button). Each chain gets one band, top to bottom, its
// marker in a column on the left — the main chain, the watchers, the
// outcomes, each section under its label. Every column is as wide as the
// node in it and every row as tall as its tallest node — from the sizes
// Svelte Flow measured, when the caller has them — so nodes never overlap
// however many ports they grew.
export function autoLayout(
	doc: FlowDoc,
	sizes: Record<string, NodeSize> = {}
): Record<string, { x: number; y: number }> {
	const size = (key: string, guess = GUESS) => sizes[key] ?? guess;
	const pos: Record<string, { x: number; y: number }> = {};
	const bands: (DocPhase | undefined)[] = doc.phases?.length ? doc.phases : [undefined];
	const byId = new Map(doc.stages.map((s, i) => [s.id, i]));
	// What begins each band — its phase's marker — stands in one column on
	// the left, its left edges aligned: the widest marker sets where the
	// column starts.
	const markers = bands.map(markerKey);
	const markerX = -(Math.max(...markers.map((k) => size(k, GUESS_TERMINAL).width)) + GAP_X);
	const sections = showsSections(doc);
	const hub = hasHub(doc);
	const main = doc.phases?.find((p) => p.kind === 'main');
	// The hub stands left of the marker column, on a row of its own between
	// the main chain and what branches from it.
	const hubX = markerX - size(HUB, GUESS_HUB).width - GAP_X;
	let y0 = 0;
	let right = 0; // the widest band's right edge: where the terminals go
	let mainRight = 0; // the main chain's: where its own ends go, beside a hub
	bands.forEach((ph, band) => {
		const phase = ph?.id;
		// What comes after the main chain opens on the hub (a flow that only
		// watches begins there: its start marker above it).
		if (hub && ph && ph.kind !== 'main' && !(HUB in pos)) {
			if (!main) {
				pos[START] = { x: hubX, y: y0 };
				y0 += size(START, GUESS_TERMINAL).height + GAP_Y / 2;
			}
			pos[HUB] = { x: hubX, y: y0 };
			y0 += size(HUB, GUESS_HUB).height + GAP_Y / 2;
		}
		// A section begins: its label heads the marker column.
		if (sections && ph && ph.kind !== bands[band - 1]?.kind) {
			const key = sectionKey(ph.kind);
			pos[key] = { x: markerX, y: y0 };
			y0 += size(key, GUESS_SECTION).height + SECTION_GAP;
		}
		const chain = new Set<string>();
		// The chain ends where success leaves the stages: end, done, abort.
		let cur = phaseEntry(doc, phase)?.id;
		while (cur && byId.has(cur) && !chain.has(cur)) {
			chain.add(cur);
			cur = effectiveSuccess(doc, byId.get(cur)!);
		}
		const onChain: DocStage[] = [];
		const offChain: DocStage[] = [];
		for (const s of phaseOrder(doc, phase)) (chain.has(s.id) ? onChain : offChain).push(s);
		// Under a row: first the label rows of its stages' failure edges (each
		// label hangs under the handle it leaves from), then a strip for the
		// debug leaves of the stages that escalate there — each beside its own
		// stage. Returns the row's new bottom edge.
		const leaves = (row: DocStage[], bottom: number): number => {
			const lines = Math.max(0, ...row.map((s) => edgeLinks(s).length));
			const under = lines ? bottom + LABEL_PAD + lines * LABEL_ROW : bottom;
			let h = 0;
			for (const s of row) {
				if (!parksOnDebug(s)) continue;
				const key = debugKey(s.id);
				pos[key] = { x: pos[s.id].x + 24, y: under + LEAF_GAP };
				h = Math.max(h, size(key, GUESS_TERMINAL).height);
			}
			return h ? under + LEAF_GAP + h : under;
		};
		// Row 0: the chain. Row 1, below the tallest chain node: the rest.
		let x = 0;
		let rowH = 0;
		for (const s of onChain) {
			const { width, height } = size(s.id);
			pos[s.id] = { x, y: y0 };
			x += width + GAP_X;
			rowH = Math.max(rowH, height);
		}
		const y1 = leaves(onChain, y0 + rowH) + GAP_Y;
		let x1 = LANE_INDENT;
		let laneH = 0;
		for (const s of offChain) {
			const { width, height } = size(s.id);
			pos[s.id] = { x: x1, y: y1 };
			x1 += width + GAP_X;
			laneH = Math.max(laneH, height);
		}
		right = Math.max(right, x, offChain.length ? x1 : 0);
		if (!ph || ph.kind === 'main') mainRight = Math.max(x, offChain.length ? x1 : 0);
		// The band's marker: in the column, on its band's row. A chain-less
		// outcome is its marker alone, so its band is no taller.
		pos[markers[band]] = { x: markerX, y: y0 };
		// A watcher's or an outcome's ends close its row: where its chain goes
		// back to waiting, or ends the run.
		if (ph && ph.kind !== 'main') {
			let ty = y0;
			for (const to of chainEnds(doc, ph)) {
				const key = terminalKey(to, ph);
				pos[key] = { x, y: ty };
				ty += size(key, GUESS_TERMINAL).height + LEAF_GAP;
			}
		}
		if (!onChain.length && !offChain.length) {
			y0 += size(markers[band], GUESS_TERMINAL).height + GAP_Y / 2;
			return;
		}
		y0 = (offChain.length ? leaves(offChain, y1 + laneH) : leaves(onChain, y0 + rowH)) + BAND_GAP;
	});
	// The main chain's terminals: end, done and abort stacked right of
	// everything — right of the main chain, beside a hub, which is its end
	// (done and abort draw only when a route reaches them, but a position
	// costs nothing). Debug is no terminal — its leaves sit by their stages.
	pos[START] ??= { x: markerX, y: 0 };
	let ty = 0;
	for (const key of [END, DONE, ABORT]) {
		pos[key] = { x: hub ? mainRight : right, y: ty };
		ty += size(key, GUESS_TERMINAL).height + GAP_Y;
	}
	return pos;
}

// placeChain makes room on a laid-out canvas for a chain added since it was
// laid out: its band goes at the end of its section — its section's label
// first, when it opens the section — and the sections after it move down by
// its height, so a new watcher lands among the watchers and nothing else
// moves. Its marker and stages are placed; a chain already placed is left
// as it is.
export function placeChain(
	doc: FlowDoc,
	layout: Record<string, { x: number; y: number }>,
	id: string
) {
	const ph = doc.phases?.find((p) => p.id === id);
	if (!ph || layout[markerKey(ph)]) return;
	const others = (doc.phases ?? []).filter((p) => p.id !== id && layout[markerKey(p)]);
	const keysOf = (p: DocPhase) => [
		markerKey(p),
		...stagesOf(doc, p.id).flatMap((s) => [s.id, debugKey(s.id)]),
		...([END, DONE, ABORT] as Terminal[]).map((to) => terminalKey(to, p))
	];
	const height = (key: string) =>
		key === START || /^(trigger|debug|end|done|abort):/.test(key)
			? GUESS_TERMINAL.height
			: GUESS.height;
	const rank = PHASE_KINDS.indexOf(ph.kind);
	const before = others.filter((p) => PHASE_KINDS.indexOf(p.kind) <= rank);
	const after = others.filter((p) => PHASE_KINDS.indexOf(p.kind) > rank);
	const bottom = Math.max(
		...before.flatMap(keysOf).flatMap((k) => (layout[k] ? [layout[k].y + height(k)] : []))
	);
	let y = Number.isFinite(bottom) ? bottom + BAND_GAP : 0;
	const markerX = layout[START]?.x ?? (others.length ? layout[markerKey(others[0])].x : -280);
	const top = y;
	// The first watcher or outcome opens the hub, on a row above it.
	if (ph.kind !== 'main' && !layout[HUB]) {
		layout[HUB] = { x: markerX - GUESS_HUB.width - GAP_X, y };
		y += GUESS_HUB.height + GAP_Y / 2;
	}
	if (showsSections(doc) && !before.some((p) => p.kind === ph.kind)) {
		layout[sectionKey(ph.kind)] = { x: markerX, y };
		y += GUESS_SECTION.height + SECTION_GAP;
	}
	layout[markerKey(ph)] = { x: markerX, y };
	// Its stages in a row, from where the other chains' first stages start.
	const firsts = others.flatMap((p) => {
		const first = phaseEntry(doc, p.id);
		return first && layout[first.id] ? [layout[first.id].x] : [];
	});
	let x = firsts.length ? Math.min(...firsts) : markerX + GUESS_TERMINAL.width + GAP_X;
	const own = stagesOf(doc, ph.id);
	for (const s of own) {
		layout[s.id] = { x, y };
		x += GUESS.width + GAP_X;
	}
	// Its ends close the row.
	if (ph.kind !== 'main') {
		let ty = y;
		for (const to of chainEnds(doc, ph)) {
			layout[terminalKey(to, ph)] = { x, y: ty };
			ty += GUESS_TERMINAL.height + LEAF_GAP;
		}
	}
	// Everything after it moves down by what it took.
	const took = y - top + (own.length ? GUESS.height + BAND_GAP : GUESS_TERMINAL.height + GAP_Y / 2);
	const below = new Set(after.flatMap(keysOf));
	for (const p of after) below.add(sectionKey(p.kind));
	for (const k of below) if (layout[k]) layout[k] = { x: layout[k].x, y: layout[k].y + took };
}

// columnLayout stacks a sequence's stages top to bottom, in document order —
// a sequence is a short chain, read best down a narrow pane beside its flow.
// The marker sits above the first stage, each stage's debug leaf to its
// right, and the sequence's two exits (keys END and DONE, which a sequence
// view draws as its ways out) under the last stage.
export function columnLayout(
	doc: FlowDoc,
	sizes: Record<string, NodeSize> = {}
): Record<string, { x: number; y: number }> {
	const size = (key: string, guess = GUESS) => sizes[key] ?? guess;
	const pos: Record<string, { x: number; y: number }> = {};
	pos[START] = { x: 0, y: -(size(START, GUESS_TERMINAL).height + GAP_Y / 2) };
	let y = 0;
	for (const s of doc.stages) {
		const { width, height } = size(s.id);
		pos[s.id] = { x: 0, y };
		if (parksOnDebug(s)) pos[debugKey(s.id)] = { x: width + GAP_X, y: y + height / 2 };
		const labels = edgeLinks(s).length;
		y += height + GAP_Y / 2 + (labels ? LABEL_PAD + labels * LABEL_ROW : 0);
	}
	pos[END] = { x: 0, y };
	pos[DONE] = { x: size(END, GUESS_TERMINAL).width + GAP_X, y };
	return pos;
}

// freshPhaseId is a phase id not yet taken: watcher_1, outcome_2, …
function freshPhaseId(doc: FlowDoc, base: string): string {
	const ids = new Set((doc.phases ?? []).map((p) => p.id));
	let n = 1;
	while (ids.has(`${base}_${n}`)) n++;
	return `${base}_${n}`;
}

// armMain: a main chain that nothing watches ends the run with done; once
// there is a watcher or an outcome to wait for, it ends with end — the run
// stays open for them (FLOWLANG §1). Only the chain's last stage changes: a
// done said earlier in it is the author's.
function armMain(doc: FlowDoc) {
	if (doc.phases?.some((p) => p.kind !== 'main')) return;
	const last = stagesOf(doc, MAIN).at(-1);
	if (last?.on_success === DONE) last.on_success = END;
}

/** addWatcher adds a watcher a person starts from the run page, with one
 *  command stage — a watcher needs a chain — after the last watcher. */
export function addWatcher(doc: FlowDoc): DocPhase {
	armMain(doc);
	doc.phases ??= [];
	const ph: DocPhase = {
		id: freshPhaseId(doc, 'watcher'),
		kind: 'watcher',
		on: { signal: MANUAL }
	};
	const at = doc.phases.findIndex((p) => p.kind === 'outcome');
	doc.phases.splice(at < 0 ? doc.phases.length : at, 0, ph);
	const s = freshStage(doc, 'command');
	doc.stages.push(s);
	placeInPhase(doc, s.id, ph.id);
	return ph;
}

/** addOutcome adds an outcome that completes the run once every merge
 *  request merged — nothing to do first. */
export function addOutcome(doc: FlowDoc): DocPhase {
	armMain(doc);
	doc.phases ??= [];
	const ph: DocPhase = {
		id: freshPhaseId(doc, 'outcome'),
		kind: 'outcome',
		on: { signal: 'mr_merged' },
		then: DONE
	};
	doc.phases.push(ph);
	return ph;
}

/** removePhase drops a watcher or an outcome with its chain. Nothing routes
 *  into a chain from outside it, so nothing else changes. */
export function removePhase(doc: FlowDoc, id: string) {
	doc.phases = doc.phases?.filter((p) => p.id !== id);
	doc.stages = doc.stages.filter((s) => s.phase !== id);
}

/** Every stage list of the document: the chains' and each sequence's. */
export function allStages(doc: FlowDoc): DocStage[] {
	return [...doc.stages, ...(doc.sequences ?? []).flatMap((q) => q.stages)];
}

// freshStage builds a new stage of the given type with an id not yet in
// taken (the chains' stages by default; a sequence's are its own).
export function freshStage(doc: FlowDoc, type: StageKind, taken = doc.stages): DocStage {
	const base = {
		agent: 'agent',
		command: 'cmd',
		human_gate: 'gate',
		action: 'action',
		wait: 'wait',
		service: 'service',
		sequence: 'seq'
	}[type];
	let n = 1;
	let id = `${base}_${n}`;
	const ids = new Set(taken.map((s) => s.id));
	while (ids.has(id)) id = `${base}_${++n}`;
	const s: DocStage = { id, type, on_success: END };
	if (doc.phases?.length) s.phase = doc.phases[0].id;
	if (type === 'agent') s.agent = doc.agents?.[0]?.name ?? '';
	if (type === 'command') s.run_ref = '';
	if (type === 'human_gate') s.question = '';
	if (type === 'action') s.action = '';
	if (type === 'wait') s.for = '';
	if (type === 'service') s.service = serviceFor(doc);
	// The main chain is a sequence too — for the chains that are not it.
	if (type === 'sequence') s.sequence = doc.sequences?.[0]?.id ?? (hasMain(doc) ? MAIN : '');
	return s;
}

/** serviceFor is the service a new service stage names: the one the flow's
 *  service stages already do, else the convention — `$app`, a parameter
 *  whose default is the project's `default` service, so a project calling
 *  its app anything binds its own name. The parameter is declared here when
 *  the flow has none of that name. */
function serviceFor(doc: FlowDoc): string {
	const used = allStages(doc).find((x) => x.type === 'service' && x.service)?.service;
	if (used) return used;
	doc.parameters ??= {};
	doc.parameters.app ??= {
		type: 'string',
		default: 'default',
		description: 'the project service the run brings up'
	};
	return '$app';
}

/** Whether the flow has a main chain — which other chains may run as the
 *  sequence `main`. */
export const hasMain = (doc: FlowDoc) => !!doc.phases?.some((p) => p.kind === 'main');

/** How a chain ends when its last stage says nothing: the run is done for a
 *  main chain nothing watches, an outcome the way its then says, a watcher
 *  goes back to waiting. */
function chainEnd(doc: FlowDoc, phase: string): string {
	const ph = doc.phases?.find((p) => p.id === phase);
	if (ph?.kind === 'outcome') return ph.then ?? DONE;
	return ph?.kind === 'main' && doc.phases?.length === 1 ? DONE : END;
}

/** appendStage adds a stage at the end of a chain and wires it in: the
 *  chain's last stage goes on to it, and it ends the chain the way that
 *  stage did. `init` is set on it before it joins the document: once in, a
 *  stage is changed through the document, never through the object made
 *  here (the reactive document holds its own view of it). */
export function appendStage(
	doc: FlowDoc,
	type: StageKind,
	phase: string,
	init: Partial<DocStage> = {}
): DocStage {
	const s = Object.assign(freshStage(doc, type), init);
	s.phase = phase;
	const chain = stagesOf(doc, phase);
	const last = chain.at(-1);
	s.on_success = chainEnd(doc, phase);
	if (last && (!last.on_success || isReservedTarget(last.on_success))) {
		s.on_success = last.on_success || s.on_success;
		last.on_success = s.id;
	}
	if (last) doc.stages.splice(doc.stages.indexOf(last) + 1, 0, s);
	else {
		// An empty chain: its stages go where the section order says.
		const order = (doc.phases ?? []).map((p) => p.id);
		const at = doc.stages.findIndex((x) => order.indexOf(x.phase ?? '') > order.indexOf(phase));
		doc.stages.splice(at < 0 ? doc.stages.length : at, 0, s);
	}
	return s;
}

/** appendSequenceStage adds a stage at the end of a sequence: the last stage
 *  goes on to it, and it leaves the sequence the way that one did. */
export function appendSequenceStage(
	doc: FlowDoc,
	type: StageKind,
	id: string
): DocStage | undefined {
	const q = doc.sequences?.find((x) => x.id === id);
	if (!q) return undefined;
	const s = freshStage(doc, type, q.stages);
	s.phase = undefined;
	s.on_success = undefined;
	// A sequence never runs itself: another one, else the main chain.
	if (s.sequence === id) s.sequence = doc.sequences?.find((x) => x.id !== id)?.id ?? MAIN;
	const last = q.stages.at(-1);
	if (last?.on_success && isReservedTarget(last.on_success)) {
		s.on_success = last.on_success;
		last.on_success = undefined;
	}
	q.stages.push(s);
	return s;
}

/** moveSequenceStage moves a sequence's stage one place up (-1) or down
 *  (+1). A sequence runs from its first stage and, where a stage says no
 *  success, on to the next: the order is part of the chain. */
export function moveSequenceStage(doc: FlowDoc, id: string, stage: string, delta: -1 | 1) {
	const q = doc.sequences?.find((x) => x.id === id);
	const i = q?.stages.findIndex((s) => s.id === stage) ?? -1;
	if (!q || i < 0 || i + delta < 0 || i + delta >= q.stages.length) return;
	const [s] = q.stages.splice(i, 1);
	q.stages.splice(i + delta, 0, s);
}

/** The stage ids — in a chain or a sequence — that run the sequence id. */
export function sequenceUsers(doc: FlowDoc, id: string): string[] {
	return allStages(doc)
		.filter((s) => s.type === 'sequence' && s.sequence === id)
		.map((s) => s.id);
}

/** addSequence adds a sequence of one command stage, nothing running it yet. */
export function addSequence(doc: FlowDoc): DocSequence {
	const ids = new Set((doc.sequences ?? []).map((q) => q.id));
	let n = 1;
	while (ids.has(`sequence_${n}`) || `sequence_${n}` === MAIN) n++;
	const stage = freshStage(doc, 'command', []);
	stage.phase = undefined;
	stage.on_success = undefined;
	const q: DocSequence = { id: `sequence_${n}`, stages: [stage] };
	doc.sequences = [...(doc.sequences ?? []), q];
	return q;
}

/** renameSequence renames a sequence and every stage that runs it. */
export function renameSequence(doc: FlowDoc, from: string, to: string) {
	const q = doc.sequences?.find((x) => x.id === from);
	if (!q) return;
	q.id = to;
	for (const s of allStages(doc)) if (s.type === 'sequence' && s.sequence === from) s.sequence = to;
}

/** removeSequence drops a sequence nothing runs. */
export function removeSequence(doc: FlowDoc, id: string) {
	doc.sequences = doc.sequences?.filter((q) => q.id !== id);
	if (!doc.sequences?.length) doc.sequences = undefined;
}

/** What an agent's name — and each of its inputs' and outputs' — may be. */
export const AGENT_NAME = /^[a-z0-9][a-z0-9._-]*$/;

/** What a schema's name may be: the same slug as an agent's. */
export const SCHEMA_NAME = AGENT_NAME;

/** What names the schema: an agent's input or output, a command's binding. */
export type SchemaUser =
	{ agent: string; port: string } | { stage: string; sequence?: string; port: string };

export function schemaUsers(doc: FlowDoc, name: string): SchemaUser[] {
	const out: SchemaUser[] = [];
	for (const a of doc.agents ?? []) {
		for (const p of [...(a.inputs ?? []), ...(a.outputs ?? [])])
			if (p.schema === name) out.push({ agent: a.name, port: p.name });
	}
	const scan = (stages: DocStage[], sequence?: string) => {
		for (const s of stages)
			for (const [port, p] of [...portEntries(s.input), ...portEntries(s.output)])
				if (p.schema === name) out.push({ stage: s.id, sequence, port });
	};
	scan(doc.stages);
	for (const q of doc.sequences ?? []) scan(q.stages, q.id);
	return out;
}

/** addSchema declares an object schema with no properties yet, as schema_N. */
export function addSchema(doc: FlowDoc): DocSchema {
	const names = new Set((doc.schemas ?? []).map((q) => q.name));
	let n = 1;
	while (names.has(`schema_${n}`)) n++;
	const q: DocSchema = { name: `schema_${n}`, json_schema: { type: 'object', properties: {} } };
	doc.schemas = [...(doc.schemas ?? []), q];
	return q;
}

/** renameSchema renames a schema and every port that names it. */
export function renameSchema(doc: FlowDoc, from: string, to: string) {
	const q = doc.schemas?.find((x) => x.name === from);
	if (!q) return;
	q.name = to;
	for (const a of doc.agents ?? [])
		for (const p of [...(a.inputs ?? []), ...(a.outputs ?? [])])
			if (p.schema === from) p.schema = to;
	for (const s of allStages(doc))
		for (const [, p] of [...portEntries(s.input), ...portEntries(s.output)])
			if (p.schema === from) p.schema = to;
}

/** removeSchema drops a schema no port names. */
export function removeSchema(doc: FlowDoc, name: string) {
	doc.schemas = doc.schemas?.filter((q) => q.name !== name);
	if (!doc.schemas?.length) doc.schemas = undefined;
}

/** What an MCP server's name may be: a slug, as the agents' mcp lists name it. */
export const MCP_NAME = /^[a-z][a-z0-9_-]*$/;

/** The agents that list the MCP server name. */
export function mcpUsers(doc: FlowDoc, name: string): string[] {
	return (doc.agents ?? []).filter((a) => a.mcp?.includes(name)).map((a) => a.name);
}

/** addMCPServer declares a server: the project's browser when the flow has
 *  none yet — the one most flows want — else a command to fill in. */
export function addMCPServer(doc: FlowDoc): DocMCPServer {
	const list = doc.mcp_servers ?? [];
	const names = new Set(list.map((m) => m.name));
	let m: DocMCPServer;
	if (!list.some((x) => x.builtin === 'browser') && !names.has('browser'))
		m = { name: 'browser', builtin: 'browser' };
	else {
		let n = 1;
		while (names.has(`mcp_${n}`)) n++;
		m = { name: `mcp_${n}`, command: [] };
	}
	doc.mcp_servers = [...list, m];
	return m;
}

/** renameMCPServer renames a server and every agent's mention of it. */
export function renameMCPServer(doc: FlowDoc, from: string, to: string) {
	const m = doc.mcp_servers?.find((x) => x.name === from);
	if (!m) return;
	m.name = to;
	for (const a of doc.agents ?? []) if (a.mcp) a.mcp = a.mcp.map((x) => (x === from ? to : x));
}

/** removeMCPServer drops a server, and it from every agent that listed it. */
export function removeMCPServer(doc: FlowDoc, name: string) {
	doc.mcp_servers = doc.mcp_servers?.filter((m) => m.name !== name);
	if (!doc.mcp_servers?.length) doc.mcp_servers = undefined;
	for (const a of doc.agents ?? []) {
		if (!a.mcp?.includes(name)) continue;
		a.mcp = a.mcp.filter((x) => x !== name);
		if (!a.mcp.length) a.mcp = undefined;
	}
}

/** The stage ids — in a chain or a sequence — that run the agent name. */
export function agentUsers(doc: FlowDoc, name: string): string[] {
	return allStages(doc)
		.filter((s) => s.type === 'agent' && s.agent === name)
		.map((s) => s.id);
}

/** addAgent declares an agent with an empty prompt, for a stage to run —
 *  under the name given when it is free, else agent_N. */
export function addAgent(doc: FlowDoc, name?: string): DocAgent {
	const names = new Set((doc.agents ?? []).map((a) => a.name));
	let n = 1;
	while (names.has(`agent_${n}`)) n++;
	const free = name && AGENT_NAME.test(name) && !names.has(name);
	const a: DocAgent = { name: free ? name : `agent_${n}`, prompt: '' };
	doc.agents = [...(doc.agents ?? []), a];
	return a;
}

/** renameAgent renames an agent and every stage that runs it. */
export function renameAgent(doc: FlowDoc, from: string, to: string) {
	const a = doc.agents?.find((x) => x.name === from);
	if (!a) return;
	a.name = to;
	for (const s of allStages(doc)) if (s.type === 'agent' && s.agent === from) s.agent = to;
}

/** renameAgentPort renames one of an agent's inputs or outputs, and the
 *  binding every stage running the agent holds under that name — keeping
 *  the bindings' order. */
export function renameAgentPort(
	doc: FlowDoc,
	agent: string,
	field: 'input' | 'output',
	from: string,
	to: string
) {
	for (const s of allStages(doc)) {
		const io = s[field];
		if (s.type !== 'agent' || s.agent !== agent || !io || !(from in io)) continue;
		s[field] = Object.fromEntries(Object.entries(io).map(([k, v]) => [k === from ? to : k, v]));
	}
}

/** addMainChain gives a flow that only watches the chain a run starts with:
 *  one command stage. */
export function addMainChain(doc: FlowDoc): DocPhase {
	doc.phases ??= [];
	const ph: DocPhase = { id: MAIN, kind: 'main', on: { signal: '' } };
	doc.phases.unshift(ph);
	appendStage(doc, 'command', MAIN);
	return ph;
}

/** setChainSequence makes a watcher's or an outcome's chain one stage that
 *  runs the sequence id — written `sequence: id` — or, with no id, stages of
 *  its own: one command stage for a watcher (it needs a chain), none for an
 *  outcome (its then is enough). Its stages so far are dropped. */
export function setChainSequence(doc: FlowDoc, phase: string, id: string | undefined) {
	const ph = doc.phases?.find((p) => p.id === phase);
	if (!ph) return;
	doc.stages = doc.stages.filter((s) => s.phase !== phase);
	if (!id) {
		if (ph.kind === 'watcher') appendStage(doc, 'command', phase);
		return;
	}
	// Named after its chain, as the parser names `sequence: id` — unless a
	// stage elsewhere has that id; then it is written out as stages.
	const named = !doc.stages.some((x) => x.id === phase);
	appendStage(doc, 'sequence', phase, { sequence: id, ...(named ? { id: phase } : {}) });
}

/** The sequence a watcher's or an outcome's chain is written as
 *  `sequence: X` for, or undefined when it has stages of its own. */
export function chainSequence(doc: FlowDoc, ph: DocPhase): string | undefined {
	return sequenceOf(ph, stagesOf(doc, ph.id));
}

// --- dataflow (FLOWLANG §4) -------------------------------------------------
//
// Every stage declares what it consumes and produces, so the wiring between
// stages is derivable rather than drawn: two stages are connected when one's
// output path is another's input path. These helpers are the single source for
// the graph's io dots and its derived data edges.

/** One io port on a stage: a dot in the graph, a row in the inspector. */
export interface IOPort {
	/** The port's name. */
	name: string;
	/** Its source (an input) or destination (an output), "" when unstated. */
	value: string;
	/** True when value is a value the engine provides (run.*, edge.*). */
	engine: boolean;
	/** What it is: its type, its schema. */
	type?: string;
	schema?: string;
	/** Declared description, when the contract carries one. */
	description?: string;
	/** True for a declared port the stage does not state yet. */
	unbound?: boolean;
}

/** The agent/action contract shapes the graph needs; a subset of api.ts types. */
export interface IODecl {
	name: string;
	description?: string;
	optional?: boolean;
	type?: string;
	schema?: string;
	required?: boolean;
	defaultPath?: string;
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

const port = (name: string, p: DocPort | undefined, decl?: IODecl): IOPort => ({
	name,
	value: portValue(p),
	engine: isEngineSource(portValue(p)),
	type: p?.type ?? decl?.type,
	schema: p?.schema ?? decl?.schema,
	description: p?.description ?? decl?.description,
	unbound: p == null || undefined
});

/**
 * stageInputs lists a stage's input ports: a contract's (an agent's, an
 * action's) in declaration order — including any it does not state yet, so
 * the contract is visible before it is wired — then every other port it
 * states; a command's or a gate's as written.
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
		case 'command':
		case 'human_gate':
		case 'wait':
			return portEntries(s.input).map(([name, p]) => port(name, p));
		// A sequence stage states nothing itself: its sequence's stages do. A
		// service stage reads and writes nothing: its address is a source.
		case 'sequence':
		case 'service':
			return [];
	}
}

/**
 * declaredPorts lists a contract's ports in declaration order, followed by
 * any port the contract does not declare — the validator refuses those, but
 * hiding them would make the graph lie about the YAML.
 */
function declaredPorts(ports: StagePorts | undefined, decls: IODecl[]): IOPort[] {
	const out = decls.map((d) => port(d.name, ports?.[d.name], d));
	for (const [name, p] of portEntries(ports)) {
		if (!decls.some((d) => d.name === name)) out.push(port(name, p));
	}
	return out;
}

/** stageOutputs lists what a stage writes, the same way. */
export function stageOutputs(
	s: DocStage,
	agents: AgentContract[] = [],
	actions: ActionContract[] = []
): IOPort[] {
	switch (s.type) {
		case 'agent':
			return declaredPorts(s.output, agents.find((a) => a.name === s.agent)?.outputs ?? []);
		case 'action':
			return declaredPorts(s.output, actions.find((a) => a.slug === s.action)?.outputs ?? []);
		case 'command':
			return portEntries(s.output).map(([name, p]) => port(name, p));
		case 'human_gate':
		case 'wait':
		case 'service':
		case 'sequence':
			return [];
	}
}

/** givesVerdict reports whether a stage's success is a verdict (mirror of
 *  Go's flow.GivesVerdict): a gate, or an agent writing a verdict. Its on_success target may read
 *  edge.verdict too — an approval carries what was said with it. */
export function givesVerdict(s: DocStage, agents: AgentContract[] = []): boolean {
	if (s.type === 'human_gate') return true;
	if (s.type !== 'agent') return false;
	return (
		Object.values(s.output ?? {}).some((p) => p.type === 'verdict') ||
		!!verdictOutput(agents.find((a) => a.name === s.agent))
	);
}

/** verdictOutput returns an agent's verdict-typed output decl, if it has one. */
export function verdictOutput(agent?: AgentContract): IODecl | undefined {
	return agent?.outputs?.find((o) => o.type === 'verdict');
}

/** One derived writer→reader connection. Read-only: paths are the wiring. */
export interface DataLink {
	source: string;
	sourceHandle: string;
	target: string;
	targetHandle: string;
	value: string;
}

/**
 * deriveDataLinks connects every input port to the stage that writes its
 * path. Derived, never stored: the ports' paths are the wiring, and this
 * only draws what they already say. A value the engine provides has no
 * writer to draw from.
 */
export function deriveDataLinks(
	doc: FlowDoc,
	agents: AgentContract[] = [],
	actions: ActionContract[] = []
): DataLink[] {
	// path -> writing (stage, port). Later writers win, which keeps the link
	// pointing at the most recent writer in document order.
	const writers = new Map<string, { stage: string; name: string }>();
	for (const s of doc.stages) {
		for (const o of stageOutputs(s, agents, actions)) {
			if (o.value) writers.set(o.value, { stage: s.id, name: o.name });
		}
	}
	const links: DataLink[] = [];
	for (const s of doc.stages) {
		for (const i of stageInputs(s, agents, actions)) {
			if (!i.value || i.unbound || i.engine) continue;
			const w = writers.get(i.value);
			if (!w || w.stage === s.id) continue;
			links.push({
				source: w.stage,
				sourceHandle: `out:${w.name}`,
				target: s.id,
				targetHandle: `in:${i.name}`,
				value: i.value
			});
		}
	}
	return links;
}

/**
 * feedDots maps "<stage id>|<edge source>" to the input reading it, so the
 * graph can land a control edge on the exact dot that receives its content:
 * an on_reject edge — or a deciding stage's success — ends at whatever the
 * target called its edge.verdict input.
 */
export function feedDots(doc: FlowDoc): Map<string, string> {
	const out = new Map<string, string>();
	for (const s of doc.stages) {
		for (const [name, p] of portEntries(s.input)) {
			if (p.from && isEdgeSource(p.from)) out.set(`${s.id}|${p.from}`, name);
		}
	}
	return out;
}
