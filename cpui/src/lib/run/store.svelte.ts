// One live run, as the run page and its views share it: the run, its event
// log (SSE) and the agent's live deltas, plus the bits of workspace state
// several views read (denials, ports, services, the record, resource
// samples).
//
// Polling is demand-driven: a view that needs a feed holds it with use*()
// while it is visible, and every poll skips while the tab is hidden.

import { errorMessage } from '$lib/core/errors';
import { parse as parseYaml } from 'yaml';
import { api } from '$lib/api';
import type {
	Attachment,
	EgressDenial,
	EgressStatus,
	ExposedPort,
	FormContent,
	LiveDelta,
	ObservedPort,
	ResourceSample,
	Run,
	RunEvent,
	RunServices,
	Snapshot,
	TicketRecord
} from '$lib/domain';
import { fleet } from '$lib/app/fleet.svelte';
import { to } from '$lib/core/urls';
import { expandDoc, normalizeDoc, successOrder } from '$lib/domain/flowdoc';
import { isTerminal } from '$lib/core/format';
import { toast } from '$lib/ui/toast.svelte';
import { t } from '$lib/i18n/index.svelte';
import { applyDelta, emptyLive, pruneLive, type LiveState } from '$lib/run/transcriptItems';
import { currentStage, envLinks, pingsOf, totals, workingSince, type StageDef } from './derive';
import { ComposerRegistry } from './composerTarget.svelte';
import { OpenFiles } from './openFiles.svelte';

/** How long the stream's messages gather before they land: about a frame. */
export const STREAM_BATCH_MS = 16;

export class RunStore {
	readonly runId: string;

	run = $state<Run | null>(null);
	/** The log, replaced whole on each batch (raw: a long log must not be
	 *  proxied event by event on every read). */
	events = $state.raw<RunEvent[]>([]);
	/** The agent's in-flight progress, superseded by persisted events. */
	live = $state.raw<LiveState>(emptyLive());
	loadError = $state('');
	/** The last action's failure, shown under the header. */
	error = $state('');
	/** The run's ticket's record, with the workshops it shows through. */
	ticket = $state<TicketRecord | null>(null);
	/** The workshop's name (the fleet's copy may not have landed yet). */
	projectName = $state('');
	/** The pinned recipe's stages, every phase's, in order; null until loaded
	 *  (or unknown). */
	stageDefs = $state<StageDef[] | null>(null);
	/** The pinned recipe has a stage that opens a merge request. */
	opensMRs = $state(false);
	denials = $state<EgressDenial[]>([]);
	dismissed = $state<Set<string>>(new Set());
	exposed = $state<ExposedPort[]>([]);
	observed = $state<ObservedPort[]>([]);
	/** Whether ports get browser links (RunPorts.links); until the feed says. */
	portLinks = $state(true);
	snapshots = $state<Snapshot[]>([]);
	attachments = $state<Attachment[]>([]);
	samples = $state<ResourceSample[]>([]);
	/** The workshop's services as they stand in the workspace; null until
	 *  first read. */
	services = $state<RunServices | null>(null);
	servicesError = $state('');
	/** Ticks every second: live clocks. */
	now = $state(Date.now());
	answering = $state(false);
	/** A release was clicked and the engine has not yet recorded it (it
	 *  waits for the agent's turn in flight to end). */
	releasing = $state(false);
	/** Counts the views report about themselves (tab badges). */
	changesCount = $state<number | null>(null);
	/** The Logs view's source: 'workspace', '_provision' or a stage id. */
	logSource = $state('workspace');
	/** The Session's between-phases nudge was dismissed after this many
	 *  phase completions: it shows again once another phase ends. */
	phasesNudgeDismissedAt = $state(-1);
	/** The Egress view's rule filter (the banner opens it on held hosts). */
	egressRule = $state<'' | EgressStatus>('');
	/** The chats an annotation from the Files viewer can go to. */
	readonly composers = new ComposerRegistry();
	/** The Files pane's tabs and tree, kept per run. */
	readonly files: OpenFiles;

	// Plain counters: polls read them, nothing renders them.
	#portUsers = 0;
	#resourceUsers = 0;
	#serviceUsers = 0;
	/** Reads of the run in the order they were asked, and the latest shown. */
	#runAsked = 0;
	#runShown = 0;
	/** Reads of the run's files in the order they were asked, and the latest shown. */
	#filesAsked = 0;
	#filesShown = 0;
	/** What the stream delivered since the last commit, in arrival order. */
	#inbox: ({ ev: RunEvent } | { d: LiveDelta })[] = [];
	#inboxTimer: ReturnType<typeof setTimeout> | undefined;

