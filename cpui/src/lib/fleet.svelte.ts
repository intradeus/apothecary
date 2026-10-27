// One's desk at a glance: runs, pending pings, workshops and the deployment's
// caps, polled once for the whole shell. The sidebar, the desk, the runs
// list and the capacity strip all read this one copy, so their counts never
// disagree. Everything in it is the signed-in user's: the engine lists
// nothing of anyone else's.
//
// Polling pauses while the tab is hidden; on return `paused` stays true until
// the first refresh lands (DESIGN 4.17 "Live indicators").

import { api } from '$lib/api';
import { stateTab } from '$lib/format';
import type { Limits, Ping, Project, Run } from '$lib/domain';

const POLL_MS = 4000;

class Fleet {
	runs = $state<Run[]>([]);
	pending = $state<Ping[]>([]);
	projects = $state<Project[]>([]);
	/** The deployment's caps, read once: they come from the boot env. */
	limits = $state<Limits | null>(null);
	/** ms epoch of the last successful refresh; null before the first. */
	updatedAt = $state<number | null>(null);
	/** The tab was hidden and nothing has refreshed since it came back. */
	paused = $state(false);
	error = $state('');

	#users = 0;
	#timer: ReturnType<typeof setInterval> | null = null;
	#inflight: Promise<void> | null = null;

	readonly active = $derived(this.runs.filter((r) => stateTab[r.state] === 'active'));
	readonly finished = $derived(this.runs.filter((r) => stateTab[r.state] === 'finished'));
	/** Runs holding a concurrency slot: a parked run (waiting on a human,
	 *  paused, in review) gives its slot back, as the engine schedules. */
	readonly slots = $derived(
		this.runs.filter((r) => r.state === 'running' || r.state === 'provisioning')
	);
	/** Runs queued for a slot (pending, FIFO). */
	readonly queued = $derived(this.runs.filter((r) => r.state === 'pending'));
	/** Runs holding an environment (what the max-environments cap counts). */
	readonly envs = $derived(
		this.runs.filter((r) => !!r.envId && r.envStatus !== 'absent' && r.state !== 'archived')
	);
	readonly runById = $derived(new Map(this.runs.map((r) => [r.id, r])));
	readonly projectById = $derived(new Map(this.projects.map((p) => [p.id, p])));

	/** A workshop's name, or a short id while projects are loading (or for
	 *  one that was deleted). Reactive inside components. */
	projectName(id: string | undefined): string {
		if (!id) return '';
		return this.projectById.get(id)?.name ?? id.slice(-6);
	}

	/** The deployment's caps against what holds them now (sidebar, desk). A
	 *  cap of 0 is "none": the meter shows the count alone. */
	readonly capacity = $derived({
		runs: { used: this.slots.length, max: this.limits?.maxConcurrentRuns ?? 0 },
		envs: { used: this.envs.length, max: this.limits?.maxEnvironments ?? 0 },
		queued: this.queued.length
	});

	/** Active runs per workshop. */
	readonly liveByProject = $derived.by(() => {
		const m = new Map<string, Run[]>();
		for (const r of this.active) {
			if (!r.projectId) continue;
			m.set(r.projectId, [...(m.get(r.projectId) ?? []), r]);
		}
		return m;
	});
	/** Each ticket's runs, newest first (ticket lists, a ticket's status). */
	readonly runsByTicket = $derived.by(() => {
		const m = new Map<string, Run[]>();
		for (const r of this.runs) {
			if (!r.ticketId) continue;
			m.set(r.ticketId, [...(m.get(r.ticketId) ?? []), r]);
		}
		for (const arr of m.values()) arr.sort((a, b) => b.createdAt.localeCompare(a.createdAt));
		return m;
	});
	/** Pending pings per workshop (through the ping's run). */
	readonly pingsByProject = $derived.by(() => {
		const m = new Map<string, number>();
		for (const p of this.pending) {
			const pid = this.runById.get(p.runId)?.projectId;
			if (pid) m.set(pid, (m.get(pid) ?? 0) + 1);
		}
		return m;
	});

	/** Start polling for one consumer; returns its stop. Ref-counted, so the
	 *  shell and a page can both hold it without double-polling. */
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
				if (this.#timer) clearInterval(this.#timer);
				this.#timer = null;
				document.removeEventListener('visibilitychange', this.#onVis);
			}
		};
	}

	#onVis = () => {
		if (document.hidden) this.paused = true;
		else void this.refresh();
	};

	/** Refresh now (also after an action that changed something). */
	refresh(): Promise<void> {
		this.#inflight ??= this.#load().finally(() => (this.#inflight = null));
		return this.#inflight;
	}

	async #load() {
		try {
			const [runs, pending, projects, limits] = await Promise.all([
				api.runs.list(),
				api.pings.pending(),
				api.projects.list(),
				this.limits ? Promise.resolve(this.limits) : api.system.limits().catch(() => null)
			]);
			this.runs = runs;
			this.pending = pending;
			this.projects = projects;
			this.limits = limits;
			this.updatedAt = Date.now();
			this.paused = false;
			this.error = '';
		} catch (e) {
			this.error = e instanceof Error ? e.message : String(e);
		}
	}

	/** Forget everything: the next poll is someone else's (sign-out, sign-in). */
	reset() {
		this.runs = [];
		this.pending = [];
		this.projects = [];
		this.updatedAt = null;
		this.error = '';
	}
}

export const fleet = new Fleet();
