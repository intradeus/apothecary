// Review documents as the curation page gets them: valid and boring, so a test
// states only what it is about.
import type {
	CurationDocument,
	CurationView,
	Ping,
	ReviewContinuation,
	ReviewDocument,
	ReviewNote,
	ReviewReply,
	ReviewThread,
	ThreadComment
} from '$lib/domain';
import { initialState, itemsOf, type CurationState, type ItemState } from '$lib/curation/curation';
import { aPing } from '$test/fixtures';

export const REVIEW_PATH = '.apothecary/review.yaml';
export const REPLIES_PATH = '.apothecary/replies.yaml';
export const THREADS_PATH = '.apothecary/threads.yaml';

export const aNote = (over: Partial<ReviewNote> = {}): ReviewNote => ({
	path: 'src/pay.go',
	line: 12,
	kind: 'blocking',
	body: 'Retry without a key charges twice.',
	...over
});

export const aContinuation = (over: Partial<ReviewContinuation> = {}): ReviewContinuation => ({
	thread: 'th-1',
	body: 'Still happens on the second path.',
	resolve: false,
	...over
});

export const aReply = (over: Partial<ReviewReply> = {}): ReviewReply => ({
	thread: 'th-1',
	did: 'fixed',
	reply: 'Added the key in the retry path.',
	resolve: true,
	...over
});

export const aReview = (over: Partial<ReviewDocument> = {}): ReviewDocument => ({
	summary: 'Two problems in the webhook.',
	notes: [],
	replies: [],
	...over
});

/** A review_threads document, captured, as the gate showed it. */
export const aReviewDoc = (
	review: Partial<ReviewDocument> = {},
	over: Partial<CurationDocument> = {}
): CurationDocument => ({
	path: REVIEW_PATH,
	type: 'review_threads',
	captured: true,
	review: aReview(review),
	...over
});

/** A review_replies document, captured, as the gate showed it. */
export const aRepliesDoc = (
	replies: ReviewReply[] = [],
	over: Partial<CurationDocument> = {}
): CurationDocument => ({
	path: REPLIES_PATH,
	type: 'review_replies',
	captured: true,
	replies,
	...over
});

export const aComment = (over: Partial<ThreadComment> = {}): ThreadComment => ({
	id: 'c-1',
	author: 'ada',
	ours: false,
	new: false,
	body: 'Why is this retried?',
	...over
});

export const aThread = (over: Partial<ReviewThread> = {}): ReviewThread => ({
	id: 'th-1',
	path: 'src/pay.go',
	line: 12,
	resolved: false,
	ours: false,
	comments: [aComment()],
	...over
});

export const aView = (
	doc: CurationDocument,
	threads?: Partial<NonNullable<CurationView['threads']>>
): CurationView => ({
	documents: [doc],
	...(threads ? { threads: { threads: [], dropped: [], ...threads } } : {})
});

/** A gate over a review document (or a round's replies, by the document's
 *  type), with whatever other files it shows beside it. */
export const aCuratedPing = (
	doc: Pick<CurationDocument, 'path' | 'type'> = { path: REVIEW_PATH, type: 'review_threads' },
	over: Partial<Ping> = {}
): Ping =>
	aPing({
		id: 'ping-curate',
		files: [doc.path],
		fileTypes: { [doc.path]: doc.type },
		raisedAt: '2026-10-31T12:00:00Z',
		...over
	});

/** The state a fresh page holds for a document, with some of its items edited. */
export function aState(
	doc: CurationDocument,
	items: Record<string, Partial<ItemState>> = {},
	over: Partial<Omit<CurationState, 'items'>> = {}
): CurationState {
	const st = initialState(doc, itemsOf(doc));
	for (const [key, edit] of Object.entries(items)) st.items[key] = { ...st.items[key], ...edit };
	return { ...st, ...over };
}
