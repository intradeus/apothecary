import { render, screen, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Curation, CurationDocument, CurationView, Ping } from '$lib/domain';
import { deferred } from '$test/helpers';
import {
	REPLIES_PATH,
	REVIEW_PATH,
	aContinuation,
	aCuratedPing,
	aNote,
	aReply,
	aRepliesDoc,
	aReviewDoc,
	aThread,
	aView
} from '$test/fixtures/curation';
import CurationHistory from './CurationHistory.svelte';

vi.mock('$lib/api', () => ({ api: { pings: { curation: vi.fn() } } }));

const review = () =>
	aReviewDoc({
		summary: 'As the agent wrote it.',
		notes: [
			aNote({ body: 'Charges twice.' }),
			aNote({ kind: 'nit', path: 'src/a.go', line: 3, body: 'Typo.' }),
			aNote({ kind: 'question', line: 0, body: 'Why not a queue?' })
		]
	});

const round = () =>
	aRepliesDoc([
		aReply({ reply: 'Fixed it.', resolve: true }),
		aReply({ thread: 'th-2', did: 'declined', reply: 'Out of scope.', resolve: false })
	]);

/** The answered ping over a document, with the person's curation on it. */
const answered = (doc: CurationDocument, curation?: Curation, over: Partial<Ping> = {}) =>
	aCuratedPing(doc, {
		status: 'answered',
		answer: 'approve',
		...(curation ? { curated: { [doc.path]: curation } } : {}),
		...over
	} as Partial<Ping>);

async function setup(view: CurationView, ping: Ping) {
	vi.mocked(api.pings.curation).mockResolvedValue(view);
	render(CurationHistory, { ping });
	return await screen.findByText(/^(Posted|Sent) \d+ of \d+/);
}

/** The card of the item whose text is `text`. */
const card = (text: string) => screen.getByText(text).closest<HTMLElement>('.hi')!;

