// Which clipboard keys the run's terminal takes from xterm (the decision only;
// Terminal.svelte acts on it). Off a Mac, Ctrl+C and Ctrl+V belong to the
// shell, so a terminal has to choose:
//
//   - Ctrl+Shift+C and Ctrl+Shift+V always copy and paste;
//   - Ctrl+C copies when text is selected, and is ^C when none is;
//   - Ctrl+V pastes: the shell's quoted-insert (^V) is given up for it.
//
// On a Mac, Cmd+C and Cmd+V already copy and paste through the browser and
// Ctrl+C / Ctrl+V stay the shell's: every key passes to xterm.
// docs/notes/2026-11-14-terminal-and-log-copy-paste-links-and-find.md.

/** What the terminal does with a key: copy, paste, or hand it to xterm. */
export type TerminalKeyAction = 'copy' | 'paste' | 'pass';

export type KeyLike = Pick<
	KeyboardEvent,
	'key' | 'code' | 'ctrlKey' | 'shiftKey' | 'altKey' | 'metaKey'
>;

/**
 * The letter a key stands for: what the layout gives, or, on a layout
 * without Latin letters, the one at its place (Ctrl+С on a Cyrillic layout
 * is Ctrl+C).
 */
function letter(e: KeyLike): string {
	if (/^[a-z]$/i.test(e.key)) return e.key.toLowerCase();
	const at = /^Key([A-Z])$/.exec(e.code);
	return at ? at[1].toLowerCase() : '';
}

/**
 * The same answer for a key's keydown and its keyup, so xterm never sees
 * half of a key it was not given; only a keydown is acted on.
 */
export function terminalKey(e: KeyLike, selected: boolean, mac: boolean): TerminalKeyAction {
	if (mac || !e.ctrlKey || e.altKey || e.metaKey) return 'pass';
	const k = letter(e);
	if (k === 'v') return 'paste';
	if (k === 'c' && (e.shiftKey || selected)) return 'copy';
	return 'pass';
}

/** The copy and paste shortcuts to name in a tooltip or a hint. */
export function terminalShortcuts(mac: boolean): { copy: string; paste: string } {
	return mac ? { copy: '⌘C', paste: '⌘V' } : { copy: 'Ctrl+Shift+C', paste: 'Ctrl+Shift+V' };
}
