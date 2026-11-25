// The app's top-level destinations, once. The sidebar, the mobile tab bar,
// the command palette and the "G then …" chords all read this list, so a
// renamed page or a new section changes one entry.

import { to } from '$lib/core/urls';
import type { GlyphName } from '$lib/design/glyphs';
import { t } from '$lib/i18n/index.svelte';
import { WORK_ITEMS } from './workItems';

export interface NavItem {
	id:
		| 'desk'
		| 'inbox'
		| 'notices'
		| 'runs'
		| 'warRoom'
		| 'tickets'
		| 'mrs'
		| 'skills'
		| 'analytics'
		| 'system';
	label: string;
	/** Its SVG icon ($lib/design/glyphs). */
	icon: GlyphName;
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
		icon: 'desk',
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
		icon: 'inbox',
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
		icon: 'notices',
		href: () => to.notices(),
		mobile: false,
		match: (p) => p.startsWith(to.notices())
	},
	{
		id: 'runs',
		get label() {
			return t('nav.runs');
		},
		icon: 'runs',
		href: () => to.runs(),
		chord: 'r',
		mobile: true,
		match: (p) => p.startsWith('/runs')
	},
	{
		id: 'warRoom',
		get label() {
			return t('nav.warRoom');
		},
		icon: 'warRoom',
		href: () => to.warRoom(),
		chord: 'w',
		mobile: false,
		match: (p) => p.startsWith(to.warRoom())
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
		id: 'skills',
		get label() {
			return t('nav.skills');
		},
		icon: 'skills',
		href: () => to.skills(),
		mobile: false,
		match: (p) => p.startsWith(to.skills())
	},
	{
		id: 'analytics',
		get label() {
			return t('nav.analytics');
		},
		icon: 'analytics',
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
		icon: 'system',
		href: () => to.system(),
		mobile: false,
		match: (p) => p.startsWith('/system')
	}
];

/** The destination a "G then <key>" chord goes to. */
export const navByChord = (key: string) => NAV.find((n) => n.chord === key.toLowerCase());
