import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Limits } from '$lib/domain';
import { aPing, aProject, aRun, aTicket } from '$test/fixtures';
import { visibility } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import { fleet } from './fleet.svelte';

vi.mock('$lib/api', () => ({
	api: {
		runs: { list: vi.fn() },
		pings: { pending: vi.fn() },
		projects: { list: vi.fn() },
		tickets: { list: vi.fn() },
		system: { limits: vi.fn() }
	}
}));

const POLL_MS = 4000;
const limits = { maxConcurrentRuns: 4, maxEnvironments: 8 } as Limits;

function stubEngine() {
	vi.mocked(api.runs.list).mockResolvedValue([]);
	vi.mocked(api.pings.pending).mockResolvedValue([]);
	vi.mocked(api.projects.list).mockResolvedValue([]);
	vi.mocked(api.tickets.list).mockResolvedValue([]);
	vi.mocked(api.system.limits).mockResolvedValue(limits);
}

beforeEach(() => {
	vi.resetAllMocks();
	stubEngine();
	fleet.reset();
	fleet.limits = null;
	fleet.paused = false;
});

describe('fleet polling', () => {
	let stops: (() => void)[] = [];
	const use = () => {
		let stopped = false;
		const stop = fleet.use();
		const once = () => {
			if (!stopped) stop();
			stopped = true;
		};
		stops.push(once);
		return once;
	};
	const polls = () => vi.mocked(api.runs.list).mock.calls.length;

	beforeEach(() => {
		vi.useFakeTimers();
		stops = [];
	});
	afterEach(() => {
		stops.forEach((s) => s());
		vi.useRealTimers();
	});

	it('refreshes at once and then every four seconds', async () => {
		use();
		await vi.advanceTimersByTimeAsync(0);
		expect(polls()).toBe(1);

		await vi.advanceTimersByTimeAsync(POLL_MS - 1);
		expect(polls()).toBe(1);
		await vi.advanceTimersByTimeAsync(1);
		expect(polls()).toBe(2);
		await vi.advanceTimersByTimeAsync(POLL_MS);
		expect(polls()).toBe(3);
	});

	it('polls once for two consumers, and stops only when the last one leaves', async () => {
		const first = use();
		const second = use();
		await vi.advanceTimersByTimeAsync(POLL_MS);
		expect(polls()).toBe(2);

		first();
		await vi.advanceTimersByTimeAsync(POLL_MS);
		expect(polls()).toBe(3);

		second();
		await vi.advanceTimersByTimeAsync(POLL_MS * 3);
		expect(polls()).toBe(3);
	});

	it('starts over when a consumer comes back after everyone left', async () => {
		use()();
		await vi.advanceTimersByTimeAsync(POLL_MS);
		expect(polls()).toBe(1);

		use();
		await vi.advanceTimersByTimeAsync(0);
		expect(polls()).toBe(2);
		await vi.advanceTimersByTimeAsync(POLL_MS);
		expect(polls()).toBe(3);
	});

	it('does not poll a hidden tab, and is paused until a refresh lands', async () => {
		const tab = visibility();
		use();
		await vi.advanceTimersByTimeAsync(0);

		tab.set(true);
		expect(fleet.paused).toBe(true);
		await vi.advanceTimersByTimeAsync(POLL_MS * 3);
		expect(polls()).toBe(1);

		const refresh = deferred<never[]>();
		vi.mocked(api.runs.list).mockReturnValueOnce(refresh.promise);
		tab.set(false);
		expect(polls()).toBe(2);
		expect(fleet.paused).toBe(true);

		refresh.resolve([]);
		await vi.advanceTimersByTimeAsync(0);
		expect(fleet.paused).toBe(false);
	});

	it('stays paused when the refresh on return fails', async () => {
		const tab = visibility();
		use();
		await vi.advanceTimersByTimeAsync(0);
		tab.set(true);

		vi.mocked(api.runs.list).mockRejectedValueOnce(new Error('engine down'));
		tab.set(false);
		await vi.advanceTimersByTimeAsync(0);

		expect(fleet.paused).toBe(true);
		expect(fleet.error).toBe('engine down');
	});

	it('stops listening to the tab once nobody is using it', async () => {
		const tab = visibility();
		use()();
		await vi.advanceTimersByTimeAsync(0);
		const before = polls();

		tab.set(true);
		tab.set(false);
		await vi.advanceTimersByTimeAsync(POLL_MS);

		expect(fleet.paused).toBe(false);
		expect(polls()).toBe(before);
	});

	it('keeps polling after a failed refresh', async () => {
		vi.mocked(api.runs.list).mockRejectedValueOnce(new Error('blip'));
		use();
		await vi.advanceTimersByTimeAsync(0);
		expect(fleet.error).toBe('blip');

		await vi.advanceTimersByTimeAsync(POLL_MS);
		expect(fleet.error).toBe('');
	});
});

