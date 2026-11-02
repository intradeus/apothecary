import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import {
	aDoc,
	anAgent,
	aStage,
	aWatcher,
	fill,
	handlers,
	MAIN_PHASE
} from '$test/fixtures/flow.svelte';
import type { DocAgent, DocStage, FlowDoc } from '$lib/domain/flowdoc';
import type { HarnessSpec } from '$lib/domain';
import AgentStage from './AgentStage.svelte';

const planner = anAgent('planner', {
	inputs: [{ name: 'ticket', type: 'ticket' }],
	outputs: [{ name: 'plan', type: 'file' }]
});

const ompSpec: HarnessSpec = {
	kind: 'omp',
	options: [
		{ name: 'advisor', kind: 'model', description: 'the advisor' },
		{ name: 'advisor_review', kind: 'enum', values: ['turn', 'agent-end'], needs: 'advisor' },
		{ name: 'smol', kind: 'model' },
		{ name: 'slow', kind: 'model' }
	]
};

const setup = (
	over: Partial<DocStage> = {},
	agents: DocAgent[] = [planner],
	doc: Partial<FlowDoc> = {},
	harnessSpecs: HarnessSpec[] = []
) => {
	const d = aDoc({ stages: [aStage('plan', 'agent', over)], agents, ...doc });
	const { onchange, onopen } = handlers();
	render(AgentStage, { doc: d, stage: d.stages[0], agents, harnessSpecs, onchange, onopen });
	return { doc: d, stage: d.stages[0], onchange, onopen };
};

