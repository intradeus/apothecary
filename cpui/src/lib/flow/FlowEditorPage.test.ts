import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import type { Flow, FlowValidation } from '$lib/domain';
import type { FlowDoc } from '$lib/domain/flowdoc';
import { confirmState } from '$lib/ui/confirm.svelte';
import {
	aFlow,
	anAgent,
	answer,
	aSequence,
	aStage,
	aVersion,
	fill,
	MAIN_PHASE,
	pick
} from '$test/fixtures/flow.svelte';
import { deferred } from '$test/helpers';
import FlowEditorPage from './FlowEditorPage.svelte';

vi.mock('@xyflow/svelte', () => import('$test/fixtures/xyflow.svelte'));
vi.mock('$lib/flow/YamlEditor.svelte', () => import('$test/fixtures/YamlEditorStub.svelte'));
vi.mock('$lib/api', () => ({
	api: {
		flows: {
			get: vi.fn(),
			versions: vi.fn(),
			validate: vi.fn(),
			schema: vi.fn(),
			save: vi.fn(),
			version: vi.fn()
		},
		system: { actions: vi.fn(), signals: vi.fn() }
	}
}));

const plainDoc = (over: Partial<FlowDoc> = {}): FlowDoc => ({
	name: 'builder',
	parameters: {},
	stages: [aStage('build', 'command', { on_success: 'done' })],
	phases: [{ ...MAIN_PHASE }],
	...over
});
const validated = (
	doc: FlowDoc | undefined,
	over: Partial<FlowValidation> = {}
): FlowValidation => ({
	ok: true,
	errors: [],
	warnings: [],
	doc,
	stages: doc?.stages.length,
	parameters: [],
	refs: [],
	phases: [],
	...over
});

async function setup(doc: FlowDoc = plainDoc(), flow: Partial<Flow> = {}) {
	vi.mocked(api.flows.get).mockResolvedValue(aFlow(flow));
	vi.mocked(api.flows.validate).mockImplementation(async () => validated(structuredClone(doc)));
	render(FlowEditorPage, { projectId: 'p1', name: 'builder' });
	await screen.findByText('nothing selected');
	return doc;
}

const inspector = () => screen.getByRole('complementary');
const clickNode = (id: string) =>
	userEvent.click(screen.getByRole('button', { name: `node ${id}` }));
const backspace = () => userEvent.keyboard('{Backspace}');
const nodeIds = () => screen.getAllByTestId('node').map((n) => n.dataset.node);
const blur = () => (document.activeElement as HTMLElement | null)?.blur();

