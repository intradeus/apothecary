import type { LanguageSupport } from '@codemirror/language';
import { describe, expect, it } from 'vitest';
import { languageFor } from './codeLanguages';

// Every node name in a parse, mounted (nested) trees included.
const nodes = (support: LanguageSupport, text: string) => {
	const names = new Set<string>();
	support.language.parser.parse(text).iterate({ enter: (n) => void names.add(n.name) });
	return names;
};

describe('languageFor', () => {
	it.each([
		['internal/run/run.go', 'Go'],
		['src/app.ts', 'TypeScript'],
		['src/lib/ui/CodeViewer.svelte', 'Svelte'],
		['charts/values.yaml', 'YAML'],
		['README.md', 'Markdown'],
		['tools/gen.py', 'Python'],
		['src/main.rs', 'Rust'],
		['migrations/0001_init.sql', 'SQL'],
		['deploy/images.sh', 'Shell'],
		['/home/dev/.bashrc', 'Shell'],
		['.zshrc', 'Shell'],
		['~/.profile', 'Shell'],
		['.bash_profile', 'Shell']
	])('reads %s as %s, and its grammar loads', async (path, name) => {
		const lang = languageFor(path);
		expect(lang?.name).toBe(name);
		const support = await lang!.load();
		expect(support.language.parser).toBeTruthy();
	});

	it.each([
		['#!/bin/sh', 'Shell'],
		['#!/usr/bin/env bash', 'Shell'],
		['#!/bin/dash -e', 'Shell'],
		['#!/usr/bin/env -S python3 -u', 'Python'],
		['#!/usr/bin/python3.12', 'Python'],
		['#!/usr/bin/env node', 'JavaScript'],
		['#! /usr/bin/env NODE_ENV=production deno run', 'TypeScript'],
		['#!/usr/bin/env ruby', 'Ruby']
	])('reads an extensionless script starting %s as %s', async (line, name) => {
		const lang = languageFor('bin/run', line);
		expect(lang?.name).toBe(name);
		expect((await lang!.load()).language.parser).toBeTruthy();
	});

	it('lets the file name win over the shebang', () => {
		expect(languageFor('tool.py', '#!/bin/sh')?.name).toBe('Python');
	});

	it('reads the rest as plain text', () => {
		expect(languageFor('notes.unknownext')).toBeNull();
		expect(languageFor('bin/run')).toBeNull();
		expect(languageFor('bin/run', 'echo hi')).toBeNull();
		expect(languageFor('bin/run', '#!/usr/bin/env')).toBeNull();
		expect(languageFor('bin/run', '#!/opt/bin/nosuchlang')).toBeNull();
	});

	it('reads Svelte markup, a TypeScript script and a CSS style', async () => {
		const support = await languageFor('A.svelte')!.load();
		const names = nodes(
			support,
			'<script>let n: number = 1;</script>\n<Foo bar={n} />\n<style>p { color: red; }</style>'
		);
		expect(names).toContain('Element');
		expect(names).toContain('TypeAnnotation');
		expect(names).toContain('RuleSet');
	});

	it('loads a grammar once', async () => {
		const lang = languageFor('a.go')!;
		expect(await lang.load()).toBe(await languageFor('b.go')!.load());
	});
});