describe('AgentStage', () => {
	it.each([
		['Agent', 'agent', 'planner'],
		[/Harness/, 'harness', '$harness'],
		[/Model/, 'model', '$model'],
		['Timeout', 'timeout', '5m']
	] as const)('writes %s to the stage', async (label, field, value) => {
		const { stage, onchange } = setup();
		await fill(screen.getByLabelText(label), value);
		expect(stage[field]).toBe(value);
		expect(onchange).toHaveBeenCalled();
	});

	// Every agent stage says what it runs on: there is no silent fallback.
	it('marks the harness and the model required until they are set', () => {
		setup();
		expect(screen.getByLabelText(/Harness/).closest('.field')).toHaveTextContent('required');
		expect(screen.getByLabelText(/Model/).closest('.field')).toHaveTextContent('required');
	});

	it('stops asking once they are', () => {
		setup({ harness: '$harness', model: '$model' });
		expect(screen.queryByText('required')).not.toBeInTheDocument();
	});

	describe('max turns', () => {
		it('writes a number as a number', async () => {
			const { stage, onchange } = setup();
			await fill(screen.getByLabelText('Max turns'), '12');
			expect(stage.max_turns).toBe(12);
			expect(onchange).toHaveBeenCalled();
		});

		it('writes a parameter as text', async () => {
			const { stage } = setup();
			await fill(screen.getByLabelText('Max turns'), '$turns');
			expect(stage.max_turns).toBe('$turns');
		});

		it('takes it off when emptied', async () => {
			const { stage } = setup({ max_turns: 12 });
			await fill(screen.getByLabelText('Max turns'), '');
			expect(stage.max_turns).toBeUndefined();
		});

		it('shows the flow default as the placeholder', () => {
			setup({}, [planner], { parameters: { max_turns: { default: 80 } } });
			expect(screen.getByLabelText('Max turns')).toHaveAttribute('placeholder', '80');
		});
	});

	// Absent means on: the document only ever says the off.
	it('writes reuse_session only to turn it off', async () => {
		const { stage, onchange } = setup();
		const toggle = screen.getByRole('switch', { name: 'Remember earlier attempts' });
		expect(toggle).toBeChecked();

		await userEvent.click(toggle);
		expect(stage.reuse_session).toBe(false);

		await userEvent.click(toggle);
		expect(stage.reuse_session).toBeUndefined();
		expect(onchange).toHaveBeenCalledTimes(2);
	});

	describe('the agent it runs', () => {
		it('opens a declared agent', async () => {
			const { onopen } = setup({ agent: 'planner' });
			await userEvent.click(screen.getByRole('button', { name: 'Edit agent planner' }));
			expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'agent', name: 'planner' });
		});

		it('declares the agent it names', async () => {
			const { doc, onchange, onopen } = setup({ agent: 'reviewer' });
			expect(screen.getByText(/“reviewer” is not declared/)).toBeInTheDocument();
			await userEvent.click(screen.getByRole('button', { name: 'Declare agent reviewer' }));
			expect(doc.agents?.map((a) => a.name)).toEqual(['planner', 'reviewer']);
			expect(onchange).toHaveBeenCalledOnce();
			expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'agent', name: 'reviewer' });
		});

		it('offers a fresh agent when the name could not be one', async () => {
			const { doc, onopen } = setup({ agent: '$agent' });
			await userEvent.click(screen.getByRole('button', { name: 'New agent' }));
			expect(doc.agents?.map((a) => a.name)).toEqual(['planner', 'agent_1']);
			expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'agent', name: 'agent_1' });
		});

		it('says so when it declares no inputs', () => {
			setup({ agent: 'bare' }, [anAgent('bare')]);
			expect(screen.getByText(/“bare” declares no inputs/)).toBeInTheDocument();
		});
	});

	describe('its contract', () => {
		it('wires a declared input and output', async () => {
			const { stage, onchange } = setup({ agent: 'planner' });
			await fill(screen.getByRole('combobox', { name: /From/ }), 'run.ticket');
			await fill(screen.getByLabelText(/^To/), 'artifacts/plan.md');
			expect(stage.input).toEqual({ ticket: { from: 'run.ticket', type: 'ticket' } });
			expect(stage.output).toEqual({ plan: { to: 'artifacts/plan.md', type: 'file' } });
			expect(onchange).toHaveBeenCalled();
		});

		const sources = () =>
			[...document.querySelectorAll('#ports-plan-input option')].map((o) =>
				o.getAttribute('value')
			);

		it('offers run.ci.failure only on a chain opened by a failed CI', () => {
			setup({ agent: 'planner' });
			expect(sources()).not.toContain('run.ci.failure');
		});

		it('offers run.ci.failure on a chain opened by a failed CI', () => {
			setup({ agent: 'planner', phase: 'fix' }, [planner], {
				phases: [{ ...MAIN_PHASE }, aWatcher('fix', { on: { signal: 'ci_failed' } })]
			});
			expect(sources()).toContain('run.ci.failure');
		});

		it('explains the verdict an agent renders', () => {
			setup({ agent: 'judge' }, [anAgent('judge', { outputs: [{ name: 'v', type: 'verdict' }] })]);
			expect(screen.getByText(/Renders a verdict/)).toBeInTheDocument();
		});

		it('warns of a rejection route that can never fire', () => {
			setup({ agent: 'planner', on_reject: { to: 'plan', max: 3 } });
			expect(screen.getByText(/will never fire/)).toBeInTheDocument();
		});
	});

	describe('harness options', () => {
		it('shows every kind while the harness is a parameter', () => {
			setup({ harness: '$harness' }, [planner], {}, [ompSpec]);
			expect(screen.getByText('Harness options')).toBeInTheDocument();
			expect(screen.getByLabelText('advisor')).toBeInTheDocument();
		});

		it('writes an option into its kind block', async () => {
			const { stage, onchange } = setup({ harness: '$harness' }, [planner], {}, [ompSpec]);
			await fill(screen.getByLabelText('advisor'), '$adv');
			expect(stage.harness_options).toEqual({ omp: { advisor: '$adv' } });
			expect(onchange).toHaveBeenCalled();
		});

		it('drops the block once cleared', async () => {
			const { stage } = setup(
				{ harness: 'omp', harness_options: { omp: { advisor: '$adv' } } },
				[planner],
				{},
				[ompSpec]
			);
			await userEvent.clear(screen.getByLabelText('advisor'));
			expect(stage.harness_options).toBeUndefined();
		});

		it('shows nothing for a harness that takes no options', () => {
			setup({ harness: 'codex' }, [planner], {}, [ompSpec]);
			expect(screen.queryByText('Harness options')).not.toBeInTheDocument();
		});
	});
});
