import { describe, expect, it } from 'vitest';
import { pathTree, treeOrder, treeRows, type TreeRow } from './pathTree';

/** Rows as `depth name` lines, `/` marking a folder and `+`/`-` open or closed. */
const shown = (rows: TreeRow[]) =>
	rows.map((r) => `${r.depth} ${r.name}${r.dir ? (r.open ? '/+' : '/-') : ''}`);

describe('treeRows', () => {
	it('puts folders before files, each in git’s order', () => {
		const tree = pathTree(['b.ts', 'Z.md', 'a/x.ts', 'B/y.ts', 'a.ts']);
		expect(shown(treeRows(tree))).toEqual([
			'0 B/+',
			'1 y.ts',
			'0 a/+',
			'1 x.ts',
			'0 Z.md',
			'0 a.ts',
			'0 b.ts'
		]);
	});

	it('merges a folder holding only one folder into one row', () => {
		const tree = pathTree(['cpui/src/lib/run/Changes.svelte', 'cpui/src/lib/run/Files.svelte']);
		expect(shown(treeRows(tree))).toEqual([
			'0 cpui/src/lib/run/+',
			'1 Changes.svelte',
			'1 Files.svelte'
		]);
		expect(treeRows(tree)[0].path).toBe('cpui/src/lib/run');
	});

	it('stops merging at a folder that holds a file or a second folder', () => {
		const tree = pathTree(['src/a/x.ts', 'src/b/y.ts', 'src/lib/c/z.ts', 'src/lib/top.ts']);
		expect(shown(treeRows(tree))).toEqual([
			'0 src/+',
			'1 a/+',
			'2 x.ts',
			'1 b/+',
			'2 y.ts',
			'1 lib/+',
			'2 c/+',
			'3 z.ts',
			'2 top.ts'
		]);
	});

	it('keeps a closed folder’s row and hides everything inside it', () => {
		const tree = pathTree(['src/a/x.ts', 'src/b.ts', 'top.ts']);
		expect(shown(treeRows(tree, { 'src/a': true }))).toEqual([
			'0 src/+',
			'1 a/-',
			'1 b.ts',
			'0 top.ts'
		]);
		expect(shown(treeRows(tree, { src: true }))).toEqual(['0 src/-', '0 top.ts']);
	});

	it('gives each file its full path', () => {
		const rows = treeRows(pathTree(['src/a/x.ts']));
		expect(rows.find((r) => !r.dir)).toEqual({
			dir: false,
			path: 'src/a/x.ts',
			name: 'x.ts',
			depth: 1
		});
	});

	it('shows a file and a folder of the same path side by side', () => {
		const rows = treeRows(pathTree(['a', 'a/b.ts', 'a/c.ts']));
		expect(rows.map((r) => [r.dir, r.path])).toEqual([
			[true, 'a'],
			[false, 'a/b.ts'],
			[false, 'a/c.ts'],
			[false, 'a']
		]);
	});

	it('shows nothing for no paths', () => {
		expect(treeRows(pathTree([]))).toEqual([]);
	});
});

describe('treeOrder', () => {
	it('lists the files as the tree shows them, closed folders included', () => {
		expect(treeOrder(['z.ts', 'src/b.ts', 'src/a/x.ts'])).toEqual([
			'src/a/x.ts',
			'src/b.ts',
			'z.ts'
		]);
	});
});
