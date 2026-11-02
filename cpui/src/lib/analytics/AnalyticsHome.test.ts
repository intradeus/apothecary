import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { goto } from '$app/navigation';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Dashboard, Panel } from '$lib/domain';
import { confirmState } from '$lib/ui/confirm.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { confirmed } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import AnalyticsHome from './AnalyticsHome.svelte';

vi.mock('$lib/api', () => ({
	api: {
		analytics: {
			dashboards: { list: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn() }
		}
	}
}));

const AT = '2026-10-31T12:00:00Z';
const PANEL: Panel = {
	id: 'p',
	title: 'Runs',
	viz: 'number',
	width: 3,
	query: { grain: 'runs', measures: [], groupBy: [], filters: [], time: {} }
};
const aDashboard = (over: Partial<Dashboard> = {}): Dashboard => ({
	id: 'd1',
	userId: 'u',
	name: 'Mine',
	builtin: false,
	filters: ['flow_name'],
	panels: [PANEL],
	createdAt: AT,
	updatedAt: AT,
	...over
});
const OVERVIEW = aDashboard({
	id: 'builtin-overview',
	userId: undefined,
	name: 'Overview',
	builtin: true,
	panels: []
});

const cardOf = (name: string) => screen.getByRole('link', { name }).closest('article')!;

beforeEach(() => {
	vi.resetAllMocks();
	toasts.list = [];
	confirmState.current = null;
});

describe('AnalyticsHome, listing', () => {
	it('shows a skeleton, then the built-ins apart from one’s own', async () => {
		const list = deferred<Dashboard[]>();
		vi.mocked(api.analytics.dashboards.list).mockReturnValue(list.promise);
		const { container } = render(AnalyticsHome);
		expect(container.querySelector('.skel, [aria-busy="true"]')).toBeTruthy();

		list.resolve([OVERVIEW, aDashboard()]);
		const overview = await screen.findByRole('link', { name: 'Overview' });
		expect(overview).toHaveAttribute('href', '/analytics/builtin-overview');
		expect(screen.getByRole('link', { name: 'Mine' })).toHaveAttribute('href', '/analytics/d1');
		expect(within(cardOf('Overview')).getByText('built-in')).toBeInTheDocument();
		expect(within(cardOf('Mine')).getByText('1 panel')).toBeInTheDocument();
		expect(within(cardOf('Overview')).getByText('0 panels')).toBeInTheDocument();
		expect(within(cardOf('Overview')).queryByRole('button', { name: 'Delete' })).toBeNull();
		expect(screen.getByRole('link', { name: 'Scorecards' })).toHaveAttribute(
			'href',
			'/analytics/scorecards'
		);
	});

	it('says there are none of one’s own yet', async () => {
		vi.mocked(api.analytics.dashboards.list).mockResolvedValue([OVERVIEW]);
		render(AnalyticsHome);
		expect(await screen.findByText('No dashboards of your own yet')).toBeInTheDocument();
	});

	it('shows a failed load with a retry that works', async () => {
		vi.mocked(api.analytics.dashboards.list).mockRejectedValueOnce(new Error('down'));
		render(AnalyticsHome);
		expect(await screen.findByText('down')).toBeInTheDocument();
		vi.mocked(api.analytics.dashboards.list).mockResolvedValueOnce([OVERVIEW]);
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByRole('link', { name: 'Overview' })).toBeInTheDocument();
	});
});

