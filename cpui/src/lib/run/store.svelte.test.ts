import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import type { EgressDenial, LiveDelta, Run, RunEvent, RunServices } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { aRun, aTicket, aProject } from '$test/fixtures';
import { aToolCall, settled } from '$test/fixtures/run-components';
import { aLiveRun, anAnswer, anEvent, aRaise, NOW } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import { RunStore } from './store.svelte';
import { buildTranscript } from './transcriptItems';

vi.mock('$lib/api', () => ({
	api: {
		runs: {
			get: vi.fn(),
			files: vi.fn(),
			ports: vi.fn(),
			services: vi.fn(),
			resources: vi.fn(),
			events: vi.fn(),
			takeOver: vi.fn(),
			release: vi.fn()
		},
		egress: { denials: vi.fn() },
		projects: { get: vi.fn() },
		tickets: { get: vi.fn() },
		flows: { version: vi.fn(), get: vi.fn() },
		pings: { answer: vi.fn() }
	}
}));
vi.mock('$lib/app/fleet.svelte', () => ({ fleet: { refresh: vi.fn() } }));

const RECIPE = `name: builder
version: 5
main:
  - { id: plan, type: agent, agent: planner }
  - { id: check, sequence: qa }
  - { id: open, type: action, action: create_mr }
sequences:
  - id: qa
    stages:
      - { id: lint, type: command }
`;

const empty = { snapshots: [], attachments: [] };
const noServices: RunServices = { workspace: true, services: [] };
const denial = (host: string, status: EgressDenial['status'] = 'held'): EgressDenial => ({
	host,
	count: 1,
	last: '2026-10-31T12:00:00Z',
	status
});
const usage = (seq: number, cost: number, turns: number) =>
	anEvent(seq, 'stage_completed', {
		stage_id: 'build',
		usage: { cost_usd: cost },
		turns_used: turns
	});

/** A store started on a run, with the stream's two callbacks in hand. */
async function live(run: Run | null = aLiveRun()) {
	if (run) vi.mocked(api.runs.get).mockResolvedValue({ run, events: [] });
	const rs = new RunStore(run?.id ?? 'missing');
	const stop = rs.start();
	await settled();
	const [, , emit, delta] = vi.mocked(api.runs.events).mock.calls[0];
	vi.mocked(api.runs.get).mockClear();
	vi.mocked(api.runs.files).mockClear();
	vi.mocked(api.runs.ports).mockClear();
	vi.mocked(api.runs.services).mockClear();
	vi.mocked(api.egress.denials).mockClear();
	vi.mocked(fleet.refresh).mockClear();
	return { rs, stop, emit, delta: delta as (d: LiveDelta) => void };
}

/** A store with a run in hand and nothing started. */
function store(run: Run | null = aLiveRun()) {
	const rs = new RunStore(run?.id ?? 'missing');
	rs.run = run;
	return rs;
}

beforeEach(() => {
	vi.resetAllMocks();
	toasts.list = [];
	vi.mocked(api.runs.events).mockReturnValue(vi.fn());
	vi.mocked(api.runs.files).mockResolvedValue(empty as never);
	vi.mocked(api.runs.ports).mockResolvedValue({ exposed: [], observed: [] } as never);
	vi.mocked(api.runs.services).mockResolvedValue(noServices);
	vi.mocked(api.runs.resources).mockResolvedValue([]);
	vi.mocked(api.egress.denials).mockResolvedValue([]);
	vi.mocked(api.projects.get).mockResolvedValue(aProject());
	vi.mocked(api.tickets.get).mockResolvedValue(aTicket());
	vi.mocked(api.flows.version).mockResolvedValue({ yaml: RECIPE } as never);
	vi.mocked(api.flows.get).mockResolvedValue({ yaml: RECIPE } as never);
	vi.mocked(fleet.refresh).mockResolvedValue();
});

afterEach(() => {
	vi.useRealTimers();
});

