<script lang="ts">
	import type { Selection } from '$lib/FlowGraph.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import StageChip from '$lib/ui/StageChip.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import { portLabel } from '$lib/format';
	import { EDGE_HUE, STAGE_HUE, STAGE_WORD } from '$lib/ui/hues';
	import { I } from '$lib/ui/icons';
	import { ms } from '$lib/ui/motion';
	import { fade } from 'svelte/transition';
	import { untrack } from 'svelte';
	import BindingRows from '$lib/flow/BindingRows.svelte';
	import OptionalDuration from '$lib/ui/OptionalDuration.svelte';
	import OptionalNumber from '$lib/ui/OptionalNumber.svelte';
	import ParamsEditor from '$lib/flow/ParamsEditor.svelte';
	import {
		boolParams,
		enumParamsWithin,
		intParams,
		isParamRef,
		policyParams,
		stringParams
	} from '$lib/flow/params';
	import type { ActionSpec, SignalSpec } from '$lib/domain';
	import {
		ABORT,
		DEBUG,
		FAIL,
		DONE,
		END,
		isReservedTarget,
		START,
		MAIN,
		MANUAL,
		OPERATES_ON,
		START_POLICIES,
		AGENT_NAME,
		addAgent,
		addMainChain,
		addOutcome,
		addSequence,
		addWatcher,
		agentUsers,
		appendSequenceStage,
		appendStage,
		chainSequence,
		hasMain,
		moveSequenceStage,
		removePhase,
		removeSequence,
		renameAgent,
		renameAgentPort,
		renameSequence,
		sequenceUsers,
		setChainSequence,
		stagesOf,
		fires,
		paramDefault,
		routableFrom,
		EDGE_KINDS,
		SENTINEL_DECISION,
		SENTINEL_FAILURE,
		chainLinks,
		edgeKindsFor,
		edgeOf,
		feedSentinelFor,
		ioEntries,
		isFeedSentinel,
		pruneEdgeTarget,
		sentinelsFor,
		setEdge,
		movePhase,
		triggerLabel,
		verdictOutput,
		type DocEdge,
		type DocPhase,
		type DocStage,
		type EdgeKind,
		type DocAgent,
		type DocSequence,
		type FlowDoc,
		type StageKind
	} from '$lib/flowdoc';

	// Flows are portable, project-independent data: this editor takes NOTHING
	// from any project. Agent / harness / model / run_ref are free text and are
	// validated against the project only at run creation (preflight). `agents`
	// is an OPTIONAL hint list (built-ins ∪ a project's authored agents) used
	// only to render binding fields from declared I/O — never a constraint.
	//
	// Moving between panels comes in two kinds: `onselect` puts another
	// selection in the current one's place (a rename, a delete); `onopen`
	// goes from this panel into another — a stage's agent, a sequence's
	// stage — and the page remembers where from: `back` is that selection
	// (null: the flow panel), offered at the top of every panel.
	let {
		doc,
		selection,
		onchange,
		onselect,
		onopen = (sel) => onselect(sel),
		back,
		onback,
		agents = [],
		actions = [],
		signals = []
	}: {
		doc: FlowDoc;
		selection: Selection;
		onchange: () => void;
		onselect: (sel: Selection, how?: 'rename') => void;
		onopen?: (sel: Selection) => void;
		back?: Selection;
		onback?: () => void;
		agents?: DocAgent[];
		actions?: ActionSpec[];
		signals?: SignalSpec[];
	} = $props();

	// Where a delete leaves the inspector: back where it came from, or the
	// panel given.
	const leave = (fallback: Selection) =>
		back !== undefined && onback ? onback() : onselect(fallback);

	// A stage or an edge inside a sequence is edited in its sequence: its id,
	// its routes and its neighbours are the sequence's own.
	const scopeSeq = $derived(
		(selection?.kind === 'stage' || selection?.kind === 'edge') && selection.sequence
			? doc.sequences?.find((q) => q.id === selection.sequence)
			: undefined
	);
	const scopeStages = $derived(scopeSeq ? scopeSeq.stages : doc.stages);
	// What a selection inside the open sequence carries along.
	const inSeq = $derived(scopeSeq ? { sequence: scopeSeq.id } : {});
	// The stages a stage may route to: its sequence's, or its chain's.
	const routable = (s: DocStage) => (scopeSeq ? scopeSeq.stages : routableFrom(doc, s));

	// An edge is edited in its stage's routes: selecting one shows the stage
	// with that route picked out.
	const stage = $derived(
		selection?.kind === 'stage'
			? scopeStages.find((s) => s.id === selection.id)
			: selection?.kind === 'edge'
				? scopeStages.find((s) => s.id === selection.stage)
				: undefined
	);
	const focusRoute = $derived(selection?.kind === 'edge' ? selection.edge : undefined);
	let routesEl = $state<HTMLElement | null>(null);
	$effect(() => {
		const kind = focusRoute;
		if (!kind || !routesEl) return;
		untrack(() =>
			routesEl
				?.querySelector(`[data-route="${kind}"]`)
				?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
		);
	});

	// The declared I/O of the selected stage's agent, when known. feedback is
	// excluded from bindable inputs — it is filled by edge feed:, not a stage.
	const agentDecl = $derived(
		stage?.type === 'agent' ? agents.find((a) => a.name === stage.agent) : undefined
	);
	const declInputs = $derived(agentDecl?.inputs ?? []);
	const declOutputs = $derived(agentDecl?.outputs ?? []);
	const declVerdict = $derived(verdictOutput(agentDecl));

	// The selected stage's action signature, when it names a registered one.
	const actionSig = $derived(
		stage?.type === 'action' ? actions.find((a) => a.slug === stage.action) : undefined
	);
	const waitSig = $derived(
		stage?.type === 'wait' ? signals.find((g) => g.slug === stage.for) : undefined
	);
	// Any signal observed on the run's merge requests may end a wait, answer
	// a gate, open a watcher or fire an outcome: the section decides what it
	// does (FLOWLANG §1). manual — a person's start — opens a watcher only.
	const observed = $derived(signals.filter((g) => g.kind === 'observed'));
	// A chain's kind in the marker's hue: main brand, watchers violet, an
	// outcome the end it says.
	const kindHue = (ph: DocPhase) =>
		ph.kind === 'main'
			? 'brand'
			: ph.kind === 'watcher'
				? 'vio'
				: ph.then === ABORT
					? 'ros'
					: 'grn';
	const WHEN_TERMS = ['tracker', 'not tracker', 'subject', 'not subject'];

	const phaseSel = $derived(
		selection?.kind === 'phase' ? doc.phases?.find((p) => p.id === selection.id) : undefined
	);
	const phaseSig = $derived(
		phaseSel ? signals.find((g) => g.slug === phaseSel.on.signal) : undefined
	);
	const phaseStageCount = $derived(
		phaseSel ? doc.stages.filter((x) => x.phase === phaseSel.id).length : 0
	);

	// renamePhase renames a watcher or an outcome and every stage that
	// belongs to it. The main chain is `main`. Like every rename here, it
	// says whether it took: a refused one puts the field back.
	function renamePhase(oldId: string, newId: string): boolean {
		newId = newId.trim();
		const ph = doc.phases?.find((p) => p.id === oldId);
		if (!ph || ph.kind === 'main' || !newId || newId === oldId || newId === MAIN) return false;
		if (!/^[a-z][a-z0-9_-]*$/.test(newId) || doc.phases?.some((p) => p.id === newId)) return false;
		ph.id = newId;
		for (const s of doc.stages) if (s.phase === oldId) s.phase = newId;
		onchange();
		onselect({ kind: 'phase', id: newId }, 'rename');
		return true;
	}
	// The field of a rename the document refused shows the name in force.
	const renamed = (el: HTMLInputElement, current: string, took: boolean) => {
		if (!took) el.value = current;
	};

	function setOperatesOn(v: string) {
		doc.operates_on = v === 'merge_request' ? 'merge_request' : undefined;
		onchange();
	}

	// An outcome's then is its chain's end: the last stage's success goes
	// there unless it says otherwise.
	function setThen(ph: DocPhase, then: string) {
		const chain = doc.stages.filter((x) => x.phase === ph.id);
		const last = chain[chain.length - 1];
		if (last && last.on_success === (ph.then ?? DONE)) last.on_success = then;
		ph.then = then;
		onchange();
	}

	function addChain(kind: 'watcher' | 'outcome') {
		const ph = kind === 'watcher' ? addWatcher(doc) : addOutcome(doc);
		onchange();
		onopen({ kind: 'phase', id: ph.id });
	}

	async function deletePhase(ph: DocPhase) {
		const count = doc.stages.filter((x) => x.phase === ph.id).length;
		const ok = await confirmDialog({
			title: t(
				ph.kind === 'outcome' ? 'flow.insp.deleteOutcomeTitle' : 'flow.insp.deleteWatcherTitle',
				{
					id: ph.id
				}
			),
			body: [
				count
					? th('flow.insp.deleteChainBody', { id: ph.id, count })
					: th('flow.insp.deleteChainEmpty', { id: ph.id }),
				t('flow.insp.deleteNote')
			].join(' '),
			action: t(ph.kind === 'outcome' ? 'flow.insp.deleteOutcome' : 'flow.insp.deleteWatcher')
		});
		if (!ok) return;
		removePhase(doc, ph.id);
		onchange();
		leave(null);
	}

	// What a watcher or an outcome runs: stages of its own, or a sequence in
	// their place (`sequence: X`) — one chain written once, run from several
	// places.
	const phaseRuns = $derived(
		phaseSel && phaseSel.kind !== 'main' ? (chainSequence(doc, phaseSel) ?? '') : ''
	);
	async function setRuns(ph: DocPhase, id: string) {
		const own = stagesOf(doc, ph.id);
		if (own.length && !chainSequence(doc, ph)) {
			const ok = await confirmDialog({
				title: t('flow.insp.runsReplaceTitle', { id: ph.id }),
				body: [
					th('flow.insp.runsReplaceBody', { id: ph.id, count: own.length }),
					t('flow.insp.deleteNote')
				].join(' '),
				action: t('flow.insp.runsReplace')
			});
			if (!ok) return;
		}
		setChainSequence(doc, ph.id, id || undefined);
		onchange();
	}

	function newMainChain() {
		addMainChain(doc);
		onchange();
		onopen({ kind: 'phase', id: MAIN });
	}

	// ---- agents: the flow carries their definitions (FLOWLANG §1) ----------
	const agentSel = $derived(
		selection?.kind === 'agent' ? doc.agents?.find((a) => a.name === selection.name) : undefined
	);
	function newAgent(name?: string) {
		const a = addAgent(doc, name);
		onchange();
		onopen({ kind: 'agent', name: a.name });
	}
	function renameAgentTo(from: string, to: string): boolean {
		to = to.trim();
		if (!to || to === from || !AGENT_NAME.test(to) || doc.agents?.some((a) => a.name === to))
			return false;
		renameAgent(doc, from, to);
		onchange();
		onselect({ kind: 'agent', name: to }, 'rename');
		return true;
	}
	async function deleteAgent(name: string) {
		const used = agentUsers(doc, name);
		const ok = await confirmDialog({
			title: t('flow.insp.deleteAgentTitle', { name }),
			body: [
				used.length
					? th('flow.insp.deleteAgentUsed', { name, count: used.length, stages: used.join(', ') })
					: th('flow.insp.deleteAgentBody', { name }),
				t('flow.insp.deleteNote')
			].join(' '),
			action: t('flow.insp.deleteAgent')
		});
		if (!ok) return;
		doc.agents = doc.agents?.filter((a) => a.name !== name);
		if (!doc.agents?.length) doc.agents = undefined;
		onchange();
		leave(null);
	}
	// A port is one declared input or output: a slug unique on its agent.
	const portName = (taken: { name: string }[], base: string) => {
		let n = 1;
		while (taken.some((p) => p.name === `${base}_${n}`)) n++;
		return `${base}_${n}`;
	};
	function addInput(a: DocAgent) {
		a.inputs = [...(a.inputs ?? []), { name: portName(a.inputs ?? [], 'input') }];
		onchange();
	}
	function addOutput(a: DocAgent) {
		const name = portName(a.outputs ?? [], 'output');
		a.outputs = [...(a.outputs ?? []), { name, path: `artifacts/${name}.md` }];
		onchange();
	}
	function renamePort(a: DocAgent, field: 'input' | 'output', from: string, to: string): boolean {
		to = to.trim();
		const list = (field === 'input' ? a.inputs : a.outputs) ?? [];
		const port = list.find((p) => p.name === from);
		if (!port || !to || to === from || !AGENT_NAME.test(to) || list.some((p) => p.name === to))
			return false;
		port.name = to;
		renameAgentPort(doc, a.name, field, from, to);
		onchange();
		return true;
	}
	function removeInput(a: DocAgent, name: string) {
		a.inputs = a.inputs?.filter((p) => p.name !== name);
		if (!a.inputs?.length) a.inputs = undefined;
		onchange();
	}
	function removeOutput(a: DocAgent, name: string) {
		a.outputs = a.outputs?.filter((p) => p.name !== name);
		if (!a.outputs?.length) a.outputs = undefined;
		onchange();
	}
	// What an output's type makes of the file (agentspec): an opaque file,
	// the verdict on_reject routes on, a review for post_review, or a review
	// round's replies.
	const OUTPUT_TYPES = ['', 'verdict', 'review_threads', 'review_replies'] as const;

	// ---- sequences: chains written once, run where a stage names them -----
	const seqSel = $derived(
		selection?.kind === 'sequence' ? doc.sequences?.find((q) => q.id === selection.id) : undefined
	);
	function newSequence() {
		const q = addSequence(doc);
		onchange();
		onopen({ kind: 'sequence', id: q.id });
	}
	function renameSequenceTo(from: string, to: string): boolean {
		to = to.trim();
		if (
			!to ||
			to === from ||
			to === MAIN ||
			!/^[a-z][a-z0-9_-]*$/.test(to) ||
			doc.sequences?.some((q) => q.id === to)
		)
			return false;
		renameSequence(doc, from, to);
		onchange();
		onselect({ kind: 'sequence', id: to }, 'rename');
		return true;
	}
	async function deleteSequence(q: DocSequence) {
		if (sequenceUsers(doc, q.id).length) return;
		const ok = await confirmDialog({
			title: t('flow.insp.deleteSequenceTitle', { id: q.id }),
			body: [
				th('flow.insp.deleteSequenceBody', { id: q.id, count: q.stages.length }),
				t('flow.insp.deleteNote')
			].join(' '),
			action: t('flow.insp.deleteSequence')
		});
		if (!ok) return;
		removeSequence(doc, q.id);
		onchange();
		leave(null);
	}
	// What a sequence stage may run: the flow's sequences, and the main chain
	// for the chains that are not it — never the sequence it is written in.
	const runnable = (s: DocStage) => [
		...(hasMain(doc) && s.phase !== MAIN ? [MAIN] : []),
		...(doc.sequences ?? []).map((q) => q.id).filter((id) => id !== scopeSeq?.id)
	];
	// The stages — in the chains and in sequences — that match, each as the
	// selection that opens it.
	const runners = (match: (s: DocStage) => boolean): { label: string; sel: Selection }[] => [
		...doc.stages
			.filter(match)
			.map((s) => ({ label: s.id, sel: { kind: 'stage', id: s.id } as const })),
		...(doc.sequences ?? []).flatMap((q) =>
			q.stages.filter(match).map((s) => ({
				label: `${q.id} › ${s.id}`,
				sel: { kind: 'stage', id: s.id, sequence: q.id } as const
			}))
		)
	];

	// Bindings are one form everywhere (FLOWLANG §4), so there is one editor:
	// read a name's value, write it back preserving order.
	function bindingValue(s: DocStage, field: 'input' | 'output', name: string): string {
		return s[field]?.[name] ?? '';
	}
	function setBinding(s: DocStage, field: 'input' | 'output', name: string, value: string) {
		const cur = { ...(s[field] ?? {}) };
		if (value) cur[name] = value;
		else delete cur[name];
		s[field] = Object.keys(cur).length ? cur : undefined;
		onchange();
	}
	// Reorder a contract's bindings into declaration order, so the YAML reads
	// like the contract and a round-trip never churns the flow hash.
	function orderBindings(s: DocStage, field: 'input' | 'output', names: string[]) {
		const cur = s[field];
		if (!cur) return;
		const rec: Record<string, string> = {};
		for (const n of names) if (cur[n]) rec[n] = cur[n];
		for (const [k, v] of Object.entries(cur)) if (!(k in rec)) rec[k] = v;
		s[field] = rec;
	}
	const freeBindings = $derived.by(() => {
		if (!stage) return [] as [string, string][];
		const declared = new Set(
			stage.type === 'agent'
				? declInputs.map((d) => d.name)
				: stage.type === 'action'
					? (actionSig?.inputs ?? []).map((d) => d.name)
					: []
		);
		return ioEntries(stage.input).filter(([k]) => !declared.has(k));
	});
	const isDeclared = (k: string) =>
		stage?.type === 'agent'
			? declInputs.some((d) => d.name === k)
			: (actionSig?.inputs ?? []).some((d) => d.name === k);
	// Once a stage shows its unrecognized bindings, the section stays while
	// the stage is selected — clearing the last row mid-edit must not unmount
	// the editor under the cursor.
	let freeShown = $state<Record<string, boolean>>({});
	$effect(() => {
		if (stage && freeBindings.length && !freeShown[stage.id]) freeShown[stage.id] = true;
	});

	// The inspector cross-fades between selections instead of jumping: the
	// outgoing and incoming views share one grid cell for the moment.
	const viewKey = $derived(
		selection?.kind === 'stage'
			? `s:${selection.sequence ?? ''}:${selection.id}`
			: selection?.kind === 'edge'
				? `s:${selection.sequence ?? ''}:${selection.stage}`
				: selection?.kind === 'phase'
					? `p:${selection.id}`
					: selection?.kind === 'agent'
						? `a:${selection.name}`
						: selection?.kind === 'sequence'
							? `q:${selection.id}`
							: 'flow'
	);
	const xf = () => ({ duration: ms('--dur-fade', 160) });
	// The stages a chain or a sequence may add: a sequence stage once there
	// is something for it to run — another sequence, or the main chain again
	// (from anywhere but the main chain itself).
	const addKinds = (where: { phase?: string; sequence?: string }): StageKind[] => {
		const runs =
			(doc.sequences ?? []).some((q) => q.id !== where.sequence) ||
			(hasMain(doc) && where.phase !== MAIN);
		return [
			'agent',
			'command',
			'human_gate',
			'action',
			'wait',
			...(runs ? ['sequence' as const] : [])
		];
	};
	const mainPhase = $derived(doc.phases?.find((p) => p.kind === 'main'));
	const sequenceSel = $derived(
		stage?.type === 'sequence' ? doc.sequences?.find((q) => q.id === stage.sequence) : undefined
	);

	// A stage is added at the end of a chain or a sequence, wired in after
	// its last stage; the flow panel adds to the main chain (or the first
	// chain there is).
	function addToChain(type: StageKind, phase: string) {
		const s = appendStage(doc, type, phase);
		onchange();
		onopen({ kind: 'stage', id: s.id });
	}
	function addToSequence(type: StageKind, id: string) {
		const s = appendSequenceStage(doc, type, id);
		if (!s) return;
		onchange();
		onopen({ kind: 'stage', id: s.id, sequence: id });
	}
	function addStage(type: StageKind) {
		const phase = mainPhase?.id ?? doc.phases?.[0]?.id;
		if (phase) addToChain(type, phase);
	}

	function renameStage(oldId: string, newId: string): boolean {
		newId = newId.trim();
		if (
			!newId ||
			newId === oldId ||
			[END, DONE, ABORT, DEBUG, START, FAIL].includes(newId) ||
			!/^[a-z][a-z0-9_-]*$/.test(newId)
		)
			return false;
		// Ids are unique in the chains, and in each sequence on its own.
		if (scopeStages.some((s) => s.id === newId)) return false;
		for (const s of scopeStages) {
			if (s.id === oldId) s.id = newId;
			if (s.on_success === oldId) s.on_success = newId;
			for (const kind of EDGE_KINDS) {
				for (const link of chainLinks(edgeOf(s, kind))) {
					if (link.to === oldId) link.to = newId;
				}
			}
		}
		onchange();
		onselect({ kind: 'stage', id: newId, ...inSeq }, 'rename');
		return true;
	}

	async function deleteStage(id: string) {
		const seq = scopeSeq;
		const refs = scopeStages.filter(
			(s) =>
				s.id !== id &&
				(s.on_success === id ||
					EDGE_KINDS.some((k) => chainLinks(edgeOf(s, k)).some((l) => l.to === id)))
		).length;
		const ok = await confirmDialog({
			title: t('flow.insp.deleteStageTitle', { id }),
			body: [
				th('flow.insp.deleteBody', { id }),
				refs ? th('flow.insp.deleteRewired', { count: refs }) : '',
				t('flow.insp.deleteNote')
			]
				.filter(Boolean)
				.join(' '),
			action: t('flow.insp.deleteAction')
		});
		if (!ok) return;
		const rest = (seq ? seq.stages : doc.stages).filter((s) => s.id !== id);
		for (const s of rest) {
			// A success that led there ends the chain — in a sequence it goes
			// on to the next stage, or out.
			if (s.on_success === id) s.on_success = seq ? undefined : END;
			for (const kind of EDGE_KINDS) {
				setEdge(s, kind, pruneEdgeTarget(edgeOf(s, kind), id));
			}
		}
		if (seq) seq.stages = rest;
		else doc.stages = rest;
		onchange();
		leave(seq ? { kind: 'sequence', id: seq.id } : null);
	}

	// A new route starts somewhere legal: a stage of the same chain (a route
	// may not leave it). A failure or timeout retries the stage itself; a
	// gate's rejection goes back to whatever leads into the gate.
	function addEdge(s: NonNullable<typeof stage>, kind: EdgeKind) {
		const mine = routable(s);
		const feeder = mine.find((x) => x.on_success === s.id && x.id !== s.id);
		const to = s.type === 'human_gate' ? (feeder ?? mine.find((x) => x.id !== s.id))?.id : s.id;
		setEdge(s, kind, to ? { to, max: 3 } : { to: DEBUG });
		onchange();
	}

	const targetsGate = (id: string) => scopeStages.find((x) => x.id === id)?.type === 'human_gate';
	// Unlimited (-1) needs a human on the link: gate target (each firing
	// parks on a decision) or gate source (the link only fires on reject).
	const unlimitedOK = (sourceType: StageKind, to: string) =>
		sourceType === 'human_gate' || targetsGate(to);

	// retarget switches one chain link's target, normalizing the link's other
	// fields: links to a reserved target (end, done, abort, debug) carry no
	// max/then; stage links need a max, and -1 survives only while a gate
	// remains on the link.
	function retarget(sourceType: StageKind, link: DocEdge, to: string) {
		link.to = to;
		if (isReservedTarget(to)) {
			link.max = undefined;
			link.then = undefined;
		} else if (link.max == null || (link.max === -1 && !unlimitedOK(sourceType, to))) {
			link.max = 3;
		}
		onchange();
	}

	/** Delete what is selected (⌫ / Del on the canvas): a stage, a chain, an
	 *  agent or a sequence asks first; an edge goes at once (re-adding it is
	 *  one click). */
	export function deleteSelection() {
		if (selection?.kind === 'stage') {
			if (scopeSeq?.stages.length !== 1) void deleteStage(selection.id);
		} else if (selection?.kind === 'edge') {
			const s = scopeStages.find((x) => x.id === selection.stage);
			if (!s) return;
			setEdge(s, selection.edge as EdgeKind, undefined);
			onchange();
			onselect({ kind: 'stage', id: s.id, ...inSeq });
		} else if (phaseSel && phaseSel.kind !== 'main') void deletePhase(phaseSel);
		else if (agentSel) void deleteAgent(agentSel.name);
		else if (seqSel) void deleteSequence(seqSel);
	}

	// What an action output produces: its name, then whichever of sentinel,
	// path and description it declares.
	const producesHtml = (d: {
		name: string;
		sentinel?: string;
		path?: string;
		description?: string;
	}) =>
		th('flow.insp.produces', { name: d.name }) +
		(d.sentinel ? th('flow.insp.producesSentinel', { sentinel: d.sentinel }) : '') +
		(d.path ? th('flow.insp.producesPath', { path: d.path }) : '') +
		(d.description ? th('flow.insp.producesDesc', { description: d.description }) : '') +
		'.';

	// appendLink adds a fallback ("then") after the chain's last link.
	function appendLink(e: DocEdge) {
		let last = e;
		while (last.then) last = last.then;
		if (isReservedTarget(last.to) || last.max === -1) return; // nothing follows a reserved target or an unlimited link
		// The usual fallback once retries are spent: a human with the workspace.
		last.then = { to: DEBUG };
		onchange();
	}

	// removeLink takes one link out of a route, the links after it moving up
	// (the route goes with its only link).
	function removeLink(s: DocStage, kind: EdgeKind, depth: number) {
		const links = chainLinks(edgeOf(s, kind));
		if (depth === 0) setEdge(s, kind, links[0]?.then);
		else links[depth - 1].then = links[depth].then;
		onchange();
	}
	// A link's limit: how many times it may send the run to its target
	// (a whole number from 1), or none — a human decides each time.
	function setMax(link: DocEdge, text: string) {
		const n = Number(text);
		if (text.trim() === '' || !Number.isInteger(n) || n < 1) return;
		link.max = n;
		onchange();
	}
	function setUnlimited(link: DocEdge, on: boolean) {
		link.max = on ? -1 : 3;
		if (on) link.then = undefined;
		onchange();
	}

	// What the back button names: the panel `back` opens.
	const backLabel = $derived.by(() => {
		const b = back;
		if (b === undefined) return '';
		if (b === null) return t('flow.insp.formula');
		switch (b.kind) {
			case 'stage':
				return t('flow.insp.backStage', { id: b.sequence ? `${b.sequence} › ${b.id}` : b.id });
			case 'edge':
				return t('flow.insp.backStage', {
					id: b.sequence ? `${b.sequence} › ${b.stage}` : b.stage
				});
			case 'phase': {
				const ph = doc.phases?.find((p) => p.id === b.id);
				return ph?.kind === 'main'
					? t('flow.insp.chainMain')
					: t(ph?.kind === 'outcome' ? 'flow.insp.backOutcome' : 'flow.insp.backWatcher', {
							id: b.id
						});
			}
			case 'agent':
				return t('flow.insp.backAgent', { name: b.name });
			case 'sequence':
				return t('flow.insp.backSequence', { id: b.id });
		}
	});
