// What the run's views read, beyond ../fixtures. The store here is the real
// RunStore: the views bind to its runes (`bind:value={rs.egressRule}`) and read
// its deriveds (terminal, hasWorkspace, pings), so a plain stand-in would not
// re-render and would have to restate those rules.
import type {
	AskMessage,
	AskState,
	Attachment,
	EgressAuditRow,
	EgressPolicy,
	ResourceSample,
	Run,
	RunEvent,
	RunPhase,
	ServiceStatus,
	Snapshot
} from '$lib/domain';
import { RunStore } from '$lib/run/store.svelte';
import { aRun } from '$test/fixtures';

/** The clock every fixture and every store starts on. */
export const AT = '2026-10-31T12:00:00Z';
export const NOW = Date.parse(AT);
const RUN_ID = '01TESTRUN0000000000000ABCD';

type Settable = Pick<
	RunStore,
	| 'events'
	| 'stageDefs'
	| 'exposed'
	| 'observed'
	| 'snapshots'
	| 'attachments'
	| 'samples'
	| 'services'
	| 'servicesError'
	| 'now'
	| 'answering'
	| 'releasing'
	| 'logSource'
	| 'egressRule'
	| 'projectName'
	| 'phasesNudgeDismissedAt'
>;

/** A real store on a run, with the given feeds already landed. */
export function aViewStore(run: Run | null = aRun(), over: Partial<Settable> = {}): RunStore {
	const rs = new RunStore(run?.id ?? RUN_ID);
	rs.run = run;
	rs.now = NOW;
	Object.assign(rs, over);
	return rs;
}

/** A run with a live workspace behind it. */
export const aLiveRun = (over: Partial<Run> = {}): Run =>
	aRun({ envId: 'env-1', envStatus: 'running', ...over });

/** One entry of the log; its timestamp counts seconds from AT by seq. */
export const anEvent = (
	seq: number,
	type: string,
	payload: Record<string, unknown> = {}
): RunEvent => ({
	runId: RUN_ID,
	seq,
	ts: new Date(NOW + seq * 1000).toISOString(),
	type,
	payload
});

export const aRaise = (seq: number, id: string, payload: Record<string, unknown> = {}) =>
	anEvent(seq, 'ping_raised', {
		ping_id: id,
		type: 'gate',
		answer_kind: 'yes_no',
		question: `Question ${id}?`,
		...payload
	});

export const anAnswer = (seq: number, id: string, payload: Record<string, unknown> = {}) =>
	anEvent(seq, 'ping_answered', { ping_id: id, answer: 'approve', ...payload });

export const anAskMessage = (over: Partial<AskMessage> = {}): AskMessage => ({
	seq: 1,
	id: 'm1',
	role: 'question',
	text: 'Where is the retry?',
	costUsd: 0,
	turns: 0,
	at: AT,
	...over
});

export const anAskState = (over: Partial<AskState> = {}): AskState => ({
	enabled: true,
	harness: 'acme',
	model: 'acme/one',
	models: [],
	busy: false,
	messages: [],
	...over
});

export const anAuditRow = (over: Partial<EgressAuditRow> = {}): EgressAuditRow => ({
	project: 'dev',
	run: RUN_ID,
	host: 'api.test',
	dstIp: '10.0.0.1',
	port: 443,
	proto: 'tls',
	verdict: 'allow',
	count: 3,
	bytesOut: 2048,
	bytesIn: 4096,
	firstSeen: AT,
	lastSeen: AT,
	...over
});

export const aPolicy = (over: Partial<EgressPolicy> = {}): EgressPolicy => ({
	mode: 'allow',
	domains: [],
	blockedDomains: [],
	...over
});

export const aService = (over: Partial<ServiceStatus> = {}): ServiceStatus => ({
	name: 'app',
	state: 'healthy',
	healthy: true,
	lastProbeOk: false,
	ports: [],
	...over
});

export const aSnapshot = (over: Partial<Snapshot> = {}): Snapshot => ({
	id: 'snap-1',
	runId: RUN_ID,
	kind: 'output',
	stageId: 'build',
	iteration: 2,
	name: 'plan',
	filename: 'plan.md',
	mime: 'text/markdown',
	size: 2048,
	sha256: 'abc',
	truncated: false,
	createdAt: AT,
	...over
});

export const anAttachment = (over: Partial<Attachment> = {}): Attachment => ({
	id: 'att-1',
	filename: 'brief.pdf',
	mime: 'application/pdf',
	size: 1024,
	sha256: 'def',
	createdAt: AT,
	...over
});

export const aSample = (over: Partial<ResourceSample> = {}): ResourceSample => ({
	at: AT,
	cpuPerc: 50,
	cpus: 4,
	memUsed: 1024 * 1024,
	memLimit: 4 * 1024 * 1024,
	netRx: 0,
	netTx: 0,
	pids: 7,
	...over
});

export const aPhase = (over: Partial<RunPhase> = {}): RunPhase => ({
	id: 'fix-ci',
	kind: 'watcher',
	signal: 'manual',
	start: 'manual',
	max: -1,
	runs: {},
	total: 0,
	holds: [],
	...over
});
