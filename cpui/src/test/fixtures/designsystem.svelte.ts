// For the design-system tests. A test file can't use runes or a template, so
// what a parent component would do for a control lives here.
import { createRawSnippet, mount, unmount, type Component, type Snippet } from 'svelte';

/** Props with one bound the way a parent's `bind:` would: the component's
 *  writes land in `current()` and render, and a write to `props[name]` reaches
 *  it from outside. */
export function bound<K extends string, T, R extends object = object>(
	name: K,
	initial: T,
	rest?: R
) {
	let current = $state.raw(initial);
	const props = Object.defineProperty({ ...rest }, name, {
		get: () => current,
		set: (v: T) => (current = v),
		enumerable: true,
		configurable: true
	}) as R & Record<K, T>;
	return { props, current: () => current };
}

/** Static markup as a snippet: one root element, for a field or a button. */
export const html = (markup: string): Snippet => createRawSnippet(() => ({ render: () => markup }));

/** A real component as a snippet, for one nested inside another. */
export const nested = <P extends Record<string, unknown>>(
	component: Component<P>,
	props: P
): Snippet =>
	createRawSnippet(() => ({
		render: () => '<div></div>',
		setup(target) {
			const child = mount(component, { target, props });
			return () => void unmount(child);
		}
	}));
