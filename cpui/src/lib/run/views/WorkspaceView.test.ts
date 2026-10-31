import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { RunCheckout, WorkspaceChanges } from '$lib/domain';
import { aRun } from '$test/fixtures';
import { NOW, aLiveRun, anEvent, aViewStore } from '$test/fixtures/run-views';
import WorkspaceView from './WorkspaceView.svelte';

vi.mock('$lib/api', () => ({
	api: {
		workspace: {
			changes: vi.fn(),
			diff: vi.fn(),
			tree: vi.fn(),
			file: vi.fn(),
			terminalUrl: vi.fn()
		}
	}
}));
vi.mock('@xterm/xterm', () => ({
	Terminal: class {
		cols = 80;
		rows = 24;
		loadAddon() {}
		open() {}
		focus() {}
		write() {}
		dispose() {}
		onData = () => ({ dispose() {} });
		onResize = () => ({ dispose() {} });
	}
}));
vi.mock('@xterm/addon-fit', () => ({
	FitAddon: class {
		fit() {}
	}
}));

const RUN = '01TESTRUN0000000000000ABCD';
const primary: RunCheckout = {
	name: 'app',
	dir: 'repo',
	url: 'https://git.test/app.git',
	branch: 'feat/x',
	source: 'main',
	primary: true
};
const lib: RunCheckout = { ...primary, name: 'lib', dir: 'repos/lib', primary: false };
const changes = (n = 0): WorkspaceChanges => ({
	branch: 'feat/x',
	head: 'abc',
	base: 'origin/main',
	mergeBase: 'def',
	commits: [],
	files: Array.from({ length: n }, (_, i) => ({
		path: `f${i}.go`,
		status: 'M',
		additions: 1,
		deletions: 0,
		binary: false,
		committed: true
	}))
});
const repoRadio = (name: string | RegExp) => screen.getByRole('radio', { name });

function show(
	kind: 'changes' | 'files' | 'terminal',
	rs = aViewStore(aLiveRun()),
	props: { visible?: boolean } = {}
) {
	const onrecord = vi.fn();
	const view = render(WorkspaceView, { rs, kind, visible: true, onrecord, ...props });
	return { ...view, rs, onrecord };
}

