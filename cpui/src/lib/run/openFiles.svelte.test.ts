import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { ApiError } from '$lib/api/client';
import type { FileLoc, PathStat, WorkspaceFile } from '$lib/domain';
import { anEntry, aWorkspaceFile } from '$test/fixtures/review';
import { settled } from '$test/fixtures/run-components';
import { deferred } from '$test/helpers';
import {
	OpenFiles,
	KEPT_RUNS,
	MAX_BACKOFF_MS,
	POLL_MS,
	STAT_CAP,
	STORAGE_KEY,
	locKey,
	rootKey
} from './openFiles.svelte';

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
const pathFile = vi.mocked(api.workspace.pathFile);
const stat = vi.mocked(api.workspace.stat);

const RUN = 'r1';
const aFile = (over: Partial<WorkspaceFile> = {}) => aWorkspaceFile({ mtime: '1', ...over });
const aStat = (over: Partial<PathStat> = {}): PathStat => ({
	exists: true,
	dir: false,
	size: 2048,
	mtime: '1',
	...over
});
const at = (path: string, repo = ''): FileLoc => ({ repo, path });
const refused = (status: number) => new ApiError(`refused ${status}`, status, null);
const saved = () => JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '{}');
const keys = (f: OpenFiles) => f.tabs.map((t) => t.key);

/** Every stat answers "there, unchanged" unless a test says otherwise. */
function statsAre(answer: (loc: FileLoc) => PathStat = () => aStat({ dir: true })) {
	stat.mockImplementation(async (_run, locs) => locs.map(answer));
}

beforeEach(() => {
	vi.resetAllMocks();
	localStorage.clear();
	tree.mockResolvedValue([]);
	pathTree.mockResolvedValue([]);
	file.mockImplementation(async (_run, path) => aFile({ path }));
	pathFile.mockImplementation(async (_run, path) => aFile({ path }));
	statsAre();
});

describe('locKey and rootKey', () => {
	it('tell checkouts and absolute paths apart', () => {
		expect(locKey(at('a.go'))).toBe(':a.go');
		expect(locKey(at('a.go', 'api'))).toBe('api:a.go');
		expect(locKey({ abs: '/etc/hosts' })).toBe('/etc/hosts');
		expect(rootKey({ repo: 'api' })).toBe('api:');
		expect(rootKey({ abs: '/etc' })).toBe('/etc');
	});
});

