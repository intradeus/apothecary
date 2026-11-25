import { afterEach, describe, expect, it, vi } from 'vitest';
import { scrollEdges } from './scrollEdges';

/** A row of `content` px in a box of `box` px, scrolled to `left`. */
function row(content: number, box: number) {
	const node = document.body.appendChild(document.createElement('div'));
	Object.defineProperty(node, 'scrollWidth', { value: content, configurable: true });
	Object.defineProperty(node, 'clientWidth', { value: box, configurable: true });
	return node;
}
const marks = (n: HTMLElement) => [
	n.hasAttribute('data-more-start'),
	n.hasAttribute('data-more-end')
];

afterEach(() => {
	document.body.innerHTML = '';
});

describe('scrollEdges', () => {
	it('marks nothing on a row that fits', () => {
		const n = row(300, 300);
		scrollEdges(n);
		expect(marks(n)).toEqual([false, false]);
	});

	it('marks the end with more past it, following the scroll', () => {
		const n = row(600, 300);
		const action = scrollEdges(n);
		expect(marks(n)).toEqual([false, true]);
		n.scrollLeft = 150;
		n.dispatchEvent(new Event('scroll'));
		expect(marks(n)).toEqual([true, true]);
		n.scrollLeft = 300;
		n.dispatchEvent(new Event('scroll'));
		expect(marks(n)).toEqual([true, false]);
		action?.destroy?.();
		n.scrollLeft = 0;
		n.dispatchEvent(new Event('scroll'));
		expect(marks(n)).toEqual([true, false]);
	});

	it('looks again when its children change', async () => {
		const n = row(300, 300);
		scrollEdges(n);
		Object.defineProperty(n, 'scrollWidth', { value: 500, configurable: true });
		n.appendChild(document.createElement('span'));
		await vi.waitFor(() => expect(marks(n)).toEqual([false, true]));
	});
});
