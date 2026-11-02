import { describe, expect, it } from 'vitest';
import { isBuiltinId, measureKey } from './analytics';

describe('measureKey', () => {
	it('names an aggregated measure by field and agg, a derived one by its id', () => {
		expect(measureKey({ field: 'cost_usd', agg: 'median' })).toBe('cost_usd/median');
		expect(measureKey({ field: 'success_rate' })).toBe('success_rate');
	});
});

describe('isBuiltinId', () => {
	it('knows the engine’s built-ins by their prefix', () => {
		expect(isBuiltinId('builtin-overview')).toBe(true);
		expect(isBuiltinId('01JBUILTIN')).toBe(false);
	});
});
