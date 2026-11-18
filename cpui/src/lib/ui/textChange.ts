// The one change that turns a text into another: what lies between their
// common prefix and common suffix. Applied to an editor, everything outside
// it (scroll anchor, selection, folds) maps through untouched.

export interface TextChange {
	from: number;
	to: number;
	insert: string;
}

/** The change from `prev` to `next`, or null when they are the same. */
export function minimalChange(prev: string, next: string): TextChange | null {
	if (prev === next) return null;
	const max = Math.min(prev.length, next.length);
	let start = 0;
	while (start < max && prev.charCodeAt(start) === next.charCodeAt(start)) start++;
	// The suffix may not reach back into the prefix of the shorter text.
	let end = 0;
	while (
		end < max - start &&
		prev.charCodeAt(prev.length - 1 - end) === next.charCodeAt(next.length - 1 - end)
	)
		end++;
	return { from: start, to: prev.length - end, insert: next.slice(start, next.length - end) };
}
