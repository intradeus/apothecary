import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
	aDoc,
	anAgent,
	aSequence,
	aStage,
	aWatcher,
	fill,
	handlers,
	MAIN_PHASE,
	optionsOf,
	pick
} from '$test/fixtures/flow.svelte';
import type { DocAgent, DocStage, FlowDoc } from '$lib/domain/flowdoc';
import RoutesSection from './RoutesSection.svelte';

const successField = /^success:/;
const remove = () => screen.getAllByRole('button', { name: 'Remove this link' });
const maxes = () => screen.getAllByRole('spinbutton', { name: 'Max' });

const setup = (
	stage: DocStage,
	others: DocStage[] = [],
	opts: { focusRoute?: string; agents?: DocAgent[]; doc?: Partial<FlowDoc> } = {}
) => {
	const doc = aDoc({ stages: [stage, ...others], ...opts.doc });
	const { onchange } = handlers();
	render(RoutesSection, {
		doc,
		stage: doc.stages[0],
		focusRoute: opts.focusRoute,
		agents: opts.agents ?? [],
		onchange
	});
	return { doc, stage: doc.stages[0], onchange };
};

const inSequence = (at = 0, opts: Partial<DocStage> = {}) => {
	const doc = aDoc({
		sequences: [
			aSequence('lint', [
				aStage('fmt', 'command', { phase: undefined, ...(at === 0 ? opts : {}) }),
				aStage('vet', 'command', { phase: undefined, ...(at === 1 ? opts : {}) })
			])
		]
	});
	const scopeSeq = doc.sequences![0];
	const { onchange } = handlers();
	render(RoutesSection, { doc, stage: scopeSeq.stages[at], scopeSeq, agents: [], onchange });
	return { stage: scopeSeq.stages[at], onchange };
};

