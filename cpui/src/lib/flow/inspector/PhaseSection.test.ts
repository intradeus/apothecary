import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import {
	aDoc,
	anOutcome,
	answer,
	aSequence,
	aSignal,
	aStage,
	aWatcher,
	fill,
	handlers,
	MAIN_PHASE,
	optionsOf,
	pick,
	retype
} from '$test/fixtures/flow.svelte';
import type { SignalSpec } from '$lib/domain';
import type { DocPhase, FlowDoc } from '$lib/domain/flowdoc';
import PhaseSection from './PhaseSection.svelte';

const signals = [
	aSignal('ci_failed', { title: 'CI failed', description: 'A check went red.', debounced: true }),
	aSignal('mr_merged', { title: 'Merged' }),
	aSignal('manual', { title: 'A person', kind: 'hand' })
];

const setup = (phase: DocPhase, over: Partial<FlowDoc> = {}, sigs: SignalSpec[] = signals) => {
	const doc = aDoc({
		phases: [{ ...MAIN_PHASE }, phase],
		stages: [aStage('build', 'command', { on_success: 'done' })],
		...over
	});
	const h = handlers();
	render(PhaseSection, { doc, phase: doc.phases![doc.phases!.length - 1], signals: sigs, ...h });
	return { doc, phase: doc.phases![doc.phases!.length - 1], ...h };
};

