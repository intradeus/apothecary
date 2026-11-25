import { flushSync } from 'svelte';
import { afterEach, describe, expect, it } from 'vitest';
import { assistant, offerPage, pageKey, type PageAssist } from './assistant.svelte';
import { HandoffGuard } from './fillGuard.svelte';

const target = (over: Partial<PageAssist> = {}): PageAssist => ({
	kind: 'run',
	subject: 'r1',
	workshop: 'w1',
	guard: new HandoffGuard(() => null),
	pageState: () => ({}),
	...over
});

afterEach(() => {
	assistant.close();
	assistant.chosen = 'general';
	assistant.carried = '';
});

describe('assistant', () => {
	it('opens about the page when one offers itself, and everywhere otherwise', () => {
		assistant.show();
		expect([assistant.open, assistant.mode]).toEqual([true, 'general']);
		const withdraw = assistant.offer(target());
		assistant.show();
		expect(assistant.mode).toBe('page');
		assistant.show('general');
		expect(assistant.mode).toBe('general');
		withdraw();
	});

	it('shows page mode only while a page is offered, the last one offered', () => {
		const first = assistant.offer(target({ subject: 'r1' }));
		const second = assistant.offer(target({ subject: 'r2' }));
		assistant.show('page');
		expect(assistant.page?.subject).toBe('r2');
		second();
		expect(assistant.page?.subject).toBe('r1');
		first();
		expect([assistant.page, assistant.mode, assistant.chosen]).toEqual([null, 'general', 'page']);
		// The page's mode comes back with a page.
		const again = assistant.offer(target());
		expect(assistant.mode).toBe('page');
		again();
	});

	it('opens, switches and closes on the keyboard’s toggle', () => {
		assistant.toggle();
		expect([assistant.open, assistant.mode]).toEqual([true, 'general']);
		assistant.toggle();
		expect(assistant.open).toBe(false);
		const withdraw = assistant.offer(target());
		assistant.toggle();
		expect(assistant.mode).toBe('page');
		assistant.toggle();
		expect([assistant.open, assistant.mode]).toEqual([true, 'general']);
		assistant.toggle();
		expect(assistant.mode).toBe('page');
		withdraw();
	});

	it('carries a question to everywhere, opening it', () => {
		const withdraw = assistant.offer(target());
		assistant.show('page');
		assistant.carry('start a run');
		expect([assistant.open, assistant.mode, assistant.carried]).toEqual([
			true,
			'general',
			'start a run'
		]);
		withdraw();
	});

	it('offers a page while its effect lives, again as what it reads changes', () => {
		let subject = $state('r1');
		let on = $state(true);
		const stop = $effect.root(() => offerPage(() => (on ? target({ subject }) : null)));
		flushSync();
		expect(assistant.page?.subject).toBe('r1');
		subject = 'r2';
		flushSync();
		expect(assistant.page?.subject).toBe('r2');
		on = false;
		flushSync();
		expect(assistant.page).toBeNull();
		on = true;
		flushSync();
		stop();
		expect(assistant.page).toBeNull();
	});

	it('keys a page’s conversation by its kind, subject and workshop', () => {
		expect(pageKey(target())).toBe(pageKey(target({ name: 'other name' })));
		expect(pageKey(target())).not.toBe(pageKey(target({ workshop: 'w2' })));
		expect(pageKey(target({ workshop: undefined }))).toBe(pageKey(target({ workshop: '' })));
	});
});
