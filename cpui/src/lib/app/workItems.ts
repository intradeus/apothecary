// The work items runs are started on — a ticket, a merge request — once.
// Each kind is a value of a flow's operates_on (the engine's workitem
// package registers the same list). The sidebar, the New run dialog and the
// command palette read this list, so a new kind is one entry here and its
// pages: nothing that lists, picks or starts runs names a kind.
//
// operates_on has one more value, `nothing`: a run with no work item behind
// it. It has no items to list or pick, so it is not here — the New run
// dialog starts such a run itself (NothingRunForm).

import { api } from '$lib/api';
import type { GlyphName } from '$lib/design/glyphs';
import { to } from '$lib/core/urls';
import type { OperatesOn } from '$lib/domain/flowdoc';
import { t } from '$lib/i18n/index.svelte';

/** One item a run can be started on, as the New run dialog offers it. */
export interface WorkItemOption {
	value: string;
	label: string;
	/** Under the label: the workshop, the author. */
	detail?: string;
	/** Its page, where a run is started on it. */
	href: string;
}

/** The operates_on values that name a kind of work item: all but `nothing`. */
export type WorkItemName = Exclude<OperatesOn, 'nothing'>;

export interface WorkItemKind {
	/** The flow language's operates_on value. */
	kind: WorkItemName;
	/** Its sidebar destination's id. */
	navId: 'tickets' | 'mrs';
	/** Plural, for the sidebar and its list page. */
	readonly label: string;
	/** Singular, for the New run dialog's tab and picker. */
	readonly one: string;
	/** Its SVG icon ($lib/design/glyphs). */
	icon: GlyphName;
	/** The second key of its "G then …" chord. */
	chord: string;
	/** On the mobile tab bar. */
	mobile: boolean;
	list: () => string;
	/** Whether a path is inside its pages. */
	match: (path: string) => boolean;
	/** Its items live in a workshop's repository: the dialog asks which
	 *  workshop first. */
	perWorkshop: boolean;
	/** What the New run dialog says of it, with none to pick, and when its
	 *  items cannot be read. */
	readonly intro: string;
	readonly none: string;
	readonly loadError: string;
	/** One can be written here when there is none: the label of the link,
	 *  shown only where the workshop's ticket source takes new ones. */
	create?: { readonly label: string };
	/** The workshop's items to pick from, in its source's order. */
	options: (projectId: string) => Promise<WorkItemOption[]>;
}

/** How many of a workshop's tickets a picker offers: the first page. */
const OPTIONS_LIMIT = 50;

export const WORK_ITEMS: readonly WorkItemKind[] = [
	{
		kind: 'ticket',
		navId: 'tickets',
		get label() {
			return t('nav.tickets');
		},
		get one() {
			return t('page.runs.newModeTicket');
		},
		icon: 'tickets',
		chord: 't',
		mobile: true,
		list: () => to.tickets(),
		match: (p) => p.startsWith('/tickets'),
		// A workshop's tickets are its source's, read live: pick the workshop first.
		perWorkshop: true,
		get intro() {
			return t('page.runs.newIntro');
		},
		get none() {
			return t('page.runs.newNone');
		},
		get loadError() {
			return t('page.runs.newLoadError');
		},
		create: {
			get label() {
				return t('page.desk.newTicket');
			}
		},
		options: async (projectId) =>
			(await api.projects.tickets(projectId, { limit: OPTIONS_LIMIT })).tickets.map((tk) => ({
				value: tk.key,
				label: tk.title,
				detail: tk.key,
				href: to.workshopTicket(projectId, tk.key)
			}))
	},
	{
		kind: 'merge_request',
		navId: 'mrs',
		get label() {
			return t('nav.mrs');
		},
		get one() {
			return t('page.runs.newModeMR');
		},
		icon: 'mergeRequests',
		chord: 'm',
		mobile: false,
		list: () => to.mergeRequests(),
		match: (p) => p.startsWith('/merge-requests'),
		perWorkshop: true,
		get intro() {
			return t('page.runs.newIntroMR');
		},
		get none() {
			return t('page.runs.newNoMR');
		},
		get loadError() {
			return t('page.runs.newMRLoadError');
		},
		options: async (projectId) =>
			(await api.projects.mergeRequests(projectId)).map((r) => ({
				value: String(r.mr.iid),
				label: `!${r.mr.iid} ${r.mr.title}`,
				detail: r.mr.author,
				href: to.mergeRequest(projectId, r.mr.iid)
			}))
	}
];

/** The kind operates_on names. */
export const workItem = (kind: WorkItemName) => WORK_ITEMS.find((w) => w.kind === kind)!;