describe('WorkspaceView', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		vi.mocked(api.workspace.changes).mockResolvedValue(changes());
		vi.mocked(api.workspace.diff).mockResolvedValue({ path: '', diff: '', truncated: false });
		vi.mocked(api.workspace.tree).mockResolvedValue([]);
		vi.mocked(api.workspace.terminalUrl).mockReturnValue('ws://terminal.test');
		// jsdom would dial it for real.
		vi.stubGlobal(
			'WebSocket',
			class {
				static OPEN = 1;
				readyState = 0;
				close() {}
				send() {}
			}
		);
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	describe('without a workspace', () => {
		it.each(['changes', 'files', 'terminal'] as const)('touches nothing on %s', (kind) => {
			show(kind, aViewStore(aRun({ state: 'completed' })));

			expect(api.workspace.changes).not.toHaveBeenCalled();
			expect(api.workspace.tree).not.toHaveBeenCalled();
			expect(api.workspace.terminalUrl).not.toHaveBeenCalled();
			expect(screen.getByText('No live workspace')).toBeInTheDocument();
		});

		it('says one is coming on a run yet to provision', () => {
			show('files', aViewStore(aRun({ state: 'provisioning' })));
			expect(screen.getByText('No workspace yet')).toBeInTheDocument();
			expect(
				screen.getByText('It appears here once the run has provisioned one.')
			).toBeInTheDocument();
		});

		it('says when it was destroyed, and that the record keeps everything', () => {
			vi.setSystemTime(NOW + 7_200_000 + 1000);
			show(
				'changes',
				aViewStore(aLiveRun({ state: 'completed', envStatus: 'absent' }), {
					events: [anEvent(1, 'env_destroyed')]
				})
			);
			expect(screen.getByText('Workspace destroyed 2h ago')).toBeInTheDocument();
			expect(
				screen.getByText('The Record keeps every snapshot and log this run captured.')
			).toBeInTheDocument();
		});

		it('says an archived run’s went with it', () => {
			show('terminal', aViewStore(aLiveRun({ state: 'archived' })));
			expect(screen.getByText('Workspace archived away')).toBeInTheDocument();
		});

		it('opens the Record', async () => {
			const { onrecord } = show('files', aViewStore(aRun({ state: 'failed' })));
			await userEvent.click(screen.getByRole('button', { name: 'Open the Record' }));
			expect(onrecord).toHaveBeenCalledOnce();
		});

		it('gives way when the workspace appears', async () => {
			const { rs } = show('files', aViewStore(aRun({ state: 'provisioning' })));
			rs.run = aLiveRun();
			await tick();

			expect(screen.queryByText('No workspace yet')).not.toBeInTheDocument();
			expect(api.workspace.tree).toHaveBeenCalledOnce();
		});

		it('takes over when the workspace goes', async () => {
			const { rs } = show('files');
			await tick();

			rs.run = aLiveRun({ envStatus: 'absent', state: 'failed' });
			await tick();

			expect(screen.getByText('No live workspace')).toBeInTheDocument();
		});
	});

	describe('which view', () => {
		it('reads what changed on the changes view', async () => {
			show('changes');
			await vi.waitFor(() => expect(api.workspace.changes).toHaveBeenCalledWith(RUN, ''));
			expect(api.workspace.tree).not.toHaveBeenCalled();
		});

		it('reads the tree on the files view', async () => {
			show('files');
			await vi.waitFor(() => expect(api.workspace.tree).toHaveBeenCalledWith(RUN, '', ''));
			expect(api.workspace.changes).not.toHaveBeenCalled();
		});

		it('opens a shell on the terminal view', async () => {
			show('terminal');
			await vi.waitFor(() =>
				expect(api.workspace.terminalUrl).toHaveBeenCalledWith(RUN, 80, 24, '')
			);
		});

		it('shows the shell’s directory: the primary checkout’s', async () => {
			show('terminal', aViewStore(aLiveRun({ checkouts: [primary] })));
			expect(await screen.findByText('~/repo')).toBeInTheDocument();
		});

		it('has no directory to show without checkouts', () => {
			show('terminal');
			expect(screen.queryByText(/^~\//)).not.toBeInTheDocument();
		});
	});

	describe('the repo picker', () => {
		const multi = () => aViewStore(aLiveRun({ checkouts: [primary, lib] }));

		it('stays away on a single repo', () => {
			show('files', aViewStore(aLiveRun({ checkouts: [primary] })));
			expect(screen.queryByRole('radiogroup')).not.toBeInTheDocument();
			expect(screen.queryByText('read-only · first 1 MB')).not.toBeInTheDocument();
		});

		it('starts on the primary', () => {
			show('files', multi());
			expect(repoRadio(/^app/)).toBeChecked();
		});

		it('moves the changes view to the repo picked', async () => {
			show('changes', multi());
			await userEvent.click(repoRadio('lib'));
			expect(api.workspace.changes).toHaveBeenLastCalledWith(RUN, 'lib');
		});

		it('moves the files view to the repo picked', async () => {
			show('files', multi());
			await userEvent.click(repoRadio('lib'));
			expect(api.workspace.tree).toHaveBeenLastCalledWith(RUN, '', 'lib');
		});

		it('opens a new shell in the repo picked, and says where', async () => {
			show('terminal', multi());
			await userEvent.click(repoRadio('lib'));

			await vi.waitFor(() =>
				expect(api.workspace.terminalUrl).toHaveBeenLastCalledWith(RUN, 80, 24, 'lib')
			);
			expect(await screen.findByText('~/repos/lib')).toBeInTheDocument();
		});

		it.each([
			['changes', 'refreshes every 5s'],
			['files', 'read-only · first 1 MB'],
			['terminal', 'xterm · a shell in the repo']
		] as const)('explains the %s view', (kind, hint) => {
			show(kind, multi());
			expect(screen.getByText(hint)).toBeInTheDocument();
		});

		it('does not promise a refresh on a finished run', () => {
			show('changes', aViewStore(aLiveRun({ state: 'completed', checkouts: [primary, lib] })));
			expect(screen.queryByText('refreshes every 5s')).not.toBeInTheDocument();
		});
	});

	describe('the changes badge', () => {
		it('counts the primary repo’s changes', async () => {
			vi.mocked(api.workspace.changes).mockResolvedValue(changes(3));
			const { rs } = show('changes');
			await vi.waitFor(() => expect(rs.changesCount).toBe(3));
		});

		it('keeps that count while another repo is on screen', async () => {
			vi.mocked(api.workspace.changes).mockResolvedValue(changes(3));
			const { rs } = show('changes', aViewStore(aLiveRun({ checkouts: [primary, lib] })));
			await vi.waitFor(() => expect(rs.changesCount).toBe(3));

			vi.mocked(api.workspace.changes).mockResolvedValue(changes(9));
			await userEvent.click(repoRadio('lib'));
			await vi.waitFor(() => expect(api.workspace.changes).toHaveBeenCalledTimes(2));
			await tick();

			expect(rs.changesCount).toBe(3);
		});
	});

	describe('keeping the changes fresh', () => {
		beforeEach(() => {
			vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
		});

		it('refreshes while the run works', async () => {
			show('changes');
			await vi.advanceTimersByTimeAsync(5000);
			expect(api.workspace.changes).toHaveBeenCalledTimes(2);
		});

		it('leaves a finished run’s changes as they are', async () => {
			show('changes', aViewStore(aLiveRun({ state: 'completed' })));
			await vi.advanceTimersByTimeAsync(5000);
			expect(api.workspace.changes).toHaveBeenCalledOnce();
		});

		it('rests while the window is hidden, and catches up when it returns', async () => {
			const { rerender } = show('changes');
			await rerender({ visible: false });
			await vi.advanceTimersByTimeAsync(5000);
			expect(api.workspace.changes).toHaveBeenCalledOnce();

			await rerender({ visible: true });
			expect(api.workspace.changes).toHaveBeenCalledTimes(2);
		});
	});
});
