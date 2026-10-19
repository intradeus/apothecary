// Engine durations ↔ the number + unit picker (DESIGN 7.10).
//
// The engine stores Go duration strings ("48h", "1h30m", "90m") or "never".
// Go's parser has no day unit, so days travel as hours: 2 d → "48h".

export type DurUnit = 'm' | 'h' | 'd' | 'never';

export interface Dur {
	n: number;
	unit: DurUnit;
}

const SECONDS: Record<string, number> = {
	ns: 1e-9,
	us: 1e-6,
	µs: 1e-6,
	ms: 1e-3,
	s: 1,
	m: 60,
	h: 3600
};

/** Seconds in a Go duration string; null when it does not parse. */
export function parseGoDuration(raw: string): number | null {
	const s = raw.trim();
	if (!s) return null;
	const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/gy;
	let total = 0;
	let consumed = 0;
	for (const m of s.matchAll(re)) {
		total += Number(m[1]) * SECONDS[m[2]];
		consumed += m[0].length;
	}
	return consumed === s.length && consumed > 0 ? total : null;
}

/** An engine value as the picker shows it: the largest unit that divides it
 *  evenly. Unparseable values come back as null so the caller can say so. */
export function toDur(raw: string): Dur | null {
	if (raw.trim() === 'never') return { n: 0, unit: 'never' };
	const secs = parseGoDuration(raw);
	if (secs === null) return null;
	const mins = secs / 60;
	if (mins >= 1440 && mins % 1440 === 0) return { n: mins / 1440, unit: 'd' };
	if (mins >= 60 && mins % 60 === 0) return { n: mins / 60, unit: 'h' };
	return { n: Math.round(mins * 100) / 100, unit: 'm' };
}

/** The picker's value as the engine stores it. */
export function fromDur(d: Dur): string {
	if (d.unit === 'never') return 'never';
	if (d.unit === 'd') return `${d.n * 24}h`;
	return `${d.n}${d.unit}`;
}

/** "48 hours", "2 days", "never" — for sentences. */
export function durWords(raw: string): string {
	const d = toDur(raw);
	if (!d) return raw;
	if (d.unit === 'never') return 'never';
	const word = { m: 'minute', h: 'hour', d: 'day' }[d.unit];
	return `${d.n} ${word}${d.n === 1 ? '' : 's'}`;
}
