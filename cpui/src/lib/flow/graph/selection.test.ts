import { describe, expect, it } from 'vitest';
import { readDoc } from '$test/fixtures/logic';
import { renameInTrail, selectionExists, type Selection } from './selection';

const doc = readDoc(`
name: f
version: 5
parameters: {}
schemas: [{ name: s, json_schema: {} }]
mcp_servers: [{ name: m, builtin: browser }]
agents: [{ name: a, prompt: p }]
sequences: [{ id: q, stages: [{ id: inner, type: command, run: r }] }]
main: [{ id: top, type: command, run: r }]
`);

describe('selectionExists', () => {
	it.each<[Selection, boolean]>([
		[null, true],
		[{ kind: 'stage', id: 'top' }, true],
		[{ kind: 'stage', id: 'gone' }, false],
		[{ kind: 'stage', id: 'inner' }, false],
		[{ kind: 'stage', id: 'inner', sequence: 'q' }, true],
		[{ kind: 'stage', id: 'top', sequence: 'q' }, false],
		[{ kind: 'stage', id: 'x', sequence: 'gone' }, false],
		[{ kind: 'edge', stage: 'top', edge: 'on_fail' }, true],
		[{ kind: 'edge', stage: 'gone', edge: 'on_fail' }, false],
		[{ kind: 'edge', stage: 'inner', edge: 'on_fail', sequence: 'q' }, true],
		[{ kind: 'phase', id: 'main' }, true],
		[{ kind: 'phase', id: 'gone' }, false],
		[{ kind: 'agent', name: 'a' }, true],
		[{ kind: 'agent', name: 'gone' }, false],
		[{ kind: 'sequence', id: 'q' }, true],
		[{ kind: 'sequence', id: 'gone' }, false],
		[{ kind: 'schema', name: 's' }, true],
		[{ kind: 'schema', name: 'gone' }, false],
		[{ kind: 'mcp', name: 'm' }, true],
		[{ kind: 'mcp', name: 'gone' }, false]
	])('checks %j', (sel, exists) => {
		expect(selectionExists(doc, sel)).toBe(exists);
	});

	it('takes anything on trust with no document', () => {
		expect(selectionExists(null, { kind: 'stage', id: 'x' })).toBe(true);
	});
});

describe('renameInTrail', () => {
	const stage = (id: string, sequence?: string): Selection => ({ kind: 'stage', id, sequence });
	const edge = (id: string, sequence?: string): Selection => ({
		kind: 'edge',
		stage: id,
		edge: 'on_fail',
		sequence
	});

	it.each<[string, Selection, Selection, Selection[], Selection[]]>([
		[
			'a stage, and the edges leaving it',
			stage('a'),
			stage('b'),
			[stage('a'), edge('a'), stage('c')],
			[stage('b'), edge('b'), stage('c')]
		],
		[
			'a sequence’s stage, only in that sequence',
			stage('a', 'q'),
			stage('b', 'q'),
			[stage('a', 'q'), stage('a'), edge('a', 'q'), stage('a', 'r')],
			[stage('b', 'q'), stage('a'), edge('b', 'q'), stage('a', 'r')]
		],
		[
			'a sequence, and everything in it',
			{ kind: 'sequence', id: 'q' },
			{ kind: 'sequence', id: 'r' },
			[{ kind: 'sequence', id: 'q' }, stage('a', 'q'), edge('a', 'q'), stage('a')],
			[{ kind: 'sequence', id: 'r' }, stage('a', 'r'), edge('a', 'r'), stage('a')]
		],
		[
			'an agent',
			{ kind: 'agent', name: 'a' },
			{ kind: 'agent', name: 'b' },
			[
				{ kind: 'agent', name: 'a' },
				{ kind: 'agent', name: 'c' }
			],
			[
				{ kind: 'agent', name: 'b' },
				{ kind: 'agent', name: 'c' }
			]
		],
		[
			'a phase',
			{ kind: 'phase', id: 'a' },
			{ kind: 'phase', id: 'b' },
			[{ kind: 'phase', id: 'a' }],
			[{ kind: 'phase', id: 'b' }]
		],
		[
			'a schema',
			{ kind: 'schema', name: 'a' },
			{ kind: 'schema', name: 'b' },
			[{ kind: 'schema', name: 'a' }],
			[{ kind: 'schema', name: 'b' }]
		],
		[
			'a server',
			{ kind: 'mcp', name: 'a' },
			{ kind: 'mcp', name: 'b' },
			[{ kind: 'mcp', name: 'a' }],
			[{ kind: 'mcp', name: 'b' }]
		]
	])('renames %s', (_, from, to, trail, out) => {
		expect(renameInTrail(trail, from, to)).toEqual(out);
	});

	it('keeps the trail’s empty step', () => {
		expect(renameInTrail([null], stage('a'), stage('b'))).toEqual([null]);
	});

	it.each<[string, Selection, Selection]>([
		['no from', null, stage('b')],
		['no to', stage('a'), null],
		['a change of kind', stage('a'), { kind: 'phase', id: 'a' }]
	])('leaves the trail alone on %s', (_, from, to) => {
		const trail = [stage('a')];
		expect(renameInTrail(trail, from, to)).toBe(trail);
	});
});
