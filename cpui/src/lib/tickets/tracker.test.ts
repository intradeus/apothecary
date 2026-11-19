import { describe, expect, it } from 'vitest';
import { isInternal } from './tracker';

describe('isInternal', () => {
	it('is true only on the internal tracker', () => {
		expect(isInternal({ kind: 'internal' })).toBe(true);
		expect(isInternal({ kind: 'jira' })).toBe(false);
		expect(isInternal(undefined)).toBe(false);
	});
});
