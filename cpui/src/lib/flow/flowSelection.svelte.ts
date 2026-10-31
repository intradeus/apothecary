// What the flow editor has selected, how it got there, and the sequence open
// beside the flow: the selection is what the pane follows, so the three move
// together.
import { columnLayout, type FlowDoc, type NodeSize } from '$lib/domain/flowdoc';
import type { FlowEditor } from '$lib/flow/flowEditor.svelte';
import { renameInTrail, selectionExists, type Selection } from '$lib/flow/graph/selection';
import { levelsOf, sequenceOf, type Level, type Pane } from '$lib/flow/graph/sequence';

export class FlowSelection {
	selection = $state<Selection>(null);

	// Where a panel was opened from: opening one panel from another — a stage's
	// agent, a sequence's stage — leaves a trail, and every panel offers the way
	// back to the one before. Selecting on the canvas starts afresh.
	trail = $state<Selection[]>([]);

	// A stage or an edge of the open sequence is edited in place, the pane
	// staying open; selecting anything else (or the empty canvas) closes it.
	pane = $state<Pane | null>(null);
	sequenceLayout = $state<Record<string, { x: number; y: number }>>({});
	sequenceRev = $state(0);

	#sequenceSizes: Record<string, NodeSize> = {};
	readonly #ed: FlowEditor;

	constructor(ed: FlowEditor) {
		this.#ed = ed;
	}

	readonly levels = $derived.by((): Level[] => {
		const p = this.pane;
		if (!this.#ed.doc || this.#ed.tab !== 'graph' || !p) return [];
		return levelsOf(this.#ed.doc, p);
	});
	readonly open = $derived(this.levels.at(-1));
	// The open sequence as a document of its own: the flow's agents,
	// parameters and sequences, the sequence's stages.
	readonly sequenceDoc = $derived.by((): FlowDoc | null => {
		const doc = this.#ed.doc;
		return doc && this.open
			? { ...doc, phases: undefined, stages: this.open.sequence.stages }
			: null;
	});
	// The pane is drawn afresh when it shows another sequence or the open
	// one gains, loses or renames a stage — its nodes measured again.
	readonly paneKey = $derived(
		this.open
			? `${this.levels.map((l) => l.sequence.id).join('/')}|${this.open.sequence.stages.map((s) => s.id).join(',')}`
			: ''
	);

	openPanel = (sel: Selection) => {
		if (sel?.kind === 'agent') return this.#ed.openAgent(sel.name);
		this.trail = [...this.trail, this.selection];
		this.select(sel, true);
	};

	goBack = () => {
		let rest = this.trail;
		while (rest.length) {
			const prev = rest[rest.length - 1];
			rest = rest.slice(0, -1);
			if (selectionExists(this.#ed.doc, prev)) {
				this.trail = rest;
				this.select(prev, true);
				return;
			}
		}
		this.trail = [];
		this.select(null);
	};

	// What the inspector asks for: another selection in this one's place,
	// the trail kept (renamed along with it).
	replace = (sel: Selection, how?: 'rename') => {
		if (how === 'rename') this.trail = renameInTrail(this.trail, this.selection, sel);
		this.select(sel, true);
	};

	// select is every selection's way in — the canvas, the pane, the
	// inspector — so the pane follows it. From the canvas (`keep` unset) it
	// starts the trail afresh.
	select = (sel: Selection, keep = false) => {
		if (sel?.kind === 'agent') return this.#ed.openAgent(sel.name);
		if (!keep) this.trail = [];
		this.selection = sel;
		if (!this.#ed.doc) return;
		if (sel?.kind === 'stage' && !sel.sequence) {
			const st = this.#ed.doc.stages.find((s) => s.id === sel.id);
			if (st?.type !== 'sequence') this.pane = null;
			else if (this.pane?.root !== st.id || this.pane.sequence)
				this.pane = { root: st.id, path: [] };
			return;
		}
		const seq =
			sel?.kind === 'sequence'
				? sel.id
				: sel?.kind === 'stage' || sel?.kind === 'edge'
					? sel.sequence
					: undefined;
		if (!seq) {
			this.pane = null;
			return;
		}
		// Already open, or one of the sequences above it: show that one.
		const at = this.levels.findIndex((l) => l.sequence.id === seq);
		if (at >= 0 && this.pane) this.pane = { ...this.pane, path: this.pane.path.slice(0, at) };
		else this.pane = { root: '', sequence: seq, path: [] };
	};

	relayoutSequence() {
		if (!this.sequenceDoc) return;
		this.sequenceLayout = columnLayout(this.sequenceDoc, this.#sequenceSizes);
		this.sequenceRev++;
	}
	/** The pane shows something else: lay it out without the old sizes. */
	resetSequenceLayout() {
		this.#sequenceSizes = {};
		this.relayoutSequence();
	}
	onSequenceMeasured = (sizes: Record<string, NodeSize>) => {
		this.#sequenceSizes = sizes;
		this.relayoutSequence();
	};
	// A click in the pane edits what it lands on in its sequence; the
	// marker, the ways out and the empty canvas select the sequence itself.
	onSequenceSelect = (sel: Selection) => {
		if (!this.open || !this.pane) return;
		this.trail = [];
		const seq = this.open.sequence.id;
		if (sel?.kind === 'stage') {
			this.selection = { kind: 'stage', id: sel.id, sequence: seq };
			const s = this.open.sequence.stages.find((x) => x.id === sel.id);
			if (sequenceOf(this.#ed.doc, s))
				this.pane = { ...this.pane, path: [...this.pane.path, sel.id] };
		} else if (sel?.kind === 'edge') this.selection = { ...sel, sequence: seq };
		else this.selection = { kind: 'sequence', id: seq };
	};
}
