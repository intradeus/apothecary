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
			'War room',
			'Tickets',
			'Merge requests',
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
			'/war-room',
			'/tickets',
			'/merge-requests',
			'/analytics',
			'/system/integrations'
		]);
	});

	it('leaves Notices, the war room, Merge requests, Analytics and System off the mobile tab bar', () => {
		expect(NAV.filter((n) => !n.mobile).map((n) => n.id)).toEqual([
			'notices',
			'warRoom',
			'mrs',
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
		['warRoom', '/war-room'],
		['tickets', '/tickets/new'],
		['mrs', '/merge-requests/01PROJ/12'],
		['analytics', '/analytics/scorecards'],
		['system', '/system/skills/abc']
	])('keeps %s lit on the pages inside it', (id, path) => {
		expect(item(id).match(path)).toBe(true);
	});

	it('has exactly one destination for a page inside the app', () => {
		for (const path of [
			'/',
			'/inbox',
			'/runs/1',
			'/war-room',
			'/tickets/1',
			'/merge-requests',
			'/analytics/d1',
			'/system/limits'
		])
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
		['w', 'warRoom'],
		['t', 'tickets'],
		['m', 'mrs'],
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
