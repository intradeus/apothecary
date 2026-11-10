import { describe, expect, it } from 'vitest';
import { initialsOf, to, workshopVial } from './urls';

describe('to', () => {
	it.each<[string, string]>([
		[to.desk(), '/'],
		[to.inbox(), '/inbox'],
		[to.ping('p1'), '/inbox/p1'],
		[to.runs(), '/runs'],
		[to.runs({ project: 'p1' }), '/runs?project=p1'],
		[to.runs({ tab: 'finished' }), '/runs?tab=finished'],
		[to.runs({ project: 'p1', tab: 'active' }), '/runs?project=p1&tab=active'],
		[to.run('r1'), '/runs/r1'],
		[to.tickets(), '/tickets'],
		[to.tickets({ project: 'p1' }), '/tickets?project=p1'],
		[to.newTicket(), '/tickets/new'],
		[to.newTicket({ project: 'p1' }), '/tickets/new?project=p1'],
		[to.ticket('t1'), '/tickets/t1'],
		[to.mergeRequests(), '/merge-requests'],
		[to.mergeRequests({ project: 'p1' }), '/merge-requests?project=p1'],
		[to.mergeRequest('p1', 12), '/merge-requests/p1/12'],
		[to.mergeRequest('p1', 12, 'docs'), '/merge-requests/p1/12?repo=docs'],
		[to.workshops(), '/w'],
		[to.newWorkshop(), '/w/new'],
		[to.workshop('p1'), '/w/p1'],
		[to.workshop('p1', 'overview'), '/w/p1'],
		[to.workshop('p1', 'flows'), '/w/p1?tab=flows'],
		[to.workshopSettings('p1'), '/w/p1/settings'],
		[to.flow('p1', 'builder'), '/w/p1/flows/builder'],
		[to.system(), '/system/integrations'],
		[to.system('limits'), '/system/limits'],
		[to.account(), '/system/account'],
		[to.users(), '/system/users'],
		[to.skills(), '/system/skills'],
		[to.newSkill(), '/system/skills/new'],
		[to.skill('s1'), '/system/skills/s1'],
		[to.newSkillset(), '/system/skills/sets/new'],
		[to.skillset('s1'), '/system/skills/sets/s1']
	])('builds %s', (built, url) => {
		expect(built).toBe(url);
	});

	it('leaves an empty query value out', () => {
		expect(to.runs({ project: '' })).toBe('/runs');
	});

	it('escapes a query value', () => {
		expect(to.tickets({ project: 'a b&c=d' })).toBe('/tickets?project=a+b%26c%3Dd');
	});

	it('escapes a flow’s name in its path', () => {
		expect(to.flow('p1', 'my flow/2')).toBe('/w/p1/flows/my%20flow%2F2');
	});
});

describe('workshopVial', () => {
	const ids = ['a', 'b', 'c', 'dev', 'prod', '01TESTPROJ', 'another-one', ''];

	it('gives a workshop the same vial every time', () => {
		for (const id of ids) expect(workshopVial(id)).toEqual(workshopVial(id));
	});

	it('picks a theme token, never a colour', () => {
		for (const id of ids) expect(workshopVial(id).hue).toMatch(/^var\(--[a-z0-9-]+\)$/);
	});

	it('keeps the fill between 30 and 74', () => {
		for (const id of ids) {
			const { fill } = workshopVial(id);
			expect(fill).toBeGreaterThanOrEqual(30);
			expect(fill).toBeLessThan(75);
		}
	});

	it('tells workshops apart', () => {
		expect(new Set(ids.map((id) => workshopVial(id).hue)).size).toBeGreaterThan(1);
	});
});

describe('initialsOf', () => {
	it.each([
		['Lucas Roland', 'LR'],
		['lucas', 'LU'],
		['x', 'X'],
		['Ada Byron Lovelace', 'AL'],
		['  spaced   out  ', 'SO'],
		['', '?'],
		['   ', '?']
	])('reads %j as %s', (name, initials) => {
		expect(initialsOf(name)).toBe(initials);
	});
});
