import { describe, expect, it } from 'vitest';
import { aCrew, aSample, aUnit } from '$test/fixtures/swarm';
import { alerting, cpuFraction, drones, hudStats, memFraction, online, UNIT_HUE } from './stats';

describe('a unit’s bars', () => {
	it('reads cpu against the unit’s own cores', () => {
		expect(cpuFraction(aSample({ cpuPerc: 200, cpus: 4 }))).toBe(0.5);
		expect(cpuFraction(aSample({ cpuPerc: 900, cpus: 4 }))).toBe(1);
		expect(cpuFraction(aSample({ cpuPerc: 50, cpus: 0 }))).toBe(0.5);
		expect(cpuFraction(undefined)).toBeUndefined();
	});

	it('reads memory against its limit, and nothing without one', () => {
		expect(memFraction(aSample({ memUsed: 1, memLimit: 4 }))).toBe(0.25);
		expect(memFraction(aSample({ memLimit: 0 }))).toBeUndefined();
		expect(memFraction(undefined)).toBeUndefined();
	});
});

describe('a unit', () => {
	it('counts the subagents of every spawn in flight', () => {
		const crew = aCrew({
			subagents: [
				{ toolCallId: 'a', title: 'A', count: 2, since: '' },
				{ toolCallId: 'b', title: 'B', count: 1, since: '' }
			]
		});
		expect(drones(aUnit({ crew }))).toBe(3);
		expect(drones(aUnit())).toBe(0);
	});

	it('is online with its sandbox up, not before, after or with it down', () => {
		expect(online(aUnit())).toBe(true);
		expect(online(aUnit({ state: 'waiting_on_human', envStatus: undefined }))).toBe(true);
		expect(online(aUnit({ state: 'pending' }))).toBe(false);
		expect(online(aUnit({ state: 'completed' }))).toBe(false);
		expect(online(aUnit({ envStatus: 'stopped' }))).toBe(false);
	});

	it('alerts on a question or a failure', () => {
		expect(alerting(aUnit({ state: 'waiting_on_human' }))).toBe(true);
		expect(alerting(aUnit({ openPings: 1 }))).toBe(true);
		expect(alerting(aUnit({ state: 'failed' }))).toBe(true);
		expect(alerting(aUnit())).toBe(false);
	});

	it('wears its state’s role', () => {
		expect(UNIT_HUE.running).toBe('running');
		expect(UNIT_HUE.waiting_on_human).toBe('attention');
		expect(UNIT_HUE.failed).toBe('danger');
		expect(UNIT_HUE.completed).toBe('success');
	});
});

describe('the HUD', () => {
	it('adds up a snapshot, an unsampled unit adding no load', () => {
		const s = hudStats([
			aUnit({
				runId: 'a',
				resources: aSample({ cpuPerc: 150, memUsed: 2, memLimit: 8 }),
				crew: aCrew()
			}),
			aUnit({
				runId: 'b',
				resources: aSample({ cpuPerc: 50, memUsed: 1, memLimit: 8 }),
				crew: aCrew({ subagents: [{ toolCallId: 'x', title: 'X', count: 2, since: '' }] })
			}),
			aUnit({ runId: 'c', state: 'waiting_on_human', openPings: 1 }),
			aUnit({ runId: 'd', state: 'failed' })
		]);
		expect(s).toEqual({
			units: 4,
			online: 3,
			sampled: 2,
			cores: 2,
			memUsed: 3,
			memLimit: 16,
			agents: 2,
			drones: 2,
			alerts: 2
		});
	});

	it('is all zeros for an empty board', () => {
		expect(hudStats([])).toMatchObject({ units: 0, online: 0, cores: 0, alerts: 0 });
	});
});
