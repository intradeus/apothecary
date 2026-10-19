<script lang="ts">
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
	import { prefs } from '$lib/prefs.svelte';
	import { untrack } from 'svelte';
	import FlowStageNode from '$lib/FlowStageNode.svelte';
	import FlowDoneNode from '$lib/FlowDoneNode.svelte';
	import FlowAbortNode from '$lib/FlowAbortNode.svelte';
	import FlowStartNode from '$lib/FlowStartNode.svelte';
	import FitOnInit from '$lib/flow/FitOnInit.svelte';
	import {
		ABORT,
		DONE,
		START,
		EDGE_KINDS,
		chainLinks,
		deriveDataLinks,
		effectiveSuccess,
		edgeOf,
		phaseEntry,
		triggerLabel,
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
		onarrange,
		agents = [],
		actions = []
	}: {
		doc: FlowDoc;
		layout: Record<string, { x: number; y: number }>;
		rev: number;
		onselect: (sel: Selection) => void;
		onmove: (id: string, x: number, y: number) => void;
		onlink: (source: string, handle: string, target: string) => void;
		/** Re-run the success-chain layout (the controls' auto-arrange button). */
		onarrange?: () => void;
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

	// Edges and handles (DESIGN 3.6): success solid green; failure edges
	// dashed and animated in their hue; a "then" fallback is the same hue at
	// half opacity; dataflow is faint and dotted.
	const HUE: Record<string, string> = {
		on_fail: 'ros',
		on_reject: 'amb',
		on_timeout: 'tea'
	};
	const SHORT: Record<string, string> = {
		on_fail: 'fail',
		on_reject: 'reject',
		on_timeout: 'timeout'
	};
	const edgeStyle = (kind: string, depth: number) =>
		`stroke: var(--${HUE[kind]}); stroke-width: 1.6; stroke-dasharray: 6 4;${depth > 0 ? ' opacity: 0.5;' : ''}`;
	const labelStyle = (kind: string, depth: number) =>
		`color: var(--${HUE[kind]}); border-color: var(--${HUE[kind]}Ln);${depth > 0 ? ' opacity: 0.7;' : ''}`;

	let nodes = $state.raw<Node[]>([]);
	let edges = $state.raw<Edge[]>([]);

	function rebuild() {
		const ns: Node[] = doc.stages.map((s, i) => ({
			id: s.id,
			type: 'stage',
			position: layout[s.id] ?? { x: i * 240, y: 0 },
			data: { stage: s, entry: i === 0 || phaseEntry(doc, s.phase)?.id === s.id, agents, actions }
		}));
		// Reaction phases (PLAN6): a marker per phase naming what starts it,
		// pointing at the phase's first stage.
		const triggers = (doc.phases ?? []).slice(1);
		for (const ph of triggers) {
			const first = phaseEntry(doc, ph.id);
			const at = first ? (layout[first.id] ?? { x: 0, y: 0 }) : { x: 0, y: 0 };
			ns.push({
				id: `__trigger:${ph.id}`,
				type: 'start',
				position: layout[`trigger:${ph.id}`] ?? { x: at.x - 220, y: at.y },
				data: {
					label: `${ph.id}: on ${triggerLabel(ph.on)}`,
					title: `Phase ${ph.id} starts when ${ph.on.signal} holds for one of the run's merge requests — drag onto a stage of this phase to start it there`
				}
			});
		}
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
				type: 'smoothstep',
				selectable: false,
				focusable: false,
				deletable: false,
				zIndex: -1,
				style: 'stroke: var(--ln2); stroke-dasharray: 1 4; stroke-width: 1.4; stroke-linecap: round'
			});
		}
		if (doc.stages.length) {
			es.push({
				id: 'entry|__start',
				source: '__start',
				target: doc.stages[0].id,
				type: 'smoothstep',
				style: 'stroke: var(--brand); stroke-width: 1.6; opacity: 0.8'
			});
		}
		for (const ph of triggers) {
			const first = phaseEntry(doc, ph.id);
			if (first)
				es.push({
					id: `entry|__trigger:${ph.id}`,
					source: `__trigger:${ph.id}`,
					target: first.id,
					type: 'smoothstep',
					style: 'stroke: var(--vio); stroke-width: 1.6; stroke-dasharray: 6 4'
				});
		}
		doc.stages.forEach((s, i) => {
			const target = effectiveSuccess(doc, i);
			es.push({
				id: `success|${s.id}`,
				source: s.id,
				sourceHandle: 'success',
				target: target === DONE ? '__done' : target,
				type: 'smoothstep',
				style: 'stroke: var(--grn); stroke-width: 1.8'
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
						type: 'smoothstep',
						// "fail ×3": the word as well as the hue, so colour is never the
						// only signal; "then#1" marks a fallback link.
						label:
							link.to === ABORT
								? `${SHORT[kind]}${depth > 0 ? ` · then#${depth}` : ''} → abort`
								: `${SHORT[kind]}${depth > 0 ? ` · then#${depth}` : ''} ×${link.max === -1 ? '∞' : (link.max ?? '?')}`,
						labelStyle: labelStyle(kind, depth),
						animated: true,
						style: edgeStyle(kind, depth)
					});
				});
			}
		});
		nodes = ns;
		edges = es;
	}
	// Build once before SvelteFlow mounts, so its initial fitView sees the
	// nodes (it fits on init only); the effect keeps them in step afterwards.
	untrack(rebuild);
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
		if (conn.source === '__start' || conn.source.startsWith('__trigger:')) {
			onlink(START, 'entry', target); // re-point the (phase's) entry
			return;
		}
		onlink(conn.source, conn.sourceHandle ?? 'success', target);
	}

	const nodeKey = (id: string) =>
		id === '__done'
			? DONE
			: id === '__abort'
				? ABORT
				: id === '__start'
					? START
					: id.startsWith('__trigger:')
						? id.slice(2)
						: id;
