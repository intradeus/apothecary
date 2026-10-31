import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { token } from '$lib/design/tokens';
import {
	bump,
	dragDismiss,
	indicator,
	materialize,
	ms,
	project,
	rubberband,
	scrim,
	slide,
	spring,
	springBounce,
	type DragDismissParams
} from './motion';

vi.mock('$lib/design/tokens', () => ({ token: vi.fn() }));

const TOKENS: Record<string, string> = {
	'--dur-spring': '450ms',
	'--dur-fade': '160ms',
	'--enter-scale': '0.96',
	'--enter-blur': '8px'
};
const reducedMotion = () =>
	vi.spyOn(globalThis, 'matchMedia').mockReturnValue({ matches: true } as MediaQueryList);

beforeEach(() => {
	vi.mocked(token).mockImplementation((name) => TOKENS[name] ?? '');
});
afterEach(() => {
	document.body.replaceChildren();
});

describe('spring', () => {
	it('runs from 0 to exactly 1', () => {
		expect(spring(0)).toBe(0);
		expect(spring(1)).toBe(1);
		expect(spring(2)).toBe(1);
	});

	it('never overshoots or turns back: damping 1', () => {
		let last = 0;
		for (let t = 0.01; t < 1; t += 0.01) {
			const v = spring(t);
			expect(v).toBeGreaterThanOrEqual(last);
			expect(v).toBeLessThanOrEqual(1);
			last = v;
		}
		expect(last).toBeGreaterThan(0.99);
	});
});

describe('springBounce', () => {
	it('runs from 0 to exactly 1', () => {
		expect(springBounce(0)).toBeCloseTo(0, 10);
		expect(springBounce(1)).toBe(1);
		expect(springBounce(2)).toBe(1);
	});

	it('overshoots a little on the way: damping 0.8', () => {
		const samples = Array.from({ length: 99 }, (_, i) => springBounce((i + 1) / 100));
		expect(Math.max(...samples)).toBeGreaterThan(1);
		expect(Math.max(...samples)).toBeLessThan(1.05);
	});
});

describe('ms', () => {
	it.each([
		['450ms', 450],
		['0.45s', 450],
		['0ms', 0],
		['0s', 0]
	])('reads %s as %d', (value, expected) => {
		vi.mocked(token).mockReturnValue(value);
		expect(ms('--x', 999)).toBe(expected);
	});

	it.each(['', 'fast'])('falls back on %j', (value) => {
		vi.mocked(token).mockReturnValue(value);
		expect(ms('--x', 999)).toBe(999);
	});

	it('reads the token it was asked for', () => {
		ms('--dur-fade', 0);
		expect(token).toHaveBeenCalledWith('--dur-fade');
	});
});

describe('materialize', () => {
	const el = () => document.createElement('div');

	it('grows from the origin it is given, or the centre', () => {
		const a = el();
		materialize(a, { origin: 'left top' });
		expect(a.style.transformOrigin).toBe('left top');

		const b = el();
		materialize(b);
		expect(b.style.transformOrigin).toBe('center');
	});

	it('lasts as long as its longer part', () => {
		expect(materialize(el()).duration).toBe(450);

		TOKENS['--dur-spring'] = '0ms';
		expect(materialize(el()).duration).toBe(160);
		TOKENS['--dur-spring'] = '450ms';
	});

	it('starts small, blurred, away and invisible', () => {
		const css = materialize(el(), { x: 4, y: -6 }).css!(0, 1);
		expect(css).toBe('opacity:0;transform:translate(4px,-6px) scale(0.96);filter:blur(8px)');
	});

	it('ends exactly in place', () => {
		const css = materialize(el(), { x: 4, y: -6 }).css!(1, 0);
		expect(css).toBe('opacity:1;transform:translate(0px,0px) scale(1);filter:blur(0px)');
	});

	it('has no travel under reduced motion', () => {
		reducedMotion();
		expect(materialize(el(), { x: 4, y: -6 }).css!(0, 1)).toContain('translate(0px,0px)');
	});

	it('scales from full size when the tokens are missing', () => {
		vi.mocked(token).mockReturnValue('');
		const css = materialize(el()).css!(0, 1);
		expect(css).toContain('scale(1)');
		expect(css).toContain('blur(0px)');
	});
});

