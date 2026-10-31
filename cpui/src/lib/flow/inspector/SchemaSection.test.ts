import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import {
	aDoc,
	anAgent,
	aSequence,
	aStage,
	fill,
	handlers,
	live,
	retype
} from '$test/fixtures/flow.svelte';
import type { FlowDoc } from '$lib/domain/flowdoc';
import type { SchemaDraft } from './drafts.svelte';
import SchemaSection from './SchemaSection.svelte';

vi.mock('$lib/flow/YamlEditor.svelte', () => import('$test/fixtures/YamlEditorStub.svelte'));

const plan = { name: 'plan', json_schema: { type: 'object' } };

const setup = (over: Partial<FlowDoc> = {}, error = '') => {
	const doc = aDoc({ schemas: [plan, { name: 'taken', json_schema: {} }], ...over });
	const draft = live({ text: 'type: object\n', error }) as unknown as SchemaDraft;
	const h = handlers();
	render(SchemaSection, { doc, schema: doc.schemas![0], draft, ...h });
	return { doc, draft, ...h };
};
const name = () => screen.getByLabelText('Name');
const del = () => screen.getByRole('button', { name: 'Delete…' });

describe('SchemaSection', () => {
	it('renames the schema and every port naming it', async () => {
		const { doc, onchange, onselect } = setup({
			agents: [anAgent('a', { outputs: [{ name: 'out', schema: 'plan' }] })],
			stages: [aStage('s', 'command', { output: { r: { to: 'x', schema: 'plan' } } })]
		});
		await retype(name(), 'blueprint');
		expect(doc.schemas![0].name).toBe('blueprint');
		expect(doc.agents![0].outputs![0].schema).toBe('blueprint');
		expect(doc.stages[0].output!.r.schema).toBe('blueprint');
		expect(onchange).toHaveBeenCalledOnce();
		expect(onselect).toHaveBeenCalledExactlyOnceWith(
			{ kind: 'schema', name: 'blueprint' },
			'rename'
		);
	});

	it.each([
		['a taken name', 'taken'],
		['a bad name', 'Not A Slug'],
		['nothing', '']
	])('refuses %s and puts the name back', async (_, text) => {
		const { doc, onchange, onselect } = setup();
		await retype(name(), text);
		expect(name()).toHaveValue('plan');
		expect(doc.schemas![0].name).toBe('plan');
		expect(onchange).not.toHaveBeenCalled();
		expect(onselect).not.toHaveBeenCalled();
	});

	it('writes the description, and takes an empty one off', async () => {
		const { doc, onchange } = setup();
		const field = screen.getByLabelText('Description');
		await fill(field, 'The plan');
		expect(doc.schemas![0].description).toBe('The plan');
		await fill(field, '');
		expect(doc.schemas![0].description).toBeUndefined();
		expect(onchange).toHaveBeenCalled();
	});

	it('edits the draft, not the document', async () => {
		const { doc, draft } = setup();
		await fill(screen.getByRole('textbox', { name: 'JSON Schema' }), 'type: string');
		expect(draft.text).toBe('type: string');
		expect(doc.schemas![0].json_schema).toEqual({ type: 'object' });
	});

	it('says why the body has not been taken', () => {
		setup({}, 'bad indentation');
		expect(
			screen.getByText('Not saved until it is valid YAML: bad indentation')
		).toBeInTheDocument();
	});

	it('deletes an unused schema', async () => {
		const { ondelete } = setup();
		expect(screen.getByText(/No input or output names it yet/)).toBeInTheDocument();
		await userEvent.click(del());
		expect(ondelete).toHaveBeenCalledExactlyOnceWith('plan');
	});

	it('will not delete a schema a port names, and says so', () => {
		setup({ agents: [anAgent('a', { inputs: [{ name: 'in', schema: 'plan' }] })] });
		expect(del()).toBeDisabled();
		expect(del()).toHaveAttribute('title', 'Ports name it — clear them first');
	});

	it('opens the ports naming it, wherever they are', async () => {
		const { onopen } = setup({
			agents: [anAgent('a', { inputs: [{ name: 'in', schema: 'plan' }] })],
			stages: [aStage('s', 'command', { input: { p: { from: 'x', schema: 'plan' } } })],
			sequences: [
				aSequence('q', [aStage('t', 'command', { output: { o: { to: 'y', schema: 'plan' } } })])
			]
		});
		await userEvent.click(screen.getByRole('button', { name: 'a.in' }));
		expect(onopen).toHaveBeenLastCalledWith({ kind: 'agent', name: 'a' });
		await userEvent.click(screen.getByRole('button', { name: 's.p' }));
		expect(onopen).toHaveBeenLastCalledWith({ kind: 'stage', id: 's', sequence: undefined });
		await userEvent.click(screen.getByRole('button', { name: 't.o' }));
		expect(onopen).toHaveBeenLastCalledWith({ kind: 'stage', id: 't', sequence: 'q' });
	});
});
