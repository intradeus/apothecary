import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Curation } from '$lib/domain';
import {
	aContinuation,
	aNote,
	aReply,
	aRepliesDoc,
	aReviewDoc,
	aState,
	aThread
} from '$test/fixtures/curation';
import {
	appliedTally,
	blankKept,
	bulk,
	clearDraft,
	decisionOf,
	initialState,
	itemsOf,
	kindHue,
	kindLabel,
	loadDraft,
	rowsFor,
	saveDraft,
	shortThread,
	tally,
	threadOf,
	toCuration,
	whereOf,
	type CurItem
} from './curation';

const DRAFT_KEY = 'gate-curation-p1';

/** A review with one of each: a blocking note, a nit, a continuation. */
const reviewDoc = () =>
	aReviewDoc({
		notes: [
			aNote({ body: 'Charges twice.', rationale: 'r', evidence: 'e', example: 'x' }),
			aNote({ kind: 'nit', path: 'src/a.go', line: 3, body: 'Typo.' })
		],
		replies: [aContinuation({ body: 'Still open.', resolve: false, rationale: 'because' })]
	});

const repliesDoc = () =>
	aRepliesDoc([
		aReply({ reply: 'Fixed it.', resolve: true, rationale: 'r', evidence: 'e' }),
		aReply({ thread: 'th-2', did: 'declined', reply: 'Out of scope.', resolve: false })
	]);

describe('kindHue and kindLabel', () => {
	it.each([
		['blocking', 'danger', 'blocking'],
		['suggestion', 'attention', 'suggestion'],
		['question', 'warning', 'question'],
		['nit', 'muted', 'nit'],
		['fixed', 'success', 'fixed'],
		['declined', 'danger', 'declined'],
		['answered', 'attention', 'answered']
	])('reads %s as %s, worded %s', (kind, hue, label) => {
		expect(kindHue(kind)).toBe(hue);
		expect(kindLabel(kind)).toBe(label);
	});

	it('falls back to muted and the raw word for a kind it does not know', () => {
		expect(kindHue('praise')).toBe('muted');
		expect(kindLabel('praise')).toBe('praise');
	});
});

describe('itemsOf', () => {
	it('lists a review as its notes, then its continuations', () => {
		const items = itemsOf(reviewDoc());
		expect(items.map((i) => i.key)).toEqual(['notes/0', 'notes/1', 'replies/0']);
		expect(items.map((i) => i.index)).toEqual([0, 1, 0]);
		expect(items.map((i) => i.list)).toEqual(['notes', 'notes', 'replies']);
	});

	it('carries a note as written, with no resolve flag to change', () => {
		expect(itemsOf(reviewDoc())[0]).toEqual({
			key: 'notes/0',
			list: 'notes',
			index: 0,
			kind: 'blocking',
			path: 'src/pay.go',
			line: 12,
			text: 'Charges twice.',
			canResolve: false,
			rationale: 'r',
			evidence: 'e',
			example: 'x'
		});
	});

	it('carries a continuation with no kind, in its thread, with its flag', () => {
		expect(itemsOf(reviewDoc())[2]).toEqual({
			key: 'replies/0',
			list: 'replies',
			index: 0,
			kind: '',
			thread: 'th-1',
			text: 'Still open.',
			resolve: false,
			canResolve: true,
			rationale: 'because'
		});
	});

	it("takes a round reply's kind from what it did", () => {
		const [fixed, declined] = itemsOf(repliesDoc());
		expect(fixed).toEqual({
			key: 'replies/0',
			list: 'replies',
			index: 0,
			kind: 'fixed',
			thread: 'th-1',
			text: 'Fixed it.',
			resolve: true,
			canResolve: true,
			rationale: 'r',
			evidence: 'e'
		});
		expect(declined.key).toBe('replies/1');
		expect(declined.kind).toBe('declined');
	});

	it('finds nothing in a document with neither', () => {
		expect(itemsOf({ path: 'x', type: 'review_threads', captured: false })).toEqual([]);
	});
});

describe('whereOf', () => {
	it.each<[Partial<CurItem>, string]>([
		[{ path: 'a.go', line: 4 }, 'a.go:4'],
		[{ path: 'a.go' }, 'a.go'],
		[{ path: 'a.go', line: 0 }, 'a.go'],
		[{ thread: 'th-1' }, '']
	])('reads %j as "%s"', (it_, where) => {
		expect(whereOf(it_ as CurItem)).toBe(where);
	});
});