describe('tabs', () => {
	it('opens a tab at the end, shows it and reads it from its checkout', async () => {
		const f = new OpenFiles(RUN);
		f.open(at('a.go'));
		f.open(at('b.go', 'api'));

		expect(keys(f)).toEqual([':a.go', 'api:b.go']);
		expect(f.activeKey).toBe('api:b.go');
		expect(f.active?.loading).toBe(true);
		await settled();
		expect(file).toHaveBeenCalledWith(RUN, 'b.go', 'api');
		expect(f.active?.file?.path).toBe('b.go');
		expect(f.active?.stat).toEqual({ size: 2048, mtime: '1' });
		expect(f.active?.loading).toBe(false);
	});

	it('reads an absolute file through the path route', async () => {
		const f = new OpenFiles(RUN);
		f.open({ abs: '/etc/hosts' });
		await settled();
		expect(pathFile).toHaveBeenCalledExactlyOnceWith(RUN, '/etc/hosts');
		expect(f.active?.file?.path).toBe('/etc/hosts');
	});

	it('focuses an open tab instead of opening another, with the new line', async () => {
		const f = new OpenFiles(RUN);
		f.open(at('a.go'));
		f.open(at('b.go'));
		await settled();

		f.open(at('a.go'), { from: 3, to: 5 });

		expect(keys(f)).toEqual([':a.go', ':b.go']);
		expect(f.activeKey).toBe(':a.go');
		await settled();
		expect(f.active?.line).toEqual({ from: 3, to: 5 });
		expect(file).toHaveBeenCalledTimes(3);
	});

	it('reveals a linked line only in the content read after the link, not the copy on show', async () => {
		const f = new OpenFiles(RUN);
		f.open(at('a.go'));
		f.open(at('b.go'));
		await settled();
		// a.go, hidden and not stale (no poll saw it), gains lines before the target.
		const fresh = deferred<WorkspaceFile>();
		file.mockReturnValueOnce(fresh.promise);

		f.open(at('a.go'), { from: 9, to: 9 });

		expect(f.active?.loading).toBe(true);
		expect(f.active?.line).toBeNull();
		fresh.resolve(aFile({ path: 'a.go', content: 'new\n'.repeat(9), mtime: '2' }));
		await settled();
		expect(f.active?.file?.mtime).toBe('2');
		expect(f.active?.line).toEqual({ from: 9, to: 9 });
	});

	it('keeps a linked line through a newer read, and drops it with its tab', async () => {
		const f = new OpenFiles(RUN);
		const first = deferred<WorkspaceFile>();
		file.mockReturnValueOnce(first.promise);
		f.open(at('a.go'), { from: 2, to: 2 });

		await f.refresh();
		first.resolve(aFile({ path: 'a.go' }));
		await settled();
		expect(f.active?.line).toEqual({ from: 2, to: 2 });

		const out = deferred<WorkspaceFile>();
		file.mockReturnValueOnce(out.promise);
		f.open(at('a.go'), { from: 4, to: 4 });
		f.close(':a.go');
		f.open(at('a.go'));
		out.resolve(aFile({ path: 'a.go' }));
		await settled();
		expect(f.active?.line).toBeNull();
	});

	it('reveals a linked line on a deleted file’s last content, and on a failed one once read', async () => {
		const f = new OpenFiles(RUN);
		f.open(at('a.go'));
		await settled();
		file.mockRejectedValueOnce(refused(404));
		f.open(at('a.go'), { from: 2, to: 2 });
		await settled();
		expect(f.active?.deleted).toBe(true);
		expect(f.active?.line).toEqual({ from: 2, to: 2 });

		file.mockRejectedValueOnce(refused(500));
		f.open(at('b.go'), { from: 3, to: 3 });
		await settled();
		expect(f.active?.error).not.toBe('');
		expect(f.active?.line).toBeNull();
		f.reload(':b.go');
		await settled();
		expect(f.active?.file?.path).toBe('b.go');
		expect(f.active?.line).toEqual({ from: 3, to: 3 });
	});

	it('keeps a linked line pending through a failed read of a shown tab, for the content a Retry reads', async () => {
		const f = new OpenFiles(RUN);
		f.open(at('a.go'));
		await settled();
		file.mockRejectedValueOnce(refused(500));
		f.open(at('a.go'), { from: 9, to: 9 });
		await settled();
		expect(f.active?.error).not.toBe('');
		expect(f.active?.line).toBeNull();

		const fresh = deferred<WorkspaceFile>();
		file.mockReturnValueOnce(fresh.promise);
		f.reload(':a.go');
		expect(f.active?.line).toBeNull();
		fresh.resolve(aFile({ path: 'a.go', content: 'new\n'.repeat(9), mtime: '2' }));
		await settled();
		expect(f.active?.file?.mtime).toBe('2');
		expect(f.active?.line).toEqual({ from: 9, to: 9 });
	});

	it('keeps the line until it was revealed', async () => {
		const f = new OpenFiles(RUN);
		f.open(at('a.go'), { from: 7, to: 7 });
		f.open(at('a.go'));
		await settled();
		expect(f.active?.line).toEqual({ from: 7, to: 7 });

		f.revealed(':a.go');
		f.revealed(':gone.go');

		expect(f.active?.line).toBeNull();
	});

	it('shows the next tab when the active one closes, else the one before', async () => {
		const f = new OpenFiles(RUN);
		['a', 'b', 'c'].forEach((p) => f.open(at(p)));
		f.activate(':b');

		f.close(':b');
		expect(keys(f)).toEqual([':a', ':c']);
		expect(f.activeKey).toBe(':c');

		f.close(':c');
		expect(f.activeKey).toBe(':a');

		f.close(':a');
		expect(f.tabs).toEqual([]);
		expect(f.activeKey).toBeNull();
	});

	it('keeps the active tab when another closes, and ignores unknown keys', () => {
		const f = new OpenFiles(RUN);
		['a', 'b'].forEach((p) => f.open(at(p)));
		f.close(':a');
		f.close(':nope');
		f.activate(':nope');
		expect(keys(f)).toEqual([':b']);
		expect(f.activeKey).toBe(':b');
	});

	it('moves a tab, and ignores a move nowhere or out of range', () => {
		const f = new OpenFiles(RUN);
		['a', 'b', 'c'].forEach((p) => f.open(at(p)));

		f.move(0, 2);
		expect(keys(f)).toEqual([':b', ':c', ':a']);
		f.move(2, 0);
		expect(keys(f)).toEqual([':a', ':b', ':c']);

		for (const [from, to] of [
			[1, 1],
			[-1, 0],
			[0, -1],
			[3, 0],
			[0, 3]
		])
			f.move(from, to);
		expect(keys(f)).toEqual([':a', ':b', ':c']);
	});

	it('says why a read failed, and reads again on reload', async () => {
		file.mockRejectedValueOnce(new Error('EIO'));
		const f = new OpenFiles(RUN);
		f.open(at('a.go'));
		await settled();
		expect(f.active).toMatchObject({ error: 'EIO', file: null, loading: false });

		f.reload(':a.go');
		f.reload(':nope');
		expect(f.active?.error).toBe('');
		await settled();
		expect(f.active?.file).not.toBeNull();
	});

	it('marks a file missing at first read as gone', async () => {
		file.mockRejectedValueOnce(refused(404));
		const f = new OpenFiles(RUN);
		f.open(at('a.go'));
		await settled();
		expect(f.active).toMatchObject({ gone: true, deleted: false, error: '' });
	});

	it('keeps the content of a file that went missing since, marked deleted', async () => {
		const f = new OpenFiles(RUN);
		f.open(at('a.go'));
		await settled();
		file.mockRejectedValueOnce(refused(404));

		f.reload(':a.go');
		await settled();

		expect(f.active).toMatchObject({ gone: false, deleted: true });
		expect(f.active?.file).not.toBeNull();
	});

	it('shows the latest read when an earlier one lands after it', async () => {
		const slow = deferred<WorkspaceFile>();
		file.mockReturnValueOnce(slow.promise);
		file.mockResolvedValueOnce(aFile({ content: 'new' }));
		const f = new OpenFiles(RUN);
		f.open(at('a.go'));
		f.reload(':a.go');
		await settled();

		slow.resolve(aFile({ content: 'old' }));
		await settled();

		expect(f.active?.file?.content).toBe('new');
	});

	it('drops what a closed tab read, or failed to', async () => {
		const ok = deferred<WorkspaceFile>();
		const bad = deferred<WorkspaceFile>();
		file.mockReturnValueOnce(ok.promise).mockReturnValueOnce(bad.promise);
		const f = new OpenFiles(RUN);
		f.open(at('a.go'));
		f.open(at('b.go'));
		f.close(':a.go');
		f.close(':b.go');

		ok.resolve(aFile());
		bad.reject(new Error('late'));
		await settled();

		expect(f.tabs).toEqual([]);
	});

	it.each(['read', 'failure'])(
		'keeps a reopened tab’s content over the closed one’s late %s',
		async (late) => {
			const old = deferred<WorkspaceFile>();
			const fresh = deferred<WorkspaceFile>();
			file.mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise);
			const f = new OpenFiles(RUN);
			f.open(at('a.go'));
			f.close(':a.go');
			f.open(at('a.go'));

			fresh.resolve(aFile({ content: 'new' }));
			await settled();
			if (late === 'read') old.resolve(aFile({ content: 'old' }));
			else old.reject(new Error('late'));
			await settled();

			expect(f.active).toMatchObject({ error: '', loading: false });
			expect(f.active?.file?.content).toBe('new');
		}
	);
});

