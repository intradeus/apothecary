// A ticket: a unit of work that owns many runs. The flow is NOT here — it is
// chosen per run.

/** What a ticket is (ports.ChangeType); its base branch is named after it,
 *  and a flow's `run_if: run.ticket.change_type == fix` reads it. */
export type ChangeType = 'feat' | 'fix' | 'hotfix' | 'perf' | 'docs' | 'chore';
export const CHANGE_TYPES: ChangeType[] = ['feat', 'fix', 'hotfix', 'perf', 'docs', 'chore'];
export const asChangeType = (s: string | undefined): ChangeType =>
	CHANGE_TYPES.includes(s as ChangeType) ? (s as ChangeType) : 'feat';

export interface Ticket {
	id: string;
	projectId: string;
	title: string;
	goal: string;
	/** Branch stem; each run gets baseBranch + a per-run suffix. */
	baseBranch: string;
	changeType: ChangeType;
	attachmentIds: string[];
	/** Provenance of an imported ticket (absent when typed in by hand). A
	 *  record of the copy, not a live link: nothing is written back. */
	source?: TicketSource;
	/** What an import could not carry over (a skipped attachment).
	 *  Create-time only, never stored. */
	warnings: string[];
	createdAt: string;
	updatedAt: string;
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
	/** The base branch an import names from it: {changeType}/{key}-{title}. */
	branch: string;
}

export interface SourceAttachment {
	filename: string;
	mime?: string;
	size?: number;
}
