// What the review workbench and the workspace views need beyond the shared
// fixtures: files, comments, a file's snapshot on the wire, a text selection.
import { vi } from 'vitest';
import type {
	Attachment,
	ChangedFile,
	FileDiff,
	PingFile,
	ReviewComment,
	WorkspaceChanges,
	WorkspaceEntry,
	WorkspaceFile
} from '$lib/domain';
import type { DocBlock, DraftComment } from '$lib/review/reviewDoc';
import type { ShownFile } from '$lib/review/files.svelte';
import { rect } from '$test/helpers';

const AT = '2026-10-31T12:00:00Z';

export const aPingFile = (over: Partial<PingFile> = {}): PingFile => ({
	path: 'plan.md',
	captured: true,
	snapshotId: 'plan',
	mime: 'text/markdown',
	size: 120,
	...over
});

export const aShownFile = (over: Partial<ShownFile> = {}): ShownFile => ({
	...aPingFile(),
	kind: 'text',
	...over
});

export const aComment = (over: Partial<DraftComment> = {}): DraftComment => ({
	id: 'c1',
	path: 'plan.md',
	quote: '',
	heading: '',
	startLine: 0,
	endLine: 0,
	text: 'Say why',
	...over
});

export const aReviewComment = (over: Partial<ReviewComment> = {}): ReviewComment => ({
	path: 'plan.md',
	body: 'Say why',
	attachmentIds: [],
	...over
});

export const aBlock = (over: Partial<DocBlock> = {}): DocBlock => ({
	raw: 'text',
	heading: '',
	startLine: 1,
	endLine: 1,
	...over
});

export const anAttachment = (over: Partial<Attachment> = {}): Attachment => ({
	id: 'att-1',
	filename: 'notes.txt',
	mime: 'text/plain',
	size: 2048,
	sha256: 'abc',
	createdAt: AT,
	...over
});

export const aChangedFile = (over: Partial<ChangedFile> = {}): ChangedFile => ({
	path: 'src/app.ts',
	status: 'M',
	additions: 3,
	deletions: 1,
	binary: false,
	committed: true,
	...over
});

export const someChanges = (over: Partial<WorkspaceChanges> = {}): WorkspaceChanges => ({
	branch: 'feat/idempotency',
	head: 'ffffffffffffffff',
	base: 'origin/main',
	mergeBase: 'a1b2c3d4e5f6a7b8',
	commits: [],
	files: [],
	...over
});

export const aFileDiff = (over: Partial<FileDiff> = {}): FileDiff => ({
	path: 'src/app.ts',
	diff: '@@ -1,2 +1,2 @@\n keep\n-old line\n+new line\n',
	truncated: false,
	...over
});

export const anEntry = (over: Partial<WorkspaceEntry> = {}): WorkspaceEntry => {
	const path = over.path ?? 'README.md';
	return { name: path.split('/').pop()!, path, dir: false, ignored: false, ...over };
};

export const aWorkspaceFile = (over: Partial<WorkspaceFile> = {}): WorkspaceFile => ({
	path: 'README.md',
	size: 2048,
	binary: false,
	truncated: false,
	content: 'first line\nsecond line',
	...over
});

/** What a snapshot answers: its text or bytes, a status, a failure, or any of
 *  those later (a promise). */
type Served = string | Uint8Array<ArrayBuffer> | number | Error;

/** Snapshots on the wire, by id (`snapshotUrl` in the mocked api ends in it).
 *  An id nobody served is a failure of the test, not a quiet 404. */
export function serveSnapshots(snapshots: Record<string, Served | Promise<Served>>) {
	return vi.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
		const id = String(url).split('/').pop()!;
		if (!(id in snapshots)) throw new Error(`no snapshot served for ${url}`);
		const body = await snapshots[id];
		if (body instanceof Error) throw body;
		return typeof body === 'number' ? new Response(null, { status: body }) : new Response(body);
	});
}

/** What jsdom leaves out and the review calls: a Range's place on screen, and
 *  scrolling an element to a spot. */
export function stubGeometry() {
	Range.prototype.getBoundingClientRect = () => rect(100, 200, 50, 20);
	Element.prototype.scrollTo = () => {};
}

/** Lay a selection over the DOM: from a text node's offset to another's. */
export function select(from: Node, start: number, to: Node = from, end = start): Range {
	const range = document.createRange();
	range.setStart(from, start);
	range.setEnd(to, end);
	const sel = window.getSelection()!;
	sel.removeAllRanges();
	sel.addRange(range);
	return range;
}
