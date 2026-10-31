import { describe, expect, it } from 'vitest';
import { durWords, fromDur, parseGoDuration, toDur } from './duration';

describe('parseGoDuration', () => {
	it.each([
		['48h', 172_800],
		['90m', 5_400],
		['45s', 45],
		['1h30m', 5_400],
		['1h0m0s', 3_600],
		['1.5h', 5_400],
		['0s', 0],
		['500ms', 0.5],
		['  2h  ', 7_200],
		['1m30s', 90]
	])('reads %s as %d seconds', (raw, secs) => {
		expect(parseGoDuration(raw)).toBe(secs);
	});

	it.each([
		['1us', 1e-6],
		['1µs', 1e-6],
		['1ns', 1e-9]
	])('reads the small unit in %s', (raw, secs) => {
		expect(parseGoDuration(raw)).toBeCloseTo(secs, 12);
	});

	it.each(['', '   ', 'soon', 'h', '5', '1d', '1h30', '1h 30m', 'x1h', '1hx', '1h,30m', 'never'])(
		'refuses %j',
		(raw) => {
			expect(parseGoDuration(raw)).toBeNull();
		}
	);
});

describe('toDur', () => {
	it.each([
		['24h', { n: 1, unit: 'd' }],
		['48h', { n: 2, unit: 'd' }],
		['1440m', { n: 1, unit: 'd' }],
		['60m', { n: 1, unit: 'h' }],
		['36h', { n: 36, unit: 'h' }],
		['90m', { n: 90, unit: 'm' }],
		['1h30m', { n: 90, unit: 'm' }],
		['90s', { n: 1.5, unit: 'm' }],
		['1s', { n: 0.02, unit: 'm' }],
		['0s', { n: 0, unit: 'm' }]
	])('shows %s in the largest unit that divides it', (raw, dur) => {
		expect(toDur(raw)).toEqual(dur);
	});

	it.each(['never', '  never  '])('reads %j as never', (raw) => {
		expect(toDur(raw)).toEqual({ n: 0, unit: 'never' });
	});

	it.each(['', 'soon', '1d', 'Never'])('hands %j back as unreadable', (raw) => {
		expect(toDur(raw)).toBeNull();
	});
});

describe('fromDur', () => {
	it('sends days as hours: Go has no day unit', () => {
		expect(fromDur({ n: 2, unit: 'd' })).toBe('48h');
		expect(fromDur({ n: 1.5, unit: 'd' })).toBe('36h');
	});

	it('keeps minutes and hours as they are', () => {
		expect(fromDur({ n: 90, unit: 'm' })).toBe('90m');
		expect(fromDur({ n: 5, unit: 'h' })).toBe('5h');
	});

	it('says never whatever the number', () => {
		expect(fromDur({ n: 7, unit: 'never' })).toBe('never');
	});

	it.each(['48h', '36h', '90m', '1h30m', '90s', '72h', '15m'])(
		'round-trips %s to the same length of time',
		(raw) => {
			expect(parseGoDuration(fromDur(toDur(raw)!))).toBe(parseGoDuration(raw));
		}
	);

	it('round-trips never', () => {
		expect(fromDur(toDur('never')!)).toBe('never');
	});
});

describe('durWords', () => {
	it.each([
		['1m', '1 minute'],
		['90m', '90 minutes'],
		['1h', '1 hour'],
		['36h', '36 hours'],
		['24h', '1 day'],
		['48h', '2 days'],
		['never', 'never']
	])('says %s as "%s"', (raw, words) => {
		expect(durWords(raw)).toBe(words);
	});

	it('hands back what it cannot read rather than hiding it', () => {
		expect(durWords('soon')).toBe('soon');
	});
});
