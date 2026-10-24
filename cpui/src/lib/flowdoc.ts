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
export type StageKind = 'agent' | 'command' | 'human_gate' | 'action' | 'wait' | 'sequence';

// CURRENT_VERSION is the grammar this build writes; every document declares
// it, so a future break is a refusal rather than a mis-parse (FLOWLANG §1).
export const CURRENT_VERSION = 5;

// An edge is a chain: when a link's max is exhausted the next failure
// falls through to `then` (retry-then-escalate). A link with to: abort
// stops the run, one with to: debug parks it for a human; both carry no
// max/then.
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
	if (type === 'action' || type === 'sequence') out.push('on_fail');
	return out;
}

/** Which sentinel an edge kind's carried content fills (mirror of Go). */
export function feedSentinelFor(kind: EdgeKind): string {
	return kind === 'on_reject' ? SENTINEL_DECISION : SENTINEL_FAILURE;
}

// Sentinels are the engine-provided input values (FLOWLANG §4). The rule is
// lexical: a $NAME input is resolved by the engine, anything else is a
// repo-relative artifact path.
// The git ones are named after the git they run, one range each wherever
// they are bound: what the branch changes against its target, what this
// phase run committed since it began, and what a commit would record now
// (staged, unstaged, untracked — which the diffs, comparing commits, miss).
export const SENTINEL_DIFF_TARGET = '$DIFF_TARGET';
export const SENTINEL_DIFF_PHASE = '$DIFF_PHASE';
export const SENTINEL_STATUS = '$STATUS';
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
	if (type === 'agent')
		return [
			SENTINEL_DIFF_TARGET,
			SENTINEL_DIFF_PHASE,
			SENTINEL_STATUS,
			SENTINEL_DECISION,
			SENTINEL_FAILURE
		];
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
	/** sequence: the sequence this stage runs in its place. */
	sequence?: string;
	/** Optional human note; documentation only, reaches no prompt. */
	description?: string;
	agent?: string;
	model?: string;
	harness?: string;
	max_turns?: number | string;
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
	/** A condition (FLOWLANG §2): `tracker`, `subject`, `$bool_param`, each
	 *  optionally negated as `not x`. A stage whose condition does not hold
	 *  completes as skipped. */
	when?: string;
	/** action: a slug from the compiled-in registry (GET /api/actions). */
	action?: string;
	/** wait: a signal slug to block on (GET /api/signals). */
	for?: string;
	on_success?: string;
	on_fail?: DocEdge;
	on_reject?: DocEdge;
	on_timeout?: DocEdge;
	/** Editor-only: the chain this stage belongs to — `main`, or a watcher's
	 *  or an outcome's id. The YAML writes stages under their section; the
	 *  editor keeps one flat list. */
	phase?: string;
}

/** A phase's `on:` — the wait point that opens it (FLOWLANG §1): run_start
 *  for the main chain (a person or an automation starts the run), manual for
 *  a watcher a person starts from the run page, or an observed signal with
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
// RUN_START is how the main chain opens: a person or an automation starts a
// run. MANUAL opens a watcher from the run page only. Every other signal is
// observed on the run's merge requests, and takes a start policy.
export const RUN_START = 'run_start';
export const MANUAL = 'manual';
export const START_POLICIES = ['auto', 'confirm', 'manual'] as const;
export type StartPolicy = (typeof START_POLICIES)[number];
/** What a flow's runs are started on (`run_start:`): a ticket, the default,
 *  or a merge request someone opened. */
export const RUN_STARTS = ['ticket', 'merge_request'] as const;
export type RunStart = (typeof RUN_STARTS)[number];

/** One parameter of the flow's `parameters:` block (FLOWLANG §1): its
 *  default — whose JS type is the parameter's type: bool, int/float, string —
 *  and optionally a description and an enum. Values come from outside the
 *  document (the workshop's bindings, a run's overrides). */
export interface DocParam {
	default: boolean | number | string;
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
	run_start?: RunStart;
	/** The flow's one settings block, in document order. */
	parameters: Record<string, DocParam>;
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
		phases.push({ id: MAIN, kind: 'main', on: { signal: RUN_START } });
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
 *  run_start and manual are a person's act. */
export function fires(w: DocWait): boolean {
	return w.signal !== RUN_START && w.signal !== MANUAL;
}

/** A parameter's default as text, or undefined when the document omits it
 *  (a known one then runs at the engine's default). */
export function paramDefault(doc: FlowDoc, name: string): string | undefined {
	const p = doc.parameters?.[name];
	return p == null ? undefined : String(p.default);
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
	if (doc.run_start && doc.run_start !== 'ticket') obj.run_start = doc.run_start;
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
		if (!p.description && !p.enum?.length) params[name] = p.default;
		else {
			const o: Record<string, unknown> = { default: p.default };
			if (p.description) o.description = p.description;
			if (p.enum?.length) o.enum = p.enum;
			params[name] = o;
		}
	}
	obj.parameters = params;
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
		if (s.when) o.when = s.when;
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

// nonEmptyIO reports whether a binding has anything to serialize.
function nonEmptyIO(v?: StageIO): boolean {
	return v != null && Object.keys(v).length > 0;
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
	// The main chain is a sequence too — for the chains that are not it.
	if (type === 'sequence') s.sequence = doc.sequences?.[0]?.id ?? (hasMain(doc) ? MAIN : '');
	return s;
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
	const ph: DocPhase = { id: MAIN, kind: 'main', on: { signal: RUN_START } };
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
	/** Binding name, or the path itself for a command's unnamed entries. */
	name: string;
	/** The bound artifact path, or a sentinel. */
	value: string;
	/** True when value is engine-provided ($DIFF_TARGET, $STATUS, $MR_URL, …). */
	sentinel: boolean;
	/** Declared description, when the contract carries one. */
	description?: string;
	/** True for a declared-but-unbound input (nothing to connect yet). */
	unbound?: boolean;
	/** True for a literal the stage writes (an enum choice, a text input):
	 *  the author's word, not a path — shown, never connectable. */
	literal?: boolean;
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
	unbound: unbound || undefined,
	literal: decl?.kind === 'enum' || decl?.kind === 'text' || undefined
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
		// A sequence stage binds nothing itself: its sequence's stages do.
		case 'sequence':
			return [];
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
		case 'sequence':
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
