import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import {
	aDoc,
	aSequence,
	aStage,
	aWatcher,
	handlers,
	MAIN_PHASE,
	optionsOf,
	pick
} from '$test/fixtures/flow.svelte';
import type { DocStage, FlowDoc } from '$lib/domain/flowdoc';
import SequenceStage from './SequenceStage.svelte';

const sequences = [
	aSequence('lint', [aStage('fmt', 'command'), aStage('vet', 'agent')], {
		description: 'Keeps it tidy.'
	}),
	aSequence('deploy')
];

const setup = (over: Partial<DocStage> = {}, doc: Partial<FlowDoc> = {}, inSequence?: string) => {
	const d = aDoc({
		stages: [aStage('run', 'sequence', { phase: 'on-ci', ...over })],
		phases: [{ ...MAIN_PHASE }, aWatcher('on-ci')],
		sequences,
		...doc
	});
	const { onchange, onopen } = handlers();
	render(SequenceStage, {
		doc: d,
		stage: d.stages[0],
		scopeSeq: d.sequences?.find((q) => q.id === inSequence),
		onchange,
		onopen
	});
	return { stage: d.stages[0], onchange, onopen };
};

describe('SequenceStage', () => {
	it('writes the sequence it runs', async () => {
		const { stage, onchange } = setup();
		await pick('Sequence', 'deploy');
		expect(stage.sequence).toBe('deploy');
		expect(onchange).toHaveBeenCalledOnce();
	});

	it('offers the main chain from a chain that is not it', async () => {
		setup();
		expect(await optionsOf('Sequence')).toEqual(['main — the main chain, again', 'lint', 'deploy']);
	});

	it('never offers the main chain to itself', async () => {
		setup({ phase: 'main' });
		expect(await optionsOf('Sequence')).toEqual(['lint', 'deploy']);
	});

	it('never offers the sequence it is written in', async () => {
		setup({ phase: undefined }, {}, 'lint');
		expect(await optionsOf('Sequence')).not.toContain('lint');
	});

	it('keeps a name the flow no longer has, so the choice still reads', async () => {
		setup({ sequence: 'gone' });
		expect(await optionsOf('Sequence')).toContain('gone');
	});

	it('shows the chosen sequence as it will run, under the stage', () => {
		setup({ sequence: 'lint' });
		expect(screen.getByText('Keeps it tidy.')).toBeInTheDocument();
		expect(screen.getByText('run.fmt')).toBeInTheDocument();
		expect(screen.getByText('run.vet')).toBeInTheDocument();
	});

	it('opens the sequence to edit it', async () => {
		const { onopen } = setup({ sequence: 'lint' });
		await userEvent.click(screen.getByRole('button', { name: 'Edit sequence' }));
		expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'sequence', id: 'lint' });
	});

	it('offers no edit for the main chain, which is no sequence', () => {
		setup({ sequence: 'main' });
		expect(screen.queryByRole('button', { name: 'Edit sequence' })).not.toBeInTheDocument();
	});
});
