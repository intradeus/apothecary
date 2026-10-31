import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Ping, PingField } from '$lib/domain';
import { confirmState } from '$lib/ui/confirm.svelte';
import { aPing } from '$test/fixtures';
import { aComment, aPingFile, select, serveSnapshots, stubGeometry } from '$test/fixtures/review';
import { deferred } from '$test/helpers';
import DocReview from './DocReview.svelte';

vi.mock('$lib/api', () => ({
	api: {
		pings: { payload: vi.fn(), answer: vi.fn() },
		files: {
			upload: vi.fn(),
			limits: vi.fn(),
			snapshotUrl: (id: string, download = false) =>
				`/api/snapshots/${id}${download ? '?download=1' : ''}`
		}
	}
}));

const PLAN = '# Plan\n\nShip the webhook first.\n\n## Risks\n\nRetries may double charge.\n';
const SHIP = 'Ship the webhook first.';
const RETRY = 'Retries may double charge.';
const KEY = 'doc-review-p1';
const plan = aPingFile({ path: 'docs/plan.md', snapshotId: 'plan' });
const cfg = aPingFile({ path: 'cfg.yaml', snapshotId: 'cfg', mime: 'text/yaml' });
const shot = aPingFile({ path: 'shot.png', snapshotId: 'shot', mime: 'image/png' });
const snapshots = { plan: PLAN, cfg: 'retries: 3\n' };
const aGate = (over: Partial<Ping> = {}) =>
	aPing({
		id: 'p1',
		question: 'Is the plan **sound**?',
		raisedAt: '2026-10-31T12:00:00Z',
		...over
	});

const textField = (over: Partial<PingField> = {}): PingField => ({
	id: 'q',
	type: 'string',
	required: true,
	options: [],
	...over
});
const aQuestion = (...fields: PingField[]) =>
	aGate({ type: 'clarification', answerKind: 'form', form: { fields } });

/** The review on screen with its first file read. */
async function mount(ping = aGate(), files = [plan, cfg, shot]) {
	vi.mocked(api.pings.payload).mockResolvedValue({ files });
	const fetched = serveSnapshots(snapshots);
	const onanswered = vi.fn();
	const view = render(DocReview, { ping, onanswered });
	await screen.findByRole('button', { name: /Comment on this/ });
	if (files[0] === plan) await screen.findByText(SHIP);
	return { ...view, onanswered, fetched };
}

const button = (name: string | RegExp) => screen.getByRole('button', { name });
const approve = () => button(/^Approve/);
const reject = () => button('Request changes');
const summary = () => screen.getByRole('textbox', { name: /The review as a whole/ });
const composer = () => screen.findByRole('dialog');
const commentBox = () => screen.findByRole('textbox', { name: 'What should change here?' });
const words = (s: string) => screen.getByText(s).firstChild!;
const sent = () => vi.mocked(api.pings.answer).mock.calls[0][1];
const stored = () => JSON.parse(localStorage.getItem(KEY) ?? 'null');

/** Let go of a drag, and let the selection settle as the page waits for it. */
async function letGo() {
	vi.useFakeTimers();
	await fireEvent.mouseUp(document);
	vi.runOnlyPendingTimers();
	vi.useRealTimers();
	await tick();
}

/** Drag over some text and let go, as the page hears it. */
async function drag(from: Node, start: number, to: Node = from, end = start) {
	select(from, start, to, end);
	await letGo();
	return composer();
}

async function comment(text: string) {
	await userEvent.type(await commentBox(), text);
	await userEvent.click(button('Add'));
	await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
}

async function commentOnWords(text: string, on = RETRY) {
	await drag(words(on), 0, words(on), 7);
	await comment(text);
}

