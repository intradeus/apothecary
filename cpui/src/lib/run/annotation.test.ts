import { describe, expect, it } from 'vitest';
import type { RunCheckout } from '$lib/domain';
import {
	annotationRef,
	annotationText,
	fenceLang,
	makeAnnotation,
	type Annotation
} from './annotation';

const checkout = (name: string, primary = false): RunCheckout => ({
	name,
	dir: primary ? 'repo' : `repos/${name}`,
	url: `https://git.test/dev/${name}`,
	branch: 'apothecary/run',
	source: 'main',
	primary
});
const ONE = [checkout('api', true)];
const TWO = [checkout('api', true), checkout('web')];

const anAnnotation = (over: Partial<Annotation> = {}): Annotation => ({
	id: 'a1',
	ref: 'internal/run/run.go:12-13',
	lang: 'go',
	quote: 'func run() {\n\treturn\n',
	comment: 'Why return here?',
	...over
});

describe('annotationRef', () => {
	it('leaves the checkout out on a single-checkout run', () => {
		expect(annotationRef({ repo: '', path: 'internal/run/run.go' }, 12, 18, ONE)).toBe(
			'internal/run/run.go:12-18'
		);
		expect(annotationRef({ repo: '', path: 'a.go' }, 3, 3, [])).toBe('a.go:3');
	});

	it('names the checkout on a multi-checkout run, the primary by its name', () => {
		expect(annotationRef({ repo: 'web', path: 'src/app.ts' }, 4, 9, TWO)).toBe(
			'web:src/app.ts:4-9'
		);
		expect(annotationRef({ repo: '', path: 'main.go' }, 7, 7, TWO)).toBe('api:main.go:7');
	});

	it('names no primary when none is marked', () => {
		expect(annotationRef({ repo: '', path: 'x.go' }, 1, 2, [checkout('a'), checkout('b')])).toBe(
			':x.go:1-2'
		);
	});

	it('gives an absolute file its full path', () => {
		expect(annotationRef({ abs: '/etc/hosts' }, 1, 3, TWO)).toBe('/etc/hosts:1-3');
		expect(annotationRef({ abs: '/home/dev/.bashrc' }, 5, 5, ONE)).toBe('/home/dev/.bashrc:5');
	});
});

describe('fenceLang', () => {
	it.each([
		['internal/run/run.go', 'go'],
		['src/App.svelte', 'svelte'],
		['config.yml', 'yaml'],
		['README.MD', 'md'],
		['x.mjs', 'js'],
		['/home/dev/.bashrc', 'sh'],
		['Dockerfile', 'dockerfile'],
		['build/Makefile', 'make'],
		['notes.c++', 'c++']
	])('%s fences as %s', (path, lang) => {
		expect(fenceLang(path)).toBe(lang);
	});

	it.each(['LICENSE', '.gitignore', 'weird.(x)', 'dir/', 'src/constructor'])(
		'%s fences without a language',
		(path) => {
			expect(fenceLang(path)).toBe('');
		}
	);
});

describe('makeAnnotation', () => {
	it('carries the reference, the language, the lines and the comment, each its own', () => {
		const a = makeAnnotation({ repo: 'web', path: 'src/a.ts' }, 2, 4, 'x\ny\nz', 'tidy', TWO);
		const b = makeAnnotation({ abs: '/etc/hosts' }, 1, 1, '127.0.0.1', '', TWO);

		expect(a).toMatchObject({
			ref: 'web:src/a.ts:2-4',
			lang: 'ts',
			quote: 'x\ny\nz',
			comment: 'tidy'
		});
		expect(b).toMatchObject({ ref: '/etc/hosts:1', lang: '', quote: '127.0.0.1', comment: '' });
		expect(a.id).not.toBe(b.id);
	});
});

describe('annotationText', () => {
	it('gives the reference, the lines fenced in their language, then the comment', () => {
		expect(annotationText([anAnnotation()])).toBe(
			'`internal/run/run.go:12-13`\n\n```go\nfunc run() {\n\treturn\n```\n\nWhy return here?'
		);
	});

	it('leaves a blank comment out', () => {
		expect(annotationText([anAnnotation({ comment: '  ', lang: '', quote: 'x' })])).toBe(
			'`internal/run/run.go:12-13`\n\n```\nx\n```'
		);
	});

	it('fences lines holding backticks with a longer fence', () => {
		const text = annotationText([
			anAnnotation({ lang: 'md', quote: '```sh\nls\n````', comment: '' })
		]);
		expect(text).toBe('`internal/run/run.go:12-13`\n\n`````md\n```sh\nls\n````\n`````');
	});

	it('quotes a reference holding backticks', () => {
		expect(annotationText([anAnnotation({ ref: 'a`b.go:1', comment: '', quote: 'x' })])).toMatch(
			/^``a`b\.go:1``\n/
		);
		expect(annotationText([anAnnotation({ ref: '`x.go:1', comment: '', quote: 'x' })])).toMatch(
			/^`` `x\.go:1 ``\n/
		);
	});

	it('joins several with blank lines, in order', () => {
		const text = annotationText([
			anAnnotation({ ref: 'a.go:1', quote: 'a', comment: 'first' }),
			anAnnotation({ ref: 'b.go:2', quote: 'b', comment: 'second' })
		]);
		expect(text).toBe('`a.go:1`\n\n```go\na\n```\n\nfirst\n\n`b.go:2`\n\n```go\nb\n```\n\nsecond');
	});

	it('is empty for none', () => {
		expect(annotationText([])).toBe('');
	});
});