describe('fleet.refresh', () => {
	it('loads everything and stamps the time', async () => {
		vi.useFakeTimers({ now: 1_000_000 });
		const run = aRun();
		const ping = aPing();
		const project = aProject();
		vi.mocked(api.runs.list).mockResolvedValue([run]);
		vi.mocked(api.pings.pending).mockResolvedValue([ping]);
		vi.mocked(api.projects.list).mockResolvedValue([project]);
		const ticket = aTicket();
		vi.mocked(api.tickets.list).mockResolvedValue([ticket]);

		await fleet.refresh();
		vi.useRealTimers();

		expect(fleet.runs).toEqual([run]);
		expect(fleet.pending).toEqual([ping]);
		expect(fleet.projects).toEqual([project]);
		expect(fleet.tickets).toEqual([ticket]);
		expect(fleet.ticketById.get(ticket.id)).toEqual(ticket);
		// Every ticket, done ones too: the lists filter them.
		expect(api.tickets.list).toHaveBeenCalledWith();
		expect(fleet.limits).toEqual(limits);
		expect(fleet.updatedAt).toBe(1_000_000);
	});

	it('shares one load between callers while it is in flight', async () => {
		const runs = deferred<never[]>();
		vi.mocked(api.runs.list).mockReturnValueOnce(runs.promise);

		const a = fleet.refresh();
		const b = fleet.refresh();
		runs.resolve([]);
		await Promise.all([a, b]);
		expect(api.runs.list).toHaveBeenCalledOnce();

		await fleet.refresh();
		expect(api.runs.list).toHaveBeenCalledTimes(2);
	});

	it('reads the deployment caps once', async () => {
		await fleet.refresh();
		await fleet.refresh();
		expect(api.system.limits).toHaveBeenCalledOnce();
		expect(fleet.limits).toEqual(limits);
	});

	it('survives without caps, and asks again on the next refresh', async () => {
		vi.mocked(api.system.limits).mockRejectedValueOnce(new Error('forbidden'));
		await fleet.refresh();
		expect(fleet.error).toBe('');
		expect(fleet.limits).toBeNull();

		await fleet.refresh();
		expect(fleet.limits).toEqual(limits);
	});

	it('keeps what it had when a refresh fails, and clears the error on the next', async () => {
		const run = aRun();
		vi.mocked(api.runs.list).mockResolvedValueOnce([run]);
		await fleet.refresh();
		const stamped = fleet.updatedAt;

		vi.mocked(api.pings.pending).mockRejectedValueOnce(new Error('engine down'));
		await fleet.refresh();
		expect(fleet.error).toBe('engine down');
		expect(fleet.runs).toEqual([run]);
		expect(fleet.updatedAt).toBe(stamped);

		await fleet.refresh();
		expect(fleet.error).toBe('');
	});

	// The layout resets on a new sign-in; a poll started for the last user can
	// still be in the air at that moment.
	it('drops a load still in flight when it is reset', async () => {
		const old = deferred<ReturnType<typeof aRun>[]>();
		vi.mocked(api.runs.list).mockReturnValueOnce(old.promise);
		const loading = fleet.refresh();

		fleet.reset();
		old.resolve([aRun({ title: 'someone else' })]);
		await loading;

		expect(fleet.runs).toEqual([]);
	});

	it('keeps quiet when that load fails', async () => {
		const old = deferred<ReturnType<typeof aRun>[]>();
		vi.mocked(api.runs.list).mockReturnValueOnce(old.promise);
		const loading = fleet.refresh();

		fleet.reset();
		old.reject(new Error('not yours'));
		await loading;

		expect(fleet.error).toBe('');
	});

	it('asks afresh after a reset, and the older load stays out of it', async () => {
		const old = deferred<ReturnType<typeof aRun>[]>();
		vi.mocked(api.runs.list).mockReturnValueOnce(old.promise);
		const loading = fleet.refresh();

		fleet.reset();
		const mine = aRun({ title: 'mine' });
		vi.mocked(api.runs.list).mockResolvedValueOnce([mine]);
		await fleet.refresh();
		expect(fleet.runs).toEqual([mine]);

		const again = deferred<ReturnType<typeof aRun>[]>();
		vi.mocked(api.runs.list).mockReturnValueOnce(again.promise);
		const next = fleet.refresh();
		old.resolve([aRun({ title: 'someone else' })]);
		await loading;
		expect(fleet.refresh()).toBe(next);

		again.resolve([mine]);
		await next;
		expect(fleet.runs).toEqual([mine]);
	});
});

