// What a view is handed (ViewPlace) follows field by field: moving a divider
// changes a view's size, never its `visible`, so an effect that reads only
// `visible` (a log pane loading its log) does not run again on every frame.
import { fireEvent, render } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { aProbe, sizeDesk, twoWindows } from '$test/fixtures/desk';
import Desk from './Desk.svelte';

beforeEach(() => {
	localStorage.clear();
	sizeDesk();
});
afterEach(() => vi.restoreAllMocks());

it('wakes a view reading `visible` only when it changes, not as a divider moves', async () => {
	const { desk } = twoWindows();
	const probe = aProbe();
	render(Desk, { desk, meta: () => ({}), view: probe.view });
	await vi.waitFor(() => expect(desk.rects.length).toBeGreaterThan(0));
	// Read as a view reads its props: through the snippet's argument.
	const place = () => probe.place('session');
	let runs = 0;
	const stop = $effect.root(() => {
		$effect(() => {
			void place().visible;
			runs++;
		});
	});
	flushSync();
	const width = place().w;
	const g = document.querySelector<HTMLElement>('.gutter')!;
	const at = (x: number) => ({ clientX: x, clientY: 400, pointerId: 1 });
	await fireEvent.pointerDown(g, { ...at(600), button: 0 });
	for (const x of [620, 650, 700]) {
		await fireEvent.pointerMove(g, at(x));
		flushSync();
	}
	await fireEvent.pointerUp(g, at(700));
	flushSync();
	expect(place().w).not.toBe(width);
	expect(place().visible).toBe(true);
	expect(runs).toBe(1);
	stop();
});
