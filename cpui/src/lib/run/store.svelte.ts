// One live run, as the run page and its views share it: the run, its event
// log (SSE) and the agent's live deltas, plus the bits of workspace state
// several views read (denials, ports, the record, resource samples).
//
// Polling is demand-driven: a view that needs a feed holds it with use*()
// while it is visible, and every poll skips while the tab is hidden.

import { parse as parseYaml } from 'yaml';
import { api } from '$lib/api';
import type {
	Attachment,
	EgressDenial,
	ObservedPort,
	ResourceSample,
	Run,
	RunEvent,
	Snapshot,
	Ticket
} from '$lib/domain';
import { fleet } from '$lib/fleet.svelte';
import { normalizeDoc, successOrder } from '$lib/flowdoc';
import { isTerminal } from '$lib/format';
import { applyDelta, emptyLive, pruneLive, type LiveState } from '$lib/transcript';
import { currentStage, envLinks, pingsOf, totals, workingSince, type StageDef } from './derive';

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e));

export class RunStore {
	readonly runId: string;

	run = $state<Run | null>(null);
	events = $state<RunEvent[]>([]);
	/** The agent's in-flight progress, superseded by persisted events. */
	live = $state<LiveState>(emptyLive());
	loadError = $state('');
	/** The last action's failure, shown under the header. */
	error = $state('');
	ticket = $state<Ticket | null>(null);
	/** The workshop's name (the fleet's copy may not have landed yet). */
	projectName = $state('');
	/** The pinned recipe's stages, in order; null until loaded (or unknown). */
	stageDefs = $state<StageDef[] | null>(null);
	denials = $state<EgressDenial[]>([]);
	dismissed = $state<Set<string>>(new Set());
	exposed = $state<{ port: number; url: string }[]>([]);
	observed = $state<ObservedPort[]>([]);
	snapshots = $state<Snapshot[]>([]);
	attachments = $state<Attachment[]>([]);
	samples = $state<ResourceSample[]>([]);
	/** Ticks every second: live clocks. */
	now = $state(Date.now());
	answering = $state(false);
	/** Counts the views report about themselves (tab badges). */
	changesCount = $state<number | null>(null);
	/** The Logs view's source: 'workspace', '_provision' or a stage id. */
	logSource = $state('workspace');
	/** The Egress view's verdict filter (the banner opens it on denials). */
	egressVerdict = $state<'' | 'allow' | 'deny'>('');

	// Plain counters: polls read them, nothing renders them.
	#portUsers = 0;
	#resourceUsers = 0;

	constructor(runId: string) {
		this.runId = runId;
	}

	readonly terminal = $derived(this.run !== null && isTerminal(this.run.state));
	/** A container is (probably) up behind the workspace views. */
	readonly hasWorkspace = $derived(
		this.run !== null &&
			!!this.run.envId &&
			this.run.state !== 'archived' &&
			this.run.envStatus !== 'absent'
	);
	readonly totals = $derived(totals(this.events));
	readonly pings = $derived(pingsOf(this.events, this.run));
	/** Newest first: each answer reveals the next-earlier one. */
	readonly pendingPings = $derived(this.pings.filter((p) => p.status === 'pending').reverse());
	readonly current = $derived(currentStage(this.events));
	readonly since = $derived(workingSince(this.events, this.current));
	readonly links = $derived(envLinks(this.events, this.exposed));
	readonly newDenials = $derived(this.denials.filter((d) => !this.dismissed.has(d.host)));
	/** When the workspace went away, for the "destroyed 2h ago" states. */
	readonly destroyedAt = $derived(this.events.findLast((e) => e.type === 'env_destroyed')?.ts);