	constructor(runId: string) {
		this.runId = runId;
		this.files = new OpenFiles(runId);
	}

	readonly terminal = $derived(this.run !== null && isTerminal(this.run.state));
	/** A container is (probably) up behind the workspace views. */
	readonly hasWorkspace = $derived(
		this.run !== null && !!this.run.envId && !this.run.archivedAt && this.run.envStatus !== 'absent'
	);
	/** A workspace is up or on its way: from the moment provisioning starts,
	 *  its exec log streams and its egress gateway answers. */
	readonly workspaceLive = $derived(
		this.hasWorkspace || (this.run !== null && this.run.state === 'provisioning')
	);
	/** A rebuild is on offer: a workspace to rebuild, and a run that is not
	 *  using it (paused, or over but not archived). The header and the broken
	 *  banner both ask this, so they never disagree. */
	readonly canRebuild = $derived(
		this.run !== null &&
			!!this.run.envId &&
			!this.run.archivedAt &&
			(this.terminal || this.run.state === 'paused')
	);
	/** The run can be paused: it lives, has started and is not paused yet. */
	readonly canPause = $derived(
		this.run !== null &&
			!this.terminal &&
			this.run.state !== 'paused' &&
			this.run.state !== 'pending'
	);
	readonly totals = $derived(totals(this.events));
	readonly pings = $derived(pingsOf(this.events, this.run));
	/** Newest first: each answer reveals the next-earlier one. */
	readonly pendingPings = $derived(this.pings.filter((p) => p.status === 'pending').reverse());
	readonly current = $derived(currentStage(this.events));
	readonly since = $derived(workingSince(this.events, this.current));
	readonly links = $derived(envLinks(this.events, this.exposed, this.services?.services ?? []));
	/** Held hosts the banner still asks about: a blocked host was reviewed,
	 *  a dismissed one is snoozed until the page reloads. */
	readonly newDenials = $derived(
		this.denials.filter((d) => d.status === 'held' && !this.dismissed.has(d.host))
	);
	/** When the workspace went away, for the "destroyed 2h ago" states. */
	readonly destroyedAt = $derived(this.events.findLast((e) => e.type === 'env_destroyed')?.ts);
	/** The ticket's page: through the run's workshop when that one is on the
	 *  ticket's source, else the record's address, which redirects. */
	readonly ticketHref = $derived.by(() => {
		const id = this.run?.ticketId;
		if (!id) return '';
		const pid = this.run?.projectId;
		const rec = this.ticket;
		return pid && rec?.workshops.some((w) => w.id === pid)
			? to.workshopTicket(pid, rec.key)
			: to.ticket(id, { workshop: pid });
	});

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
		// A service's health flips without an event: poll while one is on screen.
		timers.push(
			setInterval(() => {
				if (document.hidden || this.#serviceUsers === 0 || !this.hasWorkspace) return;
				void this.refreshServices();
			}, 3000)
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
					.then((tk) => (this.ticket = tk))
					.catch(() => {});
			void this.loadRecipe(r);
			if (this.hasWorkspace) {
				void this.refreshDenials();
				void this.refreshPorts();
				void this.refreshServices();
			}
		});
		void this.refreshFiles();

		const stop = api.runs.events(
			this.runId,
			0,
			(ev) => this.#receive({ ev }),
			(d) => this.#receive({ d })
		);
		const stopFiles = this.files.start(() => this.hasWorkspace);

		return () => {
			timers.forEach(clearInterval);
			clearTimeout(this.#inboxTimer);
			this.#inbox = [];
			stop();
			stopFiles();
		};
	}