describe('DocReview', () => {
	beforeAll(stubGeometry);

	beforeEach(() => {
		vi.resetAllMocks();
		vi.mocked(api.pings.answer).mockResolvedValue(undefined);
		vi.mocked(api.files.limits).mockResolvedValue(null as never);
		localStorage.clear();
		window.getSelection()?.removeAllRanges();
	});

	describe('loading', () => {
		it('shows skeletons until the files are known', () => {
			vi.mocked(api.pings.payload).mockReturnValue(deferred<never>().promise);
			render(DocReview, { ping: aGate(), onanswered: vi.fn() });
			expect(screen.getAllByLabelText('Loading')).toHaveLength(2);
		});

		it('shows the ask beside the file', async () => {
			await mount();
			expect(screen.getByText('The ask')).toBeInTheDocument();
			expect(screen.getByText('sound')).toBeInTheDocument();
		});

		it('says so when the ping shows no files', async () => {
			vi.mocked(api.pings.payload).mockResolvedValue({ files: [] });
			render(DocReview, { ping: aGate(), onanswered: vi.fn() });
			expect(await screen.findByRole('alert')).toHaveTextContent('this ping has no attached files');
		});

		it('says why the files could not be listed', async () => {
			vi.mocked(api.pings.payload).mockRejectedValue(new Error('engine down'));
			render(DocReview, { ping: aGate(), onanswered: vi.fn() });
			expect(await screen.findByRole('alert')).toHaveTextContent('engine down');
		});
	});

	describe('commenting on a selection', () => {
		it('opens the composer on the section, lines and text selected', async () => {
			await mount();

			const dialog = await drag(words(RETRY), 8, words(RETRY), 11);

			expect(within(dialog).getByText('§ Risks')).toBeInTheDocument();
			expect(within(dialog).getByText('L7–7')).toBeInTheDocument();
			expect(within(dialog).getByText('may')).toBeInTheDocument();
			expect(await commentBox()).toHaveFocus();
		});

		it('holds Add until there is something to say', async () => {
			await mount();
			await drag(words(RETRY), 0, words(RETRY), 7);
			expect(button('Add')).toBeDisabled();

			await userEvent.type(await commentBox(), '   ');
			expect(button('Add')).toBeDisabled();

			await userEvent.type(await commentBox(), 'Why?');
			expect(button('Add')).toBeEnabled();
		});

		it('adds the comment to the file, the list and the draft', async () => {
			await mount();

			await commentOnWords('  How often?  ');

			expect(screen.getByRole('listitem')).toHaveTextContent('How often?');
			expect(button('§ Risks · L7')).toBeInTheDocument();
			expect(window.getSelection()?.isCollapsed).toBe(true);
			expect(stored().comments).toEqual([
				expect.objectContaining({
					path: 'docs/plan.md',
					quote: 'Retries',
					heading: 'Risks',
					startLine: 7,
					endLine: 7,
					text: 'How often?'
				})
			]);
		});

		it('sends the comment with where it sits and not how the page held it', async () => {
			const { onanswered } = await mount();
			await commentOnWords('How often?');

			await userEvent.click(approve());

			await waitFor(() => expect(onanswered).toHaveBeenCalled());
			expect(sent().review).toEqual([
				{
					path: 'docs/plan.md',
					startLine: 7,
					endLine: 7,
					heading: 'Risks',
					quote: 'Retries',
					body: 'How often?',
					attachmentIds: []
				}
			]);
		});

		it('spans the lines of a selection across blocks', async () => {
			await mount();
			const dialog = await drag(words(SHIP), 0, words(RETRY), 7);
			expect(within(dialog).getByText('§ Plan')).toBeInTheDocument();
			expect(within(dialog).getByText('L3–7')).toBeInTheDocument();
		});

		it.each([
			['Meta', { metaKey: true }],
			['Ctrl', { ctrlKey: true }]
		])('adds with %s+Enter', async (_, mods) => {
			await mount();
			await drag(words(RETRY), 0, words(RETRY), 7);
			await userEvent.type(await commentBox(), 'Quick');
			await fireEvent.keyDown(await commentBox(), { key: 'Enter', ...mods });
			expect(await screen.findByRole('listitem')).toHaveTextContent('Quick');
		});

		it('does not add on a plain Enter', async () => {
			await mount();
			await drag(words(RETRY), 0, words(RETRY), 7);
			await userEvent.type(await commentBox(), 'one{Enter}two');
			expect(await commentBox()).toHaveValue('one\ntwo');
			expect(screen.queryByRole('listitem')).not.toBeInTheDocument();
		});

		it('ignores a selection of a single character', async () => {
			await mount();
			select(words(RETRY), 0, words(RETRY), 1);
			await letGo();
			expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
		});

		it('ignores a selection outside the file', async () => {
			await mount();
			const ask = screen.getByText('The ask').firstChild!;
			select(ask, 0, ask, 5);
			await letGo();
			expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
		});

		it('opens on a selection made with the keyboard', async () => {
			await mount();
			select(words(RETRY), 0, words(RETRY), 7);
			await fireEvent.keyUp(document, { key: 'ArrowRight', shiftKey: true });
			expect(await composer()).toBeInTheDocument();
		});

		it('never replaces a composer that holds work', async () => {
			await mount();
			await drag(words(RETRY), 0, words(RETRY), 7);
			await userEvent.type(await commentBox(), 'half a thought');

			select(words(SHIP), 0, words(SHIP), 4);
			await letGo();

			expect(await commentBox()).toHaveValue('half a thought');
			expect(screen.getByRole('dialog')).toHaveTextContent('L7–7');
		});

		it('moves an empty composer to the new selection', async () => {
			await mount();
			await drag(words(RETRY), 0, words(RETRY), 7);

			select(words(SHIP), 0, words(SHIP), 4);
			await letGo();

			await waitFor(() => expect(screen.getByRole('dialog')).toHaveTextContent('L3–3'));
		});

		it('stays open when the page is clicked', async () => {
			await mount();
			await drag(words(RETRY), 0, words(RETRY), 7);
			await userEvent.type(await commentBox(), 'keep me');

			await userEvent.click(screen.getByText('Review'));

			expect(await commentBox()).toHaveValue('keep me');
		});

		it('closes on Esc and on Close, keeping nothing', async () => {
			await mount();
			await drag(words(RETRY), 0, words(RETRY), 7);
			await userEvent.keyboard('{Escape}');
			await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

			await drag(words(RETRY), 0, words(RETRY), 7);
			await userEvent.click(button('Close'));
			await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

			expect(screen.queryByRole('listitem')).not.toBeInTheDocument();
			expect(window.getSelection()?.isCollapsed).toBe(true);
		});

		it('keeps j and k for the comment being typed', async () => {
			await mount();
			await drag(words(RETRY), 0, words(RETRY), 7);
			await userEvent.type(await commentBox(), 'jk');
			expect(await commentBox()).toHaveValue('jk');
			expect(screen.getByRole('button', { name: /plan\.md/ })).toHaveAttribute(
				'aria-current',
				'true'
			);
		});

		it('keeps a comment on the file it was started on after moving to another', async () => {
			await mount();
			await drag(words(RETRY), 0, words(RETRY), 7);
			await userEvent.type(await commentBox(), 'Still about the plan');

			await userEvent.click(screen.getByRole('button', { name: /cfg\.yaml/ }));
			await userEvent.click(button('Add'));
			await userEvent.click(approve());

			await waitFor(() => expect(api.pings.answer).toHaveBeenCalled());
			expect(sent().review?.[0]).toMatchObject({
				path: 'docs/plan.md',
				body: 'Still about the plan'
			});
		});
	});

	describe('commenting on a whole file', () => {
		it('comments on the file with no lines or quote', async () => {
			await mount();
			await userEvent.click(button(/Comment on this file/));

			const dialog = await composer();
			expect(within(dialog).getByText('plan.md')).toBeInTheDocument();
			expect(within(dialog).getByText('whole file')).toBeInTheDocument();

			await comment('Too long');
			await userEvent.click(approve());

			await waitFor(() => expect(api.pings.answer).toHaveBeenCalled());
			expect(sent().review).toEqual([
				{
					path: 'docs/plan.md',
					startLine: undefined,
					endLine: undefined,
					heading: undefined,
					quote: undefined,
					body: 'Too long',
					attachmentIds: []
				}
			]);
		});

		it('offers an image only a comment as a whole', async () => {
			await mount(aGate(), [shot]);
			expect(screen.getByText(/An image is commented/)).toBeInTheDocument();
			await userEvent.click(button(/Comment on this image/));
			expect(await composer()).toHaveTextContent('shot.png');
		});

		it('returns to the comment being typed instead of starting another', async () => {
			await mount();
			await drag(words(RETRY), 0, words(RETRY), 7);
			await userEvent.type(await commentBox(), 'half a thought');

			await userEvent.click(button(/Comment on this file/));

			expect(await commentBox()).toHaveValue('half a thought');
			expect(await commentBox()).toHaveFocus();
			expect(screen.getByRole('dialog')).toHaveTextContent('L7–7');
		});
	});

	describe('editing and deleting', () => {
		it('edits a comment in place, keeping where it sits', async () => {
			await mount();
			await commentOnWords('Before');

			await userEvent.click(button('Edit'));
			const box = await commentBox();
			expect(box).toHaveValue('Before');
			await userEvent.clear(box);
			await userEvent.type(box, 'After');
			await userEvent.click(button('Save'));

			await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
			expect(screen.getAllByRole('listitem')).toHaveLength(1);
			expect(screen.getByRole('listitem')).toHaveTextContent('After');
			expect(stored().comments).toHaveLength(1);
			expect(stored().comments[0]).toMatchObject({ text: 'After', quote: 'Retries' });
		});

		it('opens the comment’s own file to edit it', async () => {
			await mount();
			await commentOnWords('On the plan');
			await userEvent.click(screen.getByRole('button', { name: /cfg\.yaml/ }));
			await userEvent.click(screen.getByRole('radio', { name: /All files/ }));

			await userEvent.click(button('Edit'));

			expect(await screen.findByText(SHIP)).toBeInTheDocument();
			expect(await commentBox()).toHaveValue('On the plan');
		});

		it('deletes a comment once confirmed', async () => {
			await mount();
			await commentOnWords('Gone soon');

			await userEvent.click(button('Delete comment'));
			expect(confirmState.current).toMatchObject({
				title: 'Delete this comment?',
				action: 'Delete comment'
			});
			confirmState.settle(true);

			await waitFor(() => expect(screen.queryByText('Gone soon')).not.toBeInTheDocument());
			expect(localStorage.getItem(KEY)).toBeNull();
		});

		it('keeps the comment when the delete is cancelled', async () => {
			await mount();
			await commentOnWords('Staying');

			await userEvent.click(button('Delete comment'));
			confirmState.settle(false);

			await waitFor(() => expect(confirmState.current).toBeNull());
			expect(screen.getByText('Staying')).toBeInTheDocument();
			expect(stored().comments).toHaveLength(1);
		});

		it('closes the composer of a comment being deleted', async () => {
			await mount();
			await commentOnWords('Gone soon');
			await userEvent.click(button('Edit'));
			await commentBox();

			await userEvent.click(button('Delete comment'));
			confirmState.settle(true);

			await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
		});
	});

	describe('moving around', () => {
		it('opens the file picked, read once', async () => {
			const { fetched } = await mount();
			await userEvent.click(screen.getByRole('button', { name: /cfg\.yaml/ }));
			expect(await screen.findByText('retries: 3')).toBeInTheDocument();
			expect(fetched.mock.calls.filter(([u]) => String(u).endsWith('/cfg'))).toHaveLength(1);
		});

		it('moves through the files with j and k', async () => {
			await mount();
			await userEvent.keyboard('j');
			expect(await screen.findByText('retries: 3')).toBeInTheDocument();
			await userEvent.keyboard('k');
			expect(await screen.findByText(SHIP)).toBeInTheDocument();
		});

		it('outlines a markdown file, not any other file', async () => {
			await mount();
			expect(screen.getByRole('navigation', { name: 'Document outline' })).toBeInTheDocument();
			await userEvent.keyboard('j');
			await screen.findByText('retries: 3');
			expect(
				screen.queryByRole('navigation', { name: 'Document outline' })
			).not.toBeInTheDocument();
		});

		it('counts comments on the file list and under the outline', async () => {
			await mount();
			await commentOnWords('One');
			const outline = screen.getByRole('navigation', { name: 'Document outline' });
			const files = screen.getByRole('navigation', { name: 'Files under review' });
			expect(within(outline).getByTitle('Risks')).toHaveTextContent('Risks1');
			expect(within(files).getByTitle('docs/plan.md')).toHaveTextContent('1');
		});

		it('jumps from the outline to the heading', async () => {
			const scroll = vi.spyOn(Element.prototype, 'scrollIntoView');
			await mount();
			await userEvent.click(screen.getByRole('button', { name: 'Risks' }));
			expect(scroll.mock.contexts[0]).toBe(
				screen.getByRole('heading', { name: 'Risks' }).closest('.block')
			);
		});

		it('opens a comment’s file and brings its text into view', async () => {
			const scroll = vi.spyOn(Element.prototype, 'scrollIntoView');
			await mount();
			await commentOnWords('On the plan');
			await userEvent.click(screen.getByRole('button', { name: /cfg\.yaml/ }));
			await userEvent.click(screen.getByRole('radio', { name: /All files/ }));

			await userEvent.click(button('§ Risks · L7'));

			await screen.findByText(SHIP);
			await waitFor(() =>
				expect(scroll.mock.contexts[0]).toBe(screen.getByText(RETRY).closest('.block'))
			);
		});

		it('scrolls up for a whole-file comment', async () => {
			const scroll = vi.spyOn(Element.prototype, 'scrollTo');
			await mount();
			await userEvent.click(button(/Comment on this file/));
			await comment('All of it');

			await userEvent.click(button('whole file'));

			await waitFor(() => expect(scroll).toHaveBeenCalledWith({ top: 0, behavior: 'smooth' }));
		});

		it.each([
			['text', [plan], /Select any text/],
			['an image', [shot], /An image is commented/],
			[
				'a binary',
				[aPingFile({ path: 'a.zip', mime: 'application/zip' })],
				/can only be commented/
			],
			['a file not captured', [aPingFile({ captured: false })], /can only be commented/]
		])('tells how to comment on %s', async (_, files, hint) => {
			await mount(aGate(), files);
			expect(screen.getByText(hint)).toBeInTheDocument();
		});
	});

	describe('answering a gate', () => {
		it('approves with nothing said', async () => {
			const { onanswered } = await mount();
			expect(approve()).toHaveTextContent(/^Approve$/);

			await userEvent.click(approve());

			await waitFor(() => expect(onanswered).toHaveBeenCalledOnce());
			expect(api.pings.answer).toHaveBeenCalledExactlyOnceWith('p1', {
				answer: 'approve',
				feedback: undefined,
				attachmentIds: [],
				review: [],
				content: undefined
			});
			expect(api.files.upload).not.toHaveBeenCalled();
		});

		it('says Approve carries the review once something is said', async () => {
			await mount();
			await userEvent.type(summary(), 'Mostly fine');
			expect(approve()).toHaveTextContent('Approve with review');
		});

		it('refuses Request changes with nothing said, and says why', async () => {
			await mount();
			expect(reject()).toBeDisabled();
			expect(reject()).toHaveAttribute(
				'title',
				'Say what should change: a comment, or the summary.'
			);
		});

		it('does not count a blank summary as saying something', async () => {
			await mount();
			await userEvent.type(summary(), '   ');
			expect(reject()).toBeDisabled();
		});

		it('requests changes on a summary, trimmed', async () => {
			const { onanswered } = await mount();
			await userEvent.type(summary(), '  Redo the risks  ');

			await userEvent.click(reject());

			await waitFor(() => expect(onanswered).toHaveBeenCalled());
			expect(sent()).toMatchObject({ answer: 'reject', feedback: 'Redo the risks', review: [] });
		});

		it('requests changes on a comment alone', async () => {
			await mount();
			await commentOnWords('Redo this');
			expect(reject()).toBeEnabled();

			await userEvent.click(reject());

			await waitFor(() => expect(api.pings.answer).toHaveBeenCalled());
			expect(sent()).toMatchObject({ answer: 'reject', feedback: undefined });
			expect(sent().review).toHaveLength(1);
		});

		it('holds both buttons while sending, busy on the one pressed', async () => {
			const answer = deferred();
			vi.mocked(api.pings.answer).mockReturnValue(answer.promise);
			await mount();
			await userEvent.type(summary(), 'Redo');

			await userEvent.click(reject());

			expect(approve()).toBeDisabled();
			expect(reject()).toBeDisabled();
			expect(reject()).toHaveAttribute('aria-busy', 'true');
			expect(approve()).toHaveAttribute('aria-busy', 'false');
			answer.resolve();
		});

		it('forgets the draft once answered', async () => {
			const { onanswered } = await mount();
			await commentOnWords('Sent soon');
			expect(localStorage.getItem(KEY)).not.toBeNull();

			await userEvent.click(approve());

			await waitFor(() => expect(onanswered).toHaveBeenCalled());
			expect(localStorage.getItem(KEY)).toBeNull();
		});

		it('keeps everything and says why when the answer fails', async () => {
			vi.mocked(api.pings.answer).mockRejectedValueOnce(new Error('ping already answered'));
			const { onanswered } = await mount();
			await commentOnWords('Keep me');

			await userEvent.click(approve());

			expect(await screen.findByText('ping already answered')).toBeInTheDocument();
			expect(onanswered).not.toHaveBeenCalled();
			expect(stored().comments).toHaveLength(1);
			expect(approve()).toBeEnabled();
			expect(screen.getByText('Keep me')).toBeInTheDocument();
		});

		it('clears the error on the next try', async () => {
			vi.mocked(api.pings.answer).mockRejectedValueOnce(new Error('ping already answered'));
			const { onanswered } = await mount();
			await userEvent.click(approve());
			await screen.findByText('ping already answered');

			await userEvent.click(approve());

			await waitFor(() => expect(onanswered).toHaveBeenCalled());
			expect(screen.queryByText('ping already answered')).not.toBeInTheDocument();
		});
	});

	describe('attachments', () => {
		const review = new File(['r'], 'review.txt', { type: 'text/plain' });
		const first = new File(['1'], 'first.txt', { type: 'text/plain' });
		const second = new File(['2'], 'second.txt', { type: 'text/plain' });
		const pick = (root: HTMLElement) => root.querySelector<HTMLInputElement>('input[type="file"]')!;
		const uploaded = (...ids: string[]) =>
			vi.mocked(api.files.upload).mockResolvedValue(ids.map((id) => ({ id })) as never);

		it('sends the review’s attachments up, then their ids', async () => {
			uploaded('up-1');
			const { container } = await mount();
			await userEvent.upload(pick(container), review);

			await userEvent.click(approve());

			await waitFor(() => expect(api.pings.answer).toHaveBeenCalled());
			expect(api.files.upload).toHaveBeenCalledExactlyOnceWith([review]);
			expect(sent().attachmentIds).toEqual(['up-1']);
		});

		it('gives each comment its own among the answer’s, in one upload', async () => {
			uploaded('up-1', 'up-2', 'up-3');
			const { container } = await mount();

			await userEvent.click(button(/Comment on this file/));
			await userEvent.upload(pick(await composer()), first);
			await comment('On the plan');

			await userEvent.click(screen.getByRole('button', { name: /cfg\.yaml/ }));
			await userEvent.click(button(/Comment on this file/));
			await userEvent.type(await commentBox(), 'No attachment');
			await userEvent.click(button('Add'));
			await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

			await userEvent.click(button(/Comment on this file/));
			await userEvent.upload(pick(await composer()), second);
			await comment('On the config');

			await userEvent.upload(pick(container), review);
			await userEvent.click(approve());

			await waitFor(() => expect(api.pings.answer).toHaveBeenCalled());
			expect(api.files.upload).toHaveBeenCalledExactlyOnceWith([first, second, review]);
			expect(sent().attachmentIds).toEqual(['up-1', 'up-2', 'up-3']);
			expect(sent().review?.map((c) => [c.body, c.attachmentIds])).toEqual([
				['On the plan', ['up-1']],
				['No attachment', []],
				['On the config', ['up-2']]
			]);
		});

		it('sends no answer when the upload fails', async () => {
			vi.mocked(api.files.upload).mockRejectedValue(new Error('too large'));
			const { container, onanswered } = await mount();
			await userEvent.upload(pick(container), review);

			await userEvent.click(approve());

			expect(await screen.findByText('too large')).toBeInTheDocument();
			expect(api.pings.answer).not.toHaveBeenCalled();
			expect(onanswered).not.toHaveBeenCalled();
		});

		it('keeps a comment’s attachments in the list but not in the draft', async () => {
			await mount();
			await userEvent.click(button(/Comment on this file/));
			await userEvent.upload(pick(await composer()), first);
			await comment('With a file');

			expect(screen.getByRole('listitem')).toHaveTextContent('first.txt');
			expect(stored().comments[0].files).toBeUndefined();
		});
	});

	describe('answering a question', () => {
		const answerBox = () => screen.getByRole('textbox', { name: /Your answer/ });

		describe('with one free-text field', () => {
			const ask = () => aQuestion(textField());

			it('takes the summary as the answer itself', async () => {
				const { onanswered } = await mount(ask());
				await userEvent.type(answerBox(), '  Use Redis  ');

				await userEvent.click(button('Send review'));

				await waitFor(() => expect(onanswered).toHaveBeenCalled());
				expect(sent()).toMatchObject({
					answer: 'accept',
					feedback: undefined,
					content: { q: 'Use Redis' }
				});
			});

			it('needs the text before it sends', async () => {
				await mount(ask());
				expect(button('Send review')).toBeDisabled();
				await userEvent.type(answerBox(), 'x');
				expect(button('Send review')).toBeEnabled();
			});

			it('offers no gate buttons', async () => {
				await mount(ask());
				expect(screen.queryByRole('button', { name: /^Approve/ })).not.toBeInTheDocument();
				expect(screen.queryByRole('button', { name: 'Request changes' })).not.toBeInTheDocument();
			});

			it('carries comments alongside', async () => {
				await mount(ask());
				await commentOnWords('See here');
				await userEvent.type(answerBox(), 'Use Redis');

				await userEvent.click(button('Send review'));

				await waitFor(() => expect(api.pings.answer).toHaveBeenCalled());
				expect(sent().review).toHaveLength(1);
			});
		});

		describe('with one option to pick', () => {
			const ask = () =>
				aQuestion(
					textField({
						options: [
							{ id: 'redis', label: 'Redis', detail: 'Already deployed' },
							{ id: 'pg', label: 'Postgres' }
						]
					})
				);

			it('offers a button each, the detail on hover', async () => {
				await mount(ask());
				expect(button('Redis')).toHaveAttribute('title', 'Already deployed');
				expect(button('Postgres')).toBeEnabled();
				expect(screen.queryByRole('button', { name: 'Send review' })).not.toBeInTheDocument();
			});

			it('answers with the option picked and the summary alongside', async () => {
				const { onanswered } = await mount(ask());
				await userEvent.type(summary(), 'Cheaper');

				await userEvent.click(button('Postgres'));

				await waitFor(() => expect(onanswered).toHaveBeenCalled());
				expect(sent()).toMatchObject({
					answer: 'accept',
					feedback: 'Cheaper',
					content: { q: 'pg' }
				});
			});

			it('shows busy on the option pressed only', async () => {
				const answer = deferred();
				vi.mocked(api.pings.answer).mockReturnValue(answer.promise);
				await mount(ask());

				await userEvent.click(button('Postgres'));

				expect(button('Postgres')).toHaveAttribute('aria-busy', 'true');
				expect(button('Redis')).toHaveAttribute('aria-busy', 'false');
				expect(button('Redis')).toBeDisabled();
				answer.resolve();
			});
		});

		describe('with several fields', () => {
			const ask = () =>
				aQuestion(
					textField({ id: 'name', title: 'Name' }),
					textField({ id: 'why', title: 'Why', required: false })
				);

			it('draws them, and holds Send until the required ones are answered', async () => {
				await mount(ask());
				expect(button('Send review')).toBeDisabled();
				expect(button('Send review')).toHaveAttribute('title', 'Still to answer: Name');

				await userEvent.type(screen.getByRole('textbox', { name: 'Name' }), 'Ada');

				expect(button('Send review')).toBeEnabled();
				expect(button('Send review')).toHaveAttribute('title', '');
			});

			it('sends the fields, blanks dropped, and the summary', async () => {
				const { onanswered } = await mount(ask());
				await userEvent.type(screen.getByRole('textbox', { name: 'Name' }), '  Ada ');
				await userEvent.type(summary(), 'Notes');

				await userEvent.click(button('Send review'));

				await waitFor(() => expect(onanswered).toHaveBeenCalled());
				expect(sent()).toMatchObject({
					answer: 'accept',
					feedback: 'Notes',
					content: { name: 'Ada' }
				});
			});
		});

		it('sends a form-less question with what the summary says', async () => {
			await mount(aGate({ answerKind: 'form', form: undefined }));
			await userEvent.type(summary(), 'Noted');

			await userEvent.click(button('Send review'));

			await waitFor(() => expect(api.pings.answer).toHaveBeenCalled());
			expect(sent()).toMatchObject({ answer: 'accept', feedback: 'Noted', content: {} });
		});

		it('declines with a reason, and only with one', async () => {
			const { onanswered } = await mount(aQuestion(textField()));
			expect(button('Decline')).toBeDisabled();
			expect(button('Decline')).toHaveAttribute(
				'title',
				'Say in the summary why none of this fits — the agent reads it as your answer.'
			);

			await userEvent.type(answerBox(), 'None of these');
			expect(button('Decline')).toHaveAttribute(
				'title',
				'None of this fits — answer with your reason instead'
			);
			await userEvent.click(button('Decline'));

			await waitFor(() => expect(onanswered).toHaveBeenCalled());
			expect(sent()).toMatchObject({
				answer: 'decline',
				feedback: 'None of these',
				content: undefined
			});
		});
	});

	describe('the draft', () => {
		const onFile = (over = {}) =>
			aComment({ id: 'kept', path: 'docs/plan.md', text: 'From before', ...over });
		const save = (draft: unknown) => localStorage.setItem(KEY, JSON.stringify(draft));

		it('brings back the comments and summary saved in this browser', async () => {
			save({ comments: [onFile()], summary: 'Half done' });
			await mount();
			expect(await screen.findByText('From before')).toBeInTheDocument();
			expect(summary()).toHaveValue('Half done');
			expect(approve()).toHaveTextContent('Approve with review');
		});

		it('drops a saved comment on a file no longer shown', async () => {
			save({ comments: [onFile({ path: 'gone.md', text: 'Lost' }), onFile()], summary: '' });
			await mount();
			await screen.findByText('From before');

			await userEvent.click(approve());

			await waitFor(() => expect(api.pings.answer).toHaveBeenCalled());
			expect(sent().review?.map((c) => c.body)).toEqual(['From before']);
		});

		it('puts an old bare list on the first file', async () => {
			save([{ text: 'Older still' }]);
			await mount();
			expect(await screen.findByText('Older still')).toBeInTheDocument();
		});

		it('saves the summary as it is typed', async () => {
			await mount();
			await userEvent.type(summary(), 'abc');
			expect(stored().summary).toBe('abc');
		});

		it('says the draft is saved once something is said', async () => {
			await mount();
			expect(screen.getByText(/Drafts are kept in this browser/)).toBeInTheDocument();
			await userEvent.type(summary(), 'abc');
			expect(screen.getByText('Draft saved', { exact: false })).toBeInTheDocument();
		});

		describe('before the first file arrives', () => {
			/** The review on screen, its first file still on the way. */
			async function waiting() {
				vi.mocked(api.pings.payload).mockResolvedValue({ files: [plan] });
				const body = deferred<string>();
				serveSnapshots({ plan: body.promise });
				render(DocReview, { ping: aGate(), onanswered: vi.fn() });
				await screen.findByRole('button', { name: /Comment on this/ });
				return body;
			}

			it('keeps the saved comments when the summary is typed', async () => {
				save({ comments: [onFile()], summary: '' });
				const body = await waiting();

				await userEvent.type(summary(), 'x');
				body.resolve(PLAN);

				expect(await screen.findByText('From before')).toBeInTheDocument();
				expect(summary()).toHaveValue('x');
				expect(stored().comments).toHaveLength(1);
				expect(stored().summary).toBe('x');
			});

			it('adds what was typed to the saved summary', async () => {
				save({ comments: [], summary: 'Half done' });
				const body = await waiting();

				await userEvent.type(summary(), 'more');
				body.resolve(PLAN);

				await waitFor(() => expect(summary()).toHaveValue('Half done\n\nmore'));
				expect(stored().summary).toBe('Half done\n\nmore');
			});

			it('keeps a comment added with the saved ones', async () => {
				save({ comments: [onFile()], summary: '' });
				const body = await waiting();

				await userEvent.click(button(/Comment on this/));
				await comment('Too soon');
				body.resolve(PLAN);

				expect(await screen.findByText('From before')).toBeInTheDocument();
				expect(screen.getByText('Too soon')).toBeInTheDocument();
				expect(stored().comments).toHaveLength(2);
			});
		});

		it('leaves the saved draft alone when the files cannot be read', async () => {
			save({ comments: [onFile()], summary: 'Half done' });
			vi.mocked(api.pings.payload).mockRejectedValue(new Error('engine down'));
			render(DocReview, { ping: aGate(), onanswered: vi.fn() });
			await screen.findByText('engine down');

			await userEvent.type(summary(), 'x');

			expect(stored().comments).toHaveLength(1);
			expect(stored().summary).toBe('Half done');
		});
	});
});
