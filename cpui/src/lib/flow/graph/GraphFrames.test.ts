import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { Frame } from './frames';
import GraphFrames from './GraphFrames.svelte';

vi.mock('@xyflow/svelte', () => import('$test/fixtures/xyflow.svelte'));

const frame = (over: Partial<Frame> = {}): Frame => ({
	id: 'on-ci',
	hue: 'accent',
	x: 10,
	y: 20,
	w: 300,
	h: 200,
	folded: false,
	...over
});

describe('GraphFrames', () => {
	it('sizes a frame around its chain, behind the nodes', () => {
		render(GraphFrames, { frames: [frame()], onfold: vi.fn() });
		const box = screen.getByTestId('portal-back').firstElementChild as HTMLElement;
		expect(box).toHaveClass('tone-accent');
		expect(box.style.transform).toBe('translate(10px, 20px)');
		expect(box.style.width).toBe('300px');
		expect(box.style.height).toBe('200px');
	});

	it('folds a chain, in front of the nodes', async () => {
		const onfold = vi.fn();
		render(GraphFrames, { frames: [frame()], onfold });
		const btn = within(screen.getByTestId('portal-front')).getByRole('button', {
			name: 'Fold on-ci to its marker'
		});
		expect(btn).toHaveAttribute('aria-expanded', 'true');
		await userEvent.click(btn);
		expect(onfold).toHaveBeenCalledExactlyOnceWith('on-ci');
	});

	it('unfolds one folded', () => {
		render(GraphFrames, { frames: [frame({ folded: true })], onfold: vi.fn() });
		const btn = screen.getByRole('button', { name: 'Unfold on-ci' });
		expect(btn).toHaveAttribute('aria-expanded', 'false');
	});

	it('gives each chain its own', () => {
		render(GraphFrames, { frames: [frame(), frame({ id: 'merged' })], onfold: vi.fn() });
		expect(screen.getAllByRole('button')).toHaveLength(2);
	});
});
