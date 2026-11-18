import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { WorkspaceEntry } from '$lib/domain';
import { anEntry, aWorkspaceFile } from '$test/fixtures/review';
import { deferred } from '$test/helpers';
import FileTree from './FileTree.svelte';
import { OpenFiles } from './openFiles.svelte';

vi.mock('$lib/api', () => ({
	api: {
		workspace: {
			tree: vi.fn(),
			file: vi.fn(),
			pathTree: vi.fn(),
			pathFile: vi.fn(),
			stat: vi.fn()
		}
	}
}));

const tree = vi.mocked(api.workspace.tree);
const file = vi.mocked(api.workspace.file);
const pathTree = vi.mocked(api.workspace.pathTree);
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

/** The tree on a fresh store, shown as the Files pane shows it. */
function show(files = new OpenFiles('r1')) {
	files.show();
	render(FileTree, { files });
	return files;
}
async function mount(files?: OpenFiles) {
	tree.mockImplementation(async (_run, path = '') => dirs[path] ?? []);
	const f = show(files);
	await screen.findByTitle('README.md');
	return f;
}

beforeEach(() => {
	vi.resetAllMocks();
	localStorage.clear();
	file.mockResolvedValue(aWorkspaceFile());
	vi.mocked(api.workspace.stat).mockImplementation(async (_run, locs) =>
		locs.map(() => ({ exists: true, dir: true, size: 0, mtime: '1' }))
	);
});

describe('FileTree', () => {
	it('says it is loading until the checkout is listed', () => {
		tree.mockReturnValue(deferred<WorkspaceEntry[]>().promise);
		show();
		expect(screen.getByRole('status')).toHaveTextContent('Loading…');
	});

	it('says files are unavailable, with the cause folded', async () => {
		tree.mockRejectedValue(new Error('no workspace for this run'));
		show();
		expect(await screen.findByRole('alert')).toHaveTextContent('Files are unavailable.');
		expect(screen.getByText('no workspace for this run')).toBeInTheDocument();
	});

	it('tries again on Retry', async () => {
		tree.mockRejectedValueOnce(new Error('no workspace'));
		tree.mockResolvedValue([readme]);
		show();

		await userEvent.click(await screen.findByRole('button', { name: /Retry/ }));

		expect(await screen.findByTitle('README.md')).toBeInTheDocument();
		expect(screen.queryByRole('alert')).not.toBeInTheDocument();
	});

	it('stops loading on an empty checkout', async () => {
		tree.mockResolvedValue([]);
		show();
		await waitFor(() => expect(screen.queryByRole('status')).not.toBeInTheDocument());
	});

	it('lists the checkout of its root', async () => {
		const files = new OpenFiles('r9');
		files.setRoot({ repo: 'api' });
		await mount(files);
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

	it('names the folder it could not list, and lists it on Retry', async () => {
		await mount();
		tree.mockRejectedValueOnce(new Error('permission denied'));
		await userEvent.click(entry('src'));
		const card = await screen.findByRole('alert');
		expect(card).toHaveTextContent('Could not list src.');

		await userEvent.click(within(card).getByRole('button', { name: /Retry/ }));

		expect(await screen.findByTitle('src/main.ts')).toBeInTheDocument();
		expect(screen.queryByRole('alert')).not.toBeInTheDocument();
	});

	it('lets a folder error be dismissed', async () => {
		await mount();
		tree.mockRejectedValueOnce(new Error('permission denied'));
		await userEvent.click(entry('src'));
		const card = await screen.findByRole('alert');

		await userEvent.click(within(card).getByRole('button', { name: /dismiss/i }));

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

	it('opens a file in a tab and marks it', async () => {
		const files = await mount();

		await userEvent.click(entry('README.md'));

		expect(files.active?.loc).toEqual({ repo: '', path: 'README.md' });
		expect(entry('README.md')).toHaveClass('on');
		expect(entry('src')).not.toHaveClass('on');
	});

	it('marks no entry for a tab of another root', async () => {
		const files = new OpenFiles('r1');
		files.open({ repo: 'api', path: 'README.md' });
		await mount(files);
		expect(entry('README.md')).not.toHaveClass('on');

		files.open({ abs: '/README.md' });
		await waitFor(() => expect(files.activeKey).toBe('/README.md'));
		expect(entry('README.md')).not.toHaveClass('on');
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

		it('keeps the tree when a refresh fails', async () => {
			await mount();
			tree.mockRejectedValue(new Error('blip'));

			await userEvent.click(refresh());

			expect(await screen.findByText('Could not list ..')).toBeInTheDocument();
			expect(entry('README.md')).toBeInTheDocument();
			expect(screen.queryByText('Files are unavailable.')).not.toBeInTheDocument();
		});
	});

	describe('an absolute root', () => {
		const hosts = anEntry({ path: '/etc/hosts' });
		const ssl = anEntry({ path: '/etc/ssl', dir: true });
		const cert = anEntry({ path: '/etc/ssl/cert.pem' });

		async function absolute() {
			pathTree.mockImplementation(async (_run, path) => (path === '/etc' ? [ssl, hosts] : [cert]));
			const files = new OpenFiles('r1');
			files.setRoot({ repo: 'api' });
			files.setRoot({ abs: '/etc' });
			show(files);
			await screen.findByTitle('/etc/hosts');
			return files;
		}

		it('lists through the path route, labelled with its path', async () => {
			await absolute();
			expect(pathTree).toHaveBeenCalledWith('r1', '/etc');
			expect(screen.getByTitle('/etc')).toHaveTextContent('/etc');
			expect(entry('/etc/hosts')).toHaveTextContent(/^hosts$/);
		});

		it('opens its folders and files by absolute path', async () => {
			const files = await absolute();

			await userEvent.click(entry('/etc/ssl'));
			await userEvent.click(await screen.findByTitle('/etc/ssl/cert.pem'));

			expect(pathTree).toHaveBeenCalledWith('r1', '/etc/ssl');
			expect(files.active?.loc).toEqual({ abs: '/etc/ssl/cert.pem' });
			expect(entry('/etc/ssl/cert.pem')).toHaveClass('on');
		});

		it('goes back to the checkout', async () => {
			const files = await absolute();
			tree.mockResolvedValue([readme]);

			await userEvent.click(screen.getByRole('button', { name: 'Back to the checkout' }));

			expect(files.root).toEqual({ repo: 'api' });
			expect(await screen.findByTitle('README.md')).toBeInTheDocument();
			expect(
				screen.queryByRole('button', { name: 'Back to the checkout' })
			).not.toBeInTheDocument();
		});

		it('goes back to the checkout from a root it could not list', async () => {
			pathTree.mockRejectedValue(new Error('permission denied'));
			tree.mockResolvedValue([readme]);
			const files = new OpenFiles('r1');
			files.setRoot({ repo: 'api' });
			files.setRoot({ abs: '/root' });
			show(files);
			expect(await screen.findByRole('alert')).toHaveTextContent('Files are unavailable.');
			expect(screen.getByTitle('/root')).toHaveTextContent('/root');

			await userEvent.click(screen.getByRole('button', { name: 'Back to the checkout' }));

			expect(files.root).toEqual({ repo: 'api' });
			expect(await screen.findByTitle('README.md')).toBeInTheDocument();
			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});
	});
});
