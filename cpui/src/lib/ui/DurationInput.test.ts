import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { bound } from '$test/fixtures/designsystem.svelte';
import DurationInput from './DurationInput.svelte';

const setup = (value: string, rest: Record<string, unknown> = {}) => {
	const b = bound('value', value, { label: 'Keep for', ...rest });
	render(DurationInput, b.props);
	return b;
};
const number = () => screen.getByLabelText('Keep for') as HTMLInputElement;
const unit = () => screen.getByRole('combobox', { name: 'unit' });
const pick = async (label: string) => {
	await userEvent.click(unit());
	await userEvent.click(screen.getByRole('option', { name: label }));
};
const units = async () => {
	await userEvent.click(unit());
	return screen.getAllByRole('option').map((o) => o.textContent!.replace(/\s*✓$/, '').trim());
};
const retype = async (s: string) => {
	await userEvent.clear(number());
	await userEvent.type(number(), s);
};

describe('DurationInput', () => {
	it.each([
		['48h', 2, 'days'],
		['24h', 1, 'days'],
		['2h', 2, 'hours'],
		['90m', 90, 'minutes'],
		['1h30m', 90, 'minutes']
	])('reads %s as %s %s', (value, n, label) => {
		setup(value);
		expect(number()).toHaveValue(n);
		expect(unit()).toHaveTextContent(label);
	});

	it('writes what is typed in the current unit', async () => {
		const b = setup('2h');
		await retype('5');
		expect(b.current()).toBe('5h');
		expect(number()).toHaveValue(5);
	});

	// Go's parser has no day unit.
	it('sends days as hours', async () => {
		const b = setup('48h');
		await retype('3');
		expect(b.current()).toBe('72h');
		expect(unit()).toHaveTextContent('days');
	});

	// 60m reads back as 1 hour: the next digit must not land in hours, or "600"
	// minutes is stored as 600h ("240" hours as 240 days the same way).
	it('keeps the unit while a number is being typed', async () => {
		const b = setup('5m');
		await retype('600');
		expect(b.current()).toBe('600m');
		expect(unit()).toHaveTextContent('minutes');
	});

	it('reads the value afresh on leaving the number', async () => {
		const b = setup('5m');
		await retype('600');
		await fireEvent.blur(number());
		expect(b.current()).toBe('600m');
		expect(number()).toHaveValue(10);
		expect(unit()).toHaveTextContent('hours');
	});

	it('keeps the number when the unit changes', async () => {
		const b = setup('2h');
		await pick('days');
		expect(b.current()).toBe('48h');
		await pick('minutes');
		expect(b.current()).toBe('2m');
	});

	it('leaves the value alone for a number it cannot use', async () => {
		const b = setup('2h');
		await retype('0');
		expect(b.current()).toBe('2h');
		expect(unit()).toHaveAttribute('aria-invalid', 'true');
	});

	it('restores the number on leaving a bad entry', async () => {
		setup('2h');
		await retype('-3');
		await fireEvent.blur(number());
		expect(number()).toHaveValue(2);
		expect(unit()).not.toHaveAttribute('aria-invalid');
	});

	it('leaves the value alone while the field is empty', async () => {
		const b = setup('2h');
		await userEvent.clear(number());
		expect(b.current()).toBe('2h');
		expect(number()).toHaveValue(null);
	});

	it('keeps never off the list by default', async () => {
		setup('2h');
		expect(await units()).toEqual(['minutes', 'hours', 'days']);
	});

	it('offers never when allowed', async () => {
		setup('2h', { allowNever: true });
		expect(await units()).toEqual(['minutes', 'hours', 'days', 'never']);
	});

	it('still lists never when that is the stored value', async () => {
		setup('never');
		expect(unit()).toHaveTextContent('never');
		expect(await units()).toContain('never');
	});

	it('locks the number on never', () => {
		setup('never');
		expect(number()).toBeDisabled();
		expect(number()).toHaveValue(null);
		expect(number()).toHaveAttribute('placeholder', '—');
	});

	it('remembers the number across never and back', async () => {
		const b = setup('3h', { allowNever: true });
		await pick('never');
		expect(b.current()).toBe('never');
		await pick('hours');
		expect(b.current()).toBe('3h');
	});

	it('starts from a day of hours when never is all it has known', async () => {
		const b = setup('never');
		await pick('hours');
		expect(b.current()).toBe('24h');
	});

	it('remembers a typed number across never and back', async () => {
		const b = setup('3h', { allowNever: true });
		await retype('7');
		await fireEvent.blur(number());
		await pick('never');
		await pick('hours');
		expect(b.current()).toBe('7h');
	});

	describe('a value it cannot read', () => {
		it('says so and shows the value', () => {
			setup('soon');
			expect(screen.getByText('soon').tagName).toBe('CODE');
			expect(screen.getByText(/which this picker cannot read/)).toBeInTheDocument();
			expect(unit()).toHaveAttribute('aria-invalid', 'true');
		});

		// The message goes through {@html} and the value comes from the engine.
		it('escapes it', () => {
			const { container } = render(DurationInput, { value: '<img src=x>', label: 'Other' });
			expect(container.querySelector('img')).not.toBeInTheDocument();
			expect(screen.getByText('<img src=x>')).toBeInTheDocument();
		});

		it('is replaced by a number', async () => {
			const b = setup('soon');
			await userEvent.type(number(), '4');
			expect(b.current()).toBe('4h');
			expect(screen.queryByText(/cannot read/)).not.toBeInTheDocument();
		});

		it('is replaced by a unit', async () => {
			const b = setup('soon');
			await pick('minutes');
			expect(b.current()).toBe('24m');
		});
	});

	it('flags an empty value without a message', () => {
		setup('');
		expect(unit()).toHaveAttribute('aria-invalid', 'true');
		expect(screen.queryByText(/cannot read/)).not.toBeInTheDocument();
	});

	it('disables both controls and raises no flag while disabled', () => {
		setup('', { disabled: true });
		expect(number()).toBeDisabled();
		expect(unit()).toBeDisabled();
		expect(unit()).not.toHaveAttribute('aria-invalid');
	});

	it('gives its id to the number, for a label to point at', () => {
		setup('2h', { id: 'keep' });
		expect(number()).toHaveAttribute('id', 'keep');
	});
});
