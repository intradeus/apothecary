// The work items runs are started on — a ticket, a merge request — once.
// Each kind is a value of a flow's operates_on (the engine's workitem
// package registers the same list). The sidebar, the New run dialog and the
// command palette read this list, so a new kind is one entry here and its
// pages: nothing that lists, picks or starts runs names a kind.

import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { to } from '$lib/core/urls';
import { I } from '$lib/design/icons';
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

export interface WorkItemKind {
	/** The flow language's operates_on value. */
	kind: OperatesOn;
	/** Its sidebar destination's id. */
	navId: 'tickets' | 'mrs';
	/** Plural, for the sidebar and its list page. */
	readonly label: string;
	/** Singular, for the New run dialog's tab and picker. */
	readonly one: string;
	icon: string;
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
	/** Where to make one when there is none. */
	create?: { readonly label: string; href: (projectId: string) => string };
	/** The items to pick from, newest first: within projectId ('' = every
	 *  workshop, for a kind that is not perWorkshop). */
	options: (projectId: string) => Promise<WorkItemOption[]>;
}

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
		icon: I.tickets,
		chord: 't',
		mobile: true,
		list: () => to.tickets(),
		match: (p) => p.startsWith('/tickets'),
		perWorkshop: false,
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
			},
			href: (projectId) => to.newTicket(projectId ? { project: projectId } : undefined)
		},
		options: async (projectId) =>
			(await api.tickets.list())
				.filter((tk) => !projectId || tk.projectId === projectId)
				.sort((a, b) => b.createdAt.localeCompare(a.createdAt))
				.map((tk) => ({
					value: tk.id,
					label: tk.title,
					detail: projectId ? undefined : fleet.projectName(tk.projectId),
					href: to.ticket(tk.id)
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
		icon: I.branch,
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
export const workItem = (kind: OperatesOn) => WORK_ITEMS.find((w) => w.kind === kind)!;
