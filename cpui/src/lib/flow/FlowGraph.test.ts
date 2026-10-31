import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import type { Node } from '@xyflow/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { aDoc, anOutcome, aStage, aWatcher, MAIN_PHASE } from '$test/fixtures/flow.svelte';
import { canvas } from '$test/fixtures/xyflow/canvas';
import type { FlowDoc } from '$lib/domain/flowdoc';
import FlowGraph from './FlowGraph.svelte';

vi.mock('@xyflow/svelte', () => import('$test/fixtures/xyflow.svelte'));

type Props = Partial<Parameters<typeof FlowGraph>[1]>;

const setup = (doc: FlowDoc = aDoc(), over: Props = {}) => {
	const h = { onselect: vi.fn(), onmove: vi.fn(), onlink: vi.fn() };
	const view = render(FlowGraph, { doc, layout: {}, rev: 0, ...h, ...over });
	return { doc, ...h, ...view };
};
const twoStages = () =>
	aDoc({
		stages: [
			aStage('build', 'command', { on_success: 'test', on_fail: { to: 'build', max: 2 } }),
			aStage('test', 'command', { on_success: 'done' })
		]
	});
const nodeIds = () => screen.getAllByTestId('node').map((n) => n.dataset.node);
const node = (id: string) => canvas.nodes.find((n) => n.id === id) as Node;

