// What a run started on nothing is called when no one named it: "manual
// run - 2026-11-17 18:40", in the reader's own time — and what its fresh
// branch is called until someone writes another name. The engine names an
// unnamed start the same way, in UTC, and a branch it is not given one for;
// the dialog sends these so the person reads both before starting.
import { slug } from '$lib/core/branchname';

const two = (n: number) => String(n).padStart(2, '0');

export function manualRunName(now: Date = new Date()): string {
	const day = `${now.getFullYear()}-${two(now.getMonth() + 1)}-${two(now.getDate())}`;
	return `manual run - ${day} ${two(now.getHours())}:${two(now.getMinutes())}`;
}

// The letters a run id is written in (Crockford base32), lowercased: a tail
// drawn from them reads like the one the engine cuts from a run's id.
const TAIL_LETTERS = '0123456789abcdefghjkmnpqrstvwxyz';

/** A short random tail for a fresh branch's name, so two runs of one name
 *  do not share a branch. The engine ends a branch it names on the run id's
 *  tail; no one knows that id before the run exists, so this one is drawn. */
export function branchTail(): string {
	return Array.from(
		crypto.getRandomValues(new Uint8Array(4)),
		(b) => TAIL_LETTERS[b % TAIL_LETTERS.length]
	).join('');
}

/** The fresh branch of a run of that name, in the engine's own shape for a
 *  run with no ticket: apothecary/<name>-<tail>. */
export function manualRunBranch(name: string, tail: string): string {
	return `apothecary/${slug(name) || 'run'}-${tail}`;
}
