import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { to } from '$lib/core/urls';
import type { ResourceSample } from '$lib/domain';
import { deferred } from '$test/helpers';
import { aCrew, aSample, aUnit } from '$test/fixtures/swarm';
import Dossier from './Dossier.svelte';

vi.mock('$lib/api', () => ({ api: { runs: { resources: vi.fn() } } }));

const resources = vi.mocked(api.runs.resources);
const nameOf = (id: string) => (id === 'p1' ? 'Billing' : id);

const crewed = aUnit({
	crew: aCrew({
		agent: 'builder',
		harness: 'omp',
		tool: 'Reading the schema',
		subagents: [
			{ toolCallId: 'c1', title: 'Scout the API', count: 2, since: '2026-10-31T12:00:00Z' },
			{ toolCallId: 'c2', title: 'Review the plan', count: 1, since: '2026-10-31T12:00:00Z' }
		]
	})
});

const mount = (unit = crewed) => {
	const onclose = vi.fn();
	const r = render(Dossier, { unit, nameOf, onclose });
	return { ...r, onclose };
};

beforeEach(() => {
	resources.mockReset();
	resources.mockResolvedValue([aSample()]);
});
afterEach(() => vi.useRealTimers());

describe('Dossier', () => {
	it('says what the unit is', () => {
		mount();
		expect(screen.getByRole('heading', { name: 'Add idempotency keys' })).toBeInTheDocument();
		expect(screen.getByText('Billing')).toBeInTheDocument();
		expect(screen.getByText('Running')).toBeInTheDocument();
		expect(screen.getAllByText('builder').length).toBeGreaterThan(0);
		expect(screen.getByText('Reading the schema')).toBeInTheDocument();
	});

	it('lists the drones the agent launched, with their counts', () => {
		mount();
		expect(screen.getByText('Scout the API')).toBeInTheDocument();
		expect(screen.getByText('×2')).toBeInTheDocument();
		expect(screen.getByText('Review the plan')).toBeInTheDocument();
	});

	it('says when the agent has launched no drones', () => {
		mount(aUnit({ crew: aCrew({ turnActive: false }) }));
		expect(screen.getByText('None launched.')).toBeInTheDocument();
		expect(screen.getByText('between turns')).toBeInTheDocument();
	});

	it('shows the phase, the workspace and an open question', () => {
		mount(aUnit({ phase: 'agent', envStatus: 'running', openPings: 2, held: true }));
		expect(screen.getByText('agent')).toBeInTheDocument();
		expect(screen.getByText('2')).toHaveClass('hot');
		expect(screen.getByText('yes')).toBeInTheDocument();
	});

	it('says when no agent is on station', () => {
		mount(aUnit());
		expect(screen.getByText('No agent on station.')).toBeInTheDocument();
	});

	it('loads the telemetry, showing it is loading meanwhile', async () => {
		const held = deferred<ResourceSample[]>();
		resources.mockReturnValue(held.promise);
		mount();
		expect(resources).toHaveBeenCalledWith('01TESTRUN0000000000000ABCD');
		expect(screen.getByRole('status')).toBeInTheDocument();
		held.resolve([aSample(), aSample({ at: '2026-10-31T12:00:10Z' })]);
		expect(await screen.findByText('CPU')).toBeInTheDocument();
		expect(screen.queryByRole('status')).not.toBeInTheDocument();
	});

	it('says why the telemetry did not load', async () => {
		resources.mockRejectedValue(new Error('boom'));
		mount();
		expect(await screen.findByText('Couldn’t load the samples: boom')).toBeInTheDocument();
	});

	it('reloads the telemetry while open, and stops once closed', async () => {
		vi.useFakeTimers();
		const { unmount } = mount();
		expect(resources).toHaveBeenCalledTimes(1);
		await vi.advanceTimersByTimeAsync(10_000);
		expect(resources).toHaveBeenCalledTimes(2);
		unmount();
		await vi.advanceTimersByTimeAsync(30_000);
		expect(resources).toHaveBeenCalledTimes(2);
	});

	it('loads the next unit’s telemetry when the unit changes', async () => {
		const { rerender } = mount();
		await rerender({ unit: aUnit({ runId: 'other' }) });
		expect(resources).toHaveBeenLastCalledWith('other');
	});

	it('links to the run', () => {
		mount();
		expect(screen.getByRole('link', { name: /Open run/ })).toHaveAttribute(
			'href',
			to.run('01TESTRUN0000000000000ABCD')
		);
	});

	it('closes from its button', async () => {
		const { onclose } = mount();
		await userEvent.click(screen.getByRole('button', { name: 'Close dossier' }));
		expect(onclose).toHaveBeenCalledOnce();
	});

	it('closes on Escape', async () => {
		const { onclose } = mount();
		await fireEvent.keyDown(window, { key: 'Escape' });
		expect(onclose).toHaveBeenCalledOnce();
	});

	it('ignores other keys', async () => {
		const { onclose } = mount();
		await fireEvent.keyDown(window, { key: 'Enter' });
		expect(onclose).not.toHaveBeenCalled();
	});
});
