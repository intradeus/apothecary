import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { fill } from '$test/fixtures/flow.svelte';
import { deferred, text } from '$test/helpers';
import NameFlowDialog from './NameFlowDialog.svelte';

const setup = (over: Partial<Parameters<typeof NameFlowDialog>[1]> = {}) => {
	const onsubmit = vi.fn(async () => {});
	const onclose = vi.fn();
	render(NameFlowDialog, { title: 'New flow', action: 'Create', onsubmit, onclose, ...over });
	return { onsubmit, onclose };
};
const submit = () => screen.getByRole('button', { name: 'Create' });
const field = () => screen.getByLabelText('Name');

describe('NameFlowDialog', () => {
	it('shows its title and what it says above the field', () => {
		setup({ children: text('Starts from the current draft.') });
		expect(screen.getByRole('dialog', { name: 'New flow' })).toBeInTheDocument();
		expect(screen.getByText('Starts from the current draft.')).toBeInTheDocument();
	});

	it('opens on the name it is given', () => {
		setup({ initial: 'builder-copy' });
		expect(field()).toHaveValue('builder-copy');
		expect(submit()).toBeEnabled();
	});

	it('needs a name', () => {
		setup();
		expect(submit()).toBeDisabled();
	});

	it('refuses a name that is no slug', async () => {
		setup();
		await fill(field(), 'Not Ok');
		expect(submit()).toBeDisabled();
	});

	it('refuses a name already taken, saying which', async () => {
		setup({ taken: ['builder'] });
		await fill(field(), 'builder');
		expect(submit()).toBeDisabled();
		expect(
			screen.getByText('This workshop already has a flow called builder.')
		).toBeInTheDocument();
	});

	it('hands over the name, trimmed', async () => {
		const { onsubmit } = setup();
		await fill(field(), '  fresh ');
		await userEvent.click(submit());
		expect(onsubmit).toHaveBeenCalledExactlyOnceWith('fresh');
	});

	it('submits on Enter', async () => {
		const { onsubmit } = setup();
		await fill(field(), 'fresh{Enter}');
		expect(onsubmit).toHaveBeenCalledExactlyOnceWith('fresh');
	});

	it('holds the button while it works', async () => {
		const run = deferred();
		setup({ onsubmit: () => run.promise });
		await fill(field(), 'fresh');
		await userEvent.click(submit());
		const busy = screen.getByRole('button', { name: 'Working…' });
		expect(busy).toBeDisabled();
		expect(busy).toHaveAttribute('aria-busy', 'true');
		run.resolve();
	});

	it('shows a failure and lets it be tried again', async () => {
		setup({ onsubmit: () => Promise.reject(new Error('already exists')) });
		await fill(field(), 'fresh');
		await userEvent.click(submit());
		expect(await screen.findByText('already exists')).toBeInTheDocument();
		expect(submit()).toBeEnabled();
	});

	it('closes on Cancel', async () => {
		const { onclose } = setup();
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(onclose).toHaveBeenCalledOnce();
	});
});
