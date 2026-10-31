import { beforeEach, describe, expect, it, vi } from 'vitest';
import { readTests, writeTest } from './testResults';

const KEY = 'apothecary.integrationTests';

beforeEach(() => localStorage.clear());

describe('test results', () => {
	it('reads nothing before anything was saved', () => {
		expect(readTests()).toEqual({});
	});

	it('keeps one result per integration', () => {
		writeTest('a', { ok: true, at: 1 });
		writeTest('b', { ok: false, error: 'denied', at: 2 });
		expect(readTests()).toEqual({
			a: { ok: true, at: 1 },
			b: { ok: false, error: 'denied', at: 2 }
		});
	});

	it('replaces the last result', () => {
		writeTest('a', { ok: false, at: 1 });
		writeTest('a', { ok: true, warnings: ['no probe'], at: 2 });
		expect(readTests().a).toEqual({ ok: true, warnings: ['no probe'], at: 2 });
	});

	it('forgets one result without touching the others', () => {
		writeTest('a', { ok: true, at: 1 });
		writeTest('b', { ok: true, at: 2 });
		writeTest('a', null);
		expect(Object.keys(readTests())).toEqual(['b']);
	});

	it('reads corrupt storage as empty', () => {
		localStorage.setItem(KEY, '{not json');
		expect(readTests()).toEqual({});
	});

	it('survives blocked storage', () => {
		vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
			throw new DOMException('blocked', 'SecurityError');
		});
		vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
			throw new DOMException('blocked', 'SecurityError');
		});
		expect(readTests()).toEqual({});
		expect(() => writeTest('a', { ok: true, at: 1 })).not.toThrow();
	});
});
