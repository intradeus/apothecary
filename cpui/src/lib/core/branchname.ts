// A ticket's base branch, named by its workshop's branch format: free text
// with three placeholders, {ticket_type} (the change type), {ticket_number}
// (the tracker's issue key) and {ticket_title}. The engine names a ticket
// created without a branch the same way (internal/project/branchname); both
// test suites read its testdata/cases.json, so the two cannot drift.

export const BRANCH_PLACEHOLDERS = ['ticket_type', 'ticket_number', 'ticket_title'] as const;
export type BranchPlaceholder = (typeof BRANCH_PLACEHOLDERS)[number];

export const DEFAULT_BRANCH_FORMAT = '{ticket_type}/{ticket_number}-{ticket_title}';

/** A format's length cap, in characters. */
export const MAX_BRANCH_FORMAT = 100;
/** A rendered name's cap, in bytes: a backstop under a ref's 255. */
const MAX_NAME = 200;

/** Why a format is refused; `name` is the unknown placeholder or the
 *  rendered name git would refuse. */
export type BranchFormatError =
	| { code: 'unknown'; name: string }
	| { code: 'unbalanced' }
	| { code: 'no_stem' }
	| { code: 'too_long' }
	| { code: 'ref'; name: string }
	| { code: 'empty' };

type Token = { lit: string; ph?: undefined } | { lit?: undefined; ph: BranchPlaceholder };

const isPlaceholder = (s: string): s is BranchPlaceholder =>
	(BRANCH_PLACEHOLDERS as readonly string[]).includes(s);

// parse splits a format into its "/"-separated segments of tokens. A brace
// that opens no known placeholder is the error; rendering keeps it as text.
function parse(format: string): { segs: Token[][]; error: BranchFormatError | null } {
	const segs: Token[][] = [[]];
	let lit = '';
	let error: BranchFormatError | null = null;
	const flush = () => {
		if (lit) segs[segs.length - 1].push({ lit });
		lit = '';
	};
	for (let i = 0; i < format.length; i++) {
		const c = format[i];
		if (c === '/') {
			flush();
			segs.push([]);
		} else if (c === '{') {
			const rest = format.slice(i + 1);
			const end = rest.search(/[{}]/);
			if (end < 0 || rest[end] === '{') {
				error ??= { code: 'unbalanced' };
				lit += c;
				continue;
			}
			const name = rest.slice(0, end);
			if (isPlaceholder(name)) {
				flush();
				segs[segs.length - 1].push({ ph: name });
			} else {
				error ??= { code: 'unknown', name };
				lit += format.slice(i, i + 2 + end);
			}
			i += 1 + end;
		} else {
			if (c === '}') error ??= { code: 'unbalanced' };
			lit += c;
		}
	}
	flush();
	return { segs, error };
}

const count = (segs: Token[][], ph: BranchPlaceholder) =>
	segs.flat().filter((t) => t.ph === ph).length;

/** Lowercases s to [a-z0-9] runs joined by single dashes. */
export function slug(s: string): string {
	let out = '';
	let dash = false;
	for (const r of s.toLowerCase()) {
		if ((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			if (dash && out) out += '-';
			out += r;
			dash = false;
		} else {
			dash = true;
		}
	}
	return out;
}

/** The literal separators an empty placeholder takes with it. */
const SEPARATORS = '-_.';

function render(segs: Token[][], vals: Record<BranchPlaceholder, string>): string {
	const out: string[] = [];
	for (const seg of segs) {
		const parts = seg.map((t) => (t.ph ? vals[t.ph] : t.lit));
		let emptied = false;
		seg.forEach((t, i) => {
			if (!t.ph || parts[i]) return;
			emptied = true;
			if (parts.slice(0, i).join('') === '') {
				const next = parts[i + 1];
				if (next && !seg[i + 1].ph && SEPARATORS.includes(next[0])) parts[i + 1] = next.slice(1);
			} else {
				const prev = parts[i - 1];
				if (prev && !seg[i - 1].ph && SEPARATORS.includes(prev[prev.length - 1]))
					parts[i - 1] = prev.slice(0, -1);
			}
		});
		const s = parts.join('');
		if (s || !emptied) out.push(s);
	}
	return out.join('/');
}

/** A ticket's base branch by `format` ('' is the default). Each value is
 *  slugged; a key carrying its repository ("lu/greeter#5") keeps only its
 *  number, and a ticket with neither number nor title is titled "ticket". A
 *  placeholder that renders empty takes one adjacent separator (-, _ or .)
 *  with it, the next one when it starts its "/" segment, else the one before,
 *  and a segment it leaves empty is dropped. The title is cut so the name
 *  stays within 200 bytes. */
export function renderBranch(format: string, changeType: string, key: string, title: string) {
	const { segs } = parse(format || DEFAULT_BRANCH_FORMAT);
	const vals: Record<BranchPlaceholder, string> = {
		ticket_type: slug(changeType),
		ticket_number: slug(key.slice(key.lastIndexOf('#') + 1)),
		ticket_title: slug(title)
	};
	if (!vals.ticket_number && !vals.ticket_title) vals.ticket_title = 'ticket';
	let name = render(segs, vals);
	const over = new TextEncoder().encode(name).length - MAX_NAME;
	const n = count(segs, 'ticket_title');
	if (over > 0 && n > 0) {
		const keep = Math.max(vals.ticket_title.length - Math.ceil(over / n), 0);
		vals.ticket_title = vals.ticket_title.slice(0, keep).replace(/-+$/, '');
		name = render(segs, vals);
	}
	return name;
}

// The refusals of git check-ref-format the engine checks a branch against
// (checkout.CheckBranch).
function validRef(s: string): boolean {
	if (s.startsWith('/') || s.endsWith('/') || s.includes('//')) return false;
	if (s.includes('..') || s.includes('@{')) return false;
	for (const seg of s.split('/')) {
		if (seg.startsWith('.') || seg.endsWith('.') || seg.endsWith('.lock')) return false;
	}
	for (const r of s) {
		const c = r.codePointAt(0) ?? 0;
		if (c <= 0x20 || c === 0x7f || '~^:?*[\\'.includes(r)) return false;
	}
	return true;
}

/** Why the engine would refuse `format` as a workshop's branch format, or
 *  null ('' is the default, so fine). */
export function branchFormatError(format: string): BranchFormatError | null {
	const f = format || DEFAULT_BRANCH_FORMAT;
	if ([...f].length > MAX_BRANCH_FORMAT) return { code: 'too_long' };
	const { segs, error } = parse(f);
	if (error) return error;
	if (count(segs, 'ticket_number') + count(segs, 'ticket_title') === 0) return { code: 'no_stem' };
	for (const key of ['PROJ-123', '']) {
		const name = renderBranch(f, 'feat', key, 'Add footnotes');
		if (!name) return { code: 'empty' };
		if (!validRef(name)) return { code: 'ref', name };
	}
	return null;
}