describe('initialState', () => {
	it('starts with every item kept as written and the summary as written', () => {
		const doc = reviewDoc();
		expect(initialState(doc, itemsOf(doc))).toEqual({
			summary: 'Two problems in the webhook.',
			feedback: '',
			items: {
				'notes/0': { keep: true, body: 'Charges twice.', resolve: false },
				'notes/1': { keep: true, body: 'Typo.', resolve: false },
				'replies/0': { keep: true, body: 'Still open.', resolve: false }
			}
		});
	});

	it("keeps a reply's resolve flag, and has no summary on a round", () => {
		const doc = repliesDoc();
		const st = initialState(doc, itemsOf(doc));
		expect(st.summary).toBe('');
		expect(st.items['replies/0'].resolve).toBe(true);
		expect(st.items['replies/1'].resolve).toBe(false);
	});
});

describe('drafts', () => {
	const doc = reviewDoc();
	const fresh = () => initialState(doc, itemsOf(doc));

	beforeEach(() => localStorage.clear());

	it('reads back what was saved', () => {
		const st = aState(
			doc,
			{ 'notes/0': { keep: false, body: 'Mine.' } },
			{ summary: 'S', feedback: 'F' }
		);
		saveDraft('p1', st);
		expect(loadDraft('p1', fresh())).toEqual(st);
	});

	it("keeps each ping's draft apart", () => {
		saveDraft('p1', aState(doc, {}, { feedback: 'for p1' }));
		expect(loadDraft('p2', fresh())).toEqual(fresh());
	});

	it('forgets a cleared draft', () => {
		saveDraft('p1', aState(doc, {}, { feedback: 'gone' }));
		clearDraft('p1');
		expect(localStorage.getItem(DRAFT_KEY)).toBeNull();
		expect(loadDraft('p1', fresh())).toEqual(fresh());
	});

	it.each([
		['not JSON', '{oops'],
		['null', 'null'],
		['a number', '12'],
		['a string', '"draft"']
	])('starts fresh from a draft that is %s', (_, raw) => {
		localStorage.setItem(DRAFT_KEY, raw);
		expect(loadDraft('p1', fresh())).toEqual(fresh());
	});

	it('drops an item the document no longer has', () => {
		localStorage.setItem(
			DRAFT_KEY,
			JSON.stringify({ items: { 'notes/9': { keep: false, body: 'stale', resolve: true } } })
		);
		expect(Object.keys(loadDraft('p1', fresh()).items)).toEqual([
			'notes/0',
			'notes/1',
			'replies/0'
		]);
	});

	it('keeps the written value for each field of the wrong type', () => {
		localStorage.setItem(
			DRAFT_KEY,
			JSON.stringify({
				summary: 7,
				feedback: false,
				items: {
					'notes/0': { keep: 'no', body: 3, resolve: 'yes' },
					'notes/1': 'junk',
					'replies/0': { keep: false }
				}
			})
		);
		expect(loadDraft('p1', fresh())).toEqual({
			summary: 'Two problems in the webhook.',
			feedback: '',
			items: {
				'notes/0': { keep: true, body: 'Charges twice.', resolve: false },
				'notes/1': { keep: true, body: 'Typo.', resolve: false },
				'replies/0': { keep: false, body: 'Still open.', resolve: false }
			}
		});
	});

	it('leaves the items alone when the draft has none', () => {
		localStorage.setItem(DRAFT_KEY, JSON.stringify({ feedback: 'just this' }));
		expect(loadDraft('p1', fresh())).toEqual({ ...fresh(), feedback: 'just this' });
	});

	it('survives storage that refuses every call', () => {
		const refuse = () => {
			throw new Error('blocked');
		};
		vi.spyOn(Storage.prototype, 'getItem').mockImplementation(refuse);
		vi.spyOn(Storage.prototype, 'setItem').mockImplementation(refuse);
		vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(refuse);

		expect(loadDraft('p1', fresh())).toEqual(fresh());
		expect(() => saveDraft('p1', fresh())).not.toThrow();
		expect(() => clearDraft('p1')).not.toThrow();
	});
});

