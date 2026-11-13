import { describe, expect, it } from 'vitest';
import { aCrew, aSwarm, aUnit } from '$test/fixtures/swarm';
import { diffSwarm } from './feed';

const drone = (id: string, title = id) => ({ toolCallId: id, title, count: 1, since: '' });

describe('the kill feed', () => {
	it('says nothing about the board as first found', () => {
		expect(diffSwarm(null, aSwarm([aUnit()]))).toEqual([]);
	});

	it('says nothing when nothing changed', () => {
		const s = aSwarm([aUnit({ crew: aCrew({ subagents: [drone('c1')] }) })]);
		expect(diffSwarm(s, structuredClone(s))).toEqual([]);
	});

	it('deploys a unit that appeared', () => {
		const got = diffSwarm(aSwarm([]), aSwarm([aUnit({ runId: 'r9', title: 'New' })], 't2'));
		expect(got).toEqual([
			{
				id: 't2|r9|deployed|',
				at: 't2',
				kind: 'deployed',
				runId: 'r9',
				projectId: 'p1',
				title: 'New',
				detail: undefined
			}
		]);
	});

	it('names the stage a unit cleared', () => {
		const got = diffSwarm(
			aSwarm([aUnit({ currentStage: 'plan' })]),
			aSwarm([aUnit({ currentStage: 'build' })])
		);
		expect(got.map((e) => [e.kind, e.detail])).toEqual([['stage', 'plan']]);
	});

	it.each([
		['waiting_on_human', 'needs_you'],
		['in_review', 'in_review'],
		['completed', 'completed'],
		['failed', 'failed'],
		['abandoned', 'abandoned']
	] as const)('reports a unit going %s', (state, kind) => {
		const got = diffSwarm(aSwarm([aUnit()]), aSwarm([aUnit({ state })]));
		expect(got.map((e) => e.kind)).toEqual([kind]);
	});

	it('reports nothing for a state with no line', () => {
		expect(diffSwarm(aSwarm([aUnit({ state: 'pending' })]), aSwarm([aUnit()]))).toEqual([]);
	});

	it('launches a drone per new subagent spawn, not per one already out', () => {
		const got = diffSwarm(
			aSwarm([aUnit({ crew: aCrew({ subagents: [drone('c1')] }) })]),
			aSwarm([aUnit({ crew: aCrew({ subagents: [drone('c1'), drone('c2', 'Scouts')] }) })])
		);
		expect(got.map((e) => [e.kind, e.detail])).toEqual([['drone', 'Scouts']]);
	});

	it('reports an ending once: not again as the unit leaves', () => {
		const running = aSwarm([aUnit()]);
		const failed = aSwarm([aUnit({ state: 'failed' })]);
		expect(diffSwarm(running, failed).map((e) => e.kind)).toEqual(['failed']);
		expect(diffSwarm(failed, aSwarm([]))).toEqual([]);
	});

	it('reports a unit first seen already over by how it ended alone', () => {
		expect(
			diffSwarm(aSwarm([]), aSwarm([aUnit({ state: 'completed' })])).map((e) => e.kind)
		).toEqual(['completed']);
		expect(diffSwarm(aSwarm([]), aSwarm([aUnit({ state: 'archived' })]))).toEqual([]);
	});
});
