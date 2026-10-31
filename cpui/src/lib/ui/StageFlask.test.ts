import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import StageFlask, { type StageStatus } from './StageFlask.svelte';

const liquid = () => screen.getByTitle(/./).querySelector<HTMLElement>('.liquid')!;

describe('StageFlask', () => {
	it.each<[StageStatus, string, string]>([
		['done', '100%', 'var(--success)'],
		['running', '55%', 'var(--running)'],
		['waiting', '70%', 'var(--attention)'],
		['failed', '100%', 'var(--danger)'],
		['paused', '50%', 'var(--text-muted)'],
		['pending', '0%', 'var(--line-strong)']
	])('fills a %s stage to %s', (status, level, tone) => {
		render(StageFlask, { status });
		expect(screen.getByTitle(status).style.getPropertyValue('--tone')).toBe(tone);
		expect(liquid().style.height).toBe(level);
	});

	it('bubbles and sloshes only while running', async () => {
		const { container, rerender } = render(StageFlask, { status: 'running' });
		expect(container.querySelector('.bubble')).not.toBeNull();
		expect(liquid()).toHaveClass('slosh');

		await rerender({ status: 'paused' });
		expect(container.querySelector('.bubble')).toBeNull();
		expect(liquid()).not.toHaveClass('slosh');
	});

	it('pulses while waiting on someone', () => {
		render(StageFlask, { status: 'waiting' });
		expect(screen.getByTitle('waiting')).toHaveClass('pulse');
	});
});
