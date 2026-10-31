// Motion for things CSS can't do alone: enter/exit transitions on mount,
// the sliding selection indicator, the count bump and drag-to-dismiss.
//
// Everything follows the fluid-interface rules (DESIGN 2.5):
//  - one spring (damping 1.0); the bouncier one only after a flick
//  - exits retrace the entry path; Svelte's `transition:` reverses from the
//    live progress, so an interrupted open/close never jumps
//  - drags track 1:1 from where they were grabbed, rubber-band at their
//    limits, project momentum on release and hand the velocity to the spring
//  - durations come from tokens, so prefers-reduced-motion (which zeroes the
//    move tokens) turns every one of these into a plain fade

import type { Action } from 'svelte/action';
import type { TransitionConfig } from 'svelte/transition';
import { token } from '$lib/design/tokens';

// ── springs ─────────────────────────────────────────────────────────────

/** Critically damped spring (damping 1.0, response 0.35s) over its settle
 *  time, as a 0→1 easing. The same curve as the CSS --spring. */
export const spring = (t: number): number => {
	if (t >= 1) return 1;
	const w = (2 * Math.PI) / 0.35;
	const x = t * 0.45;
	return 1 - (1 + w * x) * Math.exp(-w * x);
};

/** Damping 0.8, response 0.4s: a little overshoot, for momentum only. */
export const springBounce = (t: number): number => {
	if (t >= 1) return 1;
	const z = 0.8;
	const w = (2 * Math.PI) / 0.4;
	const wd = w * Math.sqrt(1 - z * z);
	const x = t * 0.6;
	return (
		1 - Math.exp(-z * w * x) * (Math.cos(wd * x) + (z / Math.sqrt(1 - z * z)) * Math.sin(wd * x))
	);
};

const easeOut = (t: number) => 1 - Math.pow(1 - t, 3);

/** A duration token in ms ("450ms" → 450). Reduced motion makes the move
 *  tokens ~0, which is how every transition here degrades to a fade. */
export const ms = (name: string, fallback: number): number => {
	const v = token(name);
	const n = parseFloat(v);
	if (Number.isNaN(n)) return fallback;
	return v.endsWith('ms') ? n : n * 1000;
};

const reduced = () =>
	typeof matchMedia !== 'undefined' && matchMedia('(prefers-reduced-motion: reduce)').matches;

// ── enter / exit ────────────────────────────────────────────────────────

export interface MaterializeParams {
	/** transform-origin: where the surface grows from (its trigger). */
	origin?: string;
	/** Extra travel in px toward/away from the origin. */
	y?: number;
	x?: number;
}

/** A glass surface arriving: scale, blur and opacity together, growing out
 *  of its trigger, and leaving the same way. For popovers, menus, dialogs. */
export function materialize(node: Element, p: MaterializeParams = {}): TransitionConfig {
	const move = ms('--dur-spring', 450);
	const fade = ms('--dur-fade', 160);
	const scale0 = parseFloat(token('--enter-scale')) || 1;
	const blur0 = parseFloat(token('--enter-blur')) || 0;
	const x0 = reduced() ? 0 : (p.x ?? 0);
	const y0 = reduced() ? 0 : (p.y ?? 0);
	const duration = Math.max(move, fade);
	(node as HTMLElement).style.transformOrigin = p.origin ?? 'center';
	return {
		duration,
		css: (t) => {
			const m = spring(Math.min(1, (t * duration) / move));
			const o = easeOut(Math.min(1, (t * duration) / fade));
			const s = scale0 + (1 - scale0) * m;
			return `opacity:${o};transform:translate(${x0 * (1 - m)}px,${y0 * (1 - m)}px) scale(${s});filter:blur(${blur0 * (1 - m)}px)`;
		}
	};
}

/** A scrim fading in and out under a dialog. */
export function scrim(_node: Element): TransitionConfig {
	return { duration: ms('--dur-fade', 160) * 1.4, css: (t) => `opacity:${easeOut(t)}` };
}

/** A sheet travelling in from an edge and back out the same edge (drawers,
 *  toasts). `distance` defaults to the node's own size along the axis. */