describe('FlowGraph', () => {
	describe('what it draws', () => {
		it('draws each stage by the real node, with its chain’s ends', () => {
			setup(twoStages());
			expect(nodeIds()).toEqual(expect.arrayContaining(['build', 'test', '__start']));
			expect(screen.getByText('build')).toBeInTheDocument();
		});

		it('draws the edges, keyed by what they are', () => {
			setup(twoStages());
			expect(canvas.edges.map((e) => e.id)).toEqual(
				expect.arrayContaining([expect.stringMatching(/^on_fail\|build/)])
			);
		});

		it('follows the document on a new revision', async () => {
			const doc = twoStages();
			const { rerender } = setup(doc);
			doc.stages.push(aStage('ship', 'command'));
			await rerender({ rev: 1 });
			await waitFor(() => expect(nodeIds()).toContain('ship'));
		});

		it('marks a stage just added as arriving', async () => {
			const doc = twoStages();
			setup(doc);
			expect(node('build').data.fresh).toBeFalsy();
			doc.stages.push(aStage('ship', 'command'));
			await waitFor(() => expect(node('ship').data.fresh).toBe(true));
		});

		it('frames each watcher, not the main chain', () => {
			setup(
				aDoc({
					phases: [{ ...MAIN_PHASE }, aWatcher('w')],
					stages: [aStage('build'), aStage('fix', 'command', { phase: 'w' })]
				})
			);
			expect(screen.getAllByRole('button', { name: /^Fold / })).toHaveLength(1);
			expect(screen.getByRole('button', { name: 'Fold w to its marker' })).toBeInTheDocument();
		});

		it('draws no frames for a sequence', () => {
			setup(
				aDoc({
					phases: [{ ...MAIN_PHASE }, aWatcher('w')],
					stages: [aStage('fix', 'command', { phase: 'w' })]
				}),
				{ sequence: { id: 'lint', exit: 'back', fail: 'out' } }
			);
			expect(screen.queryByRole('button', { name: /^Fold / })).not.toBeInTheDocument();
		});
	});

	describe('selecting', () => {
		it('selects the stage clicked', async () => {
			const { onselect } = setup();
			await userEvent.click(screen.getByRole('button', { name: 'node build' }));
			expect(onselect).toHaveBeenCalledExactlyOnceWith({ kind: 'stage', id: 'build' });
		});

		it('selects the main chain from its marker', async () => {
			const { onselect } = setup();
			await userEvent.click(screen.getByRole('button', { name: 'node __start' }));
			expect(onselect).toHaveBeenCalledExactlyOnceWith({ kind: 'phase', id: 'main' });
		});

		it('selects nothing on the bare canvas', async () => {
			const { onselect } = setup();
			await userEvent.click(screen.getByRole('button', { name: 'pane' }));
			expect(onselect).toHaveBeenCalledExactlyOnceWith(null);
		});

		it('selects a failure edge as an edge', async () => {
			const { onselect } = setup(twoStages());
			const id = canvas.edges.find((e) => e.id.startsWith('on_fail|'))!.id;
			await userEvent.click(screen.getByRole('button', { name: `edge ${id}` }));
			expect(onselect).toHaveBeenCalledExactlyOnceWith({
				kind: 'edge',
				stage: 'build',
				edge: 'on_fail'
			});
		});

		it('selects the edge from its label too', () => {
			const { onselect } = setup(twoStages());
			const e = canvas.edges.find((x) => x.id.startsWith('on_fail|'))!;
			(e.data?.pick as () => void)();
			expect(onselect).toHaveBeenCalledExactlyOnceWith({
				kind: 'edge',
				stage: 'build',
				edge: 'on_fail'
			});
		});

		// The keys are the inspector's: Svelte Flow must not delete behind its back.
		it('leaves deleting to the inspector', () => {
			setup();
			expect(canvas.props.deleteKey).toBeNull();
		});
	});

	describe('connecting', () => {
		const conn = { source: 'build', sourceHandle: 'on_fail', target: 'test', targetHandle: null };

		it('asks to rewire a control handle', () => {
			const { onlink } = setup(twoStages());
			canvas.props.onconnect?.(conn);
			expect(onlink).toHaveBeenCalledExactlyOnceWith('build', 'on_fail', 'test');
		});

		it('never rewires from a data dot', () => {
			const { onlink } = setup(twoStages());
			canvas.props.onconnect?.({ ...conn, sourceHandle: 'out:report' });
			expect(onlink).not.toHaveBeenCalled();
		});

		it('connects nothing when read-only', () => {
			const { onlink } = setup(twoStages(), { readonly: true });
			expect(canvas.props.nodesConnectable).toBe(false);
			canvas.props.onconnect?.(conn);
			expect(onlink).not.toHaveBeenCalled();
		});

		it('connects by default', () => {
			setup();
			expect(canvas.props.nodesConnectable).toBe(true);
		});
	});

	describe('arranging', () => {
		it('offers it only with a way to do it', () => {
			setup();
			expect(screen.queryByRole('button', { name: 'Auto-arrange' })).not.toBeInTheDocument();
		});

		it('hands over the nodes’ measured sizes, keyed like the layout', async () => {
			const onarrange = vi.fn();
			setup(aDoc(), { onarrange });
			canvas.setNodes(
				canvas.nodes.map((n) =>
					n.id === 'build'
						? { ...n, measured: { width: 200, height: 90 } }
						: n.id === '__start'
							? { ...n, measured: { width: 80, height: 30 } }
							: n
				)
			);
			await userEvent.click(screen.getByRole('button', { name: 'Auto-arrange' }));
			expect(onarrange).toHaveBeenCalledExactlyOnceWith({
				build: { width: 200, height: 90 },
				start: { width: 80, height: 30 }
			});
		});
	});

	describe('folding', () => {
		const watched = () =>
			aDoc({
				phases: [{ ...MAIN_PHASE }, aWatcher('w')],
				stages: [aStage('build'), aStage('fix', 'command', { phase: 'w', on_success: 'end' })]
			});

		it('folds a chain to its marker and back', async () => {
			setup(watched());
			expect(nodeIds()).toContain('fix');

			await userEvent.click(screen.getByRole('button', { name: 'Fold w to its marker' }));
			await waitFor(() => expect(nodeIds()).not.toContain('fix'));
			expect(nodeIds()).toContain('build');

			await userEvent.click(screen.getByRole('button', { name: 'Unfold w' }));
			await waitFor(() => expect(nodeIds()).toContain('fix'));
		});
	});

	describe('dragging', () => {
		it('saves where a stage lands, under its layout key', () => {
			const { onmove } = setup();
			const n = { ...node('build'), position: { x: 12, y: 34 } };
			canvas.props.onnodedragstart?.({ targetNode: n });
			canvas.props.onnodedragstop?.({ targetNode: n });
			expect(onmove).toHaveBeenCalledExactlyOnceWith('build', 12, 34);
		});

		it('takes a chain along with its marker, saving every member', async () => {
			const doc = aDoc({
				phases: [{ ...MAIN_PHASE }, aWatcher('w')],
				stages: [aStage('build'), aStage('fix', 'command', { phase: 'w', on_success: 'end' })]
			});
			const { onmove } = setup(doc, {
				layout: { build: { x: 0, y: 0 }, fix: { x: 300, y: 200 }, 'trigger:w': { x: 100, y: 200 } }
			});
			const marker = node('__trigger:w');
			const fix = node('fix');

			canvas.props.onnodedragstart?.({ targetNode: marker });
			const moved = {
				...marker,
				position: { x: marker.position.x + 50, y: marker.position.y + 10 }
			};
			canvas.props.onnodedrag?.({ targetNode: moved });
			await waitFor(() => expect(node('fix').position.x).toBe(fix.position.x + 50));

			canvas.props.onnodedragstop?.({ targetNode: moved });
			expect(onmove).toHaveBeenCalledWith('fix', fix.position.x + 50, fix.position.y + 10);
			expect(onmove).toHaveBeenCalledWith('trigger:w', moved.position.x, moved.position.y);
		});

		it('moves nothing else with an ordinary stage', () => {
			const { onmove } = setup(twoStages());
			const n = { ...node('build'), position: { x: 1, y: 2 } };
			canvas.props.onnodedragstart?.({ targetNode: n });
			canvas.props.onnodedrag?.({ targetNode: n });
			canvas.props.onnodedragstop?.({ targetNode: n });
			expect(onmove).toHaveBeenCalledOnce();
		});

		it('ignores a selection dragged with no node of its own', () => {
			const { onmove } = setup();
			canvas.props.onnodedragstart?.({ targetNode: null });
			canvas.props.onnodedragstop?.({ targetNode: null });
			expect(onmove).not.toHaveBeenCalled();
		});
	});

	describe('a stage leaving', () => {
		beforeEach(() => {
			vi.useFakeTimers();
		});
		afterEach(() => {
			vi.useRealTimers();
		});

		it('stays as a ghost while it dematerialises, then goes', async () => {
			const doc = twoStages();
			const { rerender } = setup(doc);
			doc.stages = [doc.stages[0]];
			await rerender({ rev: 1 });
			await vi.waitFor(() => expect(node('test')?.data.leaving).toBe(true));

			await vi.advanceTimersByTimeAsync(450);
			expect(nodeIds()).not.toContain('test');
		});
	});

	describe('a stage renamed', () => {
		it('keeps its place instead of leaving and arriving', async () => {
			const doc = twoStages();
			const { onmove, rerender } = setup(doc, { layout: { test: { x: 70, y: 80 } } });
			doc.stages[1].id = 'check';
			await rerender({ rev: 1 });
			await waitFor(() => expect(onmove).toHaveBeenCalledExactlyOnceWith('check', 70, 80));
			expect(node('test')).toBeUndefined();
		});

		it('takes the layout’s place when it has one', async () => {
			const doc = twoStages();
			const { onmove, rerender } = setup(doc, {
				layout: { test: { x: 70, y: 80 }, check: { x: 5, y: 5 } }
			});
			doc.stages[1].id = 'check';
			await rerender({ rev: 1 });
			await waitFor(() => expect(node('check')).toBeDefined());
			expect(onmove).not.toHaveBeenCalled();
		});
	});

	it('marks an outcome with the end it says', () => {
		setup(
			aDoc({
				phases: [{ ...MAIN_PHASE }, anOutcome('stop', { then: 'abort' })],
				stages: [aStage('build')]
			})
		);
		const marker = screen
			.getAllByTestId('node')
			.find((n) => n.dataset.node === '__trigger:stop') as HTMLElement;
		expect(marker.querySelector('.tone-danger')).not.toBeNull();
	});
});