describe('the event stream', () => {
	it('subscribes to this run from the top', async () => {
		const run = aLiveRun();
		const { stop } = await live(run);
		expect(api.runs.events).toHaveBeenCalledOnce();
		expect(vi.mocked(api.runs.events).mock.calls[0].slice(0, 2)).toEqual([run.id, 0]);
		stop();
	});

	it('closes the stream on stop', async () => {
		const close = vi.fn();
		vi.mocked(api.runs.events).mockReturnValue(close);
		const { stop } = await live();
		stop();
		expect(close).toHaveBeenCalledOnce();
	});

	it('keeps the events in the order they arrive', async () => {
		const { rs, stop, emit } = await live();
		const events = [anEvent(1, 'stage_started'), anEvent(2, 'agent_notice'), anEvent(3, 'x')];
		events.forEach(emit);
		expect(rs.events.map((e) => e.seq)).toEqual([1, 2, 3]);
		stop();
	});

	it('folds a stage through a question and back into what the page reads', async () => {
		const { rs, stop, emit } = await live();
		const start = anEvent(1, 'stage_started', { stage_id: 'build', iteration: 1 });

		emit(start);
		expect(rs.current?.seq).toBe(1);
		expect(rs.since).toBe(start.ts);

		emit(aRaise(2, 'p1', { stage_id: 'build', type: 'clarification' }));
		emit(
			anEvent(3, 'stage_paused', {
				stage_id: 'build',
				usage: { cost_usd: 0.25 },
				turns_used: 2
			})
		);
		expect(rs.current).toBeNull();
		expect(rs.since).toBeNull();
		expect(rs.pendingPings.map((p) => p.id)).toEqual(['p1']);
		expect(rs.totals).toEqual({ cost: 0.25, turns: 2 });

		emit(anAnswer(4, 'p1'));
		const resumed = anEvent(5, 'stage_resumed', { stage_id: 'build' });
		emit(resumed);
		expect(rs.current?.seq).toBe(1);
		expect(rs.since).toBe(resumed.ts);
		expect(rs.pendingPings).toEqual([]);
		expect(rs.pings.map((p) => p.status)).toEqual(['answered']);

		emit(usage(6, 0.5, 3));
		expect(rs.current).toBeNull();
		expect(rs.totals).toEqual({ cost: 0.75, turns: 5 });
		stop();
	});

	it('shows the newest pending ping first: each answer reveals the one before', async () => {
		const { rs, stop, emit } = await live();
		[aRaise(1, 'older'), aRaise(2, 'middle'), aRaise(3, 'newest')].forEach(emit);
		expect(rs.pendingPings.map((p) => p.id)).toEqual(['newest', 'middle', 'older']);

		emit(anAnswer(4, 'newest'));
		expect(rs.pendingPings.map((p) => p.id)).toEqual(['middle', 'older']);
		stop();
	});

	it('remembers when the workspace last went away', async () => {
		const { rs, stop, emit } = await live();
		expect(rs.destroyedAt).toBeUndefined();
		const last = anEvent(9, 'env_destroyed');
		[anEvent(2, 'env_destroyed'), last].forEach(emit);
		expect(rs.destroyedAt).toBe(last.ts);
		stop();
	});

	describe('what an event refreshes', () => {
		const each = [
			['run_paused', { run: true, fleet: true }],
			['ping_raised', { run: true, fleet: true }],
			['env_ready', { run: true, fleet: false }],
			['hold_taken', { run: true, fleet: false }],
			['stage_started', { run: false, fleet: false }],
			['agent_message', { run: false, fleet: false }]
		] as const;

		it.each(each)('%s', async (type, want) => {
			const { stop, emit } = await live();
			emit(anEvent(1, type));
			expect(api.runs.get).toHaveBeenCalledTimes(want.run ? 1 : 0);
			expect(fleet.refresh).toHaveBeenCalledTimes(want.fleet ? 1 : 0);
			stop();
		});

		it.each(['snapshot_captured', 'ping_answered'])('re-reads the record on %s', async (type) => {
			const { stop, emit } = await live();
			emit(anEvent(1, type));
			expect(api.runs.files).toHaveBeenCalledOnce();
			stop();
		});

		it('leaves the record alone otherwise', async () => {
			const { stop, emit } = await live();
			emit(anEvent(1, 'ping_raised'));
			expect(api.runs.files).not.toHaveBeenCalled();
			stop();
		});

		it.each(['service_started', 'service_ready', 'service_stopped', 'service_failed'])(
			're-reads the services on %s',
			async (type) => {
				const { stop, emit } = await live();
				emit(anEvent(1, type));
				expect(api.runs.services).toHaveBeenCalledOnce();
				stop();
			}
		);

		it('clears the hand-back once the engine records it', async () => {
			const { rs, stop, emit } = await live();
			rs.releasing = true;
			emit(anEvent(1, 'hold_released'));
			expect(rs.releasing).toBe(false);
			stop();
		});
	});

	describe('live deltas', () => {
		const said = (segment: string, text: string) =>
			({ kind: 'message', segment, text }) as LiveDelta;

		it('folds into the live state', async () => {
			const { rs, stop, delta } = await live();
			delta(said('s1', 'Hel'));
			delta(said('s1', 'Hello'));
			expect(rs.live.messages.get('s1')?.text).toBe('Hello');
			expect(rs.events).toEqual([]);
			stop();
		});

		it('gives way to the persisted event', async () => {
			const { rs, stop, emit, delta } = await live();
			delta(said('s1', 'Hello'));
			emit(anEvent(1, 'agent_message', { segment: 's1', text: 'Hello' }));
			expect(rs.live.messages.size).toBe(0);
			stop();
		});

		it('is dropped when the attempt ends', async () => {
			const { rs, stop, emit, delta } = await live();
			delta(said('s1', 'Half a thou'));
			emit(anEvent(1, 'stage_failed', { stage_id: 'build' }));
			expect(rs.live.order).toEqual([]);
			stop();
		});
	});

	// A dropped connection: the browser reconnects with Last-Event-ID, and the
	// server resumes after the last event the store got. Deltas carry no id, so
	// nothing replays them and whatever was on screen has to settle by itself.
	describe('across a reconnect', () => {
		const said = (segment: string, text: string) =>
			({ kind: 'message', segment, text }) as LiveDelta;

		it('carries on from where the stream resumes', async () => {
			const { rs, stop, emit } = await live();
			[usage(1, 0.25, 1), usage(2, 0.25, 1)].forEach(emit);
			[usage(3, 0.5, 2), usage(4, 0.5, 2)].forEach(emit);
			expect(rs.events.map((e) => e.seq)).toEqual([1, 2, 3, 4]);
			expect(rs.totals).toEqual({ cost: 1.5, turns: 6 });
			stop();
		});

		it('settles a message left half written when its event replays', async () => {
			const { rs, stop, emit, delta } = await live();
			delta(said('s1', 'Half a'));
			emit(anEvent(1, 'agent_message', { segment: 's1', text: 'Half a sentence, finished.' }));

			const chat = buildTranscript(rs.events, rs.live);
			expect(chat).toHaveLength(1);
			expect(chat[0]).toMatchObject({ text: 'Half a sentence, finished.', live: false });
			stop();
		});

		it('clears what was in flight when the replay shows the attempt ended', async () => {
			const { rs, stop, emit, delta } = await live();
			delta(said('s1', 'Half a'));
			delta({ kind: 'tool', tool: aToolCall({ toolCallId: 't1' }) } as LiveDelta);
			emit(anEvent(1, 'stage_completed', { stage_id: 'build' }));
			expect(rs.live.order).toEqual([]);
			stop();
		});

		it('shows a delta landing after its persisted event only once', async () => {
			const { rs, stop, emit, delta } = await live();
			emit(anEvent(1, 'agent_message', { segment: 's1', text: 'Final' }));
			delta(said('s1', 'Fin'));
			expect(buildTranscript(rs.events, rs.live).map((i) => i.key)).toEqual(['e1']);
			stop();
		});

		it('refreshes the header from the first replayed event', async () => {
			const { stop, emit } = await live();
			emit(anEvent(5, 'run_completed'));
			expect(api.runs.get).toHaveBeenCalledOnce();
			stop();
		});

		// The server does not replay a seq (Watch filters them, Last-Event-ID
		// resumes), but anything that loses that header would double the money.
		it('counts an event delivered twice only once', async () => {
			const { rs, stop, emit } = await live();
			emit(usage(1, 0.5, 2));
			emit(usage(1, 0.5, 2));
			expect(rs.totals).toEqual({ cost: 0.5, turns: 2 });
			stop();
		});

		it('takes what follows a replay of the log', async () => {
			const { rs, stop, emit } = await live();
			emit(usage(1, 0.5, 2));
			emit(usage(2, 0.25, 1));
			emit(usage(1, 0.5, 2));
			emit(usage(2, 0.25, 1));
			emit(usage(3, 1, 1));
			expect(rs.events.map((e) => e.seq)).toEqual([1, 2, 3]);
			stop();
		});

		it('shows a ping delivered twice only once', async () => {
			const { rs, stop, emit } = await live();
			emit(aRaise(1, 'p1'));
			emit(aRaise(1, 'p1'));
			expect(rs.pendingPings).toHaveLength(1);
			stop();
		});
	});
});

