// The curation page's pure helpers (TODO 45): a gate over a review document
// — a reviewer's notes before post_review posts them, a round's replies
// before reply_review_comments — flattened into items a person keeps, drops
// or rewords. The page collects DECISIONS over the document as the gate
// showed it (by item key); the engine rebuilds the file from its raise-time
// snapshot and those, so what is posted is provably a subset, reworded, of
// what the person saw. All selected is the default: unpicking is the
// deliberate act.
import type { Curation, CuratedItem, CurationDocument, ReviewThread } from '$lib/domain';
import type { Hue } from '$lib/ui/hues';
import { t } from '$lib/i18n/index.svelte';

/** A note's kind, a reply's did: its hue, and its word. */
const KIND_HUE: Record<string, Hue> = {
	blocking: 'ros',
	suggestion: 'az',
	question: 'amb',
	nit: 'mut',
	fixed: 'grn',
	declined: 'ros',
	answered: 'az'
};
export const kindHue = (kind: string): Hue => KIND_HUE[kind] ?? 'mut';
export function kindLabel(kind: string): string {
	switch (kind) {
		case 'blocking':
			return t('cur.kind.blocking');
		case 'suggestion':
			return t('cur.kind.suggestion');
		case 'question':
			return t('cur.kind.question');
		case 'nit':
			return t('cur.kind.nit');
		case 'fixed':
			return t('cur.kind.fixed');
		case 'declined':
			return t('cur.kind.declined');
		case 'answered':
			return t('cur.kind.answered');
	}
	return kind;
}

export type ItemList = 'notes' | 'replies';

/** One item of the document as shown: a note (opens a thread), a reply the
 *  reviewer writes in a thread (a review's `replies`), or a round's reply. */
export interface CurItem {
	/** "notes/<i>" or "replies/<i>": what the answer names. */
	key: string;
	list: ItemList;
	index: number;
	/** A note's kind, a round reply's did; "" for a reviewer's continuation. */
	kind: string;
	path?: string;
	line?: number;
	/** The thread a reply is in. */
	thread?: string;
	/** The posted text, as written. */
	text: string;
	/** The resolve flag as written; only a reply has one. */
	resolve?: boolean;
	canResolve: boolean;
	rationale?: string;
	evidence?: string;
	example?: string;
}

/** The document's items, in the order the agent wrote them. */
export function itemsOf(doc: CurationDocument): CurItem[] {
	const out: CurItem[] = [];
	if (doc.review) {
		doc.review.notes.forEach((n, i) =>
			out.push({
				key: `notes/${i}`,
				list: 'notes',
				index: i,
				kind: n.kind,
				path: n.path,
				line: n.line,
				text: n.body,
				canResolve: false,
				rationale: n.rationale,
				evidence: n.evidence,
				example: n.example
			})
		);
		doc.review.replies.forEach((r, i) =>
			out.push({
				key: `replies/${i}`,
				list: 'replies',
				index: i,
				kind: '',
				thread: r.thread,
				text: r.body,
				resolve: r.resolve,
				canResolve: true,
				rationale: r.rationale
			})
		);
	}
	if (doc.replies) {
		doc.replies.forEach((r, i) =>
			out.push({
				key: `replies/${i}`,
				list: 'replies',
				index: i,
				kind: r.did,
				thread: r.thread,
				text: r.reply,
				resolve: r.resolve,
				canResolve: true,
				rationale: r.rationale,
				evidence: r.evidence
			})
		);
	}
	return out;
}

/** Where an item sits, as a person reads it. */
export function whereOf(it: CurItem): string {
	if (it.path) return it.line ? `${it.path}:${it.line}` : it.path;
	return '';
}

/** One item as the page holds it: kept or not, its text and flag as edited. */
export interface ItemState {
	keep: boolean;
	body: string;
	resolve: boolean;
}

/** The page's whole draft over one document. */
export interface CurationState {
	summary: string;
	items: Record<string, ItemState>;
	feedback: string;
}

/** Every item kept as written: the state a fresh page starts from. */
export function initialState(doc: CurationDocument, items: CurItem[]): CurationState {
	const st: CurationState = { summary: doc.review?.summary ?? '', items: {}, feedback: '' };
	for (const it of items)
		st.items[it.key] = { keep: true, body: it.text, resolve: it.resolve ?? false };
	return st;
}

// ---- drafts (this browser only, best effort) --------------------------------

const draftKey = (pingId: string) => `gate-curation-${pingId}`;

/** The saved draft folded over a fresh state: only items the document still
 *  has are read back, so a stale draft never names an item that is not shown. */
export function loadDraft(pingId: string, fresh: CurationState): CurationState {
	try {
		const raw = localStorage.getItem(draftKey(pingId));
		if (!raw) return fresh;
		const d = JSON.parse(raw) as Partial<CurationState> | null;
		if (!d || typeof d !== 'object') return fresh;
		const out: CurationState = {
			summary: typeof d.summary === 'string' ? d.summary : fresh.summary,
			feedback: typeof d.feedback === 'string' ? d.feedback : '',
			items: { ...fresh.items }
		};
		for (const [key, base] of Object.entries(fresh.items)) {
			const s = d.items?.[key];
			if (!s || typeof s !== 'object') continue;
			out.items[key] = {
				keep: typeof s.keep === 'boolean' ? s.keep : base.keep,
				body: typeof s.body === 'string' ? s.body : base.body,
				resolve: typeof s.resolve === 'boolean' ? s.resolve : base.resolve
			};
		}
		return out;
	} catch {
		return fresh;
	}
}

