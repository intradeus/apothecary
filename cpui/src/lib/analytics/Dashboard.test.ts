import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { api } from '$lib/api';
import type { AnalyticsQuery } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { aCatalog, aDashboard, aPanel, aResult } from '$test/fixtures/analytics';
import { catalog } from './catalog.svelte';
import Dashboard from './Dashboard.svelte';

vi.mock('$lib/api', () => ({
	api: {
		analytics: {
			catalog: vi.fn(),
			query: vi.fn(),
			values: vi.fn(),
			drill: vi.fn(),
			dashboards: { get: vi.fn(), create: vi.fn(), update: vi.fn() },
			scorecards: { list: vi.fn() }
		}
	}
}));

const stages = aPanel({
	id: 'p2',
	title: 'Spend by stage',
	query: {
		grain: 'attempts',
		measures: [{ field: 'cost_usd', agg: 'sum' }],
		groupBy: ['stage_id'],
		filters: [],
		time: {}
	},
	viz: 'number',
	width: 3
});
const mine = aDashboard({ panels: [aPanel(), stages] });

const panelTitles = () =>
	screen.getAllByRole('heading', { level: 3 }).map((h) => h.textContent?.trim());
const queries = () => vi.mocked(api.analytics.query).mock.calls.map((c) => c[0] as AnalyticsQuery);
const DAY = 86_400_000;

beforeEach(() => {
	vi.resetAllMocks();
	catalog.reset();
	toasts.list = [];
	vi.mocked(api.analytics.catalog).mockResolvedValue(aCatalog());
	vi.mocked(api.analytics.dashboards.get).mockResolvedValue(mine);
	vi.mocked(api.analytics.query).mockResolvedValue(aResult());
	vi.mocked(api.analytics.values).mockResolvedValue([{ value: 'builder', label: 'builder' }]);
	vi.mocked(api.analytics.scorecards.list).mockResolvedValue([]);
});

