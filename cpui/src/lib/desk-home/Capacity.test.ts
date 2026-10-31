import { render, screen } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fleet } from '$lib/app/fleet.svelte';
import type { Limits } from '$lib/domain';
import { aRun } from '$test/fixtures';
import Capacity from './Capacity.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

beforeEach(() => {
	fleet.reset();
	fleet.limits = { maxConcurrentRuns: 4, maxEnvironments: 6 } as Limits;
	fleet.paused = false;
});

describe('Capacity', () => {
	it('meters runs and environments against the caps', () => {
		fleet.runs = [aRun({ id: 'a', envId: 'e1' }), aRun({ id: 'b', state: 'paused', envId: 'e2' })];
		render(Capacity);
		expect(screen.getByText('1/4')).toBeInTheDocument();
		expect(screen.getByText('2/6')).toBeInTheDocument();
	});

	it('says the queue is empty', () => {
		render(Capacity);
		expect(screen.getByText('0 pending')).toBeInTheDocument();
		expect(screen.getByText('nothing waiting for a slot')).toBeInTheDocument();
	});

	it('names the oldest queued run as next', () => {
		fleet.runs = [
			aRun({ id: 'b', state: 'pending', title: 'Later', createdAt: '2026-10-02T00:00:00Z' }),
			aRun({ id: 'a', state: 'pending', title: 'First', createdAt: '2026-10-01T00:00:00Z' })
		];
		render(Capacity);
		expect(screen.getByText('2 pending')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'First' })).toHaveAttribute('href', '/runs/a');
		expect(screen.queryByRole('link', { name: 'Later' })).not.toBeInTheDocument();
	});

	it('links to the limits', () => {
		render(Capacity);
		expect(screen.getByRole('link', { name: /Limits/ })).toHaveAttribute('href', '/system/limits');
	});

	it('says the numbers are paused while the tab was away', () => {
		fleet.paused = true;
		render(Capacity);
		expect(screen.getAllByText('paused').length).toBeGreaterThan(0);
	});
});
