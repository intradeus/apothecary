import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { bound } from '$test/fixtures/designsystem.svelte';
import OptionalNumber from './OptionalNumber.svelte';

const base = { empty: 'flow default', fallback: 50, label: 'Max turns' };
const setup = (value: number | undefined, rest: Record<string, unknown> = {}) => {
	const onchange = vi.fn();
	const b = bound('value', value, { ...base, onchange, ...rest });
	render(OptionalNumber, b.props);
	return { ...b, onchange };
};

describe('OptionalNumber', () => {
	it('reads as a quiet line with Set while unset', () => {
		setup(undefined);
		expect(screen.getByText('flow default')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Set' })).toBeInTheDocument();
		expect(screen.queryByRole('spinbutton')).not.toBeInTheDocument();
	});

	it('treats null as unset, the way the API sends it', () => {
		setup(null as unknown as undefined);
		expect(screen.getByText('flow default')).toBeInTheDocument();
	});

	it('starts from the fallback on Set', async () => {
		const { current, onchange } = setup(undefined);

		await userEvent.click(screen.getByRole('button', { name: 'Set' }));

		expect(current()).toBe(50);
		expect(onchange).toHaveBeenCalledOnce();
		expect(screen.getByRole('spinbutton', { name: 'Max turns' })).toHaveValue(50);
		expect(screen.queryByText('flow default')).not.toBeInTheDocument();
	});

	it('counts 0 as a value, not as unset', () => {
		setup(0, { min: 0 });
		expect(screen.getByRole('spinbutton', { name: 'Max turns' })).toHaveValue(0);
		expect(screen.queryByText('flow default')).not.toBeInTheDocument();
	});

	it('steps within its bounds and reports each change', async () => {
		const { current, onchange } = setup(9, { max: 10 });

		await userEvent.click(screen.getByRole('button', { name: 'increase' }));

		expect(current()).toBe(10);
		expect(onchange).toHaveBeenCalledOnce();
		expect(screen.getByRole('button', { name: 'increase' })).toBeDisabled();
	});

	it('takes a typed number', async () => {
		const { current, onchange } = setup(5, { max: 100 });
		const field = screen.getByRole('spinbutton', { name: 'Max turns' });

		await userEvent.clear(field);
		await userEvent.type(field, '42');

		expect(current()).toBe(42);
		expect(onchange).toHaveBeenCalled();
	});

	it('clears back to unset', async () => {
		const { current, onchange } = setup(5);

		await userEvent.click(screen.getByRole('button', { name: 'Clear Max turns' }));

		expect(current()).toBeUndefined();
		expect(onchange).toHaveBeenCalledOnce();
		expect(screen.getByText('flow default')).toBeInTheDocument();
	});

	it('names the clear generically without a label', () => {
		setup(5, { label: undefined });
		expect(screen.getByRole('button', { name: 'Clear value' })).toBeInTheDocument();
	});

	it('gives its id to whichever control is showing', async () => {
		setup(undefined, { id: 'turns' });
		expect(screen.getByRole('button', { name: 'Set' })).toHaveAttribute('id', 'turns');

		await userEvent.click(screen.getByRole('button', { name: 'Set' }));
		expect(screen.getByRole('spinbutton')).toHaveAttribute('id', 'turns');
	});
});
