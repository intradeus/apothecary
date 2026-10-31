import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { WorkspaceEntry, WorkspaceFile } from '$lib/domain';
import { anEntry, aWorkspaceFile } from '$test/fixtures/review';
import { deferred } from '$test/helpers';
import Files from './Files.svelte';

vi.mock('$lib/api', () => ({ api: { workspace: { tree: vi.fn(), file: vi.fn() } } }));

const tree = vi.mocked(api.workspace.tree);
const file = vi.mocked(api.workspace.file);
const src = anEntry({ path: 'src', dir: true });
const readme = anEntry({ path: 'README.md' });
const dist = anEntry({ path: 'dist', dir: true, ignored: true });
const main = anEntry({ path: 'src/main.ts' });
const util = anEntry({ path: 'src/util', dir: true });
const deep = anEntry({ path: 'src/util/deep.ts' });
const dirs: Record<string, WorkspaceEntry[]> = {
	'': [src, readme, dist],
	src: [util, main],
	'src/util': [deep]
};
const entry = (path: string) => screen.getByTitle(path);
const refresh = () => screen.getByRole('button', { name: 'Refresh' });

async function mount(props = {}) {
	tree.mockImplementation(async (_run, path = '') => dirs[path] ?? []);
	file.mockResolvedValue(aWorkspaceFile());
	const view = render(Files, { runId: 'r1', ...props });
	await screen.findByTitle('README.md');
	return view;
}

