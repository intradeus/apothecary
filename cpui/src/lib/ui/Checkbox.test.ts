import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import { bound } from '$test/fixtures/designsystem.svelte';
import { text } from '$test/helpers';
import Checkbox from './Checkbox.svelte';

describe('Checkbox', () => {
	it('is a native checkbox named by its label', () => {
		render(Checkbox, { label: 'Keep the workspace' });
		expect(screen.getByRole('checkbox', { name: 'Keep the workspace' })).not.toBeChecked();
	});

	it('puts the detail after the label', () => {
		render(Checkbox, { label: 'Keep', detail: 'for 48 hours' });
		expect(screen.getByRole('checkbox', { name: /^Keep\s*— for 48 hours$/ })).toBeInTheDocument();
	});

	it('prefers rich children over the label', () => {
		render(Checkbox, { label: 'Plain', children: text('Rich') });
		expect(screen.getByRole('checkbox', { name: 'Rich' })).toBeInTheDocument();
		expect(screen.queryByText('Plain')).not.toBeInTheDocument();
	});

	it('toggles from a click on the label and reports the new value', async () => {
		const onchange = vi.fn();
		const b = bound('checked', false, { label: 'Keep', onchange });
		render(Checkbox, b.props);

		await userEvent.click(screen.getByText('Keep'));
		expect(b.current()).toBe(true);
		expect(onchange).toHaveBeenLastCalledWith(true);

		await userEvent.click(screen.getByText('Keep'));
		expect(b.current()).toBe(false);
		expect(onchange).toHaveBeenLastCalledWith(false);
		expect(onchange).toHaveBeenCalledTimes(2);
	});

	it('toggles with Space from the keyboard', async () => {
		const onchange = vi.fn();
		render(Checkbox, { label: 'Keep', onchange });

		await userEvent.tab();
		expect(screen.getByRole('checkbox')).toHaveFocus();
		await userEvent.keyboard(' ');

		expect(screen.getByRole('checkbox')).toBeChecked();
		expect(onchange).toHaveBeenCalledExactlyOnceWith(true);
	});

	it('follows a value the parent changes', async () => {
		const b = bound('checked', false, { label: 'Keep' });
		render(Checkbox, b.props);

		b.props.checked = true;
		await tick();

		expect(screen.getByRole('checkbox')).toBeChecked();
	});

	it('ignores clicks and keys while disabled', async () => {
		const onchange = vi.fn();
		render(Checkbox, { label: 'Keep', disabled: true, onchange });

		await userEvent.click(screen.getByText('Keep'));
		await userEvent.tab();

		expect(screen.getByRole('checkbox')).toBeDisabled();
		expect(screen.getByRole('checkbox')).not.toHaveFocus();
		expect(screen.getByRole('checkbox')).not.toBeChecked();
		expect(onchange).not.toHaveBeenCalled();
	});

	it('reads as mixed when indeterminate', () => {
		render(Checkbox, { label: 'All', indeterminate: true });
		expect(screen.getByRole('checkbox')).toBePartiallyChecked();
	});

	it('takes part in a form under its name and value', () => {
		render(Checkbox, { label: 'Keep', id: 'keep', name: 'opts', value: 'keep', checked: true });
		const box = screen.getByRole('checkbox') as HTMLInputElement;
		expect(box).toHaveAttribute('id', 'keep');
		expect(box).toHaveAttribute('name', 'opts');
		expect(box.value).toBe('keep');
	});
});