describe('storage', () => {
	it('restores a run’s tabs, active tab, root and folders in a new instance', () => {
		const f = new OpenFiles(RUN);
		f.open(at('a.go'));
		f.open({ abs: '/etc/hosts' });
		f.activate(':a.go');
		f.setRoot({ repo: 'api' });
		f.toggle('src');

		const again = new OpenFiles(RUN);

		expect(again.tabs.map((t) => t.loc)).toEqual([at('a.go'), { abs: '/etc/hosts' }]);
		expect(again.tabs.every((t) => t.file === null)).toBe(true);
		expect(again.activeKey).toBe(':a.go');
		expect(again.root).toEqual({ repo: 'api' });
		expect(again.checkoutRepo).toBe('api');
		expect(again.isOpen('src')).toBe(true);
		expect(new OpenFiles('other').tabs).toEqual([]);
	});

	it('restores an absolute root', () => {
		new OpenFiles(RUN).setRoot({ abs: '/etc' });
		const again = new OpenFiles(RUN);
		expect(again.root).toEqual({ abs: '/etc' });
		expect(again.checkoutRepo).toBe('');
	});

	it.each([
		['not JSON', '{'],
		['not a map', '[1]'],
		['a run that is not an object', JSON.stringify({ [RUN]: 'x' })]
	])('starts empty on storage that is %s', (_, raw) => {
		localStorage.setItem(STORAGE_KEY, raw);
		const f = new OpenFiles(RUN);
		expect(f.tabs).toEqual([]);
		expect(f.activeKey).toBeNull();
		expect(f.root).toEqual({ repo: '' });

		f.open(at('a.go'));
		expect(saved()[RUN].tabs).toEqual([at('a.go')]);
	});

	it('keeps what is sound in a damaged entry', () => {
		localStorage.setItem(
			STORAGE_KEY,
			JSON.stringify({
				[RUN]: {
					tabs: [at('a.go'), at('a.go'), { repo: '', path: '' }, { abs: '' }, 7, { abs: '/x' }],
					active: ':zzz',
					root: { nope: 1 },
					expanded: { ':': ['src', 3], 'api:': 'src' }
				}
			})
		);
		const f = new OpenFiles(RUN);
		expect(keys(f)).toEqual([':a.go', '/x']);
		expect(f.activeKey).toBe(':a.go');
		expect(f.root).toEqual({ repo: '' });
		expect(f.isOpen('src')).toBe(true);
	});

	it.each([
		['no tabs and a root that is not an object', { tabs: 'x', root: 'x', expanded: [] }],
		['an empty entry', {}]
	])('restores nothing from %s', (_, entry) => {
		localStorage.setItem(STORAGE_KEY, JSON.stringify({ [RUN]: entry }));
		const f = new OpenFiles(RUN);
		expect(f.tabs).toEqual([]);
		expect(f.activeKey).toBeNull();
		expect(f.root).toEqual({ repo: '' });
	});

	it('works on, unsaved, when storage is blocked', async () => {
		vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
			throw new Error('blocked');
		});
		vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
			throw new Error('blocked');
		});
		const f = new OpenFiles(RUN);
		f.open(at('a.go'));
		await settled();
		expect(f.active?.file).not.toBeNull();
	});

	it('keeps only the most recent runs', () => {
		const old = Object.fromEntries(
			Array.from({ length: KEPT_RUNS + 5 }, (_, i) => [`old${i}`, { at: i + 1, tabs: [] }])
		);
		localStorage.setItem(STORAGE_KEY, JSON.stringify({ ...old, junk: 'x' }));

		new OpenFiles(RUN).open(at('a.go'));

		const kept = Object.keys(saved());
		expect(kept).toHaveLength(KEPT_RUNS);
		expect(kept[0]).toBe(RUN);
		expect(kept).toContain(`old${KEPT_RUNS + 4}`);
		expect(kept).not.toContain('old0');
		expect(kept).not.toContain('junk');
	});
});

