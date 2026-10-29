// The icon vocabulary (DESIGN 2.6). No icon font: Unicode glyphs in text
// presentation. Every glyph the UI shows comes from here by meaning, so a
// redesign that swaps the set (or moves to SVG) edits this file only.
//
//   import { I } from '$lib/ui/icons';   <button>{I.retry} Retry</button>

const T = '︎'; // force text presentation (no emoji)

export const I = {
	// navigation
	desk: '◈' + T,
	inbox: '✉' + T,
	runs: '▶' + T,
	tickets: '☰' + T,
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
	copy: '⧉',
	branch: '⎇',
	attach: '⎘',
	drag: '⠿',
	// windows
	splitRight: '◫',
	splitDown: '⊟',
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
	dot: '●',
	caret: '▸',
	// brand and domain
	rx: '℞',
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
	limits: '▤',
	reaper: '⌛' + T,
	storage: '⛁',
	// files
	file: '▤',
	image: '◩',
	binary: '◇',
	folder: '▸'
} as const;

export type IconName = keyof typeof I;
