import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import {
	aDoc,
	anAgent,
	aSequence,
	aServer,
	aStage,
	aWatcher,
	fill,
	handlers,
	MAIN_PHASE,
	pick
} from '$test/fixtures/flow.svelte';
import type { FlowDoc } from '$lib/domain/flowdoc';
import FlowSection from './FlowSection.svelte';

const setup = (over: Partial<FlowDoc> = {}) => {
	const doc = aDoc(over);
	const { onchange, onopen } = handlers();
	render(FlowSection, { doc, onchange, onopen });
	return { doc, onchange, onopen };
};
const button = (name: string | RegExp) => screen.getByRole('button', { name });

describe('FlowSection', () => {
	it('writes the description, and takes an empty one off', async () => {
		const { doc, onchange } = setup();
		const field = screen.getByLabelText('Description');
		await fill(field, 'Builds things');
		expect(doc.description).toBe('Builds things');
		await fill(field, '');
		expect(doc.description).toBeUndefined();
		expect(onchange).toHaveBeenCalled();
	});

	it('writes what runs are started on', async () => {
		const { doc, onchange } = setup();
		await pick('Operates on', 'a merge request someone opened');
		expect(doc.operates_on).toBe('merge_request');
		expect(onchange).toHaveBeenCalledOnce();
	});

	it('writes nothing for a ticket, the default', async () => {
		const { doc } = setup({ operates_on: 'merge_request' });
		await pick('Operates on', 'a ticket');
		expect(doc.operates_on).toBeUndefined();
	});

	describe('the Ask pane', () => {
		const ask = () => screen.getByLabelText('Ask pane');

		it('turns on with a harness', async () => {
			const { doc, onchange } = setup();
			await fill(ask(), ' $harness ');
			expect(doc.ask).toEqual({ harness: '$harness' });
			expect(onchange).toHaveBeenCalled();
		});

		it('turns off when emptied', async () => {
			const { doc } = setup({ ask: { harness: 'omp' } });
			expect(ask()).toHaveValue('omp');
			await fill(ask(), '');
			expect(doc.ask).toBeUndefined();
		});

		it('reads its suggestions from the inspector’s parameters list', () => {
			setup();
			expect(ask()).toHaveAttribute('list', 'flow-params');
		});
	});

	it('edits the parameters', async () => {
		const { doc, onchange } = setup();
		await userEvent.click(button('+ parameter'));
		expect(doc.parameters.param_1).toBeDefined();
		expect(onchange).toHaveBeenCalledOnce();
	});

	describe('what it carries', () => {
		const full = {
			schemas: [{ name: 'plan', json_schema: {} }],
			mcp_servers: [aServer('tools')],
			agents: [anAgent('planner')],
			sequences: [aSequence('lint')]
		};

		it.each([
			['plan', { kind: 'schema', name: 'plan' }],
			['tools', { kind: 'mcp', name: 'tools' }],
			['planner', { kind: 'agent', name: 'planner' }],
			['lint', { kind: 'sequence', id: 'lint' }]
		])('opens %s', async (name, sel) => {
			const { onopen } = setup(full);
			await userEvent.click(button(name));
			expect(onopen).toHaveBeenCalledExactlyOnceWith(sel);
		});

		it('adds a schema and opens it', async () => {
			const { doc, onchange, onopen } = setup();
			await userEvent.click(button(/schema$/));
			expect(doc.schemas?.[0].name).toBe('schema_1');
			expect(onchange).toHaveBeenCalledOnce();
			expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'schema', name: 'schema_1' });
		});

		it('adds an MCP server and opens it', async () => {
			const { doc, onopen } = setup();
			await userEvent.click(button(/server$/));
			expect(doc.mcp_servers?.[0].name).toBe('browser');
			expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'mcp', name: 'browser' });
		});

		it('adds an agent and opens it', async () => {
			const { doc, onopen } = setup();
			await userEvent.click(button(/^\S+ agent$/));
			expect(doc.agents?.[0].name).toBe('agent_1');
			expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'agent', name: 'agent_1' });
		});

		it('adds a sequence and opens it', async () => {
			const { doc, onopen } = setup();
			await userEvent.click(button(/^\S+ sequence$/));
			const id = doc.sequences?.[0].id;
			expect(id).toBeTruthy();
			expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'sequence', id });
		});
	});

	describe('its chains', () => {
		it('adds a watcher and opens it', async () => {
			const { doc, onchange, onopen } = setup();
			await userEvent.click(button('watcher'));
			const ph = doc.phases?.at(-1);
			expect(ph?.kind).toBe('watcher');
			expect(onchange).toHaveBeenCalledOnce();
			expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'phase', id: ph?.id });
		});

		it('adds an outcome and opens it', async () => {
			const { doc, onopen } = setup();
			await userEvent.click(button('outcome'));
			const ph = doc.phases?.at(-1);
			expect(ph?.kind).toBe('outcome');
			expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'phase', id: ph?.id });
		});

		it('offers no second main chain', () => {
			setup();
			expect(screen.queryByRole('button', { name: 'main chain' })).not.toBeInTheDocument();
		});

		it('gives a flow that only watches a main chain', async () => {
			const { doc, onchange, onopen } = setup({
				phases: [aWatcher('w')],
				stages: [aStage('s', 'command', { phase: 'w' })]
			});
			await userEvent.click(button('main chain'));
			expect(doc.phases?.[0].kind).toBe('main');
			expect(onchange).toHaveBeenCalledOnce();
			expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'phase', id: 'main' });
		});
	});

	describe('adding a stage', () => {
		it('adds to the main chain', async () => {
			const { doc, onchange, onopen } = setup({ phases: [aWatcher('w'), { ...MAIN_PHASE }] });
			expect(screen.getByText('Add a stage to the main chain')).toBeInTheDocument();
			await userEvent.click(button('wait'));
			const added = doc.stages.find((s) => s.type === 'wait')!;
			expect(added.phase).toBe('main');
			expect(onchange).toHaveBeenCalledOnce();
			expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'stage', id: added.id });
		});

		it('adds to the first chain there is, when there is no main', async () => {
			const { doc } = setup({ phases: [aWatcher('w')], stages: [] });
			expect(screen.getByText('Add a stage to w')).toBeInTheDocument();
			await userEvent.click(button('command'));
			expect(doc.stages[0].phase).toBe('w');
		});

		it('offers nothing with no chain to add to', () => {
			setup({ phases: [], stages: [] });
			expect(screen.queryByRole('button', { name: 'command' })).not.toBeInTheDocument();
		});
	});
});
