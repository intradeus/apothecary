// scrollEdges: a row that scrolls sideways says which of its ends has more
// past it — `data-more-start` and `data-more-end` on the row, while there is —
// so its CSS can fade that end (a window's tabs, too many for its width).
// Kept up to date on scroll, on resize and as children come and go.
import type { Action } from 'svelte/action';

/** Within this many pixels of an end counts as at it. */
const SLACK = 1;

export const scrollEdges: Action<HTMLElement> = (node) => {
	const mark = () => {
		const start = node.scrollLeft > SLACK;
		const end = node.scrollLeft + node.clientWidth < node.scrollWidth - SLACK;
		node.toggleAttribute('data-more-start', start);
		node.toggleAttribute('data-more-end', end);
	};
	node.addEventListener('scroll', mark, { passive: true });
	const ro = new ResizeObserver(mark);
	ro.observe(node);
	const mo = new MutationObserver(mark);
	mo.observe(node, { childList: true, subtree: true, characterData: true });
	mark();
	return {
		destroy() {
			node.removeEventListener('scroll', mark);
			ro.disconnect();
			mo.disconnect();
		}
	};
};