describe('bulk', () => {
	const doc = reviewDoc();
	const items = itemsOf(doc);
	const keeps = (st: ReturnType<typeof aState>) =>
		Object.fromEntries(Object.entries(st.items).map(([k, v]) => [k, v.keep]));

	it.each([
		[
			'all',
			{ 'notes/0': false, 'notes/1': false, 'replies/0': false },
			{ 'notes/0': true, 'notes/1': true, 'replies/0': true }
		],
		[
			'none',
			{ 'notes/0': true, 'notes/1': true, 'replies/0': true },
			{ 'notes/0': false, 'notes/1': false, 'replies/0': false }
		],
		// An item already dropped stays dropped: only the nits are touched.
		[
			'dropNits',
			{ 'notes/0': false, 'notes/1': true, 'replies/0': true },
			{ 'notes/0': false, 'notes/1': false, 'replies/0': true }
		]
	] as const)('%s turns %j into %j', (mode, before, after) => {
		const st = aState(doc, {
			'notes/0': { keep: before['notes/0'] },
			'notes/1': { keep: before['notes/1'] },
			'replies/0': { keep: before['replies/0'] }
		});
		expect(keeps(bulk(st, items, mode))).toEqual(after);
	});

	it("leaves a kept item's edits, the summary and the feedback alone", () => {
		const st = aState(
			doc,
			{ 'notes/0': { body: 'Mine.', resolve: true } },
			{ summary: 'S', feedback: 'F' }
		);
		const out = bulk(st, items, 'none');
		expect(out.items['notes/0']).toEqual({ keep: false, body: 'Mine.', resolve: true });
		expect(out.summary).toBe('S');
		expect(out.feedback).toBe('F');
	});

	it('does not touch the state it is given', () => {
		const st = aState(doc);
		bulk(st, items, 'none');
		expect(st.items['notes/0'].keep).toBe(true);
	});

	it('fills in an item the state does not hold yet, as written', () => {
		const st = aState(doc);
		delete st.items['replies/0'];
		expect(bulk(st, items, 'none').items['replies/0']).toEqual({
			keep: false,
			body: 'Still open.',
			resolve: false
		});
	});
});

describe('toCuration', () => {
	const doc = reviewDoc();
	const items = itemsOf(doc);

	it('says nothing at all for a draft left as written', () => {
		expect(toCuration(doc, items, aState(doc))).toBeUndefined();
	});

	it.each<[string, Parameters<typeof aState>[1], Curation['items']]>([
		['a dropped item', { 'notes/0': { keep: false } }, [{ item: 'notes/0', keep: false }]],
		[
			'a dropped item, ignoring its edits',
			{ 'notes/0': { keep: false, body: 'Mine.' } },
			[{ item: 'notes/0', keep: false }]
		],
		[
			'a reworded item, trimmed',
			{ 'notes/1': { body: '  Better.  ' } },
			[{ item: 'notes/1', keep: true, body: 'Better.' }]
		],
		[
			'a changed resolve flag, with no body',
			{ 'replies/0': { resolve: true } },
			[{ item: 'replies/0', keep: true, resolve: true }]
		],
		[
			'a reworded reply with a changed flag',
			{ 'replies/0': { body: 'New.', resolve: true } },
			[{ item: 'replies/0', keep: true, body: 'New.', resolve: true }]
		],
		// The page blocks this; the engine still never gets an empty body.
		[
			'a blanked item, with no body',
			{ 'notes/0': { body: '   ' } },
			[{ item: 'notes/0', keep: true }]
		],
		[
			'several, in document order',
			{ 'replies/0': { keep: false }, 'notes/0': { body: 'First.' } },
			[
				{ item: 'notes/0', keep: true, body: 'First.' },
				{ item: 'replies/0', keep: false }
			]
		]
	])('records %s', (_, edit, decided) => {
		expect(toCuration(doc, items, aState(doc, edit))).toEqual({ items: decided });
	});

	it.each<[string, Parameters<typeof aState>[1]]>([
		['whitespace around the same text', { 'notes/0': { body: '\n Charges twice. \t' } }],
		['a resolve flag on a note, which has none', { 'notes/0': { resolve: true } }]
	])('ignores %s', (_, edit) => {
		expect(toCuration(doc, items, aState(doc, edit))).toBeUndefined();
	});

	it('counts a text the agent wrote with stray spaces as unchanged', () => {
		const padded = aReviewDoc({ notes: [aNote({ body: '  Padded.  ' })] });
		const st = aState(padded, { 'notes/0': { body: 'Padded.' } });
		expect(toCuration(padded, itemsOf(padded), st)).toBeUndefined();
	});

	it('carries a reworded summary, trimmed, on its own', () => {
		expect(toCuration(doc, items, aState(doc, {}, { summary: ' Shorter. ' }))).toEqual({
			summary: 'Shorter.',
			items: []
		});
	});

	it('carries an emptied summary, which is a decision too', () => {
		expect(toCuration(doc, items, aState(doc, {}, { summary: '  ' }))).toEqual({
			summary: '',
			items: []
		});
	});

	it('ignores spaces around the summary', () => {
		expect(
			toCuration(doc, items, aState(doc, {}, { summary: ' Two problems in the webhook. ' }))
		).toBeUndefined();
	});

	it('has no summary to change on a round of replies', () => {
		const round = repliesDoc();
		expect(
			toCuration(round, itemsOf(round), aState(round, {}, { summary: 'Sneaked in' }))
		).toBeUndefined();
	});

	it('flags a round reply against the flag it was written with', () => {
		const round = repliesDoc();
		expect(
			toCuration(round, itemsOf(round), aState(round, { 'replies/0': { resolve: false } }))
		).toEqual({ items: [{ item: 'replies/0', keep: true, resolve: false }] });
	});

	it('skips an item the state does not hold', () => {
		const st = aState(doc);
		delete st.items['notes/0'];
		expect(toCuration(doc, items, st)).toBeUndefined();
	});
});

