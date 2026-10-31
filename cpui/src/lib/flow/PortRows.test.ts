import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { aStage, fill, handlers, live, pick } from '$test/fixtures/flow.svelte';
import type { DocStage, IODecl } from '$lib/domain/flowdoc';
import PortRows from './PortRows.svelte';

type Props = Partial<Omit<Parameters<typeof PortRows>[1], 'stage' | 'onchange'>>;

const setup = (over: Partial<DocStage> = {}, props: Props = {}) => {
	const stage = live(aStage('s', 'command', over));
	const { onchange } = handlers();
	render(PortRows, { stage, field: 'input', onchange, ...props });
	return { stage, onchange };
};
const output = (over: Partial<DocStage> = {}, props: Props = {}) =>
	setup(over, { field: 'output', ...props });

const addRow = () => userEvent.click(screen.getByRole('button', { name: /Add/ }));
const name = (i = 0) => screen.getAllByRole('textbox', { name: 'Name' })[i];
const from = (i = 0) => screen.getAllByRole('combobox', { name: /^From/ })[i];
const type = (i = 0) => screen.getAllByRole('combobox', { name: 'Type' })[i];
const optional = (i = 0) => screen.getAllByRole('switch', { name: 'optional' })[i];

const contract: IODecl[] = [
	{ name: 'ticket', type: 'ticket', description: 'What to do.' },
	{ name: 'notes', type: 'file', optional: true }
];

