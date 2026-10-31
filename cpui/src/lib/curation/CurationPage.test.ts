import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Attachment, CurationDocument, CurationView, Ping, PingFile } from '$lib/domain';
import { deferred } from '$test/helpers';
import {
	REPLIES_PATH,
	REVIEW_PATH,
	THREADS_PATH,
	aContinuation,
	aCuratedPing,
	aNote,
	aReply,
	aRepliesDoc,
	aReviewDoc,
	aThread,
	aView
} from '$test/fixtures/curation';
import CurationPage from './CurationPage.svelte';

vi.mock('$lib/api', () => ({
	api: {
		pings: { curation: vi.fn(), payload: vi.fn(), answer: vi.fn() },
		files: { upload: vi.fn(), limits: vi.fn(async () => null), snapshotUrl: vi.fn() }
	}
}));

const PING = 'ping-curate';
const DRAFT = `gate-curation-${PING}`;

const review = (over: Parameters<typeof aReviewDoc>[0] = {}) =>
	aReviewDoc({
		summary: 'As the agent wrote it.',
		notes: [
			aNote({ body: 'Charges twice.' }),
			aNote({ kind: 'nit', path: 'src/a.go', line: 3, body: 'Typo.' })
		],
		...over
	});

const round = () =>
	aRepliesDoc([
		aReply({ reply: 'Fixed it.', resolve: true }),
		aReply({ thread: 'th-2', did: 'declined', reply: 'Out of scope.', resolve: false })
	]);

/** The gate's files as the engine lists them: nothing to fetch, by default. */
const shown = (...paths: string[]): PingFile[] => paths.map((path) => ({ path, captured: false }));

interface Setup {
	ping?: Partial<Ping>;
	threads?: Parameters<typeof aView>[1];
	files?: PingFile[];
}

function start(doc: CurationDocument, { ping, threads, files }: Setup = {}) {
	vi.mocked(api.pings.curation).mockResolvedValue(aView(doc, threads));
	vi.mocked(api.pings.payload).mockResolvedValue({ files: files ?? shown(doc.path) });
	const onanswered = vi.fn();
	render(CurationPage, { ping: aCuratedPing(doc, ping), onanswered });
	return { onanswered };
}

const settled = () =>
	waitFor(() => expect(screen.queryByLabelText('Loading')).not.toBeInTheDocument());

async function setup(doc: CurationDocument, opts: Setup = {}) {
	const out = start(doc, opts);
	await settled();
	return out;
}

const approve = () => screen.getByRole('button', { name: /^Approve/ });
const requestChanges = () => screen.getByRole('button', { name: 'Request changes' });
const feedbackBox = () => screen.getByRole('textbox', { name: 'To the agent' });
const summaryBox = () =>
	screen.getByPlaceholderText<HTMLTextAreaElement>(
		'Posted with the review. Reword it as you like.'
	);
const toolbarBox = () =>
	screen.getByRole<HTMLInputElement>('checkbox', { name: /of \d+ (posted|sent)/ });
const bulkButton = (name: string) => screen.getByRole('button', { name });
const restoreSummary = () => screen.getByText('Restore as written', { selector: 'button' });
/** The keep box of an item, by its place in the list. */
const keepBox = (i: number, title = 'Post it') =>
	within(screen.getAllByTitle(title)[i]).getByRole('checkbox');
const sent = () => vi.mocked(api.pings.answer).mock.calls[0][1];
const saved = () => JSON.parse(localStorage.getItem(DRAFT) ?? 'null');