describe('blankKept', () => {
	const doc = reviewDoc();
	const items = itemsOf(doc);

	it.each<[string, Parameters<typeof aState>[1], string[]]>([
		['nothing when every text is there', {}, []],
		['a kept item with no text', { 'notes/0': { body: '' } }, ['notes/0']],
		['a kept item with only spaces', { 'notes/1': { body: ' \n ' } }, ['notes/1']],
		['nothing for a blank that is dropped', { 'notes/0': { body: '', keep: false } }, []]
	])('finds %s', (_, edit, keys) => {
		expect(blankKept(items, aState(doc, edit)).map((i) => i.key)).toEqual(keys);
	});

	it('finds nothing for an item the state does not hold', () => {
		const st = aState(doc);
		delete st.items['notes/0'];
		expect(blankKept(items, st)).toEqual([]);
	});
});

describe('tally', () => {
	const doc = reviewDoc();
	const items = itemsOf(doc);

	it.each<
		[string, Parameters<typeof aState>[1], { kept: number; dropped: number; reworded: number }]
	>([
		['all kept', {}, { kept: 3, dropped: 0, reworded: 0 }],
		['one dropped', { 'notes/0': { keep: false } }, { kept: 2, dropped: 1, reworded: 0 }],
		['one reworded', { 'notes/1': { body: 'New.' } }, { kept: 3, dropped: 0, reworded: 1 }],
		['a changed flag', { 'replies/0': { resolve: true } }, { kept: 3, dropped: 0, reworded: 1 }],
		[
			'a flag on a note, which is nothing',
			{ 'notes/0': { resolve: true } },
			{ kept: 3, dropped: 0, reworded: 0 }
		],
		[
			'spaces, which are nothing',
			{ 'notes/0': { body: ' Charges twice. ' } },
			{ kept: 3, dropped: 0, reworded: 0 }
		],
		[
			'a reworded item that was dropped, as dropped only',
			{ 'notes/0': { keep: false, body: 'New.' } },
			{ kept: 2, dropped: 1, reworded: 0 }
		]
	])('counts %s', (_, edit, want) => {
		expect(tally(doc, items, aState(doc, edit))).toEqual({
			...want,
			total: 3,
			summaryReworded: false
		});
	});

	it('counts an item the state does not hold as kept', () => {
		const st = aState(doc);
		delete st.items['notes/0'];
		expect(tally(doc, items, st).kept).toBe(3);
	});

	it.each([
		['Shorter.', true],
		['  Two problems in the webhook.  ', false],
		['', true]
	])('summary "%s" reworded: %s', (summary, reworded) => {
		expect(tally(doc, items, aState(doc, {}, { summary })).summaryReworded).toBe(reworded);
	});

	it('has no summary to reword on a round', () => {
		const round = repliesDoc();
		expect(tally(round, itemsOf(round), aState(round, {}, { summary: 'x' })).summaryReworded).toBe(
			false
		);
	});
});

