// The Files pane's state for one run: the open tabs (checkout files and
// absolute ones), the tree's root and expanded folders, and the live refresh
// that keeps both current while the pane is on screen.
//
// Tabs, the active tab, the root and the expanded folders are kept per run in
// localStorage, under one key for every run, pruned to the most recent runs.
// They are conveniences, not records (as in prefs.svelte.ts): every read and
// write tolerates blocked or corrupt storage. Contents and listings live in
// memory only.
//
// Live refresh polls a batched stat (size and mtime, no git) of every open tab
// and every expanded folder, and reads again only what changed.

import { api } from '$lib/api';
import { ApiError } from '$lib/api/client';
import { errorMessage } from '$lib/core/errors';
import type { FileLoc, PathStat, WorkspaceEntry, WorkspaceFile } from '$lib/domain';

/** 1-based inclusive line range. */
export interface LineRange {
	from: number;
	to: number;
}

/** What the tree shows: a checkout ('' is the primary) or an absolute folder. */
export type TreeRoot = { repo: string } | { abs: string };

/** What a change is told by: a stat's or a read's size and mtime. */
interface Baseline {
	size: number;
	mtime: string;
}

export interface FileTab {
	key: string;
	loc: FileLoc;
	/** Lines to reveal and select in `file`; cleared once shown. A line-targeted
	 *  open sets it only with the content read after it. */
	line: LineRange | null;
	/** The last content read; kept while a newer read is on its way. */
	file: WorkspaceFile | null;
	/** Why the last read failed ('' when it did not). */
	error: string;
	/** The file was not there when first read (a restored tab whose file is gone). */
	gone: boolean;
	loading: boolean;
	/** Changed on disk while hidden: read again when activated. */
	stale: boolean;
	/** No longer on disk; the last content stays on show. */
	deleted: boolean;
	/** The size and mtime `file` was read with. */
	stat: Baseline | null;
}

/** A folder that could not be listed, in which root. */
export interface FolderError {
	root: string;
	path: string;
	message: string;
}

export const STORAGE_KEY = 'apothecary.files.v1';
/** Runs whose tabs are kept; older ones are dropped on each write. */
export const KEPT_RUNS = 20;
export const POLL_MS = 3000;
export const MAX_BACKOFF_MS = 30_000;
/** The most paths one stat call may carry (the engine refuses more). */
export const STAT_CAP = 256;

/** A tab's identity: the absolute path, or the checkout and its path. */
export const locKey = (loc: FileLoc): string =>
	'abs' in loc ? loc.abs : `${loc.repo}:${loc.path}`;
/** A tree root's identity (its folders' state is kept per root). */
export const rootKey = (root: TreeRoot): string => ('abs' in root ? root.abs : `${root.repo}:`);
/** Where one of a root's folders lives. */
const folderLoc = (root: TreeRoot, path: string): FileLoc =>
	'abs' in root ? { abs: path } : { repo: root.repo, path };
/** A copy free of reactive proxies, for storage and the wire. */
const plainLoc = (loc: FileLoc): FileLoc =>
	'abs' in loc ? { abs: loc.abs } : { repo: loc.repo, path: loc.path };
const plainRoot = (root: TreeRoot): TreeRoot =>
	'abs' in root ? { abs: root.abs } : { repo: root.repo };
const same = (a: Baseline, b: Baseline) => a.size === b.size && a.mtime === b.mtime;
/** The workspace's sidecar (or the engine) predates the stat route. */
const routeMissing = (e: unknown) =>
	e instanceof ApiError && (e.status === 501 || e.status === 404);

const newTab = (loc: FileLoc): FileTab => ({
	key: locKey(loc),
	loc: plainLoc(loc),
	line: null,
	file: null,
	error: '',
	gone: false,
	loading: false,
	stale: false,
	deleted: false,
	stat: null
});

// --- storage ------------------------------------------------------------------

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);
const isStr = (v: unknown): v is string => typeof v === 'string';

