import { describe, expect, it } from 'vitest';
import type { Connection } from '@xyflow/svelte';
import { aFlow, aStage, readDoc } from '$test/fixtures/logic';
import { debugNodeID, edgeSelection, EXIT, isMarker, linkOf, nodeKey, nodeSelection } from './ids';

const conn = (over: Partial<Connection>): Connection =>
	({ source: 'a', target: 'b', sourceHandle: null, targetHandle: null, ...over }) as Connection;

const flow = () =>
	readDoc(`
name: f
version: 5
parameters: {}
main:
  - { id: a, type: command, run: a }
watchers:
  - { id: w, on: manual, stages: [{ id: x, type: command, run: x }] }
outcomes:
  - { id: o, on: mr_merged }
`);

describe('names on the canvas', () => {
	it('keeps a sequence’s way out apart from the stages', () => {
		expect(EXIT).toBe('__exit');
	});

	it('names a stage’s debug leaf', () => {
		expect(debugNodeID('build')).toBe('__debug:build');
	});

	it.each([
		['__start', true],
		['__trigger:w', true],
		['__hub', false],
		['build', false],
		['__end', false]
	])('says whether %s is a chain’s marker', (id, marker) => {
		expect(isMarker(id)).toBe(marker);
	});

	it.each([
		['__end', 'end'],
		['__done', 'done'],
		['__abort', 'abort'],
		['__start', 'start'],
		['__hub', 'hub'],
		['__debug:a', 'debug:a'],
		['__trigger:w', 'trigger:w'],
		['__section:watcher', 'section:watcher'],
		['__end:w', 'end:w'],
		['__done:w', 'done:w'],
		['__abort:w', 'abort:w'],
		['stage', 'stage']
	])('saves %s under %s', (id, key) => {
		expect(nodeKey(id)).toBe(key);
	});
});

describe('linkOf', () => {
	it.each<[string, Partial<Connection>]>([
		['no source', { source: '' }],
		['no target', { target: '' }],
		['a debug leaf’s own handle', { source: '__debug:a' }],
		['a dataflow dot, out', { sourceHandle: 'out:plan' }],
		['a dataflow dot, in', { sourceHandle: 'in:plan' }]
	])('asks nothing of %s', (_, over) => {
		expect(linkOf(conn(over))).toBeNull();
	});

	it.each<[string, string]>([
		['b', 'b'],
		['__end', 'end'],
		['__done', 'done'],
		['__abort', 'abort'],
		['__end:w', 'end'],
		['__done:w', 'done'],
		['__abort:w', 'abort'],
		['__hub', 'end'],
		['__debug:a', 'debug']
	])('reads a drop on %s as %s', (target, to) => {
		expect(linkOf(conn({ target, sourceHandle: 'on_fail' }))).toEqual({
			source: 'a',
			handle: 'on_fail',
			target: to
		});
	});

	it('reads a stage’s plain handle as its success', () => {
		expect(linkOf(conn({}))?.handle).toBe('success');
	});

	it.each(['__start', '__trigger:w'])('reads a drag from %s as a new entry', (source) => {
		expect(linkOf(conn({ source, target: 'b' }))).toEqual({
			source: 'start',
			handle: 'entry',
			target: 'b'
		});
	});
});

describe('nodeSelection', () => {
	it.each<[string, unknown]>([
		['a', { kind: 'stage', id: 'a' }],
		['__debug:a', { kind: 'stage', id: 'a' }],
		['__trigger:w', { kind: 'phase', id: 'w' }],
		['__end:w', { kind: 'phase', id: 'w' }],
		['__done:o', { kind: 'phase', id: 'o' }],
		['__abort:o', { kind: 'phase', id: 'o' }],
		['__start', { kind: 'phase', id: 'main' }],
		['__hub', null],
		['__end', null],
		['__done', null],
		['__section:watcher', null]
	])('selects %s', (id, sel) => {
		expect(nodeSelection(flow(), id)).toEqual(sel);
	});

	it('selects nothing for the start of a flow with no main chain', () => {
		const doc = readDoc(`
name: f
version: 5
parameters: {}
watchers:
  - { id: w, on: manual, stages: [{ id: x, type: command, run: x }] }
`);
		expect(nodeSelection(doc, '__start')).toBeNull();
	});
});

describe('edgeSelection', () => {
	it.each<[string, unknown]>([
		['success|a', { kind: 'stage', id: 'a' }],
		['on_fail|a|0', { kind: 'edge', stage: 'a', edge: 'on_fail' }],
		['on_reject|a|1', { kind: 'edge', stage: 'a', edge: 'on_reject' }],
		['data|a|b|path', { kind: 'stage', id: 'a' }],
		['debugreturn|a', { kind: 'stage', id: 'a' }],
		['hub|w', { kind: 'phase', id: 'w' }],
		['entry|__start', { kind: 'stage', id: 'a' }],
		['entry|__trigger:w', { kind: 'stage', id: 'x' }],
		['entry|__trigger:o', null]
	])('selects %s', (id, sel) => {
		expect(edgeSelection(flow(), id)).toEqual(sel);
	});

	it('leaves the selection alone for the start of an empty flow', () => {
		expect(edgeSelection(aFlow([]), 'entry|__start')).toBeUndefined();
	});

	it('takes the first stage as the entry of a flat flow', () => {
		expect(edgeSelection(aFlow([aStage({ id: 'z' })]), 'entry|__start')).toEqual({
			kind: 'stage',
			id: 'z'
		});
	});
});
