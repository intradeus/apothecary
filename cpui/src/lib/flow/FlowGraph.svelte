<script lang="ts" module>
	import type { Selection as GraphSelection } from '$lib/flow/graph/selection';
	import type { SequenceView as GraphSequenceView } from '$lib/flow/graph/sequence';

	// Declared here, not re-exported: `export type { … }` trips eslint's Svelte parser.
	export type Selection = GraphSelection;
	export type SequenceView = GraphSequenceView;
</script>

<script lang="ts">
	// The Svelte Flow canvas. What it draws is built from the document in
	// graph/ (nodes, edges, frames); this component keeps what lives between
	// builds — what the last one drew, the ghosts leaving, the folds, a chain's
	// drag — and the look of Svelte Flow's own classes.
	import {
		SvelteFlow,
		Background,
		BackgroundVariant,
		Controls,
		ControlButton,
		type Node,
		type Edge,
		type Connection
	} from '@xyflow/svelte';
	import '@xyflow/svelte/dist/style.css';
	import { prefs } from '$lib/app/prefs.svelte';
	import { untrack } from 'svelte';
	import FlowStageNode from '$lib/flow/FlowStageNode.svelte';
	import FlowTerminalNode from '$lib/flow/FlowTerminalNode.svelte';
	import FlowSectionNode from '$lib/flow/FlowSectionNode.svelte';
	import FlowHubNode from '$lib/flow/FlowHubNode.svelte';
	import FitOnInit from '$lib/flow/FitOnInit.svelte';
	import FlowLabeledEdge from '$lib/flow/FlowLabeledEdge.svelte';
	import GraphFrames from '$lib/flow/graph/GraphFrames.svelte';
	import { startChainDrag, dragChain, type ChainDrag } from '$lib/flow/graph/chainDrag';
	import { buildEdges } from '$lib/flow/graph/edges';
	import { chainFrames } from '$lib/flow/graph/frames';
	import { edgeSelection, linkOf, nodeKey, nodeSelection } from '$lib/flow/graph/ids';
	import { arrivalsOf, chromeNodes, ghostOf, stageNodes, widthIn } from '$lib/flow/graph/nodes';
	import { sceneOf } from '$lib/flow/graph/scene';
	import { ms } from '$lib/ui/motion';
	import { t } from '$lib/i18n/index.svelte';
	import { token } from '$lib/design/tokens';
	import type { ActionContract, AgentContract, FlowDoc, NodeSize } from '$lib/domain/flowdoc';

	let {
		doc,
		layout,
		rev,
		onselect,
		onmove,
		onlink,
		onarrange,
		onmeasured,
		agents = [],
		actions = [],
		sequence,
		readonly = false,
		refit = 0,
		legible
	}: {
		doc: FlowDoc;
		layout: Record<string, { x: number; y: number }>;
		rev: number;
		onselect: (sel: Selection) => void;
		onmove: (id: string, x: number, y: number) => void;
		onlink: (source: string, handle: string, target: string) => void;
		/** Re-run the success-chain layout (the controls' auto-arrange button),
		 *  given every node's rendered size so nothing overlaps. */
		onarrange?: (sizes: Record<string, NodeSize>) => void;
		/** The nodes have been measured for the first time: a layout guessed
		 *  without sizes can now be redone with them. */
		onmeasured?: (sizes: Record<string, NodeSize>) => void;
		// Contracts the dataflow layer reads: which inputs/outputs a stage's agent
		// or action declares. Optional — without them only explicit bindings show.
		agents?: AgentContract[];
		actions?: ActionContract[];
		/** Draw a sequence instead of a flow: its chain from a marker named
		 *  after it to its two exits, never to the run's ends. */
		sequence?: SequenceView;
		/** Nothing to rewire: handles do not connect. */
		readonly?: boolean;
		/** Each change fits the view again, animated (the canvas resized). */
		refit?: number;
		/** The smallest zoom a fit may land on before it anchors on the
		 *  entry instead (FitOnInit's default when unset). */
		legible?: number;
	} = $props();

	const nodeTypes = {
		stage: FlowStageNode,
		end: FlowTerminalNode,
		done: FlowTerminalNode,
		abort: FlowTerminalNode,
		debug: FlowTerminalNode,
		start: FlowTerminalNode,
		exit: FlowTerminalNode,
		failexit: FlowTerminalNode,
		section: FlowSectionNode,
		hub: FlowHubNode
	};
	// Control edges carry their label by the source (FlowLabeledEdge), so
	// labels never land on nodes or on each other.
	const edgeTypes = { labeled: FlowLabeledEdge };

	let nodes = $state.raw<Node[]>([]);
	let edges = $state.raw<Edge[]>([]);
	// The watchers and outcomes folded to their marker (this view only).
	let collapsed = $state(new Set<string>());

	// What the last build drew, so a rebuild can tell what just arrived (it
	// materialises) from what just left (a ghost dematerialises, then goes).
	let known: Set<string> | null = null;
	const ghosts = new Map<string, Node>();

	function rebuild() {
		const ids = new Set(doc.stages.map((s) => s.id));
		// Untracked: Svelte Flow writes `nodes` on every drag frame, and a drag
		// must never rebuild the graph.
		const prevAll = untrack(() => nodes);
		const arrivals = arrivalsOf(doc, ids, prevAll, known);
		const { renamed } = arrivals;
		if (renamed && !layout[renamed.to]) {
			const { x, y } = renamed.from.position;
			queueMicrotask(() => onmove(renamed.to, x, y));
		}
		if (!renamed) {
			for (const n of arrivals.removed) {
				ghosts.set(n.id, ghostOf(n));
				setTimeout(
					() => {
						if (ghosts.delete(n.id)) rebuild();
					},
					ms('--dur-spring', 450)
				);
			}
		}
		known = ids;
		const scene = sceneOf({ doc, layout, agents, actions, sequence, collapsed });
		const width = widthIn(prevAll);
		const { nodes: ns, placed } = stageNodes(scene, arrivals, width);
		for (const [id, g] of ghosts) {
			if (ids.has(id)) ghosts.delete(id);
			else ns.push(g);
		}
		ns.push(...chromeNodes(scene, placed, width));
		nodes = ns;
		edges = buildEdges(scene, (sel) => onselect(sel));
	}
	// Build once before SvelteFlow mounts, so its initial fitView sees the
	// nodes (it fits on init only); the effect keeps them in step afterwards.
	untrack(rebuild);
	$effect(() => {
		void rev;
		rebuild();
	});

	function handleConnect(conn: Connection) {
		if (readonly) return;
		const link = linkOf(conn);
		if (link) onlink(link.source, link.handle, link.target);
	}

	// The canvas grid, from the flow tokens (Svelte Flow takes a number).
	const gridGap = parseFloat(token('--grid-gap')) || 20;

	// measuredSizes reports every node's rendered size, keyed like `layout`, so
	// autoLayout can give each column and row the room its nodes take.
	function measuredSizes(): Record<string, NodeSize> {
		const out: Record<string, NodeSize> = {};
		for (const n of nodes) {
			const { width, height } = n.measured ?? {};
			if (width && height) out[nodeKey(n.id)] = { width, height };
		}
		return out;
	}

	const frames = $derived(sequence ? [] : chainFrames(doc, nodes, collapsed));
	function fold(id: string) {
		const next = new Set(collapsed);
		if (!next.delete(id)) next.add(id);
		collapsed = next;
		rebuild();
	}

	// A chain's marker being dragged, its members saved where they land.
	let group: ChainDrag | null = null;
	function dragStart(n: Node | null) {
		group = startChainDrag(n, nodes);
	}
	function dragMove(n: Node | null) {
		const g = group;
		if (!g || !n || n.id !== g.anchor) return;
		nodes = dragChain(g, n, nodes);
	}
	function dragStop(n: Node | null) {
		if (n) onmove(nodeKey(n.id), n.position.x, n.position.y);
		const g = group;
		group = null;
		if (!g) return;
		for (const m of nodes)
			if (g.members.has(m.id)) onmove(nodeKey(m.id), m.position.x, m.position.y);
	}
