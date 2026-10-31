import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import PingPill, { pingMeta } from './PingPill.svelte';

const WANTED = 'Asked on purpose by the flow';
const RAISED = 'Raised by the agent — counts against the run';

describe('PingPill', () => {
	it.each([
		['gate', 'Gate', 'tint-success', WANTED],
		['debug', 'Debug', 'tint-attention', WANTED],
		['phase_confirm', 'Start', 'tint-success', WANTED],
		['select', 'Select', 'tint-accent-2', WANTED],
		['clarification', 'Clarification', 'tint-attention', RAISED],
		['blocker', 'Blocker', 'tint-danger', RAISED],
		['permission', 'Permission', 'tint-accent', RAISED]
	])('shows a %s ping as %s', (type, label, tint, title) => {
		render(PingPill, { type });
		const pill = screen.getByText(label);
		expect(pill).toHaveClass(tint);
		expect(pill).toHaveAttribute('title', title);
	});

	it('shows a type it does not know as its raw name, muted', () => {
		render(PingPill, { type: 'novel' });
		const pill = screen.getByText('novel');
		expect(pill).toHaveClass('tint-muted');
		expect(pill).toHaveAttribute('title', RAISED);
	});

	it('follows the type when it changes', async () => {
		const { rerender } = render(PingPill, { type: 'gate' });
		await rerender({ type: 'blocker' });
		expect(screen.getByText('Blocker')).toBeInTheDocument();
		expect(screen.queryByText('Gate')).not.toBeInTheDocument();
	});
});

describe('pingMeta', () => {
	it('falls back for an unknown type', () => {
		expect(pingMeta('novel')).toEqual({ label: 'novel', hue: 'muted', wanted: false });
	});
});
