import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { AnalyticsQuery, AnalyticsResult, Viz } from '$lib/domain';
import { aPanel, aResult } from '$test/fixtures/analytics';
import { deferred, text } from '$test/helpers';
import { downloadCsv } from './csv';
import Panel from './Panel.svelte';

vi.mock('$lib/api', () => ({
	api: { analytics: { query: vi.fn(), drill: vi.fn() } }
}));
vi.mock('./csv', async (orig) => ({ ...(await orig<object>()), downloadCsv: vi.fn() }));

const query = (over: Partial<AnalyticsQuery> = {}): AnalyticsQuery => ({
	...aPanel().query,
	time: { from: '2026-08-01T00:00:00.000Z' },
	...over
});

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.analytics.query).mockResolvedValue(aResult());
	vi.mocked(api.analytics.drill).mockResolvedValue([]);
});

describe('Panel', () => {
	it('runs its query and shows the result, handing it on', async () => {
		const onresult = vi.fn();
		render(Panel, { panel: aPanel(), query: query(), onresult });
		expect(await screen.findByRole('button', { name: 'builder v3' })).toBeInTheDocument();
		expect(api.analytics.query).toHaveBeenCalledWith(query());
		expect(onresult).toHaveBeenCalledWith(aResult());
		expect(screen.getByRole('heading', { name: 'Cost by version' })).toBeInTheDocument();
	});

	it.each<[Viz, string]>([
		['number', 'n = 12'],
		['bar', 'svg'],
		['line', 'svg'],
		['scatter', 'svg'],
		['heatmap', 'svg'],
		['box', 'A box plot needs']
	])('draws the %s viz', async (viz, expected) => {
		const { container } = render(Panel, {
			panel: aPanel({ viz }),
			query: query(),
			chartWidth: 300
		});
		await waitFor(() => expect(api.analytics.query).toHaveBeenCalled());
		if (expected === 'svg')
			await waitFor(() => expect(container.querySelector('svg')).not.toBeNull());
		else expect(await screen.findByText(new RegExp(expected))).toBeInTheDocument();
	});

	it('shows a skeleton until the first answer', async () => {
		const d = deferred<AnalyticsResult>();
		vi.mocked(api.analytics.query).mockReturnValue(d.promise);
		const { container } = render(Panel, { panel: aPanel(), query: query() });
		expect(container.querySelector('.skel')).not.toBeNull();
		d.resolve(aResult());
		expect(await screen.findByRole('button', { name: 'builder v3' })).toBeInTheDocument();
	});

	it('says when nothing matched, except as a number', async () => {
		vi.mocked(api.analytics.query).mockResolvedValue(aResult({ rows: [] }));
		render(Panel, { panel: aPanel(), query: query() });
		expect(await screen.findByText('No runs in this range')).toBeInTheDocument();
	});

	it('flags rows resting on few runs', async () => {
		render(Panel, { panel: aPanel(), query: query() });
		expect(await screen.findByText('few runs — noisy')).toBeInTheDocument();
	});

	it('does not flag a result on enough runs', async () => {
		vi.mocked(api.analytics.query).mockResolvedValue(
			aResult({ rows: [{ dims: ['abc'], values: [1], n: 9, small: false }] })
		);
		render(Panel, { panel: aPanel(), query: query() });
		await screen.findByRole('button', { name: 'builder v3' });
		expect(screen.queryByText('few runs — noisy')).not.toBeInTheDocument();
	});

	it('shows a failure in place and retries', async () => {
		vi.mocked(api.analytics.query).mockRejectedValueOnce(new Error('query took longer than 5s'));
		render(Panel, { panel: aPanel(), query: query() });
		expect(await screen.findByText('Couldn’t run this panel’s query')).toBeInTheDocument();
		expect(screen.getByText('query took longer than 5s')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByRole('button', { name: 'builder v3' })).toBeInTheDocument();
	});

	it('runs again when its query changes, keeping only the latest answer', async () => {
		const first = deferred<AnalyticsResult>();
		const firstFail = deferred<AnalyticsResult>();
		vi.mocked(api.analytics.query)
			.mockReturnValueOnce(first.promise)
			.mockReturnValueOnce(firstFail.promise)
			.mockResolvedValueOnce(aResult({ labels: { flow_hash: { abc: 'latest' } } }));
		const { rerender } = render(Panel, { panel: aPanel(), query: query() });
		await rerender({ panel: aPanel(), query: query({ groupBy: ['flow_name'] }) });
		await rerender({ panel: aPanel(), query: query({ groupBy: ['project_id'] }) });
		expect(await screen.findByRole('button', { name: 'latest' })).toBeInTheDocument();

		first.resolve(aResult());
		firstFail.reject(new Error('late'));
		await Promise.allSettled([first.promise, firstFail.promise]);
		await tick();
		expect(screen.getByRole('button', { name: 'latest' })).toBeInTheDocument();
		expect(screen.queryByText('late')).not.toBeInTheDocument();
	});

	it('renders the controls it is given', async () => {
		render(Panel, { panel: aPanel(), query: query(), actions: text('move me') });
		expect(screen.getByText('move me')).toBeInTheDocument();
	});

	it('exports its result as CSV from the menu', async () => {
		render(Panel, { panel: aPanel(), query: query() });
		await screen.findByRole('button', { name: 'builder v3' });
		await userEvent.click(screen.getByRole('button', { name: 'Panel actions' }));
		await userEvent.click(screen.getByRole('menuitem', { name: 'Export CSV' }));
		expect(downloadCsv).toHaveBeenCalledWith(
			'Cost by version',
			expect.stringContaining('builder v3,0.25,12')
		);
	});

	it('offers no export before there is a result', async () => {
		vi.mocked(api.analytics.query).mockReturnValue(deferred<AnalyticsResult>().promise);
		render(Panel, { panel: aPanel(), query: query() });
		await userEvent.click(screen.getByRole('button', { name: 'Panel actions' }));
		const item = screen.getByRole('menuitem', { name: 'Export CSV' });
		expect(item).toBeDisabled();
	});

	it('lists the runs behind the whole panel from the menu', async () => {
		render(Panel, { panel: aPanel(), query: query() });
		await screen.findByRole('button', { name: 'builder v3' });
		await userEvent.click(screen.getByRole('button', { name: 'Panel actions' }));
		await userEvent.click(screen.getByRole('menuitem', { name: 'Show the runs' }));
		expect(
			await screen.findByRole('dialog', { name: 'Runs behind Cost by version' })
		).toBeInTheDocument();
		expect(api.analytics.drill).toHaveBeenCalledWith('runs', [], {
			from: '2026-08-01T00:00:00.000Z'
		});
	});

	it('lists the runs behind a clicked row', async () => {
		render(Panel, { panel: aPanel(), query: query() });
		await userEvent.click(await screen.findByRole('button', { name: 'builder v4' }));
		expect(api.analytics.drill).toHaveBeenCalledWith(
			'runs',
			[{ field: 'flow_hash', op: 'eq', values: ['def'] }],
			{ from: '2026-08-01T00:00:00.000Z' }
		);
		const sheet = await screen.findByRole('dialog');
		expect(sheet).toHaveTextContent('builder v4');
	});
});
