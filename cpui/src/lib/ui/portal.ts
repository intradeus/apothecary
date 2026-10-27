// Moves an element to <body> while it lives. For surfaces that must escape
// their place in the tree: a `position: fixed` element inside glass (.c, .lg
// — backdrop-filter makes them its containing block) would be positioned and
// clipped by that pane instead of the viewport.
//
//   <div use:portal>…</div>     (use:portal={false} leaves it in place)
import type { Action } from 'svelte/action';

export const portal: Action<HTMLElement, boolean | undefined> = (node, on = true) => {
	if (on) document.body.appendChild(node);
	return {
		destroy() {
			if (on && node.parentNode === document.body) node.remove();
		}
	};
};