describe('start', () => {
	it('loads what the page shows around the run', async () => {
		const run = aLiveRun({ projectId: 'proj', ticketId: 'tick' });
		vi.mocked(api.projects.get).mockResolvedValue(aProject({ name: 'dev shop' }));
		const ticket = aTicket({ title: 'The ticket' });
		vi.mocked(api.tickets.get).mockResolvedValue(ticket);
		const { rs, stop } = await live(run);
		expect(rs.run).toEqual(run);
		expect(rs.projectName).toBe('dev shop');
		expect(rs.ticket).toEqual(ticket);
		expect(rs.stageDefs?.length).toBeGreaterThan(0);
		stop();
	});

	it('asks a live workspace what it holds', async () => {
		vi.mocked(api.runs.get).mockResolvedValue({ run: aLiveRun(), events: [] });
		const rs = new RunStore(aLiveRun().id);
		const stop = rs.start();
		await settled();
		expect(api.egress.denials).toHaveBeenCalledOnce();
		expect(api.runs.ports).toHaveBeenCalledOnce();
		expect(api.runs.services).toHaveBeenCalledOnce();
		stop();
	});

	it('does not ask a run with no workspace', async () => {
		vi.mocked(api.runs.get).mockResolvedValue({ run: aRun(), events: [] });
		const rs = new RunStore(aRun().id);
		const stop = rs.start();
		await settled();
		expect(api.egress.denials).not.toHaveBeenCalled();
		expect(api.runs.ports).not.toHaveBeenCalled();
		expect(api.runs.services).not.toHaveBeenCalled();
		stop();
	});

	it('reads the record whether or not the run loaded', async () => {
		vi.mocked(api.runs.get).mockRejectedValue(new Error('down'));
		const rs = new RunStore('missing');
		const stop = rs.start();
		await settled();
		expect(api.runs.files).toHaveBeenCalledOnce();
		stop();
	});

	it('shows why a run could not load and fetches nothing that hangs off it', async () => {
		vi.mocked(api.runs.get).mockRejectedValue(new Error('no such run'));
		const rs = new RunStore('missing');
		const stop = rs.start();
		await settled();
		expect(rs.loadError).toBe('no such run');
		expect(api.projects.get).not.toHaveBeenCalled();
		expect(api.flows.version).not.toHaveBeenCalled();
		stop();
	});

	it('survives a project or ticket it cannot read', async () => {
		vi.mocked(api.projects.get).mockRejectedValue(new Error('gone'));
		vi.mocked(api.tickets.get).mockRejectedValue(new Error('gone'));
		const { rs, stop } = await live(aLiveRun({ projectId: 'proj', ticketId: 'tick' }));
		expect(rs.projectName).toBe('');
		expect(rs.ticket).toBeNull();
		expect(rs.loadError).toBe('');
		stop();
	});

	it('skips what the run does not point at', async () => {
		const { stop } = await live(aLiveRun());
		expect(api.projects.get).not.toHaveBeenCalled();
		expect(api.tickets.get).not.toHaveBeenCalled();
		stop();
	});
});