export function slide(
	node: Element,
	p: { from: 'left' | 'right' | 'top' | 'bottom'; distance?: number }
): TransitionConfig {
	const el = node as HTMLElement;
	const horizontal = p.from === 'left' || p.from === 'right';
	const dist = p.distance ?? (horizontal ? el.offsetWidth : el.offsetHeight) + 16;
	const sign = p.from === 'left' || p.from === 'top' ? -1 : 1;
	const move = ms('--dur-spring', 450);
	const fade = ms('--dur-fade', 160);
	if (reduced()) return { duration: fade, css: (t) => `opacity:${easeOut(t)}` };
	return {
		duration: move,
		css: (t) => {
			const d = sign * dist * (1 - spring(t));
			return `transform:${horizontal ? `translateX(${d}px)` : `translateY(${d}px)`};opacity:${Math.min(1, t * 3)}`;
		}
	};
}

// ── the sliding selection indicator ─────────────────────────────────────

/** A bead that slides between options (segmented controls, tabs, side
 *  navs) instead of jumping. Put it on the container; it keeps a `.ind`
 *  element under whichever child matches `selector` (default `.on`) and
 *  springs it there whenever the selection or the layout changes. The first
 *  placement doesn't animate. Style `.ind` in the component (or use the
 *  shared one in components.css). */
export const indicator: Action<HTMLElement, { selector?: string } | undefined> = (node, opts) => {
	let selector = opts?.selector ?? '.on';
	const ind = document.createElement('span');
	ind.className = 'ind';
	ind.setAttribute('aria-hidden', 'true');
	node.prepend(ind);
	// The selected option stops drawing its own bead; .ind draws it.
	node.classList.add('has-ind');
	let placed = false;

	const place = () => {
		const on = node.querySelector<HTMLElement>(`:scope > ${selector}`);
		if (!on) {
			ind.style.opacity = '0';
			return;
		}
		ind.style.opacity = '1';
		if (!placed) ind.style.transition = 'none';
		ind.style.width = `${on.offsetWidth}px`;
		ind.style.height = `${on.offsetHeight}px`;
		ind.style.transform = `translate(${on.offsetLeft}px, ${on.offsetTop}px)`;
		if (!placed) {
			void ind.offsetWidth; // commit the jump before re-enabling motion
			ind.style.transition = '';
			placed = true;
		}
	};

	const mo = new MutationObserver(place);
	mo.observe(node, {
		subtree: true,
		attributes: true,
		attributeFilter: ['class'],
		childList: true
	});
	const ro = new ResizeObserver(place);
	ro.observe(node);
	requestAnimationFrame(place);

	return {
		update(next) {
			selector = next?.selector ?? '.on';
			place();
		},
		destroy() {
			mo.disconnect();
			ro.disconnect();
			ind.remove();
			node.classList.remove('has-ind');
		}
	};
};

// ── the count bump ──────────────────────────────────────────────────────

/** Pop a count when it goes UP (something new arrived), never when it falls:
 *  status feedback that earns its place. `use:bump={count}`. */
export const bump: Action<HTMLElement, number> = (node, value) => {
	let last = value;
	return {
		update(next) {
			if (next > last) {
				node.classList.remove('bumped');
				void node.offsetWidth; // restart the animation
				node.classList.add('bumped');
			}
			last = next;
		}
	};
};

// ── drag to dismiss ─────────────────────────────────────────────────────

export interface DragDismissParams {
	/** The direction that dismisses. */
	direction: 'left' | 'right' | 'down' | 'up';
	ondismiss: () => void;
	/** Fraction of the node's size past which a slow release still dismisses. */
	threshold?: number;
}

/** Apple's momentum projection (Designing Fluid Interfaces): where a flick
 *  would come to rest. decel 0.998 ≈ scroll feel. */
export const project = (velocityPxPerS: number, decel = 0.998) =>
	((velocityPxPerS / 1000) * decel) / (1 - decel);

/** Progressive resistance past a boundary instead of a hard stop. */
export const rubberband = (overshoot: number, dimension: number, c = 0.55) =>
	(overshoot * dimension * c) / (dimension + c * Math.abs(overshoot));

/** Drag a sheet (drawer, toast) to dismiss it. Tracks the pointer 1:1 from
 *  where it was grabbed after a 10px intent threshold, resists in the wrong
 *  direction, and on release projects the flick: past the threshold it
 *  leaves with the finger's velocity, otherwise it springs home. Clicks on
 *  controls inside still work (it only takes over once a drag is clear). */