describe('FlowEditorPage', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		fleet.projects = [];
		vi.mocked(api.flows.versions).mockResolvedValue([]);
		vi.mocked(api.flows.schema).mockResolvedValue({ type: 'object' });
		vi.mocked(api.system.actions).mockResolvedValue([]);
		vi.mocked(api.system.signals).mockResolvedValue([]);
	});
	afterEach(() => confirmState.settle(false));

	describe('loading', () => {
		it('holds a skeleton until the flow is in', () => {
			vi.mocked(api.flows.get).mockReturnValue(deferred<Flow>().promise);
			render(FlowEditorPage, { projectId: 'p1', name: 'builder' });
			expect(document.querySelector('[aria-busy="true"]')).toBeInTheDocument();
			expect(screen.queryByTestId('node')).not.toBeInTheDocument();
		});

		it('offers a retry when it cannot load, and recovers', async () => {
			vi.mocked(api.flows.get).mockRejectedValueOnce(new Error('engine down'));
			vi.mocked(api.flows.validate).mockResolvedValue(validated(plainDoc()));
			render(FlowEditorPage, { projectId: 'p1', name: 'builder' });
			expect(await screen.findByRole('alert')).toHaveTextContent('Something went wrong');
			expect(screen.getByText('engine down')).toBeInTheDocument();

			vi.mocked(api.flows.get).mockResolvedValue(aFlow());
			await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
			await waitFor(() => expect(screen.getAllByTestId('node').length).toBeGreaterThan(0));
			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});

		it('shows the graph beside the flow’s panel', async () => {
			await setup();
			expect(nodeIds()).toContain('build');
			expect(inspector()).toHaveTextContent('nothing selected');
			expect(api.flows.get).toHaveBeenCalledExactlyOnceWith('p1', 'builder');
		});

		it('says a template is one', async () => {
			await setup(plainDoc(), { builtin: true });
			expect(screen.getByText('Built-in template.')).toBeInTheDocument();
		});

		it('opens on the YAML of a flow that does not parse', async () => {
			vi.mocked(api.flows.get).mockResolvedValue(aFlow({ yaml: 'nope: [' }));
			vi.mocked(api.flows.validate).mockResolvedValue(
				validated(undefined, { ok: false, errors: ['flow:1: bad'] })
			);
			render(FlowEditorPage, { projectId: 'p1', name: 'builder' });
			expect(await screen.findByRole('textbox', { name: 'Flow YAML' })).toHaveValue('nope: [');
		});
	});

	describe('selecting', () => {
		it('opens a stage’s panel from the canvas', async () => {
			await setup();
			await clickNode('build');
			await waitFor(() => expect(inspector()).toHaveTextContent('Run if'));
		});

		it('goes back to the flow on the bare canvas', async () => {
			await setup();
			await clickNode('build');
			await waitFor(() => expect(inspector()).toHaveTextContent('Run if'));
			await userEvent.click(screen.getByRole('button', { name: 'pane' }));
			await waitFor(() => expect(inspector()).not.toHaveTextContent('Run if'));
		});

		it('leaves a trail from panel to panel', async () => {
			await setup(plainDoc({ schemas: [{ name: 'plan', json_schema: {} }] }));
			await userEvent.click(screen.getByRole('button', { name: 'plan' }));
			await waitFor(() => expect(inspector()).toHaveTextContent('Named by'));
			await userEvent.click(screen.getByRole('button', { name: /The formula/ }));
			await waitFor(() => expect(inspector()).not.toHaveTextContent('Named by'));
		});
	});

	describe('an edit', () => {
		it('marks the flow unsaved, and redraws the canvas', async () => {
			await setup();
			await clickNode('build');
			const add = await screen.findByLabelText('Id');
			await fill(add, 'compile');
			await userEvent.tab();
			expect(await screen.findByText('Unsaved changes')).toBeInTheDocument();
			await waitFor(() => expect(nodeIds()).toContain('compile'));
		});
	});

	describe('Delete and Backspace', () => {
		it('delete the selected stage, after asking', async () => {
			await setup(
				plainDoc({ stages: [aStage('build', 'command', { on_success: 'ship' }), aStage('ship')] })
			);
			await clickNode('ship');
			await waitFor(() => expect(inspector()).toHaveTextContent('Run if'));
			blur();
			await backspace();
			expect((await answer(true)).title).toBe('Delete stage ship?');
			await waitFor(() => expect(screen.getByText('Unsaved changes')).toBeInTheDocument());
			await waitFor(() => expect(inspector()).not.toHaveTextContent('Run if'));
		});

		it('delete the selected schema', async () => {
			await setup(plainDoc({ schemas: [{ name: 'plan', json_schema: {} }] }));
			await userEvent.click(screen.getByRole('button', { name: 'plan' }));
			await waitFor(() => expect(inspector()).toHaveTextContent('Named by'));
			blur();
			await userEvent.keyboard('{Delete}');
			expect((await answer(true)).title).toBe('Delete schema plan?');
			await waitFor(() =>
				expect(screen.queryByRole('button', { name: 'plan' })).not.toBeInTheDocument()
			);
		});

		it('refuse a schema a port names, asking nothing', async () => {
			await setup(
				plainDoc({
					schemas: [{ name: 'plan', json_schema: {} }],
					agents: [anAgent('a', { outputs: [{ name: 'o', schema: 'plan' }] })]
				})
			);
			await userEvent.click(screen.getByRole('button', { name: 'plan' }));
			await waitFor(() => expect(inspector()).toHaveTextContent('Named by'));
			blur();
			await backspace();
			await Promise.resolve();
			expect(confirmState.current).toBeNull();
			expect(inspector()).toHaveTextContent('Named by');
			expect(screen.queryByText('Unsaved changes')).not.toBeInTheDocument();
		});

		it('leave a selection alone while a field is being typed in', async () => {
			await setup();
			await clickNode('build');
			const field = await screen.findByLabelText('Description');
			await userEvent.click(field);
			await backspace();
			expect(confirmState.current).toBeNull();
		});

		it('leave a selection alone in the schema’s editor', async () => {
			await setup(plainDoc({ schemas: [{ name: 'plan', json_schema: {} }] }));
			await userEvent.click(screen.getByRole('button', { name: 'plan' }));
			await userEvent.click(await screen.findByRole('textbox', { name: 'JSON Schema' }));
			await backspace();
			expect(confirmState.current).toBeNull();
		});

		it('do nothing with nothing selected', async () => {
			await setup();
			blur();
			await backspace();
			expect(confirmState.current).toBeNull();
		});

		it('do nothing off the graph tab', async () => {
			await setup(plainDoc({ agents: [anAgent('planner')] }));
			await clickNode('build');
			await userEvent.click(screen.getByRole('radio', { name: 'Agents' }));
			blur();
			await backspace();
			expect(confirmState.current).toBeNull();
		});

		// The app's dropdown is a button: Backspace on one, after the type-ahead
		// say, must not reach the stage selected behind it.
		it('leave a selection alone on a dropdown', async () => {
			await setup();
			await clickNode('build');
			const field = await screen.findByRole('combobox', { name: 'Run if' });
			field.focus();
			await backspace();
			expect(confirmState.current).toBeNull();
		});
	});

	// Esc on a dropdown is the dropdown's, as Backspace is.
	it('keeps the panel open when Esc lands on a dropdown', async () => {
		await setup();
		await clickNode('build');
		const field = await screen.findByRole('combobox', { name: 'Run if' });
		field.focus();
		await userEvent.keyboard('{Escape}');
		expect(screen.getByRole('combobox', { name: 'Run if' })).toBeInTheDocument();
	});

	describe('saving', () => {
		const dirty = async () => {
			await clickNode('build');
			await fill(await screen.findByLabelText('Description'), 'x');
			await screen.findByText('Unsaved changes');
		};

		it.each([
			['⌘S', { metaKey: true }],
			['Ctrl+S', { ctrlKey: true }]
		])('saves on %s, holding back the browser’s own', async (_, mod) => {
			await setup();
			await dirty();
			vi.mocked(api.flows.save).mockResolvedValue(aFlow({ hash: 'ffff0000' }));
			const notPrevented = await fireEvent.keyDown(window, { key: 's', ...mod });
			expect(notPrevented).toBe(false);
			await waitFor(() => expect(api.flows.save).toHaveBeenCalledOnce());
			expect(vi.mocked(api.flows.save).mock.calls[0][1]).toBe('builder');
			await waitFor(() => expect(screen.queryByText('Unsaved changes')).not.toBeInTheDocument());
		});

		it('saves nothing with nothing changed', async () => {
			await setup();
			await fireEvent.keyDown(window, { key: 's', metaKey: true });
			expect(api.flows.save).not.toHaveBeenCalled();
		});
	});

	describe('a sequence beside the flow', () => {
		const doc = () =>
			plainDoc({
				sequences: [aSequence('lint', [aStage('fmt', 'command', { phase: undefined })])],
				stages: [aStage('run', 'sequence', { sequence: 'lint', on_success: 'done' })]
			});
		const pane = () => screen.findByRole('region', { name: 'Sequence lint' });

		it('opens from its stage', async () => {
			await setup(doc());
			await clickNode('run');
			expect(await pane()).toBeInTheDocument();
		});

		it('closes on Esc', async () => {
			await setup(doc());
			await clickNode('run');
			await pane();
			blur();
			await userEvent.keyboard('{Escape}');
			await waitFor(() =>
				expect(screen.queryByRole('region', { name: 'Sequence lint' })).not.toBeInTheDocument()
			);
		});

		it('stays on an Esc pressed in a field', async () => {
			await setup(doc());
			await clickNode('run');
			await pane();
			await userEvent.click(await screen.findByLabelText('Description'));
			await userEvent.keyboard('{Escape}');
			expect(await pane()).toBeInTheDocument();
		});
	});

	describe('its tabs', () => {
		it('shows the agents, and opens a stage back on the graph', async () => {
			await setup(
				plainDoc({
					agents: [anAgent('planner')],
					stages: [aStage('plan', 'agent', { agent: 'planner' })]
				})
			);
			await userEvent.click(screen.getByRole('radio', { name: 'Agents' }));
			expect(
				await screen.findByRole('navigation', { name: 'The flow’s agents' })
			).toBeInTheDocument();
			expect(screen.queryByRole('complementary')).not.toBeInTheDocument();

			await userEvent.click(screen.getByRole('button', { name: 'plan' }));
			await waitFor(() => expect(inspector()).toHaveTextContent('Run if'));
		});

		it('opens an agent from a stage on its tab', async () => {
			await setup(
				plainDoc({
					agents: [anAgent('planner')],
					stages: [aStage('plan', 'agent', { agent: 'planner' })]
				})
			);
			await clickNode('plan');
			await userEvent.click(await screen.findByRole('button', { name: 'Edit agent planner' }));
			expect(
				await screen.findByRole('navigation', { name: 'The flow’s agents' })
			).toBeInTheDocument();
		});

		it('gives the YAML editor its text, its schema and the parser', async () => {
			await setup();
			await userEvent.click(screen.getByRole('radio', { name: 'YAML' }));
			const editor = await screen.findByRole('textbox', { name: 'Flow YAML' });
			expect(editor).toHaveValue('name: builder\n');
			expect(editor).toHaveAttribute('data-check', 'yes');
			await waitFor(() => expect(editor).toHaveAttribute('data-schema', 'loaded'));
		});

		it('re-reads the document leaving the YAML', async () => {
			await setup();
			await userEvent.click(screen.getByRole('radio', { name: 'YAML' }));
			await screen.findByRole('textbox', { name: 'Flow YAML' });
			await userEvent.click(screen.getByRole('radio', { name: 'Graph' }));
			await waitFor(() => expect(nodeIds()).toContain('build'));
		});
	});

	describe('its side column', () => {
		it('swaps the inspector for the versions and back', async () => {
			vi.mocked(api.flows.versions).mockResolvedValue([aVersion('abcdef0123456789')]);
			await setup();
			await userEvent.click(screen.getByRole('button', { name: /Versions/ }));
			expect(await screen.findByRole('button', { name: 'Close versions' })).toBeInTheDocument();
			expect(inspector()).not.toHaveTextContent('nothing selected');

			await userEvent.click(screen.getByRole('button', { name: 'Close versions' }));
			await waitFor(() => expect(inspector()).toHaveTextContent('nothing selected'));
		});
	});

	describe('cloning', () => {
		it('asks for a name, refusing the flow’s own', async () => {
			await setup();
			await userEvent.click(screen.getByRole('button', { name: 'Clone' }));
			const dialog = await screen.findByRole('dialog', { name: 'Clone builder' });
			expect(dialog).toBeInTheDocument();
			expect(screen.getByLabelText('Name')).toHaveValue('builder-copy');
			await fill(screen.getByLabelText('Name'), 'builder');
			expect(screen.getByRole('button', { name: 'Clone & open' })).toBeDisabled();
		});

		it('closes on Cancel', async () => {
			await setup();
			await userEvent.click(screen.getByRole('button', { name: 'Clone' }));
			await screen.findByRole('dialog');
			await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
			await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
		});
	});

	it('shows a notice until it is dismissed', async () => {
		const version = aVersion('0011223344556677');
		vi.mocked(api.flows.versions).mockResolvedValue([version]);
		vi.mocked(api.flows.version).mockResolvedValue(version as never);
		await setup();
		await userEvent.click(screen.getByRole('button', { name: /Versions/ }));
		await userEvent.click(await screen.findByRole('button', { name: 'Load into editor' }));
		expect(await screen.findByText(/Loaded @00112233 into the editor/)).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Dismiss/i }));
		await waitFor(() => expect(screen.queryByText(/Loaded @00112233/)).not.toBeInTheDocument());
	});

	it('picks routes through the real panels', async () => {
		await setup(
			plainDoc({ stages: [aStage('a', 'command', { on_success: 'done' }), aStage('b')] })
		);
		await clickNode('a');
		await screen.findByLabelText('Id');
		await pick(/^success:/, 'b');
		await screen.findByText('Unsaved changes');
	});
});
