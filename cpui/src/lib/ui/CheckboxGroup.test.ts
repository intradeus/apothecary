import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { bound } from '$test/fixtures/designsystem.svelte';
import CheckboxGroup from './CheckboxGroup.svelte';

const options = [
	{ value: 'a', label: 'Alpha' },
	{ value: 'b', label: 'Beta', detail: 'second' },
	{ value: 'c', label: 'Gamma' }
];
const box = (name: string | RegExp) => screen.getByRole('checkbox', { name });

describe('CheckboxGroup', () => {
	it('is a named group with a checkbox per option', () => {
		render(CheckboxGroup, { options, value: [], label: 'Threads' });
		const group = screen.getByRole('group', { name: 'Threads' });
		expect(group).toContainElement(box('Alpha'));
		expect(screen.getAllByRole('checkbox')).toHaveLength(3);
	});

	it('shows an option’s detail', () => {
		render(CheckboxGroup, { options, value: [] });
		expect(screen.getByText(/second/)).toBeInTheDocument();
	});

	it('checks what the value holds', () => {
		render(CheckboxGroup, { options, value: ['b'] });
		expect(box('Alpha')).not.toBeChecked();
		expect(box(/Beta/)).toBeChecked();
	});

	it('keeps the picked values in the options’ order, not the clicks’', async () => {
		const onchange = vi.fn();
		const b = bound('value', [] as string[], { options, onchange });
		render(CheckboxGroup, b.props);

		await userEvent.click(box('Gamma'));
		await userEvent.click(box('Alpha'));

		expect(b.current()).toEqual(['a', 'c']);
		expect(onchange).toHaveBeenLastCalledWith(['a', 'c']);
	});

	it('drops a value when it is unticked, down to none', async () => {
		const onchange = vi.fn();
		const b = bound('value', ['a'], { options, onchange });
		render(CheckboxGroup, b.props);

		await userEvent.click(box('Alpha'));

		expect(b.current()).toEqual([]);
		expect(onchange).toHaveBeenCalledExactlyOnceWith([]);
		expect(box('Alpha')).not.toBeChecked();
	});

	it('disables every box when the group is disabled', () => {
		render(CheckboxGroup, { options, value: ['a'], disabled: true });
		for (const b of screen.getAllByRole('checkbox')) expect(b).toBeDisabled();
	});

	it('disables one option and leaves the others working', async () => {
		const onchange = vi.fn();
		const some = [options[0], { ...options[2], disabled: true }];
		render(CheckboxGroup, { options: some, value: [], onchange });

		await userEvent.click(screen.getByText('Gamma'));
		expect(onchange).not.toHaveBeenCalled();

		await userEvent.click(box('Alpha'));
		expect(onchange).toHaveBeenCalledExactlyOnceWith(['a']);
	});
});
