import { describe, expect, it } from 'vitest';
import { editLines, fileLines, lineCounts, parseUnified } from './diff';

const HEADER = [
	'diff --git a/x.ts b/x.ts',
	'index 83db48f..bf2a1c3 100644',
	'--- a/x.ts',
	'+++ b/x.ts'
];
const body = (...lines: string[]) => [...HEADER, ...lines].join('\n');

describe('parseUnified', () => {
	it('numbers each side from the hunk header', () => {
		expect(parseUnified(body('@@ -10,3 +20,3 @@ fn', ' same', '-old', '+new', ' tail'))).toEqual([
			{ kind: 'hunk', text: '@@ -10,3 +20,3 @@ fn' },
			{ kind: 'ctx', old: 10, n: 20, text: ' same' },
			{ kind: 'del', old: 11, text: '−old' },
			{ kind: 'add', n: 21, text: '+new' },
			{ kind: 'ctx', old: 12, n: 22, text: ' tail' }
		]);
	});

	it('reads a hunk header without counts', () => {
		expect(parseUnified(body('@@ -5 +7 @@', '-a', '+b'))).toEqual([
			{ kind: 'hunk', text: '@@ -5 +7 @@' },
			{ kind: 'del', old: 5, text: '−a' },
			{ kind: 'add', n: 7, text: '+b' }
		]);
	});

	it('restarts the numbers at every hunk', () => {
		const lines = parseUnified(body('@@ -1 +1 @@', ' a', '@@ -40 +50 @@', ' b'));
		expect(lines.filter((l) => l.kind === 'ctx')).toEqual([
			{ kind: 'ctx', old: 1, n: 1, text: ' a' },
			{ kind: 'ctx', old: 40, n: 50, text: ' b' }
		]);
	});

	it.each([
		'diff --git a/x b/x',
		'index 83db48f..bf2a1c3',
		'new file mode 100644',
		'deleted file mode 100644',
		'similarity index 98%',
		'rename from a.ts',
		'rename to b.ts',
		'--- a/x',
		'+++ b/x'
	])('drops the file header line %s', (line) => {
		expect(parseUnified(`${line}\n@@ -1 +1 @@\n a`).map((l) => l.text)).toEqual([
			'@@ -1 +1 @@',
			' a'
		]);
	});

	it('keeps the no-newline marker without using up a line number', () => {
		expect(
			parseUnified(body('@@ -1 +1 @@', '-a', '\\ No newline at end of file', '+b', ' c'))
		).toEqual([
			{ kind: 'hunk', text: '@@ -1 +1 @@' },
			{ kind: 'del', old: 1, text: '−a' },
			{ kind: 'meta', text: '\\ No newline at end of file' },
			{ kind: 'add', n: 1, text: '+b' },
			{ kind: 'ctx', old: 2, n: 2, text: ' c' }
		]);
	});

	it('keeps a blank line inside a hunk and trims the blank tail', () => {
		const lines = parseUnified(`${body('@@ -1,3 +1,3 @@', ' a', '', ' b')}\n\n`);
		expect(lines.map((l) => l.text)).toEqual(['@@ -1,3 +1,3 @@', ' a', ' ', ' b']);
	});

	it('returns nothing for an empty diff', () => {
		expect(parseUnified('')).toEqual([]);
	});

	// `-- note` removed from a SQL file arrives as `--- note`.
	it('keeps a removed line that starts with two dashes', () => {
		expect(parseUnified(body('@@ -1,2 +1 @@', '--- note', ' next'))).toEqual([
			{ kind: 'hunk', text: '@@ -1,2 +1 @@' },
			{ kind: 'del', old: 1, text: '−-- note' },
			{ kind: 'ctx', old: 2, n: 1, text: ' next' }
		]);
	});

	it('keeps an added line that starts with two pluses', () => {
		expect(parseUnified(body('@@ -1 +1,2 @@', '+++ counter', ' next'))).toEqual([
			{ kind: 'hunk', text: '@@ -1 +1,2 @@' },
			{ kind: 'add', n: 1, text: '+++ counter' },
			{ kind: 'ctx', old: 1, n: 2, text: ' next' }
		]);
	});

	it("still drops the next file's header once a hunk is done", () => {
		const second = ['diff --git a/y b/y', '--- a/y', '+++ b/y', '@@ -1 +1 @@', '-c', '+d'];
		expect(parseUnified(body('@@ -1 +1 @@', '-a', '+b', ...second)).map((l) => l.text)).toEqual([
			'@@ -1 +1 @@',
			'−a',
			'+b',
			'@@ -1 +1 @@',
			'−c',
			'+d'
		]);
	});

	// `git diff` writes these before any hunk (the shim hands its output over
	// unchanged); as context, `chmod +x` would show as "ld mode 100644".
	it.each([
		'old mode 100644',
		'new mode 100755',
		'copy from a.ts',
		'copy to b.ts',
		'dissimilarity index 90%'
	])('drops the file header line %s', (line) => {
		expect(parseUnified(`diff --git a/x b/x\n${line}\n`)).toEqual([]);
	});
});

