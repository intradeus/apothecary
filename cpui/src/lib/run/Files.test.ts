import { EditorView } from '@codemirror/view';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { ApiError } from '$lib/api/client';
import type { RunCheckout, WorkspaceEntry, WorkspaceFile } from '$lib/domain';
import { anEntry, aWorkspaceFile } from '$test/fixtures/review';
import { aLiveRun, aViewStore } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import Files from './Files.svelte';
import type { RunStore } from './store.svelte';

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
const pathFile = vi.mocked(api.workspace.pathFile);
const pathTree = vi.mocked(api.workspace.pathTree);
const app: RunCheckout = {
	name: 'app',
	dir: 'repo',
	url: 'https://git.test/app.git',
	branch: 'feat/x',
	source: 'main',
	primary: true
};
const lib: RunCheckout = { ...app, name: 'lib', dir: 'repos/lib', primary: false };
const dirs: Record<string, WorkspaceEntry[]> = {
	'': [anEntry({ path: 'README.md' }), anEntry({ path: 'main.go' })]
};
const TEXT = 'first line\nsecond line\nthird line';
const entry = (path: string) => screen.getByTitle(path);
const viewer = (path: string) => screen.getByRole('textbox', { name: `Contents of ${path}` });
const editor = (path: string) => EditorView.findFromDOM(viewer(path))!;

/** A store on a run of two checkouts, as the page makes it. */
const aStore = () => aViewStore(aLiveRun({ checkouts: [app, lib] }));

function show(rs: RunStore = aStore(), props: { visible?: boolean } = {}) {
	const onannotated = vi.fn();
	const view = render(Files, { rs, visible: true, onannotated, ...props });
	return { ...view, rs, onannotated };
}
async function mount(rs?: RunStore) {
	const view = show(rs);
	await screen.findByTitle('README.md');
	return view;
}
async function openReadme() {
	await userEvent.click(entry('README.md'));
	return viewer('README.md');
}
/** Select lines `from`–`to` (1-based) of the shown file. */
function select(path: string, from: number, to: number) {
	const v = editor(path);
	v.dispatch({ selection: { anchor: v.state.doc.line(from).from, head: v.state.doc.line(to).to } });
}

beforeAll(() => {
	// CodeMirror measures text; jsdom lays nothing out.
	Range.prototype.getClientRects = () => [] as unknown as DOMRectList;
	Range.prototype.getBoundingClientRect = () => new DOMRect();
});

beforeEach(() => {
	vi.resetAllMocks();
	localStorage.clear();
	tree.mockImplementation(async (_run, path = '') => dirs[path] ?? []);
	file.mockImplementation(async (_run, path) => aWorkspaceFile({ path, content: TEXT }));
	vi.mocked(api.workspace.stat).mockResolvedValue([]);
});

