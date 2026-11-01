// Repo-relative paths as a folder tree, for the file lists that can show
// either way (a run's Changes, a review's files). Folders come before files,
// each in code-point order like git's own; a folder holding one folder and
// nothing else merges with it into one row (`src/lib`), so a sparse list
// doesn't stair-step through folders with nothing else in them.

export interface PathDir {
	/** As shown: one folder, or a merged chain of them (`src/lib`). */
	name: string;
	/** Repo-relative; '' for the root. */
	path: string;
	dirs: PathDir[];
	/** The files directly inside: shown name and full path. */
	files: { name: string; path: string }[];
}

/** One line of the tree as shown. A folder and a file may share a path
 *  (a file replaced by a folder), so key rows by `dir` too. */
export type TreeRow =
	| { dir: true; path: string; name: string; depth: number; open: boolean }
	| { dir: false; path: string; name: string; depth: number };

interface Node {
	dirs: Map<string, Node>;
	/** Full path by file name. */
	files: Map<string, string>;
}

export function pathTree(paths: readonly string[]): PathDir {
	const root: Node = { dirs: new Map(), files: new Map() };
	for (const p of paths) {
		const parts = p.split('/');
		let at = root;
		for (const part of parts.slice(0, -1)) {
			let next = at.dirs.get(part);
			if (!next) at.dirs.set(part, (next = { dirs: new Map(), files: new Map() }));
			at = next;
		}
		at.files.set(parts[parts.length - 1], p);
	}
	return shape('', '', root);
}

function shape(name: string, path: string, node: Node): PathDir {
	// Never the root: its single folder is still a row of its own.
	while (path && !node.files.size && node.dirs.size === 1) {
		const [[sub, child]] = node.dirs;
		name += `/${sub}`;
		path += `/${sub}`;
		node = child;
	}
	// Code-unit order, git's own rather than the locale's; names in one
	// folder are unique, so no two compare equal.
	return {
		name,
		path,
		dirs: [...node.dirs]
			.sort(([a], [b]) => (a < b ? -1 : 1))
			.map(([sub, child]) => shape(sub, path ? `${path}/${sub}` : sub, child)),
		files: [...node.files]
			.sort(([a], [b]) => (a < b ? -1 : 1))
			.map(([file, full]) => ({ name: file, path: full }))
	};
}

/** The tree's rows in display order; a folder in `closed` shows, its
 *  contents don't. */
export function treeRows(root: PathDir, closed: Readonly<Record<string, boolean>> = {}): TreeRow[] {
	const out: TreeRow[] = [];
	const walk = (d: PathDir, depth: number) => {
		for (const sub of d.dirs) {
			const open = !closed[sub.path];
			out.push({ dir: true, path: sub.path, name: sub.name, depth, open });
			if (open) walk(sub, depth + 1);
		}
		for (const f of d.files) out.push({ dir: false, ...f, depth });
	};
	walk(root, 0);
	return out;
}

/** Every file in the order the tree shows them, folders open or not. */
export const treeOrder = (paths: readonly string[]): string[] =>
	treeRows(pathTree(paths)).flatMap((r) => (r.dir ? [] : [r.path]));