describe('the tree', () => {
	const src = anEntry({ path: 'src', dir: true });

	it('lists the root once, its baseline taken by a stat issued first', async () => {
		tree.mockResolvedValue([src]);
		const f = new OpenFiles(RUN);
		f.show();
		f.show();
		await settled();

		expect(stat).toHaveBeenCalledExactlyOnceWith(RUN, [at('')]);
		expect(stat.mock.invocationCallOrder[0]).toBeLessThan(tree.mock.invocationCallOrder[0]);
		expect(tree).toHaveBeenCalledExactlyOnceWith(RUN, '', '');
		expect(f.dirs['']).toEqual([src]);
		expect(f.isOpen('')).toBe(true);

		f.show();
		await settled();
		expect(tree).toHaveBeenCalledOnce();
	});

	it('starts with nothing open, ignored files hidden', () => {
		const f = new OpenFiles(RUN);
		expect(f.active).toBeNull();
		expect(f.showIgnored).toBe(false);
		f.showIgnored = true;
		expect(f.showIgnored).toBe(true);
	});

	it('reads the restored active tab and lists the restored folders when shown', async () => {
		const f = new OpenFiles(RUN);
		f.open(at('a.go'));
		f.toggle('src');
		f.toggle('src/x');
		await settled();
		tree.mockClear();
		file.mockClear();

		const again = new OpenFiles(RUN);
		again.show();
		await settled();

		expect(file).toHaveBeenCalledExactlyOnceWith(RUN, 'a.go', '');
		expect(tree.mock.calls.map(([, p]) => p)).toEqual(['', 'src', 'src/x']);
	});

	it('shows nothing to read with no tab open', async () => {
		new OpenFiles(RUN).show();
		await settled();
		expect(file).not.toHaveBeenCalled();
	});

	it('folds and unfolds a folder, listing it once', async () => {
		const f = new OpenFiles(RUN);
		f.toggle('src');
		await settled();
		expect(f.isOpen('src')).toBe(true);
		expect(tree).toHaveBeenCalledExactlyOnceWith(RUN, 'src', '');

		f.toggle('src');
		expect(f.isOpen('src')).toBe(false);
		f.toggle('src');
		await settled();
		expect(tree).toHaveBeenCalledOnce();
	});

	it('keeps each root’s folders apart', async () => {
		const f = new OpenFiles(RUN);
		f.toggle('src');
		f.setRoot({ repo: 'api' });
		expect(f.isOpen('src')).toBe(false);
		f.setRoot({ repo: '' });
		expect(f.isOpen('src')).toBe(true);
	});

	it('lists an absolute root through the path route', async () => {
		pathTree.mockResolvedValue([anEntry({ path: '/etc/hosts' })]);
		const f = new OpenFiles(RUN);
		f.setRoot({ abs: '/etc' });
		await settled();

		expect(f.rootPath).toBe('/etc');
		expect(stat).toHaveBeenCalledWith(RUN, [{ abs: '/etc' }]);
		expect(pathTree).toHaveBeenCalledExactlyOnceWith(RUN, '/etc');
		expect(f.dirs['/etc']).toHaveLength(1);
		expect(f.isOpen('/etc')).toBe(true);
	});

	it('goes back to the checkout it came from', async () => {
		const f = new OpenFiles(RUN);
		f.setRoot({ repo: 'api' });
		f.setRoot({ abs: '/etc' });
		f.toCheckout();
		expect(f.root).toEqual({ repo: 'api' });
	});

	it('shows a checkout folder by unfolding down to it', async () => {
		const f = new OpenFiles(RUN);
		f.showDir(at('a/b/c', 'api'));
		await settled();

		expect(f.root).toEqual({ repo: 'api' });
		expect(['a', 'a/b', 'a/b/c'].every((p) => f.isOpen(p))).toBe(true);
		expect(tree.mock.calls.map(([, p, r]) => `${r}:${p}`)).toEqual([
			'api:',
			'api:a',
			'api:a/b',
			'api:a/b/c'
		]);
	});

	it('makes an absolute folder the root', () => {
		const f = new OpenFiles(RUN);
		f.showDir({ abs: '/var/log' });
		expect(f.root).toEqual({ abs: '/var/log' });
	});

	it('names a folder it could not list, and lists it on retry', async () => {
		tree.mockRejectedValueOnce(new Error('permission denied'));
		const f = new OpenFiles(RUN);
		f.toggle('src');
		await settled();
		expect(f.folderError).toEqual({ root: ':', path: 'src', message: 'permission denied' });

		f.retryFolder();
		await settled();

		expect(f.folderError).toBeNull();
		expect(f.dirs.src).toEqual([]);
	});

	it('retries nothing without an error, or with one from another root', async () => {
		const f = new OpenFiles(RUN);
		f.retryFolder();
		tree.mockRejectedValueOnce(new Error('nope'));
		f.toggle('src');
		await settled();
		f.setRoot({ repo: 'api' });
		await settled();
		tree.mockClear();

		f.retryFolder();
		await settled();

		expect(tree).not.toHaveBeenCalled();
		expect(f.folderError?.path).toBe('src');
	});

	it('keeps another folder’s error when one lists', async () => {
		tree.mockRejectedValueOnce(new Error('nope'));
		const f = new OpenFiles(RUN);
		f.toggle('src');
		await settled();
		f.toggle('lib');
		await settled();
		expect(f.folderError?.path).toBe('src');
	});

	it('lists without baselines when the stat fails', async () => {
		stat.mockRejectedValue(new Error('blip'));
		const f = new OpenFiles(RUN);
		f.toggle('src');
		await settled();
		expect(f.dirs.src).toEqual([]);
	});

	it('stops asking for baselines once the stat route is missing', async () => {
		stat.mockRejectedValue(refused(501));
		const f = new OpenFiles(RUN);
		f.toggle('src');
		await settled();
		f.toggle('lib');
		await settled();
		expect(stat).toHaveBeenCalledOnce();
		expect(f.dirs.lib).toEqual([]);
	});

	it('asks for at most the cap of baselines per stat', async () => {
		const f = new OpenFiles(RUN);
		for (let i = 0; i <= STAT_CAP; i++) f.toggle(`d${i}`);
		await settled();
		tree.mockClear();
		stat.mockClear();

		await f.refresh();

		expect(stat.mock.calls.map(([, locs]) => locs.length)).toEqual([STAT_CAP, 1]);
		expect(tree).toHaveBeenCalledTimes(STAT_CAP + 1);
	});

	describe('refresh', () => {
		it('lists every folder read again and reads the active tab', async () => {
			const f = new OpenFiles(RUN);
			f.show();
			f.toggle('src');
			f.open(at('a.go'));
			await settled();
			tree.mockClear();
			file.mockClear();

			await f.refresh();

			expect(tree.mock.calls.map(([, p]) => p)).toEqual(['', 'src']);
			expect(file).toHaveBeenCalledOnce();
		});

		it('lists the root when nothing was listed, and reads no tab with none', async () => {
			const f = new OpenFiles(RUN);
			await f.refresh();
			expect(tree).toHaveBeenCalledExactlyOnceWith(RUN, '', '');
			expect(file).not.toHaveBeenCalled();
		});
	});
});