</script>

<div class="insp form-dense">
	{#key viewKey}
		<div class="view-x" in:fade={xf()} out:fade={xf()}>
			{#if back !== undefined && onback}
				<button
					class="btn-sm btn-ghost back"
					title={t('flow.insp.back', { what: backLabel })}
					onclick={onback}>{I.back} {backLabel}</button
				>
			{/if}
			{#if stage}
				{@const s = stage}
				{@const only = scopeSeq?.stages.length === 1}
				<div class="ih">
					<StageChip type={s.type} />
					{#if scopeSeq}<span class="mono fnt">{scopeSeq.id}.</span>{/if}
					<span class="mono iid ell">{s.id}</span>
					<span class="spacer"></span>
					<button
						class="btn-danger btn-sm"
						disabled={only}
						onclick={() => deleteStage(s.id)}
						title={only ? t('flow.insp.onlyStage') : t('flow.insp.deleteTitle')}
						>{t('flow.insp.delete')}</button
					>
				</div>
				<div class="field">
					<label for="st-id">{t('flow.insp.id')}</label>
					<input
						id="st-id"
						class="mono"
						value={s.id}
						onchange={(e) =>
							renamed(e.currentTarget, s.id, renameStage(s.id, e.currentTarget.value))}
					/>
					<div class="help">{t('flow.insp.renameHelp')}</div>
				</div>
				{#if scopeSeq}
					{@const q = scopeSeq}
					<div class="field">
						<span class="lbl-in">{t('flow.insp.inSequence')}</span>
						<div class="row">
							<span class="mono">{q.id}</span>
							<span class="fnt small ell grow">{q.description ?? ''}</span>
							<button class="btn-sm" onclick={() => onopen({ kind: 'sequence', id: q.id })}
								>{t('flow.insp.editSequence')}</button
							>
						</div>
						<div class="help">{t('flow.insp.inSequenceHelp')}</div>
					</div>
				{:else if doc.phases?.length}
					<div class="field">
						<label for="st-phase">{t('flow.insp.chain')}</label>
						<select
							id="st-phase"
							value={s.phase}
							onchange={(e) => {
								movePhase(doc, s.id, e.currentTarget.value);
								onchange();
							}}
						>
							{#each doc.phases as ph (ph.id)}
								<option value={ph.id}
									>{ph.kind === 'main'
										? t('flow.insp.chainMain')
										: t(`flow.insp.chainOption.${ph.kind}`, {
												id: ph.id,
												trigger: triggerLabel(ph.on)
											})}</option
								>
							{/each}
						</select>
						<div class="help">{t('flow.insp.phaseHelp')}</div>
					</div>
				{/if}

				{#if s.type !== 'sequence'}
					<div class="field">
						<label for="st-when">{t('flow.insp.when')}</label>
						<select
							id="st-when"
							value={s.when ?? ''}
							onchange={(e) => {
								s.when = e.currentTarget.value || undefined;
								onchange();
							}}
						>
							<option value="">{t('flow.insp.when.none')}</option>
							<option value="tracker">{t('flow.insp.when.tracker')}</option>
							<option value="not tracker">{t('flow.insp.when.notTracker')}</option>
							<option value="subject">{t('flow.insp.when.subject')}</option>
							<option value="not subject">{t('flow.insp.when.notSubject')}</option>
							{#each boolParams(doc) as p (p)}
								<option value={'$' + p}>${p}</option>
								<option value={'not $' + p}>not ${p}</option>
							{/each}
							{#if s.when && !WHEN_TERMS.includes(s.when) && !boolParams(doc).some((p) => s.when === '$' + p || s.when === 'not $' + p)}
								<option value={s.when}>{s.when}</option>
							{/if}
						</select>
						<div class="help">{t('flow.insp.whenHelp')}</div>
					</div>
				{/if}
				<datalist id="flow-params">
					{#each stringParams(doc) as p (p)}<option value={'$' + p}></option>{/each}
				</datalist>
				<datalist id="flow-int-params">
					{#each intParams(doc) as p (p)}<option value={'$' + p}></option>{/each}
				</datalist>

				{#if s.type === 'agent'}
					<div class="field">
						<label for="st-agent">{t('flow.insp.agent')}</label>
						<input
							id="st-agent"
							list="agent-names"
							bind:value={s.agent}
							oninput={onchange}
							placeholder={agents[0]?.name ?? ''}
						/>
						<datalist id="agent-names">
							{#each agents as a (a.name)}<option value={a.name}></option>{/each}
						</datalist>
						<div class="help">{t('flow.insp.agentHelp')}</div>
						{#if agentDecl}
							{@const name = agentDecl.name}
							<div class="chips">
								<button class="btn-sm" onclick={() => onopen({ kind: 'agent', name })}
									>{t('flow.insp.editAgent', { name })}</button
								>
							</div>
						{:else}
							<div class="chips">
								<button class="btn-sm" onclick={() => newAgent(s.agent)}
									>{s.agent && AGENT_NAME.test(s.agent)
										? t('flow.insp.declareAgent', { name: s.agent })
										: t('flow.insp.newAgent')}</button
								>
							</div>
						{/if}
					</div>
					<div class="field">
						<label for="st-harness">{t('flow.insp.harness')}</label>
						<input
							id="st-harness"
							list="flow-params"
							bind:value={s.harness}
							oninput={onchange}
							placeholder={paramDefault(doc, 'harness') || 'opencode'}
						/>
						<div class="help">
							{t('flow.insp.overrideDefault', {
								value: paramDefault(doc, 'harness') || t('flow.insp.flowDefault')
							})}
						</div>
					</div>
					<div class="field">
						<label for="st-model">{t('flow.insp.model')}</label>
						<input
							id="st-model"
							list="flow-params"
							bind:value={s.model}
							oninput={onchange}
							placeholder={paramDefault(doc, 'model')}
						/>
						<div class="help">
							{t('flow.insp.overrideDefault', {
								value: paramDefault(doc, 'model') || t('flow.insp.flowDefault')
							})}
						</div>
					</div>
					<div class="field">
						<label for="st-turns">{t('flow.insp.maxTurns')}</label>
						<input
							id="st-turns"
							class="mono"
							list="flow-int-params"
							value={s.max_turns ?? ''}
							placeholder={paramDefault(doc, 'max_turns') ?? '50'}
							oninput={(e) => {
								const v = e.currentTarget.value.trim();
								s.max_turns = v === '' ? undefined : /^\d+$/.test(v) ? Number(v) : v;
								onchange();
							}}
						/>
						<div class="help">{t('flow.insp.numberOrParam')}</div>
					</div>
					<div class="field">
						<label for="st-timeout">{t('flow.insp.timeout')}</label>
						<input
							id="st-timeout"
							class="mono"
							list="flow-params"
							bind:value={s.timeout}
							oninput={onchange}
							placeholder={paramDefault(doc, 'timeout') ?? '30m'}
						/>
						<div class="help">{t('flow.insp.durationOrParam')}</div>
					</div>
					{#if declInputs.length > 0}
						<div class="grp">
							<div class="lbl">{t('flow.insp.inputs')}</div>
							<div class="help">{t('flow.insp.inputsHelp')}</div>
						</div>
						{#each declInputs as d (d.name)}
							{@const val = bindingValue(s, 'input', d.name)}
							<div class="io-bind card-in field">
								<label for="in-{d.name}"
									><span class="mono">{d.name}</span>
									{#if d.optional}<span class="fnt">{t('flow.insp.optional')}</span>{:else}<span
											class="hue-amb">{t('flow.insp.required')}</span
										>{/if}</label
								>
								{#if d.description}<div class="help">{d.description}</div>{/if}
								<input
									id="in-{d.name}"
									list="agent-sentinels"
									placeholder="artifacts/…"
									value={val}
									oninput={(e) => setBinding(s, 'input', d.name, e.currentTarget.value.trim())}
								/>
								{#if isFeedSentinel(val)}
									<div class="help">{t('flow.insp.feedHelp')}</div>
								{/if}
							</div>
						{/each}
						<datalist id="agent-sentinels">
							{#each sentinelsFor('agent') as v (v)}<option value={v}></option>{/each}
						</datalist>
					{:else if s.agent}
						<div class="help" class:hue-amb={!agentDecl}>
							{agentDecl
								? t('flow.insp.noInputs', { name: s.agent })
								: t('flow.insp.unknownAgent', { name: s.agent })}
						</div>
					{/if}

					{#if declOutputs.length > 0}
						<div class="grp">
							<div class="lbl">{t('flow.insp.outputs')}</div>
							<div class="help">{t('flow.insp.outputsHelp')}</div>
						</div>
						{#each declOutputs as d (d.name)}
							<div class="io-bind card-in field">
								<label for="out-{d.name}"
									><span class="mono">{d.name}</span>
									{#if d.type === 'verdict'}<span class="pill sm t-vio"
											>{t('flow.insp.verdict')}</span
										>{/if}</label
								>
								{#if d.description}<div class="help">{d.description}</div>{/if}
								<input
									id="out-{d.name}"
									value={bindingValue(s, 'output', d.name) || (d.path ?? '')}
									placeholder={d.path}
									oninput={(e) => {
										const v = e.currentTarget.value.trim();
										setBinding(s, 'output', d.name, v === d.path ? '' : v);
									}}
								/>
							</div>
						{/each}
					{/if}
					{#if declVerdict}
						<div class="help">
							{@html th('flow.insp.verdictHelp', { sentinel: SENTINEL_DECISION })}
						</div>
					{:else if s.agent && agentDecl && edgeOf(s, 'on_reject')}
						<div class="help hue-amb">{@html th('flow.insp.neverFires', { agent: s.agent })}</div>
					{/if}
				{:else if s.type === 'command'}
					<div class="field">
						<label for="st-runref">run_ref</label>
						<input
							id="st-runref"
							class="mono"
							list="flow-params"
							bind:value={s.run_ref}
							oninput={onchange}
							placeholder={t('flow.insp.runRefPlaceholder')}
						/>
						<div class="help">{t('flow.insp.commandHelp')}</div>
					</div>
					<div class="field">
						<label for="st-run">{t('flow.insp.literalScript')}</label>
						<textarea
							id="st-run"
							class="mono"
							bind:value={s.run}
							oninput={onchange}
							rows="3"
							placeholder="make test"></textarea>
					</div>
					<div class="field">
						<label for="st-cmd-timeout">{t('flow.insp.timeout')}</label>
						<input
							id="st-cmd-timeout"
							class="mono"
							list="flow-params"
							bind:value={s.timeout}
							oninput={onchange}
							placeholder={paramDefault(doc, 'timeout') ?? '30m'}
						/>
						<div class="help">{t('flow.insp.commandTimeoutHelp')}</div>
					</div>
					<Toggle
						checked={!!s.each_repo}
						label={t('flow.insp.eachRepo')}
						title={t('flow.insp.eachRepoTitle')}
						onchange={(v) => {
							s.each_repo = v || undefined;
							onchange();
						}}
					/>
					<div class="grp">
						<div class="lbl">{t('flow.insp.reads')}</div>
						<div class="help">{t('flow.insp.readsHelp')}</div>
					</div>
					<BindingRows stage={s} field="input" valuePlaceholder="artifacts/commit.md" {onchange} />
					<div class="grp">
						<div class="lbl">{t('flow.insp.writes')}</div>
						<div class="help">{t('flow.insp.writesHelp')}</div>
					</div>
					<BindingRows
						stage={s}
						field="output"
						valuePlaceholder="artifacts/report.txt"
						{onchange}
					/>
					<div class="help">{t('flow.insp.nameLabelHelp')}</div>
				{:else if s.type === 'human_gate'}
					<div class="field">
						<label for="st-question">{t('flow.insp.question')}</label>
						<textarea
							id="st-question"
							bind:value={s.question}
							oninput={onchange}
							rows="2"
							placeholder={t('flow.insp.questionPlaceholder')}></textarea>
						<div class="help">{t('flow.insp.questionHelp')}</div>
					</div>
					<div class="grp">
						<div class="lbl">{t('flow.insp.shows')}</div>
						<div class="help">{@html th('flow.insp.showsHelp')}</div>
					</div>
					<BindingRows
						stage={s}
						field="input"
						valuePlaceholder="artifacts/…"
						addLabel={t('flow.insp.addShows')}
						{onchange}
					/>
					<div class="grp">
						<div class="lbl">{t('flow.insp.autoBy')}</div>
						<div class="help">{t('flow.insp.autoByHelp')}</div>
					</div>
					<div class="grid2">
						<div class="field">
							<label for="st-auto-ok">{t('flow.insp.approveWhen')}</label>
							<select
								id="st-auto-ok"
								value={s.auto_approve ?? ''}
								onchange={(e) => {
									s.auto_approve = e.currentTarget.value || undefined;
									onchange();
								}}
							>
								<option value="">—</option>
								{#each observed as sig (sig.slug)}
									<option value={sig.slug} disabled={sig.slug === s.auto_reject}>{sig.title}</option
									>
								{/each}
							</select>
						</div>
						<div class="field">
							<label for="st-auto-no">{t('flow.insp.rejectWhen')}</label>
							<select
								id="st-auto-no"
								value={s.auto_reject ?? ''}
								onchange={(e) => {
									s.auto_reject = e.currentTarget.value || undefined;
									onchange();
								}}
							>
								<option value="">—</option>
								{#each observed as sig (sig.slug)}
									<option value={sig.slug} disabled={sig.slug === s.auto_approve}
										>{sig.title}</option
									>
								{/each}
							</select>
						</div>
					</div>
					{#if s.auto_approve || s.auto_reject}
						<div class="help">{t('flow.insp.manualStillOk')}</div>
					{/if}
				{:else if s.type === 'action'}
					<div class="field">
						<label for="st-action">{t('flow.insp.action')}</label>
						<select
							id="st-action"
							value={s.action ?? ''}
							onchange={(e) => {
								s.action = e.currentTarget.value || undefined;
								// Prefill the new action's file inputs with their conventional paths.
								const sig = actions.find((a) => a.slug === s.action);
								const rec: Record<string, string> = {};
								for (const d of sig?.inputs ?? []) if (d.defaultPath) rec[d.name] = d.defaultPath;
								s.input = Object.keys(rec).length ? rec : undefined;
								onchange();
							}}
						>
							<option value="">—</option>
							{#each actions as a (a.slug)}<option value={a.slug}>{a.title}</option>{/each}
						</select>
						<div class="help">{t('flow.insp.actionHelp')}</div>
					</div>
					{#if actionSig}
						{#if actionSig.description}<div class="help">{actionSig.description}</div>{/if}
						{#if actionSig.requires === 'tracker'}
							<div class="help">{t('flow.insp.tracker')}</div>
						{:else if actionSig.requires}
							<div class="help">
								{@html th('flow.insp.needsIntegration', { kind: portLabel(actionSig.requires) })}
							</div>
						{/if}
						{#if actionSig.needs === 'merge_request'}
							<div class="help">{t('flow.insp.needsMR')}</div>
						{/if}
						{#if actionSig.exactlyOne?.length}
							<div class="help">
								{t('flow.insp.exactlyOne', { names: actionSig.exactlyOne.join(' / ') })}
							</div>
						{/if}
						{#each actionSig.inputs ?? [] as d (d.name)}
							{@const cur = bindingValue(s, 'input', d.name)}
							{@const fits = d.kind === 'enum' ? enumParamsWithin(doc, d.values ?? []) : []}
							<div class="io-bind card-in field">
								<label for="act-{d.name}"
									><span class="mono">{d.name}</span>
									{#if d.required}<span class="hue-amb">{t('flow.insp.required')}</span>{:else}<span
											class="fnt">{t('flow.insp.optional')}</span
										>{/if}</label
								>
								{#if d.description}<div class="help">{d.description}</div>{/if}
								{#if d.kind === 'enum'}
									<select
										id="act-{d.name}"
										value={bindingValue(s, 'input', d.name) || d.defaultValue || ''}
										onchange={(e) => {
											setBinding(s, 'input', d.name, e.currentTarget.value);
											onchange();
										}}
									>
										{#if !d.required && !d.defaultValue}<option value="">—</option>{/if}
										{#each d.values ?? [] as v (v)}<option value={v}>{v}</option>{/each}
										{#each fits as p (p)}<option value={'$' + p}>${p}</option>{/each}
										{#if isParamRef(cur) && !fits.includes(cur.slice(1))}
											<option value={cur}>{cur}</option>
										{/if}
									</select>
									{#if fits.length}<div class="help">{t('flow.insp.enumParamHelp')}</div>{/if}
								{:else if d.kind === 'text'}
									<input
										id="act-{d.name}"
										list="flow-params"
										value={cur}
										placeholder={t('flow.insp.textPlaceholder')}
										oninput={(e) => setBinding(s, 'input', d.name, e.currentTarget.value.trim())}
									/>
									<div class="help">{t('flow.insp.textInputHelp')}</div>
								{:else}
									<input
										id="act-{d.name}"
										list="flow-params"
										value={cur}
										placeholder={d.defaultPath ?? ''}
										oninput={(e) => setBinding(s, 'input', d.name, e.currentTarget.value.trim())}
									/>
								{/if}
							</div>
						{/each}
						{#each actionSig.outputs ?? [] as d (d.name)}
							<div class="help">{@html producesHtml(d)}</div>
						{/each}
					{:else if s.action}
						<div class="help hue-amb">{t('flow.insp.unknownAction', { name: s.action })}</div>
					{/if}
				{:else if s.type === 'sequence'}
					{@const options = runnable(s)}
					<div class="field">
						<label for="st-sequence">{t('flow.insp.sequence')}</label>
						<select
							id="st-sequence"
							value={s.sequence ?? ''}
							onchange={(e) => {
								s.sequence = e.currentTarget.value;
								onchange();
							}}
						>
							{#each options as id (id)}<option value={id}
									>{id === MAIN ? t('flow.insp.runsMain') : id}</option
								>{/each}
							{#if s.sequence && !options.includes(s.sequence)}
								<option value={s.sequence}>{s.sequence}</option>
							{/if}
						</select>
						<div class="help">{t('flow.insp.sequenceHelp')}</div>
						{#if sequenceSel}
							{@const id = sequenceSel.id}
							<div class="chips">
								<button class="btn-sm" onclick={() => onopen({ kind: 'sequence', id })}
									>{t('flow.insp.editSequence')}</button
								>
							</div>
						{/if}
					</div>
					{#if sequenceSel}
						{#if sequenceSel.description}<div class="help">{sequenceSel.description}</div>{/if}
						<div class="grp">
							<div class="lbl">{t('flow.insp.sequenceStages')}</div>
							{#each sequenceSel.stages as q (q.id)}
								<div class="seq-row">
									<StageChip type={q.type} />
									<span class="mono">{s.id}.{q.id}</span>
									{#if q.sequence}<span class="mut small">→ {q.sequence}</span>{/if}
								</div>
							{/each}
						</div>
						<div class="help">{t('flow.insp.sequenceEditHelp')}</div>
					{/if}
				{:else if s.type === 'wait'}
					<div class="field">
						<label for="st-for">{t('flow.insp.waitFor')}</label>
						<select
							id="st-for"
							value={s.for ?? ''}
							onchange={(e) => {
								s.for = e.currentTarget.value || undefined;
								onchange();
							}}
						>
							<option value="">—</option>
							{#each observed as sig (sig.slug)}<option value={sig.slug}>{sig.title}</option>{/each}
						</select>
						<div class="help">{t('flow.insp.waitHelp')}</div>
					</div>
					{#if waitSig}
						{#if waitSig.description}<div class="help">{waitSig.description}</div>{/if}
						{#if waitSig.requires}
							<div class="help">
								{@html th('flow.insp.needsIntegration', { kind: portLabel(waitSig.requires) })}
							</div>
						{/if}
					{:else if s.for}
						<div class="help hue-amb">{t('flow.insp.unknownSignal', { name: s.for })}</div>
					{/if}
					<div class="field">
						<label for="st-wait-timeout">{t('flow.insp.timeout')}</label>
						<OptionalDuration
							id="st-wait-timeout"
							label={t('flow.insp.timeout')}
							bind:value={s.timeout}
							empty={t('flow.insp.waitsForever')}
							fallback="24h"
							{onchange}
						/>
					</div>
				{/if}

				{#if (freeBindings.length > 0 || freeShown[s.id]) && (s.type === 'agent' || s.type === 'action')}
					<div class="grp">
						<div class="lbl">{t('flow.insp.unrecognized')}</div>
						<div class="help hue-amb">
							{s.type === 'agent'
								? t('flow.insp.unrecognizedAgent')
								: t('flow.insp.unrecognizedAction')}
						</div>
					</div>
					{#key s.type === 'agent' ? s.agent : s.action}
						<BindingRows stage={s} field="input" keep={isDeclared} {onchange} />
					{/key}
				{/if}

				<div class="field">
					<label for="st-desc">{t('flow.insp.description')}</label>
					<input
						id="st-desc"
						bind:value={s.description}
						oninput={onchange}
						placeholder={t('flow.insp.descPlaceholder')}
					/>
					<div class="help">{t('flow.insp.descHelp')}</div>
				</div>

				<!-- Every way out of the stage, one row each in the same shape: its
				     kind, where it goes — and for a failure route, how many times,
				     then what. -->
				<div class="routes" bind:this={routesEl}>
					<div class="lbl">{t('flow.insp.routes')}</div>
					<div class="route" class:hl={focusRoute === 'success'} data-route="success">
						<div class="link">
							<span class="rk mono t-{EDGE_HUE.success}" title={t('flow.insp.successTitle')}
								>success</span
							>
							<select
								id="st-succ"
								class="to"
								aria-label={t('flow.insp.successTitle')}
								value={s.on_success ?? ''}
								onchange={(e) => {
									s.on_success = e.currentTarget.value || undefined;
									onchange();
								}}
							>
								{#if scopeSeq}
									<!-- In a sequence, saying nothing goes on to the next stage —
									     or, from the last, out by the sequence stage's success. -->
									<option value=""
										>{scopeSeq.stages.at(-1)?.id === s.id
											? t('flow.insp.leaveOption')
											: t('flow.insp.nextOption', {
													next: scopeSeq.stages[scopeSeq.stages.indexOf(s) + 1]?.id ?? ''
												})}</option
									>
								{/if}
								{#each routable(s).filter((x) => x.id !== s.id) as x (x.id)}<option value={x.id}
										>{x.id}</option
									>{/each}
								<option value={END}>{t('flow.insp.endOption')}</option>
								<option value={DONE}>{t('flow.insp.doneOption')}</option>
								<option value={ABORT}>{t('flow.insp.abortOption')}</option>
							</select>
						</div>
					</div>
					{#each edgeKindsFor(s.type) as kind (kind)}
						{@const e = edgeOf(s, kind)}
						{#if e}
							{@const links = chainLinks(e)}
							{@const last = links[links.length - 1]}
							<div class="route" class:hl={focusRoute === kind} data-route={kind}>
								{#each links as link, depth (depth)}
									<div class="link">
										<span
											class="rk mono t-{EDGE_HUE[kind]}"
											class:then={depth > 0}
											title={t('flow.insp.routeCarries', {
												kind,
												sentinel: feedSentinelFor(kind)
											})}>{depth > 0 ? 'then' : kind}</span
										>
										<select
											id="rt-{kind}-{depth}"
											class="to"
											aria-label={depth > 0 ? `${kind} · then #${depth}` : kind}
											value={link.to}
											onchange={(ev) => retarget(s.type, link, ev.currentTarget.value)}
										>
											{#each routable(s) as x (x.id)}<option value={x.id}>{x.id}</option>{/each}
											{#if scopeSeq}
												<option value={FAIL}>{t('flow.insp.failOption')}</option>
											{/if}
											<option value={END}>{t('flow.insp.endOption')}</option>
											<option value={DONE}>{t('flow.insp.doneOption')}</option>
											<option value={ABORT}>{t('flow.insp.abortOption')}</option>
											<option value={DEBUG}>{t('flow.insp.debugOption')}</option>
										</select>
										{#if !isReservedTarget(link.to)}
											{#if link.max === -1}
												<button
													class="btn-sm btn-sel unl"
													title={t('flow.insp.limitAgain')}
													onclick={() => setUnlimited(link, false)}>∞</button
												>
											{:else}
												<span class="x" aria-hidden="true">×</span>
												<input
													id="rt-{kind}-{depth}-max"
													class="mono max"
													type="number"
													min="1"
													aria-label={t('flow.insp.max')}
													title={t('flow.insp.maxTitle')}
													value={link.max ?? 1}
													oninput={(ev) => setMax(link, ev.currentTarget.value)}
												/>
												{#if unlimitedOK(s.type, link.to)}
													<button
														class="btn-sm unl"
														aria-label={t('flow.insp.noLimit')}
														title={targetsGate(link.to)
															? t('flow.insp.noLimitTitleGate')
															: t('flow.insp.noLimitTitle')}
														onclick={() => setUnlimited(link, true)}>∞</button
													>
												{/if}
											{/if}
										{/if}
										<button
											class="btn-icon"
											title={t('flow.insp.removeLink')}
											aria-label={t('flow.insp.removeLink')}
											onclick={() => removeLink(s, kind, depth)}>{I.close}</button
										>
									</div>
								{/each}
								{#if !isReservedTarget(last.to) && last.max !== -1}
									<button class="btn-sm btn-ghost add-then" onclick={() => appendLink(e)}
										>{I.add} {t('flow.insp.thenLink')}</button
									>
								{/if}
							</div>
						{/if}
					{/each}
					<div class="chips">
						{#each edgeKindsFor(s.type).filter((k) => !edgeOf(s, k)) as kind (kind)}
							<button class="btn-sm hue-{EDGE_HUE[kind]}" onclick={() => addEdge(s, kind)}
								>{I.add} {kind}</button
							>
						{/each}
					</div>
					<div class="help">
						{t(scopeSeq ? 'flow.insp.endsHelpSequence' : 'flow.insp.endsHelp')}
					</div>
					{#if edgeKindsFor(s.type).length}
						<div class="help">{t('flow.insp.routesHelp')}</div>
					{/if}
					{#if edgeKindsFor(s.type).includes('on_timeout')}
						<div class="help">{t('flow.insp.timeoutsHelp')}</div>
					{/if}
				</div>
			{:else if phaseSel}
				{@const ph = phaseSel}
				{@const firing = fires(ph.on)}
				<div class="ih">
					<span class="pill sm mono t-{kindHue(ph)}">{t(`flow.insp.kind.${ph.kind}`)}</span>
					{#if ph.kind !== 'main'}<span class="mono iid ell">{ph.id}</span>{/if}
					<span class="spacer"></span>
					<span class="fnt tiny"
						>{ph.kind === 'outcome' && !phaseStageCount
							? t('flow.insp.phaseThen', { then: ph.then ?? DONE })
							: t('flow.insp.phaseStages', { count: phaseStageCount })}</span
					>
					{#if ph.kind !== 'main'}
						<button
							class="btn-danger btn-sm"
							onclick={() => deletePhase(ph)}
							title={t(
								ph.kind === 'outcome' ? 'flow.insp.deleteOutcome' : 'flow.insp.deleteWatcher'
							)}>{t('flow.insp.delete')}</button
						>
					{/if}
				</div>
				{#if ph.kind === 'main'}
					<div class="help">{t('flow.insp.mainHelp')}</div>
					<div class="field">
						<label for="ph-operates">{t('flow.insp.operatesOn')}</label>
						<select
							id="ph-operates"
							value={doc.operates_on ?? 'ticket'}
							onchange={(e) => setOperatesOn(e.currentTarget.value)}
						>
							{#each OPERATES_ON as r (r)}<option value={r}>{t(`flow.insp.operatesOn.${r}`)}</option
								>{/each}
						</select>
						<div class="help">{t('flow.insp.operatesOnHelp')}</div>
					</div>
					{@render addTo(ph)}
				{:else}
					<div class="help">
						{t(ph.kind === 'outcome' ? 'flow.insp.outcomeHelp' : 'flow.insp.watcherHelp')}
					</div>
					<div class="field">
						<label for="ph-id">{t('flow.insp.id')}</label>
						<input
							id="ph-id"
							class="mono"
							value={ph.id}
							onchange={(e) =>
								renamed(e.currentTarget, ph.id, renamePhase(ph.id, e.currentTarget.value))}
						/>
						<div class="help">{t('flow.insp.phaseRenameHelp')}</div>
					</div>
					<div class="field">
						<label for="ph-desc">{t('flow.insp.description')}</label>
						<input
							id="ph-desc"
							value={ph.description ?? ''}
							oninput={(e) => {
								ph.description = e.currentTarget.value || undefined;
								onchange();
							}}
						/>
					</div>
					<div class="field">
						<label for="ph-on"
							>{t(ph.kind === 'outcome' ? 'flow.insp.firesOn' : 'flow.insp.opensOn')}</label
						>
						<select
							id="ph-on"
							value={ph.on.signal}
							onchange={(e) => {
								ph.on.signal = e.currentTarget.value;
								const sig = signals.find((g) => g.slug === ph.on.signal);
								if (!fires(ph.on)) {
									// A person's act is the start: no policy, nothing to debounce.
									ph.on.start = undefined;
									ph.on.quiet_for = undefined;
								} else if (!sig?.debounced) {
									ph.on.quiet_for = undefined;
								}
								onchange();
							}}
						>
							{#if ph.kind === 'watcher'}
								<option value={MANUAL}>{t('flow.insp.opensOn.manual')}</option>
							{/if}
							{#each observed as sig (sig.slug)}<option value={sig.slug}>{sig.title}</option>{/each}
							{#if firing && !observed.some((g) => g.slug === ph.on.signal)}
								<option value={ph.on.signal}>{ph.on.signal}</option>
							{/if}
						</select>
						<div class="help">
							{ph.on.signal === MANUAL
								? t('flow.insp.opensOnHelp.manual')
								: (phaseSig?.description ?? '')}
						</div>
					</div>
					{#if firing}
						<div class="field">
							<label for="ph-start">{t('flow.insp.start')}</label>
							<select
								id="ph-start"
								value={ph.on.start ?? 'auto'}
								onchange={(e) => {
									const v = e.currentTarget.value;
									ph.on.start = v === 'auto' ? undefined : v;
									onchange();
								}}
							>
								{#each START_POLICIES as p (p)}<option value={p}>{t(`flow.insp.start.${p}`)}</option
									>{/each}
								{#each policyParams(doc) as p (p)}<option value={'$' + p}>${p}</option>{/each}
								{#if isParamRef(ph.on.start) && !policyParams(doc).includes(ph.on.start.slice(1))}
									<option value={ph.on.start}>{ph.on.start}</option>
								{/if}
							</select>
							<div class="help">
								{t(ph.kind === 'outcome' ? 'flow.insp.startHelpOutcome' : 'flow.insp.startHelp')}
							</div>
						</div>
						{#if phaseSig?.debounced || ph.on.quiet_for}
							<div class="field">
								<label for="ph-quiet">{t('flow.insp.quietFor')}</label>
								<OptionalDuration
									id="ph-quiet"
									label={t('flow.insp.quietFor')}
									bind:value={ph.on.quiet_for}
									empty={t('flow.insp.noQuiet')}
									fallback="1h"
									params={stringParams(doc)}
									{onchange}
								/>
								<div class="help">{t('flow.insp.quietForHelp')}</div>
							</div>
						{/if}
					{/if}
					{#if ph.kind === 'outcome'}
						<div class="field">
							<label for="ph-then">{t('flow.insp.then')}</label>
							<select
								id="ph-then"
								value={ph.then ?? DONE}
								onchange={(e) => setThen(ph, e.currentTarget.value)}
							>
								<option value={DONE}>{t('flow.insp.then.done')}</option>
								<option value={ABORT}>{t('flow.insp.then.abort')}</option>
							</select>
							<div class="help">
								{t(phaseStageCount ? 'flow.insp.outcomeStagesHelp' : 'flow.insp.phaseThenHelp')}
							</div>
						</div>
					{:else}
						<div class="field">
							<label for="ph-max">{t('flow.insp.phaseMax')}</label>
							<input
								id="ph-max"
								class="mono"
								list="flow-int-params"
								value={ph.on.max ?? ''}
								placeholder={t('flow.insp.unlimited')}
								oninput={(e) => {
									const v = e.currentTarget.value.trim();
									ph.on.max = v === '' ? undefined : /^-?\d+$/.test(v) ? Number(v) : v;
									onchange();
								}}
							/>
							<div class="help">{t('flow.insp.phaseMaxHelp')}</div>
						</div>
					{/if}
					{@const choices = [
						...(hasMain(doc) ? [MAIN] : []),
						...(doc.sequences ?? []).map((q) => q.id)
					]}
					<div class="field">
						<label for="ph-runs">{t('flow.insp.runs')}</label>
						<select
							id="ph-runs"
							value={phaseRuns}
							onchange={(e) => {
								const el = e.currentTarget;
								void setRuns(ph, el.value).then(() => (el.value = phaseRuns));
							}}
						>
							<option value=""
								>{ph.kind === 'outcome' && !phaseStageCount
									? t('flow.insp.runsNothing')
									: t('flow.insp.runsOwn', { count: phaseStageCount })}</option
							>
							{#each choices as id (id)}<option value={id}
									>{id === MAIN
										? t('flow.insp.runsMain')
										: t('flow.insp.runsSequence', { id })}</option
								>{/each}
						</select>
						<div class="help">
							{choices.length ? t('flow.insp.runsHelp') : t('flow.insp.runsHelpNone')}
						</div>
					</div>
					{@render addTo(ph)}
				{/if}
				<datalist id="flow-int-params">
					{#each intParams(doc) as p (p)}<option value={'$' + p}></option>{/each}
				</datalist>
			{:else if agentSel}
				{@const a = agentSel}
				{@const used = runners((x) => x.type === 'agent' && x.agent === a.name)}
				<div class="ih">
					<span class="pill sm mono t-{STAGE_HUE.agent}">{t('flow.insp.kind.agent')}</span>
					<span class="mono iid ell">{a.name}</span>
					<span class="spacer"></span>
					<span class="fnt tiny">{t('flow.insp.runByCount', { count: used.length })}</span>
					<button
						class="btn-danger btn-sm"
						onclick={() => deleteAgent(a.name)}
						title={t('flow.insp.deleteAgent')}>{t('flow.insp.delete')}</button
					>
				</div>
				<div class="help">{t('flow.insp.agentPanelHelp')}</div>
				<div class="field">
					<label for="ag-name">{t('flow.insp.agentName')}</label>
					<input
						id="ag-name"
						class="mono"
						value={a.name}
						onchange={(e) =>
							renamed(e.currentTarget, a.name, renameAgentTo(a.name, e.currentTarget.value))}
					/>
					<div class="help">{t('flow.insp.agentRenameHelp')}</div>
				</div>
				<div class="field">
					<label for="ag-desc">{t('flow.insp.description')}</label>
					<input
						id="ag-desc"
						value={a.description ?? ''}
						placeholder={t('flow.insp.agentDescPlaceholder')}
						oninput={(e) => {
							a.description = e.currentTarget.value || undefined;
							onchange();
						}}
					/>
				</div>
				<Toggle
					checked={!!a.read_only}
					label={t('flow.insp.readOnly')}
					title={t('flow.insp.readOnlyTitle')}
					onchange={(v) => {
						a.read_only = v || undefined;
						onchange();
					}}
				/>
				<Toggle
					checked={!!a.allow_bash}
					label={t('flow.insp.allowBash')}
					title={t('flow.insp.allowBashTitle')}
					onchange={(v) => {
						a.allow_bash = v || undefined;
						onchange();
					}}
				/>
				<div class="field">
					<label for="ag-prompt">{t('flow.insp.prompt')}</label>
					<textarea
						id="ag-prompt"
						class="mono prompt"
						rows="12"
						bind:value={a.prompt}
						oninput={onchange}
						placeholder={t('flow.insp.promptPlaceholder')}></textarea>
					<div class="help">{t('flow.insp.promptHelp')}</div>
				</div>
				<div class="grp">
					<div class="lbl">{t('flow.insp.inputs')}</div>
					<div class="help">{t('flow.insp.agentInputsHelp')}</div>
				</div>
				{#each a.inputs ?? [] as d, i (i)}
					<div class="io-bind card-in port">
						<div class="row nowrap">
							<input
								class="mono grow"
								value={d.name}
								aria-label={t('flow.insp.portName')}
								onchange={(e) =>
									renamed(
										e.currentTarget,
										d.name,
										renamePort(a, 'input', d.name, e.currentTarget.value)
									)}
							/>
							<button class="btn-sm btn-ghost" onclick={() => removeInput(a, d.name)}
								>{t('flow.insp.removePort')}</button
							>
						</div>
						<input
							value={d.description ?? ''}
							aria-label={t('flow.insp.description')}
							placeholder={t('flow.insp.portDescPlaceholder')}
							oninput={(e) => {
								d.description = e.currentTarget.value || undefined;
								onchange();
							}}
						/>
						<Toggle
							checked={!!d.optional}
							label={t('flow.insp.optional')}
							title={t('flow.insp.optionalTitle')}
							onchange={(v) => {
								d.optional = v || undefined;
								onchange();
							}}
						/>
					</div>
				{/each}
				<div class="chips">
					<button class="btn-sm" onclick={() => addInput(a)}
						>{I.add} {t('flow.insp.addInput')}</button
					>
				</div>
				<div class="grp">
					<div class="lbl">{t('flow.insp.outputs')}</div>
					<div class="help">{t('flow.insp.agentOutputsHelp')}</div>
				</div>
				{#each a.outputs ?? [] as d, i (i)}
					<div class="io-bind card-in port">
						<div class="row nowrap">
							<input
								class="mono grow"
								value={d.name}
								aria-label={t('flow.insp.portName')}
								onchange={(e) =>
									renamed(
										e.currentTarget,
										d.name,
										renamePort(a, 'output', d.name, e.currentTarget.value)
									)}
							/>
							<button class="btn-sm btn-ghost" onclick={() => removeOutput(a, d.name)}
								>{t('flow.insp.removePort')}</button
							>
						</div>
						<input
							class="mono"
							value={d.path}
							aria-label={t('flow.insp.portPath')}
							placeholder="artifacts/…"
							oninput={(e) => {
								d.path = e.currentTarget.value.trim();
								onchange();
							}}
						/>
						<select
							value={d.type ?? ''}
							aria-label={t('flow.insp.portType')}
							onchange={(e) => {
								d.type = e.currentTarget.value || undefined;
								onchange();
							}}
						>
							{#each OUTPUT_TYPES as ty (ty)}<option value={ty}
									>{t(`flow.insp.outputType.${ty || 'file'}`)}</option
								>{/each}
							{#if d.type && !(OUTPUT_TYPES as readonly string[]).includes(d.type)}
								<option value={d.type}>{d.type}</option>
							{/if}
						</select>
						<input
							value={d.description ?? ''}
							aria-label={t('flow.insp.description')}
							placeholder={t('flow.insp.portDescPlaceholder')}
							oninput={(e) => {
								d.description = e.currentTarget.value || undefined;
								onchange();
							}}
						/>
					</div>
				{/each}
				<div class="chips">
					<button class="btn-sm" onclick={() => addOutput(a)}
						>{I.add} {t('flow.insp.addOutput')}</button
					>
				</div>
				<div class="grp">
					<div class="lbl">{t('flow.insp.runBy')}</div>
					{#if used.length}
						<div class="chips">
							{#each used as u (u.label)}<button class="btn-sm mono" onclick={() => onopen(u.sel)}
									>{u.label}</button
								>{/each}
						</div>
					{:else}
						<div class="help">{t('flow.insp.agentUnused')}</div>
					{/if}
				</div>
			{:else if seqSel}
				{@const q = seqSel}
				{@const users = runners((x) => x.type === 'sequence' && x.sequence === q.id)}
				<div class="ih">
					<span class="pill sm mono t-{STAGE_HUE.sequence}">{t('flow.insp.kind.sequence')}</span>
					<span class="mono iid ell">{q.id}</span>
					<span class="spacer"></span>
					<span class="fnt tiny">{t('flow.insp.phaseStages', { count: q.stages.length })}</span>
					<button
						class="btn-danger btn-sm"
						disabled={users.length > 0}
						onclick={() => deleteSequence(q)}
						title={users.length ? t('flow.insp.sequenceInUse') : t('flow.insp.deleteSequence')}
						>{t('flow.insp.delete')}</button
					>
				</div>
				<div class="help">{t('flow.insp.sequencePanelHelp')}</div>
				<div class="field">
					<label for="sq-id">{t('flow.insp.id')}</label>
					<input
						id="sq-id"
						class="mono"
						value={q.id}
						onchange={(e) =>
							renamed(e.currentTarget, q.id, renameSequenceTo(q.id, e.currentTarget.value))}
					/>
					<div class="help">{t('flow.insp.sequenceRenameHelp')}</div>
				</div>
				<div class="field">
					<label for="sq-desc">{t('flow.insp.description')}</label>
					<input
						id="sq-desc"
						value={q.description ?? ''}
						oninput={(e) => {
							q.description = e.currentTarget.value || undefined;
							onchange();
						}}
					/>
				</div>
				<div class="grp">
					<div class="lbl">{t('flow.insp.sequenceStages')}</div>
					{#each q.stages as x, i (x.id)}
						<div class="seq-line">
							<button
								class="rowbtn seq-row"
								onclick={() => onopen({ kind: 'stage', id: x.id, sequence: q.id })}
							>
								<StageChip type={x.type} />
								<span class="mono">{x.id}</span>
								{#if x.sequence}<span class="mut small">→ {x.sequence}</span>{/if}
							</button>
							<button
								class="btn-icon"
								disabled={i === 0}
								title={t('flow.insp.moveUp')}
								aria-label={t('flow.insp.moveUp')}
								onclick={() => {
									moveSequenceStage(doc, q.id, x.id, -1);
									onchange();
								}}>↑</button
							>
							<button
								class="btn-icon"
								disabled={i === q.stages.length - 1}
								title={t('flow.insp.moveDown')}
								aria-label={t('flow.insp.moveDown')}
								onclick={() => {
									moveSequenceStage(doc, q.id, x.id, 1);
									onchange();
								}}>↓</button
							>
						</div>
					{/each}
					<div class="help">{t('flow.insp.sequenceOrderHelp')}</div>
				</div>
				<div class="lbl">{t('flow.insp.addToSequence')}</div>
				<div class="chips">
					{#each addKinds({ sequence: q.id }) as kind (kind)}
						<button class="btn-sm" onclick={() => addToSequence(kind, q.id)}
							><span class="ldot sm flat hue-{STAGE_HUE[kind]}"></span>{STAGE_WORD[kind] ??
								kind}</button
						>
					{/each}
				</div>
				<div class="grp">
					<div class="lbl">{t('flow.insp.runBy')}</div>
					{#if users.length}
						<div class="chips">
							{#each users as u (u.label)}<button class="btn-sm mono" onclick={() => onopen(u.sel)}
									>{u.label}</button
								>{/each}
						</div>
					{:else}
						<div class="help">{t('flow.insp.sequenceUnused')}</div>
					{/if}
				</div>
			{:else}
				<div class="ih">
					<span class="serif ttl">{t('flow.insp.formula')}</span>
					<span class="spacer"></span>
					<span class="fnt tiny">{t('flow.insp.nothingSelected')}</span>
				</div>
				<div class="field">
					<label for="fl-desc">{t('flow.insp.description')}</label>
					<input
						id="fl-desc"
						value={doc.description ?? ''}
						oninput={(e) => {
							doc.description = e.currentTarget.value || undefined;
							onchange();
						}}
					/>
				</div>
				<div class="field">
					<label for="fl-operates">{t('flow.insp.operatesOn')}</label>
					<select
						id="fl-operates"
						value={doc.operates_on ?? 'ticket'}
						onchange={(e) => setOperatesOn(e.currentTarget.value)}
					>
						{#each OPERATES_ON as r (r)}<option value={r}>{t(`flow.insp.operatesOn.${r}`)}</option
							>{/each}
					</select>
					<div class="help">{t('flow.insp.operatesOnHelp')}</div>
				</div>
				<div class="lbl">{t('flow.insp.params')}</div>
				<div class="help">{t('flow.insp.paramsHelp')}</div>
				<ParamsEditor {doc} {onchange} />
				<div class="lbl">{t('flow.insp.agents')}</div>
				<div class="help">{t('flow.insp.agentsHelp')}</div>
				<div class="chips">
					{#each doc.agents ?? [] as a (a.name)}<button
							class="btn-sm mono"
							onclick={() => onopen({ kind: 'agent', name: a.name })}>{a.name}</button
						>{/each}
					<button class="btn-sm" onclick={() => newAgent()}
						>{I.add} {t('flow.insp.addAgent')}</button
					>
				</div>
				<div class="lbl">{t('flow.insp.sequences')}</div>
				<div class="help">{t('flow.insp.sequencesHelp')}</div>
				<div class="chips">
					{#each doc.sequences ?? [] as q (q.id)}<button
							class="btn-sm mono"
							onclick={() => onopen({ kind: 'sequence', id: q.id })}>{q.id}</button
						>{/each}
					<button class="btn-sm" onclick={newSequence}>{I.add} {t('flow.insp.addSequence')}</button>
				</div>
				<div class="lbl">{t('flow.insp.addChain')}</div>
				<div class="chips">
					{#if !mainPhase}
						<button class="btn-sm" onclick={newMainChain}
							><span class="ldot sm flat hue-brand"></span>{t('flow.insp.addMain')}</button
						>
					{/if}
					<button class="btn-sm" onclick={() => addChain('watcher')}
						><span class="ldot sm flat hue-vio"></span>{t('flow.insp.addWatcher')}</button
					>
					<button class="btn-sm" onclick={() => addChain('outcome')}
						><span class="ldot sm flat hue-grn"></span>{t('flow.insp.addOutcome')}</button
					>
				</div>
				<div class="help">
					{t(mainPhase ? 'flow.insp.addChainHelp' : 'flow.insp.addChainHelpNoMain')}
				</div>
				{#if doc.phases?.length}
					{@const into = mainPhase ?? doc.phases[0]}
					<div class="lbl">
						{into.kind === 'main'
							? t('flow.insp.addStageMain')
							: t('flow.insp.addStageTo', { id: into.id })}
					</div>
					<div class="chips">
						{#each addKinds({ phase: into.id }) as kind (kind)}
							<button class="btn-sm" onclick={() => addStage(kind)}
								><span class="ldot sm flat hue-{STAGE_HUE[kind]}"></span>{STAGE_WORD[kind] ??
									kind}</button
							>
						{/each}
					</div>
				{/if}
				<div class="help">{t('flow.insp.canvasHelp')}</div>
			{/if}
		</div>
	{/key}
</div>

<!-- A chain's own way to grow: a stage of each kind, appended after its
     last stage and wired in. -->
{#snippet addTo(ph: DocPhase)}
	<div class="lbl">{t('flow.insp.addToChain')}</div>
	<div class="chips">
		{#each addKinds({ phase: ph.id }) as kind (kind)}
			<button class="btn-sm" onclick={() => addToChain(kind, ph.id)}
				><span class="ldot sm flat hue-{STAGE_HUE[kind]}"></span>{STAGE_WORD[kind] ?? kind}</button
			>
		{/each}
	</div>
	<div class="help">{t('flow.insp.addToChainHelp')}</div>
{/snippet}

<style>
	/* The inspector sits on control glass, given by the page (DESIGN 7.8). */
	.insp {
		display: grid;
		padding: 12px 14px 16px;
		font-size: var(--fs-ui);
	}
	/* A sequence stage's sequence, stage by stage, as it will run. */
	.seq-row {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
		padding: 2px 0;
	}
	.seq-row.rowbtn {
		padding: 4px 6px;
	}
	.seq-line {
		display: flex;
		align-items: center;
		gap: 2px;
	}
	/* An agent's declared input or output: its name, then what describes it. */
	.port {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.prompt {
		min-height: 180px;
		resize: vertical;
	}
	.lbl-in {
		font-size: var(--fs-label, inherit);
	}
	/* The way back to the panel this one was opened from. */
	.back {
		align-self: flex-start;
		margin: -4px 0 -2px -6px;
		color: var(--mut);
	}
	/* A stage's routes: one row per link, each in its route's hue. */
	.routes {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.route {
		display: flex;
		flex-direction: column;
		gap: 4px;
		padding: 6px 8px;
		border-radius: var(--r-row);
		border: 1px solid var(--ln);
		transition:
			border-color var(--dur-fade) var(--ease-out),
			box-shadow var(--dur-fade) var(--ease-out);
	}
	/* The route an edge picked on the canvas. */
	.route.hl {
		border-color: var(--brandLn);
		box-shadow: 0 0 0 3px var(--brandBg);
	}
	.link {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
	}
	.rk {
		flex: none;
		width: 84px;
		font-size: var(--fs-id);
		font-weight: 600;
	}
	.rk.then {
		padding-left: 12px;
		opacity: 0.75;
	}
	.to {
		flex: 1;
		min-width: 0;
	}
	.x {
		color: var(--mut);
	}
	.max {
		width: 56px;
	}
	.unl {
		min-width: 28px;
		padding-inline: 6px;
	}
	.add-then {
		align-self: flex-start;
		margin-left: 84px;
	}
	/* Outgoing and incoming views share the cell while they cross-fade. */
	.view-x {
		grid-area: 1 / 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.ih {
		display: flex;
		align-items: center;
		gap: 8px;
		padding-bottom: 10px;
		border-bottom: 1px solid var(--ln);
		min-width: 0;
	}
	.iid {
		font-size: var(--fs-body);
		font-weight: 600;
	}
	.ttl {
		font-size: var(--fs-section);
	}
	.grp {
		display: flex;
		flex-direction: column;
		gap: 2px;
		margin-top: 4px;
	}
	.lbl {
		margin-top: 4px;
	}
	.io-bind {
		padding: 7px 9px;
	}
	.io-bind label {
		display: flex;
		align-items: center;
		gap: 6px;
	}
</style>