</script>

<div class="graph-wrap">
	<SvelteFlow
		bind:nodes
		bind:edges
		{nodeTypes}
		{edgeTypes}
		colorMode={prefs.themeDef.scheme}
		fitView
		fitViewOptions={{ padding: 0.12, maxZoom: 1.1 }}
		nodesConnectable={!readonly}
		minZoom={0.15}
		deleteKey={null}
		onconnect={handleConnect}
		onnodedragstart={({ targetNode }) => dragStart(targetNode)}
		onnodedrag={({ targetNode }) => dragMove(targetNode)}
		onnodedragstop={({ targetNode }) => dragStop(targetNode)}
		onnodeclick={({ node }) => onselect(nodeSelection(doc, node.id))}
		onedgeclick={({ edge }) => {
			const sel = edgeSelection(doc, edge.id);
			if (sel !== undefined) onselect(sel);
		}}
		onpaneclick={() => onselect(null)}
	>
		<FitOnInit {refit} {legible} onmeasured={() => onmeasured?.(measuredSizes())} />
		<GraphFrames {frames} onfold={fold} />
		<Background variant={BackgroundVariant.Dots} gap={gridGap} size={1.2} />
		<Controls position="bottom-left" class="island fg-ctl">
			{#snippet after()}
				{#if onarrange}
					<ControlButton
						onclick={() => onarrange?.(measuredSizes())}
						title={t('flow.graph.arrangeTitle')}
						aria-label={t('flow.graph.arrangeAria')}>⇶</ControlButton
					>
				{/if}
			{/snippet}
		</Controls>
	</SvelteFlow>
</div>

<style>
	.graph-wrap {
		height: 100%;
		min-height: 420px;
		position: relative;
	}
	/* Svelte Flow themed from the tokens, in one place: every --xy-* variable
	 * it reads maps to ours, so its own rules draw our look and only what it
	 * has no variable for (sizes, the label pill, edge kinds) is styled below. */
	.graph-wrap :global(.svelte-flow) {
		--xy-background-color: transparent;
		--xy-background-pattern-color: var(--line);
		--xy-edge-stroke: var(--line-strong);
		--xy-edge-stroke-width: var(--edge-w);
		--xy-edge-stroke-selected: var(--brand);
		--xy-edge-label-background-color: var(--island-bg);
		--xy-edge-label-color: var(--text);
		--xy-connectionline-stroke: var(--brand);
		--xy-connectionline-stroke-width: var(--edge-w);
		--xy-node-color: var(--text);
		--xy-node-background-color: var(--glass-bg);
		--xy-node-border: 1px solid var(--glass-line);
		--xy-node-border-radius: var(--r-card);
		--xy-node-group-background-color: var(--fill-1);
		--xy-node-boxshadow-hover: none;
		--xy-node-boxshadow-selected: none;
		--xy-handle-background-color: var(--text-muted);
		--xy-handle-border-color: var(--frame);
		--xy-selection-background-color: var(--brand-bg);
		--xy-selection-border: 1px dashed var(--brand-line);
		--xy-resize-background-color: var(--brand);
		--xy-controls-button-background-color: transparent;
		--xy-controls-button-background-color-hover: var(--fill-3);
		--xy-controls-button-color: var(--text-muted);
		--xy-controls-button-color-hover: var(--text);
		--xy-controls-button-border-color: var(--line);
		--xy-controls-box-shadow: var(--specular), var(--shadow-pop);
		--xy-minimap-background-color: var(--island-bg);
		--xy-minimap-mask-background-color: var(--scrim);
		--xy-minimap-mask-stroke-color: var(--line-strong);
		--xy-minimap-mask-stroke-width: 1;
		--xy-minimap-node-background-color: var(--fill-3);
		--xy-minimap-node-stroke-color: var(--line);
		--xy-minimap-node-stroke-width: 1;
		--xy-attribution-background-color: transparent;
		font-family: var(--font-body);
	}

	/* Edges (DESIGN 3.6). Each carries its hue as --eh; width, dash and the
	 * fallback fade come from the flow tokens. Hover and selection thicken
	 * the stroke on the spring (interruptible: it retargets mid-way). */
	.graph-wrap :global(.svelte-flow__edge .svelte-flow__edge-path) {
		stroke: var(--eh, var(--xy-edge-stroke));
		transition:
			stroke-width var(--dur-spring) var(--spring),
			filter var(--dur-fade) var(--ease-out);
	}
	.graph-wrap :global(.e-success .svelte-flow__edge-path) {
		stroke-width: var(--edge-w-strong);
	}
	.graph-wrap :global(.e-dash .svelte-flow__edge-path) {
		stroke-dasharray: var(--edge-dash);
	}
	.graph-wrap :global(.e-then .svelte-flow__edge-path) {
		opacity: var(--edge-fallback-o);
	}
	.graph-wrap :global(.e-data .svelte-flow__edge-path) {
		stroke: var(--line-strong);
		stroke-dasharray: var(--edge-dash-data);
		stroke-linecap: round;
	}
	.graph-wrap :global(.svelte-flow__edge.selected .svelte-flow__edge-path),
	.graph-wrap :global(.svelte-flow__edge.selectable:hover .svelte-flow__edge-path) {
		stroke-width: var(--edge-w-hover);
		filter: drop-shadow(0 0 var(--glow-xs) var(--eh, var(--brand)));
	}
	/* Edge labels: "fail ×3" as a .pill.sm.mono in the edge's hue. It is a
	 * <button> (app button styles skip .svelte-flow), so it drops the UA look
	 * here. FlowLabeledEdge centres a transparent box at its anchor and the
	 * pill inside shifts back by half its own width, so the label starts at
	 * the anchor. */
	.graph-wrap :global(.e-lbl-pill) {
		appearance: none;
		margin: 0;
		cursor: pointer;
		transform: translateX(50%);
		background: var(--xy-edge-label-background-color);
		border-color: var(--eh-ln, var(--line-strong));
		color: var(--eh, var(--text));
	}

	/* Handles: dots in their edge's hue (--xy-handle-background-color, set
	 * per handle), sized by --handle (data dots shrink it through --hs). */
	.graph-wrap :global(.svelte-flow__handle) {
		width: var(--hs, var(--handle));
		height: var(--hs, var(--handle));
		min-width: var(--hs, var(--handle));
		min-height: var(--hs, var(--handle));
		transition: box-shadow var(--dur-fade) var(--ease-out);
	}
	.graph-wrap :global(.svelte-flow__handle.connectingto),
	.graph-wrap :global(.svelte-flow__handle.connectable:hover) {
		box-shadow: 0 0 0 3px var(--brand-bg);
	}

	/* Controls: an island stack (.island) with rounded corners. */
	.graph-wrap :global(.fg-ctl) {
		border-radius: var(--r-btn);
		overflow: hidden;
	}
	.graph-wrap :global(.svelte-flow__attribution a) {
		color: var(--text-faint);
	}
	/* Marching dashes stop for reduced motion; the dash itself stays. */
	@media (prefers-reduced-motion: reduce) {
		.graph-wrap :global(.svelte-flow__edge.animated path) {
			animation: none;
		}
	}
</style>
