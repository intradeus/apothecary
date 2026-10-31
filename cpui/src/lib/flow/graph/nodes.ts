// The canvas's nodes: the stages, then what the chains begin and end on. The
// order is the drawing order: FlowGraph puts the leaving ghosts between the two.
import type { Node } from '@xyflow/svelte';
import { t } from '$lib/i18n/index.svelte';
import {
	ABORT,
	chainEnds,
	chainLinks,
	debugKey,
	DONE,
	EDGE_KINDS,
	edgeLinks,
	edgeOf,
	END,
	FAIL,
	HUB,
	MAIN,
	MANUAL,
	markerKey,
	parksOnDebug,
	PHASE_KINDS,
	phaseEntry,
	sectionKey,
	showsSections,
	START,
	terminalKey,
	triggerLabel,
	type DocPhase,
	type DocSequence,
	type DocStage,
	type FlowDoc,
	type Terminal
} from '$lib/domain/flowdoc';
import { debugNodeID, EXIT } from '$lib/flow/graph/ids';
import type { Layout, Scene } from '$lib/flow/graph/scene';

/** How a stage's id changed since the last build. */
export interface Arrivals {
	added: string[];
	removed: Node[];
	/** One out, one in, in the same edit. */
	renamed: { from: Node; to: string } | null;
}

/** What a node measured last time, or a guess. */
type Width = (id: string, guess?: number) => number;

/** What the last build drew (`known`, none on the first) against the document,
 *  so a rebuild can tell what just arrived from what just left. */
export function arrivalsOf(
	doc: FlowDoc,
	ids: Set<string>,
	prevAll: Node[],
	known: Set<string> | null
): Arrivals {
	const prev = prevAll.filter((n) => n.type === 'stage' && !n.data.leaving);
	const added = known ? doc.stages.filter((s) => !known.has(s.id)).map((s) => s.id) : [];
	const removed = known ? prev.filter((n) => !ids.has(n.id)) : [];
	// One out, one in, in the same edit: a rename. It keeps its place on
	// the canvas instead of leaving and arriving.
	const renamed =
		added.length === 1 && removed.length === 1 ? { from: removed[0], to: added[0] } : null;
	return { added, removed, renamed };
}

/** A stage that left, dematerialising where it stood. */
export const ghostOf = (n: Node): Node => ({
	...n,
	selected: false,
	selectable: false,
	draggable: false,
	connectable: false,
	data: { ...n.data, leaving: true, fresh: false }
});

/** What a node measured last time: where a node nobody placed goes depends
 *  on how wide the one before it is. */
export const widthIn =
	(prevAll: Node[]): Width =>
	(id, guess = 220) =>
		prevAll.find((n) => n.id === id)?.measured?.width ?? guess;

/** The stages, and where they ended up (`placed`) for what stands after them. */
export function stageNodes(sc: Scene, { added, renamed }: Arrivals, width: Width) {
	const { doc, layout, agents, actions } = sc;
	// Where a stage nobody has placed yet goes: after the stage before it
	// in its chain, else beside its chain's marker — a new watcher's first
	// stage lands on its own row, not on another chain's.
	const placed: Layout = {};
	const guess = (i: number) => {
		const s = doc.stages[i];
		const prev = doc.stages[i - 1];
		if (prev && prev.phase === s.phase && placed[prev.id])
			return { x: placed[prev.id].x + width(prev.id) + 40, y: placed[prev.id].y };
		const ph = doc.phases?.find((p) => p.id === s.phase);
		const m = ph && layout[markerKey(ph)];
		return m ? { x: m.x + 220, y: m.y } : { x: i * 240, y: 0 };
	};
	const nodes: Node[] = doc.stages.map((s, i) => ({
		id: s.id,
		type: 'stage',
		position: (placed[s.id] =
			layout[s.id] ?? (renamed?.to === s.id ? renamed.from.position : undefined) ?? guess(i)),
		hidden: sc.hidden(s.phase),
		data: {
			stage: s,
			entry: i === 0 || phaseEntry(doc, s.phase)?.id === s.id,
			agents,
			actions,
			sequences: doc.sequences ?? ([] as DocSequence[]),
			fresh: !renamed && added.includes(s.id),
			band: sc.bandOf(s.phase),
			labels: edgeLinks(s).length
		}
	}));
	return { nodes, placed };
}

// One beginning per chain: a marker naming it and what opens it, pointing at
// its first stage. The main chain's is `__start` (layout key START), drawn
// solid — what every run starts with; a watcher's is `__trigger:<phase>`,
// dashed; an outcome's (data.outcome) is the same beginning in the hue of the
// run's end it says, done or abort.
const markerTitle = (ph: DocPhase) =>
	ph.kind === 'outcome'
		? t(ph.then === ABORT ? 'flow.graph.outcomeAbortTitle' : 'flow.graph.outcomeTitle', {
				phase: ph.id,
				signal: ph.on.signal
			})
		: ph.on.signal === MANUAL
			? t('flow.graph.manualTitle', { phase: ph.id })
			: t('flow.graph.triggerTitle', { phase: ph.id, signal: ph.on.signal });

