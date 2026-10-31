import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { aDoc, aSequence, aStage, fill, handlers, retype } from '$test/fixtures/flow.svelte';
import type { FlowDoc } from '$lib/domain/flowdoc';
import SequenceSection from './SequenceSection.svelte';

const lint = () =>
	aSequence('lint', [
		aStage('fmt', 'command', { phase: undefined }),
		aStage('vet', 'command', { phase: undefined })
	]);

const setup = (over: Partial<FlowDoc> = {}) => {
	const doc = aDoc({ sequences: [lint(), aSequence('taken')], ...over });
	const h = handlers();
	render(SequenceSection, { doc, sequence: doc.sequences![0], ...h });
	return { doc, sequence: doc.sequences![0], ...h };
};
const id = () => screen.getByLabelText('Id');
const del = () => screen.getByRole('button', { name: 'Delete…' });
const usedBy = (sequence = 'lint') => ({
	stages: [aStage('run', 'sequence', { sequence })]
});

describe('SequenceSection', () => {
	it('renames the sequence and every stage running it', async () => {
		const { doc, onchange, onselect } = setup(usedBy());
		await retype(id(), 'tidy');
		expect(doc.sequences![0].id).toBe('tidy');
		expect(doc.stages[0].sequence).toBe('tidy');
		expect(onchange).toHaveBeenCalledOnce();
		expect(onselect).toHaveBeenCalledExactlyOnceWith({ kind: 'sequence', id: 'tidy' }, 'rename');
	});

	it.each([
		['a taken id', 'taken'],
		['main, which names the main chain', 'main'],
		['a bad id', 'Bad Id'],
		['nothing', '']
	])('refuses %s and puts the id back', async (_, text) => {
		const { sequence, onchange } = setup();
		await retype(id(), text);
		expect(id()).toHaveValue('lint');
		expect(sequence.id).toBe('lint');
		expect(onchange).not.toHaveBeenCalled();
	});

	it('writes the description, and takes an empty one off', async () => {
		const { sequence, onchange } = setup();
		await fill(screen.getByLabelText('Description'), 'Tidy');
		expect(sequence.description).toBe('Tidy');
		await fill(screen.getByLabelText('Description'), '');
		expect(sequence.description).toBeUndefined();
		expect(onchange).toHaveBeenCalled();
	});

	it('counts its stages', () => {
		setup();
		expect(screen.getByText('2 stages')).toBeInTheDocument();
	});

	it('opens a stage in its sequence', async () => {
		const { onopen } = setup();
		await userEvent.click(screen.getByRole('button', { name: /vet/ }));
		expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'stage', id: 'vet', sequence: 'lint' });
	});

	it('moves a stage, the order being part of the chain', async () => {
		const { sequence, onchange } = setup();
		await userEvent.click(screen.getAllByRole('button', { name: 'Move down' })[0]);
		expect(sequence.stages.map((s) => s.id)).toEqual(['vet', 'fmt']);
		expect(onchange).toHaveBeenCalledOnce();

		await userEvent.click(screen.getAllByRole('button', { name: 'Move up' })[1]);
		expect(sequence.stages.map((s) => s.id)).toEqual(['fmt', 'vet']);
	});

	it('stops at the ends of the chain', () => {
		setup();
		expect(screen.getAllByRole('button', { name: 'Move up' })[0]).toBeDisabled();
		expect(screen.getAllByRole('button', { name: 'Move down' })[1]).toBeDisabled();
	});

	it('adds a stage at the end and opens it', async () => {
		const { sequence, onchange, onopen } = setup();
		await userEvent.click(screen.getByRole('button', { name: 'wait' }));
		const added = sequence.stages.at(-1)!;
		expect(added.type).toBe('wait');
		expect(onchange).toHaveBeenCalledOnce();
		expect(onopen).toHaveBeenCalledExactlyOnceWith({
			kind: 'stage',
			id: added.id,
			sequence: 'lint'
		});
	});

	it('deletes an unused sequence', async () => {
		const { sequence, ondelete } = setup();
		expect(screen.getByText(/Nothing runs it yet/)).toBeInTheDocument();
		await userEvent.click(del());
		expect(ondelete).toHaveBeenCalledExactlyOnceWith(sequence);
	});

	it('will not delete a sequence a stage runs, and says so', () => {
		setup(usedBy());
		expect(del()).toBeDisabled();
		expect(del()).toHaveAttribute('title', 'Stages run it — point them elsewhere first');
	});

	it('opens the stages running it', async () => {
		const { onopen } = setup(usedBy());
		await userEvent.click(screen.getByRole('button', { name: 'run' }));
		expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'stage', id: 'run' });
	});
});
