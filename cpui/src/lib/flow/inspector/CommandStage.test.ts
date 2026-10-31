import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { aDoc, aStage, fill, handlers } from '$test/fixtures/flow.svelte';
import type { DocStage, FlowDoc } from '$lib/domain/flowdoc';
import CommandStage from './CommandStage.svelte';

const setup = (over: Partial<DocStage> = {}, doc: Partial<FlowDoc> = {}) => {
	const d = aDoc({ stages: [aStage('test', 'command', over)], ...doc });
	const { onchange } = handlers();
	render(CommandStage, { doc: d, stage: d.stages[0], onchange });
	return { stage: d.stages[0], onchange };
};

describe('CommandStage', () => {
	it.each([
		['run_ref', 'run_ref', 'verify'],
		['…or literal script', 'run', 'make test'],
		['Timeout', 'timeout', '5m']
	] as const)('writes %s to the stage', async (label, field, value) => {
		const { stage, onchange } = setup();
		await fill(screen.getByLabelText(label), value);
		expect(stage[field]).toBe(value);
		expect(onchange).toHaveBeenCalled();
	});

	it('suggests parameters for the fields that read one', () => {
		setup();
		expect(screen.getByLabelText('run_ref')).toHaveAttribute('list', 'flow-params');
		expect(screen.getByLabelText('Timeout')).toHaveAttribute('list', 'flow-duration-params');
	});

	it('shows the flow timeout it would otherwise run under', () => {
		setup({}, { parameters: { timeout: { default: '45m' } } });
		expect(screen.getByLabelText('Timeout')).toHaveAttribute('placeholder', '45m');
	});

	it('writes each_repo only while it is on', async () => {
		const { stage, onchange } = setup({ each_repo: true });
		const toggle = screen.getByRole('switch', { name: 'Run in each repository' });
		expect(toggle).toBeChecked();
		await userEvent.click(toggle);
		expect(stage.each_repo).toBeUndefined();
		expect(onchange).toHaveBeenCalledOnce();
	});

	it('keeps what it reads and what it writes apart', async () => {
		const { stage } = setup({ output: { report: { to: 'artifacts/report.json' } } });
		const add = screen.getAllByRole('button', { name: /Add/ });
		expect(add).toHaveLength(2);

		await userEvent.click(add[0]);
		const from = screen.getByPlaceholderText('artifacts/thresholds.json');
		await fill(from, 'artifacts/limits.json');
		const name = within(from.closest('.port') as HTMLElement).getByLabelText('Name');
		await fill(name, 'limits');

		expect(stage.input).toEqual({ limits: { from: 'artifacts/limits.json', type: 'file' } });
		expect(stage.output).toEqual({ report: { to: 'artifacts/report.json' } });
	});
});
