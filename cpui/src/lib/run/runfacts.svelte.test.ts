import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Run, RunEvent, RunState } from '$lib/domain';
import { aRun } from '$test/fixtures';
import { settled } from '$test/fixtures/run-components';
import { anEvent } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import { runFacts, stageStatus, stageType } from './runfacts.svelte';

vi.mock('$lib/api', () => ({
	api: { runs: { get: vi.fn() }, flows: { version: vi.fn() } }
}));

// The caches live in the module for the page's lifetime, so every test takes
// its own run id or flow hash.
let next = 0;
const fresh = (over: Partial<Run> = {}) => aRun({ id: `run-${next++}`, ...over });
const logOf = (events: RunEvent[]) =>
	vi.mocked(api.runs.get).mockResolvedValue({ run: aRun(), events });

const RECIPE = `name: f
version: 5
main:
  - { id: plan, type: agent }
  - { id: seal, type: human_gate }
  - { id: check, sequence: qa }
sequences:
  - id: qa
    stages:
      - { id: lint, type: command }
`;

beforeEach(() => {
	vi.resetAllMocks();
});

describe('runFacts', () => {
	/** Reads once to schedule the fetch, then again once it landed. */
	async function facts(run: Run) {
		runFacts(run);
		await settled();
		return runFacts(run);
	}

	it('is undefined until the fetch lands', () => {
		logOf([]);
		expect(runFacts(fresh())).toBeUndefined();
	});

	it('adds up what every finished and paused leg spent', async () => {
		logOf([
			anEvent(1, 'stage_completed', { usage: { cost_usd: 0.5 }, turns_used: 3 }),
			anEvent(2, 'stage_paused', { usage: { cost_usd: 0.25 }, turns_used: 2 }),
			anEvent(3, 'stage_failed', { usage: { cost_usd: 9 }, turns_used: 9 })
		]);
		expect(await facts(fresh())).toMatchObject({ cost: 0.75, turns: 5 });
	});

	it("takes the attempt from the current stage's latest start", async () => {
		const latest = anEvent(3, 'stage_started', { stage_id: 'build', iteration: 2 });
		logOf([
			anEvent(1, 'stage_started', { stage_id: 'build', iteration: 1 }),
			anEvent(2, 'stage_started', { stage_id: 'other', iteration: 7 }),
			latest
		]);
		expect(await facts(fresh({ currentStage: 'build' }))).toMatchObject({
			attempt: 2,
			stageStartedAt: latest.ts
		});
	});

	it('leaves the attempt unknown with no current stage on the log', async () => {
		logOf([anEvent(1, 'stage_started', { stage_id: 'other', iteration: 2 })]);
		const f = await facts(fresh({ currentStage: 'build' }));
		expect(f?.attempt).toBeUndefined();
		expect(f?.stageStartedAt).toBeUndefined();
	});

	it('keeps the attempt it has when a start names none', async () => {
		logOf([
			anEvent(1, 'stage_started', { stage_id: 'build', iteration: 2 }),
			anEvent(2, 'stage_started', { stage_id: 'build' })
		]);
		expect((await facts(fresh({ currentStage: 'build' })))?.attempt).toBe(2);
	});

	it('fetches once however often it is read after that', async () => {
		logOf([]);
		const run = fresh();
		await facts(run);
		runFacts(run);
		await settled();
		expect(api.runs.get).toHaveBeenCalledOnce();
	});

	it('fetches once when two cells read it in the same tick', async () => {
		logOf([]);
		const run = fresh();
		runFacts(run);
		runFacts(run);
		await settled();
		expect(api.runs.get).toHaveBeenCalledOnce();
	});

	it('does not stack a second fetch on one in flight', async () => {
		const job = deferred<{ run: Run; events: RunEvent[] }>();
		vi.mocked(api.runs.get).mockReturnValue(job.promise);
		const run = fresh();
		runFacts(run);
		await settled();
		runFacts(run);
		await settled();
		expect(api.runs.get).toHaveBeenCalledOnce();
		job.resolve({ run, events: [] });
		await settled();
	});

	it('refetches when the run moves on', async () => {
		logOf([]);
		const run = fresh({ updatedAt: '2026-10-31T12:00:00Z' });
		await facts(run);

		logOf([anEvent(1, 'stage_completed', { usage: { cost_usd: 1 } })]);
		const moved = { ...run, updatedAt: '2026-10-31T12:05:00Z' };
		runFacts(moved);
		await settled();

		expect(api.runs.get).toHaveBeenCalledTimes(2);
		expect(runFacts(moved)?.cost).toBe(1);
	});

	it('shows the last facts while the fresh ones load', async () => {
		logOf([anEvent(1, 'stage_completed', { usage: { cost_usd: 1 } })]);
		const run = fresh();
		await facts(run);
		const moved = { ...run, updatedAt: '2026-10-31T12:05:00Z' };
		expect(runFacts(moved)?.cost).toBe(1);
		await settled();
	});

	it('does not retry a failed fetch until the run moves', async () => {
		vi.mocked(api.runs.get).mockRejectedValue(new Error('down'));
		const run = fresh();
		const f = await facts(run);
		expect(f).toBeDefined();
		expect(f?.attempt).toBeUndefined();
		await settled();
		expect(api.runs.get).toHaveBeenCalledOnce();
	});

	it('keeps what was spent, and drops the attempt, when a refetch fails', async () => {
		logOf([
			anEvent(1, 'stage_started', { stage_id: 'build', iteration: 3 }),
			anEvent(2, 'stage_completed', { usage: { cost_usd: 1 }, turns_used: 4 })
		]);
		const run = fresh({ currentStage: 'build', updatedAt: '2026-10-31T12:00:00Z' });
		await facts(run);

		vi.mocked(api.runs.get).mockRejectedValue(new Error('down'));
		const moved = { ...run, updatedAt: '2026-10-31T12:05:00Z' };
		const f = await facts(moved);

		expect(f).toMatchObject({ cost: 1, turns: 4 });
		expect(f?.attempt).toBeUndefined();
	});
});

