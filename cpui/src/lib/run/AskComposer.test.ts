import { createEvent, fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { flushSync, tick } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { AskMessage, Attachment } from '$lib/domain';
import { bindable } from '$test/fixtures/app.svelte';
import { settled } from '$test/fixtures/run-components';
import { aFile } from '$test/fixtures/run.svelte';
import { anAskMessage, anAttachment, aViewStore } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import { makeAnnotation } from './annotation';
import type { RunStore } from './store.svelte';
import AskComposer from './AskComposer.svelte';

vi.mock('$lib/api', () => ({
	api: {
		ask: { send: vi.fn() },
		files: { upload: vi.fn(), limits: vi.fn() }
	}
}));

const RUN = '01TESTRUN0000000000000ABCD';
const box = () => screen.getByRole('textbox', { name: 'Ask' });
const askBtn = () => screen.getByRole('button', { name: /^(Ask|Answering…)$/ });
const shot = () => aFile('shot.png', 'image/png');
function paste(...files: File[]) {
	const e = createEvent.paste(box(), { clipboardData: { files } });
	fireEvent(box(), e);
	return e;
}

function setup(over: { session?: string; busy?: boolean } = {}) {
	const rs = aViewStore();
	const refresh = vi.spyOn(rs, 'refreshFiles').mockResolvedValue();
	const props = bindable({ rs, session: 's1', busy: false, onasked: vi.fn(), error: '', ...over });
	const view = render(AskComposer, props);
	return { ...view, rs, props, refresh };
}

describe('AskComposer', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		vi.mocked(api.files.limits).mockResolvedValue({
			maxBytes: 1024,
			maxFiles: 8,
			mimeAllow: ['image/*', 'text/plain']
		});
		vi.mocked(api.ask.send).mockResolvedValue(anAskMessage({ seq: 9, sessionId: 's1' }));
		URL.createObjectURL = vi.fn(() => 'blob:preview'); // jsdom has none
	});

	describe('asking', () => {
		it('asks on Enter, trimmed, in the session on screen, and empties the box', async () => {
			const { props } = setup();

			await userEvent.type(box(), '  Where is the retry?  {Enter}');

			expect(api.ask.send).toHaveBeenCalledExactlyOnceWith(RUN, 'Where is the retry?', 's1', []);
			expect(box()).toHaveValue('');
			await vi.waitFor(() =>
				expect(props.onasked).toHaveBeenCalledExactlyOnceWith(
					expect.objectContaining({ seq: 9 }),
					's1'
				)
			);
		});

		it('asks in no session when there is none yet', async () => {
			const { props } = setup({ session: '' });
			await userEvent.type(box(), 'Why?{Enter}');

			expect(api.ask.send).toHaveBeenCalledExactlyOnceWith(RUN, 'Why?', undefined, []);
			await vi.waitFor(() => expect(props.onasked).toHaveBeenCalledWith(expect.anything(), ''));
		});

		it('leaves an Enter that confirms an IME composition alone', async () => {
			setup();
			await userEvent.type(box(), 'こんにちは');

			await fireEvent.keyDown(box(), { key: 'Enter', isComposing: true });

			expect(api.ask.send).not.toHaveBeenCalled();
		});

		it('keeps writing on Shift+Enter', async () => {
			setup();
			await userEvent.type(box(), 'one{Shift>}{Enter}{/Shift}two');
			expect(api.ask.send).not.toHaveBeenCalled();
			expect(box()).toHaveValue('one\ntwo');
		});

		it('asks from the button', async () => {
			setup();
			await userEvent.type(box(), 'Why?');
			await userEvent.click(askBtn());
			expect(api.ask.send).toHaveBeenCalledOnce();
		});

		it('offers nothing to ask until there are words', async () => {
			setup();
			expect(askBtn()).toBeDisabled();

			await userEvent.type(box(), '   {Enter}');
			expect(askBtn()).toBeDisabled();
			expect(api.ask.send).not.toHaveBeenCalled();
		});

		it('takes the box away while the session answers', () => {
			setup({ busy: true });
			expect(box()).toBeDisabled();
			expect(askBtn()).toHaveTextContent('Answering…');
			expect(askBtn()).toBeDisabled();
		});

		it('holds the button while the question is in flight, asking it once', async () => {
			const sent = deferred<never>();
			vi.mocked(api.ask.send).mockReturnValue(sent.promise);
			setup();

			await userEvent.type(box(), 'Why?{Enter}');
			expect(askBtn()).toHaveAttribute('aria-busy', 'true');

			await userEvent.type(box(), 'Again');
			expect(askBtn()).toBeDisabled();

			await userEvent.type(box(), '{Enter}');
			expect(api.ask.send).toHaveBeenCalledOnce();
		});

		it('gives the words back with the reason when refused', async () => {
			vi.mocked(api.ask.send).mockRejectedValue(new Error('an answer is being written'));
			const { props } = setup();
			await userEvent.type(box(), 'Why?{Enter}');

			expect(await screen.findByText('an answer is being written')).toBeInTheDocument();
			expect(props.error).toBe('an answer is being written');
			expect(box()).toHaveValue('Why?');
			expect(props.onasked).not.toHaveBeenCalled();
		});

		it('keeps what was typed meanwhile over the refused words', async () => {
			const sent = deferred<never>();
			vi.mocked(api.ask.send).mockReturnValue(sent.promise);
			setup();
			await userEvent.type(box(), 'First{Enter}');
			await userEvent.type(box(), 'Second');

			sent.reject(new Error('nope'));

			expect(await screen.findByText('nope')).toBeInTheDocument();
			expect(box()).toHaveValue('Second');
		});

		it('shows the pane’s own trouble, and clears it on the next attempt', async () => {
			const { props } = setup();
			props.error = 'the pane is off';
			flushSync();
			expect(screen.getByText('the pane is off')).toBeInTheDocument();

			await userEvent.type(box(), 'Why?{Enter}');
			expect(screen.queryByText('the pane is off')).not.toBeInTheDocument();
		});
	});

	describe('files', () => {
		it('takes a pasted image as a chip, not as text', async () => {
			setup();
			await settled();
			expect(paste(shot()).defaultPrevented).toBe(true);
			expect(await screen.findByText('shot.png')).toBeInTheDocument();
			expect(box()).toHaveValue('');
		});

		it('leaves a paste without files to the browser', async () => {
			setup();
			await settled();
			expect(paste().defaultPrevented).toBe(false);
		});

		it('uploads first, then asks with the ids, and re-reads the run’s files', async () => {
			vi.mocked(api.files.upload).mockResolvedValue([anAttachment({ id: 'att-1' })]);
			const { refresh } = setup();
			await settled();
			paste(shot());
			await userEvent.type(box(), 'what is this?{Enter}');
			await vi.waitFor(() =>
				expect(api.ask.send).toHaveBeenCalledExactlyOnceWith(RUN, 'what is this?', 's1', ['att-1'])
			);
			expect(api.files.upload).toHaveBeenCalledExactlyOnceWith([expect.any(File)]);
			await vi.waitFor(() => expect(refresh).toHaveBeenCalledOnce());
			expect(screen.queryByText('shot.png')).not.toBeInTheDocument();
		});

		it('re-reads nothing for a question without files', async () => {
			const { props, refresh } = setup();
			await userEvent.type(box(), 'Why?{Enter}');
			await vi.waitFor(() => expect(props.onasked).toHaveBeenCalledOnce());
			expect(refresh).not.toHaveBeenCalled();
		});

		it('asks with files alone', async () => {
			vi.mocked(api.files.upload).mockResolvedValue([anAttachment({ id: 'att-1' })]);
			setup();
			await settled();
			paste(shot());
			await vi.waitFor(() => expect(askBtn()).toBeEnabled());
			await userEvent.click(askBtn());
			await vi.waitFor(() =>
				expect(api.ask.send).toHaveBeenCalledExactlyOnceWith(RUN, '', 's1', ['att-1'])
			);
		});

		it('refuses an oversized paste, which never goes up', async () => {
			setup();
			await settled();
			paste(aFile('big.png', 'image/png', 2048));
			expect(await screen.findByText('over the 1 KB limit')).toBeInTheDocument();
			expect(askBtn()).toBeDisabled();
			await userEvent.type(box(), '{Enter}');
			expect(api.files.upload).not.toHaveBeenCalled();
		});

		it('asks nothing when the upload fails, keeping draft and files', async () => {
			vi.mocked(api.files.upload).mockRejectedValue(new Error('too big for the server'));
			setup();
			await settled();
			paste(shot());
			await userEvent.type(box(), 'what is this?{Enter}');
			expect(await screen.findByText('too big for the server')).toBeInTheDocument();
			expect(api.ask.send).not.toHaveBeenCalled();
			expect(box()).toHaveValue('what is this?');
			expect(screen.getByText('shot.png')).toBeInTheDocument();
		});

		it('asks in the session on screen at Enter, whichever is picked while the files go up', async () => {
			const up = deferred<Attachment[]>();
			vi.mocked(api.files.upload).mockReturnValue(up.promise);
			const { props } = setup({ session: 's2' });
			await settled();
			paste(shot());
			await userEvent.type(box(), 'More?{Enter}');
			props.session = 's1';
			flushSync();

			up.resolve([anAttachment({ id: 'att-1' })]);
			await vi.waitFor(() =>
				expect(api.ask.send).toHaveBeenCalledExactlyOnceWith(RUN, 'More?', 's2', ['att-1'])
			);
			await vi.waitFor(() => expect(props.onasked).toHaveBeenCalledWith(expect.anything(), 's2'));
		});

		it('holds the question as asked while its files go up, keeping what comes after', async () => {
			const up = deferred<Attachment[]>();
			vi.mocked(api.files.upload).mockReturnValue(up.promise);
			const sent = deferred<AskMessage>();
			vi.mocked(api.ask.send).mockReturnValue(sent.promise);
			setup();
			await settled();
			paste(shot());
			await userEvent.type(box(), 'what is this?{Enter}');
			await vi.waitFor(() => expect(box()).toHaveAttribute('readonly'));

			// While the files go up, neither the words nor the files move.
			await userEvent.type(box(), ' and more');
			paste(aFile('early.png', 'image/png'));
			expect(box()).toHaveValue('what is this?');
			expect(screen.queryByText('early.png')).not.toBeInTheDocument();
			expect(screen.queryByRole('button', { name: 'Remove shot.png' })).not.toBeInTheDocument();
			expect(screen.getByRole('button', { name: /Attach$/ })).toBeDisabled();

			// Once they are up, the box is the next question's.
			up.resolve([anAttachment({ id: 'att-1' })]);
			await vi.waitFor(() =>
				expect(api.ask.send).toHaveBeenCalledExactlyOnceWith(RUN, 'what is this?', 's1', ['att-1'])
			);
			expect(box()).not.toHaveAttribute('readonly');
			await userEvent.type(box(), 'next');
			paste(aFile('later.png', 'image/png'));
			expect(await screen.findByText('later.png')).toBeInTheDocument();

			sent.resolve(anAskMessage({ seq: 9, sessionId: 's1' }));
			await vi.waitFor(() => expect(screen.queryByText('shot.png')).not.toBeInTheDocument());
			expect(screen.getByText('later.png')).toBeInTheDocument();
			expect(box()).toHaveValue('next');
		});
	});

	describe('annotations', () => {
		const chips = () => screen.queryByRole('group', { name: 'Lines to send' });
		const comments = () =>
			screen.getAllByRole<HTMLInputElement>('textbox', { name: /^Comment on/ }).map((e) => e.value);
		const QUOTED = '`run.go:3-4`\n\n```go\na\nb\n```';
		async function land(rs: RunStore, comment = 'Why?') {
			rs.composers.active!.add(
				makeAnnotation({ repo: '', path: 'run.go' }, 3, 4, 'a\nb', comment, [])
			);
			await tick();
		}

		it('is the Ask target while on screen, and no longer once gone', () => {
			const { rs, unmount } = setup();
			expect(rs.composers.active?.kind).toBe('ask');
			unmount();
			expect(rs.composers.active).toBeNull();
		});

		it('takes a chip, editable, and asks it ahead of the words', async () => {
			const { rs } = setup();
			await land(rs);
			expect(chips()).toBeInTheDocument();

			const comment = screen.getByRole('textbox', { name: 'Comment on run.go:3-4' });
			await userEvent.clear(comment);
			await userEvent.type(comment, 'What calls this?');
			await userEvent.type(box(), 'and why{Enter}');

			expect(api.ask.send).toHaveBeenCalledExactlyOnceWith(
				RUN,
				`${QUOTED}\n\nWhat calls this?\n\nand why`,
				's1',
				[]
			);
			expect(chips()).not.toBeInTheDocument();
		});

		it('asks with chips alone', async () => {
			const { rs } = setup();
			expect(askBtn()).toBeDisabled();
			await land(rs, '');

			await userEvent.click(askBtn());
			expect(api.ask.send).toHaveBeenCalledExactlyOnceWith(RUN, QUOTED, 's1', []);
		});

		it('drops a removed chip', async () => {
			const { rs } = setup();
			await land(rs);
			await userEvent.click(screen.getByRole('button', { name: 'Remove run.go:3-4' }));

			expect(chips()).not.toBeInTheDocument();
			expect(askBtn()).toBeDisabled();
		});

		it('gives the chips back with the words when refused, ahead of any landed since', async () => {
			const sent = deferred<never>();
			vi.mocked(api.ask.send).mockReturnValue(sent.promise);
			const { rs } = setup();
			await land(rs, 'first');
			await userEvent.type(box(), 'go on{Enter}');
			expect(chips()).not.toBeInTheDocument();
			await land(rs, 'second');

			sent.reject(new Error('an answer is being written'));

			await vi.waitFor(() => expect(box()).toHaveValue('go on'));
			expect(comments()).toEqual(['first', 'second']);
		});

		it('keeps the chips when the files cannot go up', async () => {
			vi.mocked(api.files.upload).mockRejectedValue(new Error('too big for the server'));
			const { rs } = setup();
			await settled();
			await land(rs);
			paste(shot());
			await userEvent.click(askBtn());

			expect(await screen.findByText('too big for the server')).toBeInTheDocument();
			expect(api.ask.send).not.toHaveBeenCalled();
			expect(comments()).toEqual(['Why?']);
		});
	});

	it('grows with the text, up to its cap', async () => {
		const height = vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(90);
		setup();
		await userEvent.type(box(), 'a');
		expect(box()).toHaveStyle({ height: '90px' });

		height.mockReturnValue(900);
		await userEvent.type(box(), 'b');
		expect(box()).toHaveStyle({ height: '220px' });
		height.mockRestore();
	});
});