describe('Files', () => {
	it('asks to pick a file until one is', async () => {
		await mount();
		expect(screen.getByText('Pick a file.')).toBeInTheDocument();
		expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
	});

	it('says it is loading until the file arrives', async () => {
		await mount();
		file.mockReturnValue(deferred<WorkspaceFile>().promise);

		await userEvent.click(entry('README.md'));

		expect(screen.getByRole('status')).toHaveTextContent('Loading…');
		expect(screen.queryByText('Pick a file.')).not.toBeInTheDocument();
	});

	it('shows a file picked in the tree, in a tab, with its size', async () => {
		await mount();

		const shown = await openReadme();

		expect(shown).toHaveTextContent('first line');
		expect(screen.getByText('· 2 KB')).toBeInTheDocument();
		expect(file).toHaveBeenCalledExactlyOnceWith(expect.any(String), 'README.md', '');
		expect(screen.getByRole('tab', { name: /README\.md/ })).toHaveAttribute(
			'aria-selected',
			'true'
		);
	});

	it('switches, moves and closes tabs', async () => {
		const { rs } = await mount();
		await openReadme();
		await userEvent.click(entry('main.go'));
		expect(await screen.findByRole('textbox', { name: 'Contents of main.go' })).toBeVisible();

		await userEvent.click(screen.getByRole('tab', { name: /README\.md/ }));
		expect(viewer('README.md')).toBeInTheDocument();

		screen.getByRole('tab', { name: /README\.md/ }).focus();
		await userEvent.keyboard('{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}');
		expect(rs.files.tabs.map((t) => t.key)).toEqual([':main.go', ':README.md']);

		await userEvent.click(screen.getByRole('button', { name: /Close .*README\.md/ }));
		expect(rs.files.tabs.map((t) => t.key)).toEqual([':main.go']);
		expect(await screen.findByRole('textbox', { name: 'Contents of main.go' })).toBeVisible();
	});

	it.each([
		['still loading', () => deferred<WorkspaceFile>().promise],
		['binary', async () => aWorkspaceFile({ path: 'main.go', binary: true })]
	])('keeps a tab’s place and selection across a tab that is %s', async (_, read) => {
		await mount();
		await openReadme();
		const before = editor('README.md');
		select('README.md', 2, 3);
		await waitFor(() => screen.getByRole('group', { name: /README\.md:2-3/ }));
		before.scrollDOM.scrollTop = 120;
		file.mockImplementation(read);

		await userEvent.click(entry('main.go'));
		await waitFor(() =>
			expect(screen.getByRole('tab', { name: /main\.go/ })).toHaveAttribute('aria-selected', 'true')
		);
		expect(screen.queryByRole('group', { name: /README/ })).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole('tab', { name: /README\.md/ }));

		const after = editor('README.md');
		expect(after).toBe(before);
		expect(after.scrollDOM.scrollTop).toBe(120);
		const sel = after.state.selection.main;
		expect(after.state.sliceDoc(sel.from, sel.to)).toBe('second line\nthird line');
		expect(screen.getByRole('group', { name: /README\.md:2-3/ })).toBeInTheDocument();
	});

	it('lets a closed tab’s selection go with it', async () => {
		const { rs } = await mount();
		await openReadme();
		select('README.md', 2, 3);
		await waitFor(() => screen.getByRole('group', { name: /README\.md:2-3/ }));

		await userEvent.click(screen.getByRole('button', { name: /Close .*README\.md/ }));
		expect(rs.files.tabs).toEqual([]);
		await openReadme();

		expect(editor('README.md').state.selection.main.empty).toBe(true);
		expect(screen.queryByRole('group', { name: /README/ })).not.toBeInTheDocument();
	});

	it('keeps tabs from two repos across a remount', async () => {
		const first = await mount();
		await openReadme();
		first.rs.files.setRoot({ repo: 'lib' });
		await waitFor(() => expect(first.rs.files.dirs['']).toBeDefined());
		await tick();
		await userEvent.click(entry('main.go'));
		await screen.findByRole('textbox', { name: 'Contents of main.go' });
		first.unmount();

		const rs = aStore();
		show(rs);

		const tabs = await screen.findAllByRole('tab');
		expect(tabs.map((t) => t.textContent?.trim())).toEqual([
			expect.stringContaining('README.md'),
			expect.stringContaining('main.go')
		]);
		expect(tabs[1]).toHaveAttribute('aria-selected', 'true');
		expect(await screen.findByRole('textbox', { name: 'Contents of main.go' })).toBeVisible();
		expect(file).toHaveBeenLastCalledWith(rs.runId, 'main.go', 'lib');
	});

	it('says when only the head of a file is shown', async () => {
		file.mockResolvedValue(aWorkspaceFile({ truncated: true }));
		await mount();
		await userEvent.click(entry('README.md'));

		expect(await screen.findByText('· 2 KB · first 1 MB shown')).toBeInTheDocument();
		expect(screen.getByText('truncated · first 1 MB shown')).toBeInTheDocument();
	});

	it('shows no text for a binary file', async () => {
		file.mockResolvedValue(aWorkspaceFile({ binary: true, content: 'garbage' }));
		await mount();
		await userEvent.click(entry('README.md'));

		expect(await screen.findByText('Binary file · 2 KB')).toBeInTheDocument();
		expect(screen.queryByText('garbage')).not.toBeInTheDocument();
		expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
	});

	it('says the file could not be read, and reads it again on Retry', async () => {
		file.mockRejectedValueOnce(new Error('EIO'));
		await mount();
		await userEvent.click(entry('README.md'));

		expect(await screen.findByRole('alert')).toHaveTextContent('This file could not be read.');
		expect(screen.getByText('EIO')).toBeInTheDocument();

		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));

		expect(await screen.findByRole('textbox', { name: 'Contents of README.md' })).toBeVisible();
		expect(screen.queryByRole('alert')).not.toBeInTheDocument();
	});

	it('says a restored tab’s file is gone, and closes the tab on request', async () => {
		file.mockRejectedValue(new ApiError('not found', 404, null));
		const rs = aStore();
		rs.files.open({ repo: '', path: 'old.go' });
		await mount(rs);

		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('This file is no longer in the workspace.');

		await userEvent.click(within(alert).getByRole('button', { name: 'Close tab' }));

		expect(rs.files.tabs).toEqual([]);
		expect(screen.getByText('Pick a file.')).toBeInTheDocument();
	});

	it('keeps a deleted file’s last content, and says so', async () => {
		const { rs } = await mount();
		await openReadme();

		rs.files.active!.deleted = true;

		expect(
			await screen.findByText('Deleted from the workspace: this is its last content.')
		).toBeInTheDocument();
		expect(viewer('README.md')).toHaveTextContent('first line');
	});

	it('reveals the line a tab was opened at', async () => {
		const { rs } = await mount();
		rs.files.open({ repo: '', path: 'README.md' }, { from: 2, to: 2 });

		await waitFor(() => expect(rs.files.active?.line).toBeNull());
		const v = editor('README.md');
		const sel = v.state.selection.main;
		expect(v.state.sliceDoc(sel.from, sel.to)).toBe('second line');
	});

	it('reveals a linked line in the file as edited since its tab was read, while the pane is hidden', async () => {
		const { rs } = show(aStore(), { visible: false });
		await screen.findByTitle('README.md');
		await openReadme();
		await userEvent.click(entry('main.go'));
		await screen.findByRole('textbox', { name: 'Contents of main.go' });
		// The agent adds two lines on top of README.md; no poll runs while hidden.
		const fresh = deferred<WorkspaceFile>();
		file.mockReturnValueOnce(fresh.promise);

		rs.files.open({ repo: '', path: 'README.md' }, { from: 4, to: 4 });
		await tick();

		// The copy on show is not the one the line is of: nothing is picked in it.
		const before = editor('README.md');
		expect(before.state.doc.toString()).toBe(TEXT);
		expect(before.state.selection.main.empty).toBe(true);
		fresh.resolve(aWorkspaceFile({ path: 'README.md', content: `new\nnewer\n${TEXT}` }));

		await waitFor(() => expect(rs.files.active?.line).toBeNull());
		const v = editor('README.md');
		const sel = v.state.selection.main;
		expect(v.state.sliceDoc(sel.from, sel.to)).toBe('second line');
	});

	it('names an absolute file by its path', async () => {
		pathFile.mockResolvedValue(aWorkspaceFile({ path: '/etc/hosts', content: '127.0.0.1' }));
		const { rs } = await mount();
		rs.files.open({ abs: '/etc/hosts' });
		expect(await screen.findByRole('textbox', { name: 'Contents of /etc/hosts' })).toBeVisible();
	});

	describe('live refresh', () => {
		it('runs while the pane is visible', async () => {
			const { rs, rerender, unmount } = show(aStore(), { visible: false });
			expect(rs.files.visible).toBe(false);
			await rerender({ visible: true });
			expect(rs.files.visible).toBe(true);
			unmount();
			expect(rs.files.visible).toBe(false);
		});
	});

	describe('go to path', () => {
		const go = async (path: string) => {
			await userEvent.click(screen.getByRole('button', { name: 'Go to path…' }));
			const input = await screen.findByRole('combobox', { name: 'Path' });
			await userEvent.clear(input);
			await userEvent.type(input, `${path}{Enter}`);
		};

		it('opens a file in a tab', async () => {
			pathTree.mockResolvedValue([]);
			pathFile.mockResolvedValue(aWorkspaceFile({ path: '/etc/hosts' }));
			const { rs } = await mount();

			await go('/etc/hosts');

			await waitFor(() => expect(rs.files.activeKey).toBe('/etc/hosts'));
			await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
		});

		it('makes a folder the tree’s root', async () => {
			pathTree.mockResolvedValue([anEntry({ path: '/etc/hosts' })]);
			const { rs } = await mount();

			await go('/etc/');

			await waitFor(() => expect(rs.files.root).toEqual({ abs: '/etc' }));
			expect(await screen.findByTitle('/etc/hosts')).toBeInTheDocument();
		});
	});

	describe('annotating', () => {
		const box = () => screen.getByRole('group', { name: /README\.md:2-3/ });

		it('offers no box without a selection', async () => {
			await mount();
			await openReadme();
			expect(screen.queryByRole('group', { name: /README/ })).not.toBeInTheDocument();
		});

		it('is off, with its reason, when no chat takes annotations', async () => {
			await mount();
			await openReadme();

			select('README.md', 2, 3);

			const send = await within(await waitFor(box)).findByRole('button', {
				name: 'Send to Session'
			});
			expect(send).toBeDisabled();
			expect(
				within(box()).getByText('Take over the agent or open Ask to send this to a chat')
			).toBeInTheDocument();
		});

		it('sends the selected lines, then lets the selection go and reveals the chat', async () => {
			const rs = aStore();
			const add = vi.fn();
			rs.composers.register({ kind: 'ask', add });
			const { onannotated } = await mount(rs);
			await openReadme();
			select('README.md', 2, 3);
			await waitFor(box);

			await userEvent.type(within(box()).getByRole('textbox'), 'why?');
			await userEvent.click(within(box()).getByRole('button', { name: 'Send to Ask' }));

			expect(add).toHaveBeenCalledOnce();
			expect(JSON.stringify(add.mock.calls[0][0])).toContain('second line\\nthird line');
			expect(onannotated).toHaveBeenCalledExactlyOnceWith('ask');
			expect(screen.queryByRole('group', { name: /README/ })).not.toBeInTheDocument();
			const sel = editor('README.md').state.selection.main;
			expect(sel.empty).toBe(true);
		});

		it('annotates a blank line picked from the gutter, inside the file and at its end', async () => {
			const rs = aStore();
			const add = vi.fn();
			rs.composers.register({ kind: 'ask', add });
			file.mockImplementation(async (_run, path) =>
				aWorkspaceFile({ path, content: 'first line\n\nthird line\n' })
			);
			await mount(rs);
			const shown = await openReadme();
			const v = editor('README.md');
			// jsdom has no layout: a gutter click lands on whichever line the test names.
			const gutterClick = (n: number) => {
				vi.spyOn(v, 'lineBlockAtHeight').mockReturnValue(v.lineBlockAt(v.state.doc.line(n).from));
				const number = shown
					.closest('.cm-editor')!
					.querySelector('.cm-lineNumbers .cm-gutterElement:last-child')!;
				fireEvent.mouseDown(number, { button: 0 });
			};

			gutterClick(2);
			expect(await screen.findByRole('group', { name: /README\.md:2$/ })).toBeInTheDocument();

			gutterClick(4);
			const last = await screen.findByRole('group', { name: /README\.md:4$/ });
			await userEvent.click(within(last).getByRole('button', { name: 'Send to Ask' }));
			expect(add).toHaveBeenCalledOnce();
			expect(JSON.stringify(add.mock.calls[0][0])).toContain('README.md:4');
		});

		it('lets the selection go on cancel', async () => {
			await mount();
			await openReadme();
			select('README.md', 2, 3);
			await waitFor(box);

			await userEvent.click(within(box()).getByRole('button', { name: 'Cancel' }));

			expect(screen.queryByRole('group', { name: /README/ })).not.toBeInTheDocument();
		});

		it('drops the box when another tab is shown', async () => {
			await mount();
			await openReadme();
			select('README.md', 2, 3);
			await waitFor(box);

			await userEvent.click(entry('main.go'));

			await screen.findByRole('textbox', { name: 'Contents of main.go' });
			expect(screen.queryByRole('group', { name: /README/ })).not.toBeInTheDocument();
		});
	});
});
