// Fixtures for the run page's components: what src/test/fixtures.ts does not
// carry. Valid and boring, so a test states only what it is about.
import { tick } from 'svelte';
import { SvelteMap } from 'svelte/reactivity';
import type {
	AgentToolCall,
	Flow,
	MergeRequestLive,
	OpenMergeRequest,
	ParamDecl,
	ResourceSample,
	RunHold,
	RunPhase
} from '$lib/domain';

const AT = '2026-10-31T12:00:00Z';

export const aHold = (over: Partial<RunHold> = {}): RunHold => ({
	stageId: 'build',
	since: AT,
	turns: 0,
	messages: 0,
	costUsd: 0,
	...over
});

export const aToolCall = (over: Partial<AgentToolCall> = {}): AgentToolCall => ({
	toolCallId: 'call-1',
	title: 'Read the router',
	kind: 'read',
	status: 'completed',
	locations: [],
	diffs: [],
	...over
});

export const aLiveMr = (over: Partial<MergeRequestLive> = {}): MergeRequestLive => ({
	repo: 'repo',
	branch: 'feat/idempotency',
	url: 'https://git.test/dev/repo/-/merge_requests/20',
	state: 'open',
	mergeable: 'clean',
	target: 'main',
	pendingThreads: 0,
	mergeableNow: false,
	draft: false,
	...over
});

export const aPhase = (over: Partial<RunPhase> = {}): RunPhase => ({
	id: 'fix-ci',
	kind: 'watcher',
	signal: 'ci_failed',
	start: 'auto',
	max: 3,
	runs: {},
	total: 0,
	holds: [],
	...over
});

export const aSample = (over: Partial<ResourceSample> = {}): ResourceSample => ({
	at: AT,
	cpuPerc: 0,
	cpus: 0,
	memUsed: 0,
	memLimit: 0,
	netRx: 0,
	netTx: 0,
	pids: 0,
	...over
});

export const aParam = (over: Partial<ParamDecl> = {}): ParamDecl => ({
	name: 'depth',
	type: 'string',
	default: 'shallow',
	known: false,
	...over
});

export const aFlow = (over: Partial<Flow> = {}): Flow => ({
	projectId: '01TESTPROJ000000000000ABCD',
	name: 'builder',
	yaml: '',
	hash: 'e7c90c12',
	updatedAt: AT,
	builtin: false,
	shadowsBuiltin: false,
	parameters: [],
	phases: [],
	...over
});

export const anOpenMr = (over: Partial<OpenMergeRequest> = {}): OpenMergeRequest => ({
	iid: 20,
	url: 'https://git.test/dev/repo/-/merge_requests/20',
	title: 'Add idempotency keys',
	author: 'ada',
	source: 'feat/idempotency',
	target: 'main',
	draft: false,
	fork: false,
	body: '',
	state: 'open',
	...over
});

/** Lets every resolved promise run its course, for a test that something did
 *  NOT happen after one settled. */
export async function settled() {
	for (let i = 0; i < 20; i++) await Promise.resolve();
	await tick();
}

/** Props as a parent's `bind:` holds them: a component's writes land here and
 *  re-render it, and a test reads or sets them directly. Pass it as
 *  `render(Component, { props })`. Each key is one accessor, as Svelte expects
 *  of a binding. */
export function bound<P extends object>(initial: P): P {
	const values = new SvelteMap<string, unknown>(Object.entries(initial));
	const props = {};
	for (const key of Object.keys(initial))
		Object.defineProperty(props, key, {
			enumerable: true,
			configurable: true,
			get: () => values.get(key),
			set: (value: unknown) => values.set(key, value)
		});
	return props as P;
}
