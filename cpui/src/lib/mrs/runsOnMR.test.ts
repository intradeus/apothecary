import { describe, expect, it } from 'vitest';
import { aRun } from '$test/fixtures';
import { runsOnMR } from './runsOnMR';

const mr = (iid: number, repo = 'repo') => ({ repo, branch: 'b', url: `u${iid}`, iid });

describe('runsOnMR', () => {
	const started = aRun({
		id: 'started',
		projectId: 'p1',
		createdAt: '2026-11-01T00:00:00Z',
		adopted: { repo: 'repo', iid: 4, url: 'u4' }
	});
	const opened = aRun({
		id: 'opened',
		projectId: 'p1',
		createdAt: '2026-11-02T00:00:00Z',
		mrs: [mr(4)]
	});
	const elsewhere = aRun({ id: 'other-project', projectId: 'p2', mrs: [mr(4)] });
	const otherRepo = aRun({ id: 'docs', projectId: 'p1', mrs: [mr(4, 'docs')] });
	const unrelated = aRun({ id: 'none', projectId: 'p1', mrs: [mr(5)] });
	const all = [started, opened, elsewhere, otherRepo, unrelated];

	it('finds the runs started on it and the one that opened it, newest first', () => {
		expect(runsOnMR(all, 'p1', 4).map((r) => r.id)).toEqual(['opened', 'started']);
	});

	it('keeps to the repository asked for', () => {
		expect(runsOnMR(all, 'p1', 4, 'docs').map((r) => r.id)).toEqual(['docs']);
		expect(runsOnMR(all, 'p1', 4, 'repo').map((r) => r.id)).toEqual(['opened', 'started']);
	});

	it('reads a run without a repo name as the primary', () => {
		const legacy = aRun({ projectId: 'p1', mrs: [{ ...mr(7), repo: '' }] });
		const adopted = aRun({ projectId: 'p1', adopted: { repo: '', iid: 7, url: 'u' } });
		expect(runsOnMR([legacy, adopted], 'p1', 7)).toHaveLength(2);
	});
});
