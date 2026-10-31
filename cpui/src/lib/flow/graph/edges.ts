// The canvas's edges (DESIGN 3.6): success solid green; failure edges dashed and
// animated in their hue; a "then" fallback is the same hue faded; dataflow is
// faint and dotted. The look lives in FlowGraph's CSS classes, sized by the flow
// tokens (--edge-w…); an edge only carries its hue (--eh, from hues.ts) so hover
// and selection can transition it.
import type { Edge } from '@xyflow/svelte';
import { EDGE_HUE } from '$lib/design/hues';
import { I } from '$lib/design/icons';
import { t } from '$lib/i18n/index.svelte';
import {
	ABORT,
	DEBUG,
	deriveDataLinks,
	DONE,
	edgeLinks,
	effectiveSuccess,
	END,
	FAIL,
	feedDots,
	feedSourceFor,
	givesVerdict,
	parksOnDebug,
	phaseEntry,
	SOURCE_VERDICT
} from '$lib/domain/flowdoc';
import { debugNodeID, EXIT } from '$lib/flow/graph/ids';
import { chainHue, type Scene } from '$lib/flow/graph/scene';
import type { Selection } from '$lib/flow/graph/selection';

const hued = (h: string) => `--eh: var(--${h}); --eh-ln: var(--${h}-line)`;
// "on_fail" reads "fail" on the canvas.
const word = (kind: string) => kind.replace(/^on_/, '');

// Dataflow, derived from the artifact paths stages read and write: drawn behind
// the control edges, never selectable, never draggable. The YAML's paths ARE the
// wiring; this only shows what they already say. Within a chain only: a watcher
// reading what the main chain wrote shows it on its port, not as a line across
// the canvas.
function dataEdges({ doc, agents, actions }: Scene): Edge[] {
	const chainOf = new Map(doc.stages.map((s) => [s.id, s.phase ?? '']));
	return deriveDataLinks(doc, agents, actions)
		.filter((l) => chainOf.get(l.source) === chainOf.get(l.target))
		.map((l) => ({
			id: `data|${l.source}|${l.target}|${l.value}`,
			source: l.source,
			sourceHandle: l.sourceHandle,
			target: l.target,
			targetHandle: l.targetHandle,
			type: 'smoothstep',
			selectable: false,
			focusable: false,
			deletable: false,
			zIndex: -1,
			class: 'e-data'
		}));
}

function entryEdges({ doc, sequence, mainPhase, watchesOnly }: Scene): Edge[] {
	if (watchesOnly)
		return [
			{
				id: 'entry|__start',
				source: '__start',
				target: '__hub',
				type: 'smoothstep',
				selectable: false,
				style: hued('brand')
			}
		];
	if (doc.stages.length && (sequence || mainPhase || !doc.phases?.length))
		return [
			{
				id: 'entry|__start',
				source: '__start',
				target: doc.stages[0].id,
				type: 'smoothstep',
				style: hued('brand')
			}
		];
	return [];
}

// Each watcher and outcome branches from the hub — one line in the hub's hue
// down the marker column; each chain wears its own after its marker.
function branchEdges({ doc, hub, triggers, termNode }: Scene): Edge[] {
	const es: Edge[] = [];
	for (const ph of triggers) {
		if (hub)
			es.push({
				id: `hub|${ph.id}`,
				source: '__hub',
				target: `__trigger:${ph.id}`,
				targetHandle: 'hub',
				type: 'smoothstep',
				class: 'e-dash',
				style: hued('accent-2')
			});
		const first = phaseEntry(doc, ph.id);
		// A stage-less outcome's marker points straight at its end.
		const target = first ? first.id : termNode(ph.id, ph.then ?? DONE);
		es.push({
			id: `entry|__trigger:${ph.id}`,
			source: `__trigger:${ph.id}`,
			target,
			type: 'smoothstep',
			class: 'e-dash',
			style: hued(chainHue(ph))
		});
	}
	return es;
}

