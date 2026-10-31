// A sequence, opened beside the flow. Selecting a sequence stage of the chains
// opens the sequence it runs in a pane of its own, its chain drawn like a flow;
// a sequence selected in the inspector opens on its own. A sequence stage inside
// the sequence opens that sequence in its place — `path` is the sequence stage
// ids opened below the root.
import { t } from '$lib/i18n/index.svelte';
import {
	chainLinks,
	effectiveSuccess,
	FAIL,
	type DocEdge,
	type DocSequence,
	type DocStage,
	type FlowDoc
} from '$lib/domain/flowdoc';

/** A sequence drawn on its own: its stages as `doc.stages`, and where its
 *  two ways out lead — the including stage's success and its on_fail. */
export interface SequenceView {
	id: string;
	description?: string;
	exit: string;
	fail?: string;
}

/** What the pane has open. */
export interface Pane {
	root: string;
	sequence?: string;
	path: string[];
}

export interface Level {
	/** The stage running the sequence; none for one opened on its own. */
	stage?: DocStage;
	sequence: DocSequence;
	view: SequenceView;
}

const failOf = (e: DocEdge | undefined, outer?: string) => {
	const to = chainLinks(e)[0]?.to;
	return to === FAIL ? outer : to;
};

/** The sequence a stage runs, when it is a sequence stage. */
export const sequenceOf = (doc: FlowDoc | null, st: DocStage | undefined) =>
	st?.type === 'sequence' ? doc?.sequences?.find((q) => q.id === st.sequence) : undefined;

/** The sequences a pane has open, outermost first. */
export function levelsOf(doc: FlowDoc, p: Pane): Level[] {
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
		const seq = sequenceOf(doc, st);
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
		const q = sequenceOf(doc, s);
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
}
