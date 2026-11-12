// The app's top-level destinations, once. The sidebar, the mobile tab bar,
// the command palette and the "G then …" chords all read this list, so a
// renamed page or a new section changes one entry.

import { to } from '$lib/core/urls';
import { t } from '$lib/i18n/index.svelte';
import { I } from '$lib/design/icons';
import { WORK_ITEMS } from './workItems';

export interface NavItem {
	id: 'desk' | 'inbox' | 'notices' | 'runs' | 'tickets' | 'mrs' | 'analytics' | 'system';
	label: string;
	icon: string;
	href: () => string;
	/** The second key of its "G then …" chord. */
	chord?: string;
	/** On the mobile tab bar (the rest live in the drawer). */
	mobile: boolean;
	/** Whether a path is inside this destination. */
	match: (path: string) => boolean;
}

export const NAV: readonly NavItem[] = [
	{
		id: 'desk',
		get label() {
			return t('nav.desk');
		},
		icon: I.desk,
		href: () => to.desk(),
		chord: 'd',
		mobile: true,
		match: (p) => p === to.desk()
	},
	{
		id: 'inbox',
		get label() {
			return t('nav.inbox');
		},
		icon: I.inbox,
		href: () => to.inbox(),
		chord: 'i',
		mobile: true,
		match: (p) => p.startsWith(to.inbox())
	},
	{
		id: 'notices',
		get label() {
			return t('nav.notices');
		},
		icon: I.notices,
		href: () => to.notices(),
		mobile: false,
		match: (p) => p.startsWith(to.notices())
	},
	{
		id: 'runs',
		get label() {
			return t('nav.runs');
		},
		icon: I.runs,
		href: () => to.runs(),
		chord: 'r',
		mobile: true,
		match: (p) => p.startsWith('/runs')
	},
	// The work items runs start on, in their registry's order.
	...WORK_ITEMS.map((w): NavItem => ({
		id: w.navId,
		get label() {
			return w.label;
		},
		icon: w.icon,
		href: w.list,
		chord: w.chord,
		mobile: w.mobile,
		match: w.match
	})),
	{
		id: 'analytics',
		get label() {
			return t('nav.analytics');
		},
		icon: I.analytics,
		href: () => to.analytics(),
		chord: 'a',
		mobile: false,
		match: (p) => p.startsWith('/analytics')
	},
	{
		id: 'system',
		get label() {
			return t('nav.system');
		},
		icon: I.system,
		href: () => to.system(),
		mobile: false,
		match: (p) => p.startsWith('/system')
	}
];

/** The destination a "G then <key>" chord goes to. */
export const navByChord = (key: string) => NAV.find((n) => n.chord === key.toLowerCase());
