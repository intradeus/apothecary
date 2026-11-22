import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import SizeFields from './SizeFields.svelte';

const placeholder = { cpus: 'default 2', memoryGiB: 'default 4' };
const cpus = () => screen.getByLabelText('CPUs') as HTMLInputElement;
const memory = () => screen.getByLabelText('Memory') as HTMLInputElement;

describe('SizeFields', () => {
	it('shows the size, and the placeholder for an unset field', () => {
		render(SizeFields, { size: { cpus: 1.5, memoryGiB: null }, idPrefix: 'p', placeholder });
		expect(cpus().value).toBe('1.5');
		expect(memory().value).toBe('');
		expect(memory().placeholder).toBe('default 4');
	});

	it('reports a typed value, and a cleared field as unset', async () => {
		const onchange = vi.fn();
		render(SizeFields, {
			size: { cpus: 2, memoryGiB: null },
			idPrefix: 'p',
			placeholder,
			onchange
		});
		await userEvent.type(memory(), '8');
		expect(onchange).toHaveBeenLastCalledWith({ cpus: 2, memoryGiB: 8 });
		await userEvent.clear(cpus());
		expect(onchange).toHaveBeenLastCalledWith({ cpus: null, memoryGiB: 8 });
	});

	it('marks the fields it is told are invalid', () => {
		render(SizeFields, {
			size: { cpus: 64, memoryGiB: 1 },
			idPrefix: 'p',
			placeholder,
			invalid: ['cpus']
		});
		expect(cpus()).toHaveAttribute('aria-invalid', 'true');
		expect(memory()).not.toHaveAttribute('aria-invalid');
	});

	it('is read-only when disabled', () => {
		render(SizeFields, {
			size: { cpus: 1, memoryGiB: 1 },
			idPrefix: 'p',
			placeholder,
			disabled: true
		});
		expect(cpus()).toBeDisabled();
		expect(memory()).toBeDisabled();
	});
});
