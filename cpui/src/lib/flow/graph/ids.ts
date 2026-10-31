// How the canvas names what it draws: the ids of its nodes and edges, the
// layout keys they save under, and what a click or a drag on one of them means.
import type { Connection } from '@xyflow/svelte';
import {
	ABORT,
	DEBUG,
	debugKey,
	DONE,
	END,
	HUB,
	MAIN,
	phaseEntry,
	START,
	type FlowDoc
} from '$lib/domain/flowdoc';
import type { Selection } from '$lib/flow/graph/selection';

/** Where a sequence's last stage goes: out of the sequence, not the phase. */
export const EXIT = '__exit';

/** A stage's debug leaf on the canvas. */
export const debugNodeID = (stage: string) => `__debug:${stage}`;

/** A chain's marker: dragging it takes the chain along. */
export const isMarker = (id: string) => id === '__start' || id.startsWith('__trigger:');

/** The key a node's position is saved under in the layout. */
export const nodeKey = (id: string) =>
	id === '__end'
		? END
		: id === '__done'
			? DONE
			: id === '__abort'
				? ABORT
				: id.startsWith('__debug:')
					? debugKey(id.slice('__debug:'.length))
					: id === '__start'
						? START
						: id === '__hub'
							? HUB
							: /^__(trigger|section|end|done|abort):/.test(id)
								? id.slice(2)
								: id;

/** What a finished connection asks of the document, or null when it asks
 *  nothing: the arguments of FlowGraph's onlink. */
export function linkOf(
	conn: Connection
): { source: string; handle: string; target: string } | null {
	if (!conn.source || !conn.target) return null;
	// A debug leaf's own handle carries the drawn return edge only.
	if (conn.source.startsWith('__debug:')) return null;
	// Dataflow dots are not rewireable: an artifact path is what connects two
	// stages, so a drag from one must not be read as a control-flow rewire
	// (setEdge's fallthrough would silently overwrite on_timeout).
	const h = conn.sourceHandle ?? '';
	if (h.startsWith('in:') || h.startsWith('out:')) return null;
	// A terminal — shared, a chain's own, or the hub (the main chain's end).
	const end = /^__(end|done|abort)(:|$)/.exec(conn.target)?.[1];
	const target =
		conn.target === '__hub'
			? END
			: end
				? end
				: conn.target.startsWith('__debug:')
					? DEBUG
					: conn.target;
	if (isMarker(conn.source)) return { source: START, handle: 'entry', target }; // re-point the (phase's) entry
	return { source: conn.source, handle: conn.sourceHandle ?? 'success', target };
}

/** What a click on a node selects. */
export function nodeSelection(doc: FlowDoc, id: string): Selection {
	// A watcher's or an outcome's own end opens its panel.
	const own = /^__(?:end|done|abort):(.+)$/.exec(id)?.[1];
	return id.startsWith('__debug:')
		? { kind: 'stage', id: id.slice('__debug:'.length) }
		: id.startsWith('__trigger:')
			? { kind: 'phase', id: id.slice('__trigger:'.length) }
			: own
				? { kind: 'phase', id: own }
				: id === '__start' && doc.phases?.some((p) => p.kind === 'main')
					? { kind: 'phase', id: MAIN }
					: id.startsWith('__')
						? null
						: { kind: 'stage', id };
}

/** What a click on an edge selects; undefined leaves the selection alone. */
export function edgeSelection(doc: FlowDoc, id: string): Selection | undefined {
	const [kind, stage] = id.split('|');
	// A data edge has nothing of its own to edit — select its producer, which
	// is where the path it carries is written.
	if (kind === 'data' || kind === 'debugreturn') return { kind: 'stage', id: stage };
	// The hub's branch to a watcher or an outcome opens its panel.
	if (kind === 'hub') return { kind: 'phase', id: stage };
	if (kind === 'entry') {
		if (stage === '__start' && !doc.stages.length) return undefined;
		const first = stage.startsWith('__trigger:')
			? phaseEntry(doc, stage.slice('__trigger:'.length))
			: doc.stages[0];
		return first ? { kind: 'stage', id: first.id } : null;
	}
	return kind === 'success' ? { kind: 'stage', id: stage } : { kind: 'edge', stage, edge: kind };
}
