import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { aSwarm, aUnit } from '$test/fixtures/swarm';
import { visibility } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import type { Swarm } from '$lib/domain';
import { FEED_CAP, swarmFeed } from './swarmFeed.svelte';

vi.mock('$lib/api', () => ({ api: { swarm: { get: vi.fn() } } }));

const POLL_MS = 2000;
const polls = () => vi.mocked(api.swarm.get).mock.calls.length;

let stops: (() => void)[] = [];
const use = () => {
	let stopped = false;
	const stop = swarmFeed.use();
	const once = () => {
		if (!stopped) stop();
		stopped = true;
	};
	stops.push(once);
	return once;
};

beforeEach(() => {
	vi.resetAllMocks();
	vi.useFakeTimers();
	vi.mocked(api.swarm.get).mockResolvedValue(aSwarm());
	stops = [];
});
afterEach(() => {
	stops.forEach((s) => s());
	vi.restoreAllMocks();
	vi.useRealTimers();
});

describe('the swarm poll', () => {
	it('reads at once, then every two seconds', async () => {
		use();
		await vi.advanceTimersByTimeAsync(0);
		expect(polls()).toBe(1);
		await vi.advanceTimersByTimeAsync(POLL_MS - 1);
		expect(polls()).toBe(1);
		await vi.advanceTimersByTimeAsync(1);
		expect(polls()).toBe(2);
	});

	it('polls once for two consumers, stops with the last, and forgets the board', async () => {
		const first = use();
		const second = use();
		await vi.advanceTimersByTimeAsync(POLL_MS);
		expect(polls()).toBe(2);
		first();
		await vi.advanceTimersByTimeAsync(POLL_MS);
		expect(polls()).toBe(3);
		expect(swarmFeed.swarm).not.toBeNull();

		second();
		await vi.advanceTimersByTimeAsync(POLL_MS * 3);
		expect(polls()).toBe(3);
		expect(swarmFeed.swarm).toBeNull();
		expect(swarmFeed.feed).toEqual([]);
	});

	it('does not poll a hidden tab, and is paused until a refresh lands', async () => {
		const tab = visibility();
		use();
		await vi.advanceTimersByTimeAsync(0);
		tab.set(true);
		expect(swarmFeed.paused).toBe(true);
		await vi.advanceTimersByTimeAsync(POLL_MS * 3);
		expect(polls()).toBe(1);

		const back = deferred<Swarm>();
		vi.mocked(api.swarm.get).mockReturnValueOnce(back.promise);
		tab.set(false);
		expect(polls()).toBe(2);
		expect(swarmFeed.paused).toBe(true);
		back.resolve(aSwarm());
		await vi.advanceTimersByTimeAsync(0);
		expect(swarmFeed.paused).toBe(false);
	});

	it('keeps the last board when a poll fails, and says why', async () => {
		vi.mocked(api.swarm.get).mockResolvedValueOnce(aSwarm([aUnit()]));
		vi.mocked(api.swarm.get).mockRejectedValueOnce(new Error('engine down'));
		use();
		await vi.advanceTimersByTimeAsync(0);
		await vi.advanceTimersByTimeAsync(POLL_MS);
		expect(swarmFeed.error).toBe('engine down');
		expect(swarmFeed.swarm?.units).toHaveLength(1);

		await vi.advanceTimersByTimeAsync(POLL_MS);
		expect(swarmFeed.error).toBe('');
	});

	it('feeds what changed between polls, newest first, up to its cap', async () => {
		let n = 0;
		vi.mocked(api.swarm.get).mockImplementation(async () =>
			aSwarm(
				Array.from({ length: n++ }, (_, i) => aUnit({ runId: `r${i}` })),
				`t${n}`
			)
		);
		use();
		await vi.advanceTimersByTimeAsync(0);
		expect(swarmFeed.feed).toEqual([]);
		await vi.advanceTimersByTimeAsync(POLL_MS);
		expect(swarmFeed.feed.map((e) => e.runId)).toEqual(['r0']);
		await vi.advanceTimersByTimeAsync(POLL_MS);
		expect(swarmFeed.feed.map((e) => e.runId)).toEqual(['r1', 'r0']);

		await vi.advanceTimersByTimeAsync(POLL_MS * (FEED_CAP + 5));
		expect(swarmFeed.feed).toHaveLength(FEED_CAP);
		expect(swarmFeed.feed[0].runId).toBe(`r${n - 2}`);
	});

	it('drops an answer that lands after everyone left', async () => {
		const late = deferred<Swarm>();
		vi.mocked(api.swarm.get).mockReturnValueOnce(late.promise);
		use()();
		late.resolve(aSwarm([aUnit()]));
		await vi.advanceTimersByTimeAsync(0);
		expect(swarmFeed.swarm).toBeNull();
	});
});
