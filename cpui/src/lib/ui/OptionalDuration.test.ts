import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { bound } from '$test/fixtures/designsystem.svelte';
import OptionalDuration from './OptionalDuration.svelte';

const base = { empty: 'flow default · 30m', fallback: '1h', label: 'Timeout' };
const setup = (value: string | undefined, rest: Record<string, unknown> = {}) => {
	const onchange = vi.fn();
	const b = bound('value', value, { ...base, onchange, ...rest });
	render(OptionalDuration, b.props);
	return { ...b, onchange };
};
const clear = () => screen.getByRole('button', { name: 'Clear Timeout' });
const options = () =>
	screen.getAllByRole('option').map((o) => o.textContent!.replace(/\s*✓$/, '').trim());

describe('OptionalDuration', () => {
	describe('unset', () => {
		it('reads as what unset means here, with Set', () => {
			setup(undefined);
			expect(screen.getByText('flow default · 30m')).toBeInTheDocument();
			expect(screen.getByRole('button', { name: 'Set' })).toBeInTheDocument();
			expect(screen.queryByLabelText('Timeout')).not.toBeInTheDocument();
		});

		it('counts an empty string as unset', () => {
			setup('');
			expect(screen.getByRole('button', { name: 'Set' })).toBeInTheDocument();
		});

		it('starts from the fallback on Set', async () => {
			const { current, onchange } = setup(undefined);

			await userEvent.click(screen.getByRole('button', { name: 'Set' }));

			expect(current()).toBe('1h');
			expect(onchange).toHaveBeenCalledOnce();
			expect(screen.getByLabelText('Timeout')).toHaveValue(1);
		});

		it('offers no parameter menu when the site has none', () => {
			setup(undefined);
			expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
		});

		it('sets a parameter from a menu that keeps its prompt', async () => {
			const { current, onchange } = setup(undefined, { params: ['wait', 'budget'] });
			const menu = screen.getByRole('combobox', { name: 'Read Timeout from a parameter' });
			expect(menu).toHaveTextContent('or a parameter…');

			await userEvent.click(menu);
			expect(options()).toEqual(['$wait', '$budget']);
			await userEvent.click(screen.getByRole('option', { name: '$budget' }));

			expect(current()).toBe('$budget');
			expect(onchange).toHaveBeenCalledOnce();
		});
	});

	describe('a duration', () => {
		it('edits through the duration picker and reports each change', async () => {
			const { current, onchange } = setup('2h');
			const field = screen.getByLabelText('Timeout');

			await userEvent.clear(field);
			await userEvent.type(field, '5');

			expect(current()).toBe('5h');
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('clears back to unset', async () => {
			const { current, onchange } = setup('2h');

			await userEvent.click(clear());

			expect(current()).toBeUndefined();
			expect(onchange).toHaveBeenCalledOnce();
			expect(screen.getByText('flow default · 30m')).toBeInTheDocument();
		});

		it('names the clear generically without a label', () => {
			setup('2h', { label: undefined });
			expect(screen.getByRole('button', { name: 'Clear value' })).toBeInTheDocument();
		});
	});

	describe('a parameter', () => {
		it('shows the parameter in place of the duration picker', () => {
			setup('$wait', { params: ['wait', 'budget'] });
			expect(screen.getByRole('combobox')).toHaveTextContent('$wait');
			expect(screen.queryByRole('spinbutton')).not.toBeInTheDocument();
		});

		it('switches to another parameter', async () => {
			const { current, onchange } = setup('$wait', { params: ['wait', 'budget'] });

			await userEvent.click(screen.getByRole('combobox'));
			expect(options()[0]).toBe('$wait');
			await userEvent.click(screen.getByRole('option', { name: '$budget' }));

			expect(current()).toBe('$budget');
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('keeps listing a chosen parameter the site no longer offers', async () => {
			setup('$gone', { params: ['wait'] });
			expect(screen.getByRole('combobox')).toHaveTextContent('$gone');

			await userEvent.click(screen.getByRole('combobox'));
			expect(options()).toEqual(['$wait', '$gone']);
		});

		it('clears back to unset', async () => {
			const { current, onchange } = setup('$wait', { params: ['wait'] });

			await userEvent.click(clear());

			expect(current()).toBeUndefined();
			expect(onchange).toHaveBeenCalledOnce();
		});
	});
});
