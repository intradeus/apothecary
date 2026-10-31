import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { aRun } from '$test/fixtures';
import { aPhase, aViewStore } from '$test/fixtures/run-views';
import PhasesView from './PhasesView.svelte';

vi.mock('$lib/api', () => ({
	api: {
		runs: { mrStatus: vi.fn(), phases: vi.fn(), startPhase: vi.fn() },
		system: { signals: vi.fn() }
	}
}));

const runNow = () => screen.findByRole('button', { name: 'Run now' });

describe('PhasesView', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		vi.mocked(api.runs.mrStatus).mockResolvedValue([]);
		vi.mocked(api.runs.phases).mockResolvedValue([aPhase()]);
		vi.mocked(api.system.signals).mockResolvedValue([]);
	});

	it('shows nothing and reads nothing before the run has loaded', () => {
		render(PhasesView, { rs: aViewStore(null) });
		expect(api.runs.phases).not.toHaveBeenCalled();
	});

	it('reads the run’s own chains', async () => {
		render(PhasesView, { rs: aViewStore(aRun({ idle: true })) });
		expect(await screen.findByText('fix-ci')).toBeInTheDocument();
		expect(api.runs.phases).toHaveBeenCalledWith('01TESTRUN0000000000000ABCD');
	});

	it('lets a watcher be started between chains', async () => {
		render(PhasesView, { rs: aViewStore(aRun({ idle: true })) });
		expect(await runNow()).toBeEnabled();
	});

	it('holds Run now while a chain has the run', async () => {
		render(PhasesView, { rs: aViewStore(aRun({ idle: false })) });
		expect(await runNow()).toBeDisabled();
	});

	it('starts the watcher that was clicked', async () => {
		vi.mocked(api.runs.startPhase).mockResolvedValue(undefined);
		render(PhasesView, { rs: aViewStore(aRun({ idle: true })) });

		await userEvent.click(await runNow());

		expect(api.runs.startPhase).toHaveBeenCalledExactlyOnceWith(
			'01TESTRUN0000000000000ABCD',
			'fix-ci',
			''
		);
	});

	it('keeps what ran on a finished run, without the buttons', async () => {
		render(PhasesView, { rs: aViewStore(aRun({ state: 'completed', idle: true })) });

		expect(await screen.findByText('fix-ci')).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Run now' })).not.toBeInTheDocument();
	});

	it.each([
		['the run going idle', { idle: true }],
		['another chain taking over', { phase: 'review' }],
		['the run ending', { state: 'completed' as const }]
	])('reads the chains again on %s', async (_, change) => {
		const rs = aViewStore(aRun());
		render(PhasesView, { rs });
		await screen.findByText('fix-ci');
		expect(api.runs.phases).toHaveBeenCalledOnce();

		rs.run = aRun(change);
		await tick();

		expect(api.runs.phases).toHaveBeenCalledTimes(2);
	});

	it('leaves the chains alone when nothing about them changed', async () => {
		const rs = aViewStore(aRun());
		render(PhasesView, { rs });
		await screen.findByText('fix-ci');

		rs.run = aRun({ title: 'Renamed' });
		await tick();

		expect(api.runs.phases).toHaveBeenCalledOnce();
	});
});
