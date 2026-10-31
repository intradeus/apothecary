<script lang="ts">
	// Svelte Flow without its viewport: its children, then each node drawn by
	// its real component, each edge a button.
	import type { Snippet } from 'svelte';
	import type { Edge, Node } from '@xyflow/svelte';
	import { canvas } from './canvas';

	type Props = Record<string, unknown> & {
		nodes: Node[];
		edges: Edge[];
		children?: Snippet;
		onnodeclick?: (e: { node: Node }) => void;
		onedgeclick?: (e: { edge: Edge }) => void;
		onpaneclick?: () => void;
	};
	let { nodes = $bindable(), edges = $bindable(), children, ...props }: Props = $props();

	$effect.pre(() => {
		canvas.props = props;
		canvas.nodes = nodes;
		canvas.edges = edges;
		canvas.setNodes = (ns) => (nodes = ns);
	});

	const Of = (n: Node) => canvas.props.nodeTypes?.[n.type ?? ''];
</script>

{@render children?.()}
<button onclick={() => props.onpaneclick?.()}>pane</button>
{#each nodes.filter((n) => !n.hidden) as n (n.id)}
	{@const Drawn = Of(n)}
	<div data-testid="node" data-node={n.id}>
		<button aria-label="node {n.id}" onclick={() => props.onnodeclick?.({ node: n })}></button>
		<Drawn id={n.id} type={n.type} data={n.data} selected={false} />
	</div>
{/each}
{#each edges as e (e.id)}
	<button aria-label="edge {e.id}" onclick={() => props.onedgeclick?.({ edge: e })}></button>
{/each}
