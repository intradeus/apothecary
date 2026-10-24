// keepInView: a row that scrolls sideways (tabs on a phone) keeps its
// selected child — `.on`, or the given selector — inside the visible part,
// scrolling just enough, smoothly after the first placement. Put it on the
// scrolling element itself.
import type { Action } from 'svelte/action';

export const keepInView: Action<HTMLElement, { selector?: string } | undefined> = (node, opts) => {
	let selector = opts?.selector ?? '.on';
	let first = true;
	const PAD = 24;

	const reveal = () => {
		const on = node.querySelector<HTMLElement>(`:scope > ${selector}`);
		if (!on || node.scrollWidth <= node.clientWidth) return;
		const behavior: ScrollBehavior = first ? 'auto' : 'smooth';
		first = false;
		// Where the child starts inside the row's scrolled content.
		const left =
			on.getBoundingClientRect().left - node.getBoundingClientRect().left + node.scrollLeft;
		if (left - PAD < node.scrollLeft) node.scrollTo({ left: left - PAD, behavior });
		else if (left + on.offsetWidth + PAD > node.scrollLeft + node.clientWidth)
			node.scrollTo({ left: left + on.offsetWidth + PAD - node.clientWidth, behavior });
	};

	const mo = new MutationObserver(reveal);
	mo.observe(node, {
		subtree: true,
		attributes: true,
		attributeFilter: ['class'],
		childList: true
	});
	const ro = new ResizeObserver(reveal);
	ro.observe(node);
	requestAnimationFrame(reveal);

	return {
		update(next) {
			selector = next?.selector ?? '.on';
			reveal();
		},
		destroy() {
			mo.disconnect();
			ro.disconnect();
		}
	};
};
