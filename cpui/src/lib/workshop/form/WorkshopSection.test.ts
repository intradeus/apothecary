import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import type { KVRow } from '$lib/ui/KeyValueEditor.svelte';
import { aFlow, bindable } from '$test/fixtures/app.svelte';
import WorkshopSection from './WorkshopSection.svelte';

const setup = (over: Record<string, unknown> = {}) => {
	const props = bindable({
		ownFlows: [aFlow({ name: 'mine' })],
		templateFlows: [aFlow({ name: 'tpl', builtin: true })],
		noFlows: false,
		dupKeys: new Set<string>(),
		name: '',
		defaultFlow: 'mine',
		commandRows: [{ key: '', value: '' }] as KVRow[],
		...over
	});
	render(WorkshopSection, props);
	return props;
};

describe('WorkshopSection', () => {
	it('binds the name', async () => {
		const props = setup();
		await userEvent.type(screen.getByLabelText('Workshop name'), 'billing');
		expect(props.name).toBe('billing');
	});

	it('requires the name', () => {
		setup();
		expect(screen.getByLabelText('Workshop name')).toBeRequired();
	});

	it("groups flows into the workshop's and the templates, and binds the pick", async () => {
		const props = setup();
		await userEvent.click(screen.getByRole('combobox', { name: 'Default flow' }));
		expect(await screen.findByText('This workshop’s flows')).toBeInTheDocument();
		expect(screen.getByText('Built-in templates')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('option', { name: 'tpl' }));
		expect(props.defaultFlow).toBe('tpl');
	});

	it('says when no flow exists', () => {
		setup({ noFlows: true, ownFlows: [], templateFlows: [], defaultFlow: '' });
		expect(screen.getByText(/No flows exist yet/)).toBeInTheDocument();
	});

	it('edits commands through to the rows, and counts the named ones', async () => {
		const props = setup();
		await userEvent.type(screen.getByRole('textbox', { name: 'Key' }), 'test');
		await userEvent.type(screen.getByRole('textbox', { name: 'Value for test' }), 'make test');
		await userEvent.click(screen.getByRole('button', { name: /Add command/ }));

		expect(props.commandRows).toEqual([
			{ key: 'test', value: 'make test' },
			{ key: '', value: '' }
		]);
		expect(screen.getByRole('heading', { name: 'Commands' }).nextElementSibling).toHaveTextContent(
			'1'
		);
	});

	it('names duplicate commands and marks their fields', () => {
		setup({
			dupKeys: new Set(['test']),
			commandRows: [
				{ key: 'test', value: 'a' },
				{ key: 'test', value: 'b' }
			]
		});
		expect(screen.getByText('Duplicate names: test')).toBeInTheDocument();
		for (const key of screen.getAllByRole('textbox', { name: 'Key' }))
			expect(key).toHaveClass('invalid');
	});
});
