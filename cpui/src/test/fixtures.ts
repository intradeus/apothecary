// Domain objects for tests: valid and boring, so a test states only what it
// is about. `aRun({ state: 'failed' })` reads as the scenario.
import { vi } from 'vitest';
import type { Ping, Project, Run, Ticket } from '$lib/domain';
import type { RunStore } from '$lib/run/store.svelte';

const AT = '2026-10-31T12:00:00Z';

export const aRun = (over: Partial<Run> = {}): Run => ({
	id: '01TESTRUN0000000000000ABCD',
	state: 'running',
	title: 'Add idempotency keys',
	flowName: 'builder',
	flowHash: 'e7c90c12',
	mrs: [],
	checkouts: [],
	idle: false,
	envDirty: false,
	provisionFailed: false,
	createdAt: AT,
	updatedAt: AT,
	facts: { costUsd: 0, turns: 0, wallMs: 0, attempts: 0, open: {} },
	...over
});

export const aPing = (over: Partial<Ping> = {}): Ping =>
	({
		id: '01TESTPING000000000000ABCD',
		runId: '01TESTRUN0000000000000ABCD',
		type: 'gate',
		status: 'pending',
		question: 'Approve the plan?',
		files: [],
		fileTypes: {},
		answerKind: 'yes_no',
		endsPhase: false,
		createdAt: AT,
		...over
	}) as Ping;

export const aProject = (over: Partial<Project> = {}): Project =>
	({
		id: '01TESTPROJ000000000000ABCD',
		name: 'dev',
		repoAccountId: 'acc',
		provisionerId: 'prov',
		harnessIds: ['h1'],
		repo: {
			url: 'https://git.test/dev/repo.git',
			locator: '1',
			defaultBranch: 'main',
			agentsMdPath: ''
		},
		repos: [],
		commands: {},
		defaultFlow: 'builder',
		createdAt: AT,
		updatedAt: AT,
		...over
	}) as Project;

export const aTicket = (over: Partial<Ticket> = {}): Ticket => ({
	id: '01TESTTICK000000000000ABCD',
	projectId: '01TESTPROJ000000000000ABCD',
	title: 'Add idempotency keys',
	goal: 'Make the webhook safe to retry.',
	baseBranch: 'feat/idempotency',
	changeType: 'feat',
	attachmentIds: [],
	labels: [],
	links: [],
	syncState: '',
	warnings: [],
	createdAt: AT,
	updatedAt: AT,
	...over
});

/** The slice of RunStore the run's components read. `act` runs what it's given
 *  and answers like the real one: false on a failure. */
export const aRunStore = (run: Run = aRun(), over: Partial<RunStore> = {}): RunStore =>
	({
		runId: run.id,
		run,
		error: '',
		events: [],
		links: [],
		terminal: ['completed', 'failed', 'abandoned', 'archived'].includes(run.state),
		hasWorkspace: !!run.envId,
		act: vi.fn(async (fn: () => Promise<unknown>) => {
			try {
				await fn();
				return true;
			} catch {
				return false;
			}
		}),
		...over
	}) as unknown as RunStore;
