// A ticket: a unit of work that owns many runs. The flow is NOT here — it is
// chosen per run.

export interface Ticket {
  id: string;
  projectId: string;
  title: string;
  goal: string;
  /** Branch stem; each run gets baseBranch + a per-run suffix. */
  baseBranch: string;
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
}

/** What creating or updating a ticket needs. Supplying source turns a create
 *  into an import: the tracker provides title, goal and attachments, and
 *  anything sent alongside overrides them. */
export interface TicketDraft {
  title: string;
  goal: string;
  projectId: string;
  branch?: string;
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
}

export interface SourceAttachment {
  filename: string;
  mime?: string;
  size?: number;
}
