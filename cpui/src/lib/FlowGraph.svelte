<script lang="ts">
	import {
		SvelteFlow,
		Background,
		Controls,
		type Node,
		type Edge,
		type Connection
	} from '@xyflow/svelte';
	import '@xyflow/svelte/dist/style.css';
	import FlowStageNode from '$lib/FlowStageNode.svelte';
	import FlowDoneNode from '$lib/FlowDoneNode.svelte';
	import FlowAbortNode from '$lib/FlowAbortNode.svelte';
	import FlowStartNode from '$lib/FlowStartNode.svelte';
	import {
		ABORT,
		DONE,
		START,
		EDGE_KINDS,
		chainLinks,
		deriveDataLinks,
		effectiveSuccess,
		edgeOf,
		feedDots,
		feedSentinelFor,
		type ActionContract,
		type AgentContract,
		type FlowDoc
	} from '$lib/flowdoc';

	export type Selection =
		{ kind: 'stage'; id: string } | { kind: 'edge'; stage: string; edge: string } | null;

	let {
		doc,
		layout,
		rev,
		onselect,
		onmove,
		onlink,
		agents = [],
		actions = []
	}: {
		doc: FlowDoc;
		layout: Record<string, { x: number; y: number }>;
		rev: number;
		onselect: (sel: Selection) => void;
		onmove: (id: string, x: number, y: number) => void;
		onlink: (source: string, handle: string, target: string) => void;
		// Contracts the dataflow layer reads: which inputs/outputs a stage's agent
		// or action declares. Optional — without them only explicit bindings show.
		agents?: AgentContract[];
		actions?: ActionContract[];
	} = $props();

	const nodeTypes = {
		stage: FlowStageNode,
		done: FlowDoneNode,
		abort: FlowAbortNode,
		start: FlowStartNode
	};

	const edgeStyles: Record<string, string> = {
		on_fail: 'stroke:#e06c75; stroke-dasharray:6 3',
		on_reject: 'stroke:#e0af68; stroke-dasharray:6 3',
		on_timeout: 'stroke:#7dcfff; stroke-dasharray:6 3'
	};

	let nodes = $state.raw<Node[]>([]);
	let edges = $state.raw<Edge[]>([]);

	function rebuild() {
		const ns: Node[] = doc.stages.map((s, i) => ({
			id: s.id,
			type: 'stage',
			position: layout[s.id] ?? { x: i * 240, y: 0 },
			data: { stage: s, entry: i === 0, agents, actions }
		}));
		// The entry marker: its edge points at stages[0]; dragging that edge
		// onto another stage makes it the entry (moves it to document front).
		const entryPos = doc.stages.length
			? (layout[doc.stages[0].id] ?? { x: 0, y: 0 })
			: { x: 0, y: 0 };
		ns.push({
			id: '__start',
			type: 'start',
			position: layout[START] ?? { x: entryPos.x - 170, y: entryPos.y },
			data: {}
		});
		ns.push({
			id: '__done',
			type: 'done',
			position: layout[DONE] ?? { x: doc.stages.length * 240, y: 0 },
			data: {}
		});
		// The abort terminal renders only when some edge targets it.
		const hasAbort = doc.stages.some((s) =>
			EDGE_KINDS.some((k) => chainLinks(edgeOf(s, k)).some((l) => l.to === ABORT))
		);
		if (hasAbort) {
			ns.push({
				id: '__abort',
				type: 'abort',
				position: layout[ABORT] ?? { x: doc.stages.length * 240, y: 160 },
				data: {}
			});
		}
		const es: Edge[] = [];
		const fed = feedDots(doc);
		// Dataflow, derived from the artifact paths stages read and write: drawn
		// behind the control edges, never selectable, never draggable. The YAML's
		// paths ARE the wiring; this only shows what they already say.
		for (const l of deriveDataLinks(doc, agents, actions)) {
			es.push({
				id: `data|${l.source}|${l.target}|${l.value}`,
				source: l.source,
				sourceHandle: l.sourceHandle,
				target: l.target,
				targetHandle: l.targetHandle,
				type: 'step',
				selectable: false,
				focusable: false,
				deletable: false,
				zIndex: -1,
				style: 'stroke:#565f89; stroke-dasharray:2 3; stroke-width:1; opacity:0.6'
			});
		}
		if (doc.stages.length) {
			es.push({
				id: 'entry|__start',
				source: '__start',
				target: doc.stages[0].id,
				type: 'step',
				style: 'stroke:#7aa2f7; stroke-width:2'
			});
		}
		doc.stages.forEach((s, i) => {
			const target = effectiveSuccess(doc, i);
			es.push({
				id: `success|${s.id}`,
				source: s.id,
				sourceHandle: 'success',
				target: target === DONE ? '__done' : target,
				type: 'step',
				style: 'stroke:#4caf7d; stroke-width:2'
			});
			for (const kind of EDGE_KINDS) {
				// One rendered edge per chain link; deeper links are the "then"
				// fallbacks and label their position in the chain.
				chainLinks(edgeOf(s, kind)).forEach((link, depth) => {
					// The edge lands on the exact dot that receives its content, when the
					// target bound one: the picture shows where the rationale goes.
					const dot = fed.get(`${link.to}|${feedSentinelFor(kind)}`);
					es.push({
						id: `${kind}|${s.id}|${depth}`,
						source: s.id,
						sourceHandle: kind,
						target: link.to === ABORT ? '__abort' : link.to,
						targetHandle: dot ? `in:${dot}` : undefined,
						type: 'step',
						label:
							link.to === ABORT
								? `${kind}${depth > 0 ? ` then#${depth}` : ''}`
								: `${kind}${depth > 0 ? ` then#${depth}` : ''} ×${link.max === -1 ? '∞' : (link.max ?? '?')}`,
						animated: true,
						style: edgeStyles[kind] + (depth > 0 ? '; opacity:0.65' : '')
					});
				});
			}
		});
		nodes = ns;
		edges = es;
	}
	$effect(() => {
		void rev;
		rebuild();
	});

	function handleConnect(conn: Connection) {
		if (!conn.source || !conn.target) return;
		// Dataflow dots are not rewireable: an artifact path is what connects two
		// stages, so a drag from one must not be read as a control-flow rewire
		// (setEdge's fallthrough would silently overwrite on_timeout).
		const h = conn.sourceHandle ?? '';
		if (h.startsWith('in:') || h.startsWith('out:')) return;
		const target =
			conn.target === '__done' ? DONE : conn.target === '__abort' ? ABORT : conn.target;
		if (conn.source === '__start') {
			onlink(START, 'entry', target); // re-point the entry
			return;
		}
		onlink(conn.source, conn.sourceHandle ?? 'success', target);
	}

	const nodeKey = (id: string) =>
		id === '__done' ? DONE : id === '__abort' ? ABORT : id === '__start' ? START : id;