describe('Files', () => {
	beforeEach(() => vi.resetAllMocks());

	describe('the tree', () => {
		it('says it is loading until the checkout is listed', () => {
			tree.mockReturnValue(deferred<WorkspaceEntry[]>().promise);
			render(Files, { runId: 'r1' });
			expect(screen.getByRole('status')).toHaveTextContent('Loading…');
		});

		it('says files are unavailable, with the cause folded', async () => {
			tree.mockRejectedValue(new Error('no workspace for this run'));
			render(Files, { runId: 'r1' });
			expect(await screen.findByRole('alert')).toHaveTextContent('Files are unavailable.');
			expect(screen.getByText('no workspace for this run')).toBeInTheDocument();
		});

		it('tries again on Retry', async () => {
			tree.mockRejectedValueOnce(new Error('no workspace'));
			tree.mockResolvedValue([readme]);
			render(Files, { runId: 'r1' });

			await userEvent.click(await screen.findByRole('button', { name: /Retry/ }));

			expect(await screen.findByTitle('README.md')).toBeInTheDocument();
			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});

		it('stops loading on an empty checkout', async () => {
			tree.mockResolvedValue([]);
			render(Files, { runId: 'r1' });
			await waitFor(() => expect(screen.queryByRole('status')).not.toBeInTheDocument());
			expect(screen.getByText('Pick a file.')).toBeInTheDocument();
		});

		it('asks for the run and repo it was given', async () => {
			await mount({ runId: 'r9', repo: 'api' });
			expect(tree).toHaveBeenCalledExactlyOnceWith('r9', '', 'api');
		});

		it('marks folders and leaves files plain', async () => {
			await mount();
			expect(entry('src')).toHaveTextContent('src/');
			expect(entry('src')).toHaveAttribute('aria-expanded', 'false');
			expect(entry('README.md')).toHaveTextContent(/README\.md$/);
			expect(entry('README.md')).not.toHaveAttribute('aria-expanded');
		});

		it('hides what git ignores until asked', async () => {
			await mount();
			expect(screen.queryByTitle('dist')).not.toBeInTheDocument();

			await userEvent.click(screen.getByRole('checkbox', { name: 'Show ignored' }));
			expect(entry('dist')).toBeInTheDocument();

			await userEvent.click(screen.getByRole('checkbox', { name: 'Show ignored' }));
			expect(screen.queryByTitle('dist')).not.toBeInTheDocument();
		});

		it('opens a folder in place, listing it once', async () => {
			await mount();

			await userEvent.click(entry('src'));

			expect(await screen.findByTitle('src/main.ts')).toBeInTheDocument();
			expect(entry('src')).toHaveAttribute('aria-expanded', 'true');
			expect(tree).toHaveBeenLastCalledWith('r1', 'src', '');
			const shown = screen.getAllByRole('button').map((b) => b.title);
			expect(shown.indexOf('src/main.ts')).toBe(shown.indexOf('src') + 2);
			expect(shown.indexOf('src/main.ts')).toBeLessThan(shown.indexOf('README.md'));

			await userEvent.click(entry('src'));
			expect(screen.queryByTitle('src/main.ts')).not.toBeInTheDocument();

			await userEvent.click(entry('src'));
			expect(entry('src/main.ts')).toBeInTheDocument();
			expect(tree.mock.calls.filter(([, path]) => path === 'src')).toHaveLength(1);
		});

		it('indents by depth', async () => {
			await mount();
			await userEvent.click(entry('src'));
			await userEvent.click(await screen.findByTitle('src/util'));
			expect(entry('src')).toHaveStyle({ paddingLeft: '8px' });
			expect(entry('src/util')).toHaveStyle({ paddingLeft: '20px' });
			expect(await screen.findByTitle('src/util/deep.ts')).toHaveStyle({ paddingLeft: '32px' });
		});

		it('says so when a folder cannot be listed', async () => {
			await mount();
			tree.mockRejectedValue(new Error('permission denied'));

			await userEvent.click(entry('src'));

			expect(await screen.findByRole('alert')).toBeInTheDocument();
		});

		it('names the folder, and lists it on Retry', async () => {
			await mount();
			tree.mockRejectedValueOnce(new Error('permission denied'));
			await userEvent.click(entry('src'));
			const card = await screen.findByRole('alert');
			expect(card).toHaveTextContent('Could not list src.');

			await userEvent.click(within(card).getByRole('button', { name: /Retry/ }));

			expect(await screen.findByTitle('src/main.ts')).toBeInTheDocument();
			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});

		it('keeps both folders when two opened at once land out of order', async () => {
			await mount();
			const slow = deferred<WorkspaceEntry[]>();
			const other = anEntry({ path: 'dist/out.js' });
			tree.mockImplementationOnce(() => slow.promise);
			tree.mockImplementationOnce(async () => [other]);
			await userEvent.click(screen.getByRole('checkbox'));

			await userEvent.click(entry('src'));
			await userEvent.click(entry('dist'));
			await screen.findByTitle('dist/out.js');
			slow.resolve([main]);

			expect(await screen.findByTitle('src/main.ts')).toBeInTheDocument();
			expect(screen.getByTitle('dist/out.js')).toBeInTheDocument();
		});
	});

	describe('the file', () => {
		it('asks to pick one until one is', async () => {
			await mount();
			expect(screen.getByText('Pick a file.')).toBeInTheDocument();
		});

		it('says it is loading until the file arrives', async () => {
			await mount();
			file.mockReturnValue(deferred<WorkspaceFile>().promise);

			await userEvent.click(entry('README.md'));

			expect(screen.getByRole('status')).toHaveTextContent('Loading…');
			expect(screen.queryByText('Pick a file.')).not.toBeInTheDocument();
		});

		it('shows the file with its size', async () => {
			await mount({ repo: 'api' });

			await userEvent.click(entry('README.md'));

			expect(await screen.findByText('first line')).toBeInTheDocument();
			expect(screen.getByText('second line')).toBeInTheDocument();
			expect(screen.getByText('· 2 KB')).toBeInTheDocument();
			expect(file).toHaveBeenCalledExactlyOnceWith('r1', 'README.md', 'api');
			expect(entry('README.md')).toHaveClass('on');
		});

		it('says when only the head of a file is shown', async () => {
			await mount();
			file.mockResolvedValue(aWorkspaceFile({ truncated: true }));

			await userEvent.click(entry('README.md'));

			expect(await screen.findByText('· 2 KB · first 1 MB shown')).toBeInTheDocument();
			expect(screen.getByText('truncated · first 1 MB shown')).toBeInTheDocument();
		});

		it('shows no text for a binary file', async () => {
			await mount();
			file.mockResolvedValue(aWorkspaceFile({ binary: true, content: 'garbage' }));

			await userEvent.click(entry('README.md'));

			expect(await screen.findByText('Binary file · 2 KB')).toBeInTheDocument();
			expect(screen.queryByText('garbage')).not.toBeInTheDocument();
		});

		it('says the file could not be read, and reads it again on Retry', async () => {
			await mount();
			file.mockRejectedValueOnce(new Error('EIO'));

			await userEvent.click(entry('README.md'));

			expect(await screen.findByRole('alert')).toHaveTextContent('This file could not be read.');
			expect(screen.getByText('EIO')).toBeInTheDocument();

			await userEvent.click(screen.getByRole('button', { name: /Retry/ }));

			expect(await screen.findByText('first line')).toBeInTheDocument();
			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});

		it('drops the last error when another file is picked', async () => {
			await mount();
			await userEvent.click(entry('src'));
			file.mockRejectedValueOnce(new Error('EIO'));
			await userEvent.click(entry('README.md'));
			await screen.findByRole('alert');

			await userEvent.click(await screen.findByTitle('src/main.ts'));

			await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument());
		});

		it('shows the last file picked when an earlier read lands after it', async () => {
			await mount();
			await userEvent.click(entry('src'));
			const slow = deferred<WorkspaceFile>();
			file.mockReturnValueOnce(slow.promise);
			file.mockResolvedValueOnce(aWorkspaceFile({ content: 'from main' }));

			await userEvent.click(entry('README.md'));
			await userEvent.click(await screen.findByTitle('src/main.ts'));
			await screen.findByText('from main');
			slow.resolve(aWorkspaceFile({ content: 'from readme' }));
			await slow.promise;
			await tick();

			expect(screen.getByText('from main')).toBeInTheDocument();
		});

		it('ignores an earlier read that fails after another file is picked', async () => {
			await mount();
			await userEvent.click(entry('src'));
			const slow = deferred<WorkspaceFile>();
			file.mockReturnValueOnce(slow.promise);
			file.mockResolvedValueOnce(aWorkspaceFile({ content: 'from main' }));

			await userEvent.click(entry('README.md'));
			await userEvent.click(await screen.findByTitle('src/main.ts'));
			await screen.findByText('from main');
			slow.reject(new Error('too late'));
			await expect(slow.promise).rejects.toThrow();
			await tick();

			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});
	});

	describe('refreshing', () => {
		it('lists every folder it has read again', async () => {
			await mount();
			await userEvent.click(entry('src'));
			await screen.findByTitle('src/main.ts');
			tree.mockClear();

			await userEvent.click(refresh());

			await waitFor(() => expect(tree).toHaveBeenCalledTimes(2));
			expect(tree.mock.calls.map(([, path]) => path)).toEqual(['', 'src']);
		});

		it('shows what changed', async () => {
			await mount();
			tree.mockResolvedValue([readme, anEntry({ path: 'NEW.md' })]);

			await userEvent.click(refresh());

			expect(await screen.findByTitle('NEW.md')).toBeInTheDocument();
		});

		it('reads the open file again', async () => {
			await mount();
			await userEvent.click(entry('README.md'));
			await screen.findByText('first line');
			file.mockResolvedValue(aWorkspaceFile({ content: 'rewritten' }));

			await userEvent.click(refresh());

			expect(await screen.findByText('rewritten')).toBeInTheDocument();
			expect(file).toHaveBeenCalledTimes(2);
		});

		it('reads no file when none is open', async () => {
			await mount();
			await userEvent.click(refresh());
			await waitFor(() => expect(tree).toHaveBeenCalledTimes(2));
			expect(file).not.toHaveBeenCalled();
		});

		it('keeps the tree when a refresh fails', async () => {
			await mount();
			tree.mockRejectedValue(new Error('blip'));

			await userEvent.click(refresh());

			await waitFor(() => expect(tree).toHaveBeenCalledTimes(2));
			expect(entry('README.md')).toBeInTheDocument();
			expect(screen.queryByText('Files are unavailable.')).not.toBeInTheDocument();
		});
	});
});
