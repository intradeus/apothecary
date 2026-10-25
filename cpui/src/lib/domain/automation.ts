// Automations: how runs begin without a person pressing Start. A rule of a
// workshop naming a flow (one whose operates_on fits its kind) and parameter
// overrides,
// plus how it fires — a tracker query polled into tickets and runs, or a
// schedule for one ticket. The watcher's last poll, firing and error ride
// along for the list.

export type AutomationKind = 'tracker_query' | 'cron' | 'merge_request';

export interface Automation {
	id: string;
	projectId: string;
	name: string;
	kind: AutomationKind;
	/** tracker_query: the ticket-source integration and its query. */
	sourceId?: string;
	query?: string;
	/** cron: five fields in UTC, and the ticket each firing runs. */
	schedule?: string;
	ticketId?: string;
	/** merge_request: the repository whose merge requests it watches (empty = the
	 *  primary). */
	repo?: string;
	flow: string;
	/** Overrides over the workshop's binding, for the runs it creates. */
	params: Record<string, string>;
	enabled: boolean;
	lastPolledAt?: string;
	lastFiredAt?: string;
	lastError?: string;
	createdAt: string;
	updatedAt: string;
}

/** What the form sends; the server validates it the way a firing would. */
export interface AutomationDraft {
	name: string;
	kind: AutomationKind;
	sourceId?: string;
	query?: string;
	schedule?: string;
	ticketId?: string;
	repo?: string;
	flow: string;
	params?: Record<string, string>;
	enabled: boolean;
}
