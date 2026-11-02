import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fleet } from '$lib/app/fleet.svelte';
import { prefs } from '$lib/app/prefs.svelte';
import { session } from '$lib/app/session.svelte';
import type { Limits, Me } from '$lib/domain';
import { aPing, aProject, aRun } from '$test/fixtures';
import { bindable } from '$test/fixtures/app.svelte';
import Rail from './Rail.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

const billing = aProject({ id: 'p1', name: 'billing' });
const web = aProject({ id: 'p2', name: 'web' });

const setup = (over: Record<string, unknown> = {}) => {
	const props = bindable({
		workshops: [billing, web],
		path: '/',
		count: () => 0,
		onsearch: vi.fn(),
		userBtn: null as HTMLElement | null,
		onmenu: vi.fn(),
		...over
	});
	render(Rail, props);
	return props;
};
const nav = () => within(screen.getByRole('navigation', { name: 'Primary' }));

beforeEach(() => {
	fleet.reset();
	fleet.limits = null;
	fleet.projects = [billing, web];
	prefs.collapsed = true;
	session.me = { user: { id: 'u', handle: 'lu', name: 'Lu', admin: false } } as Me;
});

describe('Rail', () => {
	it('expands back to the full sidebar', async () => {
		setup();
		await userEvent.click(screen.getByRole('button', { name: 'Expand sidebar' }));
		expect(prefs.collapsed).toBe(false);
	});

	it('opens the search', async () => {
		const { onsearch } = setup();
		await userEvent.click(screen.getByRole('button', { name: 'Search' }));
		expect(onsearch).toHaveBeenCalledOnce();
	});

	it('keeps every destination, named though it shows only an icon', () => {
		setup({ path: '/tickets/1' });
		expect(nav().getByRole('link', { name: 'Tickets' })).toHaveAttribute('aria-current', 'page');
		expect(nav().getByRole('link', { name: 'Desk' })).not.toHaveAttribute('aria-current');
		expect(nav().getAllByRole('link')).toHaveLength(7);
		expect(nav().getByRole('link', { name: 'Analytics' })).toHaveAttribute('href', '/analytics');
	});

	it('badges the inbox with what is waiting, and nothing else', () => {
		setup({ count: () => 4 });
		expect(nav().getByRole('link', { name: 'Inbox' })).toHaveTextContent('4');
		expect(nav().getByRole('link', { name: 'Runs' })).not.toHaveTextContent('4');
	});

	it('links workshops by name in their title', () => {
		setup();
		expect(screen.getByTitle('billing')).toHaveAttribute('href', '/w/p1');
		expect(screen.getByTitle('web')).toHaveAttribute('href', '/w/p2');
	});

	it('badges a workshop with its pending pings', () => {
		fleet.runs = [aRun({ id: 'r1', projectId: 'p1' })];
		fleet.pending = [aPing({ runId: 'r1' })];
		setup();
		expect(screen.getByTitle('billing')).toHaveTextContent('1');
		expect(screen.getByTitle('web')).toHaveTextContent('0');
	});

	it('shows capacity against the cap, or a dash without one', () => {
		fleet.runs = [aRun({ id: 'r1' })];
		const { unmount } = render(Rail, {
			workshops: [],
			path: '/',
			count: () => 0,
			onsearch: vi.fn(),
			userBtn: null,
			onmenu: vi.fn()
		});
		expect(screen.getByText('1/–')).toBeInTheDocument();
		unmount();

		fleet.limits = { maxConcurrentRuns: 3, maxEnvironments: 0 } as Limits;
		setup();
		expect(screen.getByText('1/3')).toBeInTheDocument();
	});

	it('hands the user trigger up and opens the menu from it', async () => {
		const props = setup();
		const trigger = screen.getByRole('button', { name: 'Account menu' });
		expect(props.userBtn).toBe(trigger);

		await userEvent.click(trigger);
		expect(props.onmenu).toHaveBeenCalledOnce();
	});
});