describe('PortRows', () => {
	describe('free rows', () => {
		it('reaches the document only with a name and a value', async () => {
			const { stage, onchange } = setup();
			await addRow();
			await fill(name(), 'plan');
			expect(stage.input).toBeUndefined();

			await fill(from(), 'artifacts/plan.md');
			expect(stage.input).toEqual({ plan: { from: 'artifacts/plan.md', type: 'file' } });
			expect(onchange).toHaveBeenCalled();
		});

		it('writes an output’s path to `to`', async () => {
			const { stage } = output();
			await addRow();
			await fill(name(), 'report');
			await fill(screen.getByLabelText(/^To/), 'artifacts/report.md');
			expect(stage.output).toEqual({ report: { to: 'artifacts/report.md', type: 'file' } });
		});

		it('renames a row in the document', async () => {
			const { stage } = setup({ input: { a: { from: 'x', type: 'file' } } });
			await fill(name(), 'b');
			expect(stage.input).toEqual({ b: { from: 'x', type: 'file' } });
		});

		it('removes a row, and the field with its last', async () => {
			const { stage, onchange } = setup({ input: { a: { from: 'x' } } });
			await userEvent.click(screen.getByRole('button', { name: 'Remove' }));
			expect(stage.input).toBeUndefined();
			expect(onchange).toHaveBeenCalledOnce();
			expect(screen.queryByRole('textbox', { name: 'Name' })).not.toBeInTheDocument();
		});

		it('marks a name given twice, and keeps the first', async () => {
			const { stage } = setup({
				input: { a: { from: 'x' }, b: { from: 'y' } }
			});
			await fill(name(1), 'a');
			expect(name(0)).toHaveClass('invalid');
			expect(name(1)).toHaveClass('invalid');
			expect(stage.input).toEqual({ a: { from: 'x' } });
		});

		it('comes up with the same rows the document has, in order', () => {
			setup({ input: { b: { from: 'y' }, a: { from: 'x' } } });
			expect(name(0)).toHaveValue('b');
			expect(name(1)).toHaveValue('a');
			expect(from(1)).toHaveValue('x');
		});
	});

	describe('contract rows', () => {
		it('names a row by the contract and says what it wants', () => {
			setup({}, { declared: contract });
			expect(screen.getByText('ticket')).toBeInTheDocument();
			expect(screen.getByText('What to do.')).toBeInTheDocument();
			expect(screen.queryByRole('textbox', { name: 'Name' })).not.toBeInTheDocument();
			expect(screen.queryByRole('button', { name: /Add/ })).not.toBeInTheDocument();
		});

		it('marks what is required and what is optional', () => {
			setup({}, { declared: contract });
			expect(screen.getAllByText('required')).toHaveLength(1);
		});

		it('shows the conventional path as the placeholder', () => {
			setup({}, { declared: [{ name: 'msg', defaultPath: 'artifacts/msg.txt' }] });
			expect(from()).toHaveAttribute('placeholder', 'artifacts/msg.txt');
		});

		it('writes the contract’s rows first, in declaration order', async () => {
			const { stage } = setup(
				{ input: { extra: { from: 'e' }, notes: { from: 'n' } } },
				{ declared: contract }
			);
			await fill(from(0), 'run.ticket');
			expect(Object.keys(stage.input!)).toEqual(['ticket', 'notes', 'extra']);
		});

		it('takes the contract’s type for a path', async () => {
			const { stage } = setup({}, { declared: [{ name: 'plan', type: 'review_threads' }] });
			await fill(from(), 'artifacts/r.json');
			expect(stage.input?.plan.type).toBe('review_threads');
		});

		it('takes the contract’s schema, which fixes no type', async () => {
			const { stage } = setup({}, { declared: [{ name: 'plan', schema: 'blueprint' }] });
			await fill(from(), 'artifacts/plan.json');
			expect(stage.input?.plan).toEqual({ from: 'artifacts/plan.json', schema: 'blueprint' });
		});
	});

	describe('a value’s type', () => {
		it('is the engine’s for a value it provides, and cannot be changed', async () => {
			const { stage } = setup({ input: { a: { from: 'x', type: 'file' } } });
			await fill(from(), 'run.git.diff_target');
			expect(stage.input?.a).toEqual({ from: 'run.git.diff_target', type: 'changeset' });
			expect(type()).toBeDisabled();
		});

		it('is a link for a running service’s address', async () => {
			const { stage } = setup({ input: { a: { from: 'x', type: 'file' } } });
			await fill(from(), 'run.service.$app');
			expect(stage.input?.a.type).toBe('link');
		});

		it('drops the schema on a value the engine provides', async () => {
			const { stage } = setup({ input: { a: { from: 'x', schema: 'plan' } } });
			await fill(from(), 'run.ticket');
			expect(stage.input?.a.schema).toBeUndefined();
		});

		it('makes an edge’s feed optional: it is empty unless an edge brings it', async () => {
			const { stage } = setup({ input: { a: { from: 'x', type: 'file' } } });
			await fill(from(), 'edge.verdict');
			expect(stage.input?.a).toEqual({ from: 'edge.verdict', type: 'verdict', optional: true });
			expect(screen.getByText(/Filled by whichever edge brings the run here/)).toBeInTheDocument();
		});

		it('takes the type of the stage writing the path', async () => {
			const { stage } = setup(
				{ input: { a: { from: 'x', type: 'file' } } },
				{ writtenTypes: { 'artifacts/r.json': 'review_threads' } }
			);
			await fill(from(), 'artifacts/r.json');
			expect(stage.input?.a.type).toBe('review_threads');
		});

		it('keeps a type chosen on purpose over the writer’s', async () => {
			const { stage } = setup(
				{ input: { a: { from: 'x', type: 'verdict' } } },
				{ writtenTypes: { 'artifacts/r.json': 'review_threads' } }
			);
			await fill(from(), 'artifacts/r.json');
			expect(stage.input?.a.type).toBe('verdict');
		});

		it('leaves the engine’s type behind on a path', async () => {
			const { stage } = setup({ input: { a: { from: 'run.ticket', type: 'ticket' } } });
			await fill(from(), 'artifacts/t.md');
			expect(stage.input?.a.type).toBe('file');
		});

		it('is files for a path ending in /', async () => {
			const { stage } = setup({ input: { a: { from: 'run.ticket', type: 'ticket' } } });
			await fireEvent.input(from(), { target: { value: 'artifacts/shots/' } });
			expect(stage.input?.a.type).toBe('files');
		});
	});

	describe('a chosen type', () => {
		it('writes it', async () => {
			const { stage, onchange } = setup({ input: { a: { from: 'x', type: 'file' } } });
			await pick('Type', 'verdict');
			expect(stage.input?.a.type).toBe('verdict');
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('takes it off', async () => {
			const { stage } = setup({ input: { a: { from: 'x', type: 'file' } } });
			await pick('Type', 'no type');
			expect(stage.input?.a.type).toBeUndefined();
		});

		it('drops a schema the new type takes none of', async () => {
			const { stage } = setup(
				{ input: { a: { from: 'x', type: 'verdict', schema: 'plan' } } },
				{ schemas: ['plan'] }
			);
			await pick('Type', 'file');
			expect(stage.input?.a.schema).toBeUndefined();
		});

		it('keeps to the types it is given', async () => {
			setup({ input: { a: { from: 'x', type: 'odd' } } }, { types: ['file', 'files'] });
			await userEvent.click(type());
			const labels = screen
				.getAllByRole('option')
				.map((o) => o.textContent?.replace('✓', '').trim());
			expect(labels).toEqual(['no type', 'file', 'files', 'odd']);
		});

		it('offers an output no engine values', async () => {
			output({ output: { a: { to: 'x', type: 'file' } } });
			await userEvent.click(type());
			expect(screen.queryByRole('option', { name: 'ticket' })).not.toBeInTheDocument();
		});
	});

	describe('a schema', () => {
		const schemas = ['plan', 'review'];

		it('writes it', async () => {
			const { stage } = setup({ input: { a: { from: 'x' } } }, { schemas });
			await pick('Schema', 'review');
			expect(stage.input?.a.schema).toBe('review');
		});

		it('takes it off', async () => {
			const { stage } = setup({ input: { a: { from: 'x', schema: 'plan' } } }, { schemas });
			await pick('Schema', 'no schema');
			expect(stage.input?.a.schema).toBeUndefined();
		});

		it.each([['file'], ['files']])('is not offered on %s', (t) => {
			setup({ input: { a: { from: 'x', type: t } } }, { schemas });
			expect(screen.queryByRole('combobox', { name: 'Schema' })).not.toBeInTheDocument();
		});

		it('is not offered on a value the engine provides', () => {
			setup({ input: { a: { from: 'run.ticket', type: 'ticket' } } }, { schemas });
			expect(screen.queryByRole('combobox', { name: 'Schema' })).not.toBeInTheDocument();
		});

		it('is not offered when the flow has none', () => {
			setup({ input: { a: { from: 'x' } } });
			expect(screen.queryByRole('combobox', { name: 'Schema' })).not.toBeInTheDocument();
		});

		it('keeps one the flow no longer has, so it can be cleared', () => {
			setup({ input: { a: { from: 'x', schema: 'gone' } } });
			expect(screen.getByRole('combobox', { name: 'Schema' })).toHaveTextContent('gone');
		});
	});

	describe('a directory', () => {
		it('warns of a files port without the / ', () => {
			setup({ input: { a: { from: 'artifacts/shots', type: 'files' } } });
			expect(screen.getByText('files is a directory: its path ends in “/”.')).toBeInTheDocument();
		});

		it('warns of a / on what is no directory', () => {
			setup({ input: { a: { from: 'artifacts/shots/', type: 'file' } } });
			expect(
				screen.getByText('A path ending in “/” is a directory — its type is files.')
			).toBeInTheDocument();
		});

		it('says nothing of a directory that is one', () => {
			setup({ input: { a: { from: 'artifacts/shots/', type: 'files' } } });
			expect(screen.queryByText(/directory/)).not.toBeInTheDocument();
		});

		it('says nothing of a value the engine provides', () => {
			setup({ input: { a: { from: 'run.ticket', type: 'ticket' } } });
			expect(screen.queryByText(/directory/)).not.toBeInTheDocument();
		});
	});

	describe('optional', () => {
		it('writes it only while it is on', async () => {
			const { stage } = setup({ input: { a: { from: 'x' } } });
			await userEvent.click(optional());
			expect(stage.input?.a.optional).toBe(true);
			await userEvent.click(optional());
			expect(stage.input?.a.optional).toBeUndefined();
		});

		it('is an input’s only', () => {
			output({ output: { a: { to: 'x' } } });
			expect(screen.queryByRole('switch')).not.toBeInTheDocument();
		});
	});

	describe('what a row may read', () => {
		const offered = (id: string) =>
			[...document.querySelectorAll(`datalist#${id} option`)].map((o) => o.getAttribute('value'));

		it('suggests the engine’s sources and the flow’s paths', () => {
			setup(
				{ input: { a: { from: 'x' } } },
				{ sources: ['run.ticket'], paths: ['artifacts/plan.md'] }
			);
			expect(offered('ports-s-input')).toEqual(['run.ticket', 'artifacts/plan.md']);
			expect(from()).toHaveAttribute('list', 'ports-s-input');
		});

		const twoLinks = {
			a: { from: 'run.workspace' },
			b: { from: 'run.mr.url' },
			c: { from: 'artifacts/plan.md' }
		};

		it('refuses the links past its budget, the first keeping theirs', () => {
			setup({ input: twoLinks }, { links: 1 });
			expect(from(0)).not.toHaveClass('invalid');
			expect(from(1)).toHaveClass('invalid');
			expect(screen.getAllByText(/A gate opens at most one link/)).toHaveLength(1);
		});

		it('offers paths only once the budget is spent', () => {
			setup({ input: twoLinks }, { links: 1, sources: ['run.ticket'], paths: ['p'] });
			expect(from(0)).toHaveAttribute('list', 'ports-s-input');
			expect(from(2)).toHaveAttribute('list', 'ports-s-input-paths');
			expect(offered('ports-s-input-paths')).toEqual(['p']);
		});

		it('sets no budget unless told', () => {
			setup({ input: twoLinks });
			expect(screen.queryByText(/opens at most/)).not.toBeInTheDocument();
		});
	});
});
