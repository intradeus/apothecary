import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { text } from '$test/helpers';
import Radio from './Radio.svelte';

const props = { group: 'mysql', value: 'pg', label: 'Postgres' };

describe('Radio', () => {
	it('is on only when the group holds its value', async () => {
		const { rerender } = render(Radio, props);
		expect(screen.getByRole('radio', { name: 'Postgres' })).not.toBeChecked();
		await rerender({ group: 'pg' });
		expect(screen.getByRole('radio', { name: 'Postgres' })).toBeChecked();
	});

	it('takes the value on a click and says so', async () => {
		const onchange = vi.fn();
		render(Radio, { ...props, onchange });

		await userEvent.click(screen.getByText('Postgres'));

		expect(screen.getByRole('radio', { name: 'Postgres' })).toBeChecked();
		expect(onchange).toHaveBeenCalledExactlyOnceWith('pg');
	});

	it('ignores the click when disabled', async () => {
		const onchange = vi.fn();
		render(Radio, { ...props, disabled: true, onchange });

		await userEvent.click(screen.getByText('Postgres'));

		expect(screen.getByRole('radio', { name: 'Postgres' })).toBeDisabled();
		expect(onchange).not.toHaveBeenCalled();
	});

	it('moves with the arrow keys inside its name', async () => {
		const onchange = vi.fn();
		render(Radio, { group: 'a', value: 'a', name: 'db', label: 'A' });
		render(Radio, { group: 'a', value: 'b', name: 'db', label: 'B', onchange });

		screen.getByRole('radio', { name: 'A' }).focus();
		await userEvent.keyboard('{ArrowDown}');

		expect(screen.getByRole('radio', { name: 'B' })).toHaveFocus();
		expect(onchange).toHaveBeenCalledExactlyOnceWith('b');
	});

	it('reads the detail as part of its name', () => {
		render(Radio, { ...props, detail: 'managed' });
		expect(screen.getByRole('radio', { name: /^Postgres\s*— managed$/ })).toBeInTheDocument();
	});

	it('draws children in place of the label', () => {
		render(Radio, { ...props, children: text('Custom') });
		expect(screen.getByRole('radio', { name: 'Custom' })).toBeInTheDocument();
		expect(screen.queryByText('Postgres')).toBeNull();
	});

	it('passes its id to the input and its title to the row', () => {
		render(Radio, { ...props, id: 'db-pg', title: 'Recommended' });
		expect(screen.getByRole('radio')).toHaveAttribute('id', 'db-pg');
		expect(screen.getByTitle('Recommended')).toContainElement(screen.getByRole('radio'));
	});

	it('hides its ring from assistive tech: the input is what they read', () => {
		const { container } = render(Radio, props);
		expect(container.querySelector('.ring')).toHaveAttribute('aria-hidden', 'true');
	});
});
