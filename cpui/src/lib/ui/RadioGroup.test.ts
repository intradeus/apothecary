import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import RadioGroup from './RadioGroup.svelte';

const options = [
	{ value: 'pg', label: 'Postgres', detail: 'managed' },
	{ value: 'my', label: 'MySQL' },
	{ value: 'ms', label: 'SQL Server', disabled: true }
];
const radio = (name: string | RegExp) => screen.getByRole('radio', { name });

describe('RadioGroup', () => {
	it('is a named group of its options with the value on', () => {
		render(RadioGroup, { options, value: 'my', label: 'Database' });
		const group = screen.getByRole('radiogroup', { name: 'Database' });
		expect(within(group).getAllByRole('radio')).toHaveLength(3);
		expect(radio('MySQL')).toBeChecked();
		expect(radio(/Postgres/)).not.toBeChecked();
	});

	it('moves the choice: exactly one is on after a click', async () => {
		const onchange = vi.fn();
		render(RadioGroup, { options, value: 'pg', onchange });

		await userEvent.click(radio('MySQL'));

		expect(onchange).toHaveBeenCalledExactlyOnceWith('my');
		expect(radio('MySQL')).toBeChecked();
		expect(radio(/Postgres/)).not.toBeChecked();
	});

	it('starts with nothing on when the value is unset', () => {
		render(RadioGroup, { options, value: undefined });
		for (const r of screen.getAllByRole('radio')) expect(r).not.toBeChecked();
	});

	it('disables one option on its own', () => {
		render(RadioGroup, { options, value: 'pg' });
		expect(radio('SQL Server')).toBeDisabled();
		expect(radio('MySQL')).toBeEnabled();
	});

	it('disables them all at once', () => {
		render(RadioGroup, { options, value: 'pg', disabled: true });
		for (const r of screen.getAllByRole('radio')) expect(r).toBeDisabled();
	});

	it('shares one name, so the browser treats them as a set', () => {
		render(RadioGroup, { options, value: 'pg', name: 'db' });
		for (const r of screen.getAllByRole('radio')) expect(r).toHaveAttribute('name', 'db');
	});

	it('gives two groups different names unless told otherwise', () => {
		render(RadioGroup, { options, value: 'pg', label: 'First' });
		render(RadioGroup, { options, value: 'pg', label: 'Second' });
		const name = (group: string) =>
			within(screen.getByRole('radiogroup', { name: group }))
				.getAllByRole('radio')[0]
				.getAttribute('name');
		expect(name('First')).toBeTruthy();
		expect(name('First')).not.toBe(name('Second'));
	});
});
