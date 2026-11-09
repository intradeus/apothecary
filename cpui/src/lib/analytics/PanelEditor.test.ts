import { cleanup, render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi, type Mock } from 'vitest';
import { api } from '$lib/api';
import type { Panel } from '$lib/domain';
import { aCatalog, aPanel, aResult, aScorecard } from '$test/fixtures/analytics';
import { catalog } from './catalog.svelte';
import PanelEditor from './PanelEditor.svelte';

vi.mock('$lib/api', () => ({
	api: {
		analytics: {
			catalog: vi.fn(),
			query: vi.fn(),
			drill: vi.fn(),
			scorecards: { list: vi.fn() }
		}
	}
}));

const dialog = () => screen.getByRole('dialog', { name: /panel/ });
const combo = (name: string | RegExp) => within(dialog()).getByRole('combobox', { name });

async function pick(name: string | RegExp, option: string) {
	await userEvent.click(combo(name));
	await userEvent.click(screen.getByRole('option', { name: option }));
}

/** The panel handed to onsave by the last save. */
async function saved(onsave: Mock): Promise<Panel> {
	await userEvent.click(screen.getByRole('button', { name: 'Apply' }));
	expect(onsave).toHaveBeenCalled();
	return onsave.mock.lastCall![0] as Panel;
}

beforeEach(() => {
	vi.resetAllMocks();
	catalog.reset();
	catalog.data = aCatalog();
	vi.mocked(api.analytics.query).mockResolvedValue(aResult());
	vi.mocked(api.analytics.scorecards.list).mockResolvedValue([aScorecard()]);
});

