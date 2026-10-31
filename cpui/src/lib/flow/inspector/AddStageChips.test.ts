import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import AddStageChips from './AddStageChips.svelte';

it('adds the kind whose chip is clicked', async () => {
	const onadd = vi.fn();
	render(AddStageChips, { kinds: ['agent', 'wait'], onadd });
	await userEvent.click(screen.getByRole('button', { name: 'wait' }));
	expect(onadd).toHaveBeenCalledExactlyOnceWith('wait');
});

it('calls a human gate what the rest of the editor calls it', () => {
	render(AddStageChips, { kinds: ['human_gate'], onadd: vi.fn() });
	expect(screen.getByRole('button', { name: 'gate' })).toBeInTheDocument();
});

it('offers only the kinds it is given', () => {
	render(AddStageChips, { kinds: ['command'], onadd: vi.fn() });
	expect(screen.getAllByRole('button')).toHaveLength(1);
});
