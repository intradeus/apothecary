import { describe, expect, it } from 'vitest';
import { asChangeType, CHANGE_TYPES } from './ticket';

describe('asChangeType', () => {
	it.each(CHANGE_TYPES)('keeps %s', (type) => {
		expect(asChangeType(type)).toBe(type);
	});

	it.each([[undefined], [''], ['bug'], ['FEAT']])('reads %j as a feature', (value) => {
		expect(asChangeType(value)).toBe('feat');
	});

	it('knows the types a branch may be named after', () => {
		expect(CHANGE_TYPES).toEqual(['feat', 'fix', 'hotfix', 'perf', 'docs', 'chore']);
	});
});
