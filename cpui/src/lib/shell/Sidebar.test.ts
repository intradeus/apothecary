import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from '$app/state';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { prefs } from '$lib/app/prefs.svelte';
import { session } from '$lib/app/session.svelte';
import type { Limits, Me } from '$lib/domain';
import { aPing, aProject, aRun } from '$test/fixtures';
import Sidebar from './Sidebar.svelte';

vi.mock('$lib/api', () => ({ api: { auth: { logout: vi.fn() } } }));

const at = (path: string, params: Record<string, string> = {}) => {
	page.url = new URL(path, 'http://localhost') as typeof page.url;
	page.params = params;
};
const nav = () => within(screen.getByRole('navigation', { name: 'Primary' }));
const billing = aProject({ id: 'p1', name: 'billing' });
const alpha = aProject({ id: 'p2', name: 'alpha' });
const zeta = aProject({ id: 'p3', name: 'zeta' });
const liveRun = aRun({ id: '01RUNABCD', title: 'Fix the invoice', projectId: 'p1' });

beforeEach(() => {
	vi.resetAllMocks();
	at('/');
	fleet.reset();
	fleet.limits = null;
	fleet.projects = [billing, alpha, zeta];
	prefs.collapsed = false;
	session.me = { user: { id: 'u', handle: 'lu', name: 'Lu', admin: true } } as Me;
});

describe('Sidebar, full', () => {
	it('lights the page it is on', () => {
		at('/inbox/01PING');
		render(Sidebar, { onsearch: vi.fn() });
		expect(nav().getByRole('link', { name: /Inbox/ })).toHaveAttribute('aria-current', 'page');
	});

	it('counts pending pings on the inbox and live runs on runs', () => {
		fleet.runs = [liveRun, aRun({ id: 'b', state: 'completed' })];
		fleet.pending = [aPing({ id: '1' }), aPing({ id: '2' })];
		render(Sidebar, { onsearch: vi.fn() });
		expect(nav().getByRole('link', { name: /Inbox/ })).toHaveTextContent('2');
		expect(nav().getByRole('link', { name: /Runs/ })).toHaveTextContent('1');
		expect(nav().getByRole('link', { name: /Tickets/ })).not.toHaveTextContent(/\d/);
	});

	it('puts workshops with live runs first, then by name', () => {
		fleet.runs = [
			aRun({ id: 'a', projectId: 'p3' }),
			aRun({ id: 'b', projectId: 'p3' }),
			aRun({ id: 'c', projectId: 'p1' })
		];
		render(Sidebar, { onsearch: vi.fn() });
		const names = screen
			.getAllByRole('link')
			.filter((a) => /^\/w\/p/.test(a.getAttribute('href') ?? ''))
			.map((a) => a.textContent);
		expect(names[0]).toContain('zeta');
		expect(names[1]).toContain('billing');
		expect(names[2]).toContain('alpha');
	});

	it.each([
		['its own page', () => at('/w/p1', { workshop: 'p1' })],
		['one of its runs', () => at('/runs/01RUNABCD', { runId: '01RUNABCD' })],
		['a list filtered on it', () => at('/tickets?project=p1')]
	])('expands the workshop the page is in: %s', (_, go) => {
		fleet.runs = [liveRun];
		go();
		render(Sidebar, { onsearch: vi.fn() });
		expect(screen.getByRole('link', { name: /Fix the invoice/ })).toBeInTheDocument();
	});

	it('expands nothing on a page outside any workshop', () => {
		fleet.runs = [liveRun];
		render(Sidebar, { onsearch: vi.fn() });
		expect(screen.queryByRole('link', { name: /Fix the invoice/ })).not.toBeInTheDocument();
	});

	it('meters capacity', () => {
		fleet.limits = { maxConcurrentRuns: 5, maxEnvironments: 2 } as Limits;
		fleet.runs = [aRun({ id: 'a', envId: 'e1' })];
		render(Sidebar, { onsearch: vi.fn() });
		expect(screen.getByText('1/5')).toBeInTheDocument();
		expect(screen.getByText('1/2')).toBeInTheDocument();
	});

	it('opens the search', async () => {
		const onsearch = vi.fn();
		render(Sidebar, { onsearch });
		await userEvent.click(screen.getByRole('button', { name: /Search, jump, do/ }));
		expect(onsearch).toHaveBeenCalledOnce();
	});
});

describe('Sidebar, collapsing', () => {
	it('turns into the rail and back, as one surface', async () => {
		render(Sidebar, { onsearch: vi.fn() });
		await userEvent.click(screen.getByRole('button', { name: 'Collapse sidebar' }));

		expect(prefs.collapsed).toBe(true);
		expect(await screen.findByRole('button', { name: 'Expand sidebar' })).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.queryByRole('button', { name: 'Collapse sidebar' })).not.toBeInTheDocument()
		);

		await userEvent.click(screen.getByRole('button', { name: 'Expand sidebar' }));
		expect(prefs.collapsed).toBe(false);
		expect(await screen.findByRole('button', { name: 'Collapse sidebar' })).toBeInTheDocument();
	});

	it('starts on the rail when that was the last choice', () => {
		prefs.collapsed = true;
		render(Sidebar, { onsearch: vi.fn() });
		expect(screen.getByRole('button', { name: 'Expand sidebar' })).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: /Search, jump, do/ })).not.toBeInTheDocument();
	});

	it('stays full in the drawer, however it was left', () => {
		prefs.collapsed = true;
		render(Sidebar, { onsearch: vi.fn(), drawer: true });
		expect(screen.getByRole('button', { name: /Search, jump, do/ })).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Expand sidebar' })).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Collapse sidebar' })).not.toBeInTheDocument();
	});
});

describe('Sidebar, the user menu', () => {
	it.each([false, true])('opens from the user card (rail: %s)', async (rail) => {
		prefs.collapsed = rail;
		render(Sidebar, { onsearch: vi.fn() });
		await userEvent.click(screen.getByRole('button', { name: 'Account menu' }));
		expect(await screen.findByRole('button', { name: /Sign out/ })).toBeInTheDocument();
	});

	it('closes on a second click of the card', async () => {
		render(Sidebar, { onsearch: vi.fn() });
		const card = screen.getByRole('button', { name: 'Account menu' });
		await userEvent.click(card);
		await userEvent.click(card);
		await waitFor(() =>
			expect(screen.queryByRole('button', { name: /Sign out/ })).not.toBeInTheDocument()
		);
	});

	it('signs out through the session', async () => {
		vi.mocked(api.auth.logout).mockResolvedValue(undefined);
		render(Sidebar, { onsearch: vi.fn() });
		await userEvent.click(screen.getByRole('button', { name: 'Account menu' }));
		await userEvent.click(await screen.findByRole('button', { name: /Sign out/ }));
		expect(api.auth.logout).toHaveBeenCalledOnce();
	});
});
