// A connection dragged on the canvas, applied to the document.
import {
	ABORT,
	chainLinks,
	DEBUG,
	DONE,
	edgeOf,
	setEdge,
	setEntry,
	START,
	type EdgeKind,
	type FlowDoc
} from '$lib/domain/flowdoc';

/** What a drop did: changed the document, did nothing, or crossed phases
 *  (`from` and `to` the stages). */
export type Rewired = 'changed' | 'same' | { from: string; to: string };

/** rewire applies a dragged connection in place. */
export function rewire(doc: FlowDoc, source: string, handle: string, target: string): Rewired {
	if (source === START) {
		// Dragging the entry marker onto a stage makes it the entry (moves
		// it to document front); terminals can't be an entry.
		return target !== DONE && target !== ABORT && target !== DEBUG && setEntry(doc, target)
			? 'changed'
			: 'same';
	}
	const s = doc.stages.find((x) => x.id === source);
	if (!s) return 'same';
	// A phase's routes stay inside it (the parser refuses the rest): a drop
	// onto another phase's stage is explained, not applied.
	const dest = doc.stages.find((x) => x.id === target);
	if (dest && (dest.phase ?? '') !== (s.phase ?? '')) return { from: s.id, to: dest.id };
	if (handle === 'success') {
		// success never aborts or parks
		if (target !== s.id && target !== ABORT && target !== DEBUG) s.on_success = target;
	} else {
		const kind = handle as EdgeKind;
		if (target === DONE) return 'same'; // recoverable edges point at stages or abort, not done
		const existing = edgeOf(s, kind);
		if (target === ABORT) {
			// Dragging onto the abort terminal replaces the chain's first link
			// with the immediate stop (no max; nothing runs after abort).
			setEdge(s, kind, { to: ABORT });
		} else if (target === DEBUG) {
			// Dragging onto the debug leaf almost always means "and then a
			// human": it is appended after an existing finite chain. An empty
			// or unlimited chain becomes the bare leaf.
			const links = chainLinks(existing);
			const last = links[links.length - 1];
			if (last && last.to !== ABORT && last.to !== DEBUG && last.max !== -1)
				last.then = { to: DEBUG };
			else setEdge(s, kind, { to: DEBUG });
		} else {
			setEdge(s, kind, { to: target, max: existing?.max ?? 3, then: existing?.then });
		}
	}
	return 'changed';
}
