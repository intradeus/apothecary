import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { aDoc, aSequence, aStage, fill, handlers, pick } from '$test/fixtures/flow.svelte';
import type { DocStage, FlowDoc } from '$lib/domain/flowdoc';
import ServiceStage from './ServiceStage.svelte';

const setup = (over: Partial<DocStage> = {}, doc: Partial<FlowDoc> = {}, more: DocStage[] = []) => {
	const d = aDoc({ stages: [aStage('up', 'service', over), ...more], ...doc });
	const { onchange, onopen } = handlers();
	render(ServiceStage, { doc: d, stage: d.stages[0], onchange, onopen });
	return { stage: d.stages[0], onchange, onopen };
};

describe('ServiceStage', () => {
	it('writes the service it names', async () => {
		const { stage, onchange } = setup();
		await fill(screen.getByLabelText('Service'), 'web');
		expect(stage.service).toBe('web');
		expect(onchange).toHaveBeenCalled();
	});

	it('suggests string parameters and the services the flow names', () => {
		setup({}, { parameters: { app: { type: 'string', default: 'web' }, n: { type: 'int' } } }, [
			aStage('db', 'service', { service: 'postgres' })
		]);
		const offered = [...document.querySelectorAll('#flow-services option')].map((o) =>
			o.getAttribute('value')
		);
		expect(offered).toEqual(['$app', 'postgres']);
	});

	it('writes nothing for the default action', async () => {
		const { stage } = setup({ do: 'restart' });
		await pick('Does', /^start/);
		expect(stage.do).toBeUndefined();
	});

	it('writes what it does to the service', async () => {
		const { stage, onchange } = setup();
		await pick('Does', /^restart/);
		expect(stage.do).toBe('restart');
		expect(onchange).toHaveBeenCalledOnce();
	});

	// A stop has nothing to wait for: a timeout left on it would not validate.
	it('drops the timeout on a stop, and the field with it', async () => {
		const { stage } = setup({ timeout: '5m' });
		await pick('Does', 'stop');
		expect(stage.do).toBe('stop');
		expect(stage.timeout).toBeUndefined();
		expect(screen.queryByLabelText('Timeout')).not.toBeInTheDocument();
	});

	it('writes a timeout while it waits for the service', async () => {
		const { stage } = setup();
		await fill(screen.getByLabelText('Timeout'), '2m');
		expect(stage.timeout).toBe('2m');
	});

	it('gives the address the service is read at, $app before it is named', () => {
		setup();
		expect(screen.getByText('run.service.$app')).toBeInTheDocument();
	});

	it('lists the stages reading its address, in chains and sequences', async () => {
		const reads = { input: { app: { from: 'run.service.web' } } };
		const { onopen } = setup(
			{ service: 'web' },
			{ sequences: [aSequence('smoke', [aStage('probe', 'agent', reads)])] },
			[aStage('check', 'agent', reads), aStage('other', 'agent')]
		);
		await userEvent.click(screen.getByRole('button', { name: 'check' }));
		expect(onopen).toHaveBeenLastCalledWith({ kind: 'stage', id: 'check' });
		await userEvent.click(screen.getByRole('button', { name: 'smoke › probe' }));
		expect(onopen).toHaveBeenLastCalledWith({ kind: 'stage', id: 'probe', sequence: 'smoke' });
		expect(screen.queryByRole('button', { name: 'other' })).not.toBeInTheDocument();
	});
});
