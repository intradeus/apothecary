// File references in plain text — an agent's "see internal/run/run.go:336"
// — as spans, so a view can make them open the file. `path`, `path:42` and
// `path:42-50`; a column after the line (`path:42:7`) is taken along but
// dropped.
//
// Prose is full of slashes and dots that are no file, so the rule is
// conservative: a match has a slash or a line suffix, and its last segment
// has an extension with a letter in it (`.go`, `.bashrc`) or is a known
// extensionless name (`Makefile`). That keeps `and/or`, `3/4`, dates and
// versions (`v4.0.1`) plain. A match never starts inside a word, after a
// `/` or after a `:`, so nothing inside a URL links. A false match costs
// little: the click is resolved against the workspace, and a miss says so.

export interface PathRef {
	path: string;
	/** 1-based line. */
	from?: number;
	/** Last line of a range (`path:4-9`); absent for a single line. */
	to?: number;
}

export interface PathSpan {
	start: number;
	end: number;
	ref: PathRef;
}

/** Plain text split into text and path parts, in order. */
export type PathPart = { text: string; ref?: PathRef };

/** Files commonly named without an extension. */
const KNOWN = new Set([
	'Makefile',
	'makefile',
	'GNUmakefile',
	'Dockerfile',
	'Containerfile',
	'Jenkinsfile',
	'Justfile',
	'justfile',
	'Procfile',
	'Gemfile',
	'Rakefile',
	'Vagrantfile',
	'Brewfile',
	'LICENSE',
	'README',
	'CHANGELOG',
	'CODEOWNERS',
	'AUTHORS',
	'NOTICE',
	'hosts',
	'passwd'
]);

const LINE = String.raw`([1-9]\d{0,6})`;
// Not after a path character, `/` (a URL's `//` or path), `:` (a scheme or
// `host:`) or a sigil (`$HOME/x`), and not starting on a dash (`--a/b.go`
// is `a/b.go`); `~/`, `/`, `./`, `../` may lead; the last segment ends on a
// word character, so a sentence's full stop stays out.
const PATH_RE = new RegExp(
	String.raw`(?<![\w.~/@:$%])(?=[\w.~/@])((?:~(?=/))?/?(?:[\w.@+-]+/)*[\w.@+-]*\w)(?::${LINE}(?:-${LINE})?(?::\d+)?)?`,
	'g'
);
const EXT_RE = /\.[\w-]*[A-Za-z][\w-]*$/;

function accepted(path: string, line: boolean): boolean {
	if (!line && !path.includes('/')) return false;
	const last = path.slice(path.lastIndexOf('/') + 1);
	return EXT_RE.test(last) || KNOWN.has(last);
}

function lines(path: string, from?: string, to?: string): PathRef {
	const ref: PathRef = { path };
	if (from) ref.from = Number(from);
	if (to && Number(to) >= Number(from)) ref.to = Number(to);
	return ref;
}

/** Every path reference in text, in order. */
export function findPaths(text: string): PathSpan[] {
	const out: PathSpan[] = [];
	for (const m of text.matchAll(PATH_RE)) {
		if (!accepted(m[1], !!m[2])) continue;
		const start = m.index ?? 0;
		out.push({ start, end: start + m[0].length, ref: lines(m[1], m[2], m[3]) });
	}
	return out;
}

/** text as text and path parts; no path gives one text part. */
export function pathParts(text: string): PathPart[] {
	const parts: PathPart[] = [];
	let last = 0;
	for (const s of findPaths(text)) {
		if (s.start > last) parts.push({ text: text.slice(last, s.start) });
		parts.push({ text: text.slice(s.start, s.end), ref: s.ref });
		last = s.end;
	}
	if (last < text.length) parts.push({ text: text.slice(last) });
	return parts;
}

/** A string known to be a location (a tool call's `path` or `path:42`), with
 *  none of findPaths' guessing: any path, an optional line suffix. */
export function parseRef(loc: string): PathRef {
	const m = /^(.+?)(?::([1-9]\d*)(?:-([1-9]\d*))?(?::\d+)?)?$/.exec(loc.trim());
	return m ? lines(m[1], m[2], m[3]) : { path: loc };
}

/** A click handler for a container of path links (the `[data-path]`
 *  elements markdown's path mode renders): hands the clicked one to onpath.
 *  Undefined without onpath, so the container stays inert. */
export function pathClick(onpath?: (ref: PathRef) => void): ((e: MouseEvent) => void) | undefined {
	if (!onpath) return undefined;
	return (e) => {
		const el = e.target instanceof Element ? e.target.closest<HTMLElement>('[data-path]') : null;
		if (!el) return;
		e.preventDefault();
		const { path = '', line, end } = el.dataset;
		onpath(lines(path, line, end));
	};
}
