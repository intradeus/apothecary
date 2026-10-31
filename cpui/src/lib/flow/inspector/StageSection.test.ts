import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import {
	aDoc,
	aSequence,
	aStage,
	aWatcher,
	fill,
	handlers,
	MAIN_PHASE,
	optionsOf,
	pick,
	retype
} from '$test/fixtures/flow.svelte';
import type { DocStage, FlowDoc, StageKind } from '$lib/domain/flowdoc';
import StageSection from './StageSection.svelte';

const none = { agents: [], actions: [], signals: [] };
const id = () => screen.getByLabelText('Id');

const setup = (stages: DocStage[], over: Partial<FlowDoc> = {}) => {
	const doc = aDoc({ stages, ...over });
	const h = handlers();
	render(StageSection, { doc, stage: doc.stages[0], ...none, ...h });
	return { doc, stage: doc.stages[0], ...h };
};

const inSequence = (stages: DocStage[], over: Partial<FlowDoc> = {}) => {
	const doc = aDoc({ sequences: [aSequence('lint', stages, { description: 'Tidy' })], ...over });
	const scopeSeq = doc.sequences![0];
	const h = handlers();
	render(StageSection, { doc, stage: scopeSeq.stages[0], scopeSeq, ...none, ...h });
	return { doc, scopeSeq, stage: scopeSeq.stages[0], ...h };
};

describe('StageSection', () => {
	describe('renaming', () => {
		it('renames the stage and every route to it, each link of a chain', async () => {
			const { doc, onchange, onselect } = setup([
				aStage('a'),
				aStage('b', 'command', {
					on_success: 'a',
					on_fail: { to: 'x', max: 1, then: { to: 'a', max: 1 } },
					on_reject: { to: 'a', max: 1 }
				})
			]);
			await retype(id(), 'first');
			const [a, b] = doc.stages;
			expect(a.id).toBe('first');
			expect(b.on_success).toBe('first');
			expect(b.on_fail?.then?.to).toBe('first');
			expect(b.on_fail?.to).toBe('x');
			expect(b.on_reject?.to).toBe('first');
			expect(onchange).toHaveBeenCalledOnce();
			expect(onselect).toHaveBeenCalledExactlyOnceWith({ kind: 'stage', id: 'first' }, 'rename');
		});

		it.each([
			['a name taken in the chains', 'b'],
			['a bad name', 'Not Ok'],
			['end', 'end'],
			['done', 'done'],
			['abort', 'abort'],
			['debug', 'debug'],
			['start', 'start'],
			['fail', 'fail'],
			['nothing', '']
		])('refuses %s and puts the name back', async (_, text) => {
			const { stage, onchange, onselect } = setup([aStage('a'), aStage('b')]);
			await retype(id(), text);
			expect(id()).toHaveValue('a');
			expect(stage.id).toBe('a');
			expect(onchange).not.toHaveBeenCalled();
			expect(onselect).not.toHaveBeenCalled();
		});

		it('takes a name the chains use, its ids being its sequence’s own', async () => {
			const { stage, onselect } = inSequence(
				[aStage('fmt', 'command', { phase: undefined }), aStage('vet')],
				{ stages: [aStage('build')] }
			);
			await retype(id(), 'build');
			expect(stage.id).toBe('build');
			expect(onselect).toHaveBeenCalledExactlyOnceWith(
				{ kind: 'stage', id: 'build', sequence: 'lint' },
				'rename'
			);
		});

		it('refuses a name its sequence uses', async () => {
			const { stage } = inSequence([aStage('fmt'), aStage('vet')]);
			await retype(id(), 'vet');
			expect(stage.id).toBe('fmt');
		});
	});

	describe('where it is written', () => {
		const chains = { phases: [{ ...MAIN_PHASE }, aWatcher('w', { on: { signal: 'ci_failed' } })] };

		it('names every chain', async () => {
			setup([aStage('a')], chains);
			expect(await optionsOf('Chain')).toEqual(['the main chain', 'watcher w — on ci_failed']);
		});

		it('moves it to another chain', async () => {
			const { doc, onchange } = setup([aStage('a')], chains);
			await pick('Chain', 'watcher w — on ci_failed');
			expect(doc.stages[0].phase).toBe('w');
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('has no chain in a document without any', () => {
			setup([aStage('a')], { phases: undefined });
			expect(screen.queryByRole('combobox', { name: 'Chain' })).not.toBeInTheDocument();
		});

		it('shows its sequence in place of a chain', async () => {
			const { onopen } = inSequence([aStage('fmt')]);
			expect(screen.getByText('Tidy')).toBeInTheDocument();
			expect(screen.queryByRole('combobox', { name: 'Chain' })).not.toBeInTheDocument();
			await userEvent.click(screen.getByRole('button', { name: 'Edit sequence' }));
			expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'sequence', id: 'lint' });
		});
	});

	describe('its condition', () => {
		it('writes it', async () => {
			const { stage, onchange } = setup([aStage('a')]);
			await pick('Run if', 'the ticket has a tracker issue');
			expect(stage.run_if).toBe('run.ticket.tracker');
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('takes it off', async () => {
			const { stage } = setup([aStage('a', 'command', { run_if: 'run.subject' })]);
			await pick('Run if', 'always');
			expect(stage.run_if).toBeUndefined();
		});

		it('has none on a sequence stage', () => {
			setup([aStage('a', 'sequence')]);
			expect(screen.queryByRole('combobox', { name: 'Run if' })).not.toBeInTheDocument();
		});
	});

	it('writes the description', async () => {
		const { stage, onchange } = setup([aStage('a')]);
		await fill(screen.getByLabelText('Description'), 'Builds it');
		expect(stage.description).toBe('Builds it');
		expect(onchange).toHaveBeenCalled();
	});

	it.each<[StageKind, string | RegExp]>([
		['agent', 'Agent'],
		['command', 'run_ref'],
		['human_gate', 'Question'],
		['action', 'Action'],
		['service', 'Service'],
		['sequence', 'Sequence'],
		['wait', 'Wait for']
	])('shows what a %s stage asks for', (type, label) => {
		setup([aStage('a', type)]);
		expect(screen.getByLabelText(label)).toBeInTheDocument();
	});

	it('shows its routes', () => {
		setup([aStage('a')]);
		expect(screen.getByText('Routes')).toBeInTheDocument();
	});

	describe('deleting', () => {
		it('deletes the stage', async () => {
			const { ondelete } = setup([aStage('a')]);
			await userEvent.click(screen.getByRole('button', { name: 'Delete…' }));
			expect(ondelete).toHaveBeenCalledExactlyOnceWith('a');
		});

		it('will not delete a sequence’s only stage, and says why', () => {
			inSequence([aStage('fmt')]);
			const del = screen.getByRole('button', { name: 'Delete…' });
			expect(del).toBeDisabled();
			expect(del).toHaveAttribute(
				'title',
				'A sequence needs a stage — delete the sequence instead'
			);
		});

		it('deletes one of a sequence’s stages', () => {
			inSequence([aStage('fmt'), aStage('vet')]);
			expect(screen.getByRole('button', { name: 'Delete…' })).toBeEnabled();
		});
	});
});
