import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { RunState } from '$lib/domain';
import {
	age,
	ago,
	counterRates,
	fmtAbs,
	fmtBytes,
	fmtClock,
	fmtClockDuration,
	fmtCost,
	fmtDuration,
	fmtHoursMinutes,
	fmtPct,
	fmtRate,
	fmtTime,
	isTerminal,
	plural,
	portLabel,
	shortId,
	stateColors,
	stateHue,
	stateHueClass,
	stateLabel,
	stateTab,
	times
} from './format';

const STATES: RunState[] = [
	'pending',
	'provisioning',
	'running',
	'waiting_on_human',
	'in_review',
	'paused',
	'completed',
	'abandoned',
	'failed',
	'archived'
];
const SEC = 1000;
const MIN = 60 * SEC;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

describe('run states', () => {
	it.each<[RunState, boolean]>([
		['pending', false],
		['provisioning', false],
		['running', false],
		['waiting_on_human', false],
		['in_review', false],
		['paused', false],
		['completed', true],
		['abandoned', true],
		['failed', true],
		['archived', true]
	])('says whether %s is over', (state, over) => {
		expect(isTerminal(state)).toBe(over);
		expect(stateTab[state]).toBe(over ? 'finished' : 'active');
	});

	it.each<[RunState, string, string, string]>([
		['pending', 'Pending', 'var(--text-muted)', 'hue-muted'],
		['provisioning', 'Provisioning', 'var(--running)', 'hue-running'],
		['running', 'Running', 'var(--running)', 'hue-running'],
		['waiting_on_human', 'Waiting on you', 'var(--attention)', 'hue-attention'],
		['in_review', 'In review', 'var(--accent)', 'hue-accent'],
		['paused', 'Paused', 'var(--text-muted)', 'hue-muted'],
		['completed', 'Completed', 'var(--success)', 'hue-success'],
		['abandoned', 'Abandoned', 'var(--warning)', 'hue-warning'],
		['failed', 'Failed', 'var(--danger)', 'hue-danger'],
		['archived', 'Archived', 'var(--text-faint)', 'faint']
	])('words and colours %s', (state, label, hue, hueClass) => {
		expect(stateLabel[state]).toBe(label);
		expect(stateHue[state]).toBe(hue);
		expect(stateHueClass[state]).toBe(hueClass);
	});

	it('covers every state in every table', () => {
		for (const table of [stateTab, stateHue, stateHueClass, stateLabel])
			expect(Object.keys(table).sort()).toEqual([...STATES].sort());
		expect(stateColors).toBe(stateHue);
	});
});

describe('words', () => {
	it.each([
		['repository', 'repository'],
		['provisioner', 'provisioner'],
		['harness', 'harness'],
		['ticketsource', 'tracker'],
		['tracker', 'tracker'],
		['something_else', 'something_else']
	])('calls the port %s a %s', (port, word) => {
		expect(portLabel(port)).toBe(word);
	});

	it.each([
		['01TESTRUN0000000000000ABCD', undefined, 'r_abcd'],
		['01TESTRUN0000000000000ABCD', 't_', 't_abcd'],
		['ab', undefined, 'r_ab']
	])('shortens %s', (id, prefix, short) => {
		expect(shortId(id, prefix)).toBe(short);
	});

	it.each<[number, string, string | undefined, string]>([
		[0, 'run', undefined, '0 runs'],
		[1, 'run', undefined, '1 run'],
		[2, 'run', undefined, '2 runs'],
		[2, 'entry', 'entries', '2 entries']
	])('counts %i %s', (n, one, many, out) => {
		expect(plural(n, one, many)).toBe(out);
	});

	it('writes a repeat count', () => {
		expect(times(3)).toBe('×3');
	});
});

describe('sizes and money', () => {
	it.each<[number | undefined, string]>([
		[undefined, ''],
		[NaN, ''],
		[0, '0 B'],
		[1023, '1023 B'],
		[1024, '1 KB'],
		[1536, '2 KB'],
		[1024 * 1024, '1.0 MB'],
		[1.5 * 1024 * 1024, '1.5 MB'],
		[1024 ** 3, '1.00 GB'],
		[2.5 * 1024 ** 3, '2.50 GB']
	])('writes %s bytes as %j', (n, out) => {
		expect(fmtBytes(n)).toBe(out);
	});

	it.each<[number, string]>([
		[0, '0 B/s'],
		[0.4, '0 B/s'],
		[511.6, '512 B/s'],
		[2048, '2 KB/s']
	])('writes a rate of %s', (n, out) => {
		expect(fmtRate(n)).toBe(out);
	});

	it.each<[unknown, string]>([
		[0, '$0.000'],
		[1.23456, '$1.235'],
		['1.2', ''],
		[null, ''],
		[undefined, '']
	])('writes a cost of %j as %j', (v, out) => {
		expect(fmtCost(v)).toBe(out);
	});

	it.each<[number, string]>([
		[0, '0.0%'],
		[4.21, '4.2%'],
		[9.9, '9.9%'],
		[10, '10%'],
		[37.4, '37%'],
		[100, '100%']
	])('writes %s percent', (v, out) => {
		expect(fmtPct(v)).toBe(out);
	});
});

