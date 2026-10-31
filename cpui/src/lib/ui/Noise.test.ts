import { render } from '@testing-library/svelte';
import { flushSync, tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import Noise from './Noise.svelte';
import { NEUTRAL_MIX, noise } from './noise.svelte';

// jsdom has no WebGL and no layout, and tokens.css isn't loaded: the context,
// the frame clock, the resize observer and the tokens are the seams.
const tokens = vi.hoisted(() => new Map<string, string>());
vi.mock('$lib/design/tokens', () => ({ token: (name: string) => tokens.get(name) ?? '' }));

function fakeGl({ links = true } = {}) {
	return {
		VERTEX_SHADER: 1,
		FRAGMENT_SHADER: 2,
		LINK_STATUS: 3,
		TRIANGLES: 4,
		createShader: vi.fn(() => ({})),
		shaderSource: vi.fn(),
		compileShader: vi.fn(),
		createProgram: vi.fn(() => ({})),
		attachShader: vi.fn(),
		linkProgram: vi.fn(),
		getProgramParameter: vi.fn(() => links),
		useProgram: vi.fn(),
		bindVertexArray: vi.fn(),
		createVertexArray: vi.fn(() => ({})),
		viewport: vi.fn(),
		getUniformLocation: vi.fn((_: unknown, name: string) => name),
		uniform1f: vi.fn(),
		uniform1i: vi.fn(),
		uniform3fv: vi.fn(),
		drawArrays: vi.fn()
	};
}

type Gl = ReturnType<typeof fakeGl>;

let frames: (FrameRequestCallback | null)[];
let resized: ResizeObserverCallback;
let observe: (target: Element, options?: ResizeObserverOptions) => void;
let clock: number;
let gl: Gl;

/** The frame the loop is waiting on, as the browser would run it. */
function frame() {
	const pending = frames.filter(Boolean);
	expect(pending).toHaveLength(1);
	frames = [];
	pending[0]!(clock);
}
/** Time passes: the loop's timer fires, then its frame. */
function advance(ms: number) {
	clock += ms;
	vi.advanceTimersByTime(ms);
	if (frames.some(Boolean)) frame();
}
function start(glContext: Gl | null = fakeGl()) {
	if (glContext) gl = glContext;
	vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(
		glContext as unknown as RenderingContext
	);
	const view = render(Noise);
	const canvas = view.container.querySelector('canvas') as HTMLCanvasElement;
	return { ...view, canvas };
}
const lastUniform = (fn: { mock: { calls: unknown[][] } }, name: string) =>
	fn.mock.calls.filter((c) => c[0] === name).at(-1)?.[1];
const shift = () => lastUniform(gl.uniform1f, 'shift');

describe('Noise', () => {
	beforeEach(() => {
		vi.useFakeTimers();
		clock = performance.now();
		frames = [];
		vi.spyOn(globalThis, 'requestAnimationFrame').mockImplementation((cb) => frames.push(cb));
		vi.spyOn(globalThis, 'cancelAnimationFrame').mockImplementation((id) => {
			frames[id - 1] = null;
		});
		observe = vi.fn();
		vi.stubGlobal(
			'ResizeObserver',
			class {
				constructor(cb: ResizeObserverCallback) {
					resized = cb;
				}
				observe(target: Element, options?: ResizeObserverOptions) {
					observe(target, options);
				}
				disconnect() {}
			}
		);
		tokens.clear();
		tokens.set('--noise-ember', '#ff0000');
		tokens.set('--noise-sky', '#0000ff');
		tokens.set('--frame', '#000000');
		tokens.set('--noise-grain', '1');
		tokens.set('--noise-blend', 'screen');
	});

	afterEach(() => {
		vi.useRealTimers();
		noise.blue = NEUTRAL_MIX;
		noise.speed = 1;
		document.documentElement.removeAttribute('data-theme');
	});

	it('hides from assistive tech', () => {
		const { container } = start(null);
		expect(container.firstElementChild).toHaveAttribute('aria-hidden', 'true');
	});

	it('stays hidden without WebGL2 and asks for no frames', () => {
		const { canvas } = start(null);
		expect(canvas).not.toHaveClass('live');
		expect(requestAnimationFrame).not.toHaveBeenCalled();
	});

	it('stays hidden when the shader will not link', () => {
		const { canvas } = start(fakeGl({ links: false }));
		expect(canvas).not.toHaveClass('live');
		expect(requestAnimationFrame).not.toHaveBeenCalled();
	});

	it('shows once the first frame is painted', () => {
		const { canvas } = start();
		expect(canvas).not.toHaveClass('live');

		frame();
		flushSync();

		expect(gl.drawArrays).toHaveBeenCalledOnce();
		expect(canvas).toHaveClass('live');
	});

	it('rests at 10 frames a second', () => {
		start();
		frame();
		vi.advanceTimersByTime(99);
		expect(frames).toHaveLength(0);
		vi.advanceTimersByTime(1);
		expect(frames).toHaveLength(1);
	});

	it('runs at 20 while the circles hurry', () => {
		start();
		noise.speed = 3;
		frame();
		vi.advanceTimersByTime(49);
		expect(frames).toHaveLength(0);
		vi.advanceTimersByTime(1);
		expect(frames).toHaveLength(1);
	});

	it('moves the circles from frame to frame', () => {
		start();
		frame();
		const before = lastUniform(gl.uniform3fv, 'blob');
		advance(100);
		expect(gl.drawArrays).toHaveBeenCalledTimes(2);
		expect(lastUniform(gl.uniform3fv, 'blob')).not.toEqual(before);
	});

	it('stops drawing at speed 0, and keeps ticking to notice a change', () => {
		noise.speed = 0;
		start();
		frame();
		advance(100);
		advance(100);
		expect(gl.drawArrays).toHaveBeenCalledOnce();

		noise.speed = 1;
		advance(100);
		expect(gl.drawArrays).toHaveBeenCalledTimes(2);
	});

	it('draws only a colour change under reduced motion', () => {
		vi.spyOn(globalThis, 'matchMedia').mockReturnValue({ matches: true } as MediaQueryList);
		start();
		frame();
		advance(100);
		expect(gl.drawArrays).toHaveBeenCalledOnce();

		noise.blue = 100;
		advance(100);
		expect(gl.drawArrays).toHaveBeenCalledTimes(2);
	});

	it('turns the colours over three seconds and settles', () => {
		noise.speed = 0;
		start();
		frame();
		expect(shift()).toBe(0);

		noise.blue = 100;
		advance(100); // notices it: the turn starts here
		expect(shift()).toBe(0);
		advance(1500);
		expect(shift()).toBeCloseTo(0.5);
		advance(1500);
		expect(shift()).toBe(1);

		const drawn = gl.drawArrays.mock.calls.length;
		advance(100);
		expect(gl.drawArrays).toHaveBeenCalledTimes(drawn);
	});

	it('hurries the clock while a turn is under way', () => {
		noise.speed = 0;
		start();
		noise.blue = 100;
		frame();
		vi.advanceTimersByTime(50);
		expect(frames).toHaveLength(1);
	});

	it('colours itself from the theme’s tokens', () => {
		start();
		expect(lastUniform(gl.uniform3fv, 'hue')).toEqual([1, 0, 0, 0, 0, 1]);
		expect(lastUniform(gl.uniform1i, 'light')).toBe(0);
		expect(lastUniform(gl.uniform1f, 'grain')).toBeCloseTo(0.04);
	});

	it('darkens its grain on a light theme', () => {
		tokens.set('--noise-blend', 'multiply');
		start();
		expect(lastUniform(gl.uniform1i, 'light')).toBe(1);
		expect(lastUniform(gl.uniform1f, 'grain')).toBeCloseTo(-0.04);
	});

	it('repaints in the new colours when the theme changes, even at rest', async () => {
		noise.speed = 0;
		start();
		frame();

		tokens.set('--noise-sky', '#00ff00');
		document.documentElement.setAttribute('data-theme', 'dawn');
		await vi.advanceTimersByTimeAsync(0);
		advance(100);

		expect(lastUniform(gl.uniform3fv, 'hue')).toEqual([1, 0, 0, 0, 1, 0]);
		expect(gl.drawArrays).toHaveBeenCalledTimes(2);
	});

	it('sizes its buffer to the device pixels it lies on', () => {
		const { canvas } = start();
		const entry = {
			devicePixelContentBoxSize: [{ inlineSize: 1999, blockSize: 1001 }],
			contentRect: { width: 1000, height: 500 }
		};

		resized([entry] as unknown as ResizeObserverEntry[], {} as ResizeObserver);

		expect(canvas.width).toBe(1999);
		expect(canvas.height).toBe(1001);
		expect(gl.viewport).toHaveBeenLastCalledWith(0, 0, 1999, 1001);
	});

	it('falls back to the content rect where that box is missing', () => {
		const { canvas } = start();
		const entry = { contentRect: { width: 640, height: 360 } };

		resized([entry] as unknown as ResizeObserverEntry[], {} as ResizeObserver);

		expect([canvas.width, canvas.height]).toEqual([640, 360]);
	});

	it('paints at once after a resize, which clears the buffer', () => {
		start();
		const entry = { contentRect: { width: 640, height: 360 } };

		resized([entry] as unknown as ResizeObserverEntry[], {} as ResizeObserver);
		expect(gl.drawArrays).toHaveBeenCalledOnce();

		resized([entry] as unknown as ResizeObserverEntry[], {} as ResizeObserver);
		expect(gl.drawArrays).toHaveBeenCalledOnce();
	});

	// Safari has no device-pixel-content-box.
	it('observes plainly where the browser refuses the box', () => {
		observe = vi.fn((_: Element, options?: ResizeObserverOptions) => {
			if (options) throw new Error('unsupported box');
		});
		const { canvas } = start();
		expect(observe).toHaveBeenLastCalledWith(canvas, undefined);
	});

	it('stops when the context is lost and hides itself', async () => {
		const { canvas } = start();
		frame();
		flushSync();

		const lost = new Event('webglcontextlost', { cancelable: true });
		canvas.dispatchEvent(lost);
		await tick();

		expect(lost.defaultPrevented).toBe(true);
		expect(canvas).not.toHaveClass('live');
		expect(frames.filter(Boolean)).toHaveLength(0);
		vi.advanceTimersByTime(1000);
		expect(frames.filter(Boolean)).toHaveLength(0);
	});

	it('comes back when the context is restored', () => {
		const { canvas } = start();
		frame();
		canvas.dispatchEvent(new Event('webglcontextlost', { cancelable: true }));
		flushSync();

		canvas.dispatchEvent(new Event('webglcontextrestored'));
		frame();
		flushSync();

		expect(gl.linkProgram).toHaveBeenCalledTimes(2);
		expect(canvas).toHaveClass('live');
	});

	it('stops the loop when it leaves', () => {
		const { unmount } = start();
		frame();
		unmount();

		vi.advanceTimersByTime(1000);

		expect(frames.filter(Boolean)).toHaveLength(0);
	});
});