describe('polling', () => {
	const hidden = () => vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);

	beforeEach(() => {
		vi.useFakeTimers();
		vi.setSystemTime(NOW);
	});

	it('ticks the clock every second', async () => {
		const { rs, stop } = await live();
		await vi.advanceTimersByTimeAsync(3000);
		expect(rs.now).toBe(NOW + 3000);
		stop();
	});

	it('re-reads the run every fifteen seconds', async () => {
		const { stop } = await live();
		await vi.advanceTimersByTimeAsync(14_999);
		expect(api.runs.get).not.toHaveBeenCalled();
		await vi.advanceTimersByTimeAsync(1);
		expect(api.runs.get).toHaveBeenCalledOnce();
		stop();
	});

	it('watches the denials of a live workspace', async () => {
		const { stop } = await live();
		await vi.advanceTimersByTimeAsync(5000);
		expect(api.egress.denials).toHaveBeenCalledOnce();
		stop();
	});

	it('reads the ports only while something holds them', async () => {
		const { rs, stop } = await live();
		await vi.advanceTimersByTimeAsync(5000);
		expect(api.runs.ports).not.toHaveBeenCalled();

		const release = rs.usePorts();
		vi.mocked(api.runs.ports).mockClear();
		await vi.advanceTimersByTimeAsync(5000);
		expect(api.runs.ports).toHaveBeenCalledOnce();

		release();
		vi.mocked(api.runs.ports).mockClear();
		await vi.advanceTimersByTimeAsync(5000);
		expect(api.runs.ports).not.toHaveBeenCalled();
		stop();
	});

	it('keeps the ports feed while one of two holders remains', async () => {
		const { rs, stop } = await live();
		const first = rs.usePorts();
		rs.usePorts();
		first();
		vi.mocked(api.runs.ports).mockClear();
		await vi.advanceTimersByTimeAsync(5000);
		expect(api.runs.ports).toHaveBeenCalledOnce();
		stop();
	});

	it.each([
		['a finished run', aLiveRun({ state: 'completed' })],
		['a run with no workspace', aRun()]
	])('stops watching denials and ports on %s', async (_, run) => {
		const { rs, stop } = await live(run);
		rs.usePorts();
		vi.mocked(api.runs.ports).mockClear();
		await vi.advanceTimersByTimeAsync(5000);
		expect(api.egress.denials).not.toHaveBeenCalled();
		expect(api.runs.ports).not.toHaveBeenCalled();
		stop();
	});

	// The reaper takes a finished run's workspace days later; until then a
	// service in it can still go down with no event to say so.
	it('keeps reading the services of a finished run while its workspace stands', async () => {
		const { rs, stop } = await live(aLiveRun({ state: 'completed' }));
		rs.useServices();
		vi.mocked(api.runs.services).mockClear();
		await vi.advanceTimersByTimeAsync(3000);
		expect(api.runs.services).toHaveBeenCalledOnce();
		stop();
	});

	it('does not read the services with no workspace', async () => {
		const { rs, stop } = await live(aRun());
		rs.useServices();
		vi.mocked(api.runs.services).mockClear();
		await vi.advanceTimersByTimeAsync(3000);
		expect(api.runs.services).not.toHaveBeenCalled();
		stop();
	});

	it('reads the resources only while something holds them', async () => {
		const { rs, stop } = await live();
		await vi.advanceTimersByTimeAsync(5000);
		expect(api.runs.resources).not.toHaveBeenCalled();

		const release = rs.useResources();
		vi.mocked(api.runs.resources).mockClear();
		await vi.advanceTimersByTimeAsync(5000);
		expect(api.runs.resources).toHaveBeenCalledOnce();

		release();
		vi.mocked(api.runs.resources).mockClear();
		await vi.advanceTimersByTimeAsync(5000);
		expect(api.runs.resources).not.toHaveBeenCalled();
		stop();
	});

	it('reads the services every three seconds while something holds them', async () => {
		const { rs, stop } = await live();
		await vi.advanceTimersByTimeAsync(3000);
		expect(api.runs.services).not.toHaveBeenCalled();

		const release = rs.useServices();
		vi.mocked(api.runs.services).mockClear();
		await vi.advanceTimersByTimeAsync(3000);
		expect(api.runs.services).toHaveBeenCalledOnce();

		release();
		vi.mocked(api.runs.services).mockClear();
		await vi.advanceTimersByTimeAsync(3000);
		expect(api.runs.services).not.toHaveBeenCalled();
		stop();
	});

	it('polls nothing while the tab is hidden', async () => {
		const { rs, stop } = await live();
		rs.usePorts();
		rs.useServices();
		rs.useResources();
		vi.mocked(api.runs.ports).mockClear();
		vi.mocked(api.runs.services).mockClear();
		vi.mocked(api.runs.resources).mockClear();
		hidden();

		await vi.advanceTimersByTimeAsync(15_000);

		expect(api.runs.get).not.toHaveBeenCalled();
		expect(api.egress.denials).not.toHaveBeenCalled();
		expect(api.runs.ports).not.toHaveBeenCalled();
		expect(api.runs.services).not.toHaveBeenCalled();
		expect(api.runs.resources).not.toHaveBeenCalled();
		stop();
	});

	it('stops every timer on stop', async () => {
		const { rs, stop } = await live();
		rs.useResources();
		rs.useServices();
		stop();
		vi.mocked(api.runs.resources).mockClear();
		vi.mocked(api.runs.services).mockClear();

		await vi.advanceTimersByTimeAsync(60_000);

		expect(rs.now).toBe(NOW);
		expect(api.runs.get).not.toHaveBeenCalled();
		expect(api.egress.denials).not.toHaveBeenCalled();
		expect(api.runs.resources).not.toHaveBeenCalled();
		expect(api.runs.services).not.toHaveBeenCalled();
	});
});