describe('PanelEditor', () => {
	it('builds a new panel, wanting a title first', async () => {
		const onsave = vi.fn();
		render(PanelEditor, { open: true, onsave });
		expect(screen.getByRole('dialog', { name: 'New panel' })).toBeInTheDocument();
		expect(screen.getByText('None: the panel counts rows.')).toBeInTheDocument();

		await userEvent.click(screen.getByRole('button', { name: 'Apply' }));
		expect(screen.getByText('Give it a title.')).toBeInTheDocument();
		expect(onsave).not.toHaveBeenCalled();

		await userEvent.type(screen.getByLabelText('Title'), '  Spend by stage ');
		await pick('One row is', 'Stage attempts');
		await userEvent.click(screen.getByRole('button', { name: /Add measure/ }));
		await userEvent.click(screen.getByRole('button', { name: /Add group-by/ }));

		const p = await saved(onsave);
		expect(p.title).toBe('Spend by stage');
		expect(p.query).toMatchObject({
			grain: 'attempts',
			measures: [{ field: 'cost_usd', agg: 'sum' }],
			groupBy: ['stage_id']
		});
		expect(p.id).toMatch(/^p-/);
		await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
	});

	it('previews the query as it is built, over the dashboard’s range', async () => {
		render(PanelEditor, { open: true, panel: aPanel(), from: 'F', onsave: vi.fn() });
		expect(await screen.findByRole('button', { name: 'builder v3' })).toBeInTheDocument();
		expect(api.analytics.query).toHaveBeenLastCalledWith(
			expect.objectContaining({ grain: 'runs', time: { from: 'F' } })
		);
	});

	it('changes measures: field, aggregation, a derived one without, removal', async () => {
		const onsave = vi.fn();
		render(PanelEditor, { open: true, panel: aPanel(), onsave });
		expect(screen.getByRole('dialog', { name: 'Edit panel' })).toBeInTheDocument();

		await pick('Aggregation of measure 1', 'sum');
		await userEvent.click(screen.getByRole('button', { name: /Add measure/ }));
		await pick('Measure 2', 'Success rate');
		expect(combo('Measure 2')).toBeInTheDocument();
		expect(
			within(dialog()).queryByRole('combobox', { name: 'Aggregation of measure 2' })
		).toBeNull();
		await userEvent.click(screen.getByRole('button', { name: /Add measure/ }));
		await pick('Measure 3', 'Wall time');
		await userEvent.click(screen.getByRole('button', { name: 'Remove measure 3' }));

		expect((await saved(onsave)).query.measures).toEqual([
			{ field: 'cost_usd', agg: 'sum' },
			{ field: 'success_rate' }
		]);
	});

	it('keeps an aggregation the new field also takes', async () => {
		const onsave = vi.fn();
		render(PanelEditor, { open: true, panel: aPanel(), onsave });
		await pick('Measure 1', 'Wall time');
		expect((await saved(onsave)).query.measures).toEqual([{ field: 'wall_ms', agg: 'median' }]);
	});

	it('stops offering more measures and group-bys at the limit', async () => {
		render(PanelEditor, { open: true, onsave: vi.fn() });
		for (let i = 0; i < 8; i++)
			await userEvent.click(screen.getByRole('button', { name: /Add measure/ }));
		expect(screen.queryByRole('button', { name: /Add measure/ })).toBeNull();
		for (let i = 0; i < 3; i++)
			await userEvent.click(screen.getByRole('button', { name: /Add group-by/ }));
		expect(screen.queryByRole('button', { name: /Add group-by/ })).toBeNull();
	});

	it('changes group-bys, the bucket, filters, viz and width', async () => {
		const onsave = vi.fn();
		render(PanelEditor, { open: true, panel: aPanel(), onsave });
		await pick('Group by 1', 'Flow');
		await userEvent.click(screen.getByRole('button', { name: /Add group-by/ }));
		await userEvent.click(screen.getByRole('button', { name: 'Remove group-by 2' }));

		await userEvent.click(screen.getByRole('radio', { name: 'Weekly' }));
		await userEvent.click(screen.getByRole('button', { name: /Add filter/ }));
		await pick('Filter 1 field', 'Workshop');
		await pick('Filter 1 test', 'is one of');
		await userEvent.type(screen.getByPlaceholderText('Values'), 'p1{Enter}');
		await userEvent.click(screen.getByRole('button', { name: /Add filter/ }));
		await userEvent.click(screen.getByRole('button', { name: 'Remove filter 2' }));

		await userEvent.click(screen.getByRole('radio', { name: 'Bar' }));
		await userEvent.click(screen.getByRole('radio', { name: '12/12' }));

		let p = await saved(onsave);
		expect(p.query.groupBy).toEqual(['flow_name']);
		expect(p.query.time).toEqual({ bucket: 'week' });
		expect(p.query.filters).toEqual([{ field: 'project_id', op: 'in', values: ['p1'] }]);
		expect(p.viz).toBe('bar');
		expect(p.width).toBe(12);

		cleanup();
		render(PanelEditor, { open: true, panel: p, onsave });
		await userEvent.click(screen.getByRole('radio', { name: 'No' }));
		p = await saved(onsave);
		expect(p.query.time).toEqual({});
	});

	it('puts the five aggregations of one field in place for a box plot', async () => {
		const onsave = vi.fn();
		render(PanelEditor, {
			open: true,
			panel: aPanel({ query: { ...aPanel().query, measures: [{ field: 'success_rate' }] } }),
			onsave
		});
		await userEvent.click(screen.getByRole('radio', { name: 'Box' }));
		expect((await saved(onsave)).query.measures).toEqual(
			['p25', 'median', 'p75', 'min', 'max'].map((agg) => ({ field: 'wall_ms', agg }))
		);
	});

	it('keeps the measure’s field for a box plot', async () => {
		const onsave = vi.fn();
		render(PanelEditor, { open: true, panel: aPanel(), onsave });
		await userEvent.click(screen.getByRole('radio', { name: 'Box' }));
		expect((await saved(onsave)).query.measures[0]).toEqual({ field: 'cost_usd', agg: 'p25' });
	});

	it('ranks by a scorecard, optionally against a baseline row', async () => {
		const onsave = vi.fn();
		render(PanelEditor, { open: true, panel: aPanel(), onsave });
		await screen.findByRole('button', { name: 'builder v3' });

		await pick('Rank by scorecard', 'Cheapest');
		let p = await saved(onsave);
		expect(p.query.score).toEqual({ scorecard: 'builtin-cheapest' });
		expect(p.query.order).toEqual({ by: 'score', desc: true });

		cleanup();
		render(PanelEditor, { open: true, panel: p, onsave });
		await screen.findByRole('button', { name: 'builder v3' });
		await pick('Compared with', 'builder v3');
		p = await saved(onsave);
		expect(p.query.score).toEqual({ scorecard: 'builtin-cheapest', baseline: ['abc'] });

		cleanup();
		render(PanelEditor, { open: true, panel: p, onsave });
		await pick('Compared with', 'Best and worst rows (min–max)');
		await pick('Rank by scorecard', 'None');
		p = await saved(onsave);
		expect(p.query.score).toBeUndefined();
		expect(p.query.order).toBeUndefined();
	});

	it('keeps an explicit order when a scorecard is picked or dropped', async () => {
		const onsave = vi.fn();
		const order = { by: 'n', desc: false };
		render(PanelEditor, {
			open: true,
			panel: aPanel({ query: { ...aPanel().query, order } }),
			onsave
		});
		await pick('Rank by scorecard', 'Cheapest');
		await pick('Rank by scorecard', 'None');
		expect((await saved(onsave)).query.order).toEqual(order);
	});

	it('offers no scorecard when they cannot be listed', async () => {
		vi.mocked(api.analytics.scorecards.list).mockRejectedValue(new Error('down'));
		render(PanelEditor, { open: true, panel: aPanel(), onsave: vi.fn() });
		await userEvent.click(combo('Rank by scorecard'));
		await waitFor(() => expect(screen.getAllByRole('option')).toHaveLength(1));
	});

	it('leaves the panel untouched on cancel', async () => {
		const onsave = vi.fn();
		const panel = aPanel();
		render(PanelEditor, { open: true, panel, onsave });
		await userEvent.type(screen.getByLabelText('Title'), ' changed');
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(onsave).not.toHaveBeenCalled();
		expect(panel.title).toBe('Cost by version');
	});

	it('adds nothing a grain cannot take', async () => {
		catalog.data = {
			grains: [{ ...aCatalog().grains[0], dimensions: [], measures: [], derived: [] }]
		};
		const onsave = vi.fn();
		render(PanelEditor, { open: true, onsave });
		await userEvent.click(screen.getByRole('button', { name: /Add measure/ }));
		await userEvent.click(screen.getByRole('button', { name: /Add group-by/ }));
		await userEvent.click(screen.getByRole('button', { name: /Add filter/ }));
		await userEvent.click(screen.getByRole('radio', { name: 'Box' }));
		await userEvent.type(screen.getByLabelText('Title'), 'x');
		expect((await saved(onsave)).query).toMatchObject({ measures: [], groupBy: [], filters: [] });
	});
	it('loads the catalog when it has not been yet', async () => {
		catalog.reset();
		vi.mocked(api.analytics.catalog).mockResolvedValue(aCatalog());
		render(PanelEditor, { open: true, panel: aPanel(), onsave: vi.fn() });
		expect(api.analytics.catalog).toHaveBeenCalled();
		await waitFor(() => expect(combo('Aggregation of measure 1')).toHaveTextContent('median'));
	});

	it('stops offering more filters at the limit', async () => {
		render(PanelEditor, { open: true, onsave: vi.fn() });
		for (let i = 0; i < 10; i++)
			await userEvent.click(screen.getByRole('button', { name: /Add filter/ }));
		expect(screen.queryByRole('button', { name: /Add filter/ })).toBeNull();
	});

	it('runs no preview while closed', () => {
		render(PanelEditor, { open: false, panel: aPanel(), onsave: vi.fn() });
		expect(api.analytics.query).not.toHaveBeenCalled();
	});

	it('mounts where crypto.randomUUID is missing (plain HTTP, not localhost)', async () => {
		vi.stubGlobal('crypto', { getRandomValues: crypto.getRandomValues.bind(crypto) });
		try {
			const onsave = vi.fn();
			render(PanelEditor, { open: true, onsave });
			await userEvent.type(screen.getByLabelText('Title'), 'Runs');
			expect((await saved(onsave)).id).toMatch(/^p-[a-z0-9]+$/);
		} finally {
			vi.unstubAllGlobals();
		}
	});
});
