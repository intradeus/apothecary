import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import Stepper from './Stepper.svelte';

const input = () => screen.getByRole('spinbutton');
const down = () => screen.getByRole('button', { name: 'decrease' });
const up = () => screen.getByRole('button', { name: 'increase' });

describe('Stepper', () => {
	it('shows its value, named and bounded for the browser', () => {
		render(Stepper, { value: 4, min: 2, max: 9, id: 'limit', label: 'Runs' });
		const field = screen.getByRole('spinbutton', { name: 'Runs' });
		expect(field).toHaveValue(4);
		expect(field).toHaveAttribute('id', 'limit');
		expect(field).toHaveAttribute('min', '2');
		expect(field).toHaveAttribute('max', '9');
	});

	it('steps down and up', async () => {
		render(Stepper, { value: 4 });
		await userEvent.click(up());
		expect(input()).toHaveValue(5);
		await userEvent.click(down());
		await userEvent.click(down());
		expect(input()).toHaveValue(3);
	});

	it('stops at min and at max', async () => {
		render(Stepper, { value: 2, min: 1, max: 3 });

		await userEvent.click(down());
		expect(input()).toHaveValue(1);
		expect(down()).toBeDisabled();
		expect(up()).toBeEnabled();

		await userEvent.click(up());
		await userEvent.click(up());
		expect(input()).toHaveValue(3);
		expect(up()).toBeDisabled();
		expect(down()).toBeEnabled();
	});

	it('takes a typed number', async () => {
		render(Stepper, { value: 1 });
		await userEvent.clear(input());
		await userEvent.type(input(), '42');
		expect(input()).toHaveValue(42);
		expect(up()).toBeEnabled();
	});

	it('rounds a typed fraction to an integer', async () => {
		render(Stepper, { value: 1 });
		await fireEvent.input(input(), { target: { value: '2.6' } });
		expect(input()).toHaveValue(3);
	});

	it('clamps a typed value that moves it past a limit', async () => {
		render(Stepper, { value: 5, min: 1, max: 10 });
		await fireEvent.input(input(), { target: { value: '500' } });
		expect(input()).toHaveValue(10);
		expect(up()).toBeDisabled();
	});

	// The clamp lands on the value it already had, so Svelte never rewrites the
	// input.
	it('shows the clamped value when already at the limit', async () => {
		render(Stepper, { value: 10, min: 1, max: 10 });
		await fireEvent.input(input(), { target: { value: '500' } });
		expect(input()).toHaveValue(10);
	});

	it('lets the field go empty while typing without touching the value', async () => {
		render(Stepper, { value: 5, max: 5 });
		await userEvent.clear(input());
		expect(up()).toBeDisabled();
	});

	it.each([
		['empty', ''],
		['under the min', '1'],
		['a fraction the value rounded away', '5.2']
	])('puts the real value back on leaving it %s', async (_, typed) => {
		render(Stepper, { value: 5, min: 5, max: 9 });
		await fireEvent.input(input(), { target: { value: typed } });
		await fireEvent.blur(input());
		expect(input()).toHaveValue(5);
	});

	it('lets the first digit of a number stand while it is under the min', async () => {
		render(Stepper, { value: 10, min: 10, max: 99 });
		await userEvent.clear(input());
		await userEvent.type(input(), '42');
		expect(input()).toHaveValue(42);
	});

	it('follows a value set from outside', async () => {
		const { rerender } = render(Stepper, { value: 1 });
		await rerender({ value: 7 });
		expect(input()).toHaveValue(7);
	});
});
