import { render } from '@testing-library/svelte';
import type { Component } from 'svelte';
import { describe, expect, it } from 'vitest';
import { text } from '$test/helpers';
import HeaderAction from './HeaderAction.svelte';
import { provideHeaderSlot } from './headerAction.svelte';

type Slot = ReturnType<typeof provideHeaderSlot>;

// A layout providing the slot, as a component function: `context` gives it the
// component context setContext needs, without a template.
function page(children = text('New ticket')) {
	let slot!: Slot;
	const Layout = ((anchor: never, props: never) => {
		slot = provideHeaderSlot();
		return (HeaderAction as unknown as (a: never, p: never) => unknown)(anchor, props);
	}) as unknown as Component<{ children: typeof children }>;
	const view = render(Layout, { props: { children }, context: new Map() });
	return { slot, children, ...view };
}

describe('HeaderAction', () => {
	it('puts what it wraps in the header', () => {
		const { slot, children } = page();
		expect(slot.action).toBe(children);
	});

	it('renders nothing where it stands', () => {
		const { container } = page();
		expect(container).toHaveTextContent('');
	});

	it('takes it back when the page leaves', () => {
		const { slot, unmount } = page();
		unmount();
		expect(slot.action).toBeUndefined();
	});

	// The next page's effect can run before this one's cleanup.
	it('leaves the next page’s action alone when it leaves', () => {
		const { slot, unmount } = page();
		const next = text('New run');
		slot.action = next;
		unmount();
		expect(slot.action).toBe(next);
	});
});
