<script lang="ts">
	// Flow editor (DESIGN 7.8): header (name @hash, Graph | YAML, Validate,
	// Versions, Clone, Delete… / Reset, Save), then the canvas beside a 450px
	// inspector. All document logic lives in flowdoc / FlowGraph /
	// FlowInspector; this page wires them up.
	import { onMount, untrack } from 'svelte';
	import { beforeNavigate, goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api } from '$lib/api';
	import type { ActionSpec, Flow, FlowVersion, SignalSpec } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
	import { currentGuild, to } from '$lib/guild';
	import { ago, fmtAbs } from '$lib/format';
	import { t, th } from '$lib/i18n/index.svelte';
	import FlowGraph, { type SequenceView, type Selection } from '$lib/FlowGraph.svelte';
	import FlowInspector from '$lib/FlowInspector.svelte';
	import NameFlowDialog from '$lib/flow/NameFlowDialog.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import GlassGroup from '$lib/ui/GlassGroup.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import CheckChip from '$lib/ui/CheckChip.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';
	import { slide } from '$lib/ui/motion';
	import {
		ABORT,
		chainLinks,
		columnLayout,
		DEBUG,
		autoLayout,
		placeChain,
		type NodeSize,
		docToYaml,
		DONE,
		FAIL,
		START,
		edgeOf,
		effectiveSuccess,
		setEdge,
		setEntry,
		type DocSequence,
		type DocEdge,
		type DocStage,
		type EdgeKind,
		type FlowDoc
	} from '$lib/flowdoc';

	let flow = $state<Flow | null>(null);
	let yaml = $state('');
	let doc = $state<FlowDoc | null>(null);
	let layout = $state<Record<string, { x: number; y: number }>>({});
	// autoPlaced: the layout was guessed by autoLayout (no stored one); it is
	// redone once the graph has measured its nodes.
	let autoPlaced = false;
	let rev = $state(0);
	let tab = $state<'graph' | 'yaml'>('graph');
	let graphDirty = $state(false);
	let selection = $state<Selection>(null);
	let versions = $state<FlowVersion[]>([]);
	let error = $state('');
	let busy = $state(false);
	/** The last validation (or save) result, shown in the Validate popover. */
	let validation = $state<{
		ok: boolean;
		title: string;
		stages?: number;
		hash?: string;
		errors?: string[];
		warnings?: string[];
	} | null>(null);
	/** A one-line note over the canvas (a version loaded into the editor). */
	let notice = $state<{ tone: 'ok' | 'info'; text: string } | null>(null);
	/** A validation request is in flight (aria-busy on Validate). */
	let validating = $state(false);
	// The compiled-in action registry: what an `action` stage may name, and the
	// signature the inspector and the dataflow layer bind against.
	let actions = $state<ActionSpec[]>([]);
	// The signal registry: what a wait stage blocks on, or a gate auto-answers on.
	let signals = $state<SignalSpec[]>([]);
	// The flow document's JSON Schema: what the YAML tab checks as you type.
	let flowSchema = $state<Record<string, unknown> | undefined>();
	// The parser's verdict for the YAML tab's lint: its errors only.
	const checkYaml = async (text: string) => (await api.flows.validate(text)).errors ?? [];

	let validateBtn = $state<HTMLButtonElement | null>(null);
	let validateOpen = $state(false);
	let showVersions = $state(false);
	let cloning = $state(false);
	let inspector = $state<ReturnType<typeof FlowInspector> | null>(null);
	let side = $state<HTMLElement | null>(null);
	// A new selection opens its panel from the top, wherever the last one
	// was scrolled to (the flow panel's agent and sequence chips sit low) —
	// but an edge opens its stage on the route it picked, which the
	// inspector scrolls to.
	$effect(() => {
		if (selection?.kind === 'edge') return;
		untrack(() => side?.scrollTo({ top: 0 }));
	});

	// The workshop that owns this flow. A flow's stages name that workshop's
	// commands and its agents know that repository, so the editor is always
	// editing one workshop's document — never a shared one.
	const projectId = $derived(page.params.workshop!);
	const workshopName = $derived(fleet.projectById.get(projectId)?.name ?? '…');
	const guild = $derived(currentGuild());

	// The document itself is still validated on its own terms: agent, harness,
	// model and run_ref are free text here, checked against the workshop's
	// configuration at run creation (preflight).

	const dirty = $derived(flow !== null && (graphDirty || yaml !== flow.yaml));

	const load = async () => {
		error = '';
		try {
			flow = await api.flows.get(projectId, page.params.name!);
			yaml = flow.yaml;
			versions = await api.flows.versions(projectId, flow.name);
			const r = await api.flows.validate(flow.yaml);
			if (r.ok && r.doc) {
				doc = r.doc;
				layout = flow.layout?.nodes ?? autoLayout(doc);
				autoPlaced = !flow.layout?.nodes;
				if (r.warnings?.length)
					validation = {
						ok: true,
						title: t('page.flow.validWarn'),
						stages: r.stages,
						hash: r.hash,
						warnings: r.warnings
					};
				rev++;
			} else {
				tab = 'yaml';
				validation = { ok: false, title: t('page.flow.noParse'), errors: r.errors ?? [] };
				validateOpen = true;
			}
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
		// Contracts for the binding fields and the dataflow view. Best-effort:
		// without them the graph still renders explicit bindings.
		try {
			[actions, signals] = await Promise.all([api.system.actions(), api.system.signals()]);
		} catch {
			/* binding hints stay off */
		}
		api.flows.schema().then(
			(s) => (flowSchema = s),
			() => {
				/* the YAML tab still has the parser's checks */
			}
		);
	};
	onMount(load);

	function currentYAML(): string {
		if (doc && (tab === 'graph' || graphDirty)) return docToYaml(doc);
		return yaml;
	}

	function markChanged() {
		graphDirty = true;
		notice = null;
		placeMarkers();
		rev++;
		relayoutSequence();
	}

	// placeMarkers makes room for a chain added since the layout was made —
	// in the inspector or the YAML — at the end of its section; everything
	// placed keeps its place, the sections below moving down. (A stage added
	// to an existing chain is placed by the graph, after its chain's last.)
	function placeMarkers() {
		if (!doc) return;
		for (const ph of doc.phases ?? []) placeChain(doc, layout, ph.id);
	}

	function switchTab(next: 'graph' | 'yaml') {
		if (next === tab) return;
		if (next === 'yaml') {
			if (doc && graphDirty) yaml = docToYaml(doc);
			tab = 'yaml';
			return;
		}
		// yaml -> graph: re-parse through the server (single validation source).
		api.flows
			.validate(yaml)
			.then((r) => {
				if (r.ok && r.doc) {
					doc = r.doc;
					// Place stages added in the YAML editor via the success-chain layout
					// rather than dropping them all on one point (which overlaps them).
					const auto = autoLayout(doc);
					for (const s of doc.stages) if (!(s.id in layout)) layout[s.id] = auto[s.id];
					placeMarkers();
					graphDirty = false;
					validation = null;
					rev++;
					tab = 'graph';
				} else {
					tab = 'yaml';
					validation = {
						ok: false,
						title: t('page.flow.fixYaml'),
						errors: r.errors?.length ? r.errors : [t('page.flow.invalidFlow')]
					};
					validateOpen = true;
				}
			})
			.catch((e) => {
				validation = { ok: false, title: t('page.flow.validationFailed'), errors: [String(e)] };
				validateOpen = true;
			});
	}

	function onlink(source: string, handle: string, target: string) {
		if (!doc) return;
		if (source === START) {
			// Dragging the entry marker onto a stage makes it the entry (moves
			// it to document front); terminals can't be an entry.
			if (target !== DONE && target !== ABORT && target !== DEBUG && setEntry(doc, target))
				markChanged();
			return;
		}
		const s = doc.stages.find((x) => x.id === source);
		if (!s) return;
		// A phase's routes stay inside it (the parser refuses the rest): a drop
		// onto another phase's stage is explained, not applied.
		const dest = doc.stages.find((x) => x.id === target);
		if (dest && (dest.phase ?? '') !== (s.phase ?? '')) {
			notice = { tone: 'info', text: t('page.flow.crossPhase', { from: s.id, to: dest.id }) };
			return;
		}
		if (handle === 'success') {
			// success never aborts or parks
			if (target !== s.id && target !== ABORT && target !== DEBUG) s.on_success = target;
		} else {
			const kind = handle as EdgeKind;
			if (target === DONE) return; // recoverable edges point at stages or abort, not done
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
		markChanged();
	}

	let layoutTimer: ReturnType<typeof setTimeout> | undefined;
	function onmove(id: string, x: number, y: number) {
		layout[id] = { x, y };
		// Layout-only autosave (DESIGN3 §2.2): same YAML = same hash = no new
		// version; deferred while real edits are pending (they save together).
		clearTimeout(layoutTimer);
		layoutTimer = setTimeout(async () => {
			if (!flow || dirty || flow.builtin) return;
			try {
				flow = await api.flows.save(projectId, flow.name, flow.yaml, { nodes: layout });
			} catch {
				/* next explicit save carries it */
			}
		}, 800);
	}

	// autoArrange re-runs the success-chain layout with the nodes' measured
	// sizes, discarding manual positions, and persists the fresh map (via
	// onmove's layout-only autosave path).
	function autoArrange(sizes: Record<string, NodeSize> = {}) {
		if (!doc) return;
		layout = autoLayout(doc, sizes);
		rev++;
		mainRefit++; // and show the whole of it
		const first = doc.stages[0]?.id;
		if (first) onmove(first, layout[first].x, layout[first].y);
	}

	// onmeasured: a flow opened without a stored layout was placed with guessed
	// sizes; now that the nodes are measured, place them for real. Not
	// persisted — nothing was dragged; the next drag or arrange saves.
	function onmeasured(sizes: Record<string, NodeSize>) {
		if (!doc || !autoPlaced) return;
		autoPlaced = false;
		layout = autoLayout(doc, sizes);
		rev++;
	}

	// ---- a sequence, opened beside the flow ---------------------------------
	// Selecting a sequence stage of the chains opens the sequence it runs in a
	// pane of its own, its chain drawn like a flow; a sequence selected in the
	// inspector opens on its own. A stage or an edge of the open sequence is
	// edited in place, the pane staying open; selecting anything else (or the
	// empty canvas) closes it. A sequence stage inside the sequence opens
	// that sequence in its place — `path` is the sequence stage ids opened
	// below the root.
	interface Level {
		/** The stage running the sequence; none for one opened on its own. */
		stage?: DocStage;
		sequence: DocSequence;
		view: SequenceView;
	}
	let pane = $state<{ root: string; sequence?: string; path: string[] } | null>(null);
	const failOf = (e: DocEdge | undefined, outer?: string) => {
		const to = chainLinks(e)[0]?.to;
		return to === FAIL ? outer : to;
	};
	const sequenceOf = (st: DocStage | undefined) =>
		st?.type === 'sequence' ? doc?.sequences?.find((q) => q.id === st.sequence) : undefined;
	const levels = $derived.by((): Level[] => {
		const p = pane;
		if (!doc || tab !== 'graph' || !p) return [];
		const out: Level[] = [];
		if (p.sequence) {
			const seq = doc.sequences?.find((q) => q.id === p.sequence);
			if (!seq) return [];
			out.push({
				sequence: seq,
				view: {
					id: seq.id,
					description: seq.description,
					exit: t('page.flow.sequenceExitAny'),
					fail: t('page.flow.sequenceFailAny')
				}
			});
		} else {
			const i = doc.stages.findIndex((s) => s.id === p.root);
			const st = doc.stages[i];
			const seq = sequenceOf(st);
			if (!seq) return [];
			out.push({
				stage: st,
				sequence: seq,
				view: {
					id: seq.id,
					description: seq.description,
					exit: effectiveSuccess(doc, i),
					fail: failOf(st.on_fail)
				}
			});
		}
		for (const id of p.path) {
			const up = out[out.length - 1];
			const j = up.sequence.stages.findIndex((s) => s.id === id);
			const s = up.sequence.stages[j];
			const q = sequenceOf(s);
			if (!q) break;
			out.push({
				stage: s,
				sequence: q,
				view: {
					id: q.id,
					description: q.description,
					// Its success is the next stage of the sequence around it — or
					// that sequence's own way out.
					exit: s.on_success ?? up.sequence.stages[j + 1]?.id ?? up.view.exit,
					fail: failOf(s.on_fail, up.view.fail)
				}
			});
		}
		return out;
	});
	const open = $derived(levels.at(-1));

	// ---- where a panel was opened from ---------------------------------------
	// Opening one panel from another — a stage's agent, a sequence's stage —
	// leaves a trail, and every panel offers the way back to the one before.
	// Selecting on the canvas starts afresh.
	let trail = $state<Selection[]>([]);
	function openPanel(sel: Selection) {
		trail = [...trail, selection];
		select(sel, true);
	}
	// Whether a selection still names something (a delete leaves the trail
	// pointing at nothing).
	function exists(sel: Selection): boolean {
		if (!sel || !doc) return true;
		const inScope = (seq?: string) =>
			seq ? doc!.sequences?.find((q) => q.id === seq)?.stages : doc!.stages;
		switch (sel.kind) {
			case 'stage':
				return !!inScope(sel.sequence)?.some((s) => s.id === sel.id);
			case 'edge':
				return !!inScope(sel.sequence)?.some((s) => s.id === sel.stage);
			case 'phase':
				return !!doc.phases?.some((p) => p.id === sel.id);
			case 'agent':
				return !!doc.agents?.some((a) => a.name === sel.name);
			case 'sequence':
				return !!doc.sequences?.some((q) => q.id === sel.id);
		}
	}
	function goBack() {
		let rest = trail;
		while (rest.length) {
			const prev = rest[rest.length - 1];
			rest = rest.slice(0, -1);
			if (exists(prev)) {
				trail = rest;
				select(prev, true);
				return;
			}
		}
		trail = [];
		select(null);
	}
	// A rename renames what the trail remembers too: the panel renamed, and
	// a sequence's stages under its new name.
	function renamed(from: Selection, to: Selection) {
		if (!from || !to || from.kind !== to.kind) return;
		trail = trail.map((t): Selection => {
			if (!t) return t;
			if (from.kind === 'sequence' && to.kind === 'sequence') {
				if (t.kind === 'sequence' && t.id === from.id) return { ...t, id: to.id };
				if ((t.kind === 'stage' || t.kind === 'edge') && t.sequence === from.id)
					return { ...t, sequence: to.id };
			}
			if (
				from.kind === 'agent' &&
				to.kind === 'agent' &&
				t.kind === 'agent' &&
				t.name === from.name
			)
				return { ...t, name: to.name };
			if (from.kind === 'phase' && to.kind === 'phase' && t.kind === 'phase' && t.id === from.id)
				return { ...t, id: to.id };
			if (from.kind === 'stage' && to.kind === 'stage') {
				if (t.kind === 'stage' && t.sequence === from.sequence && t.id === from.id)
					return { ...t, id: to.id };
				if (t.kind === 'edge' && t.sequence === from.sequence && t.stage === from.id)
					return { ...t, stage: to.id };
			}
			return t;
		});
	}
	// What the inspector asks for: another selection in this one's place,
	// the trail kept (renamed along with it).
	function replace(sel: Selection, how?: 'rename') {
		if (how === 'rename') renamed(selection, sel);
		select(sel, true);
	}

	// select is every selection's way in — the canvas, the pane, the
	// inspector — so the pane follows it. From the canvas (`keep` unset) it
	// starts the trail afresh.
	function select(sel: Selection, keep = false) {
		if (!keep) trail = [];
		selection = sel;
		if (!doc) return;
		if (sel?.kind === 'stage' && !sel.sequence) {
			const st = doc.stages.find((s) => s.id === sel.id);
			if (st?.type !== 'sequence') pane = null;
			else if (pane?.root !== st.id || pane.sequence) pane = { root: st.id, path: [] };
			return;
		}
		const seq =
			sel?.kind === 'sequence'
				? sel.id
				: sel?.kind === 'stage' || sel?.kind === 'edge'
					? sel.sequence
					: undefined;
		if (!seq) {
			pane = null;
			return;
		}
		// Already open, or one of the sequences above it: show that one.
		const at = levels.findIndex((l) => l.sequence.id === seq);
		if (at >= 0 && pane) pane = { ...pane, path: pane.path.slice(0, at) };
		else pane = { root: '', sequence: seq, path: [] };
	}

	// The open sequence as a document of its own: the flow's agents,
	// parameters and sequences, the sequence's stages.
	const sequenceDoc = $derived<FlowDoc | null>(
		doc && open ? { ...doc, phases: undefined, stages: open.sequence.stages } : null
	);
	// The pane is drawn afresh when it shows another sequence or the open
	// one gains, loses or renames a stage — its nodes measured again.
	const paneKey = $derived(
		open
			? `${levels.map((l) => l.sequence.id).join('/')}|${open.sequence.stages.map((s) => s.id).join(',')}`
			: ''
	);
	let sequenceLayout = $state<Record<string, { x: number; y: number }>>({});
	let sequenceRev = $state(0);
	let sequenceSizes: Record<string, NodeSize> = {};
	function relayoutSequence() {
		if (!sequenceDoc) return;
		sequenceLayout = columnLayout(sequenceDoc, sequenceSizes);
		sequenceRev++;
	}
	$effect(() => {
		void paneKey;
		untrack(() => {
			sequenceSizes = {};
			relayoutSequence();
		});
	});
	function onSequenceMeasured(sizes: Record<string, NodeSize>) {
		sequenceSizes = sizes;
		relayoutSequence();
	}
	// A click in the pane edits what it lands on in its sequence; the
	// marker, the ways out and the empty canvas select the sequence itself.
	function onSequenceSelect(sel: Selection) {
		if (!open || !pane) return;
		trail = [];
		const seq = open.sequence.id;
		if (sel?.kind === 'stage') {
			selection = { kind: 'stage', id: sel.id, sequence: seq };
			const s = open.sequence.stages.find((x) => x.id === sel.id);
			if (sequenceOf(s)) pane = { ...pane, path: [...pane.path, sel.id] };
		} else if (sel?.kind === 'edge') selection = { ...sel, sequence: seq };
		else selection = { kind: 'sequence', id: seq };
	}
	// Opening or closing the pane resizes the flow's canvas: it fits again.
	let mainRefit = $state(0);
	let wasOpen = false;
	$effect(() => {
		const isOpen = levels.length > 0;
		if (isOpen === wasOpen) return;
		wasOpen = isOpen;
		untrack(() => mainRefit++);
	});

	async function validate() {
		validating = true;
		try {
			const r = await api.flows.validate(currentYAML());
			validation = r.ok
				? {
						ok: true,
						title: r.warnings?.length ? t('page.flow.validWarn') : t('page.flow.valid'),
						stages: r.stages,
						hash: r.hash,
						warnings: r.warnings
					}
				: { ok: false, title: t('page.flow.invalid'), errors: r.errors ?? [] };
			if (r.ok)
				toast(
					r.warnings?.length
						? t('page.flow.validToastWarn', {
								count: r.stages ?? stageCount,
								warnings: r.warnings.length
							})
						: t('page.flow.validToast', { count: r.stages ?? stageCount }),
					{ tone: r.warnings?.length ? 'warn' : 'ok' }
				);
		} catch (e) {
			validation = {
				ok: false,
				title: t('page.flow.validationFailed'),
				errors: [e instanceof Error ? e.message : String(e)]
			};
		} finally {
			validating = false;
		}
		validateOpen = true;
	}

	async function save() {
		if (!flow || busy || !dirty) return;
		busy = true;
		error = '';
		const wasBuiltin = flow.builtin;
		try {
			const y = currentYAML();
			flow = await api.flows.save(projectId, flow.name, y, { nodes: layout });
			yaml = flow.yaml;
			const r = await api.flows.validate(flow.yaml);
			if (r.ok && r.doc) {
				doc = r.doc;
				rev++;
			}
			graphDirty = false;
			validation = r.warnings?.length
				? {
						ok: true,
						title: t('page.flow.savedWarn'),
						stages: r.stages,
						hash: flow.hash,
						warnings: r.warnings
					}
				: null;
			notice = null;
			toast(
				wasBuiltin
					? t('page.flow.savedCopy', { hash: flow.hash.slice(0, 8) })
					: t('page.flow.saved', { hash: flow.hash.slice(0, 8) })
			);
			versions = await api.flows.versions(projectId, flow.name);
		} catch (e) {
			validation = {
				ok: false,
				title: t('page.flow.saveRefused'),
				errors: [e instanceof Error ? e.message : String(e)]
			};
			validateOpen = true;
		} finally {
			busy = false;
		}
	}

	// Clone rewrites the immutable top-level name (docToYaml/templates always
	// emit `name:` at column 0) and creates a fresh flow from the current editor
	// content plus node positions. Works on built-ins too — fork a default under
	// your own name without shadowing it.
	async function clone(name: string) {
		if (!flow) return;
		const y = currentYAML().replace(/^name:[^\n]*$/m, `name: ${name}`);
		await api.flows.create(projectId, name, y, { nodes: layout });
		cloning = false;
		toast(t('page.flow.cloned', { name }));
		leaving = true;
		await goto(to.flow(projectId, name));
	}

	async function loadVersion(v: FlowVersion) {
		if (
			dirty &&
			!(await confirmDialog({
				title: t('page.flow.replaceTitle'),
				body: th('page.flow.replaceBody', { hash: v.hash.slice(0, 8) }),
				action: t('page.flow.loadVersion'),
				tone: 'warn'
			}))
		)
			return;
		try {
			const full = await api.flows.version(projectId, v.hash);
			yaml = full.yaml; // into the YAML tab; Save makes it current again
			tab = 'yaml';
			graphDirty = false;
			showVersions = false;
			notice = {
				tone: 'info',
				text: t('page.flow.loaded', { hash: v.hash.slice(0, 8) })
			};
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function remove() {
		if (!flow) return;
		const reset = flow.shadowsBuiltin;
		const ok = await confirmDialog(
			reset
				? {
						title: t('page.flow.resetTitle', { name: flow.name }),
						body: th('page.flow.resetBody', { name: flow.name }),
						action: t('page.flow.resetAction')
					}
				: {
						title: t('page.flow.deleteTitle', { name: flow.name }),
						body: th('page.flow.deleteBody', { name: flow.name }),
						action: t('page.flow.deleteAction')
					}
		);
		if (!ok) return;
		try {
			await api.flows.remove(projectId, flow.name);
			toast(
				reset
					? t('page.flow.resetDone', { name: flow.name })
					: t('page.flow.deleted', { name: flow.name })
			);
			leaving = true;
			await goto(to.workshop(projectId, 'flows'));
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	// Leaving with unsaved changes asks first (DESIGN 4.9).
	let leaving = false;
	beforeNavigate((nav) => {
		if (!dirty || leaving) return;
		if (nav.type === 'leave') {
			nav.cancel(); // the browser shows its own "leave site?" prompt
			return;
		}
		nav.cancel();
		const dest = nav.to?.url;
		void confirmDialog({
			title: t('page.flow.leaveTitle'),
			body: th('page.flow.leaveBody', { name: flow?.name ?? '' }),
			action: t('page.flow.leaveAction')
		}).then((ok) => {
			if (ok && dest) {
				leaving = true;
				void goto(dest);
			}
		});
	});

	function keydown(e: KeyboardEvent) {
		const mod = e.metaKey || e.ctrlKey;
		if (mod && e.key.toLowerCase() === 's') {
			e.preventDefault();
			void save();
			return;
		}
		const el = e.target as HTMLElement | null;
		if (e.key === 'Escape' && levels.length && !el?.closest('input, textarea, select')) {
			select(null);
			return;
		}
		if (e.key !== 'Delete' && e.key !== 'Backspace') return;
		if (el?.closest('input, textarea, select, [contenteditable]')) return;
		if (tab === 'graph' && selection) {
			e.preventDefault();
			inspector?.deleteSelection();
		}
	}

	const stageCount = $derived(doc?.stages.length ?? 0);
</script>

<svelte:window onkeydown={keydown} />

<PageHeader
	crumbs={[
		{ label: guild.name, href: to.desk() },
		{ label: workshopName, href: to.workshop(projectId) },
		{ label: t('page.workshops.flows'), href: to.workshop(projectId, 'flows') },
		{ label: page.params.name ?? '', mono: true }
	]}
>
	{#snippet children()}
		<div class="trow">
			<h1 class="mono fname">{flow?.name ?? page.params.name}</h1>
			{#if flow}<span class="tag hash" title={t('page.flow.hashTitle')}
					>@{flow.hash.slice(0, 8)}</span
				>{/if}
			{#if flow?.builtin}<span class="pill t-mut">{t('page.flow.builtin')}</span>{/if}
			{#if flow?.shadowsBuiltin}<span class="pill t-mut">{t('page.flow.fromTemplate')}</span>{/if}
			{#if flow && fleet.projectById.get(projectId)?.defaultFlow === flow.name}<span
					class="pill t-brand">{t('page.flow.default')}</span
				>{/if}
		</div>
		<div class="meta">
			{#if doc?.description}<span class="ell desc">{doc.description}</span>{/if}
			{#if doc}<span>{t('page.flow.stages', { count: stageCount })}</span>{/if}
			{#if flow}<span title={fmtAbs(flow.updatedAt)}
					>{t('page.flow.savedAgo', { ago: ago(flow.updatedAt) })}</span
				>{/if}
		</div>
	{/snippet}
	{#snippet actions()}
		{#if flow}
			<div class="vc">
				<Segmented
					options={[
						{ value: 'graph', label: t('page.flow.graph') },
						{ value: 'yaml', label: 'YAML' }
					]}
					value={tab}
					onchange={(v) => switchTab(v)}
				/>
			</div>
			<GlassGroup>
				{#snippet label()}
					{#if dirty}
						<span class="unsaved">{t('page.flow.unsaved')}</span>
					{:else}
						<span>{t('page.flow.formula')}</span>
					{/if}
				{/snippet}
				<button
					bind:this={validateBtn}
					class="btn-sm"
					class:warnb={validation && !validation.ok}
					aria-busy={validating}
					onclick={validate}
					>{t('page.flow.validate')}{#if validation?.warnings?.length}<span
							class="pill sm mono t-amb">{validation.warnings.length}</span
						>{/if}</button
				>
				<button
					class="btn-sm"
					class:btn-sel={showVersions}
					onclick={() => (showVersions = !showVersions)}
					>{t('page.flow.versions')} <span class="mono fnt">{versions.length}</span></button
				>
				<button class="btn-sm" onclick={() => (cloning = true)} disabled={busy}
					>{t('page.flow.clone')}</button
				>
				{#if flow.shadowsBuiltin}
					<button class="btn-sm btn-danger" onclick={remove}>{t('page.flow.resetAction')}…</button>
				{:else if !flow.builtin}
					<button class="btn-sm btn-danger" onclick={remove}>{t('page.ticket.delete')}</button>
				{/if}
				<button
					class="btn-sm btn-primary"
					onclick={save}
					disabled={busy || !dirty}
					aria-busy={busy}
					title={flow.builtin ? t('page.flow.saveCopyTitle') : '⌘S'}
					>{busy
						? t('page.flow.saving')
						: flow.builtin
							? t('page.flow.saveAsCopy')
							: t('common.save')}</button
				>
			</GlassGroup>
		{/if}
	{/snippet}
</PageHeader>

<Popover
	anchor={validateBtn}
	bind:open={validateOpen}
	placement="below-end"
	width={380}
	title={t('page.flow.validation')}
>
	{#if validation}
		<div class="vp">
			<div class="vh">
				<CheckChip
					tone={validation.ok ? (validation.warnings?.length ? 'warn' : 'ok') : 'bad'}
					label={validation.title}
				/>
			</div>
			{#if validation.stages !== undefined || validation.hash}
				<div class="vm">
					{#if validation.stages !== undefined}<span
							>{t('page.flow.stagesN', { count: validation.stages })}</span
						>{/if}
					{#if validation.hash}<span class="mono">@{validation.hash.slice(0, 12)}</span>{/if}
				</div>
			{/if}
			{#if validation.errors?.length}
				<ul class="vl err">
					{#each validation.errors as e, i (i)}<li class="mono">{e}</li>{/each}
				</ul>
			{/if}
			{#if validation.warnings?.length}
				<div class="vw">
					{t('page.flow.bindingWarnings')} <span class="fnt">— {t('page.flow.advisory')}</span>
				</div>
				<ul class="vl warn">
					{#each validation.warnings as w (w)}<li>{w}</li>{/each}
				</ul>
			{/if}
		</div>
	{:else}
		<div class="vp mut small">{t('page.flow.checking')}</div>
	{/if}
</Popover>

{#if flow?.builtin}
	<div class="strip">
		<Callout glyph={I.rx}>
			{@html th('page.flow.builtinCallout')}
		</Callout>
	</div>
{/if}
{#if notice}
	<div class="strip">
		<Callout
			hue={notice.tone === 'ok' ? 'grn' : 'az'}
			glyph={notice.tone === 'ok' ? I.ok : I.info}
			ondismiss={() => (notice = null)}>{notice.text}</Callout
		>
	</div>
{/if}
{#if error}
	<div class="strip">
		<ErrorCard cause={t('page.flow.wrong')} detail={error} onretry={flow ? undefined : load} />
	</div>
{/if}

<div class="fe">
	{#if !flow && !error}
		<div class="canvas"><Skeleton pane lines={6} /></div>
	{:else if flow}
		<div class="canvas" class:yaml={tab === 'yaml'}>
			{#if tab === 'graph' && doc}
				<div class="split">
					<div class="pane">
						<FlowGraph
							{doc}
							{layout}
							{rev}
							onselect={select}
							{onmove}
							{onlink}
							onarrange={autoArrange}
							{onmeasured}
							agents={doc?.agents ?? []}
							{actions}
							refit={mainRefit}
						/>
						<div class="hint fnt">
							{@html th('page.flow.hint')}
						</div>
					</div>
					{#if open && sequenceDoc}
						<section
							class="pane seq"
							aria-label={t('page.flow.sequencePane', { sequence: open.sequence.id })}
							in:slide={{ from: 'right', distance: 32 }}
						>
							<header class="bh">
								<nav class="crumbs mono">
									{#each levels as l, d (d)}
										{#if d > 0}<span class="fnt">›</span>{/if}
										{#if d < levels.length - 1}
											<button
												class="rowbtn"
												onclick={() => select({ kind: 'sequence', id: l.sequence.id })}
												>{l.sequence.id}</button
											>
										{:else}
											<span class="cur">{l.sequence.id}</span>
										{/if}
									{/each}
								</nav>
								<span class="fnt small ell grow"
									>{open.sequence.description ??
										(levels[0].stage
											? t('page.flow.sequenceRunsAs', { stage: levels[0].stage.id })
											: '')}</span
								>
								<button
									class="btn-icon"
									title={t('page.flow.sequenceClose')}
									aria-label={t('page.flow.sequenceClose')}
									onclick={() => select(null)}>{I.close}</button
								>
							</header>
							{#key paneKey}
								<FlowGraph
									doc={sequenceDoc}
									layout={sequenceLayout}
									rev={sequenceRev}
									sequence={open.view}
									readonly
									legible={0.2}
									onselect={onSequenceSelect}
									onmove={() => {}}
									onlink={() => {}}
									onmeasured={onSequenceMeasured}
									agents={doc.agents ?? []}
									{actions}
								/>
							{/key}
						</section>
					{/if}
				</div>
			{:else}
				<!-- Loaded with the tab: the graph never pays for the editor. -->
				{#await import('$lib/flow/YamlEditor.svelte') then { default: YamlEditor }}
					<YamlEditor
						bind:value={yaml}
						schema={flowSchema}
						check={checkYaml}
						label={t('page.flow.yamlLabel')}
					/>
				{/await}
			{/if}
		</div>

		{#if showVersions}
			<aside class="side lg" in:slide={{ from: 'right', distance: 24 }}>
				<div class="sh">
					<span class="serif st">{t('page.flow.versions')}</span>
					<span class="spacer"></span>
					<button
						class="btn-icon"
						aria-label={t('page.flow.closeVersions')}
						onclick={() => (showVersions = false)}>{I.close}</button
					>
				</div>
				<p class="sp">
					{t('page.flow.versionsHelp')}
				</p>
				<div class="vers">
					{#each versions as v (v.hash)}
						<div class="vr" class:cur={v.hash === flow.hash}>
							<span class="mono">@{v.hash.slice(0, 10)}</span>
							{#if v.hash === flow.hash}<span class="pill sm t-grn">{t('page.flow.current')}</span
								>{/if}
							<span class="spacer"></span>
							<span class="fnt tiny" title={fmtAbs(v.createdAt)}>{ago(v.createdAt)}</span>
							{#if v.hash !== flow.hash}
								<button class="btn-sm" onclick={() => loadVersion(v)}
									>{t('page.flow.loadIntoEditor')}</button
								>
							{/if}
						</div>
					{:else}
						<div class="fnt small">
							{flow.builtin ? t('page.flow.noVersionsBuiltin') : t('page.flow.noVersions')}
						</div>
					{/each}
				</div>
			</aside>
		{:else if tab === 'graph' && doc}
			<aside class="side lg" bind:this={side} in:slide={{ from: 'right', distance: 24 }}>
				<FlowInspector
					bind:this={inspector}
					{doc}
					{selection}
					agents={doc?.agents ?? []}
					{actions}
					{signals}
					onchange={markChanged}
					onselect={replace}
					onopen={openPanel}
					back={trail.length ? trail[trail.length - 1] : undefined}
					onback={goBack}
				/>
			</aside>
		{/if}
	{/if}
</div>

{#if cloning && flow}
	<NameFlowDialog
		title={t('page.flow.cloneTitle', { name: flow.name })}
		initial="{flow.name}-copy"
		action={t('page.flow.cloneAction')}
		taken={[flow.name]}
		onsubmit={clone}
		onclose={() => (cloning = false)}
	>
		<p>
			{dirty ? t('page.flow.cloneHelpDirty') : t('page.flow.cloneHelp')}
		</p>
	</NameFlowDialog>
{/if}

<style>
	.trow {
		display: flex;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
	}
	.fname {
		font-family: var(--mono);
		font-size: var(--fs-mark);
		font-weight: 600;
		letter-spacing: var(--tr-heading);
	}
	.hash {
		font-size: var(--fs-help);
		color: var(--mut);
	}
	.meta {
		display: flex;
		gap: 4px 14px;
		flex-wrap: wrap;
		font-size: var(--fs-label);
		color: var(--mut);
		min-width: 0;
	}
	.desc {
		max-width: 60ch;
	}
	.warnb {
		border-color: var(--rosLn);
		color: var(--ros);
	}
	.vp {
		padding: 2px 8px 8px;
		display: flex;
		flex-direction: column;
		gap: 8px;
		font-size: var(--fs-ui);
	}
	.vm {
		display: flex;
		gap: 12px;
		color: var(--mut);
		font-size: var(--fs-label);
	}
	.vl {
		margin: 0;
		padding-left: 16px;
		display: flex;
		flex-direction: column;
		gap: 4px;
		white-space: normal;
		max-height: 300px;
		overflow: auto;
	}
	.vl.err li {
		font-size: var(--fs-meta);
		color: var(--ros);
		white-space: pre-wrap;
	}
	.vl.warn li {
		font-size: var(--fs-label);
		color: var(--amb);
	}
	.vw {
		font-size: var(--fs-label);
		font-weight: 600;
	}
	.strip {
		margin: 0 16px 8px;
	}
	.fe {
		flex: 1;
		min-height: 0;
		display: flex;
		gap: 8px;
		padding: 0 16px 12px;
	}
	.canvas {
		position: relative;
		flex: 1;
		min-width: 0;
		border-radius: var(--r-pane);
		border: 1px solid var(--cLn);
		overflow: hidden;
	}
	/* The flow, and — while a sequence stage is selected — the sequence it
	 * runs, side by side. */
	.split {
		display: flex;
		height: 100%;
	}
	.pane {
		position: relative;
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
	}
	.pane :global(.graph-wrap) {
		flex: 1;
		min-height: 0;
	}
	.pane.seq {
		flex: 0 0 55%;
		border-left: 1px solid var(--cLn);
	}
	.bh {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px 8px 6px 12px;
		border-bottom: 1px solid var(--ln);
		background: var(--cBg);
	}
	.crumbs {
		display: flex;
		align-items: center;
		gap: 4px;
		font-size: var(--fs-label);
	}
	.crumbs .cur {
		color: var(--tx);
		font-weight: 600;
	}
	.canvas.yaml {
		background: var(--cBg);
		backdrop-filter: var(--cBf);
		-webkit-backdrop-filter: var(--cBf);
	}
	.vc {
		display: flex;
		align-items: center;
	}
	.hint {
		position: absolute;
		right: 14px;
		top: 10px;
		font-size: var(--fs-meta);
		pointer-events: none;
	}
	/* .bare strips the field chrome (the canvas draws it). */
	/* Wider than a form's side column: the inspector edits whole agents —
	 * prompts included — and sequences. */
	.side {
		flex: none;
		width: 450px;
		border-radius: var(--r-pane);
		overflow: auto;
	}
	.sh {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 12px 14px 8px;
	}
	.st {
		font-size: var(--fs-section);
	}
	.sp {
		margin: 0;
		padding: 0 14px 10px;
		font-size: var(--fs-help);
		color: var(--mut);
	}
	.vers {
		display: flex;
		flex-direction: column;
		padding: 0 8px 10px;
	}
	.vr {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 7px 8px;
		border-top: 1px solid var(--ln);
		font-size: var(--fs-label);
	}
	.vr.cur {
		background: var(--p2);
		border-radius: var(--r-row);
	}
	@media (max-width: 1000px) {
		.fe {
			flex-direction: column;
		}
		.canvas {
			min-height: 420px;
		}
		.side {
			width: 100%;
		}
		.split {
			flex-direction: column;
		}
		.pane.seq {
			flex-basis: 50%;
			border-left: none;
			border-top: 1px solid var(--cLn);
		}
	}
</style>
