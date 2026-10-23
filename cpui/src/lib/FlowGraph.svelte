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
	import FlowTerminalNode from '$lib/flow/FlowTerminalNode.svelte';
	import FitOnInit from '$lib/flow/FitOnInit.svelte';
	import FlowLabeledEdge from '$lib/flow/FlowLabeledEdge.svelte';
	import { EDGE_HUE } from '$lib/ui/hues';
	import { ms } from '$lib/ui/motion';
	import { I } from '$lib/ui/icons';
	import { t } from '$lib/i18n/index.svelte';
	import { token } from '$lib/ui/tokens';
	import {
		ABORT,
		DEBUG,
		debugKey,
		parksOnDebug,
		DONE,
		START,
		TRIGGER_MANUAL,
		EDGE_KINDS,
		chainLinks,
		edgeLinks,
		deriveDataLinks,
		effectiveSuccess,
		edgeOf,
		phaseEntry,
		triggerLabel,
		feedDots,
		feedSentinelFor,
		type ActionContract,
		type AgentContract,
		type FlowDoc,
		type NodeSize
	} from '$lib/flowdoc';

	export type Selection =
		| { kind: 'stage'; id: string }
		| { kind: 'edge'; stage: string; edge: string }
		| { kind: 'phase'; id: string }
		| null;

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
		actions = []
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
	} = $props();

	const nodeTypes = {
		stage: FlowStageNode,
		done: FlowTerminalNode,
		abort: FlowTerminalNode,
		debug: FlowTerminalNode,
		start: FlowTerminalNode
	};
	// Control edges carry their label by the source (FlowLabeledEdge), so
	// labels never land on nodes or on each other.
	const edgeTypes = { labeled: FlowLabeledEdge };

	// Edges and handles (DESIGN 3.6): success solid green; failure edges
	// dashed and animated in their hue; a "then" fallback is the same hue
	// faded; dataflow is faint and dotted. The look lives in CSS classes
	// below, sized by the flow tokens (--edge-w…); an edge only carries its
	// hue (--eh, from hues.ts) so hover and selection can transition it.
	const hued = (h: string) => `--eh: var(--${h}); --eh-ln: var(--${h}Ln)`;
	// "on_fail" reads "fail" on the canvas.
	const word = (kind: string) => kind.replace(/^on_/, '');
	// A stage's debug leaf on the canvas. Declared before rebuild(), which
	// runs during setup (untrack below): a const used earlier is a TDZ crash.
	const debugNodeID = (stage: string) => `__debug:${stage}`;

	let nodes = $state.raw<Node[]>([]);
	let edges = $state.raw<Edge[]>([]);

	// What the last build drew, so a rebuild can tell what just arrived (it
	// materialises) from what just left (a ghost dematerialises, then goes).
	let known: Set<string> | null = null;
	const ghosts = new Map<string, Node>();

	function rebuild() {
		const ids = new Set(doc.stages.map((s) => s.id));
		// Untracked: Svelte Flow writes `nodes` on every drag frame, and a drag
		// must never rebuild the graph.
		const prev = untrack(() => nodes).filter((n) => n.type === 'stage' && !n.data.leaving);
		const added = known ? doc.stages.filter((s) => !known!.has(s.id)).map((s) => s.id) : [];
		const removed = known ? prev.filter((n) => !ids.has(n.id)) : [];
		// One out, one in, in the same edit: a rename. It keeps its place on
		// the canvas instead of leaving and arriving.
		const renamed =
			added.length === 1 && removed.length === 1 ? { from: removed[0], to: added[0] } : null;
		if (renamed && !layout[renamed.to]) {
			const { x, y } = renamed.from.position;
			queueMicrotask(() => onmove(renamed.to, x, y));
		}
		if (!renamed) {
			for (const n of removed) {
				ghosts.set(n.id, {
					...n,
					selected: false,
					selectable: false,
					draggable: false,
					connectable: false,
					data: { ...n.data, leaving: true, fresh: false }
				});
				setTimeout(
					() => {
						if (ghosts.delete(n.id)) rebuild();
					},
					ms('--dur-spring', 450)
				);
			}
		}
		known = ids;

		const ns: Node[] = doc.stages.map((s, i) => ({
			id: s.id,
			type: 'stage',
			position: layout[s.id] ??
				(renamed?.to === s.id ? renamed.from.position : undefined) ?? { x: i * 240, y: 0 },
			data: {
				stage: s,
				entry: i === 0 || phaseEntry(doc, s.phase)?.id === s.id,
				agents,
				actions,
				fresh: !renamed && added.includes(s.id)
			}
		}));
		for (const [id, g] of ghosts) {
			if (ids.has(id)) ghosts.delete(id);
			else ns.push(g);
		}
		// One beginning per phase (1 entrypoint = 1 phase): a marker naming the
		// phase and what begins it, pointing at the phase's first stage. The
		// entry's is `__start` (layout key START) — the phase every run begins
		// with, drawn solid; a reaction's is `__trigger:<phase>`, dashed.
		const triggers = (doc.phases ?? []).slice(1);
		for (const ph of triggers) {
			const first = phaseEntry(doc, ph.id);
			const at = first ? (layout[first.id] ?? { x: 0, y: 0 }) : { x: 0, y: 0 };
			ns.push({
				id: `__trigger:${ph.id}`,
				type: 'start',
				position: layout[`trigger:${ph.id}`] ?? { x: at.x - 220, y: at.y },
				data: {
					entry: false,
					label: t('flow.graph.beginLabel', { phase: ph.id, trigger: triggerLabel(ph.on) }),
					title:
						ph.on.signal === TRIGGER_MANUAL
							? t('flow.graph.manualTitle', { phase: ph.id })
							: t('flow.graph.triggerTitle', { phase: ph.id, signal: ph.on.signal })
				}
			});
		}
		// The entry marker: its edge points at stages[0]; dragging that edge
		// onto another stage makes it the entry (moves it to document front).
		// A flow without phases is one entry phase, begun by hand.
		const entryPos = doc.stages.length
			? (layout[doc.stages[0].id] ?? { x: 0, y: 0 })
			: { x: 0, y: 0 };
		const entryPhase = doc.phases?.[0];
		ns.push({
			id: '__start',
			type: 'start',
			position: layout[START] ?? { x: entryPos.x - 170, y: entryPos.y },
			data: {
				entry: true,
				label: entryPhase
					? t('flow.graph.beginLabel', {
							phase: entryPhase.id,
							trigger: triggerLabel(entryPhase.on)
						})
					: t('flow.graph.beginLabelPlain', { trigger: TRIGGER_MANUAL }),
				title: t('flow.graph.entryTitle')
			}
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
		// The debug leaf is no terminal: it parks the run and comes back to the
		// stage that sent it there. So it is drawn once PER STAGE that
		// escalates to it, beside that stage, with its return edge — a shared
		// sink hid the one fact that matters.
		for (const s of doc.stages) {
			if (!parksOnDebug(s)) continue;
			const at = layout[s.id] ?? { x: 0, y: 0 };
			ns.push({
				id: debugNodeID(s.id),
				type: 'debug',
				position: layout[debugKey(s.id)] ?? { x: at.x + 24, y: at.y + 170 },
				data: { stage: s.id, title: t('flow.term.debugFor', { stage: s.id }) }
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
				class: 'e-data'
			});
		}
		if (doc.stages.length) {
			es.push({
				id: 'entry|__start',
				source: '__start',
				target: doc.stages[0].id,
				type: 'smoothstep',
				style: hued('brand')
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
					class: 'e-dash',
					style: hued('vio')
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
				class: 'e-success',
				style: hued(EDGE_HUE.success)
			});
			// One rendered edge per chain link; deeper links are the "then"
			// fallbacks and label their position in the chain. The link's index
			// in edgeLinks is its label's row under the stage.
			edgeLinks(s).forEach(({ kind, link, depth }, row) => {
				// The edge lands on the exact dot that receives its content, when the
				// target bound one: the picture shows where the rationale goes.
				const dot = fed.get(`${link.to}|${feedSentinelFor(kind)}`);
				es.push({
					id: `${kind}|${s.id}|${depth}`,
					source: s.id,
					sourceHandle: kind,
					target: link.to === ABORT ? '__abort' : link.to === DEBUG ? debugNodeID(s.id) : link.to,
					targetHandle: dot ? `in:${dot}` : undefined,
					type: 'labeled',
					data: { row },
					// "fail ×3": the word as well as the hue, so colour is never the
					// only signal; "then#1" marks a fallback link. A debug link
					// names no target: its leaf sits right under the stage.
					label:
						link.to === DEBUG
							? `${word(kind)}${depth > 0 ? ` · then#${depth}` : ''}`
							: link.to === ABORT
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
					data: { anchor: 'right' },
					label: `${I.retry} ${t('flow.graph.debugReturn')}`,
					labelStyle: hued('amb'),
					class: 'e-dash e-then',
					style: hued('amb')
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
		// A debug leaf's own handle carries the drawn return edge only.
		if (conn.source.startsWith('__debug:')) return;
		// Dataflow dots are not rewireable: an artifact path is what connects two
		// stages, so a drag from one must not be read as a control-flow rewire
		// (setEdge's fallthrough would silently overwrite on_timeout).
		const h = conn.sourceHandle ?? '';
		if (h.startsWith('in:') || h.startsWith('out:')) return;
		const target =
			conn.target === '__done'
				? DONE
				: conn.target === '__abort'
					? ABORT
					: conn.target.startsWith('__debug:')
						? DEBUG
						: conn.target;
		if (conn.source === '__start' || conn.source.startsWith('__trigger:')) {
			onlink(START, 'entry', target); // re-point the (phase's) entry
			return;
		}
		onlink(conn.source, conn.sourceHandle ?? 'success', target);
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

	const nodeKey = (id: string) =>
		id === '__done'
			? DONE
			: id === '__abort'
				? ABORT
				: id.startsWith('__debug:')
					? debugKey(id.slice('__debug:'.length))
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
		{edgeTypes}
		colorMode={prefs.themeDef.scheme}
		fitView
		fitViewOptions={{ padding: 0.12, maxZoom: 1.1 }}
		minZoom={0.15}
		deleteKey={null}
		onconnect={handleConnect}
		onnodedragstop={({ targetNode }) => {
			if (targetNode) onmove(nodeKey(targetNode.id), targetNode.position.x, targetNode.position.y);
		}}
		onnodeclick={({ node }) =>
			onselect(
				node.id.startsWith('__debug:')
					? { kind: 'stage', id: node.id.slice('__debug:'.length) }
					: node.id.startsWith('__trigger:')
						? { kind: 'phase', id: node.id.slice('__trigger:'.length) }
						: node.id === '__start' && doc.phases?.length
							? { kind: 'phase', id: doc.phases[0].id }
							: node.id.startsWith('__')
								? null
								: { kind: 'stage', id: node.id }
			)}
		onedgeclick={({ edge }) => {
			const [kind, stage] = edge.id.split('|');
			// A data edge has nothing of its own to edit — select its producer, which
			// is where the path it carries is written.
			if (kind === 'data' || kind === 'debugreturn') {
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
		<FitOnInit onmeasured={() => onmeasured?.(measuredSizes())} />
		<Background variant={BackgroundVariant.Dots} gap={gridGap} size={1.2} />
		<Controls position="bottom-left" class="lg-pop fg-ctl">
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
		/* Opaque, not glass: the sigils turning behind the shell made edges
		 * and labels hard to read through a transparent canvas. */
		--xy-background-color: var(--solidBg);
		--xy-background-pattern-color: var(--ln);
		--xy-edge-stroke: var(--ln2);
		--xy-edge-stroke-width: var(--edge-w);
		--xy-edge-stroke-selected: var(--brand);
		--xy-edge-label-background-color: var(--popBg);
		--xy-edge-label-color: var(--tx);
		--xy-connectionline-stroke: var(--brand);
		--xy-connectionline-stroke-width: var(--edge-w);
		--xy-node-color: var(--tx);
		--xy-node-background-color: var(--cBg);
		--xy-node-border: 1px solid var(--cLn);
		--xy-node-border-radius: var(--r-card);
		--xy-node-group-background-color: var(--p1);
		--xy-node-boxshadow-hover: none;
		--xy-node-boxshadow-selected: none;
		--xy-handle-background-color: var(--mut);
		--xy-handle-border-color: var(--body);
		--xy-selection-background-color: var(--brandBg);
		--xy-selection-border: 1px dashed var(--brandLn);
		--xy-resize-background-color: var(--brand);
		--xy-controls-button-background-color: transparent;
		--xy-controls-button-background-color-hover: var(--p3);
		--xy-controls-button-color: var(--mut);
		--xy-controls-button-color-hover: var(--tx);
		--xy-controls-button-border-color: var(--ln);
		--xy-controls-box-shadow: var(--lgRim), var(--popSh);
		--xy-minimap-background-color: var(--popBg);
		--xy-minimap-mask-background-color: var(--scrim);
		--xy-minimap-mask-stroke-color: var(--ln2);
		--xy-minimap-mask-stroke-width: 1;
		--xy-minimap-node-background-color: var(--p3);
		--xy-minimap-node-stroke-color: var(--ln);
		--xy-minimap-node-stroke-width: 1;
		--xy-attribution-background-color: transparent;
		font-family: var(--sans);
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
		stroke: var(--ln2);
		stroke-dasharray: var(--edge-dash-data);
		stroke-linecap: round;
	}
	.graph-wrap :global(.svelte-flow__edge.selected .svelte-flow__edge-path),
	.graph-wrap :global(.svelte-flow__edge.selectable:hover .svelte-flow__edge-path) {
		stroke-width: var(--edge-w-hover);
		filter: drop-shadow(0 0 var(--glow-xs) var(--eh, var(--brand)));
	}
	/* Edge labels: "fail ×3" as a mono pill in the edge's hue. FlowLabeledEdge
	 * centres a transparent box at its anchor and the pill inside shifts back
	 * by half its own width, so the label starts at the anchor. */
	.graph-wrap :global(.e-lbl-pill) {
		display: inline-block;
		transform: translateX(50%);
		background: var(--xy-edge-label-background-color);
		font-family: var(--mono);
		font-size: var(--fs-id);
		font-weight: 600;
		padding: 0 6px;
		border-radius: var(--r-pill);
		border: 1px solid var(--eh-ln, var(--ln2));
		color: var(--eh, var(--tx));
		white-space: nowrap;
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
		box-shadow: 0 0 0 3px var(--brandBg);
	}

	/* Controls: a popover-glass stack (.lg-pop) with rounded corners. */
	.graph-wrap :global(.fg-ctl) {
		border-radius: var(--r-btn);
		overflow: hidden;
	}
	.graph-wrap :global(.svelte-flow__attribution a) {
		color: var(--fnt);
	}
	/* Marching dashes stop for reduced motion; the dash itself stays. */
	@media (prefers-reduced-motion: reduce) {
		.graph-wrap :global(.svelte-flow__edge.animated path) {
			animation: none;
		}
	}
</style>
