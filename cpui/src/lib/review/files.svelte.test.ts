import { beforeEach, describe, expect, it, vi } from 'vitest';
import { prefs } from '$lib/app/prefs.svelte';
import { api } from '$lib/api';
import { aPingFile, serveSnapshots } from '$test/fixtures/review';
import { deferred } from '$test/helpers';
import { ReviewFiles } from './files.svelte';

vi.mock('$lib/api', () => ({
	api: {
		pings: { payload: vi.fn() },
		files: { snapshotUrl: (id: string) => `/api/snapshots/${id}` }
	}
}));

const plan = aPingFile({ path: 'plan.md', snapshotId: 'plan' });
const notes = aPingFile({ path: 'notes.md', snapshotId: 'notes' });
const shot = aPingFile({ path: 'shot.png', snapshotId: 'shot', mime: 'image/png' });
const payload = (...files: ReturnType<typeof aPingFile>[]) =>
	vi.mocked(api.pings.payload).mockResolvedValue({ files });

describe('ReviewFiles', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		prefs.setFileLayout('list');
	});

	it('starts out loading, with nothing open', () => {
		const set = new ReviewFiles('p1');
		expect(set.loading).toBe(true);
		expect(set.file).toBeUndefined();
		expect(set.primary).toBe('');
	});

	it('lists the ping’s files with what each one is, and its link', async () => {
		vi.mocked(api.pings.payload).mockResolvedValue({
			files: [plan, shot],
			link: 'https://mr.test/1'
		});
		serveSnapshots({ plan: '# Plan\n' });
		const set = new ReviewFiles('p1');

		await set.init();

		expect(api.pings.payload).toHaveBeenCalledWith('p1');
		expect(set.loading).toBe(false);
		expect(set.error).toBe('');
		expect(set.link).toBe('https://mr.test/1');
		expect(set.paths).toEqual(['plan.md', 'shot.png']);
		expect(set.primary).toBe('plan.md');
		expect(set.files.map((f) => f.kind)).toEqual(['text', 'image']);
	});

	it('opens the first file, read and marked viewed, the rest left unviewed', async () => {
		payload(plan, notes);
		serveSnapshots({ plan: '# Plan\n', notes: 'n\n' });
		const set = new ReviewFiles('p1');

		await set.init();

		expect(set.current).toBe('plan.md');
		expect(set.file?.path).toBe('plan.md');
		expect(set.docs.get('plan.md')?.blocks[0].heading).toBe('Plan');
		expect([...set.viewed]).toEqual(['plan.md']);
	});

	it('reads the rest in the background, each file once', async () => {
		payload(plan, notes);
		const fetched = serveSnapshots({ plan: 'p\n', notes: 'n\n' });
		const set = new ReviewFiles('p1');

		await set.init();
		await vi.waitFor(() => expect(set.docs.has('notes.md')).toBe(true));
		await set.open('notes.md');
		await set.open('plan.md');

		expect(fetched.mock.calls.map(([url]) => url)).toEqual([
			'/api/snapshots/plan',
			'/api/snapshots/notes'
		]);
	});

	it('shares one read between two callers while it is in flight', async () => {
		payload(plan);
		const body = deferred<string>();
		const fetched = serveSnapshots({ plan: body.promise });
		const set = new ReviewFiles('p1');
		const init = set.init();
		await vi.waitFor(() => expect(set.current).toBe('plan.md'));

		const again = set.read('plan.md');
		body.resolve('p\n');
		await Promise.all([init, again]);

		expect(fetched).toHaveBeenCalledOnce();
	});

	it('says so when the ping shows no files, and reads nothing', async () => {
		payload();
		const fetched = serveSnapshots({});
		const set = new ReviewFiles('p1');

		await set.init();

		expect(set.error).toBe('this ping has no attached files');
		expect(set.loading).toBe(false);
		expect(set.current).toBe('');
		expect(fetched).not.toHaveBeenCalled();
	});

	it('carries a failed list as the error', async () => {
		vi.mocked(api.pings.payload).mockRejectedValue(new Error('engine down'));
		const set = new ReviewFiles('p1');

		await set.init();

		expect(set.error).toBe('engine down');
		expect(set.loading).toBe(false);
		expect(set.files).toEqual([]);
	});

	it.each([
		['a refused read, with its status', 404, 'document unavailable (404)'],
		['a read that never landed', new Error('offline'), 'offline']
	])('holds %s as that file’s error', async (_, served, want) => {
		payload(plan);
		serveSnapshots({ plan: served });
		const set = new ReviewFiles('p1');

		await set.init();

		expect(set.docs.get('plan.md')).toEqual({ blocks: [], error: want });
		expect(set.error).toBe('');
	});

	it('does not read a failed file again', async () => {
		payload(plan);
		const fetched = serveSnapshots({ plan: 500 });
		const set = new ReviewFiles('p1');
		await set.init();

		await set.read('plan.md');

		expect(fetched).toHaveBeenCalledOnce();
	});

	it('turns a text file whose head holds a NUL into a download', async () => {
		payload(plan);
		const fetched = serveSnapshots({ plan: new Uint8Array([72, 0, 73]) });
		const set = new ReviewFiles('p1');

		await set.init();
		await set.read('plan.md');

		expect(set.file?.kind).toBe('other');
		expect(set.docs.has('plan.md')).toBe(false);
		expect(fetched).toHaveBeenCalledOnce();
	});

	it('splits a read file by its own type, not as markdown', async () => {
		const cfg = aPingFile({ path: 'cfg.yaml', snapshotId: 'cfg', mime: 'text/yaml' });
		payload(cfg);
		serveSnapshots({ cfg: 'a: 1\n\nb: 2\n' });
		const set = new ReviewFiles('p1');

		await set.init();

		expect(set.docs.get('cfg.yaml')?.blocks.map((b) => b.startLine)).toEqual([1, 3]);
	});

	it.each([
		['by its view', 'verdict: APPROVE\n', true, '## Approved'],
		[
			'as written when it is not its type',
			'verdict: MAYBE\n',
			false,
			'```yaml\nverdict: MAYBE\n```'
		]
	])('reads a structured file %s', async (_, body, rendered, first) => {
		const v = aPingFile({ path: 'artifacts/verdict.yaml', snapshotId: 'v', type: 'verdict' });
		payload(v);
		serveSnapshots({ v: body });
		const set = new ReviewFiles('p1');

		await set.init();

		const doc = set.docs.get('artifacts/verdict.yaml');
		expect(doc?.rendered).toBe(rendered);
		expect(doc?.blocks[0].raw).toBe(first);
	});

	it.each([
		['an image', shot],
		['a file the capture missed', aPingFile({ captured: false })],
		['a file with no snapshot', aPingFile({ snapshotId: undefined })],
		['a binary type', aPingFile({ path: 'a.zip', mime: 'application/zip' })]
	])('never fetches %s', async (_, file) => {
		payload(file);
		const fetched = serveSnapshots({});
		const set = new ReviewFiles('p1');

		await set.init();

		expect(set.current).toBe(file.path);
		expect(fetched).not.toHaveBeenCalled();
	});

	it('reads nothing for a path it does not show', async () => {
		payload();
		const fetched = serveSnapshots({});
		const set = new ReviewFiles('p1');
		await set.init();

		await set.read('stray.md');

		expect(fetched).not.toHaveBeenCalled();
	});

	describe('step', () => {
		const three = async (at: string) => {
			payload(
				aPingFile({ path: 'a.png', mime: 'image/png' }),
				aPingFile({ path: 'b.png', mime: 'image/png' }),
				aPingFile({ path: 'c.png', mime: 'image/png' })
			);
			const set = new ReviewFiles('p1');
			await set.init();
			await set.open(at);
			set.viewed.clear();
			return set;
		};

		it.each([
			['forward', 'a.png', 1, 'b.png'],
			['back', 'b.png', -1, 'a.png'],
			['several along', 'a.png', 2, 'c.png'],
			['stopping at the end', 'c.png', 1, 'c.png'],
			['stopping at the start', 'a.png', -1, 'a.png'],
			['past the end in one go', 'b.png', 9, 'c.png']
		])('moves %s', async (_, at, d, want) => {
			const set = await three(at);
			set.step(d);
			expect(set.current).toBe(want);
		});

		it('marks the file it lands on viewed', async () => {
			const set = await three('a.png');
			set.step(1);
			expect(set.viewed.has('b.png')).toBe(true);
		});

		it('does not count staying put as a view', async () => {
			const set = await three('c.png');
			set.step(1);
			expect(set.viewed.size).toBe(0);
		});

		it('does nothing with no files', async () => {
			payload();
			const set = new ReviewFiles('p1');
			await set.init();
			set.step(1);
			expect(set.current).toBe('');
		});

		describe('as a tree', () => {
			const nested = async () => {
				payload(
					aPingFile({ path: 'z.png', mime: 'image/png' }),
					aPingFile({ path: 'docs/b.png', mime: 'image/png' }),
					aPingFile({ path: 'docs/a.png', mime: 'image/png' })
				);
				const set = new ReviewFiles('p1');
				await set.init();
				return set;
			};
			beforeEach(() => prefs.setFileLayout('tree'));

			it('still opens on the gate’s first file', async () => {
				expect((await nested()).current).toBe('z.png');
			});

			it('moves along the files as the tree shows them', async () => {
				const set = await nested();
				set.step(-1);
				expect(set.current).toBe('docs/b.png');
				set.step(-1);
				expect(set.current).toBe('docs/a.png');
				set.step(-1);
				expect(set.current).toBe('docs/a.png');
			});
		});
	});
});
