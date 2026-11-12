// A run: one attempt at a ticket. Frontend-owned — the shape this UI wants,
// not the shape the wire happens to have. Nothing here imports from
// $lib/api/generated; the mappers translate.

import type { PhaseKind } from '$lib/domain/flowdoc';
import type { SkillsetPin, SkillsetRef } from './skill';

/** Every state a run can be in. Exhaustive: a new one is a type error here. */
export type RunState =
	| 'pending'
	| 'provisioning'
	| 'running'
	| 'waiting_on_human'
	/** Between phases, merge requests open: reviewers have it, watchers
	 *  start when their signals hold. */
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
	/** The merge request someone opened that this run was created on. */
	adopted?: { repo: string; iid: number; url: string; author?: string; title?: string };
	/** The run's repositories, the primary first. */
	checkouts: RunCheckout[];
	/** The phase in progress — or the last one while idle — and the
	 *  checkout it acts on ('' when it acts on no merge request). */
	phase?: string;
	phaseSubject?: string;
	/** The parameter values it runs with and who began it (run_created). */
	params?: Record<string, string>;
	startedBy?: string;
	/** The skillset versions installed in its workspace (run_created). */
	skillsets?: SkillsetPin[];
	/** Idle: between phases, or parked at a wait point. */
	idle: boolean;
	/** The take-over in force: someone holds the agent's session. */
	hold?: RunHold;
	envId?: string;
	envStatus?: EnvStatus;
	/** The devcontainer configuration changed since the workspace's container
	 *  was built: a rebuild would pick it up. */
	envDirty: boolean;
	/** Why a paused run is paused: the pause button ('user'), or the flow's
	 *  budget ('budget', with the ceiling that tripped). */
	pause?: { reason: string; note?: string };
	/** The run ended because its workspace failed to come up: a retry
	 *  provisions it again. */
	provisionFailed: boolean;
	createdAt: string;
	updatedAt: string;
	/** What the run has spent and how its time went, folded from its events. */
	facts: RunFacts;
}

/** The run's analytics accumulator as the lists need it. */
export interface RunFacts {
	costUsd: number;
	turns: number;
	/** Queued + provisioning + active + waiting on a human + held + paused. */
	wallMs: number;
	/** Stage attempts started. */
	attempts: number;
	endedAt?: string;
	/** Stage id → its attempt that has not ended yet. */
	open: Record<string, { iteration: number; startedAt: string; outcome: string }>;
}

/** A take-over as the run page shows it: who holds the agent's session,
 *  since when, and what the hold has banked so far across parks. */
export interface RunHold {
	stageId: string;
	userId?: string;
	since: string;
	turns: number;
	messages: number;
	costUsd: number;
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

/** A phase of the run's flow — its main chain, a watcher or an outcome:
 *  what opens it, its runs so far, its live run and the signals holding for
 *  it (FLOWLANG §1). */
export interface RunPhase {
	id: string;
	description?: string;
	kind: PhaseKind;
	/** What opens it: manual (the run page), or a signal observed on the
	 *  run's merge requests; empty for the main chain, which runs when the
	 *  run is started. */
	signal: string;
	/** The policy when the signal holds: auto, confirm (an inbox decision
	 *  first) or manual (surfaced as pending; you start it). */
	start: 'auto' | 'confirm' | 'manual';
	/** How an outcome ends the run: done or abort. */
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

/** A merge request as its repository host reports it right now (not run history). */
export interface MergeRequestLive {
	repo: string;
	branch: string;
	url: string;
	state: 'open' | 'merged' | 'closed' | '';
	/** 'unknown' while the repository host is still computing — never a conflict. */
	mergeable: 'clean' | 'conflicts' | 'unknown' | '';
	target?: string;
	headSha?: string;
	/** Unresolved threads whose last word is a reviewer's. */
	pendingThreads: number;
	lastCommentAt?: Date;
	/** The repository host would merge it now: no conflict, required approvals and
	 *  checks in, not a draft. */
	mergeableNow: boolean;
	draft: boolean;
	/** The head commit's CI, for an open one. */
	checks?: MergeRequestChecks;
	/** Its reviews' verdict, for an open one. */
	approval?: { approved: boolean; by: string[] };
	/** The open merge request whose branch this open one targets. */
	stackedOn?: MergeRequestBase;
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
	/** Parameter values overriding the workshop's bindings for this attempt. */
	params?: Record<string, string>;
	/** Start the run on a merge request someone opened (a flow with
	 *  operates_on: merge_request): its number, in the primary unless repo
	 *  says otherwise. */
	mr?: { repo?: string; iid: number };
	/** Skillsets to install into the workspace. On a rerun, undefined keeps
	 *  the original's (at their latest), an empty list clears them. */
	skillsets?: SkillsetRef[];
}

/** An open merge request of a project's repository with the runs done on
 *  it: whether the factory itself opened it, its ticket and that ticket's
 *  newest run. */
export interface MergeRequestRow {
	mr: OpenMergeRequest;
	/** The open merge request whose branch it targets: it merges there. */
	stackedOn?: MergeRequestBase;
	/** The newest run that went through it. */
	run?: { id: string; state: RunState; flow: string; createdAt: string };
	/** How many runs have it in their data: started on it, or opened it. A
	 *  merge request belongs to no ticket; its runs are found this way. */
	runs: number;
}

/** The open merge request another is stacked on — the one whose branch it
 *  targets, so merging the stacked one lands in that branch. */
export interface MergeRequestBase {
	iid: number;
	url: string;
	title: string;
	branch: string;
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
	/** Its description: what a run started on it is given. */
	body: string;
	/** A run starts only on an open one. */
	state: 'open' | 'merged' | 'closed';
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
	/** Whether the engine hands out browser links to ports. When not (a
	 *  cluster without an apps domain), ports are reached with
	 *  `apothecary forward`, which works either way. */
	links: boolean;
}

/** How a person reaches a run's workspace over SSH (TODO 46): through the
 *  engine's door, logging in as the workspace with one of their registered
 *  keys. `enabled` false means the instance has no door, and `reason` says
 *  so; the other facts are then absent. */
export interface RunSsh {
	enabled: boolean;
	reason?: string;
	host?: string;
	port?: number;
	/** The login name: the workspace's name. */
	user?: string;
	/** The folder an editor opens there, when the sidecar could say it. */
	path?: string;
	/** The door's host key fingerprint (SHA256:…), to check on first connect. */
	fingerprint?: string;
	/** The door's host public key: with the host, a known_hosts line. */
	hostKey?: string;
	/** The ssh command that opens a shell in the workspace. */
	command?: string;
	/** The one-time command that makes `command` work, when there is one:
	 *  `apothecary ssh-config`, which routes `<workspace>.apothecary` through
	 *  the engine's HTTPS address. */
	setup?: string;
	/** The vscode:// link that opens the workspace in desktop VS Code (Remote-SSH). */
	vscodeUrl?: string;
	/** The extension ids the workspace's own editor runs — what the desktop
	 *  can carry over (remote.defaultExtensionsIfInstalledLocally). */
	extensions?: string[];
	/** How many keys the signed-in user has registered; none means the door
	 *  lets nobody in yet. */
	keys: number;
}