function triggerNodes({ doc, layout, hub, triggers, collapsed }: Scene): Node[] {
	return triggers.map((ph) => {
		const first = phaseEntry(doc, ph.id);
		const at = first ? (layout[first.id] ?? { x: 0, y: 0 }) : { x: 0, y: 0 };
		const label =
			ph.kind === 'outcome'
				? t('flow.graph.outcomeLabel', {
						phase: ph.id,
						trigger: triggerLabel(ph.on),
						then: ph.then ?? DONE
					})
				: t('flow.graph.beginLabel', { phase: ph.id, trigger: triggerLabel(ph.on) });
		const folded = collapsed.has(ph.id) ? doc.stages.filter((s) => s.phase === ph.id).length : 0;
		return {
			id: `__trigger:${ph.id}`,
			type: 'start',
			position: layout[markerKey(ph)] ?? { x: at.x - 220, y: at.y },
			data: {
				entry: false,
				outcome: ph.kind === 'outcome',
				then: ph.then ?? DONE,
				hub,
				band: ph.id,
				label: folded ? `${label} · ${t('flow.graph.folded', { count: folded })}` : label,
				title: markerTitle(ph)
			}
		};
	});
}

// The main chain's marker: its edge points at its first stage, and dragging
// that edge onto another stage of it starts it there. A flow that only watches
// begins at the hub; a sequence drawn on its own has its own marker.
function startNodes({ doc, layout, sequence, mainPhase, watchesOnly }: Scene): Node[] {
	const entryPos = doc.stages.length
		? (layout[doc.stages[0].id] ?? { x: 0, y: 0 })
		: { x: 0, y: 0 };
	const runsOn = t(`flow.graph.operatesOn.${doc.operates_on ?? 'ticket'}`);
	if (!(sequence || mainPhase || !doc.phases?.length || watchesOnly)) return [];
	return [
		{
			id: '__start',
			type: 'start',
			position: layout[START] ?? { x: entryPos.x - 170, y: entryPos.y },
			connectable: !watchesOnly,
			data: {
				entry: true,
				band: MAIN,
				label: sequence
					? t('flow.graph.sequenceLabel', { sequence: sequence.id })
					: mainPhase
						? t('flow.graph.mainLabel', { on: runsOn })
						: watchesOnly
							? t('flow.graph.watchLabel', { on: runsOn })
							: t('flow.graph.beginLabelPlain'),
				title: sequence
					? (sequence.description ?? t('flow.graph.sequenceTitle', { sequence: sequence.id }))
					: watchesOnly
						? t('flow.graph.watchTitle', { on: runsOn })
						: t('flow.graph.mainTitle', { on: runsOn })
			}
		}
	];
}

// The sections' labels, heading the marker column where the main chain, the
// watchers and the outcomes begin — once there is more than one chain to tell
// apart.
function sectionNodes({ doc, layout, sequence }: Scene): Node[] {
	if (sequence || !showsSections(doc)) return [];
	return PHASE_KINDS.flatMap((kind) => {
		const first = doc.phases!.find((p) => p.kind === kind);
		if (!first) return [];
		const at = layout[markerKey(first)] ?? { x: -220, y: 0 };
		return {
			id: `__section:${kind}`,
			type: 'section',
			position: layout[sectionKey(kind)] ?? { x: at.x, y: at.y - 32 },
			draggable: false,
			selectable: false,
			connectable: false,
			focusable: false,
			data: {
				label: t(`flow.graph.section.${kind}`),
				title: t(`flow.graph.sectionTitle.${kind}`)
			}
		};
	});
}

// The hub, left of the first chain that branches from it.
function hubNodes({ layout, hub, triggers }: Scene): Node[] {
	if (!hub) return [];
	const m = layout[markerKey(triggers[0])] ?? { x: -220, y: 0 };
	return [
		{
			id: '__hub',
			type: 'hub',
			position: layout[HUB] ?? { x: m.x - 200, y: m.y - 96 },
			data: {
				label: t('flow.graph.hubLabel'),
				sub: t('flow.graph.hubSub'),
				title: t('flow.graph.hubTitle')
			}
		}
	];
}