describe('decisionOf', () => {
	const c: Curation = { items: [{ item: 'notes/0', keep: false }] };

	it('finds the decision on an item', () => {
		expect(decisionOf(c, 'notes/0')).toEqual({ item: 'notes/0', keep: false });
	});

	it.each([
		['an item with no decision', c, 'notes/1'],
		['no curation', undefined, 'notes/0']
	])('finds nothing for %s', (_, curation, key) => {
		expect(decisionOf(curation, key)).toBeUndefined();
	});
});

describe('appliedTally', () => {
	const items = itemsOf(reviewDoc());

	it.each<[string, Curation | undefined, object]>([
		[
			'no curation: all kept',
			undefined,
			{ kept: 3, dropped: 0, reworded: 0, summaryReworded: false }
		],
		[
			'a dropped item',
			{ items: [{ item: 'notes/0', keep: false }] },
			{ kept: 2, dropped: 1, reworded: 0, summaryReworded: false }
		],
		[
			'a reworded item',
			{ items: [{ item: 'notes/0', keep: true, body: 'New.' }] },
			{ kept: 3, dropped: 0, reworded: 1, summaryReworded: false }
		],
		[
			'a flag set to false, which is still a change',
			{ items: [{ item: 'replies/0', keep: true, resolve: false }] },
			{ kept: 3, dropped: 0, reworded: 1, summaryReworded: false }
		],
		[
			'a kept item with neither, which changed nothing',
			{ items: [{ item: 'notes/0', keep: true }] },
			{ kept: 3, dropped: 0, reworded: 0, summaryReworded: false }
		],
		[
			'an emptied summary',
			{ summary: '', items: [] },
			{ kept: 3, dropped: 0, reworded: 0, summaryReworded: true }
		]
	])('counts %s', (_, c, want) => {
		expect(appliedTally(items, c)).toEqual({ ...want, total: 3 });
	});

	it('ignores a decision about an item that is not shown', () => {
		expect(appliedTally(items, { items: [{ item: 'notes/9', keep: false }] }).dropped).toBe(0);
	});
});

describe('threadOf', () => {
	const threads = [aThread({ id: 'a' }), aThread({ id: 'b' })];

	it('finds the thread by id', () => {
		expect(threadOf(threads, 'b')).toBe(threads[1]);
	});

	it.each<[string, typeof threads | undefined, string | undefined]>([
		['an id not shown', threads, 'c'],
		['no id', threads, undefined],
		['an empty id', threads, ''],
		['no threads', undefined, 'a']
	])('finds nothing for %s', (_, list, id) => {
		expect(threadOf(list, id)).toBeUndefined();
	});
});

describe('shortThread', () => {
	it.each([
		['short', 'short'],
		['exactly-16-chars', 'exactly-16-chars'],
		['seventeen-chars-1', 'sevente…ars-1'],
		['PRRT_kwDOabcdefghijklmnop', 'PRRT_kw…lmnop']
	])('shows %s as %s', (id, short) => {
		expect(shortThread(id)).toBe(short);
	});
});

describe('rowsFor', () => {
	it.each([
		['an empty text gets the floor', '', 2],
		['one short line gets the floor', 'hello', 2],
		['three lines', 'a\nb\nc', 3],
		['179 characters are one wrap', 'x'.repeat(179), 2],
		['180 characters are two wraps', 'x'.repeat(180), 3],
		['lines and length add up', 'a\nb\n' + 'x'.repeat(180), 5],
		['a very long text stops at the ceiling', 'x'.repeat(5000), 14],
		['many lines stop at the ceiling', 'a\n'.repeat(40), 14]
	])('%s', (_, text, rows) => {
		expect(rowsFor(text)).toBe(rows);
	});
});
