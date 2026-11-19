import { describe, expect, it } from 'vitest';
import { projectCan } from './project';

describe('projectCan', () => {
	const operator = { access: { role: 'operator' as const, actions: ['run:read', 'run:operate'] } };

	it('grants what the access holds', () => {
		expect(projectCan(operator, 'run:operate')).toBe(true);
	});

	it('refuses what it does not', () => {
		expect(projectCan(operator, 'run:shell')).toBe(false);
	});

	it('grants nothing in a workshop it does not know', () => {
		expect(projectCan(undefined, 'run:read')).toBe(false);
	});
});
