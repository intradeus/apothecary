// The files a review shows (DESIGN3 §3), one store per review page: the
// ping's files in the order the gate shows them, each one's content read
// once from its raise-time snapshot and kept — switching files never fetches
// twice — and which one is open. What you review is what was captured, not
// what the workspace holds by the time you look (DESIGN-FILES §9).
import { errorMessage } from '$lib/core/errors';
import { SvelteMap, SvelteSet } from 'svelte/reactivity';
import { api } from '$lib/api';
import type { PingFile } from '$lib/domain';
import {
	fileKind,
	looksBinary,
	readDoc,
	type DocBlock,
	type FileKind
} from '$lib/review/reviewDoc';
import { t } from '$lib/i18n/index.svelte';
import { treeOrder } from '$lib/core/pathTree';
import { prefs } from '$lib/app/prefs.svelte';

export interface ShownFile extends PingFile {
	kind: FileKind;
}

/** A read text file: its blocks, or why there are none, and whether they
 *  are rendered (markdown, a structured type's view) rather than a listing. */
export interface LoadedDoc {
	blocks: DocBlock[];
	rendered?: boolean;
	error?: string;
}

export class ReviewFiles {
	files = $state.raw<ShownFile[]>([]);
	/** The link the ping opens beside its files, if any. */
	link = $state<string | undefined>();
	loading = $state(true);
	error = $state('');
	/** The open file's path. */
	current = $state('');
	/** Files opened at least once. */
	readonly viewed = new SvelteSet<string>();
	/** Read text files, by path. */
	readonly docs = new SvelteMap<string, LoadedDoc>();
	/** Each file's scroll offset, so going back lands where you were. Read on
	 *  demand, never rendered. */
	readonly scrolls = new Map<string, number>();
	readonly #reads = new Map<string, Promise<void>>();
	readonly #pingId: string;

	constructor(pingId: string) {
		this.#pingId = pingId;
	}

	readonly file = $derived(this.files.find((f) => f.path === this.current));
	readonly paths = $derived(this.files.map((f) => f.path));
	/** The paths in the order the file list shows them (prefs: list or tree). */
	readonly order = $derived(prefs.fileLayout === 'tree' ? treeOrder(this.paths) : this.paths);
	/** The gate's first file, whatever the layout: the review opens on it,
	 *  and an old draft's comments are on it. */
	readonly primary = $derived(this.files[0]?.path ?? '');

	/** ONE fetch for the list; then the first file, then the rest in the
	 *  background, so moving on is instant. */
	async init(): Promise<void> {
		try {
			const p = await api.pings.payload(this.#pingId);
			this.files = p.files.map((f) => ({ ...f, kind: fileKind(f) }));
			this.link = p.link;
			if (!this.files.length) this.error = t('doc.noFiles');
		} catch (e) {
			this.error = errorMessage(e);
		} finally {
			this.loading = false;
		}
		if (!this.files.length) return;
		await this.open(this.primary);
		void (async () => {
			for (const f of this.files) await this.read(f.path);
		})();
	}

	open(path: string): Promise<void> {
		this.current = path;
		this.viewed.add(path);
		return this.read(path);
	}

	/** Move d files along the list as shown (j/k), staying at its ends. */
	step(d: number) {
		const i = this.order.indexOf(this.current);
		const next = this.order[Math.max(0, Math.min(this.order.length - 1, i + d))];
		if (next && next !== this.current) void this.open(next);
	}

	/** Read a text file's snapshot once. A file whose head holds a NUL turns
	 *  out to be binary, and is offered as a download instead. */
	read(path: string): Promise<void> {
		const f = this.files.find((x) => x.path === path);
		if (!f || f.kind !== 'text' || !f.captured || !f.snapshotId || this.docs.has(path))
			return Promise.resolve();
		let r = this.#reads.get(path);
		if (!r) {
			r = this.#fetch(f);
			this.#reads.set(path, r);
		}
		return r;
	}

	async #fetch(f: ShownFile): Promise<void> {
		try {
			const res = await fetch(api.files.snapshotUrl(f.snapshotId!));
			if (!res.ok) throw new Error(t('doc.docUnavailable', { status: res.status }));
			const bytes = new Uint8Array(await res.arrayBuffer());
			if (looksBinary(bytes)) {
				this.files = this.files.map((x) =>
					x.path === f.path ? { ...x, kind: 'other' as const } : x
				);
				return;
			}
			this.docs.set(f.path, readDoc(new TextDecoder().decode(bytes), f.path, f.type));
		} catch (e) {
			this.docs.set(f.path, { blocks: [], error: errorMessage(e) });
		}
	}
}
