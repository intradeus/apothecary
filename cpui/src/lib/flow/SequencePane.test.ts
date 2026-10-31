import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { aDoc, aSequence, aStage } from '$test/fixtures/flow.svelte';
import { canvas } from '$test/fixtures/xyflow/canvas';
import type { FlowDoc } from '$lib/domain/flowdoc';
import type { FlowSelection } from './flowSelection.svelte';
import type { Level } from './graph/sequence';
import SequencePane from './SequencePane.svelte';

vi.mock('@xyflow/svelte', () => import('$test/fixtures/xyflow.svelte'));

const levelOf = (id: string, over: Partial<Level> = {}): Level => ({
	sequence: aSequence(id),
	view: { id, exit: 'back', fail: 'out' },
	...over
});

const setup = (levels: Level[]) => {
	const panels = {
		levels,
		paneKey: levels.map((l) => l.sequence.id).join('/'),
		sequenceLayout: {},
		sequenceRev: 0,
		select: vi.fn(),
		onSequenceSelect: vi.fn(),
		onSequenceMeasured: vi.fn()
	};
	const doc: FlowDoc = aDoc({ phases: undefined, stages: [aStage('fmt')] });
	render(SequencePane, {
		panels: panels as unknown as FlowSelection,
		open: levels.at(-1)!,
		doc,
		actions: []
	});
	return panels;
};

describe('SequencePane', () => {
	it('shows where it sits, each sequence above it a way back', async () => {
		const panels = setup([levelOf('outer'), levelOf('inner')]);
		expect(screen.queryByRole('button', { name: 'inner' })).not.toBeInTheDocument();
		expect(screen.getByText('inner')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: 'outer' }));
		expect(panels.select).toHaveBeenCalledExactlyOnceWith({ kind: 'sequence', id: 'outer' });
	});

	it('says what the sequence is for', () => {
		setup([levelOf('lint', { sequence: aSequence('lint', [], { description: 'Tidies.' }) })]);
		expect(screen.getByText('Tidies.')).toBeInTheDocument();
	});

	it('says which stage it runs in place of, with nothing else to say', () => {
		setup([levelOf('lint', { stage: aStage('run', 'sequence') })]);
		expect(screen.getByText('runs in place of run')).toBeInTheDocument();
	});

	it('closes', async () => {
		const panels = setup([levelOf('lint')]);
		await userEvent.click(screen.getByRole('button', { name: 'Close the sequence (Esc)' }));
		expect(panels.select).toHaveBeenCalledExactlyOnceWith(null);
	});

	it('draws the chain read-only, a click selecting in the sequence', async () => {
		const panels = setup([levelOf('lint')]);
		expect(canvas.props.nodesConnectable).toBe(false);
		await userEvent.click(screen.getByRole('button', { name: 'node fmt' }));
		expect(panels.onSequenceSelect).toHaveBeenCalledExactlyOnceWith({ kind: 'stage', id: 'fmt' });
	});
});
