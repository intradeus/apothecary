import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import Meter from './Meter.svelte';

const bar = () => screen.getByRole('meter', { hidden: true });
const fill = () => bar().firstElementChild as HTMLElement;

describe('Meter', () => {
	it('shows the label, the count and the share of the bar', () => {
		render(Meter, { label: 'Runs', value: 3, max: 12 });
		expect(screen.getByText('Runs')).toBeInTheDocument();
		expect(screen.getByText('3/12')).toBeInTheDocument();
		expect(fill().style.width).toBe('25%');
	});

	it('exposes the numbers to assistive tech', () => {
		render(Meter, { label: 'Runs', value: 3, max: 12 });
		expect(bar()).toHaveAttribute('aria-valuenow', '3');
		expect(bar()).toHaveAttribute('aria-valuemin', '0');
		expect(bar()).toHaveAttribute('aria-valuemax', '12');
	});

	// ARIA requires a name on role=meter.
	it('names the bar after its label', () => {
		render(Meter, { label: 'Runs', value: 3, max: 12 });
		expect(screen.getByRole('meter', { name: /Runs/ })).toBeInTheDocument();
	});

	it('says it in words on hover', () => {
		const { container } = render(Meter, { label: 'Runs', value: 3, max: 12 });
		expect(container.firstElementChild).toHaveAttribute('title', 'Runs: 3 of 12');
	});

	it('stops the bar at full when over the cap', () => {
		render(Meter, { label: 'Runs', value: 15, max: 12 });
		expect(fill().style.width).toBe('100%');
		expect(screen.getByText('15/12')).toBeInTheDocument();
	});

	it('rounds the share', () => {
		render(Meter, { label: 'Runs', value: 1, max: 3 });
		expect(fill().style.width).toBe('33%');
	});

	// A cap of 0 means none (fleet.capacity).
	it('reads a missing cap as no cap: a dash, an empty bar', () => {
		const { container } = render(Meter, { label: 'Runs', value: 3, max: 0 });
		expect(screen.getByText('3/–')).toBeInTheDocument();
		expect(container.firstElementChild).toHaveAttribute('title', 'Runs: 3 of –');
		expect(fill().style.width).toBe('0%');
	});

	it.each(['brand', 'success'] as const)('fills in the %s tone', (tone) => {
		render(Meter, { label: 'Runs', value: 1, max: 2, tone });
		expect(fill()).toHaveClass(tone);
	});

	it('puts the note under the bar', () => {
		render(Meter, { label: 'Runs', value: 1, max: 2, sub: 'at the cap, new runs are refused' });
		expect(screen.getByText('at the cap, new runs are refused')).toBeInTheDocument();
	});
});
