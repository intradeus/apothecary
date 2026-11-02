// The semantic hues (DESIGN 2.2) and the one map from each domain notion to
// its hue. Components colour things by asking here, never by keeping their
// own table: a redesign that moves "command" from green to teal changes one
// line.
//
// A Hue is a ROLE, never a colour: what a role looks like is decided in
// src/styles/themes.css. Every Hue has the same family of tokens and classes:
//   var(--${hue})  fills, dots, borders       .hue-${hue}   text in the hue
//   var(--${hue}-text|-bg|-line|-glow)        .tint-${hue}  tinted pill
//                                             .tone-${hue}  sets --tone for washes

export type Hue =
	| 'running' // something is brewing
	| 'attention' // a human must act
	| 'warning'
	| 'success'
	| 'danger'
	| 'accent' // categorical: no meaning of its own
	| 'accent-2'
	| 'muted'
	| 'brand';

/** Flow stage types (DESIGN 3.3). */
export const STAGE_HUE: Record<string, Hue> = {
	agent: 'running',
	command: 'success',
	human_gate: 'warning',
	gate: 'warning',
	// A person works the workspace: a human must act.
	manual: 'attention',
	action: 'accent',
	wait: 'accent-2',
	// A service is the project's, as a command is: the same hue.
	service: 'success',
	sequence: 'brand'
};
/** The chip word for a stage type ("human_gate" reads "gate"). */
export const STAGE_WORD: Record<string, string> = { human_gate: 'gate', manual: 'by hand' };

/** Flow edge kinds (DESIGN 3.6). */
export const EDGE_HUE: Record<string, Hue> = {
	success: 'success',
	on_fail: 'danger',
	on_timeout: 'accent-2',
	on_reject: 'warning'
};

/** Agent tool-call kinds (DESIGN 3.7). */
export const TOOL_HUE: Record<string, Hue> = {
	read: 'running',
	edit: 'warning',
	search: 'accent',
	execute: 'success',
	exec: 'success',
	delete: 'danger',
	move: 'accent-2',
	fetch: 'running',
	think: 'muted',
	other: 'muted'
};

/** Git change status letters (DESIGN 4.13 file list). */
export const GIT_HUE: Record<string, Hue> = {
	A: 'success',
	M: 'warning',
	D: 'danger',
	R: 'accent',
	C: 'accent-2',
	'?': 'muted',
	U: 'muted'
};

/** Tone of a timeline / status line. */
export type Tone = 'ok' | 'bad' | 'warn' | 'info' | 'muted';
export const TONE_HUE: Record<Tone, Hue> = {
	ok: 'success',
	bad: 'danger',
	warn: 'attention',
	info: 'running',
	muted: 'muted'
};

export const hueOf = (map: Record<string, Hue>, key: string, fallback: Hue = 'muted'): Hue =>
	map[key] ?? fallback;
