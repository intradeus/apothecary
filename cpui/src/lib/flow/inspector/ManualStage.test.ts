import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { aDoc, aStage, fill, handlers } from '$test/fixtures/flow.svelte';
import type { DocStage } from '$lib/domain/flowdoc';
import ManualStage from './ManualStage.svelte';

const setup = (over: Partial<DocStage> = {}) => {
	const doc = aDoc({ stages: [aStage('work', 'manual', over)] });
	const { onchange } = handlers();
	render(ManualStage, { doc, stage: doc.stages[0], onchange });
	return { doc, stage: doc.stages[0], onchange };
};

describe('ManualStage', () => {
	it('shows what the person is to do', () => {
		setup({ question: 'Build it' });
		expect(screen.getByLabelText('What to do')).toHaveValue('Build it');
	});

	it('writes the instruction to the stage', async () => {
		const { stage, onchange } = setup();
		await fill(screen.getByLabelText('What to do'), 'Fix the tests');
		expect(stage.question).toBe('Fix the tests');
		expect(onchange).toHaveBeenCalled();
	});
});