describe('editLines', () => {
	it('folds the unchanged head and tail around the change', () => {
		expect(editLines({ oldText: 'a\nb\nold\nc\nd\ne', newText: 'a\nb\nnew\nc\nd\ne' })).toEqual([
			{ kind: 'meta', text: '… 2 unchanged lines' },
			{ kind: 'del', n: 3, text: '−old' },
			{ kind: 'add', n: 3, text: '+new' },
			{ kind: 'meta', text: '… 3 unchanged lines' }
		]);
	});

	it('says line, singular, for one', () => {
		expect(editLines({ oldText: 'a\nold', newText: 'a\nnew' })[0].text).toBe('… 1 unchanged line');
	});

	it('shows no fold when nothing around the change is the same', () => {
		expect(editLines({ oldText: 'old', newText: 'new' })).toEqual([
			{ kind: 'del', n: 1, text: '−old' },
			{ kind: 'add', n: 1, text: '+new' }
		]);
	});

	it('treats a missing old text as a new file', () => {
		expect(editLines({ newText: 'a\nb' })).toEqual([
			{ kind: 'add', n: 1, text: '+a' },
			{ kind: 'add', n: 2, text: '+b' }
		]);
	});

	// The tail must not eat what the head already matched, or a repeated line
	// would count twice and the insert would disappear.
	it('counts a repeated line once when one more is inserted', () => {
		expect(editLines({ oldText: 'a\na', newText: 'a\na\na' })).toEqual([
			{ kind: 'meta', text: '… 2 unchanged lines' },
			{ kind: 'add', n: 3, text: '+a' }
		]);
	});

	it('counts a repeated line once when one is removed', () => {
		expect(editLines({ oldText: 'a\na\na', newText: 'a\na' })).toEqual([
			{ kind: 'meta', text: '… 2 unchanged lines' },
			{ kind: 'del', n: 3, text: '−a' }
		]);
	});

	it('numbers a multi-line change from where it starts', () => {
		const lines = editLines({ oldText: 'keep\nx\ny', newText: 'keep\np\nq\nr' });
		expect(lines.filter((l) => l.kind === 'del').map((l) => l.n)).toEqual([2, 3]);
		expect(lines.filter((l) => l.kind === 'add').map((l) => l.n)).toEqual([2, 3, 4]);
	});
});

describe('lineCounts', () => {
	it('counts added and removed lines and nothing else', () => {
		const lines = parseUnified(body('@@ -1,3 +1,3 @@', ' a', '-b', '-c', '+d', '\\ meta'));
		expect(lineCounts(lines)).toEqual({ add: 1, del: 2 });
	});

	it('is zero for no lines', () => {
		expect(lineCounts([])).toEqual({ add: 0, del: 0 });
	});
});

describe('fileLines', () => {
	it('numbers a file from one', () => {
		expect(fileLines('a\nb')).toEqual([
			{ n: 1, text: 'a' },
			{ n: 2, text: 'b' }
		]);
	});
});