describe('RoutesSection', () => {
	describe('success', () => {
		it('writes where it goes', async () => {
			const { stage, onchange } = setup(aStage('a', 'command', { on_success: 'done' }), [
				aStage('b')
			]);
			await pick(successField, 'b');
			expect(stage.on_success).toBe('b');
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('offers the chain’s other stages and the ways out, not itself', async () => {
			setup(aStage('a'), [aStage('b'), aStage('other', 'command', { phase: 'w' })], {
				doc: { phases: [{ ...MAIN_PHASE }, aWatcher('w')] }
			});
			expect(await optionsOf(successField)).toEqual([
				'b',
				'end — back to waiting',
				'done — the run is done',
				'abort — stop the run'
			]);
		});

		it('in a sequence, goes on to the next stage by default', async () => {
			inSequence(0);
			expect((await optionsOf(successField))[0]).toBe('next — vet');
		});

		it('in a sequence, leaves from the last stage by default', async () => {
			inSequence(1);
			expect((await optionsOf(successField))[0]).toBe('out — on from the sequence stage');
		});

		it('in a sequence, saying nothing takes the route off', async () => {
			const { stage } = inSequence(0, { on_success: 'done' });
			await pick(successField, 'next — vet');
			expect(stage.on_success).toBeUndefined();
		});
	});

	describe('adding a route', () => {
		it('offers each route its kind has, until it has it', async () => {
			setup(aStage('a', 'agent'));
			for (const kind of ['on_fail', 'on_reject', 'on_timeout'])
				expect(screen.getByRole('button', { name: new RegExp(`${kind}$`) })).toBeInTheDocument();
			await userEvent.click(screen.getByRole('button', { name: /on_fail$/ }));
			expect(screen.queryByRole('button', { name: /on_fail$/ })).not.toBeInTheDocument();
		});

		it('retries the stage itself on a failure', async () => {
			const { stage, onchange } = setup(aStage('a'));
			await userEvent.click(screen.getByRole('button', { name: /on_fail$/ }));
			expect(stage.on_fail).toEqual({ to: 'a', max: 3 });
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('sends a gate’s rejection back to what leads into it', async () => {
			const { stage } = setup(aStage('gate', 'human_gate'), [
				aStage('other'),
				aStage('plan', 'agent', { on_success: 'gate' })
			]);
			await userEvent.click(screen.getByRole('button', { name: /on_reject$/ }));
			expect(stage.on_reject).toEqual({ to: 'plan', max: 3 });
		});

		it('sends a gate with nothing before it to the first other stage', async () => {
			const { stage } = setup(aStage('gate', 'human_gate'), [aStage('other')]);
			await userEvent.click(screen.getByRole('button', { name: /on_reject$/ }));
			expect(stage.on_reject).toEqual({ to: 'other', max: 3 });
		});

		it('sends a lone gate to a person', async () => {
			const { stage } = setup(aStage('gate', 'human_gate'));
			await userEvent.click(screen.getByRole('button', { name: /on_reject$/ }));
			expect(stage.on_reject).toEqual({ to: 'debug' });
		});
	});

	describe('a link’s target', () => {
		const onFail = /^on_fail$/;

		it('drops the limit and what follows on a reserved target', async () => {
			const { stage, onchange } = setup(
				aStage('a', 'command', { on_fail: { to: 'a', max: 2, then: { to: 'debug' } } })
			);
			await pick(onFail, 'abort — stop the run');
			expect(stage.on_fail?.to).toBe('abort');
			expect(stage.on_fail?.max).toBeUndefined();
			expect(stage.on_fail?.then).toBeUndefined();
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('gives a stage target a limit', async () => {
			const { stage } = setup(aStage('a', 'command', { on_fail: { to: 'debug' } }), [aStage('b')]);
			await pick(onFail, 'b');
			expect(stage.on_fail).toEqual({ to: 'b', max: 3 });
		});

		it('keeps a limit that was set', async () => {
			const { stage } = setup(aStage('a', 'command', { on_fail: { to: 'a', max: 7 } }), [
				aStage('b')
			]);
			await pick(onFail, 'b');
			expect(stage.on_fail?.max).toBe(7);
		});

		// Unlimited needs a human on the link: each firing parks on their answer.
		it('takes an unlimited link’s ∞ away when it leaves the gate', async () => {
			const { stage } = setup(aStage('a', 'command', { on_fail: { to: 'gate', max: -1 } }), [
				aStage('gate', 'human_gate'),
				aStage('b')
			]);
			await pick(onFail, 'b');
			expect(stage.on_fail).toEqual({ to: 'b', max: 3 });
		});

		it('keeps ∞ moving between gates', async () => {
			const { stage } = setup(aStage('a', 'command', { on_fail: { to: 'g1', max: -1 } }), [
				aStage('g1', 'human_gate'),
				aStage('g2', 'human_gate')
			]);
			await pick(onFail, 'g2');
			expect(stage.on_fail?.max).toBe(-1);
		});

		it('offers fail only in a sequence', async () => {
			inSequence(0, { on_fail: { to: 'debug' } });
			expect(await optionsOf(onFail)).toContain('fail — the sequence stage’s on_fail');
		});

		it('offers no fail in a chain', async () => {
			setup(aStage('a', 'command', { on_fail: { to: 'debug' } }));
			expect(await optionsOf(onFail)).not.toContain('fail — the sequence stage’s on_fail');
		});
	});

	describe('a link’s limit', () => {
		const stage = () => aStage('a', 'command', { on_fail: { to: 'a', max: 3 } });

		it('writes a whole number', async () => {
			const { stage: s, onchange } = setup(stage());
			await fill(maxes()[0], '5');
			expect(s.on_fail?.max).toBe(5);
			expect(onchange).toHaveBeenCalled();
		});

		it.each([['0'], ['-2'], ['1.5']])('ignores %s, which is no limit', async (text) => {
			const { stage: s } = setup(stage());
			await fireEvent.input(maxes()[0], { target: { value: text } });
			expect(s.on_fail?.max).toBe(3);
		});

		it('ignores an emptied field', async () => {
			const { stage: s, onchange } = setup(stage());
			await userEvent.clear(maxes()[0]);
			expect(s.on_fail?.max).toBe(3);
			expect(onchange).not.toHaveBeenCalled();
		});

		it('offers no ∞ between two stages no human stands on', () => {
			setup(stage());
			expect(screen.queryByRole('button', { name: 'No limit (∞)' })).not.toBeInTheDocument();
		});

		it('offers ∞ on a link to a gate, dropping what follows', async () => {
			const { stage: s, onchange } = setup(
				aStage('a', 'command', { on_fail: { to: 'gate', max: 3, then: { to: 'debug' } } }),
				[aStage('gate', 'human_gate')]
			);
			await userEvent.click(screen.getByRole('button', { name: 'No limit (∞)' }));
			expect(s.on_fail?.max).toBe(-1);
			expect(s.on_fail?.then).toBeUndefined();
			expect(onchange).toHaveBeenCalledOnce();
			expect(screen.queryByRole('button', { name: /then$/ })).not.toBeInTheDocument();
		});

		it('offers ∞ from a gate', () => {
			setup(aStage('gate', 'human_gate', { on_reject: { to: 'b', max: 3 } }), [aStage('b')]);
			expect(screen.getByRole('button', { name: 'No limit (∞)' })).toBeInTheDocument();
		});

		it('gives an unlimited link a limit again', async () => {
			const { stage: s } = setup(
				aStage('gate', 'human_gate', { on_reject: { to: 'b', max: -1 } }),
				[aStage('b')]
			);
			await userEvent.click(screen.getByTitle('No limit — click to give the link one again'));
			expect(s.on_reject?.max).toBe(3);
			expect(maxes()[0]).toHaveValue(3);
		});
	});

	describe('fallbacks', () => {
		it('adds a person after the last link', async () => {
			const { stage, onchange } = setup(
				aStage('a', 'command', { on_fail: { to: 'a', max: 2, then: { to: 'a', max: 1 } } })
			);
			await userEvent.click(screen.getByRole('button', { name: /then$/ }));
			expect(stage.on_fail?.then?.then).toEqual({ to: 'debug' });
			expect(onchange).toHaveBeenCalledOnce();
			expect(screen.getByRole('combobox', { name: 'on_fail · then #2' })).toBeInTheDocument();
		});

		it('offers nothing after a reserved target', () => {
			setup(aStage('a', 'command', { on_fail: { to: 'debug' } }));
			expect(screen.queryByRole('button', { name: /then$/ })).not.toBeInTheDocument();
		});

		it('removes the route with its only link', async () => {
			const { stage, onchange } = setup(aStage('a', 'command', { on_fail: { to: 'a', max: 2 } }));
			await userEvent.click(remove()[0]);
			expect(stage.on_fail).toBeUndefined();
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('moves the fallback up when the first link goes', async () => {
			const { stage } = setup(
				aStage('a', 'command', { on_fail: { to: 'a', max: 2, then: { to: 'debug' } } })
			);
			await userEvent.click(remove()[0]);
			expect(stage.on_fail).toEqual({ to: 'debug' });
		});

		it('closes the gap when a middle link goes', async () => {
			const { stage } = setup(
				aStage('a', 'command', {
					on_fail: { to: 'a', max: 2, then: { to: 'a', max: 1, then: { to: 'debug' } } }
				})
			);
			await userEvent.click(remove()[1]);
			expect(stage.on_fail).toEqual({ to: 'a', max: 2, then: { to: 'debug' } });
		});
	});

	describe('the route picked on the canvas', () => {
		beforeEach(() => {
			vi.spyOn(Element.prototype, 'scrollIntoView');
		});

		const stage = () => aStage('a', 'command', { on_fail: { to: 'debug' } });

		it('marks and scrolls to it', () => {
			const { stage: s } = setup(stage(), [], { focusRoute: 'on_fail' });
			expect(s.on_fail).toBeDefined();
			const route = document.querySelector('[data-route="on_fail"]');
			expect(route).toHaveClass('hl');
			expect(Element.prototype.scrollIntoView).toHaveBeenCalledOnce();
			expect(vi.mocked(Element.prototype.scrollIntoView).mock.contexts[0]).toBe(route);
		});

		it('moves nothing with none picked', () => {
			setup(stage());
			expect(document.querySelector('.hl')).toBeNull();
			expect(Element.prototype.scrollIntoView).not.toHaveBeenCalled();
		});
	});

	it('names a verdict on the success of a stage that renders one', () => {
		const judge = anAgent('judge', { outputs: [{ name: 'v', type: 'verdict' }] });
		setup(aStage('a', 'agent', { agent: 'judge' }), [], { agents: [judge] });
		expect(screen.getByText('success').title).toMatch(
			/^success: where the chain goes once this stage approves/
		);
	});
});
