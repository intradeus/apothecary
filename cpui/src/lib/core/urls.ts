// The URLs of everything in the app, built in one place. Components never
// hand-write a path.
//
// There is no organisation level (TODO 25): a person signs in and sees their
// own workshops, tickets, runs and pings, so a URL names the thing and
// nothing above it. Links an old /g/<guild>/… bookmark still carries are
// redirected by routes/g/[...rest].

const q = (params: Record<string, string | undefined>) => {
	const s = new URLSearchParams();
	for (const [k, v] of Object.entries(params)) if (v) s.set(k, v);
	const str = s.toString();
	return str ? `?${str}` : '';
};

export type WorkshopTab =
	'overview' | 'runs' | 'tickets' | 'mrs' | 'flows' | 'automations' | 'health';
export type SystemSection =
	| 'integrations'
	| 'providers'
	| 'skills'
	| 'channels'
	| 'account'
	| 'users'
	| 'limits'
	| 'reaper'
	| 'storage'
	| 'observability';

/** Route builders. */
export const to = {
	desk: () => '/',
	inbox: () => '/inbox',
	notices: () => '/notices',
	ping: (pingId: string) => `/inbox/${pingId}`,
	runs: (opts?: { project?: string; tab?: 'active' | 'finished' }) =>
		`/runs${q({ project: opts?.project, tab: opts?.tab })}`,
	run: (runId: string) => `/runs/${runId}`,
	tickets: (opts?: { project?: string }) => `/tickets${q({ project: opts?.project })}`,
	newTicket: (opts?: { project?: string }) => `/tickets/new${q({ project: opts?.project })}`,
	ticket: (ticketId: string) => `/tickets/${ticketId}`,
	// Merge requests, the other thing runs start on: across workshops, and
	// one of them — where a run is started on it. A repo other than the
	// primary is a query parameter.
	mergeRequests: (opts?: { project?: string }) => `/merge-requests${q({ project: opts?.project })}`,
	mergeRequest: (projectId: string, iid: number, repo?: string) =>
		`/merge-requests/${projectId}/${iid}${q({ repo })}`,
	workshops: () => '/w',
	newWorkshop: () => '/w/new',
	workshop: (projectId: string, tab?: WorkshopTab) =>
		`/w/${projectId}${tab && tab !== 'overview' ? `?tab=${tab}` : ''}`,
	workshopSettings: (projectId: string) => `/w/${projectId}/settings`,
	flow: (projectId: string, name: string) => `/w/${projectId}/flows/${encodeURIComponent(name)}`,
	system: (section: SystemSection = 'integrations') => `/system/${section}`,
	account: () => '/system/account',
	users: () => '/system/users',
	// The skill library (TODO 38): one's skills and skillsets, under System.
	skills: () => '/system/skills',
	newSkill: () => '/system/skills/new',
	skill: (skillId: string) => `/system/skills/${skillId}`,
	newSkillset: () => '/system/skills/sets/new',
	skillset: (skillsetId: string) => `/system/skills/sets/${skillsetId}`,
	// Run analytics (TODO 23): the user's dashboards and scorecards.
	analytics: () => '/analytics',
	dashboard: (id: string) => `/analytics/${encodeURIComponent(id)}`,
	scorecards: () => '/analytics/scorecards'
};

// --- workshop identity ------------------------------------------------------

// Token references, not colours: a vial follows the theme like everything else.
// Chart series cycle the same list.
export const HUES = [
	'var(--success)',
	'var(--running)',
	'var(--accent)',
	'var(--accent-2)',
	'var(--brand)',
	'var(--warning)'
];

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

/** Two letters for an avatar, from a name. */
export const initialsOf = (name: string): string => {
	const parts = name.trim().split(/\s+/).filter(Boolean);
	if (parts.length === 0) return '?';
	if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
	return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
};
