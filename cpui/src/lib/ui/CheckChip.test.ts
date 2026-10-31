import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { I } from '$lib/design/icons';
import CheckChip, { type CheckTone } from './CheckChip.svelte';

const chip = () => screen.getByText('401').parentElement!;

describe('CheckChip', () => {
	it.each<[CheckTone, string, string]>([
		['ok', 'tint-success', I.ok],
		['bad', 'tint-danger', I.bad],
		['warn', 'tint-warning', I.warn]
	])('reads %s through its hue and glyph', (tone, tint, glyph) => {
		render(CheckChip, { tone, label: '401' });
		expect(chip()).toHaveClass(tint);
		expect(chip()).toHaveTextContent(glyph);
	});

	it('pulses a dot, not a glyph, while the check runs', () => {
		render(CheckChip, { tone: 'live', label: '401' });
		expect(chip()).toHaveClass('tint-running');
		expect(chip().querySelector('.dot.pulse')).toBeInTheDocument();
		expect(chip()).toHaveTextContent(/^401$/);
	});

	it('shows neither for a check that never ran', () => {
		render(CheckChip, { tone: 'none', label: '401' });
		expect(chip()).toHaveClass('tint-muted');
		expect(chip().querySelector('.dot')).not.toBeInTheDocument();
		expect(chip()).toHaveTextContent(/^401$/);
	});

	it('trails when it ran and carries its title', () => {
		render(CheckChip, { tone: 'ok', label: '401', when: 'tested 3h ago', title: 'HTTP status' });
		expect(screen.getByText('· tested 3h ago')).toBeInTheDocument();
		expect(chip()).toHaveAttribute('title', 'HTTP status');
	});

	it('has a small size', () => {
		render(CheckChip, { tone: 'ok', label: '401', size: 'sm' });
		expect(chip()).toHaveClass('sm');
	});

	it('replaces the old result with the new one', async () => {
		const { rerender } = render(CheckChip, { tone: 'live', label: 'testing' });

		await rerender({ tone: 'ok', label: 'passed' });

		expect(screen.getByText('passed')).toBeInTheDocument();
		expect(screen.queryByText('testing')).not.toBeInTheDocument();
	});

	it('materialises afresh for a new label under the same tone', async () => {
		const { rerender } = render(CheckChip, { tone: 'bad', label: '401' });
		const before = chip();

		await rerender({ label: '403' });

		expect(screen.getByText('403').parentElement).not.toBe(before);
	});

	// "tested 3h ago" ticks over; that is not a new result.
	it('stays put when only the trailing note changes', async () => {
		const { rerender } = render(CheckChip, { tone: 'ok', label: '401', when: 'tested 1m ago' });
		const before = chip();

		await rerender({ when: 'tested 2m ago' });

		expect(chip()).toBe(before);
		expect(screen.getByText('· tested 2m ago')).toBeInTheDocument();
	});
});