</script>

<div class="graph-wrap">
	<SvelteFlow
		bind:nodes
		bind:edges
		{nodeTypes}
		colorMode={prefs.theme}
		fitView
		fitViewOptions={{ padding: 0.12, maxZoom: 1.1 }}
		minZoom={0.15}
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
				const first = stage.startsWith('__trigger:')
					? phaseEntry(doc, stage.slice('__trigger:'.length))
					: doc.stages[0];
				onselect(first ? { kind: 'stage', id: first.id } : null);
				return;
			}
			onselect(
				kind === 'success' ? { kind: 'stage', id: stage } : { kind: 'edge', stage, edge: kind }
			);
		}}
		onpaneclick={() => onselect(null)}
	>
		<FitOnInit />
		<Background
			variant={BackgroundVariant.Dots}
			gap={20}
			size={1.2}
			bgColor="transparent"
			patternColor="var(--ln)"
		/>
		<Controls position="bottom-left">
			{#snippet after()}
				{#if onarrange}
					<ControlButton
						onclick={onarrange}
						title="Auto-arrange along the success chain"
						aria-label="Auto-arrange">⇶</ControlButton
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
	.graph-wrap :global(.svelte-flow) {
		--xy-background-color: transparent;
		--xy-edge-label-background-color: var(--popBg);
		--xy-edge-label-color: var(--tx);
		--xy-connectionline-stroke: var(--brand);
		--xy-connectionline-stroke-width: 1.6;
		--xy-attribution-background-color: transparent;
		background: transparent;
		font-family: var(--sans);
	}
	/* Edge labels: "fail ×3" as a mono 10px pill in the edge's hue. */
	.graph-wrap :global(.svelte-flow__edge-label) {
		font-family: var(--mono);
		font-size: 10px;
		font-weight: 600;
		padding: 0 6px;
		line-height: 16px;
		border-radius: 999px;
		border: 1px solid var(--ln2);
		background: var(--popBg);
		white-space: nowrap;
	}
	.graph-wrap :global(.svelte-flow__edge.selected .svelte-flow__edge-path),
	.graph-wrap :global(.svelte-flow__edge:hover .svelte-flow__edge-path) {
		stroke-width: 2.6 !important;
		filter: drop-shadow(0 0 4px currentColor);
	}
	/* Handles: 8px dots in the edge colour; the target is neutral. */
	.graph-wrap :global(.svelte-flow__handle) {
		width: 8px;
		height: 8px;
		min-width: 8px;
		min-height: 8px;
		border: 1.5px solid var(--body);
		background: var(--mut);
	}
	.graph-wrap :global(.svelte-flow__handle.connectingto),
	.graph-wrap :global(.svelte-flow__handle:hover) {
		box-shadow: 0 0 0 3px var(--brandBg);
	}
	/* Controls: a control-glass stack, bottom-left. */
	.graph-wrap :global(.svelte-flow__controls) {
		background: var(--lgBg), var(--popBg);
		border: 1px solid var(--lgLn);
		border-radius: 12px;
		overflow: hidden;
		box-shadow: var(--lgRim);
		margin: 12px;
	}
	.graph-wrap :global(.svelte-flow__controls-button) {
		background: transparent;
		border: 0;
		border-bottom: 1px solid var(--ln);
		color: var(--mut);
		width: 28px;
		height: 26px;
		padding: 0;
		border-radius: 0;
		box-shadow: none;
		font-size: 13px;
	}
	.graph-wrap :global(.svelte-flow__controls-button:last-child) {
		border-bottom: 0;
	}
	.graph-wrap :global(.svelte-flow__controls-button:hover) {
		background: var(--p3);
		color: var(--tx);
	}
	.graph-wrap :global(.svelte-flow__controls-button svg) {
		fill: currentColor;
		max-width: 11px;
		max-height: 11px;
	}
	.graph-wrap :global(.svelte-flow__attribution) {
		opacity: 0.4;
	}
	.graph-wrap :global(.svelte-flow__attribution a) {
		color: var(--fnt);
	}
</style>
