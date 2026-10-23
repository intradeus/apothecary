// A run: one attempt at a ticket. Frontend-owned — the shape this UI wants,
// not the shape the wire happens to have. Nothing here imports from
// $lib/api/generated; the mappers translate.

/** Every state a run can be in. Exhaustive: a new one is a type error here. */
export type RunState =
	| 'pending'
	| 'provisioning'
	| 'running'
	| 'waiting_on_human'
	/** Between phases, merge requests open: reviewers have it, reaction
	 *  phases start when their triggers hold (PLAN6). */
	| 'in_review'
	| 'paused'
	| 'completed'
	| 'abandoned'
	| 'failed'
	| 'archived';

/**
 * The reconciled live container status, or an in-flight operation overlay
 * (provisioning/rebuilding/destroying) / a recently-failed one (error).
 * 'unknown' = the provisioner could not be reached. undefined = not yet
 * reconciled, and the badge falls back to whether the run has an env at all.
 */
export type EnvStatus =
	| 'running'
	| 'starting'
	| 'stopped'
	| 'absent'
	| 'unknown'
	| 'provisioning'
	| 'rebuilding'
	| 'destroying'
	| 'error';

export interface Run {
	id: string;
	state: RunState;
	title: string;
	flowName: string;
	flowHash: string;
	currentStage?: string;
	projectId?: string;
	/** Parent ticket; a ticket owns many runs. */
	ticketId?: string;
	/** The tracker issue the ticket was imported from ("PROJ-123") and its
	 *  page; absent on a ticket written by hand. */
	ticketKey?: string;
	ticketUrl?: string;
	/** Extra direction for THIS attempt, additive to the ticket's goal. */
	goal?: string;
	branch?: string;
	/** The branch this run forked off from (origin/<sourceBranch>). */
	sourceBranch?: string;
	mrUrl?: string;
	/** Every merge request the run opened: one per changed repo of a
	 *  multi-repo task. mrUrl is the first. */
	mrs: RunMergeRequest[];
	/** The run's repositories, the primary first. */
	checkouts: RunCheckout[];
	/** The phase in progress — or the last one while idle — and the
	 *  checkout it acts on ('' when it acts on no merge request). */
	phase?: string;
	phaseSubject?: string;
	/** The entrypoint the run began at, the parameter values it runs with
	 *  and who began it (run_created). */
	beginPhase?: string;
	params?: Record<string, string>;
	startedBy?: string;
	/** Idle: between phases, or parked at a wait point. */
	idle: boolean;
	envId?: string;
	envStatus?: EnvStatus;
	/** The devcontainer configuration changed since the workspace's container
	 *  was built: a rebuild would pick it up. */
	envDirty: boolean;
	createdAt: string;
	updatedAt: string;
}

export interface RunMergeRequest {
	/** Checkout name ("repo" = the primary). */
	repo: string;
	branch: string;
	/** The branch it merges into (the checkout's source branch). */
	target?: string;
	url: string;
	iid: number;
}

/** A phase of the run's flow: what opens it, its runs so far, its live run
 *  and the signals holding for it (FLOWLANG §1). */
export interface RunPhase {
	id: string;
	description?: string;
	/** What opens it: run_start (an entrypoint), manual (the run page), or a
	 *  forge signal. */
	signal: string;
	/** The policy when the signal holds: auto, confirm (an inbox decision
	 *  first) or manual (surfaced as pending; you start it). */
	start: 'auto' | 'confirm' | 'manual';
	/** A run may begin at this phase. */
	entry: boolean;
	/** The phase ends the run: it opens once, when its signal holds. `then`
	 *  is what a stage-less outcome does (done or abort). */
	outcome: boolean;
	then?: string;
	/** Debounce, in seconds (review comments). */
	quietForS?: number;
	/** Runs allowed per merge request; -1 = unlimited. */
	max: number;
	/** Runs so far, by checkout name ('' = on no merge request), and in all. */
	runs: Record<string, number>;
	total: number;
	/** The phase's run in progress, or parked at a wait point. */
	live?: { subject?: string; parkedAt?: string };
	/** The subjects its signal held for at the last poll. */
	holds: PhaseHold[];
}

/** One observation that a phase's signal holds for a merge request. */
export interface PhaseHold {
	subject: string;
	key: string;
	why: string;
	/** Already started or declined for this occurrence, or runs used up. */
	seen: boolean;
	observedAt: string;
}

