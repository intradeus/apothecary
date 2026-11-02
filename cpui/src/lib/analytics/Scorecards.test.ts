import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Catalog, Scorecard } from '$lib/domain';
import { confirmState } from '$lib/ui/confirm.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { choose, confirmed } from '$test/fixtures/app.svelte';
import { catalog } from './catalog.svelte';
import Scorecards from './Scorecards.svelte';

vi.mock('$lib/api', () => ({
	api: {
		analytics: {
			catalog: vi.fn(),
			scorecards: { list: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn() }
		}
	}
}));

const AT = '2026-10-31T12:00:00Z';
const CATALOG: Catalog = {
	grains: [
		{
			id: 'runs',
			label: 'Runs',
			timeField: 'created_at',
			dimensions: [],
			measures: [{ id: 'cost_usd', label: 'Cost', type: 'usd', aggs: ['sum', 'median'] }],
			derived: [{ id: 'success_rate', label: 'Success rate', type: 'ratio', aggs: [] }]
		},
		{
			id: 'skillsets',
			label: 'Skillsets',
			timeField: 'created_at',
			dimensions: [],
			measures: [{ id: 'cost_usd', label: 'Cost', type: 'usd', aggs: ['sum', 'median'] }],
			derived: [{ id: 'success_rate', label: 'Success rate', type: 'ratio', aggs: [] }]
		}
	]
};
const CHEAPEST: Scorecard = {
	id: 'builtin-cheapest',
	name: 'Cheapest',
	builtin: true,
	terms: [
		{ measure: { field: 'cost_usd', agg: 'median' }, direction: 'lower', weight: 0.8 },
		{ measure: { field: 'success_rate' }, direction: 'higher', weight: 0.2 }
	],
	createdAt: AT,
	updatedAt: AT
};
const MINE: Scorecard = {
	id: 's1',
	userId: 'u',
	name: 'Mine',
	builtin: false,
	terms: [{ measure: { field: 'gone_field' }, direction: 'lower', weight: 1 }],
	createdAt: AT,
	updatedAt: AT
};

const cardOf = (name: string) => screen.getByText(name, { selector: '.name' }).closest('article')!;
const save = () => screen.getByRole('button', { name: 'Save' });

beforeEach(() => {
	vi.resetAllMocks();
	catalog.reset();
	toasts.list = [];
	confirmState.current = null;
	vi.mocked(api.analytics.catalog).mockResolvedValue(CATALOG);
	vi.mocked(api.analytics.scorecards.list).mockResolvedValue([CHEAPEST, MINE]);
});

describe('Scorecards, listing', () => {
	it('lists each card’s terms by their catalog labels, a missing one by its id', async () => {
		render(Scorecards);
		await screen.findByText('Cheapest');
		const cheap = within(cardOf('Cheapest'));
		expect(cheap.getByText('Cost (median)')).toBeInTheDocument();
		expect(cheap.getByText('Lower is better · ×0.8')).toBeInTheDocument();
		expect(cheap.getByText('Success rate')).toBeInTheDocument();
		expect(cheap.getByText('built-in')).toBeInTheDocument();
		expect(cheap.queryByRole('button', { name: 'Edit' })).toBeNull();
		expect(within(cardOf('Mine')).getByText('gone_field')).toBeInTheDocument();
	});

	it('says there are none of one’s own yet', async () => {
		vi.mocked(api.analytics.scorecards.list).mockResolvedValue([CHEAPEST]);
		render(Scorecards);
		expect(await screen.findByText('No scorecards of your own yet')).toBeInTheDocument();
	});

	it('shows a failed load with a retry that works', async () => {
		vi.mocked(api.analytics.scorecards.list).mockRejectedValueOnce(new Error('down'));
		render(Scorecards);
		expect(await screen.findByText('down')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('Cheapest')).toBeInTheDocument();
	});
});

