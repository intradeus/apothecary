// Small things most component tests need. Fixtures for domain objects are in
// ./fixtures.
import { createRawSnippet, type Snippet } from 'svelte';

/** Plain text as a `children`-style snippet prop. */
export const text = (s: string): Snippet =>
	createRawSnippet(() => ({ render: () => `<span>${s}</span>` }));

/** A promise settled by the test: what an in-flight request looks like. */
export function deferred<T = void>() {
	let resolve!: (value: T) => void;
	let reject!: (reason: unknown) => void;
	const promise = new Promise<T>((res, rej) => {
		resolve = res;
		reject = rej;
	});
	return { promise, resolve, reject };
}

/** A DOMRect, for a test that stubs getBoundingClientRect. */
export const rect = (left: number, top: number, width: number, height: number): DOMRect =>
	({
		left,
		top,
		width,
		height,
		right: left + width,
		bottom: top + height,
		x: left,
		y: top,
		toJSON() {}
	}) as DOMRect;