describe('scrim', () => {
	it('fades a little longer than the surface above it', () => {
		const { duration, css } = scrim(document.createElement('div'));
		expect(duration).toBeCloseTo(224);
		expect(css!(0, 1)).toBe('opacity:0');
		expect(css!(1, 0)).toBe('opacity:1');
	});
});

describe('slide', () => {
	const sized = () => {
		const el = document.createElement('div');
		Object.defineProperty(el, 'offsetWidth', { value: 300 });
		Object.defineProperty(el, 'offsetHeight', { value: 200 });
		return el;
	};

	it.each([
		['left', 'translateX(-316px)'],
		['right', 'translateX(316px)'],
		['top', 'translateY(-216px)'],
		['bottom', 'translateY(216px)']
	] as const)('starts fully off the %s edge', (from, transform) => {
		expect(slide(sized(), { from }).css!(0, 1)).toBe(`transform:${transform};opacity:0`);
	});

	it('travels the given distance instead when told', () => {
		const { css } = slide(sized(), { from: 'bottom', distance: 40 });
		expect(css!(0, 1)).toContain('translateY(40px)');
	});

	it('ends in place', () => {
		expect(slide(sized(), { from: 'right' }).css!(1, 0)).toBe(
			'transform:translateX(0px);opacity:1'
		);
	});

	it('is a plain fade under reduced motion', () => {
		reducedMotion();
		const { duration, css } = slide(sized(), { from: 'left' });
		expect(duration).toBe(160);
		expect(css!(0, 1)).toBe('opacity:0');
		expect(css!(1, 0)).toBe('opacity:1');
	});
});

describe('indicator', () => {
	let frame: FrameRequestCallback | undefined;

	function anOption(parent: HTMLElement, left: number, cls = '') {
		const o = parent.appendChild(document.createElement('button'));
		o.className = cls;
		Object.defineProperties(o, {
			offsetWidth: { value: 80 },
			offsetHeight: { value: 30 },
			offsetLeft: { value: left },
			offsetTop: { value: 2 }
		});
		return o;
	}
	const aGroup = () => document.body.appendChild(document.createElement('div'));
	const bead = (group: HTMLElement) => group.querySelector<HTMLElement>('.ind')!;
	const settled = () => new Promise((r) => queueMicrotask(() => r(undefined)));

	beforeEach(() => {
		frame = undefined;
		vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
			frame = cb;
			return 1;
		});
	});

	it('adds one bead, first, hidden from assistive tech', () => {
		const group = aGroup();
		anOption(group, 0, 'on');
		indicator(group, undefined);
		expect(group.firstElementChild).toBe(bead(group));
		expect(bead(group)).toHaveAttribute('aria-hidden', 'true');
		expect(group).toHaveClass('has-ind');
	});

	it('sits over the selected option on the first frame', () => {
		const group = aGroup();
		anOption(group, 0);
		anOption(group, 90, 'on');
		indicator(group, undefined);
		frame?.(0);

		const { style } = bead(group);
		expect(style.width).toBe('80px');
		expect(style.height).toBe('30px');
		expect(style.transform).toBe('translate(90px, 2px)');
		expect(style.opacity).toBe('1');
	});

	it('hides with nothing selected', () => {
		const group = aGroup();
		anOption(group, 0);
		indicator(group, undefined);
		frame?.(0);
		expect(bead(group).style.opacity).toBe('0');
	});

	it('follows the selection', async () => {
		const group = aGroup();
		const a = anOption(group, 0, 'on');
		const b = anOption(group, 90);
		indicator(group, undefined);
		frame?.(0);

		a.classList.remove('on');
		b.classList.add('on');
		await settled();

		expect(bead(group).style.transform).toBe('translate(90px, 2px)');
	});

	it('hides when the selection goes', async () => {
		const group = aGroup();
		const a = anOption(group, 0, 'on');
		indicator(group, undefined);
		frame?.(0);

		a.remove();
		await settled();

		expect(bead(group).style.opacity).toBe('0');
	});

	it('takes a custom selector, and a new one on update', () => {
		const group = aGroup();
		anOption(group, 10, 'x');
		anOption(group, 20, 'y');
		const action = indicator(group, { selector: '.x' });
		frame?.(0);
		expect(bead(group).style.transform).toBe('translate(10px, 2px)');

		action?.update?.({ selector: '.y' });
		expect(bead(group).style.transform).toBe('translate(20px, 2px)');
	});

	it('leaves the container as it found it', () => {
		const group = aGroup();
		anOption(group, 0, 'on');
		const action = indicator(group, undefined);

		action?.destroy?.();

		expect(bead(group)).toBeNull();
		expect(group).not.toHaveClass('has-ind');
	});
});

