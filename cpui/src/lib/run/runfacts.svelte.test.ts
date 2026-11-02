import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Run, RunState } from '$lib/domain';
import { aRun } from '$test/fixtures';
import { settled } from '$test/fixtures/run-components';
import { deferred } from '$test/helpers';
import { runFacts, stageStatus, stageType } from './runfacts.svelte';

vi.mock('$lib/api', () => ({
	api: { flows: { version: vi.fn() } }
}));

// The flow-document cache lives in the module for the page's lifetime, so
// every test takes its own run id or flow hash.
let next = 0;
const fresh = (over: Partial<Run> = {}) => aRun({ id: `run-${next++}`, ...over });

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
	const AT = '2026-10-31T11:00:00Z';
	const withFacts = (currentStage?: string) =>
		aRun({
			currentStage,
			facts: {
				costUsd: 0.75,
				turns: 5,
				wallMs: 60_000,
				attempts: 4,
				open: { build: { iteration: 3, startedAt: AT, outcome: 'running' } }
			}
		});

	it('reads spend and the current stage’s open attempt from the run', () => {
		expect(runFacts(withFacts('build'))).toEqual({
			cost: 0.75,
			turns: 5,
			attempt: 3,
			stageStartedAt: AT
		});
	});

	it('has no attempt when the current stage has none open, or there is no stage', () => {
		expect(runFacts(withFacts('review'))).toEqual({
			cost: 0.75,
			turns: 5,
			attempt: undefined,
			stageStartedAt: undefined
		});
		expect(runFacts(withFacts())).toMatchObject({ cost: 0.75, attempt: undefined });
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