describe('Dashboard', () => {
	it('loads the dashboard and runs its panels over the last 90 days', async () => {
		render(Dashboard, { id: 'd1' });
		expect(await screen.findByRole('heading', { name: 'Spend by stage' })).toBeInTheDocument();
		expect(api.analytics.dashboards.get).toHaveBeenCalledWith('d1');
		expect(panelTitles()).toEqual(['Cost by version', 'Spend by stage']);
		expect(screen.getByRole('link', { name: 'Analytics' })).toHaveAttribute('href', '/analytics');
		expect(screen.queryByText('built-in')).not.toBeInTheDocument();
		await waitFor(() => expect(queries()).toHaveLength(2));
		const from = Date.parse(queries()[0].time.from!);
		expect(Date.now() - from).toBeGreaterThan(89 * DAY);
		expect(Date.now() - from).toBeLessThan(91 * DAY);
	});

	it('runs the panels over the range picked, or all time', async () => {
		render(Dashboard, { id: 'd1' });
		await screen.findByRole('heading', { name: 'Spend by stage' });
		await userEvent.click(screen.getByRole('radio', { name: '30 days' }));
		await waitFor(() => expect(queries()).toHaveLength(4));
		expect(Date.now() - Date.parse(queries()[3].time.from!)).toBeLessThan(31 * DAY);
		await userEvent.click(screen.getByRole('radio', { name: 'All' }));
		await waitFor(() => expect(queries()).toHaveLength(6));
		expect(queries()[5].time).toEqual({});
	});

	it('narrows the panels whose grain has a filter’s dimension', async () => {
		render(Dashboard, { id: 'd1' });
		const flow = await screen.findByRole('button', { name: 'Flow: any' });
		await userEvent.click(flow);
		expect(api.analytics.values).toHaveBeenCalledWith('runs', 'flow_name', expect.anything());
		await userEvent.click(await screen.findByRole('checkbox', { name: 'builder' }));
		await waitFor(() =>
			expect(queries().some((q) => q.filters.some((f) => f.op === 'in'))).toBe(true)
		);
		const narrowed = queries().filter((q) => q.filters.length);
		expect(narrowed).toHaveLength(1);
		expect(narrowed[0]).toMatchObject({
			grain: 'runs',
			filters: [{ field: 'flow_name', op: 'in', values: ['builder'] }]
		});
	});

	it('takes a filter no panel has from runs, by its id when the catalog lacks it', async () => {
		vi.mocked(api.analytics.dashboards.get).mockResolvedValue(
			aDashboard({ filters: ['ticket_change_type'], panels: [stages] })
		);
		render(Dashboard, { id: 'd1' });
		await userEvent.click(await screen.findByRole('button', { name: 'ticket_change_type: any' }));
		expect(api.analytics.values).toHaveBeenCalledWith(
			'runs',
			'ticket_change_type',
			expect.anything()
		);
	});

	it('leaves a panel whose grain the catalog does not know unfiltered', async () => {
		const pings = aPanel({ query: { ...aPanel().query, grain: 'pings', groupBy: [] } });
		vi.mocked(api.analytics.dashboards.get).mockResolvedValue(aDashboard({ panels: [pings] }));
		render(Dashboard, { id: 'd1' });
		await userEvent.click(await screen.findByRole('button', { name: 'Flow: any' }));
		expect(api.analytics.values).toHaveBeenCalledWith('runs', 'flow_name', expect.anything());
		await userEvent.click(await screen.findByRole('checkbox', { name: 'builder' }));
		await waitFor(() => expect(api.analytics.query).toHaveBeenCalledTimes(1));
		expect(queries().every((q) => q.filters.length === 0)).toBe(true);
	});

	it('shows a skeleton while loading, a failure in place, and retries', async () => {
		vi.mocked(api.analytics.dashboards.get).mockRejectedValueOnce(new Error('gone'));
		const { container } = render(Dashboard, { id: 'd1' });
		expect(container.querySelector('.skel')).not.toBeNull();
		expect(await screen.findByText('Couldn’t load the dashboard')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByRole('heading', { name: 'Spend by stage' })).toBeInTheDocument();
	});

	it('loads another dashboard when its id changes', async () => {
		const { rerender } = render(Dashboard, { id: 'd1' });
		await screen.findByRole('heading', { name: 'Spend by stage' });
		await rerender({ id: 'd2' });
		await waitFor(() => expect(api.analytics.dashboards.get).toHaveBeenLastCalledWith('d2'));
	});

	describe('built-in', () => {
		const builtin = aDashboard({ id: 'builtin-overview', name: 'Overview', builtin: true });

		it('is read-only and duplicates to edit', async () => {
			vi.mocked(api.analytics.dashboards.get).mockResolvedValue(builtin);
			vi.mocked(api.analytics.dashboards.create).mockResolvedValue(
				aDashboard({ id: 'd9', name: 'Overview (copy)' })
			);
			render(Dashboard, { id: 'builtin-overview' });
			expect(await screen.findByText('built-in')).toBeInTheDocument();
			expect(screen.queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument();

			await userEvent.click(screen.getByRole('button', { name: /Duplicate to edit/ }));
			expect(api.analytics.dashboards.create).toHaveBeenCalledWith({
				name: 'Overview (copy)',
				filters: builtin.filters,
				panels: builtin.panels
			});
			expect(goto).toHaveBeenCalledWith('/analytics/d9');
			expect(toasts.list.at(-1)?.text).toBe('Dashboard duplicated');
		});

		it('shows why it could not be duplicated', async () => {
			vi.mocked(api.analytics.dashboards.get).mockResolvedValue(builtin);
			vi.mocked(api.analytics.dashboards.create).mockRejectedValue(new Error('nope'));
			render(Dashboard, { id: 'builtin-overview' });
			await userEvent.click(await screen.findByRole('button', { name: /Duplicate to edit/ }));
			expect(await screen.findByText('Couldn’t save the dashboard')).toBeInTheDocument();
			expect(goto).not.toHaveBeenCalled();
		});

		it('offers nothing to do when empty', async () => {
			vi.mocked(api.analytics.dashboards.get).mockResolvedValue({ ...builtin, panels: [] });
			render(Dashboard, { id: 'builtin-overview' });
			expect(await screen.findByText('No panels yet')).toBeInTheDocument();
			expect(screen.queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument();
		});
	});

	describe('edit mode', () => {
		async function editing() {
			render(Dashboard, { id: 'd1' });
			await userEvent.click(await screen.findByRole('button', { name: 'Edit' }));
		}

		it('reorders and removes panels, and saves them as a whole', async () => {
			vi.mocked(api.analytics.dashboards.update).mockImplementation(async (id, d) => ({
				...mine,
				...d
			}));
			await editing();
			expect(screen.getByRole('button', { name: 'Move Cost by version up' })).toBeDisabled();
			expect(screen.getByRole('button', { name: 'Move Spend by stage down' })).toBeDisabled();
			await userEvent.click(screen.getByRole('button', { name: 'Move Spend by stage up' }));
			expect(panelTitles()).toEqual(['Spend by stage', 'Cost by version']);
			await userEvent.click(screen.getByRole('button', { name: 'Move Spend by stage down' }));
			expect(panelTitles()).toEqual(['Cost by version', 'Spend by stage']);
			await userEvent.click(screen.getByRole('button', { name: 'Remove Cost by version' }));
			expect(panelTitles()).toEqual(['Spend by stage']);

			await userEvent.click(screen.getByRole('button', { name: 'Save dashboard' }));
			expect(api.analytics.dashboards.update).toHaveBeenCalledWith('d1', {
				name: 'Mine',
				filters: ['flow_name'],
				panels: [stages]
			});
			await waitFor(() => expect(toasts.list.at(-1)?.text).toBe('Dashboard saved'));
			expect(screen.getByRole('button', { name: 'Edit' })).toBeInTheDocument();
			expect(panelTitles()).toEqual(['Spend by stage']);
		});

		it('discards the changes on cancel', async () => {
			await editing();
			await userEvent.click(screen.getByRole('button', { name: 'Remove Cost by version' }));
			await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
			expect(panelTitles()).toEqual(['Cost by version', 'Spend by stage']);
		});

		it('changes a panel and adds one through the editor', async () => {
			await editing();
			await userEvent.click(screen.getByRole('button', { name: 'Edit Spend by stage' }));
			const dialog = await screen.findByRole('dialog', { name: 'Edit panel' });
			const title = within(dialog).getByLabelText('Title');
			await userEvent.clear(title);
			await userEvent.type(title, 'Stage spend');
			await userEvent.click(within(dialog).getByRole('button', { name: 'Apply' }));
			expect(await screen.findByRole('heading', { name: 'Stage spend' })).toBeInTheDocument();

			await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
			await userEvent.click(screen.getByRole('button', { name: /Add panel/ }));
			const fresh = await screen.findByRole('dialog', { name: 'New panel' });
			await userEvent.type(within(fresh).getByLabelText('Title'), 'Runs');
			await userEvent.click(within(fresh).getByRole('button', { name: 'Apply' }));
			expect(await screen.findByRole('heading', { name: 'Runs' })).toBeInTheDocument();
			expect(panelTitles()).toEqual(['Cost by version', 'Stage spend', 'Runs']);
		});

		it('shows why it could not save and keeps editing', async () => {
			vi.mocked(api.analytics.dashboards.update).mockRejectedValue(new Error('too many panels'));
			await editing();
			await userEvent.click(screen.getByRole('button', { name: 'Save dashboard' }));
			expect(await screen.findByText('too many panels')).toBeInTheDocument();
			expect(screen.getByRole('button', { name: 'Save dashboard' })).toBeInTheDocument();
			await userEvent.click(screen.getByRole('button', { name: /Dismiss/ }));
			expect(screen.queryByText('too many panels')).not.toBeInTheDocument();
		});

		it('starts an empty dashboard from its empty state', async () => {
			vi.mocked(api.analytics.dashboards.get).mockResolvedValue({ ...mine, panels: [] });
			render(Dashboard, { id: 'd1' });
			expect(await screen.findByText('No panels yet')).toBeInTheDocument();
			const edits = screen.getAllByRole('button', { name: 'Edit' });
			await userEvent.click(edits[edits.length - 1]);
			const adds = screen.getAllByRole('button', { name: /Add panel/ });
			expect(adds).toHaveLength(2);
			await userEvent.click(adds[1]);
			expect(await screen.findByRole('dialog', { name: 'New panel' })).toBeInTheDocument();
		});
	});
});