/** A merge request as the forge reports it right now (not run history). */
export interface MergeRequestLive {
	repo: string;
	branch: string;
	url: string;
	state: 'open' | 'merged' | 'closed' | '';
	/** 'unknown' while the forge is still computing — never a conflict. */
	mergeable: 'clean' | 'conflicts' | 'unknown' | '';
	target?: string;
	headSha?: string;
	/** Unresolved threads whose last word is a reviewer's. */
	pendingThreads: number;
	lastCommentAt?: Date;
	/** The forge would merge it now: no conflict, required approvals and
	 *  checks in, not a draft. */
	mergeableNow: boolean;
	draft: boolean;
	/** The head commit's CI, for an open one. */
	checks?: MergeRequestChecks;
	/** Its reviews' verdict, for an open one. */
	approval?: { approved: boolean; by: string[] };
	error?: string;
}

/** A CI check's state, or the aggregate over a head's checks ('' = none
 *  reported). 'neutral' neither passes nor fails: skipped, cancelled. */
export type CheckState = '' | 'pending' | 'success' | 'failure' | 'neutral';

export interface MergeRequestChecks {
	head: string;
	state: CheckState;
	items: { name: string; state: CheckState; url?: string }[];
}

export interface RunCheckout {
	name: string;
	/** Relative to the workspace home: "repo", "repos/<name>". */
	dir: string;
	url: string;
	branch: string;
	/** The branch it forked from (what its changes are measured against). */
	source: string;
	primary: boolean;
}

/** One entry in a run's event log. Payloads stay open: the timeline renders
 *  per type, and pinning every payload shape here would be a second copy of
 *  the engine's event catalog. */
export interface RunEvent {
	runId: string;
	seq: number;
	ts: string;
	type: string;
	payload: Record<string, unknown>;
}

/** A run with the first page of its log; the SSE stream takes over after. */
export interface RunDetail {
	run: Run;
	events: RunEvent[];
}

/** Per-attempt input when starting a run from a ticket or restarting one.
 *  All optional: each field overrides what the ticket or project supplies,
 *  and none of them modify the ticket. */
export interface RunInput {
	flow?: string;
	goal?: string;
	/** Branch to fork off; empty = the project default. */
	sourceBranch?: string;
	attachmentIds?: string[];
	/** The entrypoint to begin at; empty = the flow's first. */
	phase?: string;
	/** Parameter values overriding the workshop's bindings for this attempt. */
	params?: Record<string, string>;
	/** Create the run on a merge request someone opened (an entrypoint on
	 *  mr_opened): its number, in the primary unless repo says otherwise. */
	mr?: { repo?: string; iid: number };
}

/** One row of the Reviews tab: an open merge request, whether the factory
 *  itself opened it, and the review done of it here — its ticket and that
 *  ticket's newest run. */
export interface ReviewRow {
	mr: OpenMergeRequest;
	mine: boolean;
	ticketId?: string;
	run?: { id: string; state: RunState; flow: string; createdAt: string };
	runs: number;
}

/** An open merge request of a project's repository, to create a run on. */
export interface OpenMergeRequest {
	iid: number;
	url: string;
	title: string;
	author: string;
	source: string;
	target: string;
	draft: boolean;
	fork: boolean;
}

/** One point-in-time reading of the workspace's resource usage (the whole
 *  sandbox: inner dockerd + workspace + compose siblings). Sampled ~10s; net
 *  counters are cumulative, so rates come from deltas. */
export interface ResourceSample {
	at: string;
	/** % of one core; the ceiling is cpus × 100. */
	cpuPerc: number;
	/** Host logical cores (the % denominator); 0 = unknown. */
	cpus: number;
	memUsed: number;
	memLimit: number;
	netRx: number;
	netTx: number;
	pids: number;
}

/** A port published through the relay. */
export interface ExposedPort {
	port: number;
	url: string;
}

/** A listener the in-workspace scanner found that nobody has exposed yet. */
export interface ObservedPort {
	port: number;
	process?: string;
	/** The compose sibling the port belongs to, when it is not the workspace. */
	service?: string;
}

export interface RunPorts {
	exposed: ExposedPort[];
	observed: ObservedPort[];
}