	start(): () => void {
		const timers: ReturnType<typeof setInterval>[] = [];
		timers.push(setInterval(() => (this.now = Date.now()), 1000));
		// The header's denial banner and the egress badge.
		timers.push(
			setInterval(() => {
				if (document.hidden || this.terminal || !this.hasWorkspace) return;
				void this.refreshDenials();
				if (this.#portUsers > 0) void this.refreshPorts();
			}, 5000)
		);
		// Reconciled container status changes without an event.
		timers.push(setInterval(() => !document.hidden && this.refreshRun(), 15000));
		timers.push(
			setInterval(() => {
				if (document.hidden || this.#resourceUsers === 0) return;
				void this.refreshResources();
			}, 5000)
		);

		void this.refreshRun().then(() => {
			const r = this.run;
			if (!r) return;
			if (r.projectId)
				api.projects
					.get(r.projectId)
					.then((p) => (this.projectName = p.name))
					.catch(() => {});
			if (r.ticketId)
				api.tickets
					.get(r.ticketId)
					.then((t) => (this.ticket = t))
					.catch(() => {});
			void this.loadRecipe(r);
			if (this.hasWorkspace) {
				void this.refreshDenials();
				void this.refreshPorts();
			}
		});
		void this.refreshFiles();

		const stop = api.runs.events(
			this.runId,
			0,
			(ev) => {
				this.events = [...this.events, ev];
				this.live = pruneLive(this.live, ev);
				// State-changing events refresh the header.
				if (
					ev.type.startsWith('run_') ||
					ev.type.startsWith('ping_') ||
					ev.type.startsWith('env_')
				) {
					void this.refreshRun();
					if (ev.type.startsWith('run_') || ev.type.startsWith('ping_')) void fleet.refresh();
				}
				// A capture (or an answer, which may carry files) adds a record row.
				if (ev.type === 'snapshot_captured' || ev.type === 'ping_answered')
					void this.refreshFiles();
			},
			(d) => (this.live = applyDelta(this.live, d))
		);

		return () => {
			timers.forEach(clearInterval);
			stop();
		};
	}

	async refreshRun() {
		try {
			this.run = (await api.runs.get(this.runId)).run;
			this.loadError = '';
		} catch (e) {
			if (!this.run) this.loadError = errText(e);
		}
	}

	async refreshFiles() {
		try {
			const f = await api.runs.files(this.runId);
			this.snapshots = f.snapshots;
			this.attachments = f.attachments;
		} catch {
			/* transient — keep what we have */
		}
	}

	async refreshDenials() {
		try {
			this.denials = await api.egress.denials(this.runId);
		} catch {
			/* transient */
		}
	}

	async refreshPorts() {
		try {
			const pr = await api.runs.ports(this.runId);
			this.exposed = pr.exposed;
			this.observed = pr.observed;
		} catch {
			/* transient */
		}
	}

	async refreshResources() {
		try {
			this.samples = await api.runs.resources(this.runId);
		} catch {
			/* transient */
		}
	}

	/** Hold the ports feed (Ports view, Env popover) while it is on screen. */
	usePorts(): () => void {
		this.#portUsers++;
		if (this.hasWorkspace) void this.refreshPorts();
		return () => this.#portUsers--;
	}

	/** Hold the resource samples feed (Vitals, Resources) while on screen. */
	useResources(): () => void {
		this.#resourceUsers++;
		void this.refreshResources();
		return () => this.#resourceUsers--;
	}

	/** The recipe as this run pinned it (its flow version), so pending stages
	 *  show too. Parsed here — validating is the engine's job, reading the
	 *  stage list is not. */
	async loadRecipe(r: Run) {
		if (!r.projectId) return;
		try {
			let yaml = '';
			try {
				yaml = r.flowHash ? (await api.flows.version(r.projectId, r.flowHash)).yaml : '';
			} catch {
				/* not versioned: fall back to the current document */
			}
			if (!yaml) yaml = (await api.flows.get(r.projectId, r.flowName)).yaml;
			const doc = normalizeDoc(parseYaml(yaml));
			// The entry phase is the recipe; reaction phases (merge request
			// conflicts, review rounds) show once they have run.
			const entry = doc.phases?.[0]?.id ?? '';
			this.stageDefs = successOrder(doc)
				.filter((s) => (s.phase ?? '') === entry)
				.map((s) => ({ id: s.id, type: s.type, agent: s.agent }));
		} catch {
			this.stageDefs = null; // the strip falls back to what the log shows
		}
	}

	/** Run an action; on failure keep the message for the header. */
	async act(fn: () => Promise<unknown>, refresh = true): Promise<boolean> {
		this.error = '';
		try {
			await fn();
			if (refresh) await this.refreshRun();
			void fleet.refresh();
			return true;
		} catch (e) {
			this.error = errText(e);
			return false;
		}
	}

	async answer(pingId: string, ans: string, note?: string, attachmentIds?: string[]) {
		this.answering = true;
		this.error = '';
		try {
			await api.pings.answer(pingId, ans, note?.trim() || undefined, attachmentIds);
			void fleet.refresh();
		} catch (e) {
			this.error = errText(e);
		} finally {
			this.answering = false;
		}
	}

	/** Dismiss the new-denial banner without changing any rule. */
	dismissDenials() {
		this.dismissed = new Set(this.denials.map((d) => d.host));
	}
}