describe('feeds', () => {
	it('reads the ports on first hold only with a workspace', () => {
		store(aLiveRun()).usePorts();
		expect(api.runs.ports).toHaveBeenCalledOnce();

		vi.mocked(api.runs.ports).mockClear();
		store(aRun()).usePorts();
		expect(api.runs.ports).not.toHaveBeenCalled();
	});

	it('reads services and resources on first hold, workspace or not', () => {
		const rs = store(aRun());
		rs.useServices();
		rs.useResources();
		expect(api.runs.services).toHaveBeenCalledOnce();
		expect(api.runs.resources).toHaveBeenCalledOnce();
	});

	it.each([
		[
			'files',
			() => api.runs.files,
			(rs: RunStore) => rs.refreshFiles(),
			(rs: RunStore) => rs.snapshots
		],
		[
			'denials',
			() => api.egress.denials,
			(rs: RunStore) => rs.refreshDenials(),
			(rs: RunStore) => rs.denials
		],
		[
			'ports',
			() => api.runs.ports,
			(rs: RunStore) => rs.refreshPorts(),
			(rs: RunStore) => rs.exposed
		],
		[
			'resources',
			() => api.runs.resources,
			(rs: RunStore) => rs.refreshResources(),
			(rs: RunStore) => rs.samples
		]
	])('keeps the %s it has when a read fails', async (_, fn, refresh, held) => {
		const rs = store();
		const kept = [{ marker: true }] as never;
		rs.snapshots = kept;
		rs.denials = kept;
		rs.exposed = kept;
		rs.samples = kept;
		vi.mocked(fn() as () => Promise<unknown>).mockRejectedValue(new Error('blip'));

		await refresh(rs);

		expect(held(rs)).toEqual([{ marker: true }]);
	});

	it('takes what each read returns', async () => {
		const rs = store();
		const snapshots = [{ id: 's1' }];
		const attachments = [{ id: 'a1' }];
		const exposed = [{ port: 3000, url: 'http://x' }];
		const observed = [{ port: 3000 }];
		vi.mocked(api.runs.files).mockResolvedValue({ snapshots, attachments } as never);
		vi.mocked(api.runs.ports).mockResolvedValue({ exposed, observed } as never);
		await rs.refreshFiles();
		await rs.refreshPorts();
		expect(rs.snapshots).toEqual(snapshots);
		expect(rs.attachments).toEqual(attachments);
		expect(rs.exposed).toEqual(exposed);
		expect(rs.observed).toEqual(observed);
	});

	it('shows a failed services read and clears it on the next good one', async () => {
		const rs = store();
		rs.services = noServices;
		vi.mocked(api.runs.services).mockRejectedValueOnce(new Error('sidecar down'));
		await rs.refreshServices();
		expect(rs.servicesError).toBe('sidecar down');
		expect(rs.services).toEqual(noServices);

		await rs.refreshServices();
		expect(rs.servicesError).toBe('');
	});
});