function asLoc(v: unknown): FileLoc | null {
	if (!isObj(v)) return null;
	if (isStr(v.abs) && v.abs) return { abs: v.abs };
	if (isStr(v.repo) && isStr(v.path) && v.path) return { repo: v.repo, path: v.path };
	return null;
}
function asRoot(v: unknown): TreeRoot | null {
	if (!isObj(v)) return null;
	if (isStr(v.abs) && v.abs) return { abs: v.abs };
	if (isStr(v.repo)) return { repo: v.repo };
	return null;
}

function readAll(): Obj {
	try {
		const all: unknown = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '{}');
		return isObj(all) ? all : {};
	} catch {
		return {};
	}
}
function writeAll(all: Obj) {
	try {
		localStorage.setItem(STORAGE_KEY, JSON.stringify(all));
	} catch {
		/* storage blocked: the tabs last this page only */
	}
}
const savedAt = (v: unknown) => (isObj(v) && typeof v.at === 'number' ? v.at : 0);

// --- the state ------------------------------------------------------------------

/** One item of a poll: a path, and the tabs and folder it stands for. */
interface Watched {
	loc: FileLoc;
	tabs: string[];
	dir?: string;
}

export class OpenFiles {
	readonly runId: string;

	tabs = $state<FileTab[]>([]);
	activeKey = $state<string | null>(null);
	root = $state<TreeRoot>({ repo: '' });
	showIgnored = $state(false);
	folderError = $state<FolderError | null>(null);
	/** The pane is on screen: polls run only then. */
	visible = false;

	/** Listings by root key, then folder path (replaced whole: raw). */
	#listed = $state.raw<Record<string, Record<string, WorkspaceEntry[]>>>({});
	/** Expanded folders by root key. */
	#expanded = $state.raw<Record<string, Record<string, true>>>({});
	/** The checkout to go back to from an absolute root. */
	#lastRepo = '';
	/** The latest read asked per tab: only it lands. Numbers come from one
	 *  counter, so a tab closed and opened again never reuses a closed one's. */
	#reads = new Map<string, number>();
	#readSeq = 0;
	/** Lines a line-targeted open asked for, by tab: they become the tab's
	 *  `line` with the content of the read that open issued (or a later one). */
	#wanted = new Map<string, LineRange>();
	/** Folder listings on their way, by root key and path. */
	#listing = new Set<string>();
	/** Folder baselines by the folder's loc key. */
	#dirStat = new Map<string, Baseline>();
	#timer: ReturnType<typeof setTimeout> | undefined;
	/** Bumped by start and stop: a poll from an older run of the poller ends. */
	#generation = 0;
	#delay = POLL_MS;
	/** The stat route is missing (an older sidecar): no polls, no baselines. */
	#statGone = false;

	constructor(runId: string) {
		this.runId = runId;
		this.#restore();
	}

