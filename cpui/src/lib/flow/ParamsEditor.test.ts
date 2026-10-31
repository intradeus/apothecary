import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { aDoc, answer, aStage, handlers, pick, retype } from '$test/fixtures/flow.svelte';
import type { FlowDoc } from '$lib/domain/flowdoc';
import ParamsEditor from './ParamsEditor.svelte';

const setup = (over: Partial<FlowDoc> = {}) => {
	const doc = aDoc(over);
	const { onchange } = handlers();
	render(ParamsEditor, { doc, onchange });
	return { doc, onchange };
};
const nameOf = () => screen.getByRole('textbox', { name: 'Name' });
const def = () => screen.getByLabelText('Default');

describe('ParamsEditor', () => {
	describe('the six the engine reads', () => {
		it('lists all of them, with no default for the two a workshop binds', () => {
			setup();
			for (const name of [
				'harness',
				'model',
				'timeout',
				'max_turns',
				'max_cost_usd',
				'max_total_turns'
			])
				expect(screen.getByLabelText(new RegExp(`^${name}`))).toBeInTheDocument();
			expect(screen.getByLabelText(/^harness/)).toHaveAccessibleName(
				'harness no default · the workshop binds it, or a run sets it'
			);
			expect(screen.getByLabelText(/^timeout/)).toHaveAccessibleName(
				'timeout engine default · 30m'
			);
		});

		it('writes a value of the type the engine reads', async () => {
			const { doc, onchange } = setup();
			await retype(screen.getByLabelText(/^max_turns/), '20');
			expect(doc.parameters.max_turns).toEqual({ default: 20 });
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('keeps what else it says when the value changes', async () => {
			const { doc } = setup({
				parameters: { timeout: { default: '5m', description: 'Long enough' } }
			});
			await retype(screen.getByLabelText(/^timeout/), '10m');
			expect(doc.parameters.timeout).toEqual({ default: '10m', description: 'Long enough' });
		});

		it('takes it off when emptied, falling back to the engine', async () => {
			const { doc } = setup({ parameters: { timeout: { default: '5m' } } });
			await retype(screen.getByLabelText(/^timeout/), '');
			expect(doc.parameters.timeout).toBeUndefined();
		});

		it('says more of a value that is set', async () => {
			const { doc } = setup({ parameters: { model: { default: 'acme/m' } } });
			await retype(screen.getByLabelText('Description'), 'Which model');
			await retype(screen.getByLabelText('Allowed values'), 'acme/m, , acme/n');
			expect(doc.parameters.model).toEqual({
				default: 'acme/m',
				description: 'Which model',
				enum: ['acme/m', 'acme/n']
			});
		});

		it('hides that detail until there is one', () => {
			setup({ parameters: { model: { default: 'acme/m' } } });
			expect(
				screen.getByText('description, allowed values').closest('details')
			).not.toHaveAttribute('open');
		});

		it('opens it on a value that says more', () => {
			setup({ parameters: { model: { default: 'acme/m', enum: ['acme/m'] } } });
			expect(screen.getByText('description, allowed values').closest('details')).toHaveAttribute(
				'open'
			);
		});
	});

	describe('adding and renaming', () => {
		it('adds the next free name as a string', async () => {
			const { doc, onchange } = setup({ parameters: { param_1: { type: 'string' } } });
			await userEvent.click(screen.getByRole('button', { name: '+ parameter' }));
			expect(doc.parameters.param_2).toEqual({ type: 'string', default: '' });
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('renames it and what reads it', async () => {
			const { doc, onchange } = setup({
				parameters: { region: { type: 'string', default: 'eu' } },
				stages: [aStage('a', 'command', { run_ref: '$region' })]
			});
			await retype(nameOf(), 'zone');
			expect(doc.parameters.zone).toBeDefined();
			expect(doc.parameters.region).toBeUndefined();
			expect(doc.stages[0].run_ref).toBe('$zone');
			expect(onchange).toHaveBeenCalledOnce();
		});

		it.each([
			['a taken name', 'other'],
			['a bad name', 'Bad-Name'],
			['nothing', '']
		])('refuses %s', async (_, text) => {
			const { doc, onchange } = setup({
				parameters: { region: { type: 'string' }, other: { type: 'string' } }
			});
			await retype(screen.getAllByRole('textbox', { name: 'Name' })[0], text);
			expect(doc.parameters.region).toBeDefined();
			expect(onchange).not.toHaveBeenCalled();
		});
	});

	describe('removing', () => {
		it('removes an unread one at once', async () => {
			const { doc, onchange } = setup({ parameters: { region: { type: 'string' } } });
			await userEvent.click(screen.getByRole('button', { name: 'Remove' }));
			expect(doc.parameters.region).toBeUndefined();
			expect(onchange).toHaveBeenCalledOnce();
		});

		const read = {
			parameters: { region: { type: 'string' as const } },
			stages: [aStage('a', 'command', { run_ref: '$region' })]
		};

		it('asks first when something reads it', async () => {
			const { doc, onchange } = setup(read);
			await userEvent.click(screen.getByRole('button', { name: 'Remove' }));
			expect(onchange).not.toHaveBeenCalled();
			const asked = await answer(true);
			expect(asked.title).toBe('Remove parameter region?');
			expect(asked.body).toContain('a.run_ref');
			await vi.waitFor(() => expect(doc.parameters.region).toBeUndefined());
			expect(onchange).toHaveBeenCalledOnce();
		});

		it.each<[string, Partial<FlowDoc>, string]>([
			['the Ask pane', { ask: { harness: '$region' } }, 'ask.harness'],
			[
				'another parameter’s default',
				{
					parameters: { region: { type: 'string' }, near: { type: 'string', default: '$region' } }
				},
				'near.default'
			]
		])('asks first when %s reads it', async (_, over, where) => {
			const { doc } = setup({ parameters: { region: { type: 'string' } }, ...over });
			await userEvent.click(screen.getAllByRole('button', { name: 'Remove' })[0]);
			const asked = await answer(false);
			expect(asked.body).toContain(where);
			expect(doc.parameters.region).toBeDefined();
		});

		it('keeps it on a no', async () => {
			const { doc, onchange } = setup(read);
			await userEvent.click(screen.getByRole('button', { name: 'Remove' }));
			await answer(false);
			expect(doc.parameters.region).toBeDefined();
			expect(onchange).not.toHaveBeenCalled();
		});

		it('counts where it is read', () => {
			setup(read);
			expect(screen.getByText('read in 1 place')).toBeInTheDocument();
		});
	});

	describe('its type', () => {
		const one = (p: FlowDoc['parameters'][string]) => ({ parameters: { p } });

		it('demands one of an undeclared parameter', () => {
			setup(one({ default: 'x' }));
			expect(
				screen.getByText('Say what it holds: a parameter of the flow declares its type.')
			).toBeInTheDocument();
		});

		it('reads the default as the new type where it parses', async () => {
			const { doc, onchange } = setup(one({ type: 'string', default: '4' }));
			await pick('Type', 'int');
			expect(doc.parameters.p).toMatchObject({ type: 'int', default: 4 });
			expect(onchange).toHaveBeenCalledOnce();
		});

		it.each([
			['bool', false],
			['int', 0],
			['float', 0],
			['duration', '30m']
		])('gives %s its empty value where it does not', async (type, value) => {
			const { doc } = setup(one({ type: 'string', default: 'nonsense' }));
			await pick('Type', type);
			expect(doc.parameters.p.default).toBe(value);
		});

		it('keeps a duration that is one', async () => {
			const { doc } = setup(one({ type: 'string', default: '1h30m' }));
			await pick('Type', 'duration');
			expect(doc.parameters.p.default).toBe('1h30m');
		});

		it('empties a list none of whose items parse', async () => {
			const { doc } = setup(one({ type: 'string', list: true, default: ['a', 'b'] }));
			await pick('Type', 'int');
			expect(doc.parameters.p.default).toEqual([]);
		});

		it('keeps allowed values only on a string', async () => {
			const { doc } = setup(one({ type: 'string', default: 'a', enum: ['a'] }));
			await pick('Type', 'bool');
			expect(doc.parameters.p.enum).toBeUndefined();
			expect(screen.queryByLabelText('Allowed values')).not.toBeInTheDocument();
		});

		it('makes it a list and back', async () => {
			const { doc } = setup(one({ type: 'string', default: 'a, b' }));
			await userEvent.click(screen.getByRole('checkbox', { name: 'a list' }));
			expect(doc.parameters.p).toMatchObject({ list: true, default: ['a', 'b'] });

			await userEvent.click(screen.getByRole('checkbox', { name: 'a list' }));
			expect(doc.parameters.p.list).toBeUndefined();
			expect(doc.parameters.p.default).toBe('a, b');
		});
	});

	describe('its default', () => {
		it('writes it parsed by type', async () => {
			const { doc } = setup({ parameters: { p: { type: 'int', default: 1 } } });
			await retype(def(), '7');
			expect(doc.parameters.p.default).toBe(7);
		});

		it('writes a list’s items', async () => {
			const { doc } = setup({ parameters: { p: { type: 'string', list: true, default: [] } } });
			await retype(def(), 'a, b');
			expect(doc.parameters.p.default).toEqual(['a', 'b']);
		});

		it('picks a bool’s from true and false', async () => {
			const { doc, onchange } = setup({ parameters: { p: { type: 'bool', default: false } } });
			await pick('Default', 'true');
			expect(doc.parameters.p.default).toBe(true);
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('explains an empty one a stage reads as its model', () => {
			setup({
				parameters: { fast: { type: 'string', default: '' } },
				stages: [aStage('a', 'agent', { model: '$fast' })]
			});
			expect(screen.getByText(/^No default: read as a model or a harness/)).toBeInTheDocument();
		});
	});

	it('writes allowed values and takes them off', async () => {
		const { doc, onchange } = setup({ parameters: { p: { type: 'string', default: '' } } });
		await retype(screen.getByLabelText('Allowed values'), 'a, b');
		expect(doc.parameters.p.enum).toEqual(['a', 'b']);
		await retype(screen.getByLabelText('Allowed values'), '');
		expect(doc.parameters.p.enum).toBeUndefined();
		expect(onchange).toHaveBeenCalledTimes(2);
	});

	it('writes the description and takes it off', async () => {
		const { doc } = setup({ parameters: { p: { type: 'string', default: '' } } });
		await retype(screen.getByLabelText('Description'), 'Where');
		expect(doc.parameters.p.description).toBe('Where');
		await retype(screen.getByLabelText('Description'), '');
		expect(doc.parameters.p.description).toBeUndefined();
	});

	it('leaves the six out of the flow’s own', () => {
		setup({ parameters: { timeout: { default: '5m' } } });
		expect(screen.queryByRole('textbox', { name: 'Name' })).not.toBeInTheDocument();
	});
});
