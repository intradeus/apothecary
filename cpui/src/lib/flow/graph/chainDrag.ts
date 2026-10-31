// Dragging a chain's marker takes the chain with it: its stages, leaves and
// ends move by the same amount.
import type { Node } from '@xyflow/svelte';
import { isMarker } from '$lib/flow/graph/ids';

export interface ChainDrag {
	anchor: string;
	from: { x: number; y: number };
	members: Map<string, { x: number; y: number }>;
}

/** The drag a node starts, when it is a chain's marker. */
export function startChainDrag(n: Node | null, nodes: Node[]): ChainDrag | null {
	if (!n || !isMarker(n.id) || !n.data?.band) return null;
	const band = n.data.band;
	return {
		anchor: n.id,
		from: { ...n.position },
		members: new Map(
			nodes
				.filter((m) => m.id !== n.id && m.data?.band === band && !m.data?.leaving)
				.map((m) => [m.id, { ...m.position }])
		)
	};
}

/** The nodes with the chain moved along with its marker `n`. */
export function dragChain(g: ChainDrag, n: Node, nodes: Node[]): Node[] {
	const dx = n.position.x - g.from.x;
	const dy = n.position.y - g.from.y;
	return nodes.map((m) => {
		const p = g.members.get(m.id);
		return p ? { ...m, position: { x: p.x + dx, y: p.y + dy } } : m;
	});
}
