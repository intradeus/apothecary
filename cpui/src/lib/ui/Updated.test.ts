import { act, render, screen } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import Updated from './Updated.svelte';

const NOW = 1_000_000;
// The reserved wordings are hidden; what's left is the one being shown.
const shown = (root: HTMLElement) => root.querySelector('.st > :not([aria-hidden])')?.textContent;

describe('Updated', () => {
	beforeEach(() => {
		vi.useFakeTimers({ now: NOW });
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	it('says connecting until something has landed', () => {
		const { container } = render(Updated, { at: null });
		expect(shown(container)).toBe('Connecting…');
	});

	it('says paused, even with a landed time', () => {
		const { container } = render(Updated, { at: NOW, paused: true });
		expect(shown(container)).toBe('paused');
	});

	it('says just now under two seconds', () => {
		const { container } = render(Updated, { at: NOW - 1_400 });
		expect(shown(container)).toBe('updated just now');
	});

	it('counts seconds after that', () => {
		const { container } = render(Updated, { at: NOW - 7_000 });
		expect(shown(container)).toBe('updated 7s ago');
	});

	it('does not count backwards for a time ahead of this clock', () => {
		const { container } = render(Updated, { at: NOW + 30_000 });
		expect(shown(container)).toBe('updated just now');
	});

	it('ticks along on its own', async () => {
		const { container } = render(Updated, { at: NOW });
		await act(() => vi.advanceTimersByTime(5_000));
		expect(shown(container)).toBe('updated 5s ago');
	});

	it('goes back to just now when the next poll lands', async () => {
		const { container, rerender } = render(Updated, { at: NOW });
		await act(() => vi.advanceTimersByTime(5_000));
		await rerender({ at: NOW + 5_000 });
		expect(shown(container)).toBe('updated just now');
	});

	it('reserves every wording, so the caption keeps its width', () => {
		const { container } = render(Updated, { at: NOW });
		const reserved = [...container.querySelectorAll('[aria-hidden="true"]')];
		expect(reserved.map((n) => n.textContent)).toEqual([
			'updated just now',
			'updated 88s ago',
			'paused',
			'Connecting…'
		]);
	});

	it('gives the landed time on hover, and nothing before one', async () => {
		const { container, rerender } = render(Updated, { at: null });
		const caption = container.querySelector('.up')!;
		expect(caption).toHaveAttribute('title', '');

		await rerender({ at: NOW });
		expect(caption.getAttribute('title')).toMatch(/\d{2}:\d{2}:\d{2}/);
	});

	it('stops ticking once gone', () => {
		const { unmount } = render(Updated, { at: NOW });
		expect(vi.getTimerCount()).toBe(1);
		unmount();
		expect(vi.getTimerCount()).toBe(0);
	});

	it('pulses while connecting, and marks paused', async () => {
		const { container, rerender } = render(Updated, { at: null });
		expect(container.querySelector('.d')).toHaveClass('pulse');
		await rerender({ paused: true });
		expect(container.querySelector('.d')).toHaveClass('paused');
		expect(screen.queryByText('Connecting…', { ignore: '[aria-hidden]' })).toBeNull();
	});
});
