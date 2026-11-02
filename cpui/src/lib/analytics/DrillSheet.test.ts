import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { DrillRun } from '$lib/domain';
import { aPanel, aResult } from '$test/fixtures/analytics';
import { deferred } from '$test/helpers';
import DrillSheet from './DrillSheet.svelte';

vi.mock('$lib/api', () => ({ api: { analytics: { drill: vi.fn() } } }));

const aDrillRun = (over: Partial<DrillRun> = {}): DrillRun => ({
	runId: 'r1',
	title: 'Add idempotency keys',
	flowName: 'builder',
	flowHash: 'abc',
	state: 'completed',
	createdAt: '2026-10-01T00:00:00Z',
	wallMs: 90_000,
	costUsd: 0.42,
	...over
});

const props = { open: true, title: 'Cost by version', query: aPanel().query };

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.analytics.drill).mockResolvedValue([
		aDrillRun(),
		aDrillRun({ runId: 'r2', title: '' })
	]);
});

describe('DrillSheet', () => {
	it('lists the runs behind the panel, each opening its run', async () => {
		render(DrillSheet, props);
		const link = await screen.findByRole('link', { name: 'Add idempotency keys' });
		expect(link).toHaveAttribute('href', '/runs/r1');
		expect(screen.getByRole('link', { name: 'Untitled run' })).toHaveAttribute('href', '/runs/r2');
		expect(screen.getByText('2 runs, newest first')).toBeInTheDocument();
		expect(screen.getAllByText('$0.420')).toHaveLength(2);
		expect(api.analytics.drill).toHaveBeenCalledWith('runs', [], {});
	});

	it('names the clicked row under the title', async () => {
		const r = aResult();
		render(DrillSheet, { ...props, result: r, row: r.rows[0] });
		expect(await screen.findByText('builder v3')).toBeInTheDocument();
		expect(api.analytics.drill).toHaveBeenCalledWith(
			'runs',
			[{ field: 'flow_hash', op: 'eq', values: ['abc'] }],
			{}
		);
	});

	it('shows a skeleton while it asks', () => {
		vi.mocked(api.analytics.drill).mockReturnValue(deferred<DrillRun[]>().promise);
		render(DrillSheet, props);
		expect(screen.getByRole('dialog').querySelector('.skel')).not.toBeNull();
	});

	it('says when no run is behind it', async () => {
		vi.mocked(api.analytics.drill).mockResolvedValue([]);
		render(DrillSheet, props);
		expect(await screen.findByText('No runs behind this')).toBeInTheDocument();
	});

	it('shows a failure and retries', async () => {
		vi.mocked(api.analytics.drill).mockRejectedValueOnce(new Error('boom'));
		render(DrillSheet, props);
		expect(await screen.findByText('Couldn’t list the runs')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('2 runs, newest first')).toBeInTheDocument();
	});

	it('closes', async () => {
		render(DrillSheet, props);
		await userEvent.click(screen.getByRole('button', { name: 'Close' }));
		await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
	});

	it('asks nothing while closed', () => {
		render(DrillSheet, { ...props, open: false });
		expect(api.analytics.drill).not.toHaveBeenCalled();
	});
});