describe('refreshRun', () => {
	it('clears a load error once the run reads', async () => {
		const rs = new RunStore('r');
		vi.mocked(api.runs.get).mockRejectedValueOnce(new Error('down'));
		await rs.refreshRun();
		expect(rs.loadError).toBe('down');

		vi.mocked(api.runs.get).mockResolvedValue({ run: aRun(), events: [] });
		await rs.refreshRun();
		expect(rs.loadError).toBe('');
		expect(rs.run).not.toBeNull();
	});

	it('keeps the run it has through a failed read, with no error page', async () => {
		const run = aRun();
		const rs = store(run);
		vi.mocked(api.runs.get).mockRejectedValue(new Error('blip'));
		await rs.refreshRun();
		expect(rs.run).toEqual(run);
		expect(rs.loadError).toBe('');
	});

	// Every run_/ping_/env_/hold_ event starts a read.
	it('keeps the newer run when an older read lands last', async () => {
		const rs = new RunStore('r');
		const older = deferred<{ run: Run; events: RunEvent[] }>();
		const newer = deferred<{ run: Run; events: RunEvent[] }>();
		vi.mocked(api.runs.get).mockReturnValueOnce(older.promise);
		vi.mocked(api.runs.get).mockReturnValueOnce(newer.promise);

		const first = rs.refreshRun();
		const second = rs.refreshRun();
		newer.resolve({ run: aRun({ state: 'completed' }), events: [] });
		await second;
		older.resolve({ run: aRun({ state: 'running' }), events: [] });
		await first;

		expect(rs.run?.state).toBe('completed');
	});

	it('shows a newer read landing after an older one', async () => {
		const rs = new RunStore('r');
		vi.mocked(api.runs.get).mockResolvedValueOnce({
			run: aRun({ state: 'running' }),
			events: []
		});
		vi.mocked(api.runs.get).mockResolvedValueOnce({
			run: aRun({ state: 'completed' }),
			events: []
		});

		await Promise.all([rs.refreshRun(), rs.refreshRun()]);

		expect(rs.run?.state).toBe('completed');
	});
});

