import { describe, expect, it, vi } from 'vitest';
import { findPaths, parseRef, pathClick, pathParts, type PathRef } from './paths';

/** The refs found, with the text each one spans. */
const found = (text: string) =>
	findPaths(text).map((s) => ({ text: text.slice(s.start, s.end), ...s.ref }));

describe('findPaths', () => {
	it.each<[string, string, PathRef]>([
		[
			'see internal/run/run.go:336 now',
			'internal/run/run.go:336',
			{ path: 'internal/run/run.go', from: 336 }
		],
		['in a/b.ts:4-9,', 'a/b.ts:4-9', { path: 'a/b.ts', from: 4, to: 9 }],
		['run ./x.py', './x.py', { path: './x.py' }],
		['from ../a/b.ts', '../a/b.ts', { path: '../a/b.ts' }],
		['read /etc/hosts', '/etc/hosts', { path: '/etc/hosts' }],
		['edit ~/.bashrc', '~/.bashrc', { path: '~/.bashrc' }],
		['at /src/app.css:12:7: error', '/src/app.css:12:7', { path: '/src/app.css', from: 12 }],
		['run.go:336', 'run.go:336', { path: 'run.go', from: 336 }],
		['build/Makefile', 'build/Makefile', { path: 'build/Makefile' }],
		['Dockerfile:3', 'Dockerfile:3', { path: 'Dockerfile', from: 3 }],
		['--file=cfg/app.yaml', 'cfg/app.yaml', { path: 'cfg/app.yaml' }],
		['--a/b.go', 'a/b.go', { path: 'a/b.go' }],
		['src/lib/x.svelte.ts', 'src/lib/x.svelte.ts', { path: 'src/lib/x.svelte.ts' }]
	])('finds the path in %j', (text, span, ref) => {
		expect(found(text)).toEqual([{ text: span, ...ref }]);
	});

	it.each([
		['trailing punctuation', 'Changed a/b.go.', 'a/b.go'],
		['a closing parenthesis', '(see a/b.go)', 'a/b.go'],
		['a sentence after a line', 'It is a/b.go:12. Then', 'a/b.go:12'],
		['a trailing colon', 'a/b.go: fixed', 'a/b.go'],
		['a trailing slash', 'a/b.go/ x', 'a/b.go']
	])('leaves %s out', (_, text, span) => {
		expect(found(text).map((f) => f.text)).toEqual([span]);
	});

	it('finds several, in order, with their offsets', () => {
		expect(findPaths('a/b.go and c/d.ts:2')).toEqual([
			{ start: 0, end: 6, ref: { path: 'a/b.go' } },
			{ start: 11, end: 19, ref: { path: 'c/d.ts', from: 2 } }
		]);
	});

	it.each([
		['prose', 'and/or either/or w/o TCP/IP n/a'],
		['a URL', 'https://example.test/a/b.go:12'],
		['a file URL', 'file:///etc/hosts'],
		['a URL path after //', '//cdn.test/x.js'],
		['a scheme-relative host', 'host:/srv/x.go'],
		['a version', '1.2.3 v4.0.1 node/v18.2.0'],
		['a numeric line on a version', 'v1.2:3'],
		['a date', '2026/11/18 18.11.2026'],
		['a fraction', '3/4 of 1/2'],
		['a bare file name', 'run.go Makefile'],
		['a folder', 'src/lib/ src/lib'],
		['a sigil', '$HOME/x.sh %APPDATA%/x.ini'],
		['a zero line on a bare name', 'run.go:0'],
		['a mail address', 'me@example.test'],
		['nothing', '']
	])('finds nothing in %s', (_, text) => {
		expect(findPaths(text)).toEqual([]);
	});

	it('drops a backwards range’s end', () => {
		expect(findPaths('a/b.go:9-4')[0].ref).toEqual({ path: 'a/b.go', from: 9 });
	});
});

describe('pathParts', () => {
	it('splits text around paths', () => {
		expect(pathParts('open a/b.go:3 now')).toEqual([
			{ text: 'open ' },
			{ text: 'a/b.go:3', ref: { path: 'a/b.go', from: 3 } },
			{ text: ' now' }
		]);
	});

	it('gives text without paths as one part, and a path alone as one', () => {
		expect(pathParts('and/or')).toEqual([{ text: 'and/or' }]);
		expect(pathParts('a/b.go')).toEqual([{ text: 'a/b.go', ref: { path: 'a/b.go' } }]);
		expect(pathParts('')).toEqual([]);
	});
});

describe('parseRef', () => {
	it.each<[string, PathRef]>([
		['/work/repo/internal/x', { path: '/work/repo/internal/x' }],
		['/work/repo/a.go:12', { path: '/work/repo/a.go', from: 12 }],
		['a.go:4-9', { path: 'a.go', from: 4, to: 9 }],
		['a.go:4:2', { path: 'a.go', from: 4 }],
		[' Makefile ', { path: 'Makefile' }],
		['', { path: '' }]
	])('reads %j', (loc, ref) => {
		expect(parseRef(loc)).toEqual(ref);
	});
});

describe('pathClick', () => {
	const box = () => {
		const root = document.createElement('div');
		root.innerHTML =
			'<p>x <button data-path="a/b.go" data-line="3" data-end="5"><code>a/b.go:3-5</code></button>' +
			' <button data-path="c.go">c.go</button> <span>plain</span></p>';
		document.body.append(root);
		return root;
	};

	it('is undefined without onpath', () => {
		expect(pathClick(undefined)).toBeUndefined();
	});

	it('hands the clicked link’s ref over, and keeps it from navigating', () => {
		const root = box();
		const onpath = vi.fn();
		const click = vi.fn(pathClick(onpath)!);
		root.addEventListener('click', click);

		root.querySelector('code')!.click();
		expect(onpath).toHaveBeenLastCalledWith({ path: 'a/b.go', from: 3, to: 5 });
		expect(click.mock.calls[0][0].defaultPrevented).toBe(true);

		root.querySelector<HTMLElement>('[data-path="c.go"]')!.click();
		expect(onpath).toHaveBeenLastCalledWith({ path: 'c.go' });
		root.remove();
	});

	it('ignores a click elsewhere', () => {
		const root = box();
		const onpath = vi.fn();
		const handler = pathClick(onpath)!;
		const ev = new MouseEvent('click', { bubbles: true, cancelable: true });
		root.addEventListener('click', handler);
		root.querySelector('span')!.dispatchEvent(ev);
		handler(new MouseEvent('click'));
		expect(onpath).not.toHaveBeenCalled();
		expect(ev.defaultPrevented).toBe(false);
		root.remove();
	});
});
