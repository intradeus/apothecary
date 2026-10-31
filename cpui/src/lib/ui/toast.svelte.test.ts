import { afterEach, describe, expect, it, vi } from 'vitest';
import { toast, toasts } from './toast.svelte';

describe('toast', () => {
	afterEach(() => {
		toasts.list = [];
	});

	it('defaults to a good tone for four seconds', () => {
		toast('Answer sent');
		expect(toasts.list).toEqual([
			{ id: expect.any(Number), text: 'Answer sent', tone: 'ok', action: undefined, ms: 4000 }
		]);
	});

	it('takes a tone, a length and an action', () => {
		const action = { label: 'Open run', run: vi.fn() };
		toast('Rule saved', { tone: 'warn', ms: 9000, action });
		expect(toasts.list[0]).toMatchObject({ tone: 'warn', ms: 9000, action });
	});

	it('keeps a length of zero rather than falling back', () => {
		toast('Gone', { ms: 0 });
		expect(toasts.list[0].ms).toBe(0);
	});

	it('returns a fresh id each time', () => {
		const a = toast('a');
		const b = toast('b');
		expect(b).not.toBe(a);
		expect(toasts.list.map((t) => t.id)).toEqual([a, b]);
	});

	it('keeps the four newest, oldest out first', () => {
		for (const text of ['a', 'b', 'c', 'd', 'e']) toast(text);
		expect(toasts.list.map((t) => t.text)).toEqual(['b', 'c', 'd', 'e']);
	});

	it('dismisses the one asked for', () => {
		const a = toast('a');
		toast('b');
		toasts.dismiss(a);
		expect(toasts.list.map((t) => t.text)).toEqual(['b']);
	});

	it('takes dismissing an unknown or gone toast as nothing', () => {
		const a = toast('a');
		toasts.dismiss(a);
		toasts.dismiss(a);
		toasts.dismiss(-1);
		expect(toasts.list).toEqual([]);
	});

	it('does not reuse an id after a dismiss', () => {
		const a = toast('a');
		toasts.dismiss(a);
		expect(toast('b')).not.toBe(a);
	});
});