describe('the derived state', () => {
	it.each<[string, Partial<Run>, boolean]>([
		['a live container', { envId: 'e', state: 'running' }, true],
		['a paused run with a container', { envId: 'e', state: 'paused' }, true],
		['a status not reconciled yet', { envId: 'e', envStatus: 'unknown' }, true],
		['no container', {}, false],
		['an absent container', { envId: 'e', envStatus: 'absent' }, false],
		['an archived run', { envId: 'e', state: 'archived' }, false]
	])('has a workspace or not for %s', (_, over, want) => {
		expect(store(aRun(over)).hasWorkspace).toBe(want);
	});

	it.each<[string, Partial<Run>, boolean]>([
		['a provisioning run', { state: 'provisioning' }, true],
		['a live container', { envId: 'e', state: 'running' }, true],
		['a running run without one', { state: 'running' }, false]
	])('has a live workspace or not for %s', (_, over, want) => {
		expect(store(aRun(over)).workspaceLive).toBe(want);
	});

	it('has no workspace and is not terminal before the run loads', () => {
		const rs = store(null);
		expect(rs.hasWorkspace).toBe(false);
		expect(rs.workspaceLive).toBe(false);
		expect(rs.terminal).toBe(false);
	});

	it.each<[Run['state'], boolean]>([
		['running', false],
		['paused', false],
		['completed', true],
		['failed', true],
		['abandoned', true],
		['archived', true]
	])('reads %s as terminal: %s', (state, want) => {
		expect(store(aRun({ state })).terminal).toBe(want);
	});

	it('withdraws what is pending when the run ends', () => {
		const rs = store(aRun({ state: 'failed' }));
		rs.events = [aRaise(1, 'p1')];
		expect(rs.pendingPings).toEqual([]);
		expect(rs.pings[0].status).toBe('withdrawn');
	});

	it('builds the links from the exposed ports and the services', () => {
		const rs = store();
		rs.exposed = [{ port: 3000, url: 'http://a' }];
		rs.services = {
			workspace: true,
			services: [{ name: 'web', ports: [{ port: 80, url: 'http://s' }] }]
		} as unknown as RunServices;
		expect(rs.links.map((l) => l.kind)).toEqual(['web :80', 'app :3000']);
	});
});

describe('held hosts', () => {
	it('asks only about hosts still held', () => {
		const rs = store();
		rs.denials = [denial('held.test'), denial('blocked.test', 'blocked' as never)];
		expect(rs.newDenials.map((d) => d.host)).toEqual(['held.test']);
	});

	it('snoozes the hosts it was shown', () => {
		const rs = store();
		rs.denials = [denial('a.test')];
		rs.dismissDenials();
		expect(rs.newDenials).toEqual([]);
	});

	it('raises the banner again for a new host', () => {
		const rs = store();
		rs.denials = [denial('a.test')];
		rs.dismissDenials();
		rs.denials = [denial('a.test'), denial('b.test')];
		expect(rs.newDenials.map((d) => d.host)).toEqual(['b.test']);
	});
});

describe('loadRecipe', () => {
	const run = aRun({ projectId: 'proj', flowHash: 'abc', flowName: 'builder' });

	it('reads the stages of the flow version the run pinned', async () => {
		const rs = store(run);
		await rs.loadRecipe(run);
		expect(api.flows.version).toHaveBeenCalledWith('proj', 'abc');
		expect(api.flows.get).not.toHaveBeenCalled();
		expect(rs.stageDefs?.map((s) => s.id)).toEqual(['plan', 'check.lint', 'open']);
		expect(rs.stageDefs?.[0]).toEqual({
			id: 'plan',
			type: 'agent',
			agent: 'planner',
			phase: 'main'
		});
	});

	it('sees that the flow opens a merge request', async () => {
		const rs = store(run);
		await rs.loadRecipe(run);
		expect(rs.opensMRs).toBe(true);
	});

	it('sees that it does not', async () => {
		vi.mocked(api.flows.version).mockResolvedValue({
			yaml: 'name: f\nversion: 5\nmain:\n  - { id: a, type: action, action: commit }\n'
		} as never);
		const rs = store(run);
		await rs.loadRecipe(run);
		expect(rs.opensMRs).toBe(false);
	});

	it('falls back to the current document when the version is gone', async () => {
		vi.mocked(api.flows.version).mockRejectedValue(new Error('not found'));
		const rs = store(run);
		await rs.loadRecipe(run);
		expect(api.flows.get).toHaveBeenCalledWith('proj', 'builder');
		expect(rs.stageDefs).toHaveLength(3);
	});

	it('goes straight to the current document for a run with no hash', async () => {
		const unpinned = aRun({ projectId: 'proj', flowHash: '' });
		const rs = store(unpinned);
		await rs.loadRecipe(unpinned);
		expect(api.flows.version).not.toHaveBeenCalled();
		expect(api.flows.get).toHaveBeenCalledOnce();
	});

	it.each([
		['a document that will not parse', () => ({ yaml: 'main: [' })],
		['a document with nothing in it', () => ({ yaml: '' })]
	])('leaves the strip to the log for %s', async (_, doc) => {
		vi.mocked(api.flows.version).mockResolvedValue(doc() as never);
		vi.mocked(api.flows.get).mockResolvedValue(doc() as never);
		const rs = store(run);
		rs.stageDefs = [{ id: 'stale', type: '' }];
		await rs.loadRecipe(run);
		expect(rs.stageDefs).toBeNull();
	});

	it('leaves the strip to the log when no document reads', async () => {
		vi.mocked(api.flows.version).mockRejectedValue(new Error('gone'));
		vi.mocked(api.flows.get).mockRejectedValue(new Error('gone'));
		const rs = store(run);
		await rs.loadRecipe(run);
		expect(rs.stageDefs).toBeNull();
	});

	it('does nothing for a run outside a project', async () => {
		const rs = store();
		await rs.loadRecipe(aRun());
		expect(api.flows.version).not.toHaveBeenCalled();
		expect(api.flows.get).not.toHaveBeenCalled();
	});
});

