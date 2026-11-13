import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { branchFormatError, renderBranch } from './branchname';

// The engine's own cases (internal/project/branchname): the form must name a
// branch exactly as the engine would, and refuse what the engine refuses.
interface Cases {
	render: {
		format: string;
		type: string;
		key: string;
		title: string;
		repeat?: number;
		want?: string;
		want_prefix?: string;
		want_suffix?: string;
		want_len?: number;
	}[];
	valid: string[];
	invalid: { format: string; error: string }[];
}
const cases: Cases = JSON.parse(
	readFileSync(
		resolve(import.meta.dirname, '../../../..', 'internal/project/branchname/testdata/cases.json'),
		'utf8'
	)
);

describe('renderBranch, the engine’s cases', () => {
	it.each(cases.render)('$format with $type, $key, $title', (c) => {
		const got = renderBranch(c.format, c.type, c.key, c.title.repeat(c.repeat ?? 1));
		if (c.want_len === undefined) {
			expect(got).toBe(c.want);
			return;
		}
		expect(got).toHaveLength(c.want_len);
		expect(got.startsWith(c.want_prefix ?? '')).toBe(true);
		expect(got.endsWith(c.want_suffix ?? '')).toBe(true);
	});
});

describe('branchFormatError, the engine’s cases', () => {
	it.each(cases.valid)('accepts %j', (f) => {
		expect(branchFormatError(f)).toBeNull();
	});

	it.each(cases.invalid)('refuses $format: $error', (c) => {
		expect(branchFormatError(c.format)?.code).toBe(c.error);
	});
});

describe('branchFormatError, what it names', () => {
	it('names the unknown placeholder', () => {
		expect(branchFormatError('{type}/{ticket_title}')).toEqual({ code: 'unknown', name: 'type' });
	});

	it('names the rendered branch git would refuse', () => {
		expect(branchFormatError('{ticket_type} {ticket_title}')).toEqual({
			code: 'ref',
			name: 'feat add-footnotes'
		});
	});
});
