// A ticket: a unit of work that owns many runs. The flow is NOT here — it is
// chosen per run. Tickets live in the workshop's ticket source (a remote
// tracker or the owner's internal one) and are read from it live; only a
// small local record (the Ticket below) is kept, for runs to point at.

import type { Attachment } from './files';

/** What a ticket is (ports.ChangeType); its base branch is named after it,
 *  and a flow's `run_if: run.ticket.change_type == fix` reads it. */
export type ChangeType = 'feat' | 'fix' | 'hotfix' | 'perf' | 'docs' | 'chore';
export const CHANGE_TYPES: ChangeType[] = ['feat', 'fix', 'hotfix', 'perf', 'docs', 'chore'];
export const asChangeType = (s: string | undefined): ChangeType =>
	CHANGE_TYPES.includes(s as ChangeType) ? (s as ChangeType) : 'feat';

/** The integration kind of the built-in tracker every user has one of. */
export const INTERNAL_TRACKER = 'internal';

/** One issue of a workshop's source as the tracker shows it now. On the
 *  internal tracker the key is the ticket's own id. The record fields
 *  (ticketId, baseBranch, activeRunId) are '' while the issue has no local
 *  record — nothing has needed one yet. */
export interface TicketRow {
	/** What the ticket's address carries: the tracker's key ('PROJ-123'). */
	key: string;
	/** The tracker's stable id for the issue (a key can change). */
	remoteId: string;
	title: string;
	body: string;
	url: string;
	/** The tracker's status name; '' when it has none. */
	status: string;
	statusId: string;
	done: boolean;
	/** The tracker's word for the issue's type ('Bug'). */
	type: string;
	typeId: string;
	/** What the source's type list makes of the type. */
	changeType: ChangeType;
	labels: string[];
	assignee: string;
	updatedAt?: string;
	/** The local record's id. */
	ticketId: string;
	/** Branch stem; each run gets baseBranch + a per-run suffix. */
	baseBranch: string;
	/** The ticket's active run in any workshop: one at a time across them. */
	activeRunId: string;
}

/** The integration a ticket comes from. */
export interface TicketSourceRef {
	id: string;
	kind: string;
	name: string;
}

export interface TicketLink {
	/** The linked ticket's record. */
	ticketId: string;
	url: string;
	title: string;
}

/** One ticket, as its page shows it. */
export interface TicketDetail extends TicketRow {
	source: TicketSourceRef;
	/** On the owner's internal tracker: everything is editable here. A
	 *  remote issue takes only its base branch, status, labels and comments. */
	internal: boolean;
	attachmentIds: string[];
	links: TicketLink[];
}

/** Which tickets a list shows, by whether the tracker counts them done. */
export type TicketDoneFilter = 'open' | 'done' | 'any';

/** One page of a workshop's list. next is the cursor of the page after it;
 *  '' on the last. */
export interface TicketPage {
	tickets: TicketRow[];
	next: string;
}

/** What a workshop's list asks its source for. refresh reads the tracker
 *  now rather than the cached copy. */
export interface TicketQuery {
	q?: string;
	done?: TicketDoneFilter;
	cursor?: string;
	limit?: number;
	refresh?: boolean;
}

/** A ticket's local record: what runs, comments, attachments and links
 *  point at. A remote issue gets one the first time something needs it (a
 *  run starts, an automation fires, a base branch is set) and it is never
 *  polled, so its title is the last one seen. */
export interface Ticket {
	id: string;
	/** The ticket-source integration, and the issue's stable id there (the
	 *  ticket's own id on the internal tracker). */
	sourceId: string;
	sourceKind: string;
	remoteId: string;
	/** The remote issue's key, URL and type word as last seen. */
	sourceKey?: string;
	sourceUrl?: string;
	sourceType?: string;
	title: string;
	goal: string;
	baseBranch: string;
	changeType: ChangeType;
	statusId?: string;
	typeId?: string;
	labels: string[];
	/** The automation that made it, when one did. */
	automationId?: string;
	statusChangedAt?: string;
	statusChangedBy?: string;
	createdAt: string;
	updatedAt: string;
}

export interface WorkshopRef {
	id: string;
	name: string;
}

/** A record with where to show it: the ticket belongs to no workshop, so
 *  its page is reached through any workshop on its source. */
export interface TicketRecord {
	ticket: Ticket;
	key: string;
	source?: TicketSourceRef;
	/** The workshops whose source the ticket is on. */
	workshops: WorkshopRef[];
}

/** Who wrote a comment: the remote tracker (anyone there, including you
 *  through Apothecary), you on the internal tracker, or a run. */
export type CommentOrigin = 'remote' | 'user' | 'apothecary';

export interface TicketComment {
	id: string;
	author: string;
	bodyMd: string;
	origin: CommentOrigin;
	/** The run that wrote it (internal tracker). */
	runId?: string;
	createdAt: string;
	updatedAt: string;
}

/** A remote issue's own attachment: listed, copied into a run when it
 *  starts. */
export interface RemoteAttachment {
	filename: string;
	mime?: string;
	size?: number;
}

/** A ticket's files: those uploaded here, and the remote issue's own. */
export interface TicketAttachments {
	attachments: Attachment[];
	remote: RemoteAttachment[];
}

/** What creating or editing an internal ticket sends; a remote issue takes
 *  only baseBranch. */
export interface TicketDraft {
	title: string;
	goal: string;
	baseBranch?: string;
	changeType?: ChangeType;
	/** Create only: the status and labels it starts with. */
	statusId?: string;
	labels?: string[];
	typeId?: string;
	attachmentIds?: string[];
}

/** A ticket's status as a picker offers it. A status is set by its name. */
export interface TicketStatusRef {
	name: string;
	isDone: boolean;
}

/** One status of a tracker. A remote tracker's are read live and read-only;
 *  the internal tracker's are the owner's to edit. */
export interface TrackerStatus {
	/** Empty on an internal row not saved yet. */
	id: string;
	integrationId: string;
	remoteId: string;
	name: string;
	position: number;
	isDone: boolean;
}

export interface TrackerStatusList {
	statuses: TrackerStatus[];
	/** A remote tracker's: shown, not edited. */
	readOnly: boolean;
}

export interface TrackerType {
	id: string;
	integrationId: string;
	remoteId: string;
	name: string;
	changeType: ChangeType;
	position: number;
}

/** The statuses a ticket can be set to, in tracker order. Its current status
 *  stays selectable even when the list no longer has it. */
export function statusOptions(
	statuses: TrackerStatus[],
	current?: Pick<TicketRow, 'status' | 'done'>
): TicketStatusRef[] {
	const opts: TicketStatusRef[] = [...statuses]
		.sort((a, b) => a.position - b.position)
		.map((s) => ({ name: s.name, isDone: s.isDone }));
	if (current?.status && !opts.some((o) => o.name === current.status))
		opts.unshift({ name: current.status, isDone: current.done });
	return opts;
}

/** The distinct status names across a list, for its filter, in first-seen order. */
export function statusNames(tickets: Pick<TicketRow, 'status'>[]): string[] {
	return [...new Set(tickets.flatMap((t) => (t.status ? [t.status] : [])))];
}

/** A ticket list narrowed to one status name; '' keeps all. */
export function withStatus<T extends Pick<TicketRow, 'status'>>(tickets: T[], name: string): T[] {
	return name ? tickets.filter((t) => t.status === name) : tickets;
}