describe('act', () => {
	it('runs the action, refreshes the run and the fleet, and says it worked', async () => {
		const rs = store();
		vi.mocked(api.runs.get).mockResolvedValue({ run: aRun({ state: 'paused' }), events: [] });
		const fn = vi.fn(async () => {});

		await expect(rs.act(fn)).resolves.toBe(true);

		expect(fn).toHaveBeenCalledOnce();
		expect(rs.run?.state).toBe('paused');
		expect(fleet.refresh).toHaveBeenCalledOnce();
	});

	it('skips the run refresh when asked', async () => {
		await store().act(async () => {}, false);
		expect(api.runs.get).not.toHaveBeenCalled();
		expect(fleet.refresh).toHaveBeenCalledOnce();
	});

	it('keeps a failure for the header and says it did not work', async () => {
		const rs = store();
		await expect(rs.act(() => Promise.reject(new Error('refused')))).resolves.toBe(false);
		expect(rs.error).toBe('refused');
		expect(api.runs.get).not.toHaveBeenCalled();
		expect(fleet.refresh).not.toHaveBeenCalled();
	});

	it('clears the last failure as the next action starts', async () => {
		const rs = store();
		rs.error = 'old';
		const job = deferred();
		const running = rs.act(() => job.promise);
		expect(rs.error).toBe('');
		job.resolve();
		await running;
	});
});

describe('take-over', () => {
	it('takes over this run', async () => {
		const run = aRun();
		await expect(store(run).takeOver()).resolves.toBe(true);
		expect(api.runs.takeOver).toHaveBeenCalledWith(run.id);
	});

	it('stays in "handing back" after a good release: the engine closes it', async () => {
		const rs = store();
		await expect(rs.release()).resolves.toBe(true);
		expect(rs.releasing).toBe(true);
	});

	it('shows "handing back" while the request runs', () => {
		vi.mocked(api.runs.release).mockReturnValue(deferred<void>().promise);
		const rs = store();
		void rs.release();
		expect(rs.releasing).toBe(true);
	});

	it('takes the hand-back away when the release fails', async () => {
		vi.mocked(api.runs.release).mockRejectedValue(new Error('no hold'));
		const rs = store();
		await expect(rs.release()).resolves.toBe(false);
		expect(rs.releasing).toBe(false);
		expect(rs.error).toBe('no hold');
	});
});

describe('answer', () => {
	it('sends the answer with its files and form values', async () => {
		await store().answer('p1', 'accept', 'A note', ['a1'], { env: 'dev' });
		expect(api.pings.answer).toHaveBeenCalledWith('p1', {
			answer: 'accept',
			feedback: 'A note',
			attachmentIds: ['a1'],
			content: { env: 'dev' }
		});
	});

	it.each([
		['blank', '   ', undefined],
		['missing', undefined, undefined],
		['padded', '  Fine  ', 'Fine']
	])('sends a %s note as %s', async (_, note, feedback) => {
		await store().answer('p1', 'approve', note);
		expect(vi.mocked(api.pings.answer).mock.calls[0][1]).toMatchObject({ feedback });
	});

	it('holds the card while it sends', async () => {
		const job = deferred();
		vi.mocked(api.pings.answer).mockReturnValue(job.promise as never);
		const rs = store();

		const sending = rs.answer('p1', 'approve');
		expect(rs.answering).toBe(true);

		job.resolve();
		await sending;
		expect(rs.answering).toBe(false);
	});

	it('confirms and refreshes the fleet', async () => {
		await store().answer('p1', 'approve');
		expect(toasts.list.map((t) => t.text)).toEqual(['Answer sent — the run picks it up from here']);
		expect(fleet.refresh).toHaveBeenCalledOnce();
	});

	it('keeps a failure for the header, unconfirmed', async () => {
		vi.mocked(api.pings.answer).mockRejectedValue(new Error('already answered'));
		const rs = store();
		rs.error = 'old';

		await rs.answer('p1', 'approve');

		expect(rs.error).toBe('already answered');
		expect(rs.answering).toBe(false);
		expect(toasts.list).toEqual([]);
		expect(fleet.refresh).not.toHaveBeenCalled();
	});
});
