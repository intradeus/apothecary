// The slot behind HeaderAction. A layout whose header sits above its pages (the
// System hub, a workshop's tabs) provides one and renders `slot.action` in the
// header's actions.
import { getContext, setContext, type Snippet } from 'svelte';

const KEY = Symbol('header-action');

class HeaderSlot {
	action = $state<Snippet | undefined>();
}

export const provideHeaderSlot = () => setContext(KEY, new HeaderSlot());
export const headerSlot = () => getContext<HeaderSlot>(KEY);