export function saveDraft(pingId: string, st: CurationState) {
	try {
		localStorage.setItem(draftKey(pingId), JSON.stringify(st));
	} catch {
		/* private mode etc. — drafts are best-effort */
	}
}

export function clearDraft(pingId: string) {
	try {
		localStorage.removeItem(draftKey(pingId));
	} catch {
		/* ignore */
	}
}

// ---- bulk picks --------------------------------------------------------------

export type Bulk = 'all' | 'none' | 'dropNits';

/** A toolbar pick over every item: keep all, keep none, or drop the nits. */
export function bulk(st: CurationState, items: CurItem[], mode: Bulk): CurationState {
	const out: CurationState = { ...st, items: { ...st.items } };
	for (const it of items) {
		const s = out.items[it.key] ?? { keep: true, body: it.text, resolve: it.resolve ?? false };
		const keep =
			mode === 'all' ? true : mode === 'none' ? false : it.kind === 'nit' ? false : s.keep;
		out.items[it.key] = { ...s, keep };
	}
	return out;
}

// ---- what is sent -----------------------------------------------------------

/** The decisions the answer carries: items dropped, items reworded (text or
 *  flag), the summary when reworded — nothing for an item kept as written.
 *  Undefined when the person changed nothing: approved as written. */
export function toCuration(
	doc: CurationDocument,
	items: CurItem[],
	st: CurationState
): Curation | undefined {
	const decided: CuratedItem[] = [];
	for (const it of items) {
		const s = st.items[it.key];
		if (!s) continue;
		if (!s.keep) {
			decided.push({ item: it.key, keep: false });
			continue;
		}
		const body = s.body.trim();
		const reworded = body !== it.text.trim();
		const flagged = it.canResolve && s.resolve !== (it.resolve ?? false);
		if (!reworded && !flagged) continue;
		decided.push({
			item: it.key,
			keep: true,
			...(reworded && body ? { body } : {}),
			...(flagged ? { resolve: s.resolve } : {})
		});
	}
	const summary =
		doc.review && st.summary.trim() !== (doc.review.summary ?? '').trim()
			? st.summary.trim()
			: undefined;
	if (!decided.length && summary === undefined) return undefined;
	return { ...(summary !== undefined ? { summary } : {}), items: decided };
}

/** The kept items whose text is blank: the engine refuses those, so the
 *  page says so before sending. */
export function blankKept(items: CurItem[], st: CurationState): CurItem[] {
	return items.filter((it) => st.items[it.key]?.keep && !st.items[it.key].body.trim());
}

// ---- counting ----------------------------------------------------------------

export interface Tally {
	kept: number;
	total: number;
	dropped: number;
	reworded: number;
	summaryReworded: boolean;
}

/** What the draft amounts to. */
export function tally(doc: CurationDocument, items: CurItem[], st: CurationState): Tally {
	const out: Tally = {
		kept: 0,
		total: items.length,
		dropped: 0,
		reworded: 0,
		summaryReworded: false
	};
	for (const it of items) {
		const s = st.items[it.key];
		if (!s || s.keep) out.kept++;
		else out.dropped++;
		if (
			s &&
			s.keep &&
			(s.body.trim() !== it.text.trim() || (it.canResolve && s.resolve !== (it.resolve ?? false)))
		)
			out.reworded++;
	}
	out.summaryReworded = !!doc.review && st.summary.trim() !== (doc.review.summary ?? '').trim();
	return out;
}

/** The decision a recorded curation holds for an item, if any. */
export function decisionOf(c: Curation | undefined, key: string): CuratedItem | undefined {
	return c?.items.find((d) => d.item === key);
}

/** What a recorded curation amounted to (the history). */
export function appliedTally(items: CurItem[], c: Curation | undefined): Tally {
	const out: Tally = {
		kept: 0,
		total: items.length,
		dropped: 0,
		reworded: 0,
		summaryReworded: c?.summary !== undefined
	};
	for (const it of items) {
		const d = decisionOf(c, it.key);
		if (d && !d.keep) out.dropped++;
		else out.kept++;
		if (d && d.keep && (d.body || d.resolve !== undefined)) out.reworded++;
	}
	return out;
}

/** The thread an item is in, among the threads shown. */
export const threadOf = (threads: ReviewThread[] | undefined, id?: string) =>
	id ? threads?.find((th) => th.id === id) : undefined;

/** A thread id short enough for a pill. */
export const shortThread = (id: string) =>
	id.length > 16 ? `${id.slice(0, 7)}…${id.slice(-5)}` : id;

/** How many rows a text needs, within reason. */
export const rowsFor = (text: string) =>
	Math.min(14, Math.max(2, (text.match(/\n/g)?.length ?? 0) + 1 + Math.floor(text.length / 90)));
