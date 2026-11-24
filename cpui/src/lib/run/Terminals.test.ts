import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { ApiError } from '$lib/api/client';
import type { RunCheckout, TerminalSession } from '$lib/domain';
import { confirmDialog } from '$lib/ui/confirm.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { deferred } from '$test/helpers';
import { mounted } from '$test/fixtures/TerminalStub.svelte';
import { rememberSession, rememberedSession } from './terminalSessions.svelte';
import Terminals, { MAX_SESSIONS, POLL_MS } from './Terminals.svelte';

vi.mock('$lib/api', () => ({
	api: { workspace: { terminals: vi.fn(), closeTerminal: vi.fn() } }
}));
vi.mock('$lib/ui/confirm.svelte', () => ({ confirmDialog: vi.fn() }));
vi.mock('./Terminal.svelte', () => import('$test/fixtures/TerminalStub.svelte'));

const RUN = 'r1';
const primary: RunCheckout = {
	name: 'app',
	dir: 'repo',
	url: 'https://git.test/app.git',
	branch: 'feat/x',
	source: 'main',
	primary: true
};
const lib: RunCheckout = { ...primary, name: 'lib', dir: 'repos/lib', primary: false };

const aSession = (id: string, n: number, o: Partial<TerminalSession> = {}): TerminalSession => ({
	id,
	n,
	repo: '',
	dir: '/home/dev/repo',
	startedAt: '2026-11-24T10:00:00Z',
	attached: 1,
	busy: false,
	command: '',
	...o
});
const s1 = aSession('s1', 1);
const s2 = aSession('s2', 2, { repo: 'lib', dir: '/home/dev/repos/lib' });

const terminals = vi.mocked(api.workspace.terminals);
const closeTerminal = vi.mocked(api.workspace.closeTerminal);

function show(props: { visible?: boolean; repo?: string; dir?: string } = {}) {
	return render(Terminals, {
		runId: RUN,
		visible: true,
		checkouts: [primary, lib],
		repo: '',
		dir: 'repo',
		...props
	});
}
/** The terminal on screen, once the list has been read. */
const shownTerminal = () => screen.findByTestId('terminal');
const last = () => mounted.at(-1)!;
const tab = (name: RegExp) => screen.getByRole('tab', { name });
const toastTexts = () => toasts.list.map((x) => x.text);

