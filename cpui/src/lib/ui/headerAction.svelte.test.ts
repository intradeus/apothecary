import { render, screen } from '@testing-library/svelte';
import { flushSync, type Component } from 'svelte';
import { describe, expect, it } from 'vitest';
import { text } from '$test/helpers';
import HeaderAction from './HeaderAction.svelte';
import { headerSlot, provideHeaderSlot } from './headerAction.svelte';

// Context only exists while a component initialises, so a bare function stands
// in for the layout. Its context map is the one handed to `render`, which is
// how a page mounted with that map comes to see the layout's slot.
const asComponent = (init: () => void) => init as unknown as Component;

function aLayout() {
	const context = new Map();
	let slot!: ReturnType<typeof provideHeaderSlot>;
	render(
		asComponent(() => (slot = provideHeaderSlot())),
		{ context }
	);
	return { context, slot };
}

describe('the header slot', () => {
	it('starts empty', () => {
		expect(aLayout().slot.action).toBeUndefined();
	});

	it('is the same slot for a page below the layout', () => {
		const { context, slot } = aLayout();
		let seen: unknown;
		render(
			asComponent(() => (seen = headerSlot())),
			{ context }
		);
		expect(seen).toBe(slot);
	});

	it('gives each layout its own', () => {
		expect(aLayout().slot).not.toBe(aLayout().slot);
	});

	it('is absent with no layout above', () => {
		let seen: unknown = 'unset';
		render(
			asComponent(() => (seen = headerSlot())),
			{ context: new Map() }
		);
		expect(seen).toBeUndefined();
	});

	it('receives what a page puts there, and is cleared when the page leaves', () => {
		const { context, slot } = aLayout();
		const page = render(HeaderAction, { props: { children: text('New ticket') }, context });
		flushSync();
		expect(slot.action).toBeTypeOf('function');
		expect(screen.queryByText('New ticket')).toBeNull();

		page.unmount();
		flushSync();
		expect(slot.action).toBeUndefined();
	});

	it('keeps the next page’s action when the last one leaves after it', () => {
		const { context, slot } = aLayout();
		const old = render(HeaderAction, { props: { children: text('Old') }, context });
		const next = text('New');
		render(HeaderAction, { props: { children: next }, context });
		flushSync();

		old.unmount();
		flushSync();

		expect(slot.action).toBe(next);
	});
});
