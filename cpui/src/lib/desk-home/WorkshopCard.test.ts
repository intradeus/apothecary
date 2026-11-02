import { render, screen } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { aProject, aRun } from '$test/fixtures';
import WorkshopCard from './WorkshopCard.svelte';

vi.mock('$lib/api', () => ({
	api: { runs: { get: vi.fn() }, flows: { version: vi.fn() } }
}));

const project = aProject({ id: 'p1', name: 'billing' });
const now = new Date('2026-10-31T12:00:00Z').getTime();
const props = { project, live: [], liveTotal: 0, now };

beforeEach(() => {
	vi.mocked(api.runs.get).mockReset();
	vi.mocked(api.flows.version).mockReset();
	fleet.reset();
	fleet.projects = [project];
});
afterEach(() => vi.clearAllTimers());

describe('WorkshopCard, the head', () => {
	it('links the workshop and reads idle with nothing live', () => {
		render(WorkshopCard, props);
		expect(screen.getByRole('link', { name: 'billing' })).toHaveAttribute('href', '/w/p1');
		expect(screen.getByText('idle')).toBeInTheDocument();
	});

	it('counts all live runs, not only the ones the filter shows', () => {
		render(WorkshopCard, { ...props, live: [aRun({ id: 'cnt' })], liveTotal: 3 });
		expect(screen.getByText(/3 live/)).toBeInTheDocument();
	});

	it('adds pending pings, singular and plural', () => {
		const { unmount } = render(WorkshopCard, { ...props, pings: 1 });
		expect(screen.getByText('1 ping')).toBeInTheDocument();
		unmount();
		render(WorkshopCard, { ...props, pings: 2 });
		expect(screen.getByText('2 pings')).toBeInTheDocument();
	});
});

describe('WorkshopCard, its runs', () => {
	const stage = (over: Parameters<typeof aRun>[0]) =>
		render(WorkshopCard, { ...props, live: [aRun(over)], liveTotal: 1 });

	it('shows the stage a live run is on', () => {
		stage({ id: 'r-stage', currentStage: 'build' });
		expect(screen.getByText('build')).toBeInTheDocument();
	});

	it('says a provisioning run is starting and a pending one is queued', () => {
		const { unmount } = stage({ id: 'r-prov', state: 'provisioning' });
		expect(screen.getByText('workspace starting')).toBeInTheDocument();
		unmount();
		stage({ id: 'r-pend', state: 'pending' });
		expect(screen.getByText('queued')).toBeInTheDocument();
	});

	it('shows elapsed time, cost and attempt from the run’s facts', () => {
		stage({
			id: 'r-facts',
			currentStage: 'build',
			createdAt: new Date(now - 90_000).toISOString(),
			facts: {
				...aRun().facts,
				costUsd: 1.5,
				open: { build: { iteration: 2, startedAt: '', outcome: 'running' } }
			}
		});

		expect(screen.getByText('build · attempt 2')).toBeInTheDocument();
		expect(screen.getByText('$1.500')).toBeInTheDocument();
		expect(screen.getByText(/1m/)).toBeInTheDocument();
		expect(api.runs.get).not.toHaveBeenCalled();
	});

	it('heads last finished runs only when there are some', () => {
		const { unmount } = render(WorkshopCard, props);
		expect(screen.queryByText('Last finished')).not.toBeInTheDocument();
		unmount();

		render(WorkshopCard, {
			...props,
			finished: [aRun({ id: 'r-done', title: 'Shipped', state: 'completed' })]
		});
		expect(screen.getByText('Last finished')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Shipped' })).toHaveAttribute('href', '/runs/r-done');
	});
});

describe('WorkshopCard, the foot', () => {
	it('reads Tickets until the count loads', () => {
		render(WorkshopCard, props);
		expect(screen.getByRole('link', { name: 'Tickets' })).toHaveAttribute(
			'href',
			'/tickets?project=p1'
		);
	});

	it('counts tickets once loaded', () => {
		render(WorkshopCard, { ...props, tickets: 7 });
		expect(screen.getByRole('link', { name: '7 tickets' })).toBeInTheDocument();
	});

	it('starts a ticket in this workshop', () => {
		render(WorkshopCard, props);
		expect(screen.getByRole('link', { name: /New ticket/ })).toHaveAttribute(
			'href',
			'/tickets/new?project=p1'
		);
	});
});
