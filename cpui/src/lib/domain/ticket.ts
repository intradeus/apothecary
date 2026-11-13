// A ticket: a unit of work that owns many runs. The flow is NOT here — it is
// chosen per run.

/** What a ticket is (ports.ChangeType); its base branch is named after it,
 *  and a flow's `run_if: run.ticket.change_type == fix` reads it. */
export type ChangeType = 'feat' | 'fix' | 'hotfix' | 'perf' | 'docs' | 'chore';
export const CHANGE_TYPES: ChangeType[] = ['feat', 'fix', 'hotfix', 'perf', 'docs', 'chore'];
export const asChangeType = (s: string | undefined): ChangeType =>
	CHANGE_TYPES.includes(s as ChangeType) ? (s as ChangeType) : 'feat';

/** Where a tracker status sits in the work: the step a flow moves the ticket
 *  to. Each step is held by at most one status of a tracker. */
export type FlowStep = 'todo' | 'in_progress' | 'in_review' | 'done' | 'blocked';
export const FLOW_STEPS: FlowStep[] = ['todo', 'in_progress', 'in_review', 'done', 'blocked'];
export const asFlowStep = (s: string | undefined): FlowStep | undefined =>
	FLOW_STEPS.includes(s as FlowStep) ? (s as FlowStep) : undefined;

/** How the last fetch from the tracker went: '' never synced (or not
 *  sourced), 'gone' the remote issue no longer exists. */
export type SyncState = '' | 'ok' | 'error' | 'gone';

/** What a manual sync of a tracker did. */
export interface TrackerSyncCounts {
	tickets: number;
	synced: number;
	gone: number;
	failed: number;
	busy: number;
	/** Skipped because the tracker asked us to slow down. */
	rateLimited: number;
}

export interface Ticket {
	id: string;
	projectId: string;
	title: string;
	goal: string;
	/** Branch stem; each run gets baseBranch + a per-run suffix. */
	baseBranch: string;
	changeType: ChangeType;
	attachmentIds: string[];
	/** The remote issue this ticket mirrors (absent for a ticket on the
	 *  user's internal tracker). Title, goal and type follow the remote. */
	source?: TicketSource;
	/** Missing on an imported ticket that has not synced yet. */
	status?: TicketStatusRef;
	type?: TicketTypeRef;
	labels: string[];
	links: TicketLink[];
	syncState: SyncState;
	syncError?: string;
	syncedAt?: string;
	statusChangedAt?: string;
	statusChangedBy?: string;
	/** What an import could not carry over (a skipped attachment).
	 *  Create-time only, never stored. */
	warnings: string[];
	createdAt: string;
	updatedAt: string;
}

export interface TicketStatusRef {
	id: string;
	name: string;
	isDone: boolean;
	flowStep?: FlowStep;
}

export interface TicketTypeRef {
	id: string;
	name: string;
}

export interface TicketLink {
	url: string;
	title: string;
}

/** Who wrote a comment: the remote tracker (anyone there, including you
 *  through Apothecary), you on the internal tracker, or a run. */
export type CommentOrigin = 'remote' | 'user' | 'apothecary';

export interface TicketComment {
	id: string;
	author: string;
	bodyMd: string;
	origin: CommentOrigin;
	runId?: string;
	stageId?: string;
	createdAt: string;
	updatedAt: string;
}

/** One status of a tracker. On a remote tracker only isDone and flowStep are
 *  ours to set; on the internal tracker the whole list is. */
export interface TrackerStatus {
	/** Empty on an internal row not saved yet. */
	id: string;
	integrationId: string;
	remoteId: string;
	name: string;
	remoteCategory: string;
	position: number;
	isDone: boolean;
	flowStep?: FlowStep;
}

export interface TrackerType {
	id: string;
	integrationId: string;
	remoteId: string;
	name: string;
	changeType: ChangeType;
	position: number;
}

/** A ticket with no status (not synced yet) counts as not done. */
export const isDone = (t: Pick<Ticket, 'status'>): boolean => t.status?.isDone ?? false;

/** The steps more than one status claims — a tracker may give each only once. */
export function duplicateSteps(statuses: Pick<TrackerStatus, 'flowStep'>[]): FlowStep[] {
	const seen = new Set<FlowStep>();
	const dup = new Set<FlowStep>();
	for (const s of statuses) {
		if (!s.flowStep) continue;
		if (seen.has(s.flowStep)) dup.add(s.flowStep);
		seen.add(s.flowStep);
	}
	return FLOW_STEPS.filter((s) => dup.has(s));
}

/** The integration whose statuses a ticket moves through: its source, else
 *  the user's internal tracker. */
export function trackerIdOf(
	ticket: Pick<Ticket, 'source'>,
	trackers: { id: string; kind: string }[]
): string | undefined {
	if (ticket.source) return ticket.source.integrationId;
	return trackers.find((i) => i.kind === INTERNAL_TRACKER)?.id;
}

/** The integration kind of the built-in tracker every user has one of. */
export const INTERNAL_TRACKER = 'internal';

/** The statuses a ticket can be set to, in tracker order. Its current status
 *  stays selectable even when the list no longer has it. */
export function statusOptions(
	statuses: TrackerStatus[],
	current?: TicketStatusRef
): TicketStatusRef[] {
	const opts: TicketStatusRef[] = [...statuses]
		.sort((a, b) => a.position - b.position)
		.map((s) => ({ id: s.id, name: s.name, isDone: s.isDone, flowStep: s.flowStep }));
	if (current && !opts.some((o) => o.id === current.id)) opts.unshift(current);
	return opts;
}

/** The distinct status names across a list, for its filter, in first-seen order. */
export function statusNames(tickets: Pick<Ticket, 'status'>[]): string[] {
	return [...new Set(tickets.flatMap((t) => (t.status ? [t.status.name] : [])))];
}

/** A ticket list narrowed to one status name; '' keeps all. */
export function withStatus<T extends Pick<Ticket, 'status'>>(tickets: T[], name: string): T[] {
	return name ? tickets.filter((t) => t.status?.name === name) : tickets;
}

export interface TicketSource {
	integrationId: string;
	kind: string; // 'jira'
	key: string; // 'PROJ-123'
	url?: string;
	/** The tracker's own word for the issue's type ('Bug'). */
	type?: string;
}

/** What creating or updating a ticket needs. Supplying source turns a create
 *  into an import: the tracker provides title, goal and attachments, and
 *  anything sent alongside overrides them. */
export interface TicketDraft {
	title: string;
	goal: string;
	projectId: string;
	branch?: string;
	changeType?: ChangeType;
	attachmentIds?: string[];
	sourceIntegrationId?: string;
	sourceKey?: string;
}

/** One issue in an external tracker, as the import picker shows it. Search
 *  results carry no attachments — the single-issue fetch is authoritative. */
export interface SourceTicket {
	key: string;
	title: string;
	body?: string;
	url?: string;
	status?: string;
	type?: string;
	assignee?: string;
	updated?: string;
	attachments: SourceAttachment[];
	/** What an import makes of the issue's type. */
	changeType: ChangeType;
}

export interface SourceAttachment {
	filename: string;
	mime?: string;
	size?: number;
}