describe('PhaseSection', () => {
	describe('the main chain', () => {
		it('sets what its runs are started on', async () => {
			const { doc, onchange } = setup({ ...MAIN_PHASE }, { phases: [{ ...MAIN_PHASE }] });
			await pick('Operates on', 'a merge request someone opened');
			expect(doc.operates_on).toBe('merge_request');
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('has no name to change and cannot be deleted', () => {
			setup({ ...MAIN_PHASE }, { phases: [{ ...MAIN_PHASE }] });
			expect(screen.queryByLabelText('Id')).not.toBeInTheDocument();
			expect(screen.queryByRole('button', { name: 'Delete…' })).not.toBeInTheDocument();
		});

		it('adds a stage and opens it', async () => {
			const { doc, onchange, onopen } = setup({ ...MAIN_PHASE }, { phases: [{ ...MAIN_PHASE }] });
			await userEvent.click(screen.getByRole('button', { name: 'gate' }));
			const added = doc.stages.at(-1)!;
			expect(added.type).toBe('human_gate');
			expect(doc.stages[0].on_success).toBe(added.id);
			expect(onchange).toHaveBeenCalledOnce();
			expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'stage', id: added.id });
		});

		it('adds no sequence stage with nothing to run', () => {
			setup({ ...MAIN_PHASE }, { phases: [{ ...MAIN_PHASE }] });
			expect(screen.queryByRole('button', { name: 'sequence' })).not.toBeInTheDocument();
		});
	});

	describe('its name', () => {
		const stages = [aStage('fix', 'agent', { phase: 'on-ci' })];

		it('renames the chain and moves its stages with it', async () => {
			const { doc, onchange, onselect } = setup(aWatcher(), { stages });
			await retype(screen.getByLabelText('Id'), 'on-red');
			expect(doc.phases![1].id).toBe('on-red');
			expect(doc.stages[0].phase).toBe('on-red');
			expect(onchange).toHaveBeenCalledOnce();
			expect(onselect).toHaveBeenCalledExactlyOnceWith({ kind: 'phase', id: 'on-red' }, 'rename');
		});

		it.each([
			['main', 'main'],
			['a taken id', 'other'],
			['a bad id', 'On CI'],
			['nothing', '']
		])('refuses %s and puts the id back', async (_, text) => {
			const { doc, onchange } = setup(aWatcher(), {
				stages,
				phases: [{ ...MAIN_PHASE }, aWatcher('other'), aWatcher()]
			});
			await retype(screen.getByLabelText('Id'), text);
			expect(screen.getByLabelText('Id')).toHaveValue('on-ci');
			expect(doc.phases![2].id).toBe('on-ci');
			expect(onchange).not.toHaveBeenCalled();
		});
	});

	it('writes the description, and takes an empty one off', async () => {
		const { phase, onchange } = setup(aWatcher());
		await fill(screen.getByLabelText('Description'), 'Fixes CI');
		expect(phase.description).toBe('Fixes CI');
		await fill(screen.getByLabelText('Description'), '');
		expect(phase.description).toBeUndefined();
		expect(onchange).toHaveBeenCalled();
	});

	it('deletes the chain', async () => {
		const { phase, ondelete } = setup(aWatcher());
		await userEvent.click(screen.getByRole('button', { name: 'Delete…' }));
		expect(ondelete).toHaveBeenCalledExactlyOnceWith(phase);
	});

	describe('what opens it', () => {
		it('offers a watcher a person’s start, and the observed signals', async () => {
			setup(aWatcher());
			expect(await optionsOf('Runs on')).toEqual([
				'a person, from the run page',
				'CI failed',
				'Merged'
			]);
		});

		it('offers an outcome no person’s start: it fires on what happens', async () => {
			setup(anOutcome());
			expect(await optionsOf('Fires on')).toEqual(['CI failed', 'Merged']);
		});

		it('keeps a signal the engine no longer has', async () => {
			setup(aWatcher('on-ci', { on: { signal: 'gone' } }));
			expect(await optionsOf('Runs on')).toContain('gone');
		});

		it('writes the signal and shows what it is', async () => {
			const { phase, onchange } = setup(aWatcher());
			await pick('Runs on', 'CI failed');
			expect(phase.on.signal).toBe('ci_failed');
			expect(onchange).toHaveBeenCalledOnce();
			expect(screen.getByText('A check went red.')).toBeInTheDocument();
		});

		// A person's act is the start: no policy, nothing to debounce.
		it('drops the start policy and the quiet going back to manual', async () => {
			const { phase } = setup(
				aWatcher('on-ci', { on: { signal: 'ci_failed', start: 'confirm', quiet_for: '1h' } })
			);
			await pick('Runs on', 'a person, from the run page');
			expect(phase.on).toEqual({ signal: 'manual' });
			expect(screen.queryByRole('combobox', { name: 'When the signal holds' })).toBeNull();
		});

		it('drops the quiet moving to a signal that is not debounced', async () => {
			const { phase } = setup(
				aWatcher('on-ci', { on: { signal: 'ci_failed', start: 'confirm', quiet_for: '1h' } })
			);
			await pick('Runs on', 'Merged');
			expect(phase.on).toEqual({ signal: 'mr_merged', start: 'confirm' });
		});
	});

	describe('how it starts', () => {
		const firing = (over = {}) => aWatcher('on-ci', { on: { signal: 'ci_failed', ...over } });

		it('writes nothing for the default policy', async () => {
			const { phase } = setup(firing({ start: 'confirm' }));
			await pick('When the signal holds', 'start by itself');
			expect(phase.on.start).toBeUndefined();
		});

		it('writes the policy', async () => {
			const { phase, onchange } = setup(firing());
			await pick('When the signal holds', 'ask first (an inbox decision)');
			expect(phase.on.start).toBe('confirm');
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('offers parameters holding policies, and keeps one it no longer offers', async () => {
			setup(firing({ start: '$gone' }), {
				parameters: { policy: { type: 'string', enum: ['auto', 'manual'] } }
			});
			const offered = await optionsOf('When the signal holds');
			expect(offered).toContain('$policy');
			expect(offered).toContain('$gone');
		});

		it('offers a quiet only on a debounced signal', async () => {
			const { phase, onchange } = setup(firing());
			await userEvent.click(screen.getByRole('button', { name: 'Quiet for' }));
			expect(phase.on.quiet_for).toBe('1h');
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('hides the quiet on a signal that has none', () => {
			setup(aWatcher('on-ci', { on: { signal: 'mr_merged' } }));
			expect(screen.queryByText('Quiet for')).not.toBeInTheDocument();
		});

		it('still shows a quiet it holds on a signal that has none', () => {
			setup(aWatcher('on-ci', { on: { signal: 'mr_merged', quiet_for: '2h' } }));
			expect(screen.getByText('Quiet for')).toBeInTheDocument();
		});
	});

	describe('how an outcome ends', () => {
		it('says what it does when it runs nothing', () => {
			setup(anOutcome('stop', { then: 'abort' }));
			expect(screen.getByText('then: abort')).toBeInTheDocument();
		});

		it('writes the end', async () => {
			const { phase, onchange } = setup(anOutcome());
			await pick('Then', 'the run is abandoned');
			expect(phase.then).toBe('abort');
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('moves its last stage’s success to the new end', async () => {
			const { doc } = setup(anOutcome(), {
				stages: [
					aStage('a', 'command', { phase: 'merged', on_success: 'b' }),
					aStage('b', 'command', { phase: 'merged', on_success: 'done' })
				]
			});
			await pick('Then', 'the run is abandoned');
			expect(doc.stages.map((s) => s.on_success)).toEqual(['b', 'abort']);
		});

		it('leaves a last stage that goes elsewhere', async () => {
			const { doc } = setup(anOutcome(), {
				stages: [aStage('a', 'command', { phase: 'merged', on_success: 'end' })]
			});
			await pick('Then', 'the run is abandoned');
			expect(doc.stages[0].on_success).toBe('end');
		});

		it('takes no run limit', () => {
			setup(anOutcome());
			expect(screen.queryByLabelText('Runs per merge request')).not.toBeInTheDocument();
		});
	});

	describe('runs per merge request', () => {
		it.each([
			['a number', '3', 3],
			['unlimited, as -1', '-1', -1],
			['a parameter', '$max', '$max'],
			['nothing', '', undefined]
		])('writes %s', async (_, text, value) => {
			const { phase, onchange } = setup(aWatcher('on-ci', { on: { signal: 'manual', max: 9 } }));
			await fill(screen.getByLabelText('Runs per merge request'), text);
			expect(phase.on.max).toBe(value);
			expect(onchange).toHaveBeenCalled();
		});
	});

	describe('what it runs', () => {
		const withStage = {
			stages: [aStage('fix', 'agent', { phase: 'on-ci' })],
			sequences: [aSequence('lint')]
		};
		const running = {
			stages: [
				aStage('on-ci', 'sequence', { phase: 'on-ci', sequence: 'lint', on_success: 'end' })
			],
			sequences: [aSequence('lint'), aSequence('deploy')]
		};
		const runs = () => screen.getByRole('combobox', { name: 'Runs' });

		it('offers its own stages, the main chain and the sequences', async () => {
			setup(aWatcher(), withStage);
			expect(await optionsOf('Runs')).toEqual([
				'its own stage (1)',
				'main — the main chain, again',
				'sequence lint'
			]);
		});

		it('offers nothing to run when there is nothing but an outcome', async () => {
			setup(anOutcome('merged'), { stages: [], phases: [anOutcome('merged')] });
			expect(await optionsOf('Runs')).toEqual(['nothing — it only ends the run']);
		});

		it('asks before its stages go', async () => {
			const { doc, onchange } = setup(aWatcher(), withStage);
			await pick('Runs', 'sequence lint');
			expect(onchange).not.toHaveBeenCalled();

			const asked = await answer(true);
			expect(asked.title).toBe('Replace the chain of on-ci?');
			expect(asked.action).toBe('Replace');
			await vi.waitFor(() => expect(onchange).toHaveBeenCalledOnce());
			expect(doc.stages).toHaveLength(1);
			expect(doc.stages[0]).toMatchObject({ type: 'sequence', sequence: 'lint' });
		});

		it('stays on what it runs when the replacement is refused', async () => {
			const { doc, onchange } = setup(aWatcher(), withStage);
			await pick('Runs', 'sequence lint');
			await answer(false);
			expect(doc.stages[0].id).toBe('fix');
			expect(onchange).not.toHaveBeenCalled();
			expect(runs()).toHaveTextContent('its own stage (1)');
		});

		it('swaps one sequence for another without asking', async () => {
			const { doc, onchange } = setup(aWatcher(), running);
			await pick('Runs', 'sequence deploy');
			await vi.waitFor(() => expect(onchange).toHaveBeenCalledOnce());
			expect(doc.stages[0].sequence).toBe('deploy');
		});

		it('goes back to stages of its own', async () => {
			const { doc } = setup(aWatcher(), running);
			await pick('Runs', /^its own stage/);
			await vi.waitFor(() => expect(doc.stages[0].type).toBe('command'));
		});
	});
});
