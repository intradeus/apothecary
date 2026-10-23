// The semantic hues (DESIGN 2.2) and the one map from each domain notion to
// its hue. Components colour things by asking here, never by keeping their
// own table — a redesign that moves "command" from green to teal changes
// one line.
//
// A Hue is a token name: `var(--${hue})`, `.hue-${hue}` (text),
// `.t-${hue}` (tinted pill), `.h-${hue}` (sets --h for washes and accents).

export type Hue = 'az' | 'amb' | 'grn' | 'ros' | 'vio' | 'tea' | 'mut' | 'brand';

/** Flow stage types (DESIGN 3.3). */
export const STAGE_HUE: Record<string, Hue> = {
	agent: 'az',
	command: 'grn',
	human_gate: 'amb',
	gate: 'amb',
	action: 'vio',
	wait: 'tea',
	include: 'brand'
};
/** The chip word for a stage type ("human_gate" reads "gate"). */
export const STAGE_WORD: Record<string, string> = { human_gate: 'gate' };

/** Flow edge kinds (DESIGN 3.6). */
export const EDGE_HUE: Record<string, Hue> = {
	success: 'grn',
	on_fail: 'ros',
	on_timeout: 'tea',
	on_reject: 'amb'
};

/** Agent tool-call kinds (DESIGN 3.7). */
export const TOOL_HUE: Record<string, Hue> = {
	read: 'az',
	edit: 'amb',
	search: 'vio',
	execute: 'grn',
	exec: 'grn',
	delete: 'ros',
	move: 'tea',
	fetch: 'az',
	think: 'mut',
	other: 'mut'
};

/** Git change status letters (DESIGN 4.13 file list). */
export const GIT_HUE: Record<string, Hue> = {
	A: 'grn',
	M: 'amb',
	D: 'ros',
	R: 'vio',
	C: 'tea',
	'?': 'mut',
	U: 'mut'
};

/** Tone of a timeline / status line. */
export type Tone = 'ok' | 'bad' | 'warn' | 'info' | 'mut';
export const TONE_HUE: Record<Tone, Hue> = {
	ok: 'grn',
	bad: 'ros',
	warn: 'amb',
	info: 'az',
	mut: 'mut'
};

export const hueOf = (map: Record<string, Hue>, key: string, fallback: Hue = 'mut'): Hue =>
	map[key] ?? fallback;
