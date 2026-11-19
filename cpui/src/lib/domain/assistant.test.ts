import { describe, expect, it } from 'vitest';
import { draftSubject, flowFill, skillFill, ticketFill } from './assistant';

describe('fills', () => {
	it('a flow fill is the whole document', () => {
		expect(flowFill('a: 1', { yaml: 'b: 2' })).toEqual({ next: 'b: 2', changed: ['yaml'] });
		expect(flowFill('a: 1', { yaml: 'a: 1' })).toEqual({ next: 'a: 1', changed: [] });
		expect(flowFill('a', { doc: 'x' })).toBeNull();
		expect(flowFill('a', 'x')).toBeNull();
	});

	it('a ticket fill sets only its fields, and names those that changed', () => {
		const cur = { title: 'old', goal: 'notes', changeType: 'feat', typeId: '' };
		expect(
			ticketFill(cur, { title: 'Fix the login page', change_type: 'fix', goal: 'notes' })
		).toEqual({
			next: { title: 'Fix the login page', goal: 'notes', changeType: 'fix', typeId: '' },
			changed: ['title', 'changeType']
		});
		expect(ticketFill(cur, { type_id: 'bug', labels: ['x'] })?.changed).toEqual(['typeId']);
		expect(ticketFill(cur, { title: 3 })?.changed).toEqual([]);
		expect(ticketFill(cur, null)).toBeNull();
		expect(ticketFill(cur, [])).toBeNull();
	});

	it('a skill fill is its name and every file', () => {
		const cur = {
			name: 'a',
			files: [
				{ path: 'SKILL.md', content: 'old' },
				{ path: 'gone.txt', content: 'x' }
			]
		};
		const r = skillFill(cur, {
			name: 'b',
			files: [{ path: 'SKILL.md', content: 'new' }, { path: 'ref.md', content: 'r' }, { path: 3 }]
		});
		expect(r?.next).toEqual({
			name: 'b',
			files: [
				{ path: 'SKILL.md', content: 'new' },
				{ path: 'ref.md', content: 'r' }
			]
		});
		expect(r?.changed).toEqual(['name', 'SKILL.md', 'ref.md', 'gone.txt']);
		expect(skillFill(cur, { name: 'b' })).toBeNull();
		expect(skillFill(cur, 'x')).toBeNull();
	});

	it('draft subjects differ', () => {
		expect(draftSubject()).toMatch(/^draft-/);
		expect(draftSubject()).not.toBe(draftSubject());
	});
});