describe('stageType', () => {
	let hash = 0;
	const pinned = (currentStage?: string) =>
		fresh({ currentStage, projectId: 'proj', flowHash: `hash-${hash++}` });
	const recipe = (yaml = RECIPE) =>
		vi.mocked(api.flows.version).mockResolvedValue({ yaml } as never);

	async function type(run: Run) {
		stageType(run);
		await settled();
		return stageType(run);
	}

	it.each([
		['plan', 'agent'],
		['seal', 'human_gate'],
		['check.lint', 'command'],
		['gone', undefined]
	])('reads %s from the flow document', async (stage, want) => {
		recipe();
		expect(await type(pinned(stage))).toBe(want);
	});

	it.each([
		['no current stage', { currentStage: undefined, projectId: 'p', flowHash: 'h' }],
		['no project', { currentStage: 's', projectId: undefined, flowHash: 'h' }],
		['no flow hash', { currentStage: 's', projectId: 'p', flowHash: '' }]
	])('asks nothing with %s', async (_, over) => {
		expect(await type(fresh(over))).toBeUndefined();
		expect(api.flows.version).not.toHaveBeenCalled();
	});

	it('is undefined until the document lands', () => {
		recipe();
		expect(stageType(pinned('plan'))).toBeUndefined();
	});

	it('reads a flow version once, for every run on it', async () => {
		recipe();
		const run = pinned('plan');
		await type(run);
		await type({ ...run, id: 'another-run', currentStage: 'seal' });
		expect(api.flows.version).toHaveBeenCalledOnce();
		expect(api.flows.version).toHaveBeenCalledWith('proj', run.flowHash);
	});

	it('reads a flow version once when two runs ask in the same tick', async () => {
		recipe();
		const run = pinned('plan');
		stageType(run);
		stageType({ ...run, id: 'another-run' });
		await settled();
		expect(api.flows.version).toHaveBeenCalledOnce();
	});

	it('does not stack a second read on one in flight', async () => {
		const job = deferred<never>();
		vi.mocked(api.flows.version).mockReturnValue(job.promise);
		const run = pinned('plan');
		stageType(run);
		await settled();
		stageType(run);
		await settled();
		expect(api.flows.version).toHaveBeenCalledOnce();
		job.reject(new Error('stop'));
		await settled();
	});

	it.each([
		['will not fetch', () => vi.mocked(api.flows.version).mockRejectedValue(new Error('gone'))],
		['will not parse', () => recipe('main: [')]
	])('stays unknown, without retrying, on a document that %s', async (_, arrange) => {
		arrange();
		const run = pinned('plan');
		expect(await type(run)).toBeUndefined();
		await settled();
		expect(api.flows.version).toHaveBeenCalledOnce();
	});
});

describe('stageStatus', () => {
	it.each<[RunState, string]>([
		['running', 'running'],
		['provisioning', 'pending'],
		['pending', 'pending'],
		['waiting_on_human', 'waiting'],
		['failed', 'failed'],
		['completed', 'done'],
		['paused', 'paused'],
		['abandoned', 'paused'],
		['archived', 'paused']
	])('shows a %s run as %s', (state, want) => {
		expect(stageStatus(state)).toBe(want);
	});
});
