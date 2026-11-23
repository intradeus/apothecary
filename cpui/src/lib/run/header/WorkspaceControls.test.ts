import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { aRun, aRunStore, aViewerProject } from '$test/fixtures';
import { deferred } from '$test/helpers';
import { HeaderActions } from './actions.svelte';
import WorkspaceControls from './WorkspaceControls.svelte';

vi.mock('$lib/api', () => ({ api: { runs: { openEditor: vi.fn(), wake: vi.fn() } } }));

const props = { envBtn: null, rebuildBtn: null, envOpen: false, rebuildOpen: false };
const paused = () => aRunStore(aRun({ state: 'paused', envId: 'env-1' }), { hasWorkspace: true });
const rebuildBtn = () => screen.getByRole('button', { name: /rebuild/i });
const editorBtn = () => screen.getByRole('button', { name: /editor/i });

describe('WorkspaceControls', () => {
	beforeEach(() => {
		vi.spyOn(window, 'open').mockReturnValue(null);
	});
	afterEach(() => {
		fleet.projects = [];
	});

	it('holds the editor and the rebuild from a viewer of the workshop', () => {
		fleet.projects = [aViewerProject({ id: 'p1' })];
		const rs = aRunStore(aRun({ state: 'paused', envId: 'env-1', projectId: 'p1' }), {
			hasWorkspace: true
		});
		render(WorkspaceControls, { rs, actions: new HeaderActions(), ...props });
		for (const b of [editorBtn(), rebuildBtn()]) {
			expect(b).toBeDisabled();
			expect(b).toHaveAttribute('title', 'Your role in this workshop does not allow this');
		}
	});

	it('offers a rebuild on a paused workspace', () => {
		render(WorkspaceControls, { rs: paused(), actions: new HeaderActions(), ...props });
		expect(rebuildBtn()).toBeEnabled();
	});

	it('refuses a rebuild while the run is live', () => {
		const rs = aRunStore(aRun({ state: 'running', envId: 'env-1' }), { hasWorkspace: true });
		render(WorkspaceControls, { rs, actions: new HeaderActions(), ...props });
		expect(rebuildBtn()).toBeDisabled();
	});

	// The bug this replaced: one shared value, so the editor finishing cleared
	// the rebuild's lock and offered a second build on top of the first.
	it('keeps Rebuild held when the editor opens during a build', async () => {
		const actions = new HeaderActions();
		const build = deferred<boolean>();
		void actions.run('rebuild', () => build.promise);
		vi.mocked(api.runs.openEditor).mockResolvedValue('https://editor.test');

		render(WorkspaceControls, { rs: paused(), actions, ...props });
		await userEvent.click(editorBtn());

		expect(api.runs.openEditor).toHaveBeenCalledOnce();
		expect(actions.has('editor')).toBe(false);
		expect(rebuildBtn()).toBeDisabled();
		expect(rebuildBtn()).toHaveAttribute('aria-busy', 'true');
		build.resolve(true);
	});

	// A build outlives the page: after a reload only the engine knows about it.
	it('holds Rebuild on the engine status alone', () => {
		const rs = aRunStore(aRun({ state: 'paused', envId: 'env-1', envStatus: 'rebuilding' }), {
			hasWorkspace: true
		});
		render(WorkspaceControls, { rs, actions: new HeaderActions(), ...props });
		expect(rebuildBtn()).toBeDisabled();
	});

	it('offers Wake on an asleep workspace only', () => {
		render(WorkspaceControls, { rs: paused(), actions: new HeaderActions(), ...props });
		expect(screen.queryByRole('button', { name: 'Wake' })).toBeNull();
	});

	it('wakes an asleep workspace, held while it wakes, and shows a failure', async () => {
		const rs = aRunStore(aRun({ state: 'completed', envId: 'env-1', envStatus: 'asleep' }), {
			hasWorkspace: true
		});
		const wake = deferred<void>();
		vi.mocked(api.runs.wake).mockReturnValue(wake.promise);
		render(WorkspaceControls, { rs, actions: new HeaderActions(), ...props });
		const btn = screen.getByRole('button', { name: 'Wake' });
		expect(btn).toHaveAttribute('title', expect.stringMatching(/stopped after sitting unused/));
		await userEvent.click(btn);
		expect(api.runs.wake).toHaveBeenCalledWith(rs.run!.id);
		expect(btn).toBeDisabled();
		wake.resolve();
		await vi.waitFor(() => expect(btn).toBeEnabled());

		vi.mocked(api.runs.wake).mockRejectedValue(new Error('no node has room'));
		await userEvent.click(btn);
		await vi.waitFor(() => expect(rs.error).toBe('no node has room'));
	});

	it('holds Wake from a viewer, and while the engine is waking it', () => {
		fleet.projects = [aViewerProject({ id: 'p1' })];
		const rs = aRunStore(
			aRun({ state: 'completed', envId: 'env-1', envStatus: 'waking', projectId: 'p1' }),
			{ hasWorkspace: true }
		);
		render(WorkspaceControls, { rs, actions: new HeaderActions(), ...props });
		const btn = screen.getByRole('button', { name: 'Wake' });
		expect(btn).toBeDisabled();
		expect(btn).toHaveAttribute('title', 'Your role in this workshop does not allow this');
	});

	it('shows a failed editor on the store and closes the blank tab', async () => {
		const tab = { close: vi.fn(), location: { href: '' } };
		vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window);
		vi.mocked(api.runs.openEditor).mockRejectedValue(new Error('no code-server'));
		const rs = paused();

		render(WorkspaceControls, { rs, actions: new HeaderActions(), ...props });
		await userEvent.click(editorBtn());

		expect(tab.close).toHaveBeenCalled();
		expect(rs.error).toBe('no code-server');
	});
});
