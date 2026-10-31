// Modal focus management, shared by every dialog (Modal, the command
// palette): focus moves in, Tab cycles inside, Escape closes from anywhere
// (the innermost layer first: see `layers`), and focus returns to where it
// was on close.
//
//   <div role="dialog" aria-modal="true" use:trap={{ onescape: close }}>

import type { Action } from 'svelte/action';

export interface TrapOptions {
	/** Called on Escape, wherever focus is, unless something opened on top of
	 *  the dialog takes it. */
	onescape: () => void;
	/** What to focus first; defaults to the first field, else the last button
	 *  (the action, not Cancel). */
	initial?: () => HTMLElement | null | undefined;
}

const FOCUSABLE = 'button, [href], input, select, textarea, [tabindex]';

/** What Esc can close, innermost last: dialogs and open popovers. A dialog
 *  hears the key first (window, capture) and keeps it from everything under
 *  it, so a popover on top of it closes through `dismiss`. */
export const layers: { dismiss?: () => void }[] = [];

export const trap: Action<HTMLElement, TrapOptions> = (node, initialOpts) => {
	let opts = initialOpts;
	const prev = document.activeElement as HTMLElement | null;
	const me = {};
	layers.push(me);
	const focusables = () =>
		Array.from(node.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
			(e) => !e.hasAttribute('disabled') && e.getAttribute('tabindex') !== '-1'
		);

	queueMicrotask(() => {
		const first =
			opts.initial?.() ??
			node.querySelector<HTMLElement>('input, textarea, select') ??
			focusables().at(-1);
		first?.focus();
	});

	const key = (e: KeyboardEvent) => {
		if (e.key === 'Escape') {
			e.preventDefault();
			e.stopPropagation();
			const top = layers.at(-1);
			if (top === me) opts.onescape();
			else top?.dismiss?.();
		} else if (e.key === 'Tab') {
			const f = focusables();
			if (!f.length) return;
			const i = f.indexOf(document.activeElement as HTMLElement);
			if (e.shiftKey && i <= 0) {
				e.preventDefault();
				f.at(-1)!.focus();
			} else if (!e.shiftKey && (i === -1 || i === f.length - 1)) {
				e.preventDefault();
				f[0].focus();
			}
		}
	};
	window.addEventListener('keydown', key, true);

	return {
		update(next) {
			opts = next;
		},
		destroy() {
			window.removeEventListener('keydown', key, true);
			layers.splice(layers.indexOf(me), 1);
			prev?.focus?.();
		}
	};
};