describe('bump', () => {
	const aCount = () => document.createElement('span');

	it('pops when the count goes up', () => {
		const el = aCount();
		bump(el, 1)?.update?.(2);
		expect(el).toHaveClass('bumped');
	});

	it.each([
		['falls', 1],
		['holds', 2]
	])('stays still when it %s', (_, next) => {
		const el = aCount();
		bump(el, 2)?.update?.(next);
		expect(el).not.toHaveClass('bumped');
	});

	it('compares with the last value, not the first', () => {
		const el = aCount();
		const action = bump(el, 5);
		action?.update?.(1);
		action?.update?.(3);
		expect(el).toHaveClass('bumped');
	});

	it('restarts the pop on the next rise', () => {
		const el = aCount();
		const removed = vi.spyOn(el.classList, 'remove');
		const action = bump(el, 1);
		action?.update?.(2);
		action?.update?.(3);
		expect(removed).toHaveBeenCalledTimes(2);
		expect(el).toHaveClass('bumped');
	});
});

describe('project', () => {
	it('carries a flick on past where it let go', () => {
		expect(project(1000)).toBeCloseTo(499);
	});

	it('keeps the direction of the flick', () => {
		expect(project(-1000)).toBeCloseTo(-499);
		expect(project(0)).toBe(0);
	});

	it('goes less far under a harder brake', () => {
		expect(project(1000, 0.99)).toBeLessThan(project(1000));
	});
});

describe('rubberband', () => {
	it('gives nothing for nothing', () => {
		expect(rubberband(0, 300)).toBe(0);
	});

	it('resists from the start', () => {
		expect(rubberband(100, 300)).toBeLessThan(100);
		expect(rubberband(100, 300)).toBeGreaterThan(0);
	});

	it('keeps giving, ever less, and never reaches the dimension', () => {
		const near = rubberband(100, 300);
		const far = rubberband(1000, 300);
		const very = rubberband(1_000_000, 300);
		expect(far).toBeGreaterThan(near);
		expect(far - near).toBeLessThan(900);
		expect(very).toBeLessThan(300);
		expect(very).toBeGreaterThan(299);
	});

	it('pulls the same both ways', () => {
		expect(rubberband(-100, 300)).toBeCloseTo(-rubberband(100, 300));
	});
});

