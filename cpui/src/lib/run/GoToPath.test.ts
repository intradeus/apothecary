import { fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { ApiError } from '$lib/api/client';
import type { WorkspaceEntry, WorkspaceFile } from '$lib/domain';
import { anEntry, aWorkspaceFile } from '$test/fixtures/review';
import { deferred } from '$test/helpers';
import GoToPath from './GoToPath.svelte';

vi.mock('$lib/api', () => ({ api: { workspace: { pathTree: vi.fn(), pathFile: vi.fn() } } }));

const pathTree = vi.mocked(api.workspace.pathTree);
const pathFile = vi.mocked(api.workspace.pathFile);

const PREDATES =
	'This workspace predates opening paths outside the checkouts: restart or rebuild it.';
const entry = (path: string, dir = false): WorkspaceEntry => anEntry({ path, dir });
const dirs: Record<string, WorkspaceEntry[]> = {
	'~': [entry('/home/dev/.bashrc'), entry('/home/dev/src', true)],
	'/': [entry('/etc', true), entry('/home', true)],
	'/etc': [entry('/etc/Hostname'), entry('/etc/hosts'), entry('/etc/ssl', true)],
	'/etc/ssl': [entry('/etc/ssl/certs', true)]
};

const input = () => screen.getByRole('combobox', { name: 'Path' });
const option = (name: string) => screen.getByRole('option', { name });
const names = () => screen.queryAllByRole('option').map((o) => o.textContent?.slice(1).trim());
/** Let the typing pause pass, and the listing land. */
const pause = () => vi.advanceTimersByTimeAsync(150);
async function type(value: string) {
	await fireEvent.input(input(), { target: { value } });
	await pause();
}
const key = (k: string, init: KeyboardEventInit = {}) =>
	fireEvent.keyDown(input(), { key: k, ...init });
/** Waits (advancing the fake timers) until the text shows. */
const shows = (text: string) =>
	vi.waitFor(() => expect(screen.getByText(text)).toBeInTheDocument());

function mount(open = true) {
	const onclose = vi.fn();
	const onfile = vi.fn();
	const ondir = vi.fn();
	const view = render(GoToPath, { runId: 'r1', open, onclose, onfile, ondir });
	return { ...view, onclose, onfile, ondir };
}

describe('GoToPath', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] });
		pathTree.mockImplementation(async (_run, path) => dirs[path] ?? []);
		pathFile.mockImplementation(async (_run, path) =>
			aWorkspaceFile({ path: path.replace(/^~/, '/home/dev') })
		);
	});
	afterEach(() => vi.useRealTimers());

	describe('listing', () => {
		it('starts at home, listing it once typing pauses', async () => {
			mount();
			expect(input()).toHaveValue('~/');
			await vi.advanceTimersByTimeAsync(149);
			expect(pathTree).not.toHaveBeenCalled();
			await vi.advanceTimersByTimeAsync(1);
			expect(pathTree).toHaveBeenCalledExactlyOnceWith('r1', '~');
			expect(names()).toEqual(['.bashrc', 'src/']);
			expect(screen.getByRole('listbox', { name: 'In this folder' })).toBeInTheDocument();
		});

		it('lists nothing while closed', async () => {
			mount(false);
			await pause();
			expect(pathTree).not.toHaveBeenCalled();
			expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
		});

		it('lists a typed directory, once typing pauses on it', async () => {
			mount();
			await fireEvent.input(input(), { target: { value: '/e' } });
			await fireEvent.input(input(), { target: { value: '/etc/' } });
			await pause();
			expect(pathTree).toHaveBeenCalledExactlyOnceWith('r1', '/etc');
			expect(names()).toEqual(['Hostname', 'hosts', 'ssl/']);
			expect(input()).toHaveAttribute('aria-expanded', 'true');
		});

		it('lists the root as itself', async () => {
			mount();
			await type('/');
			expect(pathTree).toHaveBeenLastCalledWith('r1', '/');
			expect(names()).toEqual(['etc/', 'home/']);
		});

		it('filters by what is typed of the name, ignoring case, without listing again', async () => {
			mount();
			await type('/etc/');
			await type('/etc/ho');
			expect(names()).toEqual(['Hostname', 'hosts']);
			await type('/etc/HOS');
			expect(names()).toEqual(['Hostname', 'hosts']);
			expect(pathTree).toHaveBeenCalledExactlyOnceWith('r1', '/etc');
		});

		it('says when nothing matches', async () => {
			mount();
			await type('/etc/zz');
			expect(names()).toEqual([]);
			expect(screen.getByText('Nothing here starts with that.')).toBeInTheDocument();
			expect(input()).toHaveAttribute('aria-expanded', 'false');
		});

		it('drops an answer for a folder typed before the latest', async () => {
			const first = deferred<WorkspaceEntry[]>();
			const second = deferred<WorkspaceEntry[]>();
			pathTree.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
			mount();
			await type('/a/');
			await type('/b/');
			second.resolve([entry('/b/new')]);
			await pause();
			first.resolve([entry('/a/old')]);
			await pause();
			expect(names()).toEqual(['new']);
		});

		it('drops a failure for a folder typed before the latest', async () => {
			const first = deferred<WorkspaceEntry[]>();
			pathTree.mockReturnValueOnce(first.promise).mockResolvedValueOnce([entry('/b/new')]);
			mount();
			await type('/a/');
			await type('/b/');
			first.reject(new ApiError('permission denied', 403, {}));
			await pause();
			expect(names()).toEqual(['new']);
			expect(screen.queryByText('permission denied')).not.toBeInTheDocument();
		});

		it('says why a folder cannot be listed', async () => {
			pathTree.mockRejectedValue(new ApiError('permission denied', 403, {}));
			mount();
			await type('/root/');
			expect(screen.getByText('permission denied')).toBeInTheDocument();
			expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
			expect(input()).not.toHaveAttribute('aria-controls');
		});

		it('says when the workspace predates listing', async () => {
			pathTree.mockRejectedValue(new ApiError('Not Implemented', 501, {}));
			mount();
			await pause();
			expect(screen.getByText(PREDATES)).toBeInTheDocument();
		});

		it('asks for an absolute path, listing nothing until one is typed', async () => {
			const { onfile, ondir } = mount();
			await pause();
			pathTree.mockClear();
			await type('etc/hosts');
			expect(screen.getByText('Start with / or ~.')).toBeInTheDocument();
			expect(screen.getByRole('button', { name: 'Open' })).toBeDisabled();
			await key('Enter');
			await pause();
			expect(pathTree).not.toHaveBeenCalled();
			expect(pathFile).not.toHaveBeenCalled();
			expect(onfile).not.toHaveBeenCalled();
			expect(ondir).not.toHaveBeenCalled();
		});

		it('shows the help, not the hint, on an empty field', async () => {
			mount();
			await type('');
			expect(screen.queryByText('Start with / or ~.')).not.toBeInTheDocument();
			expect(
				screen.getByText('An absolute path, or one under ~. Tab completes; Enter opens.')
			).toBeInTheDocument();
		});
	});

	describe('the keyboard', () => {
		it('moves the highlight with the arrows, within the list', async () => {
			mount();
			await type('/etc/');
			expect(input()).not.toHaveAttribute('aria-activedescendant');
			expect(await key('ArrowDown')).toBe(false);
			expect(option('Hostname')).toHaveAttribute('aria-selected', 'true');
			expect(input()).toHaveAttribute('aria-activedescendant', option('Hostname').id);
			await key('ArrowDown');
			await key('ArrowDown');
			await key('ArrowDown');
			expect(option('ssl/')).toHaveAttribute('aria-selected', 'true');
			await key('ArrowUp');
			expect(option('hosts')).toHaveAttribute('aria-selected', 'true');
			await key('ArrowUp');
			await key('ArrowUp');
			await key('ArrowUp');
			expect(input()).not.toHaveAttribute('aria-activedescendant');
		});

		it('keeps the arrows still with nothing listed', async () => {
			mount();
			await type('/etc/zz');
			expect(await key('ArrowDown')).toBe(false);
			expect(input()).not.toHaveAttribute('aria-activedescendant');
		});

		it('forgets the highlight on typing', async () => {
			mount();
			await type('/etc/');
			await key('ArrowDown');
			await type('/etc/h');
			expect(input()).not.toHaveAttribute('aria-activedescendant');
		});

		it('completes the highlighted folder on Tab, and lists it', async () => {
			mount();
			await type('/etc/');
			await key('ArrowDown');
			await key('ArrowDown');
			await key('ArrowDown');
			expect(await key('Tab')).toBe(false);
			expect(input()).toHaveValue('/etc/ssl/');
			await pause();
			expect(pathTree).toHaveBeenLastCalledWith('r1', '/etc/ssl');
			expect(names()).toEqual(['certs/']);
		});

		it('completes the first match on Tab with none highlighted; a file takes no slash', async () => {
			mount();
			await type('/etc/hostn');
			await key('Tab');
			expect(input()).toHaveValue('/etc/Hostname');
		});

		it('completes nothing on Shift+Tab, and leaves Tab alone with nothing to complete', async () => {
			mount();
			await type('/etc/');
			await key('Tab', { shiftKey: true });
			expect(input()).toHaveValue('/etc/');
			await type('/etc/zz');
			input().focus();
			expect(await key('Tab')).toBe(true);
			expect(await key('x')).toBe(true);
			expect(input()).toHaveValue('/etc/zz');
		});
	});

	describe('opening', () => {
		it('opens the highlighted file by its absolute path, then closes', async () => {
			const { onfile, ondir, onclose } = mount();
			await type('/etc/');
			await key('ArrowDown');
			await key('ArrowDown');
			await key('Enter');
			await vi.waitFor(() => expect(onfile).toHaveBeenCalledWith('/etc/hosts'));
			expect(pathFile).toHaveBeenCalledExactlyOnceWith('r1', '/etc/hosts');
			expect(onclose).toHaveBeenCalledOnce();
			expect(ondir).not.toHaveBeenCalled();
		});

		it('sets the highlighted folder as the root, reading nothing', async () => {
			const { onfile, ondir, onclose } = mount();
			await type('/etc/');
			await key('ArrowUp');
			await key('ArrowDown');
			await key('ArrowDown');
			await key('ArrowDown');
			await key('ArrowDown');
			await key('Enter');
			expect(ondir).toHaveBeenCalledExactlyOnceWith('/etc/ssl');
			expect(onclose).toHaveBeenCalledOnce();
			expect(pathFile).not.toHaveBeenCalled();
			expect(onfile).not.toHaveBeenCalled();
		});

		it('reads ~/.bashrc, opening it by its expanded path', async () => {
			const { onfile } = mount();
			await type('~/.bashrc');
			await key('Enter');
			await vi.waitFor(() => expect(onfile).toHaveBeenCalledWith('/home/dev/.bashrc'));
			expect(pathFile).toHaveBeenCalledExactlyOnceWith('r1', '~/.bashrc');
		});

		it('reads /etc/hosts typed before its folder is listed', async () => {
			const { onfile } = mount();
			await fireEvent.input(input(), { target: { value: '/etc/hosts' } });
			await key('Enter');
			await vi.waitFor(() => expect(onfile).toHaveBeenCalledWith('/etc/hosts'));
			expect(pathFile).toHaveBeenCalledExactlyOnceWith('r1', '/etc/hosts');
		});

		it('opens a folder the listing names, typed without its slash', async () => {
			const { ondir } = mount();
			await type('/etc/ssl');
			await key('Enter');
			expect(ondir).toHaveBeenCalledExactlyOnceWith('/etc/ssl');
			expect(pathFile).not.toHaveBeenCalled();
		});

		it.each([
			['/etc/', '/etc'],
			['/', '/'],
			['~', '~'],
			['~/src/', '~/src']
		])('opens %j, a folder by how it is typed, as %j', async (typed, root) => {
			const { ondir, onclose } = mount();
			await type(typed);
			await key('Enter');
			expect(ondir).toHaveBeenCalledExactlyOnceWith(root);
			expect(onclose).toHaveBeenCalledOnce();
			expect(pathFile).not.toHaveBeenCalled();
		});

		it('opens a folder typed without its slash before it is listed, when refused as a file', async () => {
			pathFile.mockRejectedValue(new ApiError('is a directory', 400, {}));
			const { ondir, onfile, onclose } = mount();
			await fireEvent.input(input(), { target: { value: '/etc/ssl' } });
			await key('Enter');
			await vi.waitFor(() => expect(ondir).toHaveBeenCalledWith('/etc/ssl'));
			expect(pathTree).toHaveBeenCalledWith('r1', '/etc/ssl');
			expect(onclose).toHaveBeenCalledOnce();
			expect(onfile).not.toHaveBeenCalled();
		});

		it('says why a path refused as a file is not a folder either', async () => {
			pathFile.mockRejectedValue(new ApiError('not a regular file', 400, {}));
			const { ondir, onclose } = mount();
			await pause();
			pathTree.mockRejectedValue(new ApiError('not a directory', 400, {}));
			await fireEvent.input(input(), { target: { value: '/dev/null' } });
			await key('Enter');
			await shows('not a regular file');
			expect(ondir).not.toHaveBeenCalled();
			expect(onclose).not.toHaveBeenCalled();
		});

		it('takes a listed file refused as one at its word', async () => {
			pathFile.mockRejectedValue(new ApiError('not a regular file', 400, {}));
			mount();
			await type('/etc/hosts');
			pathTree.mockClear();
			await key('Enter');
			await shows('not a regular file');
			expect(pathTree).not.toHaveBeenCalled();
		});

		it('shows a refusal (403) and opens nothing', async () => {
			pathFile.mockRejectedValue(new ApiError('permission denied', 403, {}));
			const { onfile, ondir, onclose } = mount();
			await type('/etc/shadow');
			await key('Enter');
			await shows('permission denied');
			expect(onfile).not.toHaveBeenCalled();
			expect(ondir).not.toHaveBeenCalled();
			expect(onclose).not.toHaveBeenCalled();
			await type('/etc/shado');
			expect(screen.queryByText('permission denied')).not.toBeInTheDocument();
		});

		it('says when the workspace predates reading paths', async () => {
			pathFile.mockRejectedValue(new ApiError('Not Implemented', 501, {}));
			const { onfile } = mount();
			await type('/etc/hosts');
			await key('Enter');
			await shows(PREDATES);
			expect(onfile).not.toHaveBeenCalled();
		});

		it('reads once while a read is in flight', async () => {
			const held = deferred<WorkspaceFile>();
			pathFile.mockReturnValue(held.promise);
			const { onfile } = mount();
			await type('/etc/hosts');
			await key('Enter');
			const open = screen.getByRole('button', { name: 'Open' });
			expect(open).toBeDisabled();
			expect(open).toHaveAttribute('aria-busy', 'true');
			await key('Enter');
			expect(pathFile).toHaveBeenCalledOnce();
			held.resolve(aWorkspaceFile({ path: '/etc/hosts' }));
			await vi.waitFor(() => expect(onfile).toHaveBeenCalledWith('/etc/hosts'));
		});

		it('goes into a folder on a click, and opens a file on one', async () => {
			const { onfile, ondir } = mount();
			await type('/etc/');
			await fireEvent.click(option('ssl/'));
			expect(input()).toHaveValue('/etc/ssl/');
			expect(ondir).not.toHaveBeenCalled();
			await type('/etc/');
			await fireEvent.click(option('hosts'));
			await vi.waitFor(() => expect(onfile).toHaveBeenCalledWith('/etc/hosts'));
		});

		it('opens the typed path on Open', async () => {
			const { onfile } = mount();
			await type('/etc/hosts');
			await fireEvent.click(screen.getByRole('button', { name: 'Open' }));
			await vi.waitFor(() => expect(onfile).toHaveBeenCalledWith('/etc/hosts'));
		});

		it('closes on Cancel, opening nothing', async () => {
			const { onclose, onfile, ondir } = mount();
			await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
			expect(onclose).toHaveBeenCalledOnce();
			expect(onfile).not.toHaveBeenCalled();
			expect(ondir).not.toHaveBeenCalled();
		});

		it('opens nothing when a slow read lands after the modal closed', async () => {
			const held = deferred<WorkspaceFile>();
			pathFile.mockReturnValue(held.promise);
			const view = mount();
			await type('/mnt/slow/big.log');
			await key('Enter');
			await view.rerender({ open: false });
			held.resolve(aWorkspaceFile({ path: '/mnt/slow/big.log' }));
			await vi.advanceTimersByTimeAsync(0);
			expect(view.onfile).not.toHaveBeenCalled();
			expect(view.ondir).not.toHaveBeenCalled();
			expect(view.onclose).not.toHaveBeenCalled();
		});

		it('keeps a reopened modal free of a read started before it', async () => {
			const held = deferred<WorkspaceFile>();
			pathFile.mockReturnValueOnce(held.promise);
			const view = mount();
			await type('/mnt/slow/big.log');
			await key('Enter');
			await view.rerender({ open: false });
			await view.rerender({ open: true });
			await type('/etc/hosts');
			expect(screen.getByRole('button', { name: 'Open' })).not.toBeDisabled();

			held.reject(new ApiError('permission denied', 403, {}));
			await vi.advanceTimersByTimeAsync(0);
			expect(screen.queryByText('permission denied')).not.toBeInTheDocument();
			expect(screen.getByRole('button', { name: 'Open' })).not.toBeDisabled();

			await key('Enter');
			await vi.waitFor(() => expect(view.onfile).toHaveBeenCalledWith('/etc/hosts'));
			expect(view.onfile).toHaveBeenCalledOnce();
			expect(view.onclose).toHaveBeenCalledOnce();
		});
	});

	it('starts afresh at home on each opening', async () => {
		pathFile.mockRejectedValue(new ApiError('permission denied', 403, {}));
		const view = mount();
		await type('/etc/shadow');
		await key('Enter');
		await shows('permission denied');
		await view.rerender({ open: false });
		await view.rerender({ open: true });
		await pause();
		expect(input()).toHaveValue('~/');
		expect(screen.queryByText('permission denied')).not.toBeInTheDocument();
		expect(names()).toEqual(['.bashrc', 'src/']);
	});
});