	readonly active = $derived(this.tabs.find((t) => t.key === this.activeKey) ?? null);
	/** The root's folder path ('' or the absolute folder). */
	readonly rootPath = $derived('abs' in this.root ? this.root.abs : '');
	/** The current root's listings by folder path. */
	readonly dirs = $derived(this.#listed[rootKey(this.root)] ?? {});
	/** The checkout an absolute root goes back to. */
	get checkoutRepo(): string {
		return this.#lastRepo;
	}

	isOpen(path: string): boolean {
		return path === this.rootPath || !!this.#expanded[rootKey(this.root)]?.[path];
	}

	// --- tabs ---------------------------------------------------------------------

	/** Focus the tab of `loc` (opening it at the end when there is none). With
	 *  `line`, the file is read afresh and the lines revealed in that content:
	 *  the copy on show may predate an edit that moved them. */
	open(loc: FileLoc, line?: LineRange) {
		const key = locKey(loc);
		if (!this.#tab(key)) this.tabs.push(newTab(loc));
		if (!line) return this.activate(key);
		this.#tab(key)!.line = null;
		this.#wanted.set(key, { from: line.from, to: line.to });
		this.activeKey = key;
		this.#save();
		void this.#read(key);
	}

	/** Show a tab, reading it when it has nothing yet or changed while hidden. */
	activate(key: string) {
		const tab = this.#tab(key);
		if (!tab) return;
		this.activeKey = key;
		if (!tab.loading && (tab.stale || !tab.file)) void this.#read(key);
		this.#save();
	}

	/** Close a tab; closing the active one shows its neighbour. */
	close(key: string) {
		const i = this.tabs.findIndex((t) => t.key === key);
		if (i < 0) return;
		this.tabs.splice(i, 1);
		this.#reads.delete(key);
		this.#wanted.delete(key);
		if (this.activeKey === key) {
			this.activeKey = null;
			const next = this.tabs[i] ?? this.tabs[i - 1];
			if (next) this.activate(next.key);
		}
		this.#save();
	}

	/** Move the tab at `from` to `to`. */
	move(from: number, to: number) {
		const n = this.tabs.length;
		if (from === to || from < 0 || to < 0 || from >= n || to >= n) return;
		const [tab] = this.tabs.splice(from, 1);
		this.tabs.splice(to, 0, tab);
		this.#save();
	}

	/** The viewer showed the tab's pending lines. */
	revealed(key: string) {
		const tab = this.#tab(key);
		if (tab) tab.line = null;
	}

	/** Read a tab again (its Retry). */
	reload(key: string) {
		if (this.#tab(key)) void this.#read(key);
	}

	/** The pane came on screen: list what the tree shows and read the active tab. */
	show() {
		this.#ensureTree();
		if (this.activeKey) this.activate(this.activeKey);
	}

	/** The refresh button: list the tree's folders again and re-read the active tab. */
	async refresh() {
		const listed = Object.keys(this.dirs);
		const active = this.activeKey;
		await Promise.all([
			this.#listMany(listed.length ? listed : [this.rootPath]),
			active ? this.#read(active) : undefined
		]);
	}

	#tab(key: string): FileTab | undefined {
		return this.tabs.find((t) => t.key === key);
	}

	async #read(key: string) {
		const tab = this.#tab(key)!;
		const asked = ++this.#readSeq;
		this.#reads.set(key, asked);
		const loc = plainLoc(tab.loc);
		tab.loading = true;
		tab.error = '';
		tab.gone = false;
		// The tab may have been closed (or read again) while this one was out.
		const current = () => (this.#reads.get(key) === asked ? this.#tab(key) : undefined);
		try {
			const file =
				'abs' in loc
					? await api.workspace.pathFile(this.runId, loc.abs)
					: await api.workspace.file(this.runId, loc.path, loc.repo);
			const t = current();
			if (!t) return;
			t.file = file;
			t.stat = { size: file.size, mtime: file.mtime };
			t.loading = t.stale = t.deleted = false;
			this.#land(t);
		} catch (e) {
			const t = current();
			if (!t) return;
			t.loading = t.stale = false;
			// A deleted file shows the lines on its last content; any other failure
			// keeps them pending until a read succeeds, never on the content retained
			// (a tab already marked deleted included).
			if (e instanceof ApiError && e.status === 404) {
				if (t.file) {
					t.deleted = true;
					this.#land(t);
				} else t.gone = true;
			} else t.error = errorMessage(e);
		}
	}

	/** The lines a line-targeted open asked for, with the content just read. */
	#land(tab: FileTab) {
		const want = this.#wanted.get(tab.key);
		if (!want) return;
		this.#wanted.delete(tab.key);
		tab.line = want;
	}

	// --- the tree -----------------------------------------------------------------

	setRoot(root: TreeRoot) {
		this.root = plainRoot(root);
		if ('repo' in root) this.#lastRepo = root.repo;
		this.#save();
		this.#ensureTree();
	}

	/** Back from an absolute root to the checkout it came from. */
	toCheckout() {
		this.setRoot({ repo: this.#lastRepo });
	}

	/** Show a folder: an absolute one becomes the root; a checkout's is
	 *  expanded down to in its checkout. */
	showDir(loc: FileLoc) {
		if ('abs' in loc) return this.setRoot({ abs: loc.abs });
		const parts = loc.path.split('/').filter(Boolean);
		const rk = rootKey({ repo: loc.repo });
		const open = { ...this.#expanded[rk] };
		parts.forEach((_, i) => (open[parts.slice(0, i + 1).join('/')] = true));
		this.#expanded = { ...this.#expanded, [rk]: open };
		this.setRoot({ repo: loc.repo });
	}

	/** Fold or unfold a folder, listing it the first time. */
	toggle(path: string) {
		const rk = rootKey(this.root);
		const open = { ...this.#expanded[rk] };
		const opening = !open[path];
		if (opening) open[path] = true;
		else delete open[path];
		this.#expanded = { ...this.#expanded, [rk]: open };
		this.#save();
		if (opening && !this.dirs[path]) void this.#listMany([path]);
	}

	/** List the folder that failed again. */
	retryFolder() {
		const err = this.folderError;
		if (err && err.root === rootKey(this.root)) void this.#listMany([err.path]);
	}

	/** List the root and its expanded folders that are not listed yet. */
	#ensureTree() {
		const rk = rootKey(this.root);
		const want = [this.rootPath, ...Object.keys(this.#expanded[rk] ?? {})];
		const missing = want.filter((p) => !this.dirs[p]);
		if (missing.length) void this.#listMany(missing);
	}

	/** List folders of the current root, each one's baseline taken from a
	 *  stat issued before its listing, so a change in between is not lost. */
	async #listMany(paths: string[]) {
		const root = plainRoot(this.root);
		const mine = paths.filter((p) => this.#claim(root, p));
		const bases = await this.#baselines(root, mine);
		await Promise.all(mine.map((p, i) => this.#list(root, p, bases[i])));
	}

	/** Mark a folder's listing as on its way; false when one already is. */
	#claim(root: TreeRoot, path: string): boolean {
		const id = `${rootKey(root)}\n${path}`;
		if (this.#listing.has(id)) return false;
		this.#listing.add(id);
		return true;
	}

	async #baselines(root: TreeRoot, paths: string[]): Promise<(PathStat | undefined)[]> {
		if (this.#statGone) return [];
		try {
			const out: PathStat[] = [];
			for (let i = 0; i < paths.length; i += STAT_CAP) {
				const locs = paths.slice(i, i + STAT_CAP).map((p) => folderLoc(root, p));
				out.push(...(await api.workspace.stat(this.runId, locs)));
			}
			return out;
		} catch (e) {
			if (routeMissing(e)) this.#statGone = true;
			return [];
		}
	}

	/** List a folder claimed with #claim. */
	async #list(root: TreeRoot, path: string, base?: PathStat) {
		const rk = rootKey(root);
		try {
			const entries =
				'abs' in root
					? await api.workspace.pathTree(this.runId, path)
					: await api.workspace.tree(this.runId, path, root.repo);
			this.#listed = { ...this.#listed, [rk]: { ...this.#listed[rk], [path]: entries } };
			if (base?.exists) this.#dirStat.set(locKey(folderLoc(root, path)), base);
			const err = this.folderError;
			if (err?.root === rk && err.path === path) this.folderError = null;
		} catch (e) {
			this.folderError = { root: rk, path, message: errorMessage(e) };
		} finally {
			this.#listing.delete(`${rk}\n${path}`);
		}
	}

	// --- live refresh -------------------------------------------------------------

	/** Poll while the pane is visible, the page is shown and the run has a
	 *  workspace. Returns the stop. */
	start(hasWorkspace: () => boolean): () => void {
		const generation = ++this.#generation;
		this.#delay = POLL_MS;
		this.#schedule(generation, hasWorkspace);
		return () => {
			if (this.#generation === generation) this.#generation++;
			clearTimeout(this.#timer);
		};
	}

	#schedule(generation: number, hasWorkspace: () => boolean) {
		this.#timer = setTimeout(() => void this.#tick(generation, hasWorkspace), this.#delay);
	}

	async #tick(generation: number, hasWorkspace: () => boolean) {
		if (this.#statGone) return;
		const items = this.visible && !document.hidden && hasWorkspace() ? this.#watched() : [];
		if (items.length) {
			const root = plainRoot(this.root);
			try {
				const stats = await api.workspace.stat(
					this.runId,
					items.map((w) => w.loc)
				);
				this.#delay = POLL_MS;
				if (generation === this.#generation)
					items.forEach((w, i) => this.#apply(root, w, stats[i]));
			} catch (e) {
				if (routeMissing(e)) {
					this.#statGone = true;
					return;
				}
				this.#delay = Math.min(this.#delay * 2, MAX_BACKOFF_MS);
			}
		}
		if (generation === this.#generation) this.#schedule(generation, hasWorkspace);
	}

	/** Every open tab, then every expanded folder of the root, once each, capped. */
	#watched(): Watched[] {
		const seen = new Map<string, Watched>();
		const at = (loc: FileLoc) => {
			const k = locKey(loc);
			let w = seen.get(k);
			if (!w) seen.set(k, (w = { loc: plainLoc(loc), tabs: [] }));
			return w;
		};
		for (const t of this.tabs) at(t.loc).tabs.push(t.key);
		const root = this.root;
		for (const p of Object.keys(this.dirs)) if (this.isOpen(p)) at(folderLoc(root, p)).dir = p;
		return [...seen.values()].slice(0, STAT_CAP);
	}

	#apply(root: TreeRoot, w: Watched, st: PathStat | undefined) {
		if (!st || st.error) return;
		for (const key of w.tabs) this.#applyTab(key, st);
		if (w.dir !== undefined) this.#applyDir(root, w.dir, st);
	}

	#applyTab(key: string, st: PathStat) {
		const tab = this.#tab(key);
		if (!tab) return;
		if (!st.exists) {
			tab.deleted = true;
			return;
		}
		// A read still out: its answer carries its own baseline, and the next stat compares against that.
		if (tab.loading) return;
		const back = tab.deleted;
		tab.deleted = false;
		// Never read: it is read when activated.
		if (!tab.stat || (!back && same(tab.stat, st))) return;
		if (key === this.activeKey) void this.#read(key);
		else tab.stale = true;
	}

	#applyDir(root: TreeRoot, path: string, st: PathStat) {
		if (!st.exists) return;
		const k = locKey(folderLoc(root, path));
		const base = this.#dirStat.get(k);
		if (!base) this.#dirStat.set(k, st);
		else if (!same(base, st) && this.#claim(root, path)) void this.#list(root, path, st);
	}

	// --- storage --------------------------------------------------------------------

	#restore() {
		const saved = readAll()[this.runId];
		if (!isObj(saved)) return;
		const locs = Array.isArray(saved.tabs) ? saved.tabs.map(asLoc) : [];
		const tabs: FileTab[] = [];
		for (const loc of locs)
			if (loc && !tabs.some((t) => t.key === locKey(loc))) tabs.push(newTab(loc));
		this.tabs = tabs;
		const active = saved.active;
		this.activeKey = tabs.some((t) => t.key === active)
			? (active as string)
			: (tabs[0]?.key ?? null);
		const root = asRoot(saved.root);
		if (root) {
			this.root = root;
			if ('repo' in root) this.#lastRepo = root.repo;
		}
		const expanded: Record<string, Record<string, true>> = {};
		if (isObj(saved.expanded))
			for (const [rk, paths] of Object.entries(saved.expanded))
				if (Array.isArray(paths))
					expanded[rk] = Object.fromEntries(paths.filter(isStr).map((p) => [p, true] as const));
		this.#expanded = expanded;
	}

	#save() {
		const all = readAll();
		all[this.runId] = {
			at: Date.now(),
			tabs: this.tabs.map((t) => plainLoc(t.loc)),
			active: this.activeKey,
			root: plainRoot(this.root),
			expanded: Object.fromEntries(
				Object.entries(this.#expanded).map(([rk, open]) => [rk, Object.keys(open)])
			)
		};
		const kept = Object.entries(all)
			.sort(([, a], [, b]) => savedAt(b) - savedAt(a))
			.slice(0, KEPT_RUNS);
		writeAll(Object.fromEntries(kept));
	}
}
