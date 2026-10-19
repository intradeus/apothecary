// Guilds (organisations) and the URLs of everything inside one.
//
// The engine has no organisations yet, so there is exactly one implicit
// guild. The UI is still built guild-scoped — /g/:guild/… routes, the
// switcher, per-guild desks — so real multi-tenancy later is engine work plus
// replacing GUILDS below with an API call, not a re-route.
//
// Every in-app link is built here. Components never hand-write "/g/…" paths.

import { page } from '$app/state';

export type GuildRole = 'Owner' | 'Maintainer' | 'Engineer' | 'Viewer';

export interface Guild {
	slug: string;
	name: string;
	/** Two letters on the avatar. */
	initials: string;
	/** The avatar's 160° gradient. */
	c1: string;
	c2: string;
	role: GuildRole;
}

export const DEFAULT_GUILD = 'default';

export const GUILDS: Guild[] = [
	{
		slug: DEFAULT_GUILD,
		name: 'Default guild',
		initials: 'DG',
		c1: '#9ff0c0',
		c2: '#3fae72',
		role: 'Owner'
	}
];

/** The signed-in operator. No user model on the engine yet: a local stand-in. */
export const ME = { name: 'Operator', initials: 'OP' };

export const guildBySlug = (slug: string | undefined): Guild =>
	GUILDS.find((g) => g.slug === slug) ?? GUILDS[0];

/** The guild the current route is scoped to (the default outside /g/…). */
export const currentGuild = (): Guild => guildBySlug(page.params.guild);

const g = (guild?: string) => `/g/${guild ?? page.params.guild ?? DEFAULT_GUILD}`;
const q = (params: Record<string, string | undefined>) => {
	const s = new URLSearchParams();
	for (const [k, v] of Object.entries(params)) if (v) s.set(k, v);
	const str = s.toString();
	return str ? `?${str}` : '';
};

export type WorkshopTab = 'overview' | 'runs' | 'tickets' | 'flows' | 'agents' | 'health';
export type SystemSection =
	'integrations' | 'channels' | 'members' | 'limits' | 'reaper' | 'storage';

/** Route builders. Pass `guild` only to link across guilds. */
export const to = {
	allGuilds: () => '/',
	desk: (guild?: string) => g(guild),
	inbox: (guild?: string) => `${g(guild)}/inbox`,
	ping: (pingId: string, guild?: string) => `${g(guild)}/inbox/${pingId}`,
	runs: (opts?: { project?: string; tab?: 'active' | 'finished' }, guild?: string) =>
		`${g(guild)}/runs${q({ project: opts?.project, tab: opts?.tab })}`,
	run: (runId: string, guild?: string) => `${g(guild)}/runs/${runId}`,
	tickets: (opts?: { project?: string }, guild?: string) =>
		`${g(guild)}/tickets${q({ project: opts?.project })}`,
	newTicket: (opts?: { project?: string }, guild?: string) =>
		`${g(guild)}/tickets/new${q({ project: opts?.project })}`,
	ticket: (ticketId: string, guild?: string) => `${g(guild)}/tickets/${ticketId}`,
	workshops: (guild?: string) => `${g(guild)}/w`,
	newWorkshop: (guild?: string) => `${g(guild)}/w/new`,
	workshop: (projectId: string, tab?: WorkshopTab, guild?: string) =>
		`${g(guild)}/w/${projectId}${tab && tab !== 'overview' ? `?tab=${tab}` : ''}`,
	workshopSettings: (projectId: string, guild?: string) => `${g(guild)}/w/${projectId}/settings`,
	flow: (projectId: string, name: string, guild?: string) =>
		`${g(guild)}/w/${projectId}/flows/${encodeURIComponent(name)}`,
	system: (section: SystemSection = 'integrations', guild?: string) =>
		`${g(guild)}/system/${section}`
};

// --- workshop identity ------------------------------------------------------

const HUES = ['#7ee2a8', '#7fb2ff', '#c3a2ff', '#5fd6cc', '#efe6cf', '#f5b54a'];

const hash = (s: string) => {
	let h = 2166136261;
	for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
	return h >>> 0;
};

/** A workshop's vial: a stable hue and a decorative fill level. */
export const workshopVial = (projectId: string): { hue: string; fill: number } => {
	const h = hash(projectId);
	return { hue: HUES[h % HUES.length], fill: 30 + ((h >>> 8) % 45) };
};
