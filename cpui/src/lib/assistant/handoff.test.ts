import { afterEach, describe, expect, it, vi } from 'vitest';
import { stashFlowFill, takeFlowFill } from './handoff';

afterEach(() => {
	vi.restoreAllMocks();
	sessionStorage.clear();
});

describe('the flow fill handoff', () => {
	it('holds a flow’s fill for its editor, once', () => {
		expect(stashFlowFill('w1', 'builder', 'name: builder\n')).toBe(true);
		expect(takeFlowFill('w1', 'other')).toBeNull();
		expect(takeFlowFill('w2', 'builder')).toBeNull();
		expect(takeFlowFill('w1', 'builder')).toBe('name: builder\n');
		expect(takeFlowFill('w1', 'builder')).toBeNull();
	});

	it('does without storage', () => {
		vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
			throw new Error('blocked');
		});
		vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
			throw new Error('blocked');
		});
		expect(stashFlowFill('w1', 'builder', 'x')).toBe(false);
		expect(takeFlowFill('w1', 'builder')).toBeNull();
	});
});