describe('live refresh', () => {
	let stop: () => void;
	let workspace = true;

	/** Opens a.go then b.go (b active), with src expanded, everything read. */
	async function watching(): Promise<OpenFiles> {
		const f = new OpenFiles(RUN);
		f.visible = true;
		f.show();
		f.toggle('src');
		f.open(at('a.go'));
		f.open(at('b.go'));
		await settled();
		stat.mockClear();
		tree.mockClear();
		file.mockClear();
		stop = f.start(() => workspace);
		return f;
	}
	const tickNow = () => vi.advanceTimersByTimeAsync(POLL_MS);

	beforeEach(() => {
		vi.useFakeTimers();
		workspace = true;
		statsAre((loc) => aStat({ dir: 'path' in loc && !loc.path.endsWith('.go') }));
	});
	afterEach(() => {
		stop?.();
		vi.useRealTimers();
	});

	it('stats every open tab and expanded folder in one call per tick', async () => {
		const f = await watching();
		f.open(at('src', ''));
		await settled();
		stat.mockClear();
		file.mockClear();

		await tickNow();

		expect(stat).toHaveBeenCalledExactlyOnceWith(RUN, [at('a.go'), at('b.go'), at('src'), at('')]);
		await tickNow();
		expect(stat).toHaveBeenCalledTimes(2);
	});

	it('leaves a folded folder out', async () => {
		const f = await watching();
		f.toggle('src');
		await tickNow();
		expect(stat).toHaveBeenCalledExactlyOnceWith(RUN, [at('a.go'), at('b.go'), at('')]);
	});

	it('fetches nothing on an unchanged tick', async () => {
		await watching();
		await tickNow();
		expect(stat).toHaveBeenCalledOnce();
		expect(file).not.toHaveBeenCalled();
		expect(tree).not.toHaveBeenCalled();
	});

	it('never ticks while one is in flight', async () => {
		await watching();
		const slow = deferred<PathStat[]>();
		stat.mockReturnValueOnce(slow.promise);
		await tickNow();
		await vi.advanceTimersByTimeAsync(POLL_MS * 5);
		expect(stat).toHaveBeenCalledOnce();

		slow.resolve([]);
		await tickNow();
		expect(stat).toHaveBeenCalledTimes(2);
	});

	it('does not poll while the pane is hidden', async () => {
		const f = await watching();
		f.visible = false;
		await tickNow();
		expect(stat).not.toHaveBeenCalled();

		f.visible = true;
		await tickNow();
		expect(stat).toHaveBeenCalledOnce();
	});

	it('does not poll while the page is hidden', async () => {
		vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);
		await watching();
		await tickNow();
		expect(stat).not.toHaveBeenCalled();
	});

	it('does not poll without a workspace', async () => {
		workspace = false;
		await watching();
		await tickNow();
		expect(stat).not.toHaveBeenCalled();
	});

	it('does not poll with nothing to watch', async () => {
		const f = new OpenFiles(RUN);
		f.visible = true;
		stop = f.start(() => true);
		await tickNow();
		expect(stat).not.toHaveBeenCalled();
	});

	it('backs off after errors, doubling to a cap, and resets on success', async () => {
		await watching();
		stat.mockRejectedValue(new Error('blip'));
		const calls = () => stat.mock.calls.length;

		await tickNow();
		expect(calls()).toBe(1);
		for (const wait of [6000, 12000, 24000, MAX_BACKOFF_MS, MAX_BACKOFF_MS]) {
			await vi.advanceTimersByTimeAsync(wait - 1);
			const before = calls();
			await vi.advanceTimersByTimeAsync(1);
			expect(calls()).toBe(before + 1);
		}

		statsAre();
		await vi.advanceTimersByTimeAsync(MAX_BACKOFF_MS);
		const before = calls();
		await tickNow();
		expect(calls()).toBe(before + 1);
	});

	it.each([501, 404])('stops on a missing route (%i)', async (status) => {
		await watching();
		stat.mockRejectedValue(refused(status));
		await tickNow();
		await vi.advanceTimersByTimeAsync(MAX_BACKOFF_MS * 3);
		expect(stat).toHaveBeenCalledOnce();
	});

	it('stops when stopped, even mid-flight', async () => {
		const f = await watching();
		const slow = deferred<PathStat[]>();
		stat.mockReturnValueOnce(slow.promise);
		await tickNow();
		stop();
		stop();
		slow.resolve([aStat({ size: 1 }), aStat({ size: 1 })]);
		await vi.advanceTimersByTimeAsync(POLL_MS * 3);
		expect(stat).toHaveBeenCalledOnce();
		expect(f.tabs.some((t) => t.stale)).toBe(false);
		expect(file).not.toHaveBeenCalled();
	});

	it('caps the batch', async () => {
		const f = await watching();
		for (let i = 0; i < STAT_CAP + 10; i++) f.open(at(`f${i}.go`));
		await settled();
		stat.mockClear();
		await tickNow();
		expect(stat.mock.calls[0][1]).toHaveLength(STAT_CAP);
	});

	it('reads a changed active tab again', async () => {
		const f = await watching();
		statsAre((loc) => aStat({ mtime: locKey(loc) === ':b.go' ? '2' : '1', dir: false }));
		file.mockResolvedValueOnce(aFile({ content: 'edited', mtime: '2' }));

		await tickNow();
		await settled();

		expect(file).toHaveBeenCalledExactlyOnceWith(RUN, 'b.go', '');
		expect(f.active?.file?.content).toBe('edited');
		expect(f.active?.stale).toBe(false);
	});

	it('keeps one read of the active tab in flight however long it takes, then still sees a later change', async () => {
		const f = await watching();
		const changed = (mtime: string) =>
			statsAre((loc) => aStat({ mtime: locKey(loc) === ':b.go' ? mtime : '1', dir: false }));
		changed('2');
		const slow = deferred<WorkspaceFile>();
		file.mockReturnValueOnce(slow.promise);

		await tickNow();
		for (let i = 0; i < 4; i++) await tickNow();
		expect(stat.mock.calls.length).toBeGreaterThanOrEqual(5);
		expect(file).toHaveBeenCalledOnce();

		slow.resolve(aFile({ content: 'edited', mtime: '2' }));
		await settled();
		expect(f.active?.file?.content).toBe('edited');
		await tickNow();
		expect(file).toHaveBeenCalledOnce();

		changed('3');
		file.mockResolvedValueOnce(aFile({ content: 'again', mtime: '3' }));
		await tickNow();
		await settled();
		expect(file).toHaveBeenCalledTimes(2);
		expect(f.active?.file?.content).toBe('again');
	});

	it('marks a changed hidden tab stale without reading it, and reads it when shown', async () => {
		const f = await watching();
		statsAre((loc) => aStat({ size: locKey(loc) === ':a.go' ? 9 : 2048 }));

		await tickNow();

		expect(file).not.toHaveBeenCalled();
		expect(f.tabs[0].stale).toBe(true);
		f.activate(':a.go');
		await settled();
		expect(file).toHaveBeenCalledExactlyOnceWith(RUN, 'a.go', '');
		expect(f.tabs[0].stale).toBe(false);
	});

	it('marks a missing file deleted, keeping its content, and clears it when it is back', async () => {
		const f = await watching();
		statsAre((loc) => aStat({ exists: locKey(loc) !== ':b.go' }));
		await tickNow();
		expect(f.active?.deleted).toBe(true);
		expect(f.active?.file).not.toBeNull();
		expect(file).not.toHaveBeenCalled();

		statsAre();
		await tickNow();
		await settled();
		expect(f.active?.deleted).toBe(false);
		expect(file).toHaveBeenCalledOnce();
	});

	it('marks a never-read tab deleted, and only clears it when it is back', async () => {
		localStorage.setItem(
			STORAGE_KEY,
			JSON.stringify({ [RUN]: { tabs: [at('x.go')], active: ':x.go' } })
		);
		const f = new OpenFiles(RUN);
		f.visible = true;
		stop = f.start(() => true);
		statsAre(() => aStat({ exists: false }));
		await tickNow();
		expect(f.tabs[0].deleted).toBe(true);

		statsAre(() => aStat({ size: 1 }));
		await tickNow();
		expect(f.tabs[0].deleted).toBe(false);
		expect(file).not.toHaveBeenCalled();
	});

	it('ignores a path it could not stat, and a tab closed meanwhile', async () => {
		const f = await watching();
		const slow = deferred<PathStat[]>();
		stat.mockReturnValueOnce(slow.promise);
		await tickNow();
		f.close(':a.go');
		slow.resolve([aStat({ size: 1 }), aStat({ size: 1, error: 'denied' })]);
		await settled();
		expect(file).not.toHaveBeenCalled();
		expect(f.active?.stale).toBe(false);
	});

	it('does not poll once a baseline found the route missing', async () => {
		stat.mockRejectedValue(refused(501));
		const f = new OpenFiles(RUN);
		f.visible = true;
		f.show();
		await settled();
		stop = f.start(() => true);
		await vi.advanceTimersByTimeAsync(POLL_MS * 3);
		expect(stat).toHaveBeenCalledOnce();
	});

	it('lists a changed folder alone again', async () => {
		const f = await watching();
		tree.mockResolvedValue([anEntry({ path: 'src/new.ts' })]);
		statsAre((loc) =>
			aStat({ mtime: locKey(loc) === ':src' ? '2' : '1', dir: !locKey(loc).endsWith('.go') })
		);

		await tickNow();
		await settled();

		expect(tree).toHaveBeenCalledExactlyOnceWith(RUN, 'src', '');
		expect(f.dirs.src).toHaveLength(1);
		expect(file).not.toHaveBeenCalled();

		await tickNow();
		expect(tree).toHaveBeenCalledOnce();
	});

	it('keeps a change between a folder’s first stat and its listing', async () => {
		// The folder is stat'ed (mtime 1), then changes (2), then is listed.
		stat.mockResolvedValueOnce([aStat({ dir: true, mtime: '1' })]);
		const f = new OpenFiles(RUN);
		f.visible = true;
		f.show();
		await settled();
		stop = f.start(() => true);
		statsAre(() => aStat({ dir: true, mtime: '2' }));
		tree.mockClear();

		await tickNow();

		expect(tree).toHaveBeenCalledExactlyOnceWith(RUN, '', '');
	});

	it('takes a baseline for a folder listed without one, and skips a vanished one', async () => {
		stat.mockRejectedValueOnce(new Error('blip'));
		const f = new OpenFiles(RUN);
		f.visible = true;
		f.show();
		await settled();
		stop = f.start(() => true);

		statsAre(() => aStat({ dir: true, mtime: '5' }));
		await tickNow();
		statsAre(() => aStat({ exists: false }));
		await tickNow();
		statsAre(() => aStat({ dir: true, mtime: '5' }));
		await tickNow();

		expect(tree).toHaveBeenCalledOnce();
	});

	it('watches the folders of the root on show', async () => {
		const f = new OpenFiles(RUN);
		f.visible = true;
		f.setRoot({ abs: '/etc' });
		await settled();
		stat.mockClear();
		stop = f.start(() => true);
		await tickNow();
		expect(stat).toHaveBeenCalledExactlyOnceWith(RUN, [{ abs: '/etc' }]);
	});
});
