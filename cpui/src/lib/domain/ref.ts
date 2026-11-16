// A reference: the readable name people and agents use for what a run holds
// (FLOWLANG §1, References). `#` is "number n of", `/` is "inside":
//
//	review#5             the fifth attempt of review
//	settle.resolve#2     an attempt of a sequence's stage
//	build#3/decision#1   the first ping raised in or about build#3
//	build#3/note#2       the second note in build#3's notes file
//	run                  the run as a whole
//	run/decision#1       the first ping raised outside any attempt
//
// The engine's grammar is internal/run/ref/ref.go; this mirrors it exactly.
// The UI only formats attempt references: a ping's reference comes from the
// engine (Ping.ref), never built here.

export type RefKind = 'decision' | 'note';

/** A parsed reference. */
export interface RunRef {
	/** The attempt's stage and number; both absent for the run. */
	stage?: string;
	attempt?: number;
	/** An entry inside the attempt (or the run), and its number. */
	kind?: RefKind;
	n?: number;
}

/** A stage id as the flow has it: a slug, or a sequence path of slugs. */
const STAGE = /^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)*$/;
/** A positive decimal with no leading zero. */
const NUMBER = /^[1-9][0-9]*$/;

/** An attempt's reference: review#5. */
export const attemptRef = (stage: string, n: number): string => `${stage}#${n}`;

/** name#n split, or null when it is not one. */
function numbered(s: string): [string, number] | null {
	const at = s.indexOf('#');
	if (at < 0) return null;
	const name = s.slice(0, at);
	const num = s.slice(at + 1);
	if (!name || !NUMBER.test(num)) return null;
	return [name, Number(num)];
}

/** Reads a reference back, or null for anything the grammar does not
 *  produce: no number, a zero or a leading zero, an unknown kind, a note
 *  outside an attempt, a third segment. */
export function parseRef(s: string): RunRef | null {
	const slash = s.indexOf('/');
	const head = slash < 0 ? s : s.slice(0, slash);
	const r: RunRef = {};
	if (head !== 'run') {
		const a = numbered(head);
		if (!a || !STAGE.test(a[0])) return null;
		[r.stage, r.attempt] = a;
	}
	if (slash < 0) return r;
	const [kind, n] = numbered(s.slice(slash + 1)) ?? ['', 0];
	if ((kind !== 'decision' && kind !== 'note') || (kind === 'note' && head === 'run')) return null;
	r.kind = kind;
	r.n = n;
	return r;
}
