// The find bar of a log (LogOutput): where a query matches across its lines,
// and one line cut into what each piece of it renders as — plain text, part
// of a link, a match, the current match. Pure, so the component only renders.
import type { Part } from '$lib/core/links';

/** One match: line `line`, characters [start, end). */
export interface Match {
	line: number;
	start: number;
	end: number;
}

/** A run of a part's text: inside a match or not. */
export interface Piece {
	text: string;
	mark?: boolean;
	current?: boolean;
}

/**
 * A part of a line (plain text or one link), with the pieces the matches cut
 * it into when the line has any: a link stays one link around its marks.
 */
export interface Segment extends Part {
	pieces?: Piece[];
}

const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/** Every case-insensitive match of `query`, in reading order; none for a blank query. */
export function findMatches(lines: readonly string[], query: string): Match[] {
	if (!query.trim()) return [];
	const re = new RegExp(escape(query), 'gi');
	const out: Match[] = [];
	lines.forEach((l, line) => {
		for (const m of l.matchAll(re)) out.push({ line, start: m.index, end: m.index + m[0].length });
	});
	return out;
}

/**
 * The index of the first match at or after (line, start), so a current match
 * outlives a change of the text: the same one if it is still there, else
 * the next. Past the last it wraps to the first; -1 when there are none.
 */
export function matchFrom(matches: readonly Match[], line: number, start: number): number {
	let lo = 0;
	let hi = matches.length;
	while (lo < hi) {
		const mid = (lo + hi) >> 1;
		const m = matches[mid];
		if ((m.line - line || m.start - start) < 0) lo = mid + 1;
		else hi = mid;
	}
	return matches.length ? lo % matches.length : -1;
}

/**
 * One line, given as its link parts, cut at every match edge. `marks` are
 * the line's matches in order; `current` indexes the current one among them
 * (-1: none). A match crossing a link edge is split there.
 */
export function segmentLine(
	parts: readonly Part[],
	marks: readonly { start: number; end: number }[],
	current = -1
): Segment[] {
	let pos = 0;
	let mi = 0;
	return parts.map((p) => {
		const end = pos + p.text.length;
		const pieces: Piece[] = [];
		const piece = (from: number, to: number, mark: boolean) => {
			const s: Piece = { text: p.text.slice(from - pos, to - pos) };
			if (mark) s.mark = true;
			if (mark && mi === current) s.current = true;
			pieces.push(s);
		};
		let at = pos;
		while (at < end) {
			while (mi < marks.length && marks[mi].end <= at) mi++;
			const m = marks[mi];
			if (!m || m.start >= end) {
				piece(at, end, false);
				break;
			}
			if (m.start > at) piece(at, m.start, false);
			const to = Math.min(m.end, end);
			piece(Math.max(at, m.start), to, true);
			at = to;
		}
		pos = end;
		return { ...p, pieces };
	});
}
