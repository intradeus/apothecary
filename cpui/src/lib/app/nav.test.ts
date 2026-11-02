import { describe, expect, it } from 'vitest';
import { NAV, navByChord } from './nav';

const item = (id: string) => NAV.find((n) => n.id === id)!;

describe('NAV', () => {
	it('labels each destination from the catalogue', () => {
		expect(NAV.map((n) => n.label)).toEqual([
			'Desk',
			'Inbox',
			'Notices',
			'Runs',
			'Tickets',
			'Analytics',
			'System'
		]);
	});

	it('links each destination', () => {
		expect(NAV.map((n) => n.href())).toEqual([
			'/',
			'/inbox',
			'/notices',
			'/runs',
			'/tickets',
			'/analytics',
			'/system/integrations'
		]);
	});

	it('leaves Notices, Analytics and System off the mobile tab bar', () => {
		expect(NAV.filter((n) => !n.mobile).map((n) => n.id)).toEqual([
			'notices',
			'analytics',
			'system'
		]);
	});

	it('matches the desk at the root only', () => {
		expect(item('desk').match('/')).toBe(true);
		expect(item('desk').match('/runs')).toBe(false);
	});

	it.each([
		['inbox', '/inbox/01PING'],
		['runs', '/runs/01RUN'],
		['tickets', '/tickets/new'],
		['analytics', '/analytics/scorecards'],
		['system', '/system/skills/abc']
	])('keeps %s lit on the pages inside it', (id, path) => {
		expect(item(id).match(path)).toBe(true);
	});

	it('has exactly one destination for a page inside the app', () => {
		for (const path of ['/', '/inbox', '/runs/1', '/tickets/1', '/analytics/d1', '/system/limits'])
			expect(NAV.filter((n) => n.match(path))).toHaveLength(1);
	});

	it('lights nothing on a workshop page', () => {
		expect(NAV.filter((n) => n.match('/w/01PROJ'))).toEqual([]);
	});
});

describe('navByChord', () => {
	it.each([
		['d', 'desk'],
		['i', 'inbox'],
		['r', 'runs'],
		['t', 'tickets'],
		['a', 'analytics']
	])('sends G then %s to %s', (key, id) => {
		expect(navByChord(key)?.id).toBe(id);
	});

	it('ignores the case of the key', () => {
		expect(navByChord('R')?.id).toBe('runs');
	});

	it('finds nothing for a key that is no chord', () => {
		expect(navByChord('s')).toBeUndefined();
	});
});
