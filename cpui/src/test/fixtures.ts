// Domain objects for tests: valid and boring, so a test states only what it
// is about. `aRun({ state: 'failed' })` reads as the scenario.
import { vi } from 'vitest';
import type { Ping, Project, Run, Ticket, TicketDetail, TicketRow } from '$lib/domain';
import type { RunStore } from '$lib/run/store.svelte';
import { fleet } from '$lib/app/fleet.svelte';

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
		choices: [],
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
			agentsMdPath: '',
			branchFormat: ''
		},
		repos: [],
		commands: {},
		defaultFlow: 'builder',
		ticketSourceId: '01TESTSRC0000000000000ABCD',
		ownerId: 'u1',
		access: { role: 'owner', actions: [...PROJECT_ACTIONS] },
		createdAt: AT,
		updatedAt: AT,
		...over
	}) as Project;

/** Every action a workshop role can hold: what its owner has. */
export const PROJECT_ACTIONS = [
	'project:read',
	'project:write',
	'project:members',
	'project:delete',
	'run:read',
	'run:operate',
	'run:shell',
	'ticket:read',
	'ticket:write'
] as const;

/** A workshop shared with the caller as a viewer: it may look, nothing more. */
export const aViewerProject = (over: Partial<Project> = {}): Project =>
	aProject({
		ownerId: 'u2',
		ownerHandle: 'ada',
		access: { role: 'viewer', actions: ['project:read', 'run:read', 'ticket:read'] },
		...over
	});

/** One issue of a workshop's source as its list shows it, with a record. */
export const aTicketRow = (over: Partial<TicketRow> = {}): TicketRow => ({
	key: 'PROJ-7',
	remoteId: '10007',
	title: 'Add idempotency keys',
	body: 'Make the webhook safe to retry.',
	url: 'https://tracker.test/browse/PROJ-7',
	status: 'To Do',
	statusId: 's-todo',
	done: false,
	type: 'Story',
	typeId: 't-story',
	changeType: 'feat',
	labels: [],
	assignee: '',
	updatedAt: AT,
	ticketId: '01TESTTICK000000000000ABCD',
	baseBranch: 'feat/idempotency',
	activeRunId: '',
	...over
});

/** One ticket as its page reads it: a remote issue unless told otherwise. */
export const aTicketDetail = (over: Partial<TicketDetail> = {}): TicketDetail => ({
	...aTicketRow(),
	source: { id: '01TESTSRC0000000000000ABCD', kind: 'jira', name: 'Jira' },
	internal: false,
	attachmentIds: [],
	links: [],
	...over
});

/** A ticket's local record: what runs point at. */
export const aTicket = (over: Partial<Ticket> = {}): Ticket => ({
	id: '01TESTTICK000000000000ABCD',
	sourceId: '01TESTSRC0000000000000ABCD',
	sourceKind: 'jira',
	remoteId: '10007',
	sourceKey: 'PROJ-7',
	title: 'Add idempotency keys',
	goal: 'Make the webhook safe to retry.',
	baseBranch: 'feat/idempotency',
	changeType: 'feat',
	labels: [],
	createdAt: AT,
	updatedAt: AT,
	...over
});

/** The slice of RunStore the run's components read. `act` runs what it's given
 *  and answers like the real one: false on a failure. */
export const aRunStore = (run: Run = aRun(), over: Partial<RunStore> = {}): RunStore => {
	const terminal = ['completed', 'failed', 'abandoned'].includes(run.state);
	return {
		runId: run.id,
		run,
		error: '',
		events: [],
		links: [],
		terminal,
		hasWorkspace: !!run.envId,
		canRebuild: !!run.envId && !run.archivedAt && (terminal || run.state === 'paused'),
		canPause: !terminal && run.state !== 'paused' && run.state !== 'pending',
		get canOperate() {
			return fleet.can(run.projectId, 'run:operate');
		},
		get canShell() {
			return fleet.can(run.projectId, 'run:shell');
		},
		act: vi.fn(async (fn: () => Promise<unknown>) => {
			try {
				await fn();
				return true;
			} catch {
				return false;
			}
		}),
		...over
	} as unknown as RunStore;
};
