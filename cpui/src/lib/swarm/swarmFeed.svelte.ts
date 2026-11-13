// The war room's live picture: the swarm snapshot polled every two seconds
// while the page is open, and the kill feed the snapshots' differences make.
// Ref-counted like `fleet`; polling pauses while the tab is hidden, and
// `paused` stays true until the first refresh after it comes back (the map's
// motion stops with it). When the last consumer leaves, everything is
// forgotten: the next visit starts from a fresh board, and nobody else's.

import { api } from '$lib/api';
import { errorMessage } from '$lib/core/errors';
import type { Swarm } from '$lib/domain';
import { diffSwarm, type FeedEntry } from './feed';

const POLL_MS = 2000;
/** The ticker keeps this many lines. */
export const FEED_CAP = 50;

class SwarmFeed {
	/** The latest snapshot; null before the first lands. */
	swarm = $state<Swarm | null>(null);
	/** Newest first, at most FEED_CAP. */
	feed = $state<FeedEntry[]>([]);
	/** The last poll failed; the last snapshot is kept. */
	error = $state('');
	paused = $state(false);

	#users = 0;
	#timer: ReturnType<typeof setInterval> | undefined;
	#inflight: Promise<void> | null = null;
	#epoch = 0;

	/** Start polling for one consumer; returns its stop. */
	use(): () => void {
		this.#users++;
		if (this.#users === 1) {
			void this.refresh();
			this.#timer = setInterval(() => {
				if (!document.hidden) void this.refresh();
			}, POLL_MS);
			document.addEventListener('visibilitychange', this.#onVis);
		}
		return () => {
			this.#users--;
			if (this.#users === 0) {
				clearInterval(this.#timer);
				this.#timer = undefined;
				document.removeEventListener('visibilitychange', this.#onVis);
				this.#reset();
			}
		};
	}

	#onVis = () => {
		if (document.hidden) this.paused = true;
		else void this.refresh();
	};

	refresh(): Promise<void> {
		if (this.#inflight) return this.#inflight;
		const load = this.#load().finally(() => {
			if (this.#inflight === load) this.#inflight = null;
		});
		this.#inflight = load;
		return load;
	}

	async #load() {
		const epoch = this.#epoch;
		try {
			const next = await api.swarm.get();
			if (epoch !== this.#epoch) return;
			const lines = diffSwarm(this.swarm, next);
			if (lines.length) this.feed = [...lines.reverse(), ...this.feed].slice(0, FEED_CAP);
			this.swarm = next;
			this.paused = false;
			this.error = '';
		} catch (e) {
			if (epoch === this.#epoch) this.error = errorMessage(e);
		}
	}

	#reset() {
		this.#epoch++;
		this.#inflight = null;
		this.swarm = null;
		this.feed = [];
		this.error = '';
		this.paused = false;
	}
}

export const swarmFeed = new SwarmFeed();
