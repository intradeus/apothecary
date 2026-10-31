import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import Skeleton from './Skeleton.svelte';

const bars = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('.skel')];

describe('Skeleton', () => {
	it('announces itself as loading', () => {
		render(Skeleton);
		expect(screen.getByLabelText('Loading')).toHaveAttribute('aria-busy', 'true');
	});

	it('draws three bars by default, the last one short', () => {
		const { container } = render(Skeleton);
		expect(bars(container).map((b) => b.style.width)).toEqual(['100%', '93%', '60%']);
	});

	it('draws as many bars as asked, at the height asked', () => {
		const { container } = render(Skeleton, { lines: 5, height: 20 });
		expect(bars(container)).toHaveLength(5);
		expect(bars(container)[0].style.height).toBe('20px');
	});

	it('treats a single line as the short last line', () => {
		const { container } = render(Skeleton, { lines: 1 });
		expect(bars(container).map((b) => b.style.width)).toEqual(['60%']);
	});

	it('sits in the standard pane only when asked', () => {
		const flat = render(Skeleton);
		expect(flat.container.querySelector('.pane')).toBeNull();
		flat.unmount();

		const { container } = render(Skeleton, { pane: true });
		expect(container.querySelector('.pane-body')).toContainElement(
			screen.getByLabelText('Loading')
		);
	});
});
