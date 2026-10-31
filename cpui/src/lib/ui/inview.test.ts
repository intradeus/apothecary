import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { rect } from '$test/helpers';
import { keepInView } from './inview';

const VIEW = 300;

/** A row 300 wide, scrolled to `scrollLeft`, holding 100-wide children. */
function aRow(opts: { content?: number; scrollLeft?: number } = {}) {
	const row = document.body.appendChild(document.createElement('div'));
	Object.defineProperty(row, 'clientWidth', { value: VIEW });
	Object.defineProperty(row, 'scrollWidth', { value: opts.content ?? 1000 });
	row.scrollLeft = opts.scrollLeft ?? 0;
	row.scrollTo = vi.fn() as unknown as typeof row.scrollTo;
	vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(rect(50, 0, VIEW, 40));
	return row;
}

/** A child at `offset` inside the row's scrolled content. */
function aChild(row: HTMLElement, offset: number, cls = '') {
	const child = row.appendChild(document.createElement('a'));
	child.className = cls;
	Object.defineProperty(child, 'offsetWidth', { value: 100 });
	vi.spyOn(child, 'getBoundingClientRect').mockImplementation(() =>
		rect(50 + offset - row.scrollLeft, 0, 100, 40)
	);
	return child;
}
const settled = () => new Promise((r) => queueMicrotask(() => r(undefined)));

describe('keepInView', () => {
	let row: HTMLElement;
	let frame: FrameRequestCallback | undefined;

	beforeEach(() => {
		frame = undefined;
		vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
			frame = cb;
			return 1;
		});
	});
	afterEach(() => {
		row.remove();
	});

	const place = () => frame?.(0);
	const scrolled = () => vi.mocked(row.scrollTo);

	it('waits for the first frame, when the row has its size', () => {
		row = aRow();
		aChild(row, 700, 'on');
		keepInView(row, undefined);
		expect(scrolled()).not.toHaveBeenCalled();
		place();
		expect(scrolled()).toHaveBeenCalledOnce();
	});

	it('brings a child past the right edge in, with room to spare, without animating', () => {
		row = aRow();
		aChild(row, 700, 'on');
		keepInView(row, undefined);
		place();
		expect(scrolled()).toHaveBeenCalledExactlyOnceWith({ left: 524, behavior: 'auto' });
	});

	it('brings a child past the left edge in', () => {
		row = aRow({ scrollLeft: 500 });
		aChild(row, 200, 'on');
		keepInView(row, undefined);
		place();
		expect(scrolled()).toHaveBeenCalledExactlyOnceWith({ left: 176, behavior: 'auto' });
	});

	it('leaves a child that is already in view', () => {
		row = aRow({ scrollLeft: 100 });
		aChild(row, 200, 'on');
		keepInView(row, undefined);
		place();
		expect(scrolled()).not.toHaveBeenCalled();
	});

	it('counts a child inside the padding as out of view', () => {
		row = aRow();
		aChild(row, 180, 'on'); // right edge 280, 20 short of the view: inside the 24 of room
		keepInView(row, undefined);
		place();
		expect(scrolled()).toHaveBeenCalledExactlyOnceWith({ left: 4, behavior: 'auto' });
	});

	it('does nothing when the row fits', () => {
		row = aRow({ content: VIEW });
		aChild(row, 700, 'on');
		keepInView(row, undefined);
		place();
		expect(scrolled()).not.toHaveBeenCalled();
	});

	it('does nothing with no selected child', () => {
		row = aRow();
		aChild(row, 700);
		keepInView(row, undefined);
		place();
		expect(scrolled()).not.toHaveBeenCalled();
	});

	it('looks only at direct children', () => {
		row = aRow();
		const wrap = row.appendChild(document.createElement('div'));
		wrap.className = 'wrap';
		wrap.appendChild(document.createElement('a')).className = 'on';
		keepInView(row, undefined);
		place();
		expect(scrolled()).not.toHaveBeenCalled();
	});

	it('scrolls smoothly when the selection moves after that', async () => {
		row = aRow();
		const first = aChild(row, 700, 'on');
		const second = aChild(row, 800);
		keepInView(row, undefined);
		place();

		first.classList.remove('on');
		second.classList.add('on');
		await settled();

		expect(scrolled()).toHaveBeenLastCalledWith({ left: 624, behavior: 'smooth' });
	});

	it('does not spend the instant first scroll while there was nothing to scroll', async () => {
		row = aRow();
		const later = aChild(row, 700);
		keepInView(row, undefined);
		place();

		later.classList.add('on');
		await settled();

		expect(scrolled()).toHaveBeenCalledExactlyOnceWith({ left: 524, behavior: 'auto' });
	});

	it('takes a custom selector, and a new one on update', () => {
		row = aRow();
		aChild(row, 700, 'here');
		aChild(row, 800, 'there');
		const action = keepInView(row, { selector: '.here' });
		place();
		expect(scrolled()).toHaveBeenLastCalledWith({ left: 524, behavior: 'auto' });

		action?.update?.({ selector: '.there' });
		expect(scrolled()).toHaveBeenLastCalledWith({ left: 624, behavior: 'smooth' });
	});

	it('goes back to .on when updated without a selector', () => {
		row = aRow();
		aChild(row, 700, 'on');
		const action = keepInView(row, { selector: '.none' });
		place();
		expect(scrolled()).not.toHaveBeenCalled();

		action?.update?.(undefined);
		expect(scrolled()).toHaveBeenCalledOnce();
	});

	it('stops watching once destroyed', async () => {
		row = aRow();
		const later = aChild(row, 700);
		const action = keepInView(row, undefined);
		action?.destroy?.();

		later.classList.add('on');
		await settled();

		expect(scrolled()).not.toHaveBeenCalled();
	});
});
