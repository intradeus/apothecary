import { render, screen, within } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fmtAbs } from '$lib/core/format';
import { aComment, aThread } from '$test/fixtures/curation';
import ThreadCard from './ThreadCard.svelte';

describe('ThreadCard', () => {
	beforeEach(() => {
		vi.setSystemTime(new Date('2026-10-31T12:00:00Z'));
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	it.each([
		['a file and line', { path: 'src/pay.go', line: 12 }, 'src/pay.go:12'],
		['a file alone', { path: 'src/pay.go', line: undefined }, 'src/pay.go'],
		['the conversation', { path: undefined, line: undefined }, 'conversation']
	])('says where it is on %s', (_, over, where) => {
		render(ThreadCard, { thread: aThread(over) });
		expect(screen.getByTitle(where)).toHaveTextContent(where);
	});

	it.each([
		[true, 'resolved', 'tint-success'],
		[false, 'open', 'tint-warning']
	])('marks resolved=%s as %s', (resolved, word, tint) => {
		render(ThreadCard, { thread: aThread({ resolved }) });
		expect(screen.getByText(word)).toHaveClass(tint);
	});

	it('marks a thread of ours and names its kind', () => {
		render(ThreadCard, { thread: aThread({ ours: true, kind: 'blocking', comments: [] }) });
		expect(screen.getByText('apothecary')).toBeInTheDocument();
		expect(screen.getByText('blocking')).toHaveClass('tint-danger');
	});

	it("leaves out what a thread of someone else's does not have", () => {
		render(ThreadCard, { thread: aThread({ comments: [] }) });
		expect(screen.queryByText('apothecary')).not.toBeInTheDocument();
		expect(screen.queryByText('diff hunk')).not.toBeInTheDocument();
	});

	it('shortens a long id and keeps the whole of it in the title', () => {
		const id = 'PRRT_kwDOabcdefghijklmnop';
		render(ThreadCard, { thread: aThread({ id }) });
		expect(screen.getByTitle(id)).toHaveTextContent('PRRT_kw…lmnop');
	});

	it('folds the hunk away', () => {
		render(ThreadCard, { thread: aThread({ hunk: '@@ -1 +1 @@\n-old\n+new' }) });
		const fold = screen.getByText('diff hunk').closest('details')!;
		expect(fold).not.toHaveAttribute('open');
		expect(fold).toHaveTextContent('+new');
	});

	it("shows a comment's author, age and text", () => {
		const at = '2026-10-31T11:30:00Z';
		render(ThreadCard, {
			thread: aThread({ comments: [aComment({ author: 'ada', at, body: 'Why is this retried?' })] })
		});
		expect(screen.getByText('ada')).toBeInTheDocument();
		expect(screen.getByText('Why is this retried?')).toBeInTheDocument();
		expect(screen.getByText('30m ago')).toHaveAttribute('title', fmtAbs(at));
	});

	it('names apothecary on its own comment, with what it said it did', () => {
		render(ThreadCard, {
			thread: aThread({ comments: [aComment({ author: 'bot', ours: true, did: 'fixed' })] })
		});
		expect(screen.getByText('apothecary')).toBeInTheDocument();
		expect(screen.queryByText('bot')).not.toBeInTheDocument();
		expect(screen.getByText('fixed')).toHaveClass('tint-success');
	});

	it("marks only the comments newer than apothecary's last word", () => {
		render(ThreadCard, {
			thread: aThread({
				comments: [
					aComment({ id: 'c-1', author: 'ada' }),
					aComment({ id: 'c-2', author: 'bob', new: true })
				]
			})
		});
		const pills = screen.getAllByText('new');
		expect(pills).toHaveLength(1);
		expect(pills[0].parentElement).toHaveTextContent('bob');
		expect(within(pills[0].parentElement!).queryByText('ada')).not.toBeInTheDocument();
	});

	it('says so when a thread has no comments', () => {
		render(ThreadCard, { thread: aThread({ comments: [] }) });
		expect(screen.getByText('No comments.')).toBeInTheDocument();
	});

	it('keeps the comments in the order the thread has them', () => {
		render(ThreadCard, {
			thread: aThread({
				comments: [aComment({ id: 'c-1', body: 'first' }), aComment({ id: 'c-2', body: 'second' })]
			})
		});
		const first = screen.getByText('first');
		const second = screen.getByText('second');
		expect(first.compareDocumentPosition(second)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
	});
});