export const dragDismiss: Action<HTMLElement, DragDismissParams> = (node, initial) => {
	let p = initial;
	const axis = () => (p.direction === 'left' || p.direction === 'right' ? 'x' : 'y');
	const sign = () => (p.direction === 'left' || p.direction === 'up' ? -1 : 1);
	let start: { x: number; y: number; id: number } | null = null;
	let dragging = false;
	let hist: { t: number; v: number }[] = [];
	let offset = 0;

	const set = (d: number) => {
		node.style.transform = axis() === 'x' ? `translateX(${d}px)` : `translateY(${d}px)`;
	};

	const down = (e: PointerEvent) => {
		// A touch drag ends with no click to swallow, so the mark can't outlive it.
		delete node.dataset.dragged;
		if (e.button !== 0) return;
		start = { x: e.clientX, y: e.clientY, id: e.pointerId };
		dragging = false;
		hist = [];
	};
	const move = (e: PointerEvent) => {
		if (!start || e.pointerId !== start.id) return;
		const dx = e.clientX - start.x;
		const dy = e.clientY - start.y;
		const along = axis() === 'x' ? dx : dy;
		const across = axis() === 'x' ? dy : dx;
		if (!dragging) {
			// Commit only once intent is clear and along our axis (hysteresis).
			if (Math.hypot(dx, dy) < 10) return;
			if (Math.abs(across) > Math.abs(along)) {
				start = null;
				return;
			}
			dragging = true;
			node.setPointerCapture(e.pointerId);
			node.style.transition = 'none';
		}
		const size = axis() === 'x' ? node.offsetWidth : node.offsetHeight;
		// With the dismiss direction: 1:1. Against it: rubber-band.
		const signed = along * sign();
		offset = signed >= 0 ? along : sign() * -rubberband(-signed, size);
		set(offset);
		const now = performance.now();
		hist.push({ t: now, v: along });
		hist = hist.filter((h) => now - h.t < 100);
	};
	const up = (e: PointerEvent) => {
		if (!start || e.pointerId !== start.id) return;
		start = null;
		if (!dragging) return;
		dragging = false;
		const size = axis() === 'x' ? node.offsetWidth : node.offsetHeight;
		const first = hist[0];
		const last = hist.at(-1);
		const dt = first && last ? (last.t - first.t) / 1000 : 0;
		const velocity = dt > 0 ? ((last!.v - first!.v) / dt) * sign() : 0; // px/s toward dismiss
		const projected = offset * sign() + project(velocity);
		const dismiss = projected > size * (p.threshold ?? 0.5);
		if (dismiss) {
			// Hand the release velocity to the exit: the faster the flick, the
			// shorter the remaining trip.
			const remaining = size + 24 - offset * sign();
			const t = Math.max(120, Math.min(360, (remaining / Math.max(velocity, 600)) * 1000));
			node.style.transition = `transform ${t}ms cubic-bezier(0.2, 0.8, 0.2, 1), opacity ${t}ms`;
			set(sign() * (size + 24));
			node.style.opacity = '0';
			setTimeout(() => p.ondismiss(), t);
		} else {
			// Released with momentum back toward home: the bouncier spring.
			node.style.transition = `transform var(--dur-bounce) var(--spring-bounce)`;
			set(0);
		}
		offset = 0;
	};
	// A drag that turned into a click must not also click.
	const click = (e: MouseEvent) => {
		if (node.dataset.dragged === '1') {
			e.stopPropagation();
			e.preventDefault();
			delete node.dataset.dragged;
		}
	};
	const mark = () => {
		if (dragging) node.dataset.dragged = '1';
	};

	node.style.touchAction = axis() === 'x' ? 'pan-y' : 'pan-x';
	node.addEventListener('pointerdown', down);
	node.addEventListener('pointermove', move);
	node.addEventListener('pointermove', mark);
	node.addEventListener('pointerup', up);
	node.addEventListener('pointercancel', up);
	node.addEventListener('click', click, true);
	return {
		update(next) {
			p = next;
		},
		destroy() {
			node.removeEventListener('pointerdown', down);
			node.removeEventListener('pointermove', move);
			node.removeEventListener('pointermove', mark);
			node.removeEventListener('pointerup', up);
			node.removeEventListener('pointercancel', up);
			node.removeEventListener('click', click, true);
		}
	};
};
