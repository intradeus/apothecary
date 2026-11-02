import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Catalog } from '$lib/domain';
import { deferred } from '$test/helpers';
import { catalog } from './catalog.svelte';

vi.mock('$lib/api', () => ({ api: { analytics: { catalog: vi.fn() } } }));

const f = (id: string) => ({ id, label: id, type: 'count' as const, aggs: [] });
const CATALOG: Catalog = {
	grains: [
		{
			id: 'runs',
			label: 'Runs',
			timeField: 'created_at',
			dimensions: [f('flow_name')],
			measures: [f('cost_usd')],
			derived: [f('success_rate')]
		}
	]
};

beforeEach(() => {
	vi.resetAllMocks();
	catalog.reset();
});

describe('catalog', () => {
	it('fetches once, sharing the request between concurrent callers', async () => {
		const d = deferred<Catalog>();
		vi.mocked(api.analytics.catalog).mockReturnValue(d.promise);
		const a = catalog.load();
		const b = catalog.load();
		d.resolve(CATALOG);
		await Promise.all([a, b]);
		await catalog.load();
		expect(api.analytics.catalog).toHaveBeenCalledTimes(1);
		expect(catalog.data).toEqual(CATALOG);
	});

	it('keeps the error and tries again on the next load', async () => {
		vi.mocked(api.analytics.catalog).mockRejectedValueOnce(new Error('down'));
		await catalog.load();
		expect(catalog.error).toBe('down');
		expect(catalog.data).toBeNull();
		vi.mocked(api.analytics.catalog).mockResolvedValueOnce(CATALOG);
		await catalog.load();
		expect(catalog.error).toBe('');
		expect(catalog.data).toEqual(CATALOG);
	});

	it('finds a grain and any field on it, dimension, measure or derived', async () => {
		expect(catalog.grain('runs')).toBeUndefined();
		expect(catalog.field('runs', 'cost_usd')).toBeUndefined();
		vi.mocked(api.analytics.catalog).mockResolvedValue(CATALOG);
		await catalog.load();
		expect(catalog.grain('runs')?.label).toBe('Runs');
		expect(catalog.grain('pings')).toBeUndefined();
		expect(catalog.field('runs', 'flow_name')?.id).toBe('flow_name');
		expect(catalog.field('runs', 'cost_usd')?.id).toBe('cost_usd');
		expect(catalog.field('runs', 'success_rate')?.id).toBe('success_rate');
		expect(catalog.field('runs', 'nope')).toBeUndefined();
	});
});
