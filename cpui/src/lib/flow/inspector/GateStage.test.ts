import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { aDoc, aStage, fill, handlers } from '$test/fixtures/flow.svelte';
import type { DocStage } from '$lib/domain/flowdoc';
import GateStage from './GateStage.svelte';

const setup = (over: Partial<DocStage> = {}, others: DocStage[] = []) => {
	const doc = aDoc({ stages: [aStage('gate', 'human_gate', over), ...others] });
	const { onchange } = handlers();
	render(GateStage, { doc, stage: doc.stages[0], onchange });
	return { doc, stage: doc.stages[0], onchange };
};

describe('GateStage', () => {
	it('writes the question to the stage', async () => {
		const { stage, onchange } = setup();
		await fill(screen.getByLabelText('Question'), 'Ship it?');
		expect(stage.question).toBe('Ship it?');
		expect(onchange).toHaveBeenCalled();
	});

	it('adds a file for the person to see', async () => {
		const { stage, onchange } = setup();
		await userEvent.click(screen.getByRole('button', { name: /Add/ }));
		await fill(screen.getByLabelText('Name'), 'plan');
		await fill(screen.getByRole('combobox', { name: /From/ }), 'artifacts/plan.md');
		expect(stage.input).toEqual({ plan: { from: 'artifacts/plan.md', type: 'file' } });
		expect(onchange).toHaveBeenCalled();
	});

	it('offers what the flow writes, and only one link', () => {
		setup({ input: { app: { from: 'run.workspace', type: 'link' }, mr: { from: 'run.mr.url' } } }, [
			aStage('plan', 'agent', { output: { plan: { to: 'artifacts/plan.md' } } })
		]);
		expect(screen.getAllByText(/A gate opens at most one link/)).toHaveLength(1);
		expect(
			document.querySelector('#ports-gate-input-paths option[value="artifacts/plan.md"]')
		).not.toBeNull();
	});

	it('explains curation once a review is shown', () => {
		setup({ input: { review: { from: 'artifacts/review.json', type: 'review_threads' } } });
		expect(screen.getByText(/opens the curation page/)).toBeInTheDocument();
	});

	it('says nothing of curation for a plain file', () => {
		setup({ input: { plan: { from: 'artifacts/plan.md', type: 'file' } } });
		expect(screen.queryByText(/opens the curation page/)).not.toBeInTheDocument();
	});
});