describe('dragDismiss', () => {
	const WIDTH = 300;
	const HEIGHT = 200;
	let node: HTMLElement;
	let inside: HTMLButtonElement;
	let ondismiss: ReturnType<typeof vi.fn<() => void>>;

	const pointer = (type: string, x: number, y = 0, over: PointerEventInit = {}) =>
		node.dispatchEvent(
			new PointerEvent(type, {
				bubbles: true,
				pointerId: 1,
				button: 0,
				clientX: x,
				clientY: y,
				...over
			})
		);
	const action = (over: Partial<DragDismissParams> = {}) =>
		dragDismiss(node, { direction: 'right', ondismiss, ...over });
	/** A clear, slow drag: one move, so there is no velocity to project. */
	const slowDrag = (to: number) => {
		pointer('pointerdown', 0);
		pointer('pointermove', to);
		pointer('pointerup', to);
	};

	beforeEach(() => {
		vi.useFakeTimers();
		ondismiss = vi.fn<() => void>();
		node = document.body.appendChild(document.createElement('div'));
		inside = node.appendChild(document.createElement('button'));
		Object.defineProperty(node, 'offsetWidth', { value: WIDTH });
		Object.defineProperty(node, 'offsetHeight', { value: HEIGHT });
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	it('lets the page scroll across its axis', () => {
		action();
		expect(node.style.touchAction).toBe('pan-y');

		const up = document.createElement('div');
		dragDismiss(up, { direction: 'up', ondismiss });
		expect(up.style.touchAction).toBe('pan-x');
	});

	it('ignores anything under the 10px intent threshold', () => {
		const capture = vi.spyOn(node, 'setPointerCapture');
		action();

		pointer('pointerdown', 0);
		pointer('pointermove', 9);
		pointer('pointerup', 9);
		vi.runAllTimers();

		expect(node.style.transform).toBe('');
		expect(capture).not.toHaveBeenCalled();
		expect(ondismiss).not.toHaveBeenCalled();
	});

	it('tracks the finger 1:1 with capture once the drag is clear', () => {
		const capture = vi.spyOn(node, 'setPointerCapture');
		action();

		pointer('pointerdown', 100);
		pointer('pointermove', 140);

		expect(node.style.transform).toBe('translateX(40px)');
		expect(node.style.transition).toBe('none');
		expect(capture).toHaveBeenCalledExactlyOnceWith(1);
	});

	it('tracks along y for a vertical sheet', () => {
		action({ direction: 'down' });

		pointer('pointerdown', 0, 10);
		pointer('pointermove', 0, 60);

		expect(node.style.transform).toBe('translateY(50px)');
	});

	it('gives the gesture back when it starts across the axis', () => {
		action();

		pointer('pointerdown', 0, 0);
		pointer('pointermove', 2, 40);
		pointer('pointermove', 200, 40);

		expect(node.style.transform).toBe('');
	});

	it.each([
		['right', -100, '-'],
		['left', 100, '']
	] as const)('rubber-bands the wrong way on a %s sheet', (direction, to, sign) => {
		action({ direction });

		pointer('pointerdown', 0);
		pointer('pointermove', to);

		expect(node.style.transform).toBe(`translateX(${sign}${rubberband(100, WIDTH)}px)`);
	});

	it('takes only the main button', () => {
		action();
		pointer('pointerdown', 0, 0, { button: 2 });
		pointer('pointermove', 100);
		expect(node.style.transform).toBe('');
	});

	it('follows only the pointer that started it', () => {
		action();
		pointer('pointerdown', 0);
		pointer('pointermove', 100, 0, { pointerId: 2 });
		expect(node.style.transform).toBe('');
	});

	it('ignores a move with no press', () => {
		action();
		pointer('pointermove', 100);
		expect(node.style.transform).toBe('');
	});

	it('springs home from a slow release short of the threshold', () => {
		action();

		slowDrag(WIDTH * 0.5);
		vi.runAllTimers();

		expect(ondismiss).not.toHaveBeenCalled();
		expect(node.style.transform).toBe('translateX(0px)');
		expect(node.style.transition).toContain('--spring-bounce');
	});

	it('leaves from a slow release past the threshold, finishing the trip first', () => {
		action();

		slowDrag(200);

		expect(node.style.transform).toBe(`translateX(${WIDTH + 24}px)`);
		expect(node.style.opacity).toBe('0');
		expect(ondismiss).not.toHaveBeenCalled();

		vi.advanceTimersByTime(200); // 124px left at the 600px/s floor: 206ms
		expect(ondismiss).not.toHaveBeenCalled();
		vi.advanceTimersByTime(10);
		expect(ondismiss).toHaveBeenCalledOnce();
	});

	it('honours its own threshold', () => {
		action({ threshold: 0.1 });
		slowDrag(40);
		vi.runAllTimers();
		expect(ondismiss).toHaveBeenCalledOnce();
	});

	it('leaves the way it was told, measuring against height on a vertical sheet', () => {
		action({ direction: 'up' });

		pointer('pointerdown', 0, 200);
		pointer('pointermove', 0, 60);
		pointer('pointerup', 0, 60);

		expect(node.style.transform).toBe(`translateY(-${HEIGHT + 24}px)`);
		vi.runAllTimers();
		expect(ondismiss).toHaveBeenCalledOnce();
	});

	it('projects a quick flick past the threshold from a short distance', () => {
		action();

		pointer('pointerdown', 0);
		pointer('pointermove', 20);
		vi.advanceTimersByTime(20);
		pointer('pointermove', 60);
		pointer('pointerup', 60);
		vi.runAllTimers();

		expect(ondismiss).toHaveBeenCalledOnce();
	});

	it('gives the fastest flick the shortest exit, down to its floor', () => {
		action();

		pointer('pointerdown', 0);
		pointer('pointermove', 20);
		vi.advanceTimersByTime(1);
		pointer('pointermove', 200);
		pointer('pointerup', 200);

		expect(node.style.transition).toContain('transform 120ms');
	});

	it('forgets the speed of a drag that stopped before the release', () => {
		action();

		pointer('pointerdown', 0);
		pointer('pointermove', 20);
		vi.advanceTimersByTime(10);
		pointer('pointermove', 60);
		vi.advanceTimersByTime(500);
		pointer('pointermove', 61);
		pointer('pointerup', 61);
		vi.runAllTimers();

		expect(ondismiss).not.toHaveBeenCalled();
	});

	it('stays when flicked back toward home', () => {
		action();

		pointer('pointerdown', 0);
		pointer('pointermove', 200);
		vi.advanceTimersByTime(20);
		pointer('pointermove', 120);
		pointer('pointerup', 120);
		vi.runAllTimers();

		expect(ondismiss).not.toHaveBeenCalled();
		expect(node.style.transform).toBe('translateX(0px)');
	});

	it('treats a cancelled pointer as a release', () => {
		action();

		pointer('pointerdown', 0);
		pointer('pointermove', 40);
		pointer('pointercancel', 40);

		expect(node.style.transform).toBe('translateX(0px)');
	});

	it('swallows the click that ends a drag', () => {
		const clicked = vi.fn();
		inside.addEventListener('click', clicked);
		action();

		slowDrag(40);
		inside.click();

		expect(clicked).not.toHaveBeenCalled();
	});

	it('lets the click after that through', () => {
		const clicked = vi.fn();
		inside.addEventListener('click', clicked);
		action();

		slowDrag(40);
		inside.click();
		inside.click();

		expect(clicked).toHaveBeenCalledOnce();
	});

	it('leaves the click on a control alone when there was no drag', () => {
		const clicked = vi.fn();
		inside.addEventListener('click', clicked);
		action();

		pointer('pointerdown', 0);
		pointer('pointerup', 0);
		inside.click();

		expect(clicked).toHaveBeenCalledOnce();
	});

	// A touch drag ends with no click at all: the next tap, on a link inside a
	// sheet that sprang home, must not pay for it.
	it('lets a fresh tap through after a drag that ended with no click', () => {
		const clicked = vi.fn();
		inside.addEventListener('click', clicked);
		action();

		slowDrag(40);
		pointer('pointerdown', 0);
		pointer('pointerup', 0);
		inside.click();

		expect(clicked).toHaveBeenCalledOnce();
	});

	it('uses the latest params after an update', () => {
		const first = ondismiss;
		const second = vi.fn();
		action()?.update?.({ direction: 'right', ondismiss: second });

		slowDrag(200);
		vi.runAllTimers();

		expect(first).not.toHaveBeenCalled();
		expect(second).toHaveBeenCalledOnce();
	});

	it('stops listening once destroyed', () => {
		action()?.destroy?.();
		slowDrag(200);
		vi.runAllTimers();
		expect(node.style.transform).toBe('');
		expect(ondismiss).not.toHaveBeenCalled();
	});
});