// A sequence's two ways out, where the run goes on from the stage that runs
// it: its success, and `fail` (its on_fail) when a stage routes there.
function exitNodes({ doc, layout, sequence }: Scene): Node[] {
	if (!sequence) return [];
	const at = layout[END] ?? { x: doc.stages.length * 240, y: 0 };
	const ns: Node[] = [
		{
			id: EXIT,
			type: 'exit',
			position: at,
			data: { label: t('flow.graph.sequenceExit', { to: sequence.exit }) }
		}
	];
	const fails = doc.stages.some((s) =>
		EDGE_KINDS.some((k) => chainLinks(edgeOf(s, k)).some((l) => l.to === FAIL))
	);
	if (fails)
		ns.push({
			id: '__fail',
			type: 'failexit',
			position: layout[DONE] ?? { x: at.x, y: at.y + 80 },
			data: { label: t('flow.graph.sequenceFail', { to: sequence.fail ?? FAIL }) }
		});
	return ns;
}

// The shared ends, for the main chain (or a flow, or a sequence, drawn without
// chains): end — this chain is over, the run goes back to waiting — is always
// drawn in a flow without a hub, which stands for it otherwise; done (the run
// is done) and abort only when a route reaches them.
function sharedEndNodes({ doc, layout, sequence, hub, branch }: Scene): Node[] {
	const trunk = doc.stages.filter((s) => !branch(s.phase));
	const reaches = (stages: DocStage[], to: string) =>
		stages.some(
			(s) =>
				s.on_success === to ||
				EDGE_KINDS.some((k) => chainLinks(edgeOf(s, k)).some((l) => l.to === to))
		);
	// (A sequence's view keeps END and DONE for its ways out: its own ends
	// stand under them.)
	const below = (key: string, dy: number) =>
		sequence && layout[key] ? { x: layout[key].x, y: layout[key].y + dy } : layout[key];
	const ns: Node[] = [];
	if (sequence ? reaches(trunk, END) : !hub)
		ns.push({
			id: '__end',
			type: 'end',
			position: below(END, 70) ?? { x: doc.stages.length * 240, y: 0 },
			data: { band: MAIN }
		});
	if (reaches(trunk, DONE))
		ns.push({
			id: '__done',
			type: 'done',
			position: below(DONE, 70) ?? { x: doc.stages.length * 240, y: 80 },
			data: { band: MAIN }
		});
	if (reaches(trunk, ABORT))
		ns.push({
			id: '__abort',
			type: 'abort',
			position: below(END, 140) ?? layout[ABORT] ?? { x: doc.stages.length * 240, y: 160 },
			data: { band: MAIN }
		});
	return ns;
}

// A watcher's or an outcome's own ends, closing its row: where its chain goes
// back to waiting, or ends the run. One nobody placed yet stands right of the
// chain's last stage.
function chainEndNodes(sc: Scene, placed: Layout, width: Width): Node[] {
	const { doc, layout, triggers, hidden } = sc;
	return triggers.flatMap((ph) => {
		const own = doc.stages.filter((s) => s.phase === ph.id);
		const last = own.at(-1);
		const from = last
			? { x: placed[last.id].x + width(last.id) + 48, y: placed[last.id].y }
			: (() => {
					const m = layout[markerKey(ph)] ?? { x: -220, y: 0 };
					return { x: m.x + width(`__trigger:${ph.id}`, 200) + 48, y: m.y };
				})();
		return chainEnds(doc, ph).map((to: Terminal, k) => ({
			id: `__${to}:${ph.id}`,
			type: to,
			position: layout[terminalKey(to, ph)] ?? { x: from.x, y: from.y + k * 58 },
			hidden: hidden(ph.id),
			data: {
				band: ph.id,
				title: t(to === END ? 'flow.term.endBranch' : `flow.term.${to}`)
			}
		}));
	});
}

// The debug leaf is no terminal: it parks the run and comes back to the stage
// that sent it there. So it is drawn once PER STAGE that escalates to it, beside
// that stage, with its return edge — a shared sink hid the one fact that matters.
function debugNodes({ doc, layout, bandOf, hidden }: Scene): Node[] {
	return doc.stages.filter(parksOnDebug).map((s) => {
		const at = layout[s.id] ?? { x: 0, y: 0 };
		return {
			id: debugNodeID(s.id),
			type: 'debug',
			position: layout[debugKey(s.id)] ?? { x: at.x + 24, y: at.y + 170 },
			hidden: hidden(s.phase),
			data: {
				stage: s.id,
				band: bandOf(s.phase),
				title: t('flow.term.debugFor', { stage: s.id })
			}
		};
	});
}

/** Everything that is not a stage. */
export function chromeNodes(sc: Scene, placed: Layout, width: Width): Node[] {
	return [
		...triggerNodes(sc),
		...startNodes(sc),
		...sectionNodes(sc),
		...hubNodes(sc),
		...exitNodes(sc),
		...sharedEndNodes(sc),
		...chainEndNodes(sc, placed, width),
		...debugNodes(sc)
	];
}
