import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
	aDoc,
	anAgent,
	anOutcome,
	answer,
	aSequence,
	aServer,
	aSignal,
	aStage,
	aWatcher,
	fill,
	handlers,
	MAIN_PHASE
} from '$test/fixtures/flow.svelte';
import type { FlowDoc } from '$lib/domain/flowdoc';
import { confirmState } from '$lib/ui/confirm.svelte';
import type { Selection } from './FlowGraph.svelte';
import FlowInspector from './FlowInspector.svelte';

vi.mock('$lib/flow/YamlEditor.svelte', () => import('$test/fixtures/YamlEditorStub.svelte'));

const plan = () => ({ name: 'plan', json_schema: { type: 'object' } });

function setup(doc: FlowDoc, selection: Selection, more: Record<string, unknown> = {}) {
	const h = handlers();
	const view = render(FlowInspector, { doc, selection, ...h, ...more });
	return { doc, ...h, ...view, inspector: view.component as unknown as FlowInspector };
}

const options = (list: string) =>
	[...document.querySelectorAll(`datalist#${list} option`)].map((o) => o.getAttribute('value'));

/** The list a field suggests from, as the browser finds it. */
const suggestions = (field: HTMLElement) =>
	[...document.getElementById(field.getAttribute('list') ?? '')!.querySelectorAll('option')].map(
		(o) => o.getAttribute('value')
	);

afterEach(() => confirmState.settle(false));

