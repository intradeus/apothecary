import type { Component } from 'svelte';
import type { Edge, Node } from '@xyflow/svelte';

/** What the stand-in canvas was last given, and its way back: `nodes` writes
 *  through `bind:nodes`, as Svelte Flow does on a drag. */
export const canvas = {
	props: {} as Record<string, unknown> & {
		nodeTypes?: Record<string, Component>;
		edgeTypes?: Record<string, Component>;
		nodesConnectable?: boolean;
		onconnect?: (c: Record<string, string | null>) => void;
		onnodedragstart?: (e: { targetNode: Node | null }) => void;
		onnodedrag?: (e: { targetNode: Node | null }) => void;
		onnodedragstop?: (e: { targetNode: Node | null }) => void;
	},
	nodes: [] as Node[],
	edges: [] as Edge[],
	setNodes: (_: Node[]) => {}
};
