import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import Toggle from './Toggle.svelte';

const sw = (name = 'Auto-merge') => screen.getByRole('switch', { name });

describe('Toggle', () => {
	it('is a switch named by its label, in the state given', async () => {
		const { rerender } = render(Toggle, { checked: false, label: 'Auto-merge' });
		expect(sw()).not.toBeChecked();
		await rerender({ checked: true });
		expect(sw()).toBeChecked();
	});

	it('flips on a click and reports the new state', async () => {
		const onchange = vi.fn();
		render(Toggle, { checked: false, label: 'Auto-merge', onchange });

		await userEvent.click(sw());
		expect(sw()).toBeChecked();
		expect(onchange).toHaveBeenLastCalledWith(true);

		await userEvent.click(sw());
		expect(sw()).not.toBeChecked();
		expect(onchange).toHaveBeenLastCalledWith(false);
	});

	it.each(['{Enter}', ' '])('flips from the keyboard with "%s"', async (key) => {
		const onchange = vi.fn();
		render(Toggle, { checked: false, label: 'Auto-merge', onchange });

		sw().focus();
		await userEvent.keyboard(key);

		expect(onchange).toHaveBeenCalledExactlyOnceWith(true);
	});

	it('stays put when disabled', async () => {
		const onchange = vi.fn();
		render(Toggle, { checked: true, label: 'Auto-merge', disabled: true, onchange });

		await userEvent.click(sw());

		expect(sw()).toBeDisabled();
		expect(sw()).toBeChecked();
		expect(onchange).not.toHaveBeenCalled();
	});

	it('cannot submit a form around it', () => {
		render(Toggle, { checked: false, label: 'Auto-merge' });
		expect(sw()).toHaveAttribute('type', 'button');
	});

	it('explains itself on hover when given a title', () => {
		render(Toggle, { checked: false, label: 'Auto-merge', title: 'After checks pass' });
		expect(sw()).toHaveAttribute('title', 'After checks pass');
	});
});
