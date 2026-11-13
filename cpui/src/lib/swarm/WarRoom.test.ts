import { fireEvent, render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { flushSync } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fleet } from '$lib/app/fleet.svelte';
import type { Limits } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { aProject } from '$test/fixtures';
import { aSwarm, aUnit } from '$test/fixtures/swarm';
import { swarmFeed } from './swarmFeed.svelte';
import WarRoom from './WarRoom.svelte';

vi.mock('$lib/api', () => ({
	api: { runs: { resources: vi.fn(() => new Promise(() => {})) } }
}));

const units = [
	aUnit({ runId: 'r1', projectId: 'p1', title: 'Add idempotency keys' }),
	aUnit({ runId: 'r2', projectId: 'p2', title: 'Fix the docs', state: 'waiting_on_human' })
];

beforeEach(() => {
	swarmFeed.swarm = null;
	swarmFeed.feed = [];
	swarmFeed.error = '';
	swarmFeed.paused = false;
	vi.spyOn(swarmFeed, 'use').mockReturnValue(() => {});
	vi.spyOn(swarmFeed, 'refresh').mockResolvedValue();
	fleet.reset();
	vi.spyOn(fleet, 'use').mockReturnValue(() => {});
	fleet.projects = [aProject({ id: 'p1', name: 'Billing' }), aProject({ id: 'p2', name: 'Docs' })];
	fleet.limits = { maxConcurrentRuns: 4, maxEnvironments: 0 } as Limits;
	toasts.list = [];
});
afterEach(() => vi.restoreAllMocks());

const board = () => screen.getByRole('region', { name: 'Tactical map' });

describe('WarRoom', () => {
	it('follows the swarm and the fleet while open', () => {
		render(WarRoom);
		expect(swarmFeed.use).toHaveBeenCalledOnce();
		expect(fleet.use).toHaveBeenCalledOnce();
	});

	it('shows it is loading before the first picture', () => {
		const { container } = render(WarRoom);
		expect(container.querySelector('[aria-busy="true"]')).not.toBeNull();
		expect(screen.queryByRole('region', { name: 'Tactical map' })).not.toBeInTheDocument();
	});

	it('says why the board could not load, and retries', async () => {
		swarmFeed.error = 'engine down';
		render(WarRoom);
		const card = screen.getByRole('alert');
		expect(card).toHaveTextContent('Couldn’t reach the war room');
		expect(card).toHaveTextContent('engine down');
		await userEvent.click(within(card).getByRole('button', { name: /Retry/ }));
		expect(swarmFeed.refresh).toHaveBeenCalledOnce();
	});

	it('says no units are deployed on an empty board', () => {
		swarmFeed.swarm = aSwarm([]);
		render(WarRoom);
		expect(screen.getByText('No units deployed')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Open tickets' })).toHaveAttribute('href', '/tickets');
	});

	it('draws the board: HUD, map by workshop, feed', () => {
		swarmFeed.swarm = aSwarm(units);
		render(WarRoom);
		expect(within(board()).getByText('Billing')).toBeInTheDocument();
		expect(within(board()).getByText('Docs')).toBeInTheDocument();
		expect(screen.getAllByRole('button', { name: /drones?$|attention$/ })).toHaveLength(2);
		expect(screen.getByText('Sandboxes online').nextElementSibling).toHaveTextContent('2/2');
		expect(screen.getByRole('list', { name: 'Kill feed' })).toBeInTheDocument();
		expect(screen.getByText('2 units on the board')).toBeInTheDocument();
	});

	it('keeps the last picture when a poll fails, and says so', () => {
		swarmFeed.swarm = aSwarm(units);
		swarmFeed.error = 'timeout';
		render(WarRoom);
		expect(board()).toBeInTheDocument();
		expect(screen.getByRole('status')).toHaveTextContent('timeout');
	});

	it('stands the board still while the tab was away', () => {
		swarmFeed.swarm = aSwarm(units);
		swarmFeed.paused = true;
		render(WarRoom);
		expect(board()).toHaveClass('paused');
	});

	it('opens a unit’s dossier when it is selected, and closes it', async () => {
		swarmFeed.swarm = aSwarm(units);
		render(WarRoom);
		expect(screen.queryByRole('complementary')).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /^Fix the docs,/ }));
		const dossier = screen.getByRole('complementary', { name: 'Unit dossier' });
		expect(within(dossier).getByRole('heading', { name: 'Fix the docs' })).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: 'Close dossier' }));
		expect(screen.queryByRole('complementary')).not.toBeInTheDocument();
	});

	it('closes the dossier when its unit leaves the board', async () => {
		swarmFeed.swarm = aSwarm(units);
		render(WarRoom);
		await userEvent.click(screen.getByRole('button', { name: /^Fix the docs,/ }));
		swarmFeed.swarm = aSwarm([units[0]]);
		flushSync();
		expect(screen.queryByRole('complementary')).not.toBeInTheDocument();
		swarmFeed.swarm = aSwarm(units);
		flushSync();
		expect(screen.queryByRole('complementary')).not.toBeInTheDocument();
	});

	describe('fullscreen', () => {
		beforeEach(() => {
			swarmFeed.swarm = aSwarm(units);
		});

		it('puts the board fullscreen, and back', async () => {
			const request = vi.fn(() => Promise.resolve());
			const exit = vi.fn(() => Promise.resolve());
			HTMLElement.prototype.requestFullscreen = request;
			document.exitFullscreen = exit;
			render(WarRoom);
			await userEvent.click(screen.getByRole('button', { name: /^\S+ Fullscreen$/ }));
			expect(request).toHaveBeenCalledOnce();
			expect(request.mock.contexts[0]).toBe(board());
			await userEvent.click(screen.getByRole('button', { name: /Exit fullscreen/ }));
			expect(exit).toHaveBeenCalledOnce();
			expect(screen.getByRole('button', { name: /^\S+ Fullscreen$/ })).toBeInTheDocument();
		});

		it('follows the browser leaving fullscreen on its own', async () => {
			HTMLElement.prototype.requestFullscreen = vi.fn(() => Promise.resolve());
			render(WarRoom);
			await userEvent.click(screen.getByRole('button', { name: /^\S+ Fullscreen$/ }));
			expect(screen.getByRole('button', { name: /Exit fullscreen/ })).toBeInTheDocument();
			await fireEvent(document, new Event('fullscreenchange'));
			expect(screen.getByRole('button', { name: /^\S+ Fullscreen$/ })).toBeInTheDocument();
		});

		it('says so when the browser refuses', async () => {
			HTMLElement.prototype.requestFullscreen = vi.fn(() => Promise.reject(new Error('denied')));
			render(WarRoom);
			await userEvent.click(screen.getByRole('button', { name: /^\S+ Fullscreen$/ }));
			expect(toasts.list.map((t) => t.text)).toEqual(['Fullscreen isn’t available here: denied']);
		});
	});
});
