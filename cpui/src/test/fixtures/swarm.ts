// The war room's domain objects for tests: valid and boring, so a test states
// only what it is about.
import type { CrewPresence, ResourceSample, Swarm, SwarmUnit } from '$lib/domain';

const AT = '2026-10-31T12:00:00Z';

export const aSample = (over: Partial<ResourceSample> = {}): ResourceSample => ({
	at: AT,
	cpuPerc: 100,
	cpus: 4,
	memUsed: 2 ** 30,
	memLimit: 4 * 2 ** 30,
	netRx: 0,
	netTx: 0,
	pids: 12,
	...over
});

export const aCrew = (over: Partial<CrewPresence> = {}): CrewPresence => ({
	stageId: 'build',
	iteration: 1,
	harness: 'omp',
	agent: 'builder',
	since: AT,
	turnActive: true,
	subagents: [],
	...over
});

export const aUnit = (over: Partial<SwarmUnit> = {}): SwarmUnit => ({
	runId: '01TESTRUN0000000000000ABCD',
	projectId: 'p1',
	title: 'Add idempotency keys',
	flowName: 'builder',
	state: 'running',
	currentStage: 'build',
	envStatus: 'running',
	createdAt: AT,
	updatedAt: AT,
	openPings: 0,
	held: false,
	...over
});

export const aSwarm = (units: SwarmUnit[] = [], at = AT): Swarm => ({ at, units });