describe('CurationHistory', () => {
	beforeEach(() => {
		vi.mocked(api.pings.curation).mockReset();
	});

	it('shows the skeleton while the document loads, then the document', async () => {
		const load = deferred<CurationView>();
		vi.mocked(api.pings.curation).mockReturnValue(load.promise);

		render(CurationHistory, { ping: answered(review()) });

		expect(screen.getByLabelText('Loading')).toBeInTheDocument();
		expect(api.pings.curation).toHaveBeenCalledExactlyOnceWith('ping-curate');

		load.resolve(aView(review()));

		expect(await screen.findByText('Posted 3 of 3 notes')).toBeInTheDocument();
		expect(screen.queryByLabelText('Loading')).not.toBeInTheDocument();
	});

	it('shows why the document could not be read', async () => {
		vi.mocked(api.pings.curation).mockRejectedValue(new Error('engine down'));
		render(CurationHistory, { ping: answered(review()) });

		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('Could not read what the gate showed');
		expect(alert).toHaveTextContent('engine down');
	});

	it('says it could not read a document the gate did not return', async () => {
		vi.mocked(api.pings.curation).mockResolvedValue({ documents: [] });
		render(CurationHistory, { ping: answered(review()) });

		expect(await screen.findByRole('alert')).toHaveTextContent(
			'Could not read what the gate showed'
		);
	});

	it('shows why a broken document could not be decoded', async () => {
		const broken = aReviewDoc({}, { review: undefined, error: 'line 3: bad yaml' });
		vi.mocked(api.pings.curation).mockResolvedValue(aView(broken));
		render(CurationHistory, { ping: answered(broken) });

		expect(await screen.findByRole('alert')).toHaveTextContent('line 3: bad yaml');
	});

	describe('the tally', () => {
		it('says a review went out as written', async () => {
			const line = await setup(aView(review()), answered(review()));

			expect(line).toHaveTextContent('Posted 3 of 3 notes');
			expect(screen.getByText('· Approved as written.')).toBeInTheDocument();
			expect(screen.queryByText(/dropped/)).not.toBeInTheDocument();
			expect(screen.queryByText(/reworded/)).not.toBeInTheDocument();
		});

		it('counts what was dropped and reworded', async () => {
			const line = await setup(
				aView(review()),
				answered(review(), {
					summary: 'Shorter.',
					items: [
						{ item: 'notes/0', keep: false },
						{ item: 'notes/1', keep: false },
						{ item: 'notes/2', keep: true, body: 'Better.' }
					]
				})
			);

			expect(line).toHaveTextContent('Posted 1 of 3 notes');
			expect(screen.getByText('· 2 dropped')).toBeInTheDocument();
			expect(screen.getByText('· 1 reworded')).toBeInTheDocument();
			expect(screen.getByText('· summary reworded')).toBeInTheDocument();
			expect(screen.queryByText('· Approved as written.')).not.toBeInTheDocument();
		});

		it('counts replies as sent', async () => {
			const doc = round();
			const line = await setup(
				aView(doc),
				answered(doc, { items: [{ item: 'replies/1', keep: false }] })
			);

			expect(line).toHaveTextContent('Sent 1 of 2 replies');
		});

		it('says the draft went back with these decisions', async () => {
			await setup(
				aView(review()),
				answered(review(), { items: [{ item: 'notes/0', keep: false }] }, { answer: 'reject' })
			);

			expect(
				screen.getByText('· The draft went back to the agent with these decisions.')
			).toBeInTheDocument();
		});

		it('does not call a rejected draft approved', async () => {
			await setup(aView(review()), answered(review(), undefined, { answer: 'reject' }));

			expect(screen.queryByText('· Approved as written.')).not.toBeInTheDocument();
		});
	});

	describe('the summary', () => {
		it('shows the summary as written when it was left alone', async () => {
			await setup(aView(review()), answered(review()));

			expect(screen.getByText('As the agent wrote it.')).toBeInTheDocument();
			expect(screen.queryByText('As written')).not.toBeInTheDocument();
		});

		it('shows the reworded summary, with what was written folded away', async () => {
			await setup(aView(review()), answered(review(), { summary: 'Shorter.', items: [] }));

			const posted = screen.getByText('Shorter.');
			expect(posted).toBeInTheDocument();
			const caption = screen.getByText('Summary');
			expect(within(caption).getByText('reworded')).toBeInTheDocument();
			const fold = screen.getByText('As written').closest('details')!;
			expect(fold).not.toHaveAttribute('open');
			expect(fold).toHaveTextContent('As the agent wrote it.');
		});

		it('shows no summary when it was emptied', async () => {
			await setup(aView(review()), answered(review(), { summary: '', items: [] }));

			expect(screen.queryByText('Summary')).not.toBeInTheDocument();
			expect(screen.getByText('· summary reworded')).toBeInTheDocument();
		});

		it('has no summary to show on a round', async () => {
			const doc = round();
			await setup(aView(doc), answered(doc));

			expect(screen.queryByText('Summary')).not.toBeInTheDocument();
		});
	});

	describe('the items', () => {
		it('marks a kept note posted, in its kind and place', async () => {
			await setup(aView(review()), answered(review()));

			const it_ = within(card('Charges twice.'));
			expect(it_.getByText('posted')).toBeInTheDocument();
			expect(it_.getByText('blocking')).toHaveClass('tint-danger');
			expect(it_.getByTitle('src/pay.go:12')).toBeInTheDocument();
			expect(screen.getByText('Charges twice.')).not.toHaveClass('struck');
		});

		it('leaves the line out of a note with none', async () => {
			await setup(aView(review()), answered(review()));
			expect(within(card('Why not a queue?')).getByTitle('src/pay.go')).toBeInTheDocument();
		});

		it('marks a dropped note and strikes its text', async () => {
			await setup(
				aView(review()),
				answered(review(), { items: [{ item: 'notes/1', keep: false }] })
			);

			expect(within(card('Typo.')).getByText('dropped')).toBeInTheDocument();
			expect(screen.getByText('Typo.')).toHaveClass('struck');
		});

		it('shows a reworded text with the original folded away', async () => {
			await setup(
				aView(review()),
				answered(review(), { items: [{ item: 'notes/0', keep: true, body: 'Better.' }] })
			);

			const it_ = card('Better.');
			expect(within(it_).getByText('reworded')).toBeInTheDocument();
			const fold = within(it_).getByText('As written').closest('details')!;
			expect(fold).not.toHaveAttribute('open');
			expect(fold).toHaveTextContent('Charges twice.');
		});

		it('calls a changed flag reworded, with no original to fold', async () => {
			const doc = round();
			await setup(
				aView(doc),
				answered(doc, { items: [{ item: 'replies/1', keep: true, resolve: true }] })
			);

			const it_ = within(card('Out of scope.'));
			expect(it_.getByText('reworded')).toBeInTheDocument();
			expect(it_.queryByText('As written')).not.toBeInTheDocument();
		});

		it('marks a reply sent and shows the thread it answers', async () => {
			const doc = round();
			await setup(aView(doc, { threads: [aThread()] }), answered(doc));

			const it_ = within(card('Fixed it.'));
			expect(it_.getByText('sent')).toBeInTheDocument();
			expect(it_.getByText(/answers th-1/)).toBeInTheDocument();
			expect(it_.getByText('Why is this retried?')).toBeInTheDocument();
		});

		it('does not unfold the thread a review continues', async () => {
			const doc = aReviewDoc({ replies: [aContinuation({ body: 'Still open.' })] });
			await setup(aView(doc, { threads: [aThread()] }), answered(doc));

			expect(within(card('Still open.')).getByText(/continues a thread th-1/)).toBeInTheDocument();
			expect(screen.queryByText('Why is this retried?')).not.toBeInTheDocument();
		});

		it.each<[string, boolean, Curation | undefined, boolean]>([
			['as the reply wrote it', true, undefined, true],
			['as the reply left it off', false, undefined, false],
			[
				'as the person turned it on',
				false,
				{ items: [{ item: 'replies/0', keep: true, resolve: true }] },
				true
			],
			[
				'as the person turned it off',
				true,
				{ items: [{ item: 'replies/0', keep: true, resolve: false }] },
				false
			],
			['on a dropped reply', true, { items: [{ item: 'replies/0', keep: false }] }, false]
		])('says whether the thread gets resolved: %s', async (_, resolve, curation, shown) => {
			const doc = aRepliesDoc([aReply({ reply: 'Fixed it.', resolve })]);
			await setup(aView(doc), answered(doc, curation));

			const line = within(card('Fixed it.')).queryByText('Resolve the thread');
			if (shown) expect(line).toBeInTheDocument();
			else expect(line).not.toBeInTheDocument();
		});
	});

	it('reads the document the ping points at, not the first one returned', async () => {
		const other = aRepliesDoc([aReply({ reply: 'Not this one.' })], { path: 'other.yaml' });
		const doc = aReviewDoc({ notes: [aNote({ body: 'This one.' })] }, { path: REVIEW_PATH });
		await setup({ documents: [other, doc] }, answered(doc));

		expect(screen.getByText('This one.')).toBeInTheDocument();
		expect(screen.queryByText('Not this one.')).not.toBeInTheDocument();
	});

	it("reads the curation under the document's own path", async () => {
		const doc = round();
		await setup(
			aView(doc),
			aCuratedPing(doc, {
				answer: 'approve',
				curated: { [REVIEW_PATH]: { items: [{ item: 'replies/0', keep: false }] } }
			} as Partial<Ping>)
		);

		expect(doc.path).toBe(REPLIES_PATH);
		expect(screen.getByText('Sent 2 of 2 replies')).toBeInTheDocument();
	});
});