function stageEdges(sc: Scene, pick: (sel: Selection) => void): Edge[] {
	const { doc, agents, sequence, termNode } = sc;
	const fed = feedDots(doc);
	// Where a stage's success leads. In a sequence, a stage without one goes on
	// to the next, and the last leaves the sequence (EXIT) — not the phase.
	const successOf = (i: number) =>
		sequence
			? (doc.stages[i].on_success ?? doc.stages[i + 1]?.id ?? EXIT)
			: effectiveSuccess(doc, i);
	const es: Edge[] = [];
	doc.stages.forEach((s, i) => {
		const target = successOf(i);
		// A stage whose success is a verdict carries it on: the success
		// edge lands on the target's edge.verdict dot, when it has one.
		const verdictDot = givesVerdict(s, agents) ? fed.get(`${target}|${SOURCE_VERDICT}`) : undefined;
		es.push({
			id: `success|${s.id}`,
			source: s.id,
			sourceHandle: 'success',
			target:
				target === END || target === DONE || target === ABORT ? termNode(s.phase, target) : target,
			targetHandle: verdictDot ? `in:${verdictDot}` : undefined,
			type: 'smoothstep',
			class: 'e-success',
			style: hued(EDGE_HUE.success)
		});
		// One rendered edge per chain link; deeper links are the "then"
		// fallbacks and label their position in the chain. The link's index
		// in edgeLinks is its label's row under the stage.
		edgeLinks(s).forEach(({ kind, link, depth }, row) => {
			// The edge lands on the exact dot that receives its content, when the
			// target bound one: the picture shows where the rationale goes.
			const dot = fed.get(`${link.to}|${feedSourceFor(kind)}`);
			es.push({
				id: `${kind}|${s.id}|${depth}`,
				source: s.id,
				sourceHandle: kind,
				target:
					link.to === FAIL
						? '__fail'
						: link.to === DEBUG
							? debugNodeID(s.id)
							: link.to === END || link.to === DONE || link.to === ABORT
								? termNode(s.phase, link.to)
								: link.to,
				targetHandle: dot ? `in:${dot}` : undefined,
				type: 'labeled',
				data: { row, pick: () => pick({ kind: 'edge', stage: s.id, edge: kind }) },
				// "fail ×3": the word as well as the hue, so colour is never the
				// only signal; "then#1" marks a fallback link. A debug link
				// names no target: its leaf sits right under the stage.
				label:
					link.to === DEBUG
						? `${word(kind)}${depth > 0 ? ` · then#${depth}` : ''}`
						: link.to === ABORT || link.to === END || link.to === DONE || link.to === FAIL
							? `${word(kind)}${depth > 0 ? ` · then#${depth}` : ''} ${I.forward} ${link.to}`
							: `${word(kind)}${depth > 0 ? ` · then#${depth}` : ''} ×${link.max === -1 ? '∞' : (link.max ?? '?')}`,
				labelStyle: hued(EDGE_HUE[kind]),
				animated: true,
				class: depth > 0 ? 'e-dash e-then' : 'e-dash',
				style: hued(EDGE_HUE[kind])
			});
		});
		// The way back from the stage's debug leaf: the human has fixed the
		// workspace, and the same stage runs again.
		if (parksOnDebug(s)) {
			es.push({
				id: `debugreturn|${s.id}`,
				source: debugNodeID(s.id),
				target: s.id,
				type: 'labeled',
				data: { anchor: 'right', pick: () => pick({ kind: 'stage', id: s.id }) },
				label: `${I.retry} ${t('flow.graph.debugReturn')}`,
				labelStyle: hued('warning'),
				class: 'e-dash e-then',
				style: hued('warning')
			});
		}
	});
	return es;
}

/** Every edge, the dataflow first (it draws behind). `pick` is what a click on
 *  an edge's label selects. */
export const buildEdges = (sc: Scene, pick: (sel: Selection) => void): Edge[] => [
	...dataEdges(sc),
	...entryEdges(sc),
	...branchEdges(sc),
	...stageEdges(sc, pick)
];