describe('CurationPage', () => {
	beforeEach(() => {
		localStorage.clear();
		vi.mocked(api.pings.curation).mockReset();
		vi.mocked(api.pings.payload).mockReset();
		vi.mocked(api.pings.answer)
			.mockReset()
			.mockResolvedValue(undefined as never);
		vi.mocked(api.files.upload).mockReset();
	});

	describe('loading', () => {
		it('holds both answers until the document is in', async () => {
			const load = deferred<CurationView>();
			vi.mocked(api.pings.curation).mockReturnValue(load.promise);
			vi.mocked(api.pings.payload).mockResolvedValue({ files: shown(REVIEW_PATH) });
			render(CurationPage, { ping: aCuratedPing(), onanswered: vi.fn() });

			expect(screen.getAllByLabelText('Loading').length).toBeGreaterThan(0);
			expect(api.pings.curation).toHaveBeenCalledExactlyOnceWith(PING);
			expect(approve()).toBeDisabled();
			await userEvent.type(feedbackBox(), 'No.');
			expect(requestChanges()).toBeDisabled();

			load.resolve(aView(review()));
			await settled();

			expect(approve()).toBeEnabled();
			expect(screen.getByDisplayValue('Charges twice.')).toBeInTheDocument();
		});

		// A person typing before the document is in must not save a draft of
		// nothing over the one they came back for.
		it('writes no draft while loading', async () => {
			const load = deferred<CurationView>();
			vi.mocked(api.pings.curation).mockReturnValue(load.promise);
			vi.mocked(api.pings.payload).mockResolvedValue({ files: shown(REVIEW_PATH) });
			render(CurationPage, { ping: aCuratedPing(), onanswered: vi.fn() });

			await userEvent.type(feedbackBox(), 'x');

			expect(localStorage.getItem(DRAFT)).toBeNull();
			load.resolve(aView(review()));
			await settled();
		});
	});

	describe('a document it cannot curate', () => {
		const cannot = 'Could not read what the gate showed';

		it('shows the load error and leaves only approving as written', async () => {
			vi.mocked(api.pings.curation).mockRejectedValue(new Error('engine down'));
			vi.mocked(api.pings.payload).mockResolvedValue({ files: shown(REVIEW_PATH) });
			const onanswered = vi.fn();
			render(CurationPage, { ping: aCuratedPing(), onanswered });
			await settled();

			const alert = screen.getByRole('alert');
			expect(alert).toHaveTextContent(cannot);
			expect(alert).toHaveTextContent('engine down');
			expect(approve()).toHaveTextContent('Approve as written');

			await userEvent.click(approve());

			expect(sent().curated).toBeUndefined();
			expect(onanswered).toHaveBeenCalledOnce();
		});

		it('says a file was not captured', async () => {
			await setup(aReviewDoc({}, { captured: false, review: undefined }));

			expect(screen.getByRole('alert')).toHaveTextContent(`${REVIEW_PATH} was not captured`);
			expect(approve()).toHaveTextContent('Approve as written');
		});

		it('says what is wrong with a file that does not decode', async () => {
			await setup(aReviewDoc({}, { error: 'line 3: bad yaml' }));

			const alert = screen.getByRole('alert');
			expect(alert).toHaveTextContent(`${REVIEW_PATH} does not hold what its type promises`);
			expect(alert).toHaveTextContent('line 3: bad yaml');
			expect(approve()).toHaveTextContent('Approve as written');
		});

		it('hides the summary it could not read', async () => {
			await setup(aReviewDoc({ summary: 'Broken' }, { error: 'bad' }));
			expect(screen.queryByText('Summary')).not.toBeInTheDocument();
		});

		it('treats a document the view does not hold as not captured', async () => {
			vi.mocked(api.pings.curation).mockResolvedValue({ documents: [] });
			vi.mocked(api.pings.payload).mockResolvedValue({ files: shown(REVIEW_PATH) });
			render(CurationPage, { ping: aCuratedPing(), onanswered: vi.fn() });
			await settled();

			expect(screen.getByRole('alert')).toHaveTextContent('was not captured');
		});

		it('sends no decisions even with a saved draft around', async () => {
			localStorage.setItem(DRAFT, JSON.stringify({ items: { 'notes/0': { keep: false } } }));
			await setup(aReviewDoc({}, { captured: false }));

			await userEvent.click(approve());

			expect(sent().curated).toBeUndefined();
		});
	});

	describe('an empty document', () => {
		it('says there is nothing to post and offers no toolbar', async () => {
			await setup(aReviewDoc({ summary: '', notes: [] }));

			expect(screen.getByText('Nothing to post: the review is empty.')).toBeInTheDocument();
			expect(screen.queryByRole('button', { name: 'All' })).not.toBeInTheDocument();
			expect(approve()).toHaveTextContent('Approve — post nothing');
		});

		it('still posts a summary with no notes', async () => {
			await setup(aReviewDoc({ summary: 'All good.', notes: [] }));
			expect(approve()).toHaveTextContent('Approve — post 0');
		});
	});

	describe('the items', () => {
		it('lists every item, kept, under the count', async () => {
			await setup(review());

			expect(screen.getByDisplayValue('Charges twice.')).toBeInTheDocument();
			expect(screen.getByDisplayValue('Typo.')).toBeInTheDocument();
			expect(toolbarBox()).toBeChecked();
			expect(toolbarBox()).toHaveAccessibleName('2 of 2 posted');
			expect(approve()).toHaveTextContent('Approve — post 2');
			expect(screen.getByText('2/2')).not.toHaveClass('hot');
		});

		it('words a round as sent', async () => {
			await setup(round());

			expect(toolbarBox()).toHaveAccessibleName('2 of 2 sent');
			expect(approve()).toHaveTextContent('Approve — send 2');
			expect(keepBox(0, 'Send it')).toBeChecked();
		});

		it('hands each item the thread it is in', async () => {
			await setup(round(), { threads: { threads: [aThread()] } });

			expect(screen.getByText('Why is this retried?')).toBeInTheDocument();
			expect(screen.getByText('thread th-2 is not among the files shown')).toBeInTheDocument();
		});

		it('counts what changes', async () => {
			await setup(review());
			await userEvent.click(keepBox(0));

			expect(toolbarBox()).toHaveAccessibleName('1 of 2 posted');
			expect(toolbarBox()).not.toBeChecked();
			expect(toolbarBox().indeterminate).toBe(true);
			expect(approve()).toHaveTextContent('Approve — post 1');
			expect(screen.getByText('1/2')).toHaveClass('hot');
		});

		it('lights the count for a rewording too', async () => {
			await setup(review());
			await userEvent.type(screen.getByDisplayValue('Typo.'), '!');

			expect(screen.getByText('2/2')).toHaveClass('hot');
		});

		it('shows the ask, folded', async () => {
			await setup(review(), { ping: { question: 'Post this **review**?' } });

			const fold = screen.getByText('The ask').closest('details')!;
			expect(fold).not.toHaveAttribute('open');
			expect(within(fold).getByText('review').tagName).toBe('STRONG');
		});

		it('lists the notes dropped earlier in the run', async () => {
			await setup(review(), {
				threads: {
					dropped: [aNote({ kind: 'nit', path: 'src/old.go', line: 9, body: 'An old nit.' })]
				}
			});

			const fold = screen
				.getByText('Dropped earlier in this run — never proposed again')
				.closest('details')!;
			expect(within(fold).getByText('src/old.go:9')).toBeInTheDocument();
			expect(within(fold).getByText('An old nit.')).toBeInTheDocument();
			expect(within(fold).getByText('nit')).toHaveClass('tint-muted');
		});

		it('leaves that list out when nothing was dropped', async () => {
			await setup(review());
			expect(
				screen.queryByText('Dropped earlier in this run — never proposed again')
			).not.toBeInTheDocument();
		});
	});

	describe('the toolbar', () => {
		it('drops everything from None, and warns when nothing is left to post', async () => {
			await setup(review({ summary: '' }));
			await userEvent.click(bulkButton('None'));

			expect(keepBox(0)).not.toBeChecked();
			expect(keepBox(1)).not.toBeChecked();
			expect(toolbarBox().indeterminate).toBe(false);
			expect(approve()).toHaveTextContent('Approve — post nothing');
			expect(
				screen.getByText('Nothing will be posted; the agent still reads the approval.')
			).toBeInTheDocument();
		});

		it('does not warn while a summary is still going out', async () => {
			await setup(review());
			await userEvent.click(bulkButton('None'));

			expect(approve()).toHaveTextContent('Approve — post 0');
			expect(
				screen.queryByText('Nothing will be posted; the agent still reads the approval.')
			).not.toBeInTheDocument();
		});

		it('warns on a round with everything dropped', async () => {
			await setup(round());
			await userEvent.click(bulkButton('None'));

			expect(approve()).toHaveTextContent('Approve — post nothing');
		});

		it('brings everything back from All', async () => {
			await setup(review());
			await userEvent.click(bulkButton('None'));
			await userEvent.click(bulkButton('All'));

			expect(keepBox(0)).toBeChecked();
			expect(keepBox(1)).toBeChecked();
		});

		it('disables what would change nothing', async () => {
			await setup(review());
			expect(bulkButton('All')).toBeDisabled();
			expect(bulkButton('None')).toBeEnabled();

			await userEvent.click(bulkButton('None'));

			expect(bulkButton('All')).toBeEnabled();
			expect(bulkButton('None')).toBeDisabled();
		});

		it('drops only the nits', async () => {
			await setup(review());
			await userEvent.click(bulkButton('Drop nits'));

			expect(keepBox(0)).toBeChecked();
			expect(keepBox(1)).not.toBeChecked();
		});

		it('offers Drop nits only when there are nits', async () => {
			await setup(aReviewDoc({ notes: [aNote()] }));
			expect(screen.queryByRole('button', { name: 'Drop nits' })).not.toBeInTheDocument();
		});

		it('takes the box as all from none or some, and none from all', async () => {
			await setup(review());

			await userEvent.click(toolbarBox());
			expect(toolbarBox()).toHaveAccessibleName('0 of 2 posted');

			await userEvent.click(keepBox(0));
			expect(toolbarBox()).toHaveAccessibleName('1 of 2 posted');

			await userEvent.click(toolbarBox());
			expect(toolbarBox()).toHaveAccessibleName('2 of 2 posted');
		});
	});

	describe('the summary', () => {
		it('starts as written, with nothing to restore', async () => {
			await setup(review());

			expect(summaryBox()).toHaveValue('As the agent wrote it.');
			expect(screen.queryByText('Restore as written')).not.toBeInTheDocument();
		});

		it('marks a rewording and gives the way back', async () => {
			await setup(review());
			await userEvent.type(summaryBox(), ' Mine.');
			expect(screen.getByText('reworded')).toBeInTheDocument();

			await userEvent.click(restoreSummary());

			expect(summaryBox()).toHaveValue('As the agent wrote it.');
			expect(screen.queryByText('reworded')).not.toBeInTheDocument();
			expect(saved().summary).toBe('As the agent wrote it.');
		});

		it('has none on a round', async () => {
			await setup(round());
			expect(screen.queryByText('Summary')).not.toBeInTheDocument();
		});

		it("keeps a rewording when the summary's help line is clicked", async () => {
			await setup(review());
			await userEvent.type(summaryBox(), ' Mine.');

			await userEvent.click(
				screen.getByText('Posted with the review. Reword it as you like.', { selector: 'span' })
			);

			expect(summaryBox()).toHaveValue('As the agent wrote it. Mine.');
		});

		it('keeps the summary box named once it is reworded', async () => {
			await setup(review());
			await userEvent.type(summaryBox(), ' Mine.');

			expect(summaryBox().labels?.[0]?.control).toBe(summaryBox());
		});
	});

	describe('approving', () => {
		it('sends nothing but the verdict for a draft left as written', async () => {
			const { onanswered } = await setup(review());

			await userEvent.click(approve());

			expect(api.pings.answer).toHaveBeenCalledExactlyOnceWith(PING, {
				answer: 'approve',
				feedback: undefined,
				attachmentIds: [],
				curated: undefined
			});
			expect(api.files.upload).not.toHaveBeenCalled();
			expect(onanswered).toHaveBeenCalledOnce();
		});

		it("sends the decisions under the document's path", async () => {
			await setup(
				review({
					notes: [aNote({ body: 'Charges twice.' }), aNote({ body: 'Typo.' })],
					replies: [aContinuation({ body: 'Still open.', resolve: false })]
				})
			);

			await userEvent.click(keepBox(0));
			await userEvent.type(screen.getByDisplayValue('Typo.'), ' Fixed');
			await userEvent.click(screen.getByRole('checkbox', { name: 'Resolve the thread' }));
			await userEvent.type(summaryBox(), ' Mine.');
			await userEvent.click(approve());

			expect(sent().curated).toEqual({
				[REVIEW_PATH]: {
					summary: 'As the agent wrote it. Mine.',
					items: [
						{ item: 'notes/0', keep: false },
						{ item: 'notes/1', keep: true, body: 'Typo. Fixed' },
						{ item: 'replies/0', keep: true, resolve: true }
					]
				}
			});
		});

		it("sends a round's decisions under its own path, keyed as replies", async () => {
			await setup(round());

			await userEvent.click(keepBox(1, 'Send it'));
			await userEvent.click(approve());

			expect(sent().curated).toEqual({
				[REPLIES_PATH]: { items: [{ item: 'replies/1', keep: false }] }
			});
		});

		it('carries a trimmed note to the agent', async () => {
			await setup(review());

			await userEvent.type(feedbackBox(), '  Thanks.  ');
			await userEvent.click(approve());

			expect(sent().feedback).toBe('Thanks.');
		});

		it('leaves out a note of only spaces', async () => {
			await setup(review());

			await userEvent.type(feedbackBox(), '   ');
			await userEvent.click(approve());

			expect(sent().feedback).toBeUndefined();
		});

		it('refuses while a kept text is blank, and says why', async () => {
			await setup(review());
			await userEvent.clear(screen.getByDisplayValue('Charges twice.'));

			expect(approve()).toBeDisabled();
			expect(
				screen.getByText('A kept item has no text: write something, or drop it.')
			).toBeInTheDocument();
		});

		it('allows it again once the blank is dropped', async () => {
			await setup(review());
			await userEvent.clear(screen.getByDisplayValue('Charges twice.'));
			await userEvent.click(keepBox(0));

			expect(approve()).toBeEnabled();
			expect(
				screen.queryByText('A kept item has no text: write something, or drop it.')
			).not.toBeInTheDocument();
		});

		it('uploads the attachments first and sends their ids', async () => {
			vi.mocked(api.files.upload).mockResolvedValue([{ id: 'att-1' } as Attachment]);
			await setup(review());
			const file = new File(['log'], 'log.txt', { type: 'text/plain' });

			await userEvent.upload(document.querySelector<HTMLInputElement>('input[type=file]')!, file);
			await userEvent.click(approve());

			expect(api.files.upload).toHaveBeenCalledExactlyOnceWith([file]);
			expect(sent().attachmentIds).toEqual(['att-1']);
		});
	});

	describe('requesting changes', () => {
		it('needs a word for the agent', async () => {
			await setup(review());

			expect(requestChanges()).toBeDisabled();
			expect(requestChanges()).toHaveAttribute(
				'title',
				'Say what should change — the agent reads it, and keeps off what you dropped.'
			);

			await userEvent.type(feedbackBox(), '   ');
			expect(requestChanges()).toBeDisabled();

			await userEvent.type(feedbackBox(), 'Softer.');
			expect(requestChanges()).toBeEnabled();
			expect(requestChanges()).toHaveAttribute('title', '');
		});

		it('sends the word and the decisions back', async () => {
			const { onanswered } = await setup(review());

			await userEvent.click(keepBox(1));
			await userEvent.type(feedbackBox(), 'Softer.');
			await userEvent.click(requestChanges());

			expect(api.pings.answer).toHaveBeenCalledExactlyOnceWith(PING, {
				answer: 'reject',
				feedback: 'Softer.',
				attachmentIds: [],
				curated: { [REVIEW_PATH]: { items: [{ item: 'notes/1', keep: false }] } }
			});
			expect(onanswered).toHaveBeenCalledOnce();
		});

		it('works with a blank item, which is only a draft going back', async () => {
			await setup(review());
			await userEvent.clear(screen.getByDisplayValue('Typo.'));
			await userEvent.type(feedbackBox(), 'Rethink it.');

			expect(requestChanges()).toBeEnabled();
		});
	});

	describe('while sending', () => {
		async function sending(button: () => HTMLElement) {
			const send = deferred<never>();
			vi.mocked(api.pings.answer).mockReturnValue(send.promise);
			await setup(review());
			await userEvent.type(feedbackBox(), 'Softer.');
			await userEvent.click(button());
			return send;
		}

		it('holds every control and marks the approve button', async () => {
			const send = await sending(approve);

			expect(approve()).toBeDisabled();
			expect(approve()).toHaveAttribute('aria-busy', 'true');
			expect(requestChanges()).toBeDisabled();
			expect(requestChanges()).toHaveAttribute('aria-busy', 'false');
			expect(feedbackBox()).toBeDisabled();
			expect(summaryBox()).toBeDisabled();
			expect(screen.getByDisplayValue('Typo.')).toBeDisabled();
			expect(keepBox(0)).toBeDisabled();
			expect(toolbarBox()).toBeDisabled();
			expect(bulkButton('None')).toBeDisabled();
			expect(bulkButton('Drop nits')).toBeDisabled();

			send.resolve(undefined as never);
			await waitFor(() => expect(feedbackBox()).toBeEnabled());
		});

		it('marks the request-changes button when that is what is going', async () => {
			const send = await sending(requestChanges);

			expect(requestChanges()).toHaveAttribute('aria-busy', 'true');
			expect(approve()).toHaveAttribute('aria-busy', 'false');

			send.resolve(undefined as never);
			await waitFor(() => expect(feedbackBox()).toBeEnabled());
		});

		it("holds the summary's Restore too", async () => {
			const send = deferred<never>();
			vi.mocked(api.pings.answer).mockReturnValue(send.promise);
			await setup(review());
			await userEvent.type(summaryBox(), ' Mine.');
			await userEvent.click(approve());

			try {
				expect(restoreSummary()).toBeDisabled();
			} finally {
				send.resolve(undefined as never);
			}
		});

		it('sends once', async () => {
			const send = await sending(approve);

			await userEvent.click(approve());

			expect(api.pings.answer).toHaveBeenCalledOnce();
			send.resolve(undefined as never);
			await waitFor(() => expect(feedbackBox()).toBeEnabled());
		});
	});

	describe('when the answer fails', () => {
		it('shows why, keeps the draft and lets the person try again', async () => {
			vi.mocked(api.pings.answer).mockRejectedValueOnce(new Error('gate closed'));
			const { onanswered } = await setup(review());
			await userEvent.click(keepBox(0));

			await userEvent.click(approve());

			expect(await screen.findByText('gate closed')).toBeInTheDocument();
			expect(onanswered).not.toHaveBeenCalled();
			expect(saved().items['notes/0'].keep).toBe(false);
			expect(approve()).toBeEnabled();

			await userEvent.click(approve());

			expect(onanswered).toHaveBeenCalledOnce();
			expect(screen.queryByText('gate closed')).not.toBeInTheDocument();
		});

		it('sends no answer when an attachment fails to upload', async () => {
			vi.mocked(api.files.upload).mockRejectedValue(new Error('too big'));
			const { onanswered } = await setup(review());
			const file = new File(['x'], 'x.txt', { type: 'text/plain' });

			await userEvent.upload(document.querySelector<HTMLInputElement>('input[type=file]')!, file);
			await userEvent.click(approve());

			expect(await screen.findByText('too big')).toBeInTheDocument();
			expect(api.pings.answer).not.toHaveBeenCalled();
			expect(onanswered).not.toHaveBeenCalled();
		});
	});

	describe('the draft', () => {
		const keptInBrowser = 'Drafts are kept in this browser; attachments must be re-picked.';

		it('says drafts are kept, then that one was saved', async () => {
			await setup(review());
			expect(screen.getByText(keptInBrowser)).toBeInTheDocument();

			await userEvent.type(feedbackBox(), 'x');

			expect(screen.getByText(/Draft saved/)).toBeInTheDocument();
			expect(screen.queryByText(keptInBrowser)).not.toBeInTheDocument();
		});

		it('saves each kind of edit, with the latest text', async () => {
			await setup(review());

			await userEvent.type(feedbackBox(), 'Softer');
			expect(saved().feedback).toBe('Softer');

			await userEvent.type(summaryBox(), '!');
			expect(saved().summary).toBe('As the agent wrote it.!');

			await userEvent.type(screen.getByDisplayValue('Typo.'), '!');
			expect(saved().items['notes/1'].body).toBe('Typo.!');

			await userEvent.click(keepBox(0));
			expect(saved().items['notes/0'].keep).toBe(false);

			await userEvent.click(bulkButton('All'));
			expect(saved().items['notes/0'].keep).toBe(true);
		});

		it('picks up where the person left off, as an unsaved page', async () => {
			localStorage.setItem(
				DRAFT,
				JSON.stringify({
					summary: 'Saved summary.',
					feedback: 'Saved word.',
					items: {
						'notes/0': { keep: false, body: 'Charges twice.', resolve: false },
						'notes/1': { keep: true, body: 'Saved text.', resolve: false },
						'notes/9': { keep: false, body: 'Gone.', resolve: false }
					}
				})
			);
			await setup(review());

			expect(keepBox(0)).not.toBeChecked();
			expect(screen.getByDisplayValue('Saved text.')).toBeInTheDocument();
			expect(summaryBox()).toHaveValue('Saved summary.');
			expect(feedbackBox()).toHaveValue('Saved word.');
			expect(toolbarBox()).toHaveAccessibleName('1 of 2 posted');
			expect(screen.getByText(keptInBrowser)).toBeInTheDocument();
		});

		it.each([
			['approving', () => approve()],
			['requesting changes', () => requestChanges()]
		])('forgets the draft after %s', async (_, button) => {
			await setup(review());
			await userEvent.type(feedbackBox(), 'Softer.');
			expect(saved()).not.toBeNull();

			await userEvent.click(button());

			await waitFor(() => expect(saved()).toBeNull());
		});
	});

	describe('the other files shown', () => {
		const both = () => [
			{ path: REVIEW_PATH, captured: true, snapshotId: undefined },
			{ path: THREADS_PATH, captured: false }
		];
		const fileButton = (name: string) => screen.getByRole('button', { name: new RegExp(name) });

		it('lists the files beside the items, with the review open and its count', async () => {
			await setup(review(), { files: both() });

			expect(api.pings.payload).toHaveBeenCalledExactlyOnceWith(PING);
			expect(fileButton('review.yaml')).toHaveAttribute('aria-current', 'true');
			expect(fileButton('threads.yaml')).toBeInTheDocument();
			expect(screen.getByText('2')).toBeInTheDocument();
		});

		it('opens another file in place of the items, and comes back to them', async () => {
			await setup(review(), { files: both() });

			await userEvent.click(fileButton('threads.yaml'));

			expect(screen.queryByDisplayValue('Charges twice.')).not.toBeInTheDocument();
			expect(screen.getByRole('alert')).toHaveTextContent(`${THREADS_PATH} was not captured`);

			await userEvent.click(fileButton('review.yaml'));

			expect(screen.getByDisplayValue('Charges twice.')).toBeInTheDocument();
		});

		it('keeps the draft across files', async () => {
			await setup(review(), { files: both() });
			await userEvent.type(screen.getByDisplayValue('Typo.'), '!');

			await userEvent.click(fileButton('threads.yaml'));
			await userEvent.click(fileButton('review.yaml'));

			expect(screen.getByDisplayValue('Typo.!')).toBeInTheDocument();
		});

		it('moves along the files with j and k', async () => {
			await setup(review(), { files: both() });

			await userEvent.keyboard('j');
			expect(fileButton('threads.yaml')).toHaveAttribute('aria-current', 'true');

			await userEvent.keyboard('k');
			expect(fileButton('review.yaml')).toHaveAttribute('aria-current', 'true');
		});

		it('leaves j to the text box being typed in', async () => {
			await setup(review(), { files: both() });

			await userEvent.type(feedbackBox(), 'j');

			expect(feedbackBox()).toHaveValue('j');
			expect(fileButton('review.yaml')).toHaveAttribute('aria-current', 'true');
		});

		it('answers about the review whichever file is open', async () => {
			await setup(review(), { files: both() });
			await userEvent.click(keepBox(0));
			await userEvent.click(fileButton('threads.yaml'));

			await userEvent.click(approve());

			expect(sent().curated).toEqual({
				[REVIEW_PATH]: { items: [{ item: 'notes/0', keep: false }] }
			});
		});

		it('still curates when the file list cannot be read', async () => {
			vi.mocked(api.pings.curation).mockResolvedValue(aView(review()));
			vi.mocked(api.pings.payload).mockRejectedValue(new Error('no list'));
			render(CurationPage, { ping: aCuratedPing(), onanswered: vi.fn() });
			await settled();

			expect(screen.getByDisplayValue('Charges twice.')).toBeInTheDocument();
			expect(approve()).toHaveTextContent('Approve — post 2');
		});
	});

	// The route never mounts this for such a ping; if it did, the view's first
	// document must not stand in for the one it asked about.
	it('holds no document for a ping with no curatable file', async () => {
		vi.mocked(api.pings.curation).mockResolvedValue(aView(review()));
		vi.mocked(api.pings.payload).mockResolvedValue({ files: [] });
		render(CurationPage, {
			ping: aCuratedPing({ path: 'plain.md', type: 'document' }),
			onanswered: vi.fn()
		});
		await settled();

		expect(screen.queryByDisplayValue('Charges twice.')).not.toBeInTheDocument();
		expect(screen.getByRole('alert')).toBeInTheDocument();
	});
});
