import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { aSignal, aStage, handlers, live, optionsOf, pick } from '$test/fixtures/flow.svelte';
import type { DocStage } from '$lib/domain/flowdoc';
import WaitStage from './WaitStage.svelte';

const signals = [
	aSignal('ci_passed', { title: 'CI passed', description: 'Every check is green.' }),
	aSignal('mr_merged', { title: 'Merged', requires: 'repository' }),
	aSignal('manual', { title: 'A person', kind: 'hand' })
];

const setup = (over: Partial<DocStage> = {}) => {
	const stage = live(aStage('hold', 'wait', over));
	const { onchange } = handlers();
	render(WaitStage, { stage, signals, onchange });
	return { stage, onchange };
};

describe('WaitStage', () => {
	it('writes the signal it waits for', async () => {
		const { stage, onchange } = setup();
		await pick('Wait for', 'CI passed');
		expect(stage.for).toBe('ci_passed');
		expect(onchange).toHaveBeenCalledOnce();
	});

	it('takes the signal off with the empty choice', async () => {
		const { stage } = setup({ for: 'ci_passed' });
		await pick('Wait for', '—');
		expect(stage.for).toBeUndefined();
	});

	it('offers observed signals only: a wait cannot wait for a person', async () => {
		setup();
		expect(await optionsOf('Wait for')).toEqual(['—', 'CI passed', 'Merged']);
	});

	it('says what the chosen signal is', () => {
		setup({ for: 'ci_passed' });
		expect(screen.getByText('Every check is green.')).toBeInTheDocument();
	});

	it('says which integration the signal needs', () => {
		setup({ for: 'mr_merged' });
		expect(screen.getByText(/integration on the project running this flow/)).toBeInTheDocument();
	});

	it('warns of a signal the engine does not have', () => {
		setup({ for: 'gone' });
		expect(screen.getByText('Unknown signal “gone”.')).toBeInTheDocument();
	});

	it('waits forever until a timeout is set', async () => {
		const { stage, onchange } = setup();
		expect(screen.getByText('waits indefinitely')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: 'Timeout' }));
		expect(stage.timeout).toBe('24h');
		expect(onchange).toHaveBeenCalledOnce();
	});

	it('clears a timeout', async () => {
		const { stage } = setup({ timeout: '2h' });
		await userEvent.click(screen.getByRole('button', { name: 'Clear Timeout' }));
		expect(stage.timeout).toBeUndefined();
		expect(screen.getByText('waits indefinitely')).toBeInTheDocument();
	});
});