describe('Terminals', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		localStorage.clear();
		mounted.length = 0;
		toasts.list = [];
		vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
	});
	afterEach(() => vi.useRealTimers());

	describe('which session it shows', () => {
		it('the one last shown here, when it is still listed', async () => {
			rememberSession(RUN, 's1');
			terminals.mockResolvedValue([s1, s2]);
			show();

			expect((await shownTerminal()).dataset.session).toBe('s1');
			expect(tab(/^1/)).toHaveAttribute('aria-selected', 'true');
		});

		it('else the newest', async () => {
			rememberSession(RUN, 'gone');
			terminals.mockResolvedValue([s1, s2]);
			show();

			const term = await shownTerminal();
			expect(term.dataset.session).toBe('s2');
			expect(term.dataset.dir).toBe('repos/lib');
			expect(term.dataset.repo).toBe('lib');
			expect(rememberedSession(RUN)).toBe('s2');
		});

		it('else a new one, in the repo picked', async () => {
			terminals.mockResolvedValue([]);
			show({ repo: 'lib', dir: 'repos/lib' });

			const term = await shownTerminal();
			expect(term.dataset.session).toBe('');
			expect(term.dataset.repo).toBe('lib');
			expect(term.dataset.dir).toBe('repos/lib');
			expect(tab(/opening…/)).toHaveAttribute('aria-selected', 'true');
		});

		it('says it is reading the list until it has it', async () => {
			const list = deferred<TerminalSession[]>();
			terminals.mockReturnValue(list.promise);
			show();

			expect(screen.getByRole('status')).toHaveTextContent('Loading…');
			list.resolve([s1]);
			expect((await shownTerminal()).dataset.session).toBe('s1');
		});

		it('switches to the tab picked, and remembers it', async () => {
			terminals.mockResolvedValue([s1, s2]);
			show();
			await shownTerminal();

			await userEvent.click(tab(/^1/));

			await vi.waitFor(() => expect(last().session).toBe('s1'));
			expect(mounted).toHaveLength(2);
			expect(mounted[0].unmounted).toBe(true);
			expect(rememberedSession(RUN)).toBe('s1');
		});

		it('shows where each session opened, unless the checkout is unknown', async () => {
			terminals.mockResolvedValue([s1, s2, aSession('s3', 3, { repo: 'gone', dir: '/x' })]);
			show();
			await shownTerminal();

			expect(tab(/^1 · ~\/repo$/)).toBeInTheDocument();
			expect(tab(/^2 · ~\/repos\/lib$/)).toBeInTheDocument();
			expect(tab(/^3$/)).toBeInTheDocument();
			expect((await shownTerminal()).dataset.dir).toBe('');
		});

		it('shows no place without the run’s checkouts', async () => {
			terminals.mockResolvedValue([s1]);
			render(Terminals, { runId: RUN, visible: true });

			expect((await shownTerminal()).dataset.dir).toBe('');
			expect(tab(/^1$/)).toBeInTheDocument();
		});
	});

	describe('a new session', () => {
		it('opens on "+", and becomes the one shown and remembered, without a remount', async () => {
			terminals.mockResolvedValue([s1]);
			show({ repo: 'lib', dir: 'repos/lib' });
			await shownTerminal();

			await userEvent.click(screen.getByRole('button', { name: 'New terminal session' }));
			await vi.waitFor(() => expect(last().session).toBe(''));
			expect((await shownTerminal()).dataset.repo).toBe('lib');

			const s3 = aSession('s3', 3, { repo: 'lib' });
			terminals.mockResolvedValue([s1, s3]);
			last().named('s3');

			expect(rememberedSession(RUN)).toBe('s3');
			await vi.waitFor(() => expect(tab(/^3/)).toHaveAttribute('aria-selected', 'true'));
			expect(terminals).toHaveBeenCalledTimes(2);
			expect(mounted).toHaveLength(2);
			expect(toastTexts()).toEqual([]);
		});

		it('is not taken for ended by a list asked before it was named', async () => {
			terminals.mockResolvedValue([]);
			show();
			await shownTerminal();
			const late = deferred<TerminalSession[]>();
			terminals.mockReturnValueOnce(late.promise);
			await vi.advanceTimersByTimeAsync(POLL_MS);

			terminals.mockResolvedValue([s1]);
			last().named('s1');
			await vi.waitFor(() => expect(tab(/^1/)).toHaveAttribute('aria-selected', 'true'));
			late.resolve([]);
			await tick();

			expect(toastTexts()).toEqual([]);
			expect(tab(/^1/)).toBeInTheDocument();
		});

		it('cannot be opened past the workspace’s cap', async () => {
			terminals.mockResolvedValue(
				Array.from({ length: MAX_SESSIONS }, (_, i) => aSession(`s${i}`, i + 1))
			);
			show();
			await shownTerminal();

			const add = screen.getByRole('button', { name: 'New terminal session' });
			expect(add).toBeDisabled();
			expect(add).toHaveAttribute(
				'title',
				`This workspace has ${MAX_SESSIONS} terminal sessions open: close one first`
			);
		});
	});

	describe('a busy session', () => {
		const busy = aSession('s1', 1, { busy: true, command: 'opencode' });

		it('shows what runs in it, and that it keeps the workspace awake', async () => {
			terminals.mockResolvedValue([busy]);
			show();
			await shownTerminal();

			const t1 = tab(/^1 · ~\/repo · opencode$/);
			expect(t1).toHaveAttribute('title', 'running opencode: keeps the workspace awake');
			expect(t1.querySelector('.live-dot.hue-running')).not.toBeNull();
		});

		it('says a program runs even when it is not named', async () => {
			terminals.mockResolvedValue([aSession('s1', 1, { busy: true })]);
			show();
			await shownTerminal();

			expect(tab(/^1/)).toHaveAttribute('title', 'running a program: keeps the workspace awake');
		});

		it('loses the marker once a later poll says it is idle', async () => {
			terminals.mockResolvedValue([busy]);
			show();
			await shownTerminal();

			terminals.mockResolvedValue([s1]);
			await vi.advanceTimersByTimeAsync(POLL_MS);

			await vi.waitFor(() => expect(tab(/^1 · ~\/repo$/)).not.toHaveAttribute('title'));
			expect(tab(/^1/).querySelector('.live-dot')).toBeNull();
		});

		it('counts who else is attached', async () => {
			terminals.mockResolvedValue([aSession('s1', 1, { attached: 3 })]);
			show();
			await shownTerminal();

			expect(tab(/^1 · ~\/repo3$/)).toHaveAttribute('title', '3 attached');
		});
	});

	describe('closing', () => {
		it('asks first, then closes the session and shows the next', async () => {
			vi.mocked(confirmDialog).mockResolvedValue(true);
			closeTerminal.mockResolvedValue();
			rememberSession(RUN, 's1');
			terminals.mockResolvedValue([s1, s2]);
			show();
			await shownTerminal();

			terminals.mockResolvedValue([s2]);
			await userEvent.click(screen.getByRole('button', { name: 'Close session 1' }));

			expect(confirmDialog).toHaveBeenCalledExactlyOnceWith({
				title: 'Close terminal session 1?',
				body: 'Its shell ends for everyone attached to it, and its scrollback is lost.',
				action: 'Close session',
				tone: 'danger'
			});
			expect(closeTerminal).toHaveBeenCalledExactlyOnceWith(RUN, 's1');
			await vi.waitFor(() => expect(last().session).toBe('s2'));
			expect(screen.queryByRole('tab', { name: /^1/ })).not.toBeInTheDocument();
			expect(toastTexts()).toEqual([]);
		});

		it('names the program running in it', async () => {
			vi.mocked(confirmDialog).mockResolvedValue(false);
			terminals.mockResolvedValue([aSession('s1', 1, { busy: true, command: 'vim<x>' })]);
			show();
			await shownTerminal();

			await userEvent.click(screen.getByRole('button', { name: 'Close session 1' }));

			expect(vi.mocked(confirmDialog).mock.calls[0][0].body).toBe(
				'vim&#60;x&#62; is still running in it: it ends with the shell, for everyone attached to it.'
			);
		});

		it('leaves the session alone when the confirmation is cancelled', async () => {
			vi.mocked(confirmDialog).mockResolvedValue(false);
			terminals.mockResolvedValue([s1]);
			show();
			await shownTerminal();

			await userEvent.click(screen.getByRole('button', { name: 'Close session 1' }));

			expect(closeTerminal).not.toHaveBeenCalled();
			expect(tab(/^1/)).toBeInTheDocument();
			expect(mounted).toHaveLength(1);
		});

		it('offers a new one once the last is closed', async () => {
			vi.mocked(confirmDialog).mockResolvedValue(true);
			closeTerminal.mockRejectedValue(new ApiError('gone', 404, {}));
			terminals.mockResolvedValue([s1]);
			show();
			await shownTerminal();

			terminals.mockResolvedValue([]);
			await userEvent.click(screen.getByRole('button', { name: 'Close session 1' }));

			expect(await screen.findByText('No terminal session is open.')).toBeInTheDocument();
			expect(screen.queryByTestId('terminal')).not.toBeInTheDocument();
			expect(toastTexts()).toEqual([]);

			await userEvent.click(
				within(screen.getByText('No terminal session is open.').parentElement!).getByRole(
					'button',
					{ name: /New terminal session/ }
				)
			);
			expect((await shownTerminal()).dataset.session).toBe('');
		});

		it('keeps the session switched to while the confirmation was open', async () => {
			const answer = deferred<boolean>();
			vi.mocked(confirmDialog).mockReturnValue(answer.promise);
			closeTerminal.mockResolvedValue();
			rememberSession(RUN, 's1');
			terminals.mockResolvedValue([s1, s2]);
			show();
			await shownTerminal();
			await userEvent.click(screen.getByRole('button', { name: 'Close session 1' }));
			await userEvent.click(tab(/^2/));
			await vi.waitFor(() => expect(last().session).toBe('s2'));

			terminals.mockResolvedValue([s2]);
			answer.resolve(true);

			await vi.waitFor(() => expect(closeTerminal).toHaveBeenCalledWith(RUN, 's1'));
			await vi.waitFor(() =>
				expect(screen.queryByRole('tab', { name: /^1/ })).not.toBeInTheDocument()
			);
			expect(mounted).toHaveLength(2);
			expect(last().unmounted).toBe(false);
		});

		it('says when the close failed', async () => {
			vi.mocked(confirmDialog).mockResolvedValue(true);
			closeTerminal.mockRejectedValue(new Error('engine down'));
			terminals.mockResolvedValue([s1]);
			show();
			await shownTerminal();

			await userEvent.click(screen.getByRole('button', { name: 'Close session 1' }));

			await vi.waitFor(() =>
				expect(toasts.list.map((x) => [x.text, x.tone])).toEqual([
					['Could not close the session: engine down', 'bad']
				])
			);
		});
	});

	describe('a session that ends', () => {
		it('says so, and moves to the next one', async () => {
			rememberSession(RUN, 's1');
			terminals.mockResolvedValue([s1, s2]);
			show();
			await shownTerminal();

			terminals.mockResolvedValue([s2]);
			await vi.advanceTimersByTimeAsync(POLL_MS);

			await vi.waitFor(() => expect(last().session).toBe('s2'));
			expect(toastTexts()).toEqual(['A terminal session ended.']);
			expect(rememberedSession(RUN)).toBe('s2');
		});

		it('reads the list again when its shell exits', async () => {
			terminals.mockResolvedValue([s1, s2]);
			show();
			await shownTerminal();

			terminals.mockResolvedValue([s1]);
			last().ended();

			await vi.waitFor(() => expect(last().session).toBe('s1'));
			expect(terminals).toHaveBeenCalledTimes(2);
			expect(toastTexts()).toEqual(['A terminal session ended.']);
		});

		it('says the workspace restarted when every session went, and opens a new one', async () => {
			terminals.mockResolvedValue([s1]);
			show();
			await shownTerminal();

			terminals.mockResolvedValue([]);
			await vi.advanceTimersByTimeAsync(POLL_MS);

			await vi.waitFor(() => expect(last().session).toBe(''));
			expect(toasts.list.map((x) => [x.text, x.tone])).toEqual([
				['The workspace restarted: its terminal sessions ended with it.', 'warn']
			]);
		});
	});

	describe('polling', () => {
		it('reads the list every few seconds while the pane shows', async () => {
			terminals.mockResolvedValue([s1]);
			show();
			await shownTerminal();

			await vi.advanceTimersByTimeAsync(POLL_MS * 2);

			expect(terminals).toHaveBeenCalledTimes(3);
			expect(terminals).toHaveBeenCalledWith(RUN);
		});

		it('rests while the pane is hidden, and reads again when it returns', async () => {
			terminals.mockResolvedValue([s1]);
			const { rerender } = show();
			await shownTerminal();

			await rerender({ visible: false });
			await vi.advanceTimersByTimeAsync(POLL_MS * 3);
			expect(terminals).toHaveBeenCalledOnce();

			await rerender({ visible: true });
			expect(terminals).toHaveBeenCalledTimes(2);
		});

		it('reads nothing while it starts hidden', async () => {
			show({ visible: false });
			await vi.advanceTimersByTimeAsync(POLL_MS);
			expect(terminals).not.toHaveBeenCalled();
		});

		it('acts on no answer that lands after it left', async () => {
			const list = deferred<TerminalSession[]>();
			terminals.mockReturnValue(list.promise);
			const { unmount } = show();
			unmount();

			list.resolve([s1]);
			await tick();

			expect(mounted).toEqual([]);
		});

		it('keeps what it shows when a later read fails', async () => {
			terminals.mockResolvedValue([s1]);
			show();
			await shownTerminal();

			terminals.mockRejectedValue(new Error('blip'));
			await vi.advanceTimersByTimeAsync(POLL_MS);

			expect(tab(/^1/)).toBeInTheDocument();
			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});
	});

	describe('a list it cannot read', () => {
		it('says so, and tries again on Retry', async () => {
			terminals.mockRejectedValue(new Error('engine down'));
			show();

			expect(await screen.findByRole('alert')).toHaveTextContent(
				'Could not list the terminal sessions.'
			);
			expect(screen.getByText('engine down')).toBeInTheDocument();

			terminals.mockResolvedValue([s1]);
			await userEvent.click(screen.getByRole('button', { name: /Retry/ }));

			expect((await shownTerminal()).dataset.session).toBe('s1');
		});

		it('falls back to one terminal, ending with the view, for a workspace that predates sessions', async () => {
			terminals.mockRejectedValue(new ApiError('Not Implemented', 501, {}));
			const { rerender } = show();

			const term = await shownTerminal();
			expect(term.dataset.legacy).toBe('true');
			expect(term.dataset.session).toBe('');
			expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
			expect(screen.queryByRole('button', { name: 'New terminal session' })).toBeNull();

			// Polling stops; the repo picker opens a new shell, as before sessions.
			await vi.advanceTimersByTimeAsync(POLL_MS * 2);
			expect(terminals).toHaveBeenCalledOnce();
			await rerender({ repo: 'lib', dir: 'repos/lib' });
			await vi.waitFor(() => expect(mounted).toHaveLength(2));
			expect((await shownTerminal()).dataset.repo).toBe('lib');
		});
	});
});
