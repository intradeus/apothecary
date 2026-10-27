<script lang="ts">
	import {
		SvelteFlow,
		Background,
		BackgroundVariant,
		Controls,
		ControlButton,
		ViewportPortal,
		type Node,
		type Edge,
		type Connection
	} from '@xyflow/svelte';
	import '@xyflow/svelte/dist/style.css';
	import { prefs } from '$lib/prefs.svelte';
	import { untrack } from 'svelte';
	import FlowStageNode from '$lib/FlowStageNode.svelte';
	import FlowTerminalNode from '$lib/flow/FlowTerminalNode.svelte';
	import FlowSectionNode from '$lib/flow/FlowSectionNode.svelte';
	import FlowHubNode from '$lib/flow/FlowHubNode.svelte';
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
		END,
		FAIL,
		START,
		MAIN,
		MANUAL,
		PHASE_KINDS,
		EDGE_KINDS,
		HUB,
		LABEL_PAD,
		LABEL_ROW,
		chainEnds,
		chainLinks,
		edgeLinks,
		hasHub,
		terminalKey,
		deriveDataLinks,
		effectiveSuccess,
		edgeOf,
		phaseEntry,
		markerKey,
		sectionKey,
		showsSections,
		triggerLabel,
		feedDots,
		givesVerdict,
		SOURCE_VERDICT,
		feedSourceFor,
		type ActionContract,
		type AgentContract,
		type DocSequence,
		type DocPhase,
		type DocStage,
		type FlowDoc,
		type NodeSize,
		type Terminal
	} from '$lib/flowdoc';

	/** What the editor has selected. A stage or an edge inside a sequence
	 *  names the sequence; one in the chains does not. */
	export type Selection =
		| { kind: 'stage'; id: string; sequence?: string }
		| { kind: 'edge'; stage: string; edge: string; sequence?: string }
		| { kind: 'phase'; id: string }
		| { kind: 'agent'; name: string }
		| { kind: 'sequence'; id: string }
		| { kind: 'schema'; name: string }
		| { kind: 'mcp'; name: string }
		| null;

	/** A sequence drawn on its own: its stages as `doc.stages`, and where its
	 *  two ways out lead — the including stage's success and its on_fail. */
	export interface SequenceView {
		id: string;
		description?: string;
		exit: string;
		fail?: string;
	}

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
	// The watchers and outcomes folded to their marker (this view only).
	let collapsed = $state(new Set<string>());

	// What the last build drew, so a rebuild can tell what just arrived (it
	// materialises) from what just left (a ghost dematerialises, then goes).
	let known: Set<string> | null = null;
	const ghosts = new Map<string, Node>();

	// Where a stage's success leads. In a sequence, a stage without one goes on
	// to the next, and the last leaves the sequence (EXIT) — not the phase.
	const EXIT = '__exit';
	const successOf = (i: number) =>
		sequence
			? (doc.stages[i].on_success ?? doc.stages[i + 1]?.id ?? EXIT)
			: effectiveSuccess(doc, i);

	function rebuild() {
		const ids = new Set(doc.stages.map((s) => s.id));
		// Untracked: Svelte Flow writes `nodes` on every drag frame, and a drag
		// must never rebuild the graph.
		const prevAll = untrack(() => nodes);
		const prev = prevAll.filter((n) => n.type === 'stage' && !n.data.leaving);
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
		// What a node measured last time: where a node nobody placed goes
		// depends on how wide the one before it is.
		const width = (id: string, guess = 220) =>
			prevAll.find((n) => n.id === id)?.measured?.width ?? guess;

		// The hub: once the flow has watchers or outcomes, the main chain's end
		// is the run waiting on its merge requests, and each of them branches
		// from there with ends of its own — nothing crosses the canvas to a
		// shared terminal.
		const hub = !sequence && hasHub(doc);
		const phaseById = new Map((doc.phases ?? []).map((p) => [p.id, p]));
		const branch = (phase: string | undefined) => {
			const ph = !sequence && phase ? phaseById.get(phase) : undefined;
			return ph && ph.kind !== 'main' ? ph : undefined;
		};
		// The node a route to a terminal lands on: a watcher's or an outcome's
		// own, the main chain's shared ones (its end the hub, once there is
		// one), a sequence's.
		const termNode = (phase: string | undefined, to: string) => {
			const ph = branch(phase);
			if (ph) return `__${to}:${ph.id}`;
			return to === END && hub ? '__hub' : `__${to}`;
		};
		// Which chain a node belongs to (data.band): it moves with the chain's
		// marker, and a watcher's or an outcome's frame wraps it.
		const bandOf = (phase: string | undefined) => (phase ?? '') || MAIN;
		const hidden = (phase: string | undefined) => !!branch(phase) && collapsed.has(phase!);

		// Where a stage nobody has placed yet goes: after the stage before it
		// in its chain, else beside its chain's marker — a new watcher's first
		// stage lands on its own row, not on another chain's.
		const placed: Record<string, { x: number; y: number }> = {};
		const guess = (i: number) => {
			const s = doc.stages[i];
			const prev = doc.stages[i - 1];
			if (prev && prev.phase === s.phase && placed[prev.id])
				return { x: placed[prev.id].x + width(prev.id) + 40, y: placed[prev.id].y };
			const ph = doc.phases?.find((p) => p.id === s.phase);
			const m = ph && layout[markerKey(ph)];
			return m ? { x: m.x + 220, y: m.y } : { x: i * 240, y: 0 };
		};
		const ns: Node[] = doc.stages.map((s, i) => ({
			id: s.id,
			type: 'stage',
			position: (placed[s.id] =
				layout[s.id] ?? (renamed?.to === s.id ? renamed.from.position : undefined) ?? guess(i)),
			hidden: hidden(s.phase),
			data: {
				stage: s,
				entry: i === 0 || phaseEntry(doc, s.phase)?.id === s.id,
				agents,
				actions,
				sequences: doc.sequences ?? ([] as DocSequence[]),
				fresh: !renamed && added.includes(s.id),
				band: bandOf(s.phase),
				labels: edgeLinks(s).length
			}
		}));
		for (const [id, g] of ghosts) {
			if (ids.has(id)) ghosts.delete(id);
			else ns.push(g);
		}
		// One beginning per chain: a marker naming it and what opens it,
		// pointing at its first stage. The main chain's is `__start` (layout
		// key START), drawn solid — what every run starts with; a watcher's is
		// `__trigger:<phase>`, dashed; an outcome's (data.outcome) is the same
		// beginning in the hue of the run's end it says, done or abort.
		const markerTitle = (ph: DocPhase) =>
			ph.kind === 'outcome'
				? t(ph.then === ABORT ? 'flow.graph.outcomeAbortTitle' : 'flow.graph.outcomeTitle', {
						phase: ph.id,
						signal: ph.on.signal
					})
				: ph.on.signal === MANUAL
					? t('flow.graph.manualTitle', { phase: ph.id })
					: t('flow.graph.triggerTitle', { phase: ph.id, signal: ph.on.signal });
		const mainPhase = doc.phases?.find((p) => p.kind === 'main');
		const triggers = (doc.phases ?? []).filter((p) => p.kind !== 'main');
		for (const ph of triggers) {
			const first = phaseEntry(doc, ph.id);
			const at = first ? (layout[first.id] ?? { x: 0, y: 0 }) : { x: 0, y: 0 };
			const label =
				ph.kind === 'outcome'
					? t('flow.graph.outcomeLabel', {
							phase: ph.id,
							trigger: triggerLabel(ph.on),
							then: ph.then ?? DONE
						})
					: t('flow.graph.beginLabel', { phase: ph.id, trigger: triggerLabel(ph.on) });
			const folded = collapsed.has(ph.id) ? doc.stages.filter((s) => s.phase === ph.id).length : 0;
			ns.push({
				id: `__trigger:${ph.id}`,
				type: 'start',
				position: layout[markerKey(ph)] ?? { x: at.x - 220, y: at.y },
				data: {
					entry: false,
					outcome: ph.kind === 'outcome',
					then: ph.then ?? DONE,
					hub,
					band: ph.id,
					label: folded ? `${label} · ${t('flow.graph.folded', { count: folded })}` : label,
					title: markerTitle(ph)
				}
			});
		}
		// The main chain's marker: its edge points at its first stage, and
		// dragging that edge onto another stage of it starts it there. A flow
		// that only watches begins at the hub; a sequence drawn on its own has
		// its own marker.
		const entryPos = doc.stages.length
			? (layout[doc.stages[0].id] ?? { x: 0, y: 0 })
			: { x: 0, y: 0 };
		const runsOn = t(`flow.graph.operatesOn.${doc.operates_on ?? 'ticket'}`);
		const watchesOnly = hub && !mainPhase;
		if (sequence || mainPhase || !doc.phases?.length || watchesOnly)
			ns.push({
				id: '__start',
				type: 'start',
				position: layout[START] ?? { x: entryPos.x - 170, y: entryPos.y },
				connectable: !watchesOnly,
				data: {
					entry: true,
					band: MAIN,
					label: sequence
						? t('flow.graph.sequenceLabel', { sequence: sequence.id })
						: mainPhase
							? t('flow.graph.mainLabel', { on: runsOn })
							: watchesOnly
								? t('flow.graph.watchLabel', { on: runsOn })
								: t('flow.graph.beginLabelPlain'),
					title: sequence
						? (sequence.description ?? t('flow.graph.sequenceTitle', { sequence: sequence.id }))
						: watchesOnly
							? t('flow.graph.watchTitle', { on: runsOn })
							: t('flow.graph.mainTitle', { on: runsOn })
				}
			});
		// The sections' labels, heading the marker column where the main
		// chain, the watchers and the outcomes begin — once there is more
		// than one chain to tell apart.
		if (!sequence && showsSections(doc)) {
			for (const kind of PHASE_KINDS) {
				const first = doc.phases!.find((p) => p.kind === kind);
				if (!first) continue;
				const at = layout[markerKey(first)] ?? { x: -220, y: 0 };
				ns.push({
					id: `__section:${kind}`,
					type: 'section',
					position: layout[sectionKey(kind)] ?? { x: at.x, y: at.y - 32 },
					draggable: false,
					selectable: false,
					connectable: false,
					focusable: false,
					data: {
						label: t(`flow.graph.section.${kind}`),
						title: t(`flow.graph.sectionTitle.${kind}`)
					}
				});
			}
		}
		// The hub, left of the first chain that branches from it.
		if (hub) {
			const m = layout[markerKey(triggers[0])] ?? { x: -220, y: 0 };
			ns.push({
				id: '__hub',
				type: 'hub',
				position: layout[HUB] ?? { x: m.x - 200, y: m.y - 96 },
				data: {
					label: t('flow.graph.hubLabel'),
					sub: t('flow.graph.hubSub'),
					title: t('flow.graph.hubTitle')
				}
			});
		}
		// A sequence's two ways out, where the run goes on from the stage that
		// runs it: its success, and `fail` (its on_fail) when a stage
		// routes there.
		if (sequence) {
			const at = layout[END] ?? { x: doc.stages.length * 240, y: 0 };
			ns.push({
				id: EXIT,
				type: 'exit',
				position: at,
				data: { label: t('flow.graph.sequenceExit', { to: sequence.exit }) }
			});
			const fails = doc.stages.some((s) =>
				EDGE_KINDS.some((k) => chainLinks(edgeOf(s, k)).some((l) => l.to === FAIL))
			);
			if (fails)
				ns.push({
					id: '__fail',
					type: 'failexit',
					position: layout[DONE] ?? { x: at.x, y: at.y + 80 },
					data: { label: t('flow.graph.sequenceFail', { to: sequence.fail ?? FAIL }) }
				});
		}
		// The shared ends, for the main chain (or a flow, or a sequence, drawn
		// without chains): end — this chain is over, the run goes back to
		// waiting — is always drawn in a flow without a hub, which stands for it
		// otherwise; done (the run is done) and abort only when a route reaches
		// them.
		const trunk = doc.stages.filter((s) => !branch(s.phase));
		const reaches = (stages: DocStage[], to: string) =>
			stages.some(
				(s) =>
					s.on_success === to ||
					EDGE_KINDS.some((k) => chainLinks(edgeOf(s, k)).some((l) => l.to === to))
			);
		// (A sequence's view keeps END and DONE for its ways out: its own ends
		// stand under them.)
		const below = (key: string, dy: number) =>
			sequence && layout[key] ? { x: layout[key].x, y: layout[key].y + dy } : layout[key];
		if (sequence ? reaches(trunk, END) : !hub)
			ns.push({
				id: '__end',
				type: 'end',
				position: below(END, 70) ?? { x: doc.stages.length * 240, y: 0 },
				data: { band: MAIN }
			});
		if (reaches(trunk, DONE))
			ns.push({
				id: '__done',
				type: 'done',
				position: below(DONE, 70) ?? { x: doc.stages.length * 240, y: 80 },
				data: { band: MAIN }
			});
		if (reaches(trunk, ABORT))
			ns.push({
				id: '__abort',
				type: 'abort',
				position: below(END, 140) ?? layout[ABORT] ?? { x: doc.stages.length * 240, y: 160 },
				data: { band: MAIN }
			});
		// A watcher's or an outcome's own ends, closing its row: where its
		// chain goes back to waiting, or ends the run. One nobody placed yet
		// stands right of the chain's last stage.
		for (const ph of triggers) {
			const own = doc.stages.filter((s) => s.phase === ph.id);
			const last = own.at(-1);
			const from = last
				? { x: placed[last.id].x + width(last.id) + 48, y: placed[last.id].y }
				: (() => {
						const m = layout[markerKey(ph)] ?? { x: -220, y: 0 };
						return { x: m.x + width(`__trigger:${ph.id}`, 200) + 48, y: m.y };
					})();
			chainEnds(doc, ph).forEach((to: Terminal, k) => {
				ns.push({
					id: `__${to}:${ph.id}`,
					type: to,
					position: layout[terminalKey(to, ph)] ?? { x: from.x, y: from.y + k * 58 },
					hidden: hidden(ph.id),
					data: {
						band: ph.id,
						title: t(to === END ? 'flow.term.endBranch' : `flow.term.${to}`)
					}
				});
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
				hidden: hidden(s.phase),
				data: {
					stage: s.id,
					band: bandOf(s.phase),
					title: t('flow.term.debugFor', { stage: s.id })
				}
			});
		}
		const es: Edge[] = [];
		const fed = feedDots(doc);
		// Dataflow, derived from the artifact paths stages read and write: drawn
		// behind the control edges, never selectable, never draggable. The YAML's
		// paths ARE the wiring; this only shows what they already say. Within a
		// chain only: a watcher reading what the main chain wrote shows it on
		// its port, not as a line across the canvas.
		const chainOf = new Map(doc.stages.map((s) => [s.id, s.phase ?? '']));
		for (const l of deriveDataLinks(doc, agents, actions)) {
			if (chainOf.get(l.source) !== chainOf.get(l.target)) continue;
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
		if (watchesOnly) {
			es.push({
				id: 'entry|__start',
				source: '__start',
				target: '__hub',
				type: 'smoothstep',
				selectable: false,
				style: hued('brand')
			});
		} else if (doc.stages.length && (sequence || mainPhase || !doc.phases?.length)) {
			es.push({
				id: 'entry|__start',
				source: '__start',
				target: doc.stages[0].id,
				type: 'smoothstep',
				style: hued('brand')
			});
		}
		// Each watcher and outcome branches from the hub — one line in the
		// hub's hue down the marker column; each chain wears its own after
		// its marker.
		const branchHue = (ph: DocPhase) =>
			ph.kind !== 'outcome' ? 'vio' : ph.then === ABORT ? 'ros' : 'grn';
		for (const ph of triggers) {
			if (hub)
				es.push({
					id: `hub|${ph.id}`,
					source: '__hub',
					target: `__trigger:${ph.id}`,
					targetHandle: 'hub',
					type: 'smoothstep',
					class: 'e-dash',
					style: hued('tea')
				});
			const first = phaseEntry(doc, ph.id);
			// A stage-less outcome's marker points straight at its end.
			const target = first ? first.id : termNode(ph.id, ph.then ?? DONE);
			es.push({
				id: `entry|__trigger:${ph.id}`,
				source: `__trigger:${ph.id}`,
				target,
				type: 'smoothstep',
				class: 'e-dash',
				style: hued(branchHue(ph))
			});
		}
		doc.stages.forEach((s, i) => {
			const target = successOf(i);
			// A stage whose success is a verdict carries it on: the success
			// edge lands on the target's edge.verdict dot, when it has one.
			const verdictDot = givesVerdict(s, agents)
				? fed.get(`${target}|${SOURCE_VERDICT}`)
				: undefined;
			es.push({
				id: `success|${s.id}`,
				source: s.id,
				sourceHandle: 'success',
				target:
					target === END || target === DONE || target === ABORT
						? termNode(s.phase, target)
						: target,
				targetHandle: verdictDot ? `in:${verdictDot}` : undefined,
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
				const dot = fed.get(`${link.to}|${feedSourceFor(kind)}`);
				es.push({
					id: `${kind}|${s.id}|${depth}`,
					source: s.id,
					sourceHandle: kind,
					target:
						link.to === FAIL
							? '__fail'
							: link.to === DEBUG
								? debugNodeID(s.id)
								: link.to === END || link.to === DONE || link.to === ABORT
									? termNode(s.phase, link.to)
									: link.to,
					targetHandle: dot ? `in:${dot}` : undefined,
					type: 'labeled',
					data: { row, pick: () => onselect({ kind: 'edge', stage: s.id, edge: kind }) },
					// "fail ×3": the word as well as the hue, so colour is never the
					// only signal; "then#1" marks a fallback link. A debug link
					// names no target: its leaf sits right under the stage.
					label:
						link.to === DEBUG
							? `${word(kind)}${depth > 0 ? ` · then#${depth}` : ''}`
							: link.to === ABORT || link.to === END || link.to === DONE || link.to === FAIL
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
					data: { anchor: 'right', pick: () => onselect({ kind: 'stage', id: s.id }) },
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
		if (readonly || !conn.source || !conn.target) return;
		// A debug leaf's own handle carries the drawn return edge only.
		if (conn.source.startsWith('__debug:')) return;
		// Dataflow dots are not rewireable: an artifact path is what connects two
		// stages, so a drag from one must not be read as a control-flow rewire
		// (setEdge's fallthrough would silently overwrite on_timeout).
		const h = conn.sourceHandle ?? '';
		if (h.startsWith('in:') || h.startsWith('out:')) return;
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

	// ---- frames: each watcher and outcome, a sub-run of its own ------------
	// A frame wraps a watcher's or an outcome's chain — its marker, stages,
	// debug leaves and ends — drawn behind the nodes from where they stand
	// now, so it follows a drag. Its corner folds the chain to its marker.
	const FRAME_PAD = 16;
	const frames = $derived.by(() => {
		if (sequence) return [];
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
				hue: ph.kind !== 'outcome' ? 'vio' : ph.then === ABORT ? 'ros' : 'grn',
				x: b.x0 - FRAME_PAD,
				y: b.y0 - FRAME_PAD,
				w: b.x1 - b.x0 + 2 * FRAME_PAD,
				h: b.y1 - b.y0 + 2 * FRAME_PAD,
				folded: collapsed.has(id)
			};
		});
	});
	function fold(id: string) {
		const next = new Set(collapsed);
		if (!next.delete(id)) next.add(id);
		collapsed = next;
		rebuild();
	}

	// Dragging a chain's marker takes the chain with it: its stages, leaves
	// and ends move by the same amount, and are saved where they land.
	let group: {
		anchor: string;
		from: { x: number; y: number };
		members: Map<string, { x: number; y: number }>;
	} | null = null;
	const isMarker = (id: string) => id === '__start' || id.startsWith('__trigger:');
	function dragStart(n: Node | null) {
		group = null;
		if (!n || !isMarker(n.id) || !n.data?.band) return;
		const band = n.data.band;
		group = {
			anchor: n.id,
			from: { ...n.position },
			members: new Map(
				nodes
					.filter((m) => m.id !== n.id && m.data?.band === band && !m.data?.leaving)
					.map((m) => [m.id, { ...m.position }])
			)
		};
	}
	function dragMove(n: Node | null) {
		const g = group;
		if (!g || !n || n.id !== g.anchor) return;
		const dx = n.position.x - g.from.x;
		const dy = n.position.y - g.from.y;
		nodes = nodes.map((m) => {
			const p = g.members.get(m.id);
			return p ? { ...m, position: { x: p.x + dx, y: p.y + dy } } : m;
		});
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
		onnodeclick={({ node }) => {
			// A watcher's or an outcome's own end opens its panel.
			const own = /^__(?:end|done|abort):(.+)$/.exec(node.id)?.[1];
			onselect(
				node.id.startsWith('__debug:')
					? { kind: 'stage', id: node.id.slice('__debug:'.length) }
					: node.id.startsWith('__trigger:')
						? { kind: 'phase', id: node.id.slice('__trigger:'.length) }
						: own
							? { kind: 'phase', id: own }
							: node.id === '__start' && doc.phases?.some((p) => p.kind === 'main')
								? { kind: 'phase', id: MAIN }
								: node.id.startsWith('__')
									? null
									: { kind: 'stage', id: node.id }
			);
		}}
		onedgeclick={({ edge }) => {
			const [kind, stage] = edge.id.split('|');
			// A data edge has nothing of its own to edit — select its producer, which
			// is where the path it carries is written.
			if (kind === 'data' || kind === 'debugreturn') {
				onselect({ kind: 'stage', id: stage });
				return;
			}
			// The hub's branch to a watcher or an outcome opens its panel.
			if (kind === 'hub') {
				onselect({ kind: 'phase', id: stage });
				return;
			}
			if (kind === 'entry') {
				if (stage === '__start' && !doc.stages.length) return;
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
		<FitOnInit {refit} {legible} onmeasured={() => onmeasured?.(measuredSizes())} />
		<ViewportPortal target="back">
			{#each frames as f (f.id)}
				<div
					class="frame h-{f.hue}"
					class:folded={f.folded}
					style="transform: translate({f.x}px, {f.y}px); width: {f.w}px; height: {f.h}px"
				></div>
			{/each}
		</ViewportPortal>
		<ViewportPortal target="front">
			{#each frames as f (f.id)}
				<button
					class="btn btn-icon fold h-{f.hue}"
					style="transform: translate({f.x + f.w - 30}px, {f.y + 6}px)"
					title={t(f.folded ? 'flow.graph.unfold' : 'flow.graph.fold', { phase: f.id })}
					aria-label={t(f.folded ? 'flow.graph.unfold' : 'flow.graph.fold', { phase: f.id })}
					aria-expanded={!f.folded}
					onclick={() => fold(f.id)}>{f.folded ? I.caret : I.menu}</button
				>
			{/each}
		</ViewportPortal>
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
		border-color: var(--eh-ln, var(--ln2));
		color: var(--eh, var(--tx));
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

	/* A watcher's or an outcome's frame: a faint wash of its marker's hue
	 * behind its chain, and the corner button that folds it. */
	.frame {
		position: absolute;
		top: 0;
		left: 0;
		border-radius: var(--r-card);
		border: 1px dashed color-mix(in oklch, var(--h) 30%, transparent);
		background: color-mix(in oklch, var(--h) 5%, transparent);
		pointer-events: none;
	}
	.frame.folded {
		border-style: solid;
	}
	/* A .btn-icon (svelte-flow keeps its own button defaults, so .btn opts
	 * in), pinned to the band's corner and tinted in its hue. */
	.fold {
		position: absolute;
		top: 0;
		left: 0;
		width: 22px;
		height: 22px;
		color: var(--h);
		font-size: var(--fs-badge);
		pointer-events: all;
	}
	.fold:hover {
		color: var(--h);
		background: color-mix(in oklch, var(--h) 14%, transparent);
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