describe('Scorecards, editing', () => {
	it('duplicates a built-in into a new one with its terms', async () => {
		vi.mocked(api.analytics.scorecards.create).mockResolvedValue({
			...CHEAPEST,
			id: 's2',
			name: 'Cheapest (copy)',
			builtin: false
		});
		render(Scorecards);
		await screen.findByText('Cheapest');
		await userEvent.click(within(cardOf('Cheapest')).getByRole('button', { name: 'Duplicate' }));
		expect(screen.getByRole('textbox', { name: 'Name' })).toHaveValue('Cheapest (copy)');
		expect(screen.getByRole('combobox', { name: 'Measure 1' })).toHaveTextContent('Cost (median)');
		await userEvent.click(save());
		expect(api.analytics.scorecards.create).toHaveBeenCalledWith({
			name: 'Cheapest (copy)',
			terms: CHEAPEST.terms
		});
		expect(await screen.findByText('Cheapest (copy)')).toBeInTheDocument();
		expect(api.analytics.scorecards.update).not.toHaveBeenCalled();
	});

	it('builds a new one term by term: measure, direction, weight', async () => {
		vi.mocked(api.analytics.scorecards.create).mockImplementation(async (d) => ({
			...MINE,
			id: 's3',
			...d
		}));
		render(Scorecards);
		await screen.findByText('Cheapest');
		await userEvent.click(screen.getByRole('button', { name: /New scorecard/ }));
		const name = screen.getByRole('textbox', { name: 'Name' });
		expect(name).toHaveValue('Untitled scorecard');
		await userEvent.clear(name);
		await userEvent.type(name, 'Fast');

		await userEvent.click(screen.getByRole('button', { name: /Add term/ }));
		expect(screen.getByRole('combobox', { name: 'Measure 1' })).toHaveTextContent('Cost (sum)');
		await choose('Measure 1', 'Success rate');
		await userEvent.click(
			within(screen.getByRole('radiogroup', { name: 'Better when 1' })).getByRole('radio', {
				name: 'Higher is better'
			})
		);
		const weight = screen.getByRole('spinbutton', { name: 'Weight 1' });
		await userEvent.clear(weight);
		await userEvent.type(weight, '2');

		await userEvent.click(screen.getByRole('button', { name: /Add term/ }));
		await userEvent.click(screen.getByRole('button', { name: /Add term/ }));
		await userEvent.click(screen.getByRole('button', { name: 'Remove term 3' }));

		await userEvent.click(save());
		expect(api.analytics.scorecards.create).toHaveBeenCalledWith({
			name: 'Fast',
			terms: [
				{ measure: { field: 'success_rate' }, direction: 'higher', weight: 2 },
				{ measure: { field: 'cost_usd', agg: 'sum' }, direction: 'lower', weight: 1 }
			]
		});
		await waitFor(() => expect(screen.queryByRole('textbox', { name: 'Name' })).toBeNull());
		expect(screen.getByText('Fast')).toBeInTheDocument();
	});

	it('edits one’s own in place', async () => {
		vi.mocked(api.analytics.scorecards.update).mockResolvedValue({ ...MINE, name: 'Ours' });
		render(Scorecards);
		await screen.findByText('Mine');
		await userEvent.click(within(cardOf('Mine')).getByRole('button', { name: 'Edit' }));
		const name = screen.getByRole('textbox', { name: 'Name' });
		await userEvent.clear(name);
		await userEvent.type(name, 'Ours');
		await userEvent.click(save());
		expect(api.analytics.scorecards.update).toHaveBeenCalledWith('s1', {
			name: 'Ours',
			terms: MINE.terms
		});
		expect(await screen.findByText('Ours')).toBeInTheDocument();
		expect(screen.queryByText('Mine')).toBeNull();
	});

	it('refuses a nameless or termless scorecard, then shows the engine’s refusal', async () => {
		vi.mocked(api.analytics.scorecards.create).mockRejectedValue(new Error('weight must be > 0'));
		render(Scorecards);
		await screen.findByText('Cheapest');
		await userEvent.click(screen.getByRole('button', { name: /New scorecard/ }));
		await userEvent.click(save());
		expect(screen.getByRole('alert')).toHaveTextContent('Add at least one term.');

		await userEvent.click(screen.getByRole('button', { name: /Add term/ }));
		await userEvent.clear(screen.getByRole('textbox', { name: 'Name' }));
		await userEvent.click(save());
		expect(screen.getByRole('alert')).toHaveTextContent('Give it a name.');

		await userEvent.type(screen.getByRole('textbox', { name: 'Name' }), 'X');
		await userEvent.click(save());
		expect(await screen.findByRole('alert')).toHaveTextContent(
			'Couldn’t save the scorecard: weight must be > 0'
		);
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		await waitFor(() => expect(screen.queryByRole('textbox', { name: 'Name' })).toBeNull());
	});

	it('names the catalog failure in the editor and adds a blank term', async () => {
		vi.mocked(api.analytics.catalog).mockRejectedValue(new Error('no catalog'));
		render(Scorecards);
		await screen.findByText('Cheapest');
		await userEvent.click(screen.getByRole('button', { name: /New scorecard/ }));
		expect(screen.getByText('no catalog')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Add term/ }));
		expect(screen.getByRole('spinbutton', { name: 'Weight 1' })).toHaveValue(1);
	});
});

describe('Scorecards, deleting', () => {
	it('deletes after the dialog says yes, and not otherwise', async () => {
		vi.mocked(api.analytics.scorecards.remove).mockResolvedValue(undefined);
		render(Scorecards);
		await screen.findByText('Mine');
		const del = () => within(cardOf('Mine')).getByRole('button', { name: 'Delete' });

		await userEvent.click(del());
		expect((await confirmed(false)).body).toContain('<b>Mine</b>');
		expect(api.analytics.scorecards.remove).not.toHaveBeenCalled();

		await userEvent.click(del());
		await confirmed(true);
		await waitFor(() => expect(screen.queryByText('Mine')).toBeNull());
		expect(api.analytics.scorecards.remove).toHaveBeenCalledWith('s1');
		expect(toasts.list.at(-1)?.text).toBe('Scorecard deleted');
	});

	it('toasts a failed delete and keeps the card', async () => {
		vi.mocked(api.analytics.scorecards.remove).mockRejectedValue(new Error('in use'));
		render(Scorecards);
		await screen.findByText('Mine');
		await userEvent.click(within(cardOf('Mine')).getByRole('button', { name: 'Delete' }));
		await confirmed(true);
		await waitFor(() =>
			expect(toasts.list.at(-1)).toMatchObject({
				text: 'Couldn’t save the scorecard: in use',
				tone: 'bad'
			})
		);
		expect(screen.getByText('Mine')).toBeInTheDocument();
	});
});
