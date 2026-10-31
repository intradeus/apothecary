// Frames: each watcher and outcome, a sub-run of its own. A frame wraps a
// watcher's or an outcome's chain — its marker, stages, debug leaves and ends —
// drawn behind the nodes from where they stand now, so it follows a drag.
import type { Node } from '@xyflow/svelte';
import { LABEL_PAD, LABEL_ROW, type FlowDoc } from '$lib/domain/flowdoc';
import { chainHue } from '$lib/flow/graph/scene';

const FRAME_PAD = 16;

export interface Frame {
	id: string;
	hue: string;
	x: number;
	y: number;
	w: number;
	h: number;
	folded: boolean;
}

/** The chains' frames, around the nodes as they stand. */
export function chainFrames(doc: FlowDoc, nodes: Node[], collapsed: Set<string>): Frame[] {
	const kinds = new Map((doc.phases ?? []).map((p) => [p.id, p]));
	const boxes = new Map<string, { x0: number; y0: number; x1: number; y1: number }>();
	for (const n of nodes) {
		const ph = kinds.get(n.data?.band as string);
		if (!ph || ph.kind === 'main' || n.hidden || n.data?.leaving) continue;
		const w = n.measured?.width ?? (n.type === 'stage' ? 220 : 120);
		const h =
			(n.measured?.height ?? (n.type === 'stage' ? 120 : 44)) +
			(Number(n.data?.labels) ? LABEL_PAD + Number(n.data.labels) * LABEL_ROW : 0);
		const b = boxes.get(ph.id) ?? { x0: Infinity, y0: Infinity, x1: -Infinity, y1: -Infinity };
		b.x0 = Math.min(b.x0, n.position.x);
		b.y0 = Math.min(b.y0, n.position.y);
		b.x1 = Math.max(b.x1, n.position.x + w);
		b.y1 = Math.max(b.y1, n.position.y + h);
		boxes.set(ph.id, b);
	}
	// In the document's order: folding one never reorders the others.
	return (doc.phases ?? []).flatMap((ph) => {
		const b = boxes.get(ph.id);
		if (!b) return [];
		const id = ph.id;
		return {
			id,
			hue: chainHue(ph),
			x: b.x0 - FRAME_PAD,
			y: b.y0 - FRAME_PAD,
			w: b.x1 - b.x0 + 2 * FRAME_PAD,
			h: b.y1 - b.y0 + 2 * FRAME_PAD,
			folded: collapsed.has(id)
		};
	});
}