	/** The stream delivers one message at a time — a long run's replay is
	 *  thousands — and each commit re-derives the whole page from the log.
	 *  So messages wait for the next frame and land together. */
	#receive(m: { ev: RunEvent } | { d: LiveDelta }) {
		this.#inbox.push(m);
		this.#inboxTimer ??= setTimeout(() => this.flush(), STREAM_BATCH_MS);
	}

	/** Commit what the stream delivered: one new log, one live state, and
	 *  each refresh the batch calls for asked once. */
	flush() {
		clearTimeout(this.#inboxTimer);
		this.#inboxTimer = undefined;
		const inbox = this.#inbox;
		if (!inbox.length) return;
		this.#inbox = [];
		const added: RunEvent[] = [];
		let last = this.events.at(-1)?.seq ?? 0;
		let live = this.live;
		let run = false;
		let fleetToo = false;
		let files = false;
		let services = false;
		for (const m of inbox) {
			if ('d' in m) {
				live = applyDelta(live, m.d);
				continue;
			}
			const ev = m.ev;
			// A replay must not count twice (the totals, the pings).
			if (ev.seq <= last) continue;
			last = ev.seq;
			added.push(ev);
			live = pruneLive(live, ev);
			// State-changing events refresh the header.
			if (ev.type === 'hold_released') this.releasing = false;
			if (
				ev.type.startsWith('run_') ||
				ev.type.startsWith('ping_') ||
				ev.type.startsWith('env_') ||
				ev.type.startsWith('hold_')
			) {
				run = true;
				if (ev.type.startsWith('run_') || ev.type.startsWith('ping_')) fleetToo = true;
			}
			// A capture (or an answer or a take-over message, which may carry
			// files) adds a record row.
			if (
				ev.type === 'snapshot_captured' ||
				ev.type === 'ping_answered' ||
				ev.type === 'message_queued'
			)
				files = true;
			// A service started, came up, stopped or failed.
			if (ev.type.startsWith('service_')) services = true;
		}
		if (added.length) this.events = this.events.concat(added);
		this.live = live;
		if (run) void this.refreshRun();
		if (fleetToo) void fleet.refresh();
		if (files) void this.refreshFiles();
		if (services) void this.refreshServices();
	}

	async refreshRun() {
		const asked = ++this.#runAsked;
		try {
			const { run } = await api.runs.get(this.runId);
			if (asked < this.#runShown) return;
			this.#runShown = asked;
			this.run = run;
			this.loadError = '';
		} catch (e) {
			if (!this.run) this.loadError = errorMessage(e);
		}
	}

	async refreshFiles() {
		const asked = ++this.#filesAsked;
		try {
			const f = await api.runs.files(this.runId);
			if (asked < this.#filesShown) return;
			this.#filesShown = asked;
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
			this.portLinks = pr.links;
		} catch {
			/* transient */
		}
	}

	async refreshServices() {
		try {
			this.services = await api.runs.services(this.runId);
			this.servicesError = '';
		} catch (e) {
			this.servicesError = errorMessage(e);
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

	/** Hold the services feed (Services view, Env popover) while on screen. */
	useServices(): () => void {
		this.#serviceUsers++;
		void this.refreshServices();
		return () => this.#serviceUsers--;
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
			// The run's events name a block's stages by their expanded ids.
			const doc = expandDoc(normalizeDoc(parseYaml(yaml)));
			// Which phases the strip shows is the view's call (shownStages).
			this.stageDefs = successOrder(doc).map((s) => ({
				id: s.id,
				type: s.type,
				agent: s.agent,
				phase: doc.phases?.length ? s.phase : undefined
			}));
			this.opensMRs = doc.stages.some((s) => s.type === 'action' && s.action === 'create_mr');
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
			this.error = errorMessage(e);
			return false;
		}
	}

	/** Take over the running agent's session (the composer's Take over);
	 *  force stops its turn in flight. */
	takeOver(force: boolean): Promise<boolean> {
		return this.act(() => api.runs.takeOver(this.runId, force));
	}

	/** Hand the task back. The hold closes when the agent's turn in flight
	 *  ends; until the engine records it the composer shows "handing back". */
	async release(): Promise<boolean> {
		this.releasing = true;
		const ok = await this.act(() => api.runs.release(this.runId));
		if (!ok) this.releasing = false;
		return ok;
	}

	async answer(
		pingId: string,
		ans: string,
		note?: string,
		attachmentIds?: string[],
		content?: FormContent
	) {
		this.answering = true;
		this.error = '';
		try {
			await api.pings.answer(pingId, {
				answer: ans,
				feedback: note?.trim() || undefined,
				attachmentIds,
				content
			});
			toast(t('run.store.answerSent'));
			void fleet.refresh();
		} catch (e) {
			this.error = errorMessage(e);
		} finally {
			this.answering = false;
		}
	}

	/** Snooze the held-hosts banner without ruling on them: they stay held
	 *  in the Egress view, and a newly held host raises it again. */
	dismissDenials() {
		this.dismissed = new Set(this.denials.map((d) => d.host));
	}
}
