// The icon vocabulary (DESIGN 2.6). No icon font: Unicode glyphs in text
// presentation. Every glyph the UI shows comes from here by meaning, so a
// redesign that swaps the set (or moves to SVG) edits this file only. Buttons
// draw play, pause and stop instead (TransportIcon): as text they never match.
//
//   import { I } from '$lib/design/icons';   <button>{I.retry} Retry</button>

const T = '︎'; // force text presentation (no emoji)

export const I = {
	// navigation
	desk: '◈' + T,
	inbox: '✉' + T,
	notices: '⚑' + T,
	runs: '▶' + T,
	tickets: '☰' + T,
	analytics: '▥',
	warRoom: '⌖',
	system: '⚙' + T,
	search: '⌕',
	more: '≡',
	// controls
	pause: '❚❚',
	play: '▶' + T,
	stop: '■',
	retry: '↻',
	restarted: '⟳',
	enter: '↵',
	external: '↗',
	menu: '▾',
	back: '←',
	forward: '→',
	loop: '↩',
	close: '×',
	collapse: '«',
	expand: '»',
	switcher: '⇅',
	follow: '↓',
	add: '+',
	remove: '−',
	up: '↑',
	down: '↓',
	top: '⤒',
	bottom: '⤓',
	copy: '⧉',
	paste: '⎘',
	branch: '⎇',
	attach: '⎘',
	drag: '⠿',
	// windows
	splitRight: '◫',
	splitDown: '⊟',
	layout: '⊞',
	maximise: '⤢',
	restore: '⤡',
	// a take-over: the agent's session in your hands
	hold: '◉',
	// status
	ok: '✓',
	bad: '✕',
	warn: '!',
	info: 'ⓘ',
	none: '–',
	star: '★',
	themeDark: '☾',
	themeLight: '☀',
	dot: '●',
	caret: '▸',
	// brand and domain
	workshop: '⚗' + T,
	matrix: '[m]',
	slack: '#',
	// theme
	dark: '☾' + T,
	light: '☀' + T,
	// system sections
	integrations: '⌁',
	providers: '⚿',
	skills: '✧',
	channels: '✉' + T,
	members: '☺' + T,
	provisioner: '⛁',
	// the assistant: everywhere, and about the page on screen
	sparkle: '✦',
	thisPage: '◧',
	// files
	file: '▤',
	image: '◩',
	binary: '◇',
	folder: '▸'
} as const;

export type IconName = keyof typeof I;

/** The assistant's mark: a four-point sparkle and a small one beside it,
 *  the glyph AI wears, as SVG path data on a 24×24 box. Drawn with
 *  `fill="currentColor"`, so it takes its colour from the role tokens of
 *  whatever holds it. */
export const SPARKLE_PATH =
	'M10 5C10 9.4 13.6 13 18 13C13.6 13 10 16.6 10 21C10 16.6 6.4 13 2 13C6.4 13 10 9.4 10 5Z' +
	'M19 2C19 3.7 20.3 5 22 5C20.3 5 19 6.3 19 8C19 6.3 17.7 5 16 5C17.7 5 19 3.7 19 2Z';
