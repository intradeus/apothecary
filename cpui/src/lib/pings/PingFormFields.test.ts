import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import type { FormValue, PingField } from '$lib/domain';
import { aForm, formValues, options } from '$test/fixtures/run.svelte';
import PingFormFields from './PingFormFields.svelte';

function setup(
	field: Partial<PingField> & { id: string },
	initial: Record<string, FormValue | undefined> = {},
	disabled = false
) {
	const values = formValues(initial);
	// A second field, so the field under test gets its label.
	render(PingFormFields, { form: aForm(field, { id: 'other' }), values, disabled });
	return values;
}

const labelled = (name: string) => screen.getByLabelText(name) as HTMLInputElement;

describe('PingFormFields', () => {
	describe('choosing one', () => {
		const env = {
			id: 'env',
			title: 'Environment',
			options: [
				{ id: 'dev', label: 'Development', detail: 'The safe one' },
				{ id: 'prod', label: 'Production' }
			]
		};

		it('offers each option as a radio, with its detail', () => {
			setup(env);
			expect(screen.getByRole('radiogroup', { name: 'Environment' })).toBeInTheDocument();
			expect(screen.getAllByRole('radio')).toHaveLength(2);
			expect(screen.getByText(/The safe one/)).toBeInTheDocument();
		});

		it("stores the option's id", async () => {
			const values = setup(env);
			await userEvent.click(screen.getByRole('radio', { name: /production/i }));
			expect(values.env).toBe('prod');
		});

		it('shows the value it was given', () => {
			setup(env, { env: 'prod' });
			expect(screen.getByRole('radio', { name: /production/i })).toBeChecked();
		});
	});

	describe('choosing any', () => {
		const tags = { id: 'tags', type: 'array' as const, options: options('a', 'b', 'c') };

		it('offers checkboxes, none ticked with no value', () => {
			setup(tags);
			expect(screen.getAllByRole('checkbox')).toHaveLength(3);
			screen.getAllByRole('checkbox').forEach((c) => expect(c).not.toBeChecked());
		});

		it('stores a pick in the order of the options, whatever the order of the clicks', async () => {
			const values = setup(tags);
			await userEvent.click(screen.getByRole('checkbox', { name: 'C' }));
			await userEvent.click(screen.getByRole('checkbox', { name: 'A' }));
			expect(values.tags).toEqual(['a', 'c']);
		});

		it('takes a pick back', async () => {
			const values = setup(tags, { tags: ['a', 'b'] });
			await userEvent.click(screen.getByRole('checkbox', { name: 'A' }));
			expect(values.tags).toEqual(['b']);
		});
	});

	describe('yes or no', () => {
		const flag = { id: 'flag', type: 'boolean' as const };

		it('stores a real boolean', async () => {
			const values = setup(flag);
			await userEvent.click(screen.getByRole('radio', { name: 'No' }));
			expect(values.flag).toBe(false);
			await userEvent.click(screen.getByRole('radio', { name: 'Yes' }));
			expect(values.flag).toBe(true);
		});

		it('shows a false default as No, not as unanswered', () => {
			setup(flag, { flag: false });
			expect(screen.getByRole('radio', { name: 'No' })).toBeChecked();
			expect(screen.getByRole('radio', { name: 'Yes' })).not.toBeChecked();
		});
	});

	describe('a number', () => {
		it('stores a number, not its text', async () => {
			const values = setup({ id: 'n', type: 'number' });
			await userEvent.type(labelled('n'), '2.5');
			expect(values.n).toBe(2.5);
		});

		it('stores zero as zero', async () => {
			const values = setup({ id: 'n', type: 'integer' });
			await userEvent.type(labelled('n'), '0');
			expect(values.n).toBe(0);
		});

		it('stores nothing once cleared, so a required field counts as unanswered', async () => {
			const values = setup({ id: 'n', type: 'number' }, { n: 4 });
			await userEvent.clear(labelled('n'));
			expect(values.n).toBeUndefined();
		});

		it.each([
			['integer', '1'],
			['number', 'any']
		] as const)('steps a %s by %s', (type, step) => {
			setup({ id: 'n', type });
			expect(labelled('n')).toHaveAttribute('step', step);
		});

		it('states its bounds', () => {
			setup({ id: 'n', type: 'integer', minimum: 1, maximum: 9 });
			expect(labelled('n')).toHaveAttribute('min', '1');
			expect(labelled('n')).toHaveAttribute('max', '9');
		});

		it('shows the value it was given, zero included', () => {
			setup({ id: 'n', type: 'number' }, { n: 0 });
			expect(labelled('n')).toHaveValue(0);
		});
	});

	describe('a date and time', () => {
		const when = { id: 'when', format: 'date-time' as const };

		it('turns the local input into RFC 3339', async () => {
			const values = setup(when);
			await fireEvent.input(labelled('when'), { target: { value: '2026-10-31T12:30' } });
			expect(values.when).toBe(new Date('2026-10-31T12:30').toISOString());
		});

		it('stores nothing once cleared', async () => {
			const values = setup(when, { when: '2026-10-31T12:30:00.000Z' });
			await fireEvent.input(labelled('when'), { target: { value: '' } });
			expect(values.when).toBeUndefined();
		});

		it('shows the value it was given', () => {
			setup(when, { when: '2026-10-31T12:30:00.000Z' });
			expect(labelled('when').value).not.toBe('');
		});

		it('shows that value in local time, as the input speaks it', () => {
			setup(when, { when: '2026-10-31T12:30:00.000Z' });
			expect(new Date(labelled('when').value).toISOString()).toBe('2026-10-31T12:30:00.000Z');
		});

		it('shows nothing for a value that is no date', () => {
			setup(when, { when: 'soon' });
			expect(labelled('when').value).toBe('');
		});
	});

	describe('text', () => {
		it.each([
			['email', 'email'],
			['uri', 'url'],
			['date', 'date']
		] as const)('gives a %s field an input of type %s', (format, type) => {
			setup({ id: 'f', format });
			expect(labelled('f')).toHaveAttribute('type', type);
		});

		it('gives a pattern a one-line input carrying it', () => {
			setup({ id: 'f', pattern: '[a-z]+', minLength: 2, maxLength: 500 });
			const input = labelled('f');
			expect(input.tagName).toBe('INPUT');
			expect(input).toHaveAttribute('type', 'text');
			expect(input).toHaveAttribute('pattern', '[a-z]+');
			expect(input).toHaveAttribute('minlength', '2');
			expect(input).toHaveAttribute('maxlength', '500');
		});

		it.each([
			[undefined, 'TEXTAREA'],
			[201, 'TEXTAREA'],
			[200, 'INPUT']
		])('with a limit of %s reads as a %s', (maxLength, tag) => {
			setup({ id: 'f', maxLength });
			expect(labelled('f').tagName).toBe(tag);
		});

		it('writes what is typed to the values', async () => {
			const values = setup({ id: 'f', format: 'email' });
			await userEvent.type(labelled('f'), 'a@b.test');
			expect(values.f).toBe('a@b.test');
		});

		it('writes what is typed in a long answer', async () => {
			const values = setup({ id: 'f' });
			await userEvent.type(labelled('f'), 'Long answer');
			expect(values.f).toBe('Long answer');
		});
	});

	describe('its labels', () => {
		it('names a field by its title, else by its id', () => {
			const values = formValues();
			const form = aForm({ id: 'a', title: 'First' }, { id: 'b' });
			render(PingFormFields, { form, values });
			expect(screen.getByRole('group', { name: 'First' })).toBeInTheDocument();
			expect(screen.getByRole('group', { name: 'b' })).toBeInTheDocument();
		});

		it('stars a required field', () => {
			setup({ id: 'a', required: true });
			expect(screen.getByTitle('required')).toHaveTextContent('*');
		});

		it('does not star an optional one', () => {
			setup({ id: 'a' });
			expect(screen.queryByTitle('required')).not.toBeInTheDocument();
		});

		it('shows a description', () => {
			setup({ id: 'a', description: 'What it is for' });
			expect(screen.getByText('What it is for')).toBeInTheDocument();
		});

		it('drops the legend of a lone field: the question already asks it', () => {
			const { container } = render(PingFormFields, {
				form: aForm({ id: 'a' }),
				values: formValues()
			});
			expect(container.querySelector('legend')).toBeNull();
			expect(screen.getByRole('textbox', { name: 'a' })).toHaveAttribute(
				'placeholder',
				'Your answer to the agent…'
			);
		});

		it('keeps the legend of a lone field with a title', () => {
			const { container } = render(PingFormFields, {
				form: aForm({ id: 'a', title: 'Reason' }),
				values: formValues()
			});
			expect(container.querySelector('legend')).toHaveTextContent('Reason');
		});
	});

	it('holds every kind of control when disabled', () => {
		const form = aForm(
			{ id: 'pick', options: options('a') },
			{ id: 'many', type: 'array', options: options('b') },
			{ id: 'flag', type: 'boolean' },
			{ id: 'n', type: 'number' },
			{ id: 'when', format: 'date-time' },
			{ id: 'mail', format: 'email' },
			{ id: 'text' }
		);
		const { container } = render(PingFormFields, { form, values: formValues(), disabled: true });
		const controls = container.querySelectorAll('input, textarea');
		expect(controls.length).toBeGreaterThanOrEqual(7);
		controls.forEach((c) => expect(c).toBeDisabled());
	});
});