describe('AnalyticsHome, making dashboards', () => {
	beforeEach(() => {
		vi.mocked(api.analytics.dashboards.list).mockResolvedValue([OVERVIEW, aDashboard()]);
	});

	it('starts a new one with the usual filter bar and opens it', async () => {
		vi.mocked(api.analytics.dashboards.create).mockResolvedValue(aDashboard({ id: 'new' }));
		render(AnalyticsHome);
		await userEvent.click(await screen.findByRole('button', { name: /New dashboard/ }));
		expect(api.analytics.dashboards.create).toHaveBeenCalledWith({
			name: 'Untitled dashboard',
			filters: ['project_id', 'flow_name', 'ticket_change_type'],
			panels: []
		});
		await waitFor(() => expect(goto).toHaveBeenCalledWith('/analytics/new'));
	});

	it('duplicates a built-in into a copy of one’s own and opens it', async () => {
		vi.mocked(api.analytics.dashboards.create).mockResolvedValue(aDashboard({ id: 'copy' }));
		render(AnalyticsHome);
		await screen.findByRole('link', { name: 'Overview' });
		await userEvent.click(within(cardOf('Overview')).getByRole('button', { name: 'Duplicate' }));
		expect(api.analytics.dashboards.create).toHaveBeenCalledWith({
			name: 'Overview (copy)',
			filters: OVERVIEW.filters,
			panels: OVERVIEW.panels
		});
		await waitFor(() => expect(goto).toHaveBeenCalledWith('/analytics/copy'));
	});

	it('says why a save failed and stays put', async () => {
		vi.mocked(api.analytics.dashboards.create).mockRejectedValue(new Error('too many'));
		render(AnalyticsHome);
		await userEvent.click(await screen.findByRole('button', { name: /New dashboard/ }));
		await waitFor(() =>
			expect(toasts.list.at(-1)).toMatchObject({
				text: 'Couldn’t save the dashboard: too many',
				tone: 'bad'
			})
		);
		expect(goto).not.toHaveBeenCalled();
	});
});

describe('AnalyticsHome, one’s own', () => {
	beforeEach(() => {
		vi.mocked(api.analytics.dashboards.list).mockResolvedValue([OVERVIEW, aDashboard()]);
	});

	it('renames, trimmed, keeping its panels and filters', async () => {
		vi.mocked(api.analytics.dashboards.update).mockResolvedValue(aDashboard({ name: 'Costs' }));
		render(AnalyticsHome);
		await screen.findByRole('link', { name: 'Mine' });
		await userEvent.click(within(cardOf('Mine')).getByRole('button', { name: 'Rename' }));
		const input = screen.getByRole('textbox', { name: 'Name' });
		expect(input).toHaveValue('Mine');
		await userEvent.clear(input);
		await userEvent.type(input, ' Costs {Enter}');
		expect(api.analytics.dashboards.update).toHaveBeenCalledWith('d1', {
			name: 'Costs',
			filters: ['flow_name'],
			panels: aDashboard().panels
		});
		expect(await screen.findByRole('link', { name: 'Costs' })).toBeInTheDocument();
		await waitFor(() => expect(screen.queryByRole('textbox', { name: 'Name' })).toBeNull());
	});

	it('will not save an empty name', async () => {
		render(AnalyticsHome);
		await screen.findByRole('link', { name: 'Mine' });
		await userEvent.click(within(cardOf('Mine')).getByRole('button', { name: 'Rename' }));
		await userEvent.clear(screen.getByRole('textbox', { name: 'Name' }));
		expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
		await userEvent.type(screen.getByRole('textbox', { name: 'Name' }), '  {Enter}');
		expect(api.analytics.dashboards.update).not.toHaveBeenCalled();
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		await waitFor(() => expect(screen.queryByRole('textbox', { name: 'Name' })).toBeNull());
	});

	it('deletes after the dialog says yes, and not otherwise', async () => {
		vi.mocked(api.analytics.dashboards.remove).mockResolvedValue(undefined);
		render(AnalyticsHome);
		await screen.findByRole('link', { name: 'Mine' });
		const del = () => within(cardOf('Mine')).getByRole('button', { name: 'Delete' });

		await userEvent.click(del());
		expect(confirmState.current?.body).toContain('<b>Mine</b>');
		await confirmed(false);
		expect(api.analytics.dashboards.remove).not.toHaveBeenCalled();

		await userEvent.click(del());
		await confirmed(true);
		await waitFor(() => expect(screen.queryByRole('link', { name: 'Mine' })).toBeNull());
		expect(api.analytics.dashboards.remove).toHaveBeenCalledWith('d1');
		expect(toasts.list.at(-1)?.text).toBe('Dashboard deleted');
	});
});