</script>

<div class="graph-wrap">
	<SvelteFlow
		bind:nodes
		bind:edges
		{nodeTypes}
		colorMode="dark"
		fitView
		deleteKey={null}
		onconnect={handleConnect}
		onnodedragstop={({ targetNode }) => {
			if (targetNode) onmove(nodeKey(targetNode.id), targetNode.position.x, targetNode.position.y);
		}}
		onnodeclick={({ node }) =>
			onselect(node.id.startsWith('__') ? null : { kind: 'stage', id: node.id })}
		onedgeclick={({ edge }) => {
			const [kind, stage] = edge.id.split('|');
			// A data edge has nothing of its own to edit — select its producer, which
			// is where the path it carries is written.
			if (kind === 'data') {
				onselect({ kind: 'stage', id: stage });
				return;
			}
			if (kind === 'entry') {
				onselect(doc.stages.length ? { kind: 'stage', id: doc.stages[0].id } : null);
				return;
			}
			onselect(
				kind === 'success' ? { kind: 'stage', id: stage } : { kind: 'edge', stage, edge: kind }
			);
		}}
		onpaneclick={() => onselect(null)}
	>
		<Background />
		<Controls />
	</SvelteFlow>
</div>

<style>
	.graph-wrap {
		height: 100%;
		min-height: 420px;
		border: 1px solid var(--border, #333);
		border-radius: 6px;
		overflow: hidden;
	}
	/* Edge labels: xyflow's defaults assume a light canvas; pin them to the
     app's dark palette so on_fail/on_timeout/on_reject stay readable. */
	.graph-wrap :global(.svelte-flow) {
		--xy-edge-label-background-color: var(--bg2, #1f2430);
		--xy-edge-label-color: var(--fg, #d5dbe8);
	}
	.graph-wrap :global(.svelte-flow__edge-label) {
		background: var(--bg2, #1f2430);
		color: var(--fg, #d5dbe8);
		border: 1px solid var(--border, #333);
		border-radius: 3px;
		padding: 0 4px;
		font-size: 10px;
	}
</style>