describe('FlowInspector', () => {
	describe('the panel for the selection', () => {
		const full = () =>
			aDoc({
				phases: [{ ...MAIN_PHASE }, aWatcher('w')],
				sequences: [aSequence('lint', [aStage('fmt', 'command', { phase: undefined })])],
				schemas: [plan()],
				mcp_servers: [aServer('tools')]
			});

		it('opens on the flow with nothing selected', () => {
			setup(full(), null);
			expect(screen.getByText('The formula')).toBeInTheDocument();
		});

		it('falls back to the flow when the selection is gone', () => {
			setup(full(), { kind: 'schema', name: 'gone' });
			expect(screen.getByText('The formula')).toBeInTheDocument();
		});

		it.each<[string, Selection, string]>([
			['a stage', { kind: 'stage', id: 'build' }, 'Run if'],
			['a chain', { kind: 'phase', id: 'w' }, 'Runs on'],
			['a sequence', { kind: 'sequence', id: 'lint' }, 'Add a stage to this sequence'],
			['a schema', { kind: 'schema', name: 'plan' }, 'Named by'],
			['a server', { kind: 'mcp', name: 'tools' }, 'Reached as']
		])('shows %s', (_, selection, marker) => {
			setup(full(), selection);
			expect(screen.getByText(marker)).toBeInTheDocument();
			expect(screen.queryByText('The formula')).not.toBeInTheDocument();
		});

		it('shows a stage of a sequence as its sequence’s', () => {
			setup(full(), { kind: 'stage', id: 'fmt', sequence: 'lint' });
			expect(screen.getByText('In sequence')).toBeInTheDocument();
		});

		it('shows an edge on its stage, its route picked out', () => {
			const doc = aDoc({ stages: [aStage('a', 'command', { on_fail: { to: 'debug' } })] });
			setup(doc, { kind: 'edge', stage: 'a', edge: 'on_fail' } as Selection);
			expect(document.querySelector('[data-route="on_fail"]')).toHaveClass('hl');
		});

		it('hands the panels the agents, the actions and the signals', () => {
			setup(
				aDoc({ stages: [aStage('a', 'wait', { for: 'ci' })] }),
				{ kind: 'stage', id: 'a' },
				{
					signals: [aSignal('ci', { description: 'CI says so.' })]
				}
			);
			expect(screen.getByText('CI says so.')).toBeInTheDocument();
		});
	});

	describe('an edit', () => {
		it('lands in the document and reports the change, on every panel', async () => {
			const doc = aDoc();
			const { onchange } = setup(doc, { kind: 'stage', id: 'build' });
			await fill(screen.getByLabelText('Description'), 'x');
			expect(doc.stages[0].description).toBe('x');
			expect(onchange).toHaveBeenCalled();
		});

		it('takes a schema’s body once it parses, and says why not until then', async () => {
			const doc = aDoc({ schemas: [plan()] });
			const { onchange } = setup(doc, { kind: 'schema', name: 'plan' });
			const body = await screen.findByRole('textbox', { name: 'JSON Schema' });
			await waitFor(() => expect(body).toHaveValue('type: object\n'));

			await fill(body, 'type: string');
			await waitFor(() => expect(doc.schemas![0].json_schema).toEqual({ type: 'string' }));
			expect(onchange).toHaveBeenCalled();

			await fireEvent.input(body, { target: { value: 'a: [' } });
			expect(await screen.findByText(/Not saved until it is valid YAML/)).toBeInTheDocument();
			expect(doc.schemas![0].json_schema).toEqual({ type: 'string' });
		});

		it('takes a server’s command a word per line', async () => {
			const doc = aDoc({ mcp_servers: [aServer('tools', { command: ['npx'] })] });
			const { onchange } = setup(doc, { kind: 'mcp', name: 'tools' });
			const field = screen.getByLabelText('Command');
			await waitFor(() => expect(field).toHaveValue('npx'));

			await fill(field, 'npx{Enter}  -y{Enter}{Enter}tools');
			await waitFor(() => expect(doc.mcp_servers![0].command).toEqual(['npx', '-y', 'tools']));
			expect(onchange).toHaveBeenCalled();
		});

		it('takes a server’s env by the rows that have a name', async () => {
			const doc = aDoc({ mcp_servers: [aServer('tools')] });
			setup(doc, { kind: 'mcp', name: 'tools' });
			await userEvent.click(screen.getByRole('button', { name: /variable/ }));
			expect(doc.mcp_servers![0].env).toBeUndefined();

			await fill(screen.getByLabelText('Key'), 'TOKEN');
			await fill(screen.getByLabelText('Value for TOKEN'), 'abc');
			await waitFor(() => expect(doc.mcp_servers![0].env).toEqual({ TOKEN: 'abc' }));
		});
	});

	describe('moving between panels', () => {
		it('opens from a panel by onopen', async () => {
			const onopen = vi.fn();
			setup(aDoc({ agents: [anAgent('planner')] }), null, { onopen });
			await userEvent.click(screen.getByRole('button', { name: 'planner' }));
			expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'agent', name: 'planner' });
		});

		it('opens by onselect when it is given no onopen', async () => {
			const doc = aDoc({ agents: [anAgent('planner')] });
			const h = handlers();
			render(FlowInspector, { doc, selection: null, onchange: h.onchange, onselect: h.onselect });
			await userEvent.click(screen.getByRole('button', { name: 'planner' }));
			expect(h.onselect).toHaveBeenCalledExactlyOnceWith({ kind: 'agent', name: 'planner' });
		});

		it('offers the way back, named for where it leads', async () => {
			const onback = vi.fn();
			setup(
				aDoc(),
				{ kind: 'stage', id: 'build' },
				{
					back: { kind: 'sequence', id: 'lint' },
					onback
				}
			);
			await userEvent.click(screen.getByRole('button', { name: /sequence lint/ }));
			expect(onback).toHaveBeenCalledOnce();
		});

		it('leads back to the flow panel from null', () => {
			setup(aDoc(), { kind: 'stage', id: 'build' }, { back: null, onback: vi.fn() });
			expect(screen.getByRole('button', { name: /The formula/ })).toBeInTheDocument();
		});

		it('offers no way back at the start of the trail', () => {
			setup(aDoc(), { kind: 'stage', id: 'build' }, { onback: vi.fn() });
			expect(screen.queryByTitle(/^Back to/)).not.toBeInTheDocument();
		});
	});

	describe('deleteSelection', () => {
		describe('a schema', () => {
			it('asks, then removes it and goes back to the flow', async () => {
				const doc = aDoc({ schemas: [plan()] });
				const { inspector, onchange, onselect } = setup(doc, { kind: 'schema', name: 'plan' });

				inspector.deleteSelection();
				const asked = await answer(true);

				expect(asked.title).toBe('Delete schema plan?');
				await waitFor(() => expect(doc.schemas).toBeUndefined());
				expect(onchange).toHaveBeenCalledOnce();
				expect(onselect).toHaveBeenCalledExactlyOnceWith(null);
			});

			it('keeps it when the answer is no', async () => {
				const doc = aDoc({ schemas: [plan()] });
				const { inspector, onchange, onselect } = setup(doc, { kind: 'schema', name: 'plan' });

				inspector.deleteSelection();
				await answer(false);

				expect(doc.schemas).toHaveLength(1);
				expect(onchange).not.toHaveBeenCalled();
				expect(onselect).not.toHaveBeenCalled();
			});

			// confirmDeleteSchema answers false at once: no dialog to say no to.
			it('refuses one a port names, without asking', async () => {
				const doc = aDoc({
					schemas: [plan()],
					agents: [anAgent('a', { outputs: [{ name: 'out', schema: 'plan' }] })]
				});
				const { inspector, onchange, onselect } = setup(doc, { kind: 'schema', name: 'plan' });

				inspector.deleteSelection();
				await Promise.resolve();
				await Promise.resolve();

				expect(confirmState.current).toBeNull();
				expect(doc.schemas).toHaveLength(1);
				expect(onchange).not.toHaveBeenCalled();
				expect(onselect).not.toHaveBeenCalled();
				expect(screen.getByRole('button', { name: 'Delete…' })).toBeDisabled();
			});

			it('refuses one a stage’s port names in a sequence', async () => {
				const stage = aStage('s', 'command', { output: { o: { to: 'x', schema: 'plan' } } });
				const doc = aDoc({ schemas: [plan()], sequences: [aSequence('q', [stage])] });
				const { inspector, onchange } = setup(doc, { kind: 'schema', name: 'plan' });

				inspector.deleteSelection();
				await Promise.resolve();
				await Promise.resolve();

				expect(confirmState.current).toBeNull();
				expect(doc.schemas).toHaveLength(1);
				expect(onchange).not.toHaveBeenCalled();
			});

			// A save replaces the document while the question is open.
			it('removes from the document held when the answer comes', async () => {
				const old = aDoc({ schemas: [plan()] });
				const next = aDoc({ schemas: [plan()] });
				const { inspector, rerender } = setup(old, { kind: 'schema', name: 'plan' });

				inspector.deleteSelection();
				await vi.waitFor(() => expect(confirmState.current).not.toBeNull());
				await rerender({ doc: next });
				await answer(true);

				await waitFor(() => expect(next.schemas).toBeUndefined());
				expect(old.schemas).toHaveLength(1);
			});
		});

		describe('a stage', () => {
			it('asks, rewires what pointed at it and leaves', async () => {
				const doc = aDoc({
					stages: [
						aStage('a', 'command', { on_success: 'b', on_fail: { to: 'b', max: 2 } }),
						aStage('b')
					]
				});
				const { inspector, onchange, onselect } = setup(doc, { kind: 'stage', id: 'b' });

				inspector.deleteSelection();
				const asked = await answer(true);

				expect(asked.title).toBe('Delete stage b?');
				expect(asked.body).toContain('1 other stage pointing at it is rewired');
				await waitFor(() => expect(doc.stages.map((s) => s.id)).toEqual(['a']));
				expect(doc.stages[0].on_success).toBe('end');
				expect(doc.stages[0].on_fail).toBeUndefined();
				expect(onchange).toHaveBeenCalledOnce();
				expect(onselect).toHaveBeenCalledExactlyOnceWith(null);
			});

			it('keeps it on a no', async () => {
				const doc = aDoc();
				const { inspector, onchange } = setup(doc, { kind: 'stage', id: 'build' });
				inspector.deleteSelection();
				await answer(false);
				expect(doc.stages).toHaveLength(1);
				expect(onchange).not.toHaveBeenCalled();
			});

			it('goes back along the trail rather than to the flow', async () => {
				const onback = vi.fn();
				const doc = aDoc();
				const { inspector, onselect } = setup(
					doc,
					{ kind: 'stage', id: 'build' },
					{
						back: { kind: 'phase', id: 'main' },
						onback
					}
				);
				inspector.deleteSelection();
				await answer(true);
				await waitFor(() => expect(onback).toHaveBeenCalledOnce());
				expect(onselect).not.toHaveBeenCalled();
			});

			it('in a sequence, removes it there and shows the sequence', async () => {
				const q = aSequence('lint', [
					aStage('fmt', 'command', { phase: undefined, on_success: 'vet' }),
					aStage('vet', 'command', { phase: undefined })
				]);
				const doc = aDoc({ sequences: [q] });
				const { inspector, onselect } = setup(doc, { kind: 'stage', id: 'vet', sequence: 'lint' });

				inspector.deleteSelection();
				await answer(true);

				await waitFor(() => expect(doc.sequences![0].stages.map((s) => s.id)).toEqual(['fmt']));
				expect(doc.sequences![0].stages[0].on_success).toBeUndefined();
				expect(onselect).toHaveBeenCalledExactlyOnceWith({ kind: 'sequence', id: 'lint' });
			});

			it('never removes the only stage of a sequence', async () => {
				const doc = aDoc({ sequences: [aSequence('lint', [aStage('fmt')])] });
				const { inspector } = setup(doc, { kind: 'stage', id: 'fmt', sequence: 'lint' });
				inspector.deleteSelection();
				await Promise.resolve();
				expect(confirmState.current).toBeNull();
				expect(doc.sequences![0].stages).toHaveLength(1);
			});

			// The confirm's own contract (inspector.ts): the caller removes from
			// the document it holds then, whatever landed while the dialog was open.
			it('in a sequence, removes from the document held when the answer comes', async () => {
				const make = () => aDoc({ sequences: [aSequence('lint', [aStage('fmt'), aStage('vet')])] });
				const old = make();
				const next = make();
				const { inspector, rerender } = setup(old, { kind: 'stage', id: 'vet', sequence: 'lint' });

				inspector.deleteSelection();
				await vi.waitFor(() => expect(confirmState.current).not.toBeNull());
				await rerender({ doc: next });
				await answer(true);

				await waitFor(() => expect(next.sequences![0].stages.map((s) => s.id)).toEqual(['fmt']));
			});

			it('removes nothing when the sequence went while the dialog was open', async () => {
				const old = aDoc({ sequences: [aSequence('lint', [aStage('fmt'), aStage('vet')])] });
				const next = aDoc({ stages: [aStage('vet', 'command')] });
				const { inspector, onchange, rerender } = setup(old, {
					kind: 'stage',
					id: 'vet',
					sequence: 'lint'
				});

				inspector.deleteSelection();
				await vi.waitFor(() => expect(confirmState.current).not.toBeNull());
				await rerender({ doc: next });
				await answer(true);

				await vi.waitFor(() => expect(confirmState.current).toBeNull());
				await Promise.resolve();
				expect(next.stages.map((s) => s.id)).toEqual(['vet']);
				expect(onchange).not.toHaveBeenCalled();
			});
		});

		it('removes an edge at once and shows its stage', async () => {
			const doc = aDoc({ stages: [aStage('a', 'command', { on_fail: { to: 'a', max: 2 } })] });
			const { inspector, onchange, onselect } = setup(doc, {
				kind: 'edge',
				stage: 'a',
				edge: 'on_fail'
			} as Selection);

			inspector.deleteSelection();

			expect(confirmState.current).toBeNull();
			expect(doc.stages[0].on_fail).toBeUndefined();
			expect(onchange).toHaveBeenCalledOnce();
			expect(onselect).toHaveBeenCalledExactlyOnceWith({ kind: 'stage', id: 'a' });
		});

		it('keeps a sequence’s edge in its sequence', () => {
			const stage = aStage('a', 'command', { on_fail: { to: 'a', max: 2 } });
			const doc = aDoc({ sequences: [aSequence('lint', [stage])] });
			const { inspector, onselect } = setup(doc, {
				kind: 'edge',
				stage: 'a',
				edge: 'on_fail',
				sequence: 'lint'
			} as Selection);
			inspector.deleteSelection();
			expect(doc.sequences![0].stages[0].on_fail).toBeUndefined();
			expect(onselect).toHaveBeenCalledExactlyOnceWith({
				kind: 'stage',
				id: 'a',
				sequence: 'lint'
			});
		});

		it('does nothing for an edge whose stage has gone', () => {
			const { inspector, onchange } = setup(aDoc(), {
				kind: 'edge',
				stage: 'gone',
				edge: 'on_fail'
			} as Selection);
			inspector.deleteSelection();
			expect(onchange).not.toHaveBeenCalled();
		});

		it('removes a watcher with its chain', async () => {
			const doc = aDoc({
				phases: [{ ...MAIN_PHASE }, aWatcher('w')],
				stages: [aStage('fix', 'agent', { phase: 'w' })]
			});
			const { inspector, onchange, onselect } = setup(doc, { kind: 'phase', id: 'w' });

			inspector.deleteSelection();
			const asked = await answer(true);

			expect(asked.title).toBe('Delete watcher w?');
			await waitFor(() => expect(doc.phases!.map((p) => p.id)).toEqual(['main']));
			expect(doc.stages).toHaveLength(0);
			expect(onchange).toHaveBeenCalledOnce();
			expect(onselect).toHaveBeenCalledExactlyOnceWith(null);
		});

		it('asks after an outcome by name', async () => {
			const doc = aDoc({ phases: [{ ...MAIN_PHASE }, anOutcome('merged')] });
			const { inspector } = setup(doc, { kind: 'phase', id: 'merged' });
			inspector.deleteSelection();
			expect((await answer(false)).title).toBe('Delete outcome merged?');
		});

		it('never removes the main chain', async () => {
			const doc = aDoc();
			const { inspector, onchange } = setup(doc, { kind: 'phase', id: 'main' });
			inspector.deleteSelection();
			await Promise.resolve();
			expect(confirmState.current).toBeNull();
			expect(doc.phases).toHaveLength(1);
			expect(onchange).not.toHaveBeenCalled();
		});

		it('removes an unused sequence', async () => {
			const doc = aDoc({ sequences: [aSequence('lint')] });
			const { inspector, onchange } = setup(doc, { kind: 'sequence', id: 'lint' });
			inspector.deleteSelection();
			expect((await answer(true)).title).toBe('Delete sequence lint?');
			await waitFor(() => expect(doc.sequences ?? []).toHaveLength(0));
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('refuses a sequence a stage runs', async () => {
			const doc = aDoc({
				sequences: [aSequence('lint')],
				stages: [aStage('run', 'sequence', { sequence: 'lint' })]
			});
			const { inspector, onchange } = setup(doc, { kind: 'sequence', id: 'lint' });
			inspector.deleteSelection();
			await Promise.resolve();
			await Promise.resolve();
			expect(confirmState.current).toBeNull();
			expect(doc.sequences).toHaveLength(1);
			expect(onchange).not.toHaveBeenCalled();
		});

		it('removes a server, naming the agents that lose it', async () => {
			const doc = aDoc({
				mcp_servers: [aServer('tools')],
				agents: [anAgent('a', { mcp: ['tools'] })]
			});
			const { inspector, onchange } = setup(doc, { kind: 'mcp', name: 'tools' });
			inspector.deleteSelection();
			const asked = await answer(true);
			expect(asked.title).toBe('Delete MCP server tools?');
			expect(asked.body).toContain('a');
			await waitFor(() => expect(doc.mcp_servers ?? []).toHaveLength(0));
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('does nothing with nothing selected', async () => {
			const { inspector, onchange, onselect } = setup(aDoc(), null);
			inspector.deleteSelection();
			await Promise.resolve();
			expect(confirmState.current).toBeNull();
			expect(onchange).not.toHaveBeenCalled();
			expect(onselect).not.toHaveBeenCalled();
		});

		it('reaches the same delete from the panel’s own button', async () => {
			const doc = aDoc({ schemas: [plan()] });
			setup(doc, { kind: 'schema', name: 'plan' });
			await userEvent.click(screen.getByRole('button', { name: 'Delete…' }));
			await answer(true);
			await waitFor(() => expect(doc.schemas).toBeUndefined());
		});
	});

	describe('parameter suggestions', () => {
		const typed = () =>
			aDoc({
				parameters: {
					harness: {},
					label: { type: 'string', default: 'x' },
					turns: { type: 'int', default: 3 },
					wait: { type: 'duration', default: '5m' },
					tags: { type: 'string', list: true, default: [] }
				}
			});

		// The lists used to live in StageSection, so the flow panel's Ask field
		// named a list that was not there.
		it('gives the flow panel’s Ask field its suggestions', () => {
			setup(typed(), null);
			expect(suggestions(screen.getByLabelText('Ask pane'))).toEqual(['$harness', '$label']);
		});

		it('gives a stage’s fields theirs', () => {
			setup(aDoc({ ...typed(), stages: [aStage('a', 'agent')] }), { kind: 'stage', id: 'a' });
			expect(suggestions(screen.getByLabelText(/Harness/))).toContain('$label');
			expect(suggestions(screen.getByLabelText('Max turns'))).toEqual(['$turns']);
			expect(suggestions(screen.getByLabelText('Timeout'))).toEqual(['$wait']);
		});

		it('sorts them by what they hold', () => {
			setup(typed(), null);
			expect(options('flow-params')).toEqual(['$harness', '$label']);
			expect(options('flow-int-params')).toEqual(['$turns']);
			expect(options('flow-duration-params')).toEqual(['$wait']);
		});

		it('are there on every panel', () => {
			const doc = aDoc({ ...typed(), schemas: [plan()] });
			setup(doc, { kind: 'schema', name: 'plan' });
			expect(options('flow-params')).toEqual(['$harness', '$label']);
		});

		it('follow the document', async () => {
			const doc = typed();
			const { rerender } = setup(doc, null);
			doc.parameters.region = { type: 'string', default: 'eu' };
			await rerender({ doc });
			await waitFor(() => expect(options('flow-params')).toContain('$region'));
		});

		it('are in the page once each, so an id names one list', () => {
			setup(aDoc({ phases: [{ ...MAIN_PHASE }, aWatcher('w')] }), { kind: 'phase', id: 'w' });
			expect(document.querySelectorAll('datalist#flow-int-params')).toHaveLength(1);
		});
	});
});
