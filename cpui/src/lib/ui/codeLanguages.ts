// Which grammar a file is read in: CodeMirror's language-data catalogue by
// file name and extension, plus what it lacks (Svelte, shell dotfiles,
// extensionless scripts named by their shebang). A description loads its
// grammar through its own `load()`, which caches, so each grammar is a lazy
// chunk fetched the first time a file needs it.
import { LanguageDescription } from '@codemirror/language';
import { languages } from '@codemirror/language-data';

// No Svelte grammar ships with CodeMirror: HTML, with every `<script>` read
// as TypeScript and `<style>` as CSS (lang-html's own nesting), colours the
// markup, script and style; `{#if}` blocks stay plain.
const svelte = LanguageDescription.of({
	name: 'Svelte',
	extensions: ['svelte'],
	load: () =>
		Promise.all([import('@codemirror/lang-html'), import('@codemirror/lang-javascript')]).then(
			([{ html }, { typescriptLanguage }]) =>
				html({
					autoCloseTags: false,
					selfClosingTags: true,
					nestedLanguages: [{ tag: 'script', parser: typescriptLanguage.parser }]
				})
		)
});

const SHELL_DOTFILE =
	/^\.(bashrc|bash_profile|bash_login|bash_logout|bash_aliases|profile|zshrc|zshenv|zprofile|zlogin|zlogout|kshrc)$/;

// Interpreters whose name is not a language-data name or alias.
const INTERPRETER: Record<string, string> = {
	ash: 'sh',
	dash: 'sh',
	ksh: 'sh',
	mksh: 'sh',
	nodejs: 'node',
	deno: 'ts',
	bun: 'ts',
	tsx: 'ts',
	'ts-node': 'ts'
};

const basename = (path: string) => path.slice(path.lastIndexOf('/') + 1);

/** The language a `#!` line names: `#!/bin/sh`, `#!/usr/bin/env -S python3 -u`. */
function shebang(firstLine: string): LanguageDescription | null {
	const m = /^#!\s*(\S+)([^\n]*)/.exec(firstLine);
	if (!m) return null;
	let prog = basename(m[1]);
	// env's own flags and VAR=value assignments come before the program.
	if (prog === 'env')
		prog = basename(
			m[2].split(/\s+/).find((w) => w && !w.startsWith('-') && !w.includes('=')) ?? ''
		);
	// python3, python3.12, perl5: the version is not part of the name.
	const name = prog.replace(/[\d.]+$/, '');
	return name
		? LanguageDescription.matchLanguageName(languages, INTERPRETER[name] ?? name, false)
		: null;
}

/**
 * The language for a file (its name or extension; failing that, a shebang
 * on its first line), or null to read it as plain text.
 */
export function languageFor(path: string, firstLine = ''): LanguageDescription | null {
	const name = basename(path);
	if (SHELL_DOTFILE.test(name))
		return LanguageDescription.matchLanguageName(languages, 'sh', false);
	return (
		LanguageDescription.matchFilename([svelte], name) ??
		LanguageDescription.matchFilename(languages, name) ??
		shebang(firstLine)
	);
}