describe('spans', () => {
	it.each<[number, string]>([
		[-5 * SEC, '0:00'],
		[0, '0:00'],
		[999, '0:00'],
		[4 * SEC, '0:04'],
		[4 * MIN + 12 * SEC, '4:12'],
		[59 * MIN + 59 * SEC, '59:59'],
		[HOUR, '1:00:00'],
		[HOUR + 2 * MIN + 9 * SEC, '1:02:09'],
		[26 * HOUR, '26:00:00']
	])('reads a live clock of %ims', (ms, out) => {
		expect(fmtClockDuration(ms)).toBe(out);
	});

	it.each<[number, string]>([
		[-MIN, '0:00'],
		[59 * SEC, '0:00'],
		[4 * MIN, '0:04'],
		[HOUR + 12 * MIN, '1:12'],
		[26 * HOUR + 5 * MIN, '26:05']
	])('reads %ims as hours and minutes', (ms, out) => {
		expect(fmtHoursMinutes(ms)).toBe(out);
	});

	it.each<[number, string]>([
		[-SEC, '0s'],
		[0, '0s'],
		[41 * SEC, '41s'],
		[59.9 * SEC, '59s'],
		[60 * SEC, '1m 00s'],
		[14 * MIN + 8 * SEC, '14m 08s'],
		[59 * MIN + 59 * SEC, '59m 59s'],
		[HOUR, '1h 00m'],
		[2 * HOUR + 5 * MIN, '2h 05m'],
		[23 * HOUR + 59 * MIN, '23h 59m'],
		[DAY, '1d 0h'],
		[3 * DAY + 4 * HOUR, '3d 4h']
	])('reads an elapsed %ims', (ms, out) => {
		expect(fmtDuration(ms)).toBe(out);
	});
});

describe('relative times', () => {
	const NOW = Date.UTC(2026, 9, 31, 12, 0, 0);

	beforeEach(() => {
		vi.useFakeTimers();
		vi.setSystemTime(NOW);
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	it.each<[number, string, string]>([
		[-MIN, 'just now', 'now'],
		[0, 'just now', 'now'],
		[9 * SEC, 'just now', 'now'],
		[10 * SEC, '10s ago', '10s'],
		[59 * SEC, '59s ago', '59s'],
		[MIN, '1m ago', '1m'],
		[59 * MIN, '59m ago', '59m'],
		[HOUR, '1h ago', '1h'],
		[23 * HOUR, '23h ago', '23h'],
		[DAY, '1d ago', '1d'],
		[40 * DAY, '40d ago', '40d']
	])('reads something %ims old', (old, relative, short) => {
		expect(ago(NOW - old)).toBe(relative);
		expect(age(NOW - old)).toBe(short);
	});

	it('reads ago from the clock it is given', () => {
		expect(ago(NOW, NOW + 3 * MIN)).toBe('3m ago');
	});

	it.each<[string, string | number | Date]>([
		['a string', new Date(NOW - 2 * HOUR).toISOString()],
		['a number', NOW - 2 * HOUR],
		['a date', new Date(NOW - 2 * HOUR)]
	])('takes %s', (_, ts) => {
		expect(ago(ts)).toBe('2h ago');
	});
});

describe('clock times', () => {
	const at = new Date(2026, 9, 31, 13, 21, 7);

	it('shows the 24-hour clock', () => {
		expect(fmtTime(at)).toBe('13:21:07');
		expect(fmtClock(at)).toBe('13:21');
	});

	it('shows the full date behind a relative time', () => {
		expect(fmtAbs(at)).toBe('Oct 31, 2026 13:21:07');
	});
});

describe('counterRates', () => {
	it('has no rate for the first sample', () => {
		expect(counterRates([5], [0])).toEqual([null]);
		expect(counterRates([], [])).toEqual([]);
	});

	it('divides each step by its seconds', () => {
		expect(counterRates([0, 100, 300], [0, 1000, 3000])).toEqual([null, 100, 100]);
	});

	it('reads a counter going down as a reset, not a negative rate', () => {
		expect(counterRates([100, 5], [0, 1000])).toEqual([null, 0]);
	});

	it.each([
		['the same time', [1000, 1000]],
		['an earlier time', [2000, 1000]]
	])('reads a sample at %s as a gap', (_, ts) => {
		expect(counterRates([0, 100], ts)).toEqual([null, null]);
	});
});
