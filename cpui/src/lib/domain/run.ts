// A run: one attempt at a ticket. Frontend-owned — the shape this UI wants,
// not the shape the wire happens to have. Nothing here imports from
// $lib/api/generated; the mappers translate.

/** Every state a run can be in. Exhaustive: a new one is a type error here. */
export type RunState =
	| 'pending'
	| 'provisioning'
	| 'running'
	| 'waiting_on_human'
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
	/** Extra direction for THIS attempt, additive to the ticket's goal. */
	goal?: string;
	branch?: string;
	/** The branch this run forked off from (origin/<sourceBranch>). */
	sourceBranch?: string;
	mrUrl?: string;
	/** Every merge request the run opened, in review order: one per changed
	 *  repo of a multi-repo task, one per layer of a stacked change. mrUrl is
	 *  the first. */
	mrs: RunMergeRequest[];
	/** The run's repositories, the primary first. */
	checkouts: RunCheckout[];
	envId?: string;
	envStatus?: EnvStatus;
	createdAt: string;
	updatedAt: string;
}

export interface RunMergeRequest {
	/** Checkout name ("repo" = the primary). */
	repo: string;
	branch: string;
	/** The branch it merges into: the source branch, or the layer below. */
	target?: string;
	url: string;
	iid: number;
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
