import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { RunState } from '$lib/domain';
import StatusChip from './StatusChip.svelte';

describe('StatusChip', () => {
	it.each<[RunState, string, string]>([
		['pending', 'Pending', 'tint-muted'],
		['provisioning', 'Provisioning', 'tint-running'],
		['running', 'Running', 'tint-running'],
		['waiting_on_human', 'Waiting on you', 'tint-hot'],
		['in_review', 'In review', 'tint-accent'],
		['paused', 'Paused', 'tint-muted'],
		['completed', 'Completed', 'tint-success'],
		['abandoned', 'Abandoned', 'tint-warning'],
		['failed', 'Failed', 'tint-danger'],
		['archived', 'Archived', 'archived']
	])('labels %s as "%s" with its tone', (state, label, tone) => {
		render(StatusChip, { state });
		expect(screen.getByText(label)).toHaveClass(tone);
	});

	it.each<[RunState, string]>([
		['completed', '✓'],
		['failed', '✕'],
		['paused', '❚❚']
	])('marks %s with a glyph instead of a dot', (state, glyph) => {
		const { container } = render(StatusChip, { state });
		expect(screen.getByText(glyph)).toBeInTheDocument();
		expect(container.querySelector('.live-dot')).toBeNull();
	});

	it('draws neither glyph nor dot on an archived run', () => {
		const { container } = render(StatusChip, { state: 'archived' });
		expect(container.querySelector('.g, .live-dot')).toBeNull();
	});

	it('lets only the running dot glow; waiting pulses flat', () => {
		const running = render(StatusChip, { state: 'running' });
		const glow = running.container.querySelector('.live-dot');
		expect(glow).toHaveClass('pulse');
		expect(glow).not.toHaveClass('flat');
		running.unmount();

		const { container } = render(StatusChip, { state: 'waiting_on_human' });
		const ink = container.querySelector('.live-dot');
		expect(ink).toHaveClass('pulse');
		expect(ink).toHaveClass('flat');
	});

	it('keeps the lore word off unless asked', () => {
		render(StatusChip, { state: 'running' });
		expect(screen.queryByText(/brewing/)).toBeNull();
	});

	it.each<[RunState, string]>([
		['running', '· brewing'],
		['failed', '· batch spoiled']
	])('adds the lore word to %s when asked', (state, aside) => {
		render(StatusChip, { state, suffix: true });
		expect(screen.getByText(aside)).toBeInTheDocument();
	});

	it('has no lore word for the other states', () => {
		const { container } = render(StatusChip, { state: 'paused', suffix: true });
		expect(container.querySelector('.aside')).toBeNull();
	});

	it('shrinks to a dot named for the state when compact', () => {
		const { container } = render(StatusChip, { state: 'waiting_on_human', compact: true });
		const dot = screen.getByRole('img', { name: 'Waiting on you' });
		expect(dot).toHaveAttribute('title', 'Waiting on you');
		expect(dot).toHaveClass('hue-attention');
		expect(dot).toHaveClass('pulse');
		expect(container.querySelector('.pill')).toBeNull();
	});

	it('keeps a compact dot still and flat once nothing is happening', () => {
		render(StatusChip, { state: 'paused', compact: true });
		const dot = screen.getByRole('img', { name: 'Paused' });
		expect(dot).toHaveClass('flat');
		expect(dot).not.toHaveClass('pulse');
	});
});
