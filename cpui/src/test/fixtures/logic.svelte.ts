// What a test needs of runes outside a component.

/** Run `fn` inside an effect root, the way a component's setup would; call
 *  what it returns to tear it down. */
export const inEffectRoot = (fn: () => void) => $effect.root(fn);

/** Deep $state: what `FlowEditor.doc` is. */
export function reactive<T>(value: T): T {
	const state = $state(value);
	return state;
}