describe('fleet.reset', () => {
	it('forgets the signed-in user and keeps the deployment caps', async () => {
		vi.mocked(api.runs.list).mockResolvedValue([aRun()]);
		vi.mocked(api.pings.pending).mockResolvedValue([aPing()]);
		vi.mocked(api.projects.list).mockResolvedValue([aProject()]);
		vi.mocked(api.tickets.list).mockResolvedValue([aTicket()]);
		await fleet.refresh();
		fleet.error = 'old';

		fleet.reset();

		expect(fleet.runs).toEqual([]);
		expect(fleet.pending).toEqual([]);
		expect(fleet.projects).toEqual([]);
		expect(fleet.tickets).toEqual([]);
		expect(fleet.updatedAt).toBeNull();
		expect(fleet.error).toBe('');
		expect(fleet.limits).toEqual(limits);
	});
});

describe('fleet views', () => {
	it('splits runs into active and finished by state', () => {
		fleet.runs = [
			aRun({ id: 'a', state: 'waiting_on_human' }),
			aRun({ id: 'b', state: 'paused' }),
			aRun({ id: 'c', state: 'completed' }),
			aRun({ id: 'd', state: 'archived' })
		];
		expect(fleet.active.map((r) => r.id)).toEqual(['a', 'b']);
		expect(fleet.finished.map((r) => r.id)).toEqual(['c', 'd']);
	});

	it('counts only running and provisioning runs against the concurrency cap', () => {
		fleet.limits = limits;
		fleet.runs = [
			aRun({ id: 'a', state: 'running' }),
			aRun({ id: 'b', state: 'provisioning' }),
			aRun({ id: 'c', state: 'waiting_on_human' }),
			aRun({ id: 'd', state: 'in_review' }),
			aRun({ id: 'e', state: 'pending' })
		];
		expect(fleet.capacity).toEqual({
			runs: { used: 2, max: 4 },
			envs: { used: 0, max: 8 },
			queued: 1
		});
	});

	it('counts live environments only: not absent ones, not archived runs', () => {
		fleet.runs = [
			aRun({ id: 'a', envId: 'e1' }),
			aRun({ id: 'b', envId: 'e2', state: 'paused' }),
			aRun({ id: 'c', envId: 'e3', envStatus: 'absent' }),
			aRun({ id: 'd', envId: 'e4', state: 'archived' }),
			aRun({ id: 'e' })
		];
		expect(fleet.envs.map((r) => r.id)).toEqual(['a', 'b']);
	});

	it('reads a cap it has not loaded as none', () => {
		expect(fleet.capacity.runs.max).toBe(0);
		expect(fleet.capacity.envs.max).toBe(0);
	});

	it('names a workshop, a short id for an unknown one, nothing for none', () => {
		fleet.projects = [aProject({ id: 'p1', name: 'billing' })];
		expect(fleet.projectName('p1')).toBe('billing');
		expect(fleet.projectName('01TESTPROJ00000000ABCDEF')).toBe('ABCDEF');
		expect(fleet.projectName(undefined)).toBe('');
	});

	it('groups active runs by workshop and skips the ones with none', () => {
		fleet.runs = [
			aRun({ id: 'a', projectId: 'p1' }),
			aRun({ id: 'b', projectId: 'p1', state: 'completed' }),
			aRun({ id: 'c', projectId: 'p2' }),
			aRun({ id: 'd' })
		];
		expect(fleet.liveByProject.get('p1')?.map((r) => r.id)).toEqual(['a']);
		expect(fleet.liveByProject.get('p2')?.map((r) => r.id)).toEqual(['c']);
		expect(fleet.liveByProject.size).toBe(2);
	});

	it("lists a ticket's runs newest first, finished ones included", () => {
		fleet.runs = [
			aRun({ id: 'old', ticketId: 't1', createdAt: '2026-10-01T00:00:00Z', state: 'failed' }),
			aRun({ id: 'new', ticketId: 't1', createdAt: '2026-10-03T00:00:00Z' }),
			aRun({ id: 'mid', ticketId: 't1', createdAt: '2026-10-02T00:00:00Z' }),
			aRun({ id: 'none' })
		];
		expect(fleet.runsByTicket.get('t1')?.map((r) => r.id)).toEqual(['new', 'mid', 'old']);
		expect(fleet.runsByTicket.size).toBe(1);
	});

	it("counts pending pings through the ping's run", () => {
		fleet.runs = [aRun({ id: 'r1', projectId: 'p1' }), aRun({ id: 'r2' })];
		fleet.pending = [
			aPing({ id: '1', runId: 'r1' }),
			aPing({ id: '2', runId: 'r1' }),
			aPing({ id: '3', runId: 'r2' }),
			aPing({ id: '4', runId: 'gone' })
		];
		expect(fleet.pingsByProject.get('p1')).toBe(2);
		expect(fleet.pingsByProject.size).toBe(1);
	});
});
