import { readdirSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { aFlow, aStage, EVERY_FIELD, readDoc, throughEditor, written } from '$test/fixtures/logic';
import {
	ABORT,
	addAgent,
	addMainChain,
	addMCPServer,
	addOutcome,
	addSchema,
	addSequence,
	addWatcher,
	agentOf,
	agentUsers,
	allStages,
	appendSequenceStage,
	appendStage,
	autoLayout,
	chainEnds,
	chainLinks,
	chainSequence,
	columnLayout,
	CURRENT_VERSION,
	debugKey,
	DEBUG,
	deriveDataLinks,
	DONE,
	docToYaml,
	edgeKindsFor,
	edgeLinks,
	edgeOf,
	effectiveSuccess,
	END,
	expandDoc,
	FAIL,
	feedDots,
	feedSourceFor,
	fires,
	flowServices,
	freshStage,
	givesVerdict,
	hasHub,
	hasMain,
	HUB,
	isEdgeSource,
	isEngineSource,
	isReservedTarget,
	mcpKind,
	mcpUsers,
	markerKey,
	movePhase,
	moveSequenceStage,
	normalizeDoc,
	paramDefault,
	parksOnDebug,
	phaseEntry,
	phaseOf,
	placeChain,
	portEntries,
	portValue,
	pruneCrossPhase,
	pruneEdgeTarget,
	readAsModelOrHarness,
	removeMCPServer,
	removePhase,
	removeSchema,
	removeSequence,
	renameAgent,
	renameAgentPort,
	renameMCPServer,
	renameSchema,
	renameSequence,
	routableFrom,
	schemaUsers,
	sectionKey,
	sequenceUsers,
	serviceSource,
	setChainSequence,
	setEdge,
	setEntry,
	showsSections,
	sourcesFor,
	sourceService,
	sourceType,
	stageInputs,
	stageOutputs,
	stagesOf,
	START,
	successOrder,
	takesSchema,
	terminalKey,
	triggerLabel,
	verdictOutput,
	type DocEdge,
	type DocPhase,
	type DocStage,
	type EdgeKind,
	type FlowDoc,
	type StageKind
} from './flowdoc';

const ids = (stages: { id: string }[]) => stages.map((s) => s.id);
const stageIn = (doc: FlowDoc, id: string) => doc.stages.find((s) => s.id === id)!;

/** main: a → b; watcher w: x → y; outcome o: z. */
const threeChains = () =>
	readDoc(`
name: three
version: 5
parameters: {}
main:
  - { id: a, type: command, run: a }
  - { id: b, type: command, run: b }
watchers:
  - id: w
    on: manual
    stages:
      - { id: x, type: command, run: x }
      - { id: y, type: command, run: y }
outcomes:
  - id: o
    on: mr_merged
    stages:
      - { id: z, type: command, run: z }
`);

describe('the small vocabulary', () => {
	it.each([END, DONE, ABORT, DEBUG, FAIL])('reserves %s as a route target', (to) => {
		expect(isReservedTarget(to)).toBe(true);
	});

	it.each([START, 'build', 'End', ''])('treats %j as a stage', (to) => {
		expect(isReservedTarget(to)).toBe(false);
	});

	it('flattens an edge chain, deepest last, and an absent edge to nothing', () => {
		const e: DocEdge = { to: 'a', max: 1, then: { to: 'b', max: 2, then: { to: DEBUG } } };
		expect(chainLinks(e).map((l) => l.to)).toEqual(['a', 'b', DEBUG]);
		expect(chainLinks(undefined)).toEqual([]);
	});

	it.each<[StageKind, EdgeKind[]]>([
		['agent', ['on_fail', 'on_timeout', 'on_reject']],
		['command', ['on_fail', 'on_timeout']],
		['wait', ['on_fail', 'on_timeout']],
		['service', ['on_fail', 'on_timeout']],
		['human_gate', ['on_reject']],
		['action', ['on_fail']],
		['sequence', ['on_fail']]
	])('lets a %s declare the edges the parser accepts', (type, kinds) => {
		expect(edgeKindsFor(type)).toEqual(kinds);
	});

	it.each<[EdgeKind, string]>([
		['on_reject', 'edge.verdict'],
		['on_fail', 'edge.failure'],
		['on_timeout', 'edge.failure']
	])('feeds what %s carries into %s', (kind, source) => {
		expect(feedSourceFor(kind)).toBe(source);
	});

	it.each([
		['run.service.app', 'app'],
		['run.service.$app', '$app'],
		['run.service.my-app_2', 'my-app_2'],
		['run.service.', ''],
		['run.service.App', ''],
		['run.service.1app', ''],
		['run.ticket', ''],
		['artifacts/plan.md', '']
	])('reads the service of %s as %j', (source, service) => {
		expect(sourceService(source)).toBe(service);
	});

	it.each([
		['run.ticket', 'ticket'],
		['run.git.status', 'changeset'],
		['run.git.diff_target', 'changeset'],
		['run.git.diff_phase', 'changeset'],
		['run.mr.url', 'link'],
		['run.workspace', 'link'],
		['run.ci.failure', 'failure'],
		['edge.failure', 'failure'],
		['edge.verdict', 'verdict'],
		['run.service.$app', 'link'],
		['docs/plan.md', undefined]
	])('types the source %s as %s', (source, type) => {
		expect(sourceType(source)).toBe(type);
		expect(isEngineSource(source)).toBe(type !== undefined);
	});

	it('tells the edge sources from the run facts', () => {
		expect(isEdgeSource('edge.verdict')).toBe(true);
		expect(isEdgeSource('edge.failure')).toBe(true);
		expect(isEdgeSource('run.ticket')).toBe(false);
	});

	it('builds a service source from a name', () => {
		expect(serviceSource('$app')).toBe('run.service.$app');
	});

	it.each([
		['verdict', true],
		['review_threads', true],
		[undefined, true],
		['file', false],
		['files', false],
		['ticket', false],
		['changeset', false],
		['failure', false],
		['link', false]
	])('says whether a %s port takes a schema', (type, takes) => {
		expect(takesSchema(type)).toBe(takes);
	});

	it('offers an agent every source but the CI failure, outside a ci_failed watcher', () => {
		const sources = sourcesFor('agent', ['$app']);
		expect(sources).toContain('run.ticket');
		expect(sources).toContain('edge.verdict');
		expect(sources).toContain('run.service.$app');
		expect(sources).not.toContain('run.ci.failure');
		expect(sourcesFor('agent', [], true)).toContain('run.ci.failure');
	});

	it('offers a gate links only, and every other stage nothing', () => {
		expect(sourcesFor('human_gate', ['web'])).toEqual([
			'run.workspace',
			'run.mr.url',
			'run.service.web'
		]);
		expect(sourcesFor('command')).toEqual([]);
		expect(sourcesFor('action')).toEqual([]);
	});

	it('reads a port by its source, else its destination, else nothing', () => {
		expect(portValue({ from: 'a', to: 'b' })).toBe('a');
		expect(portValue({ to: 'b' })).toBe('b');
		expect(portValue({})).toBe('');
		expect(portValue(undefined)).toBe('');
		expect(portEntries(undefined)).toEqual([]);
		expect(portEntries({ a: { from: 'x' } })).toEqual([['a', { from: 'x' }]]);
	});

	it.each<[string, { builtin?: string; url?: string; command?: string[] }, string]>([
		['a builtin', { builtin: 'browser' }, 'builtin'],
		['a url', { url: 'https://x.test' }, 'url'],
		['a command', { command: ['npx'] }, 'command'],
		['nothing yet', {}, 'command'],
		['an empty url', { url: '' }, 'url']
	])('reads a server setting %s as %s', (_, fields, kind) => {
		expect(mcpKind({ name: 'm', ...fields })).toBe(kind);
	});

	it.each([
		[{ signal: '' }, false],
		[{ signal: 'manual' }, false],
		[{ signal: 'mr_merged' }, true]
	])('says whether %j fires on its own', (wait, expected) => {
		expect(fires(wait)).toBe(expected);
	});

	it.each([
		[{ signal: 'manual' }, 'manual'],
		[{ signal: 'manual', start: 'confirm' }, 'manual'],
		[{ signal: 'mr_conflicts' }, 'mr_conflicts'],
		[{ signal: 'mr_conflicts', start: 'auto' }, 'mr_conflicts'],
		[{ signal: 'mr_conflicts', start: 'confirm' }, 'mr_conflicts (confirm)'],
		[{ signal: 'x', quiet_for: '5m' }, 'x (quiet 5m)'],
		[{ signal: 'x', max: 0 }, 'x (max 0)'],
		[
			{ signal: 'x', start: '$policy', quiet_for: '$quiet', max: '$retries' },
			'x ($policy, quiet $quiet, max $retries)'
		]
	])('words the wait point %j', (wait, label) => {
		expect(triggerLabel(wait)).toBe(label);
	});
});

describe('normalizeDoc', () => {
	it('flattens the sections in order, each stage tagged with its chain', () => {
		const doc = threeChains();
		expect(doc.stages.map((s) => [s.id, s.phase])).toEqual([
			['a', 'main'],
			['b', 'main'],
			['x', 'w'],
			['y', 'w'],
			['z', 'o']
		]);
		expect(doc.phases?.map((p) => [p.id, p.kind])).toEqual([
			['main', 'main'],
			['w', 'watcher'],
			['o', 'outcome']
		]);
	});

	it('gives the wire sections no room in the editor document', () => {
		const doc = threeChains() as unknown as Record<string, unknown>;
		for (const key of ['main', 'watchers', 'outcomes']) expect(doc).not.toHaveProperty(key);
	});

	it('leaves out a main chain with no stages', () => {
		const doc = normalizeDoc({ name: 'f', parameters: {}, main: [] });
		expect(doc.phases).toEqual([]);
		expect(doc.stages).toEqual([]);
	});

	it('reads a bare signal as a wait point', () => {
		expect(phaseOf(threeChains(), { id: 'x', type: 'command', phase: 'w' })?.on).toEqual({
			signal: 'manual'
		});
	});

	it('copies a wait point rather than sharing the wire object', () => {
		const on = { signal: 'x', start: 'auto' };
		const raw = { name: 'f', parameters: {}, watchers: [{ id: 'w', on, stages: [] }] };
		normalizeDoc(raw).phases![0].on.start = 'manual';
		expect(on.start).toBe('auto');
	});

	it('reads `sequence: X` as one stage named after its chain', () => {
		const doc = readDoc(`
name: f
version: 5
parameters: {}
sequences: [{ id: ship, stages: [{ id: push, type: action, action: push }] }]
main: [{ id: a, type: command, run: a }]
watchers: [{ id: again, on: manual, sequence: ship }]
`);
		expect(stageIn(doc, 'again')).toMatchObject({
			type: 'sequence',
			sequence: 'ship',
			phase: 'again',
			on_success: END
		});
	});

	it('gives a sequence stage its kind, in a chain and in a sequence', () => {
		const doc = readDoc(EVERY_FIELD);
		expect(stageIn(doc, 'make_ship').type).toBe('sequence');
		expect(doc.sequences?.[1].stages[0].type).toBe('sequence');
	});

	it.each<[string, unknown, DocEdge]>([
		['a scalar', 'debug', { to: 'debug' }],
		['a mapping', { to: 'x', max: 2 }, { to: 'x', max: 2 }],
		[
			'a scalar fallback',
			{ to: 'x', max: 2, then: 'abort' },
			{ to: 'x', max: 2, then: { to: ABORT } }
		],
		[
			'a nested fallback',
			{ to: 'x', max: 1, then: { to: 'y', max: 1, then: 'debug' } },
			{ to: 'x', max: 1, then: { to: 'y', max: 1, then: { to: DEBUG } } }
		]
	])('unfolds %s edge into the mapping the editor holds', (_, wire, edge) => {
		const doc = aFlow([aStage({ id: 'a', on_fail: wire as DocEdge })]);
		expect(doc.stages[0].on_fail).toEqual(edge);
	});

	it('unfolds the edges of a sequence stage too', () => {
		const doc = aFlow([], {
			sequences: [
				{ id: 'q', stages: [aStage({ id: 's', on_reject: 'fail' as unknown as DocEdge })] }
			]
		});
		expect(doc.sequences?.[0].stages[0].on_reject).toEqual({ to: FAIL });
	});

	it('resolves every chain stage to an explicit success, so the form and the canvas agree', () => {
		const doc = threeChains();
		expect(doc.stages.map((s) => s.on_success)).toEqual(['b', END, 'y', END, DONE]);
	});

	it('leaves the sugar of a sequence stage alone: its success depends on who runs it', () => {
		const doc = readDoc(EVERY_FIELD);
		expect(doc.sequences?.[0].stages[0].on_success).toBeUndefined();
	});

	it.each<[string, unknown, unknown]>([
		['a string', 'opencode', 'opencode'],
		['a number', 50, 50],
		['false', false, false],
		['zero', 0, 0],
		['an empty string', '', ''],
		['a list', ['a', 'b'], undefined]
	])('holds a parameter written as bare %s in the long form', (_, bare, expected) => {
		const doc = normalizeDoc({ name: 'f', parameters: { p: bare } });
		if (expected !== undefined) expect(doc.parameters.p).toEqual({ default: expected });
		else expect(typeof doc.parameters.p).toBe('object');
	});

	it('keeps a parameter already in the long form, and gives a flow without any none', () => {
		const long = { type: 'int' as const, default: 2, description: 'd' };
		expect(normalizeDoc({ name: 'f', parameters: { p: long } }).parameters.p).toBe(long);
		expect(normalizeDoc({ name: 'f' }).parameters).toEqual({});
	});

	it('reads a null parameter as one with a null default', () => {
		expect(normalizeDoc({ name: 'f', parameters: { p: null } }).parameters.p).toEqual({
			default: null
		});
	});
});

describe('expandDoc', () => {
	const expanded = (yaml: string) => expandDoc(readDoc(yaml));
	const HEAD = 'name: f\nversion: 5\nparameters: {}\n';

	it('hands back a document with no sequences as it is', () => {
		const doc = aFlow([aStage({ id: 'a' })]);
		expect(expandDoc(doc)).toBe(doc);
	});

	it('prefixes a sequence stage’s stages with its path, and drops `sequences`', () => {
		const doc = expanded(`${HEAD}
sequences:
  - id: ship
    stages:
      - { id: test, type: command, run: t }
      - { id: push, type: command, run: p }
main:
  - { id: make, sequence: ship }
`);
		expect(ids(doc.stages)).toEqual(['make.test', 'make.push']);
		expect(doc.sequences).toBeUndefined();
		expect(doc.stages[0].on_success).toBe('make.push');
	});

	it('leaves a sequence by the running stage’s success', () => {
		const doc = expanded(`${HEAD}
sequences:
  - id: ship
    stages:
      - { id: test, type: command, run: t }
main:
  - { id: make, sequence: ship, on_success: after }
  - { id: after, type: command, run: a, on_success: done }
`);
		expect(stageIn(doc, 'make.test').on_success).toBe('after');
	});

	it('sends a route into a sequence stage to its first stage', () => {
		const doc = expanded(`${HEAD}
sequences:
  - id: ship
    stages:
      - { id: test, type: command, run: t }
main:
  - { id: before, type: command, run: b, on_success: make, on_fail: { to: make, max: 1 } }
  - { id: make, sequence: ship, on_success: done }
`);
		expect(stageIn(doc, 'before').on_success).toBe('make.test');
		expect(stageIn(doc, 'before').on_fail).toMatchObject({ to: 'make.test', max: 1 });
	});

	it('keeps a sequence’s routes among its own expanded stages', () => {
		const doc = expanded(`${HEAD}
sequences:
  - id: ship
    stages:
      - { id: test, type: command, run: t, on_fail: { to: fix, max: 2 } }
      - { id: fix, type: command, run: f, on_success: test }
main:
  - { id: make, sequence: ship, on_success: done }
`);
		expect(stageIn(doc, 'make.test').on_fail?.to).toBe('make.fix');
		expect(stageIn(doc, 'make.fix').on_success).toBe('make.test');
	});

	it('splices the sequence stage’s on_fail in where `fail` is said', () => {
		const doc = expanded(`${HEAD}
sequences:
  - id: ship
    stages:
      - { id: test, type: command, run: t, on_fail: { to: test, max: 2, then: fail } }
      - { id: push, type: command, run: p, on_fail: fail }
main:
  - { id: make, sequence: ship, on_success: done, on_fail: { to: repair, max: 1, then: abort } }
  - { id: repair, type: command, run: r, on_success: make }
`);
		const out = { to: 'repair', max: 1, then: { to: ABORT } };
		expect(stageIn(doc, 'make.test').on_fail).toMatchObject({
			to: 'make.test',
			max: 2,
			then: out
		});
		expect(stageIn(doc, 'make.push').on_fail).toMatchObject(out);
	});

	it('gives each splice of `fail` its own copy', () => {
		const doc = expanded(`${HEAD}
sequences:
  - id: ship
    stages:
      - { id: a, type: command, run: a, on_fail: fail }
      - { id: b, type: command, run: b, on_fail: fail }
main:
  - { id: make, sequence: ship, on_fail: { to: make, max: 1 } }
`);
		expect(stageIn(doc, 'make.a').on_fail).not.toBe(stageIn(doc, 'make.b').on_fail);
	});

	it('makes `fail` a dead end when the sequence stage has no on_fail', () => {
		const doc = expanded(`${HEAD}
sequences:
  - id: ship
    stages:
      - { id: a, type: command, run: a, on_fail: fail }
main:
  - { id: make, sequence: ship }
`);
		expect(stageIn(doc, 'make.a').on_fail).toBeUndefined();
	});

	it('expands sequences in sequences, ids taking the whole path', () => {
		const doc = expanded(`${HEAD}
sequences:
  - id: inner
    stages:
      - { id: test, type: command, run: t }
  - id: outer
    stages:
      - { id: ship, sequence: inner }
      - { id: last, type: command, run: l }
main:
  - { id: before, type: command, run: b, on_success: make }
  - { id: make, sequence: outer }
`);
		expect(ids(doc.stages)).toEqual(['before', 'make.ship.test', 'make.last']);
		expect(stageIn(doc, 'before').on_success).toBe('make.ship.test');
		expect(stageIn(doc, 'make.ship.test').on_success).toBe('make.last');
	});

	it('keeps a reserved route as it is', () => {
		const doc = expanded(`${HEAD}
sequences:
  - id: ship
    stages:
      - { id: a, type: command, run: a, on_fail: abort }
main:
  - { id: make, sequence: ship }
`);
		expect(stageIn(doc, 'make.a').on_fail).toMatchObject({ to: ABORT });
	});

	it('keeps each stage in its chain', () => {
		const doc = expanded(`${HEAD}
sequences:
  - id: ship
    stages:
      - { id: a, type: command, run: a }
main:
  - { id: first, type: command, run: f }
watchers:
  - { id: again, on: manual, sequence: ship }
`);
		expect(doc.stages.map((s) => [s.id, s.phase])).toEqual([
			['first', 'main'],
			['again.a', 'again']
		]);
		expect(stageIn(doc, 'again.a').on_success).toBe(END);
	});

	it('expands a watcher that runs the main chain again (`sequence: main`)', () => {
		const doc = expanded(`${HEAD}
sequences:
  - id: unused
    stages:
      - { id: a, type: command, run: a }
main:
  - { id: first, type: command, run: f }
  - { id: second, type: command, run: s }
watchers:
  - { id: again, on: manual, sequence: main }
`);
		expect(ids(doc.stages)).toEqual(['first', 'second', 'again.first', 'again.second']);
	});

	it('runs the main chain again in a flow that declares no sequences', () => {
		const doc = expanded(`${HEAD}
main:
  - { id: first, type: command, run: f, on_fail: { to: first, max: 1 } }
  - { id: second, type: command, run: s }
watchers:
  - { id: again, on: manual, sequence: main }
`);
		expect(ids(doc.stages)).toEqual(['first', 'second', 'again.first', 'again.second']);
		expect(stageIn(doc, 'again.first')).toMatchObject({
			phase: 'again',
			on_success: 'again.second',
			on_fail: { to: 'again.first' }
		});
		expect(stageIn(doc, 'again.second').on_success).toBe(END);
	});

	it('leaves the main chain where the sequence stage says, when it runs it again', () => {
		const doc = expanded(`${HEAD}
main:
  - { id: first, type: command, run: f }
watchers:
  - id: again
    on: manual
    stages:
      - { id: redo, sequence: main }
      - { id: tidy, type: command, run: t }
`);
		expect(stageIn(doc, 'redo.first').on_success).toBe('tidy');
	});

	it('ends an outcome that runs the main chain again the way its then says', () => {
		const doc = expanded(`${HEAD}
main:
  - { id: first, type: command, run: f }
outcomes:
  - { id: closed, on: mr_closed, then: abort, sequence: main }
`);
		expect(stageIn(doc, 'closed.first').on_success).toBe(ABORT);
	});

	it('keeps a `done` the main chain said itself', () => {
		const doc = expanded(`${HEAD}
main:
  - { id: first, type: command, run: f, on_success: done }
watchers:
  - { id: again, on: manual, sequence: main }
`);
		expect(stageIn(doc, 'again.first').on_success).toBe(DONE);
	});
});

describe('routes and order', () => {
	it('keeps a stage’s routes to its own chain', () => {
		const doc = threeChains();
		expect(ids(routableFrom(doc, stageIn(doc, 'x')))).toEqual(['x', 'y']);
	});

	it('reads the stages of an unphased flow as one chain', () => {
		const doc: FlowDoc = { name: 'f', parameters: {}, stages: [aStage({ id: 'a' })] };
		expect(ids(stagesOf(doc, undefined))).toEqual(['a']);
		expect(phaseEntry(doc, undefined)?.id).toBe('a');
	});

	it.each([
		['a stated success', 'x', 0],
		['the next stage', 'a2', 1]
	])('resolves %s', (_, success, i) => {
		const doc: FlowDoc = {
			name: 'f',
			parameters: {},
			stages: [aStage({ id: 'a1', on_success: 'x' }), aStage({ id: 'a' }), aStage({ id: 'a2' })]
		};
		expect(effectiveSuccess(doc, i)).toBe(success);
	});

	it.each<[string, DocPhase[], string, string]>([
		['a flow with no chains is done', [], '', DONE],
		[
			'a main chain nothing watches is done',
			[{ id: 'main', kind: 'main', on: { signal: '' } }],
			'main',
			DONE
		],
		[
			'a main chain something watches ends',
			[
				{ id: 'main', kind: 'main', on: { signal: '' } },
				{ id: 'w', kind: 'watcher', on: { signal: 'manual' } }
			],
			'main',
			END
		],
		['a watcher ends', [{ id: 'w', kind: 'watcher', on: { signal: 'manual' } }], 'w', END],
		['an outcome is done', [{ id: 'o', kind: 'outcome', on: { signal: 'x' } }], 'o', DONE],
		[
			'an outcome ends as its then says',
			[{ id: 'o', kind: 'outcome', on: { signal: 'x' }, then: ABORT }],
			'o',
			ABORT
		]
	])('knows the last stage’s success: %s', (_, phases, phase, success) => {
		const doc: FlowDoc = {
			name: 'f',
			parameters: {},
			phases,
			stages: [aStage({ id: 'a', phase: phase || undefined })]
		};
		expect(effectiveSuccess(doc, 0)).toBe(success);
	});

	it('never lets `next` cross into another chain', () => {
		const doc: FlowDoc = {
			name: 'f',
			parameters: {},
			phases: [
				{ id: 'w1', kind: 'watcher', on: { signal: 'manual' } },
				{ id: 'w2', kind: 'watcher', on: { signal: 'manual' } }
			],
			stages: [aStage({ id: 'a', phase: 'w1' }), aStage({ id: 'b', phase: 'w2' })]
		};
		expect(effectiveSuccess(doc, 0)).toBe(END);
	});

	it('orders the stages along the success chain, the rest after, in each chain', () => {
		const doc = readDoc(`
name: f
version: 5
parameters: {}
main:
  - { id: a, type: command, run: a, on_success: c }
  - { id: orphan, type: command, run: o, on_success: done }
  - { id: c, type: command, run: c, on_success: b }
  - { id: b, type: command, run: b, on_success: done }
watchers:
  - id: w
    on: manual
    stages: [{ id: x, type: command, run: x }]
`);
		expect(ids(successOrder(doc))).toEqual(['a', 'c', 'b', 'orphan', 'x']);
	});

	it('survives a success loop', () => {
		const doc = aFlow([aStage({ id: 'a', on_success: 'b' }), aStage({ id: 'b', on_success: 'a' })]);
		expect(ids(successOrder(doc))).toEqual(['a', 'b']);
	});

	it('orders an unphased flow too', () => {
		const doc: FlowDoc = {
			name: 'f',
			parameters: {},
			stages: [aStage({ id: 'a', on_success: 'c' }), aStage({ id: 'b' }), aStage({ id: 'c' })]
		};
		expect(ids(successOrder(doc))).toEqual(['a', 'c', 'b']);
	});

	it('reads and sets each kind of edge', () => {
		const s = aStage({ id: 'a' });
		for (const kind of ['on_fail', 'on_reject', 'on_timeout'] as const) {
			setEdge(s, kind, { to: kind });
			expect(edgeOf(s, kind)).toEqual({ to: kind });
			expect(s[kind]).toEqual({ to: kind });
		}
	});

	it('makes a stage its chain’s entry by moving it first, and says so', () => {
		const doc = threeChains();
		expect(setEntry(doc, 'y')).toBe(true);
		expect(ids(doc.stages)).toEqual(['a', 'b', 'y', 'x', 'z']);
	});

	it.each(['a', 'nowhere'])(
		'changes nothing for the entry already, or an unknown stage: %s',
		(id) => {
			const doc = threeChains();
			expect(setEntry(doc, id)).toBe(false);
			expect(ids(doc.stages)).toEqual(['a', 'b', 'x', 'y', 'z']);
		}
	);

	it('prunes a link and splices its fallbacks up', () => {
		const e: DocEdge = { to: 'a', max: 1, then: { to: 'b', max: 1, then: { to: 'c', max: 1 } } };
		expect(chainLinks(pruneEdgeTarget(e, 'b')).map((l) => l.to)).toEqual(['a', 'c']);
	});

	it('prunes the head of a chain to what followed it', () => {
		const e: DocEdge = { to: 'a', max: 1, then: { to: 'b', max: 1 } };
		expect(pruneEdgeTarget(e, 'a')?.to).toBe('b');
	});

	it('prunes every link naming the target, and a last one to nothing', () => {
		const e: DocEdge = { to: 'a', max: 1, then: { to: 'a', max: 1 } };
		expect(pruneEdgeTarget(e, 'a')).toBeUndefined();
		expect(pruneEdgeTarget(undefined, 'a')).toBeUndefined();
	});

	it('moves a stage to the end of another chain and cuts the routes that now cross', () => {
		const doc = readDoc(`
name: f
version: 5
parameters: {}
main:
  - { id: a, type: command, run: a, on_fail: { to: b, max: 1, then: { to: a, max: 1 } } }
  - { id: b, type: command, run: b, on_success: a }
watchers:
  - id: w
    on: manual
    stages: [{ id: x, type: command, run: x }]
`);
		movePhase(doc, 'b', 'w');
		expect(doc.stages.map((s) => [s.id, s.phase])).toEqual([
			['a', 'main'],
			['x', 'w'],
			['b', 'w']
		]);
		// a's success went to b: the phase ends. Its chain keeps the link left.
		expect(stageIn(doc, 'a').on_success).toBe(END);
		expect(chainLinks(stageIn(doc, 'a').on_fail).map((l) => l.to)).toEqual(['a']);
		// and b's route back is cut too
		expect(stageIn(doc, 'b').on_success).toBe(END);
	});

	it('places a stage moved to an empty chain where the sections say', () => {
		const doc = readDoc(`
name: f
version: 5
parameters: {}
main:
  - { id: a, type: command, run: a }
  - { id: b, type: command, run: b }
watchers:
  - { id: w, on: manual, stages: [] }
outcomes:
  - id: o
    on: mr_merged
    stages: [{ id: z, type: command, run: z }]
`);
		movePhase(doc, 'a', 'w');
		expect(ids(doc.stages)).toEqual(['b', 'a', 'z']);
	});

	it.each(['nowhere', 'a'])('moves nothing when the stage is unknown or at home: %s', (id) => {
		const doc = threeChains();
		movePhase(doc, id, 'main');
		expect(ids(doc.stages)).toEqual(['a', 'b', 'x', 'y', 'z']);
	});

	it('keeps a reserved route when pruning across chains', () => {
		const doc = aFlow([aStage({ id: 'a', on_fail: { to: ABORT }, on_success: DONE })]);
		pruneCrossPhase(doc);
		expect(stageIn(doc, 'a').on_fail).toEqual({ to: ABORT });
		expect(stageIn(doc, 'a').on_success).toBe(DONE);
	});

	it('lists a stage’s links by kind, then depth', () => {
		const s = aStage({
			id: 'a',
			on_fail: { to: 'x', max: 1, then: { to: DEBUG } },
			on_reject: { to: 'y', max: 1 },
			on_timeout: { to: ABORT }
		});
		expect(edgeLinks(s).map((l) => [l.kind, l.link.to, l.depth])).toEqual([
			['on_fail', 'x', 0],
			['on_fail', DEBUG, 1],
			['on_reject', 'y', 0],
			['on_timeout', ABORT, 0]
		]);
	});

	it.each<[Partial<DocStage>, boolean]>([
		[{}, false],
		[{ on_fail: { to: 'x', max: 1 } }, false],
		[{ on_fail: { to: DEBUG } }, true],
		[{ on_timeout: { to: 'x', max: 1, then: { to: DEBUG } } }, true],
		[{ on_reject: { to: DEBUG } }, true]
	])('knows whether a stage parks on debug: %j', (over, parks) => {
		expect(parksOnDebug(aStage({ id: 'a', ...over }))).toBe(parks);
	});

	it('finds what ends a chain, in end, done, abort order', () => {
		const doc = readDoc(`
name: f
version: 5
parameters: {}
main:
  - { id: m, type: command, run: m }
watchers:
  - id: w
    on: manual
    stages:
      - { id: a, type: command, run: a, on_success: done, on_fail: { to: a, max: 1, then: abort } }
      - { id: b, type: command, run: b }
`);
		expect(chainEnds(doc, doc.phases![1])).toEqual([END, DONE, ABORT]);
	});

	it.each([
		[undefined, [DONE]],
		[ABORT, [ABORT]]
	])('finds a chainless outcome’s end in its then (%s)', (then, ends) => {
		const ph: DocPhase = { id: 'o', kind: 'outcome', on: { signal: 'x' }, then };
		expect(chainEnds({ name: 'f', parameters: {}, stages: [], phases: [ph] }, ph)).toEqual(ends);
	});
});

describe('questions about the document', () => {
	it('lists the services a flow names, once each, as written', () => {
		const doc = aFlow([
			aStage({ id: 'a', type: 'service', service: '$app' }),
			aStage({ id: 'b', type: 'service', service: '$app' }),
			aStage({ id: 'c', input: { url: { from: 'run.service.web' } } }),
			aStage({ id: 'd', input: { plan: { from: 'docs/plan.md' } } })
		]);
		expect(flowServices(doc)).toEqual(['$app', 'web']);
	});

	it('looks for services in a sequence too', () => {
		const doc = aFlow([], {
			sequences: [{ id: 'q', stages: [aStage({ id: 'a', type: 'service', service: 'db' })] }]
		});
		expect(flowServices(doc)).toEqual(['db']);
	});

	it.each<[string, unknown, string | undefined]>([
		['a string', 'x', 'x'],
		['a number', 3, '3'],
		['false', false, 'false'],
		['a list', ['a', 'b'], 'a, b'],
		['nothing', undefined, undefined]
	])('words a default of %s', (_, value, text) => {
		const doc = normalizeDoc({ name: 'f', parameters: { p: { default: value } } });
		expect(paramDefault(doc, 'p')).toBe(text);
	});

	it('has no default for a parameter the flow does not declare', () => {
		expect(paramDefault(aFlow([]), 'p')).toBeUndefined();
	});

	it.each<[string, Partial<FlowDoc>, boolean]>([
		['an agent stage’s model', { stages: [aStage({ id: 'a', model: '$p' })] }, true],
		['an agent stage’s harness', { stages: [aStage({ id: 'a', harness: '$p' })] }, true],
		['the Ask pane', { stages: [], ask: { harness: '$p' } }, true],
		[
			'a sequence’s stage',
			{ stages: [], sequences: [{ id: 'q', stages: [aStage({ id: 'a', model: '$p' })] }] },
			true
		],
		['a timeout', { stages: [aStage({ id: 'a', timeout: '$p' })] }, false],
		['another parameter', { stages: [aStage({ id: 'a', model: '$px' })] }, false]
	])('knows %s reads a parameter as a model or a harness', (_, over, reads) => {
		const doc: FlowDoc = { name: 'f', parameters: {}, stages: [], ...over };
		expect(readAsModelOrHarness(doc, 'p')).toBe(reads);
	});

	it('finds an agent by name', () => {
		const doc = aFlow([], { agents: [{ name: 'a', prompt: 'p' }] });
		expect(agentOf(doc, 'a')?.prompt).toBe('p');
		expect(agentOf(doc, 'b')).toBeUndefined();
	});

	it('says what the canvas draws: a hub once something waits, sections past one chain', () => {
		const mainOnly = aFlow([aStage({ id: 'a' })]);
		expect(hasHub(mainOnly)).toBe(false);
		expect(showsSections(mainOnly)).toBe(false);
		expect(hasMain(mainOnly)).toBe(true);
		const all = threeChains();
		expect(hasHub(all)).toBe(true);
		expect(showsSections(all)).toBe(true);
		const none: FlowDoc = { name: 'f', parameters: {}, stages: [] };
		expect(hasHub(none)).toBe(false);
		expect(showsSections(none)).toBe(false);
		expect(hasMain(none)).toBe(false);
	});

	it('names the layout keys', () => {
		const watcher: DocPhase = { id: 'w', kind: 'watcher', on: { signal: 'manual' } };
		const main: DocPhase = { id: 'main', kind: 'main', on: { signal: '' } };
		expect(markerKey(undefined)).toBe(START);
		expect(markerKey(main)).toBe(START);
		expect(markerKey(watcher)).toBe('trigger:w');
		expect(terminalKey(END, main)).toBe(END);
		expect(terminalKey(END, undefined)).toBe(END);
		expect(terminalKey(ABORT, watcher)).toBe('abort:w');
		expect(sectionKey('outcome')).toBe('section:outcome');
		expect(debugKey('a')).toBe('debug:a');
	});
});

describe('docToYaml', () => {
	it('declares the grammar it writes, whatever the document said', () => {
		expect(written(aFlow([aStage({ id: 'a' })])).version).toBe(CURRENT_VERSION);
		expect(written({ ...aFlow([]), version: 4 }).version).toBe(4);
	});

	it('writes the top-level keys only when they say something', () => {
		const out = written({ name: 'f', parameters: {}, stages: [] });
		expect(Object.keys(out)).toEqual(['name', 'version', 'parameters']);
	});

	it.each([
		['ticket', undefined],
		['merge_request', 'merge_request']
	] as const)('writes operates_on %s only when it is not the default', (on, out) => {
		expect(written({ ...aFlow([]), operates_on: on }).operates_on).toBe(out);
	});

	it('writes no Ask pane for a blank harness', () => {
		expect(written({ ...aFlow([]), ask: { harness: '' } }).ask).toBeUndefined();
	});

	describe('a stage', () => {
		it.each<[string, Partial<DocStage>]>([
			['description', { description: 'note' }],
			['run_if', { run_if: 'not $plan_gate' }],
			['agent', { type: 'agent', agent: 'a' }],
			['service', { type: 'service', service: '$app' }],
			['do', { type: 'service', do: 'restart' }],
			['model', { model: '$model' }],
			['harness', { harness: 'opencode' }],
			['max_turns', { max_turns: 50 }],
			['max_turns', { max_turns: '$turns' }],
			['max_turns', { max_turns: 0 }],
			['reuse_session', { reuse_session: false }],
			['timeout', { timeout: '5m' }],
			['input', { input: { a: { from: 'x' } } }],
			['output', { output: { a: { to: 'x' } } }],
			['with', { with: { title: 't', labels: ['a'] } }],
			['run', { run: 'make' }],
			['run_ref', { run_ref: 'test' }],
			['each_repo', { each_repo: true }],
			['question', { question: 'Sure?' }],
			['action', { type: 'action', action: 'push' }],
			['for', { type: 'wait', for: 'ci_passed' }]
		])('keeps its %s through a trip', (key, over) => {
			const stage = aStage({ id: 'a', ...over });
			const out = written(aFlow([stage])).main[0];
			expect(out[key]).toEqual(stage[key as keyof DocStage]);
			expect(stageIn(throughEditor(aFlow([stage])), 'a')[key as keyof DocStage]).toEqual(
				stage[key as keyof DocStage]
			);
		});

		it.each<[string, Partial<DocStage>]>([
			['reuse_session', { reuse_session: true }],
			['input', { input: {} }],
			['output', { output: {} }],
			['with', { with: {} }],
			['each_repo', { each_repo: false }],
			['description', { description: '' }]
		])('writes no %s that says nothing', (key, over) => {
			expect(written(aFlow([aStage({ id: 'a', ...over })])).main[0]).not.toHaveProperty(key);
		});

		it('states its success explicitly, the sugar resolved', () => {
			const out = written(aFlow([aStage({ id: 'a' }), aStage({ id: 'b' })]));
			expect(out.main.map((s: DocStage) => s.on_success)).toEqual(['b', DONE]);
		});

		it('writes a port in canonical order, empty fields out', () => {
			const port = { description: 'd', schema: 's', type: 't', from: 'f', optional: true };
			const out = written(aFlow([aStage({ id: 'a', input: { p: port } })]));
			expect(Object.keys(out.main[0].input.p)).toEqual([
				'from',
				'type',
				'schema',
				'description',
				'optional'
			]);
			const bare = written(aFlow([aStage({ id: 'a', input: { p: { from: 'f', type: '' } } })]));
			expect(bare.main[0].input.p).toEqual({ from: 'f' });
		});

		it('keeps the ports in the order they were stated', () => {
			const input = { z: { from: '1' }, a: { from: '2' }, m: { from: '3' } };
			const out = written(aFlow([aStage({ id: 's', input })]));
			expect(Object.keys(out.main[0].input)).toEqual(['z', 'a', 'm']);
		});

		it('writes a sequence stage with only what the parser lets it say', () => {
			const doc = aFlow([
				{
					id: 'make',
					type: 'sequence',
					sequence: 'ship',
					description: 'd',
					on_fail: { to: 'make', max: 1 },
					on_success: DONE
				}
			]);
			expect(written(doc).main[0]).toEqual({
				id: 'make',
				sequence: 'ship',
				description: 'd',
				on_success: DONE,
				on_fail: { to: 'make', max: 1 }
			});
		});
	});

	describe('an edge', () => {
		const edgeOut = (e: DocEdge) =>
			written(aFlow([aStage({ id: 'a', on_fail: e })])).main[0].on_fail;

		it.each([END, DONE, ABORT, DEBUG, FAIL])('writes a link to %s as the bare word', (to) => {
			expect(edgeOut({ to })).toBe(to);
		});

		it('writes a stage link as a mapping, its fallback nested', () => {
			expect(edgeOut({ to: 'a', max: 2, then: { to: 'b', max: 1, then: { to: DEBUG } } })).toEqual({
				to: 'a',
				max: 2,
				then: { to: 'b', max: 1, then: DEBUG }
			});
		});

		it('keeps an unlimited retry', () => {
			expect(edgeOut({ to: 'a', max: -1 })).toEqual({ to: 'a', max: -1 });
		});
	});

	describe('the parameters', () => {
		const params = (parameters: FlowDoc['parameters'], stages: DocStage[] = []) =>
			written(aFlow(stages, { parameters })).parameters;

		it('writes the engine’s six in their order, then the flow’s in document order', () => {
			const out = params({
				zebra: { default: 1 },
				max_total_turns: { default: 1 },
				harness: { default: 'h' },
				alpha: { default: 2 },
				timeout: { default: '1m' }
			});
			expect(Object.keys(out)).toEqual(['harness', 'timeout', 'max_total_turns', 'zebra', 'alpha']);
		});

		it('writes one of the engine’s six as its bare default', () => {
			expect(params({ timeout: { default: '30m' } })).toEqual({ timeout: '30m' });
		});

		it('writes one of the six in full when it says more', () => {
			expect(params({ model: { default: 'a', description: 'd', enum: ['a', 'b'] } })).toEqual({
				model: { default: 'a', description: 'd', enum: ['a', 'b'] }
			});
		});

		it('never writes a type on one of the six: it is the engine’s', () => {
			expect(params({ max_turns: { type: 'int', default: 5, description: 'd' } })).toEqual({
				max_turns: { default: 5, description: 'd' }
			});
		});

		it('leaves out one of the six that has no default', () => {
			expect(params({ model: {} })).toEqual({});
		});

		it('declares what the flow’s own hold', () => {
			expect(
				params({
					labels: { type: 'string', list: true, default: ['a'], description: 'd', enum: ['a'] }
				})
			).toEqual({
				labels: { type: 'string', list: true, default: ['a'], description: 'd', enum: ['a'] }
			});
		});

		it.each([false, 0, ''])('keeps a default of %j', (value) => {
			expect(params({ p: { type: 'string', default: value } }).p.default).toBe(value);
		});

		it('writes a blank default on a model parameter as none: the workshop binds it', () => {
			const out = params({ m: { type: 'string', default: '' } }, [
				aStage({ id: 'a', model: '$m' })
			]);
			expect(out.m).toEqual({ type: 'string' });
		});

		it('keeps a default that reads another parameter', () => {
			expect(params({ p: { type: 'string', default: '$model' } }).p.default).toBe('$model');
		});
	});

	describe('the agents, schemas and servers', () => {
		it('writes an agent’s prompt last, and leaves out what it does not say', () => {
			const out = written(aFlow([], { agents: [{ name: 'a', prompt: 'p', mcp: [], inputs: [] }] }));
			expect(out.agents).toEqual([{ name: 'a', prompt: 'p' }]);
			const full = written(
				aFlow([], {
					agents: [
						{
							name: 'a',
							prompt: 'p',
							description: 'd',
							read_only: true,
							allow_bash: true,
							mcp: ['m'],
							inputs: [{ name: 'i' }],
							outputs: [{ name: 'o' }]
						}
					]
				})
			);
			expect(Object.keys(full.agents[0]).at(-1)).toBe('prompt');
		});

		it('keeps a multi-line prompt to the byte', () => {
			const prompt = 'First: "line".\n\n  indented # not a comment\n$harness stays text\n';
			expect(throughEditor(aFlow([], { agents: [{ name: 'a', prompt }] })).agents![0].prompt).toBe(
				prompt
			);
		});

		it('keeps a schema’s structure whole', () => {
			const json_schema = { type: 'object', properties: { a: { enum: [1, 'x', null] } } };
			const doc = aFlow([], { schemas: [{ name: 's', description: 'd', json_schema }] });
			expect(written(doc).schemas).toEqual([{ name: 's', description: 'd', json_schema }]);
		});

		it.each([
			[
				'builtin',
				{ name: 'm', builtin: 'browser', command: ['x'], url: 'u' },
				{ name: 'm', builtin: 'browser' }
			],
			[
				'url',
				{ name: 'm', url: 'u', headers: { a: 'b' }, env: { c: 'd' } },
				{ name: 'm', url: 'u', headers: { a: 'b' } }
			],
			[
				'command',
				{ name: 'm', command: ['x'], env: { c: 'd' }, headers: { a: 'b' } },
				{ name: 'm', command: ['x'], env: { c: 'd' } }
			],
			['nothing yet', { name: 'm' }, { name: 'm', command: [] }]
		])('writes a server by how it is reached: %s', (_, server, out) => {
			expect(written(aFlow([], { mcp_servers: [server] })).mcp_servers).toEqual([out]);
		});

		it('leaves out an empty env or headers', () => {
			const doc = aFlow([], {
				mcp_servers: [
					{ name: 'a', command: ['x'], env: {} },
					{ name: 'b', url: 'u', headers: {} }
				]
			});
			expect(written(doc).mcp_servers).toEqual([
				{ name: 'a', command: ['x'] },
				{ name: 'b', url: 'u' }
			]);
		});
	});

	describe('the chains', () => {
		it('writes a main-less flow with no `main`', () => {
			const doc = readDoc(`
name: f
version: 5
parameters: {}
operates_on: merge_request
watchers:
  - { id: w, on: manual, stages: [{ id: a, type: command, run: a }] }
`);
			expect(written(doc)).not.toHaveProperty('main');
		});

		it.each<[object, unknown]>([
			[{ signal: 'manual' }, 'manual'],
			[{ signal: 'x', start: 'auto' }, 'x'],
			[
				{ signal: 'x', start: 'confirm' },
				{ signal: 'x', start: 'confirm' }
			],
			[
				{ signal: 'x', quiet_for: '5m' },
				{ signal: 'x', quiet_for: '5m' }
			],
			[
				{ signal: 'x', max: 0 },
				{ signal: 'x', max: 0 }
			],
			[
				{ signal: 'x', start: '$p', quiet_for: '$q', max: '$m' },
				{ signal: 'x', start: '$p', quiet_for: '$q', max: '$m' }
			]
		])('writes the wait point %j as %j', (on, out) => {
			const doc = aFlow([], {
				watchers: [{ id: 'w', on, stages: [aStage({ id: 'a' })] }]
			} as unknown as Partial<FlowDoc>);
			expect(written(doc).watchers[0].on).toEqual(out);
		});

		it('writes a chain of one sequence stage named after it as `sequence:`', () => {
			const doc = readDoc(EVERY_FIELD);
			const hand = written(doc).watchers.find((w: { id: string }) => w.id === 'by_hand');
			expect(hand).toEqual({ id: 'by_hand', on: 'manual', sequence: 'outer' });
		});

		it.each<[string, Partial<DocStage>]>([
			['a description', { description: 'd' }],
			['an on_fail', { on_fail: { to: ABORT } }],
			['a success elsewhere', { on_success: DONE }],
			['another id', { id: 'other' }]
		])('writes the stage out when it has %s the shorthand cannot say', (_, over) => {
			const doc = readDoc(`
name: f
version: 5
parameters: {}
sequences: [{ id: ship, stages: [{ id: a, type: command, run: a }] }]
main: [{ id: m, type: command, run: m }]
watchers: [{ id: w, on: manual, sequence: ship }]
`);
			Object.assign(stageIn(doc, 'w'), over);
			const out = written(doc).watchers[0];
			expect(out).not.toHaveProperty('sequence');
			expect(out.stages).toHaveLength(1);
		});

		it('writes an outcome’s then, and done for one with no chain', () => {
			const doc = threeChains();
			doc.phases!.push(
				{ id: 'bare', kind: 'outcome', on: { signal: 'x' } },
				{ id: 'quit', kind: 'outcome', on: { signal: 'y' }, then: ABORT }
			);
			const out = written(doc).outcomes;
			expect(out[0]).not.toHaveProperty('then');
			expect(out[1]).toEqual({ id: 'bare', on: 'x', then: DONE });
			expect(out[2]).toEqual({ id: 'quit', on: 'y', then: ABORT });
		});

		it('writes the chains’ descriptions', () => {
			const out = written(readDoc(EVERY_FIELD));
			expect(out.watchers[0].description).toBe('keeps the branch mergeable');
			expect(out.outcomes[0].description).toBe('the merge happened');
		});

		it('writes sections in the order main, watchers, outcomes, after the definitions', () => {
			expect(Object.keys(written(readDoc(EVERY_FIELD)))).toEqual([
				'name',
				'version',
				'description',
				'operates_on',
				'ask',
				'parameters',
				'schemas',
				'mcp_servers',
				'agents',
				'sequences',
				'main',
				'watchers',
				'outcomes'
			]);
		});

		it('writes a sequence’s stages with their sugar left as it was', () => {
			const seq = written(readDoc(EVERY_FIELD)).sequences[0];
			expect(seq.description).toBe('test, then push');
			expect(seq.stages[0]).not.toHaveProperty('on_success');
			expect(seq.stages[1].on_success).toBe('test');
			expect(seq.stages[2].on_fail).toBe('fail');
		});
	});

	describe('a round trip', () => {
		it('loses nothing of a document that says everything', () => {
			const doc = readDoc(EVERY_FIELD);
			expect(throughEditor(doc)).toEqual(doc);
		});

		it('settles after one trip', () => {
			const once = docToYaml(readDoc(EVERY_FIELD));
			expect(docToYaml(readDoc(once))).toBe(once);
		});

		const root = resolve(import.meta.dirname, '../../../..');
		const shipped = [
			'internal/flow/builtin/flows',
			'internal/flow/testdata',
			'examples/flows'
		].flatMap((dir) =>
			readdirSync(resolve(root, dir))
				.filter((f) => f.endsWith('.yaml'))
				.map((f) => `${dir}/${f}`)
		);

		it('has the shipped flows to read', () => {
			expect(shipped).toContain('internal/flow/builtin/flows/builder.yaml');
		});

		// A stage off the success chain moves after it, and a `false` the
		// default says goes: neither changes what the flow does.
		const meaning = (doc: FlowDoc) => ({
			...doc,
			stages: [...doc.stages].sort((a, b) => a.id.localeCompare(b.id)),
			agents: doc.agents?.map((a) => ({
				...a,
				read_only: a.read_only || undefined,
				allow_bash: a.allow_bash || undefined
			}))
		});

		it.each(shipped)('loses nothing of %s', (path) => {
			const doc = readDoc(readFileSync(resolve(root, path), 'utf8'));
			expect(meaning(throughEditor(doc))).toEqual(meaning(doc));
		});
	});
});

describe('growing a flow', () => {
	it('adds a manual watcher with a command stage, before the outcomes', () => {
		const doc = threeChains();
		const ph = addWatcher(doc);
		expect(ph).toEqual({ id: 'watcher_1', kind: 'watcher', on: { signal: 'manual' } });
		expect(doc.phases?.map((p) => p.id)).toEqual(['main', 'w', 'watcher_1', 'o']);
		const added = stagesOf(doc, 'watcher_1');
		expect(added).toMatchObject([{ type: 'command', phase: 'watcher_1', on_success: END }]);
		expect(ids(doc.stages)).toEqual(['a', 'b', 'x', 'y', added[0].id, 'z']);
	});

	it('names a new chain with a free number', () => {
		const doc = threeChains();
		doc.phases!.push({ id: 'watcher_1', kind: 'watcher', on: { signal: 'manual' } });
		expect(addWatcher(doc).id).toBe('watcher_2');
		expect(addOutcome(doc).id).toBe('outcome_1');
		expect(addOutcome(doc).id).toBe('outcome_2');
	});

	it.each<[string, (d: FlowDoc) => unknown]>([
		['a watcher', addWatcher],
		['an outcome', addOutcome]
	])('keeps the run open for %s: the main chain ends with end', (_, add) => {
		const doc = aFlow([aStage({ id: 'a' }), aStage({ id: 'b' })]);
		expect(stageIn(doc, 'b').on_success).toBe(DONE);
		add(doc);
		expect(stageIn(doc, 'b').on_success).toBe(END);
	});

	const saysDone = (doc: FlowDoc) =>
		doc.phases?.some((p) => p.kind === 'outcome' && (p.then ?? DONE) === DONE) ||
		doc.stages.some(
			(s) => s.on_success === DONE || edgeLinks(s).some(({ link }) => link.to === DONE)
		);

	it('leaves a route that ends the run when it adds an outcome', () => {
		const doc = aFlow([aStage({ id: 'a' })]);
		addOutcome(doc);
		expect(saysDone(doc)).toBe(true);
	});

	// A watcher can't end the run, so it can't say `done`, and inventing an
	// outcome would decide how the flow ends for the author. Until one is added
	// the parser says what's missing: "no route ends the run".
	it('leaves ending the run to the author when it adds a watcher', () => {
		const doc = aFlow([aStage({ id: 'a' })]);
		addWatcher(doc);
		expect(saysDone(doc)).toBe(false);
		addOutcome(doc);
		expect(saysDone(doc)).toBe(true);
	});

	// The same refusal the other way: the main chain gets its `done` back when
	// the outcome that said it goes.
	it('leaves a route that ends the run when it removes the only outcome', () => {
		const doc = aFlow([aStage({ id: 'a' })]);
		const ph = addOutcome(doc);
		removePhase(doc, ph.id);
		expect(saysDone(doc)).toBe(true);
	});

	it('gives the main chain its `done` back when the last watcher goes', () => {
		const doc = aFlow([aStage({ id: 'a', on_success: END })]);
		doc.phases!.push({ id: 'w', kind: 'watcher', on: { signal: 'manual' } });
		removePhase(doc, 'w');
		expect(stageIn(doc, 'a').on_success).toBe(DONE);
	});

	it('keeps an `end` while something still follows it', () => {
		const doc = threeChains();
		stageIn(doc, 'b').on_success = END;
		removePhase(doc, 'w');
		expect(stageIn(doc, 'b').on_success).toBe(END);
	});

	it('keeps an `end` while another route ends the run', () => {
		const doc = aFlow([aStage({ id: 'a', on_fail: { to: DONE } }), aStage({ id: 'b' })]);
		stageIn(doc, 'b').on_success = END;
		doc.phases!.push({ id: 'w', kind: 'watcher', on: { signal: 'manual' } });
		removePhase(doc, 'w');
		expect(stageIn(doc, 'b').on_success).toBe(END);
	});

	it('keeps a `done` the author said earlier in the chain', () => {
		const doc = aFlow([aStage({ id: 'a', on_success: DONE }), aStage({ id: 'b' })]);
		addWatcher(doc);
		expect(stageIn(doc, 'a').on_success).toBe(DONE);
	});

	it('leaves the main chain alone once something already watches', () => {
		const doc = threeChains();
		stageIn(doc, 'b').on_success = DONE;
		addOutcome(doc);
		expect(stageIn(doc, 'b').on_success).toBe(DONE);
	});

	it('adds an outcome on the merge, with nothing to do first', () => {
		const doc = threeChains();
		const ph = addOutcome(doc);
		expect(ph).toEqual({
			id: 'outcome_1',
			kind: 'outcome',
			on: { signal: 'mr_merged' },
			then: DONE
		});
		expect(doc.phases?.at(-1)).toEqual(ph);
		expect(stagesOf(doc, ph.id)).toEqual([]);
	});

	it('removes a chain with its stages', () => {
		const doc = threeChains();
		removePhase(doc, 'w');
		expect(doc.phases?.map((p) => p.id)).toEqual(['main', 'o']);
		expect(ids(doc.stages)).toEqual(['a', 'b', 'z']);
	});

	it('gives a flow that only watches a main chain, at the front', () => {
		const doc = readDoc(`
name: f
version: 5
parameters: {}
watchers:
  - { id: w, on: manual, stages: [{ id: a, type: command, run: a }] }
`);
		const ph = addMainChain(doc);
		expect(doc.phases?.[0]).toBe(ph);
		expect(doc.stages[0]).toMatchObject({ type: 'command', phase: MAIN_ID, on_success: END });
	});

	it.each<[StageKind, string, Partial<DocStage>]>([
		['agent', 'agent_1', { agent: '' }],
		['command', 'cmd_1', { run_ref: '' }],
		['human_gate', 'gate_1', { question: '' }],
		['action', 'action_1', { action: '' }],
		['wait', 'wait_1', { for: '' }],
		['service', 'service_1', { service: '$app' }],
		['sequence', 'seq_1', { sequence: '' }]
	])('starts a new %s stage as %s', (type, id, over) => {
		const doc: FlowDoc = { name: 'f', parameters: {}, stages: [] };
		expect(freshStage(doc, type)).toEqual({ id, type, on_success: END, ...over });
	});

	it('skips the ids already taken', () => {
		const doc = aFlow([aStage({ id: 'cmd_1' }), aStage({ id: 'cmd_2' })]);
		expect(freshStage(doc, 'command').id).toBe('cmd_3');
		expect(freshStage(doc, 'command', []).id).toBe('cmd_1');
	});

	it('picks a new stage’s first chain, agent and sequence', () => {
		const doc = aFlow([aStage({ id: 'a' })], {
			agents: [{ name: 'worker', prompt: 'p' }],
			sequences: [{ id: 'ship', stages: [] }]
		});
		expect(freshStage(doc, 'agent')).toMatchObject({ agent: 'worker', phase: 'main' });
		expect(freshStage(doc, 'sequence').sequence).toBe('ship');
	});

	it('offers the main chain as what a new sequence stage runs, with no sequence', () => {
		expect(freshStage(aFlow([aStage({ id: 'a' })]), 'sequence').sequence).toBe('main');
	});

	it('names the service the flow already uses', () => {
		const doc = aFlow([aStage({ id: 's', type: 'service', service: 'web' })]);
		expect(freshStage(doc, 'service').service).toBe('web');
		expect(doc.parameters).toEqual({});
	});

	it('declares $app for the first service, once', () => {
		const doc: FlowDoc = { name: 'f', parameters: {}, stages: [] };
		expect(freshStage(doc, 'service').service).toBe('$app');
		expect(doc.parameters.app).toMatchObject({ type: 'string', default: 'default' });
		const mine = { type: 'string' as const, default: 'web' };
		const own: FlowDoc = { name: 'f', parameters: { app: mine }, stages: [] };
		freshStage(own, 'service');
		expect(own.parameters.app).toBe(mine);
	});

	it('appends a stage to a chain, wired in and ending the way the last stage did', () => {
		const doc = threeChains();
		const s = appendStage(doc, 'command', 'w');
		expect(ids(doc.stages)).toEqual(['a', 'b', 'x', 'y', s.id, 'z']);
		expect(stageIn(doc, 'y').on_success).toBe(s.id);
		expect(s).toMatchObject({ phase: 'w', on_success: END });
	});

	it('carries a `done` on to the stage appended after it', () => {
		const doc = aFlow([aStage({ id: 'a' })]);
		const s = appendStage(doc, 'command', 'main');
		expect(stageIn(doc, 'a').on_success).toBe(s.id);
		expect(s.on_success).toBe(DONE);
	});

	it('leaves a last stage that routes elsewhere alone', () => {
		const doc = aFlow([aStage({ id: 'a' }), aStage({ id: 'b', on_success: 'a' })]);
		const s = appendStage(doc, 'command', 'main');
		expect(stageIn(doc, 'b').on_success).toBe('a');
		expect(s.on_success).toBe(DONE);
	});

	it('sets what it is given before the stage joins the document', () => {
		const doc = threeChains();
		const s = appendStage(doc, 'agent', 'w', { id: 'mine', agent: 'worker' });
		expect(s).toMatchObject({ id: 'mine', agent: 'worker' });
		expect(stageIn(doc, 'y').on_success).toBe('mine');
	});

	it('puts the first stage of an empty chain where the sections say', () => {
		const doc = threeChains();
		doc.phases!.splice(2, 0, { id: 'empty', kind: 'watcher', on: { signal: 'manual' } });
		const s = appendStage(doc, 'command', 'empty');
		expect(ids(doc.stages)).toEqual(['a', 'b', 'x', 'y', s.id, 'z']);
		expect(s.on_success).toBe(END);
	});

	it('ends a chainless outcome’s first stage the way its then says', () => {
		const doc = threeChains();
		doc.phases!.push({ id: 'quit', kind: 'outcome', on: { signal: 'y' }, then: ABORT });
		expect(appendStage(doc, 'command', 'quit').on_success).toBe(ABORT);
	});
});

const MAIN_ID = 'main';

describe('sequences', () => {
	const withSequence = () =>
		readDoc(`
name: f
version: 5
parameters: {}
sequences:
  - id: ship
    stages:
      - { id: test, type: command, run: t }
      - { id: push, type: action, action: push, on_success: end }
  - id: other
    stages:
      - { id: nested, sequence: ship }
main:
  - { id: make, sequence: ship }
watchers:
  - { id: w, on: manual, sequence: other }
`);

	it('appends to a sequence with sugar: the last stage goes on by itself', () => {
		const doc = aFlow([], { sequences: [{ id: 'q', stages: [aStage({ id: 'a' })] }] });
		const s = appendSequenceStage(doc, 'command', 'q')!;
		expect(s).toMatchObject({ id: 'cmd_1', phase: undefined, on_success: undefined });
		expect(doc.sequences![0].stages[0].on_success).toBeUndefined();
	});

	it('carries a reserved way out on to the stage appended', () => {
		const doc = withSequence();
		const q = doc.sequences![0];
		const s = appendSequenceStage(doc, 'command', 'ship')!;
		expect(s.on_success).toBe(END);
		expect(q.stages[1].on_success).toBeUndefined();
	});

	it('numbers a sequence’s stages on their own, apart from the chains', () => {
		const doc = aFlow([aStage({ id: 'cmd_1' })], { sequences: [{ id: 'q', stages: [] }] });
		expect(appendSequenceStage(doc, 'command', 'q')?.id).toBe('cmd_1');
	});

	it('answers nothing for an unknown sequence', () => {
		expect(appendSequenceStage(aFlow([]), 'command', 'q')).toBeUndefined();
	});

	it.each([
		['another sequence', 'ship', 'other'],
		['the next one', 'other', 'ship']
	])('never lets a sequence run itself: %s', (_, id, runs) => {
		const doc = withSequence();
		doc.sequences = [...doc.sequences!].sort((a) => (a.id === id ? -1 : 1));
		expect(appendSequenceStage(doc, 'sequence', id)?.sequence).toBe(runs);
	});

	it('falls back on the main chain when it is the only sequence', () => {
		const doc = aFlow([aStage({ id: 'a' })], { sequences: [{ id: 'q', stages: [] }] });
		expect(appendSequenceStage(doc, 'sequence', 'q')?.sequence).toBe('main');
	});

	it.each<[string, -1 | 1, string[]]>([
		['test', 1, ['push', 'test']],
		['push', -1, ['push', 'test']],
		['test', -1, ['test', 'push']],
		['push', 1, ['test', 'push']],
		['nowhere', 1, ['test', 'push']]
	])('moves %s by %i', (stage, delta, order) => {
		const doc = withSequence();
		moveSequenceStage(doc, 'ship', stage, delta);
		expect(ids(doc.sequences![0].stages)).toEqual(order);
	});

	it('finds who runs a sequence, in a chain or a sequence', () => {
		expect(sequenceUsers(withSequence(), 'ship')).toEqual(['make', 'nested']);
		expect(sequenceUsers(withSequence(), 'nobody')).toEqual([]);
	});

	it('adds a sequence of one sugar command stage under a free name', () => {
		const doc = aFlow([]);
		const q = addSequence(doc);
		expect(q.id).toBe('sequence_1');
		expect(q.stages).toEqual([{ id: 'cmd_1', type: 'command', run_ref: '' }]);
		expect(addSequence(doc).id).toBe('sequence_2');
		expect(doc.sequences).toHaveLength(2);
	});

	it('renames a sequence and everything that runs it', () => {
		const doc = withSequence();
		renameSequence(doc, 'ship', 'deliver');
		expect(doc.sequences![0].id).toBe('deliver');
		expect(stageIn(doc, 'make').sequence).toBe('deliver');
		expect(doc.sequences![1].stages[0].sequence).toBe('deliver');
	});

	it('survives a rename written through the document', () => {
		const doc = withSequence();
		renameSequence(doc, 'other', 'renamed');
		expect(written(doc).watchers[0]).toMatchObject({ id: 'w', sequence: 'renamed' });
	});

	it('renames nothing for an unknown sequence', () => {
		const doc = withSequence();
		renameSequence(doc, 'nope', 'x');
		expect(doc.sequences?.map((q) => q.id)).toEqual(['ship', 'other']);
	});

	it('removes a sequence, the list with its last', () => {
		const doc = withSequence();
		removeSequence(doc, 'other');
		expect(doc.sequences?.map((q) => q.id)).toEqual(['ship']);
		removeSequence(doc, 'ship');
		expect(doc.sequences).toBeUndefined();
	});

	it('turns a chain into one running a sequence, and back into stages of its own', () => {
		const doc = withSequence();
		setChainSequence(doc, 'w', 'ship');
		expect(stagesOf(doc, 'w')).toMatchObject([
			{ id: 'w', type: 'sequence', sequence: 'ship', phase: 'w' }
		]);
		expect(chainSequence(doc, doc.phases![1])).toBe('ship');
		setChainSequence(doc, 'w', undefined);
		expect(stagesOf(doc, 'w')).toMatchObject([{ type: 'command', phase: 'w' }]);
		expect(chainSequence(doc, doc.phases![1])).toBeUndefined();
	});

	it('drops an outcome’s stages for its then alone', () => {
		const doc = threeChains();
		setChainSequence(doc, 'o', undefined);
		expect(stagesOf(doc, 'o')).toEqual([]);
	});

	it('names the chain’s stage otherwise when a stage elsewhere has its id', () => {
		const doc = threeChains();
		stageIn(doc, 'a').id = 'w';
		stageIn(doc, 'b').on_success = END;
		setChainSequence(doc, 'w', 'ship');
		const own = stagesOf(doc, 'w');
		expect(own).toHaveLength(1);
		expect(own[0].id).not.toBe('w');
		expect(chainSequence(doc, doc.phases![1])).toBeUndefined();
	});

	it('does nothing for an unknown chain', () => {
		const doc = withSequence();
		setChainSequence(doc, 'nope', 'ship');
		expect(ids(doc.stages)).toEqual(['make', 'w']);
	});
});

describe('renames and removals', () => {
	const doc = () =>
		readDoc(`
name: f
version: 5
parameters: {}
schemas:
  - { name: report, json_schema: { type: object } }
  - { name: other, json_schema: { type: object } }
mcp_servers:
  - { name: browser, builtin: browser }
  - { name: docs, url: https://d.test }
agents:
  - name: writer
    prompt: p
    mcp: [browser, docs]
    inputs: [{ name: notes, schema: report }, { name: plain }]
    outputs: [{ name: out, schema: report }]
  - name: reader
    prompt: p
    mcp: [browser]
sequences:
  - id: q
    stages:
      - id: inner
        type: agent
        agent: writer
        input: { notes: { from: a, schema: report } }
main:
  - id: top
    type: agent
    agent: writer
    input:
      first: { from: a }
      notes: { from: b, schema: report }
      last: { from: c }
    output:
      out: { to: d, schema: report }
  - id: tool
    type: command
    run: r
    input: { other: { from: x, schema: report } }
    output: { other: { to: y, schema: other } }
`);

	it('finds every port naming a schema: agents, chains, sequences', () => {
		expect(schemaUsers(doc(), 'report')).toEqual([
			{ agent: 'writer', port: 'notes' },
			{ agent: 'writer', port: 'out' },
			{ stage: 'top', sequence: undefined, port: 'notes' },
			{ stage: 'top', sequence: undefined, port: 'out' },
			{ stage: 'tool', sequence: undefined, port: 'other' },
			{ stage: 'inner', sequence: 'q', port: 'notes' }
		]);
		expect(schemaUsers(doc(), 'nobody')).toEqual([]);
	});

	it('renames a schema everywhere it is named', () => {
		const d = doc();
		renameSchema(d, 'report', 'summary');
		expect(d.schemas?.map((q) => q.name)).toEqual(['summary', 'other']);
		expect(schemaUsers(d, 'report')).toEqual([]);
		expect(schemaUsers(d, 'summary')).toHaveLength(6);
		expect(schemaUsers(d, 'other')).toEqual([
			{ stage: 'tool', sequence: undefined, port: 'other' }
		]);
	});

	it('renames nothing for an unknown schema', () => {
		const d = doc();
		renameSchema(d, 'nope', 'x');
		expect(d.schemas?.map((q) => q.name)).toEqual(['report', 'other']);
	});

	it('adds an object schema under a free name', () => {
		const d = doc();
		d.schemas!.push({ name: 'schema_1', json_schema: {} });
		expect(addSchema(d)).toEqual({
			name: 'schema_2',
			json_schema: { type: 'object', properties: {} }
		});
		const none = aFlow([]);
		expect(addSchema(none).name).toBe('schema_1');
	});

	it('removes a schema, the list with its last', () => {
		const d = doc();
		removeSchema(d, 'report');
		expect(d.schemas?.map((q) => q.name)).toEqual(['other']);
		removeSchema(d, 'other');
		expect(d.schemas).toBeUndefined();
	});

	it('finds the agents listing a server', () => {
		expect(mcpUsers(doc(), 'browser')).toEqual(['writer', 'reader']);
		expect(mcpUsers(doc(), 'nobody')).toEqual([]);
	});

	it('renames a server and every mention of it', () => {
		const d = doc();
		renameMCPServer(d, 'browser', 'web');
		expect(d.mcp_servers?.[0].name).toBe('web');
		expect(d.agents?.map((a) => a.mcp)).toEqual([['web', 'docs'], ['web']]);
	});

	it('renames nothing for an unknown server', () => {
		const d = doc();
		renameMCPServer(d, 'nope', 'web');
		expect(d.agents?.[0].mcp).toEqual(['browser', 'docs']);
	});

	it('removes a server from every agent, an agent’s list with its last', () => {
		const d = doc();
		removeMCPServer(d, 'browser');
		expect(d.mcp_servers?.map((m) => m.name)).toEqual(['docs']);
		expect(d.agents?.map((a) => a.mcp)).toEqual([['docs'], undefined]);
		removeMCPServer(d, 'docs');
		expect(d.mcp_servers).toBeUndefined();
	});

	it('offers the browser first, once', () => {
		const d = aFlow([]);
		expect(addMCPServer(d)).toEqual({ name: 'browser', builtin: 'browser' });
		expect(addMCPServer(d)).toEqual({ name: 'mcp_1', command: [] });
		expect(addMCPServer(d)).toEqual({ name: 'mcp_2', command: [] });
	});

	it('offers a command when the name browser is taken', () => {
		const d = aFlow([], { mcp_servers: [{ name: 'browser', command: ['x'] }] });
		expect(addMCPServer(d)).toEqual({ name: 'mcp_1', command: [] });
	});

	it('offers a command when the browser goes by another name', () => {
		const d = aFlow([], { mcp_servers: [{ name: 'web', builtin: 'browser' }] });
		expect(addMCPServer(d).name).toBe('mcp_1');
	});

	it('finds the stages running an agent, in a chain or a sequence', () => {
		expect(agentUsers(doc(), 'writer')).toEqual(['top', 'inner']);
		expect(agentUsers(doc(), 'reader')).toEqual([]);
	});

	it('renames an agent and what runs it', () => {
		const d = doc();
		renameAgent(d, 'writer', 'author');
		expect(d.agents?.[0].name).toBe('author');
		expect(agentUsers(d, 'author')).toEqual(['top', 'inner']);
		expect(agentUsers(d, 'writer')).toEqual([]);
	});

	it('renames nothing for an unknown agent', () => {
		const d = doc();
		renameAgent(d, 'nope', 'x');
		expect(d.agents?.map((a) => a.name)).toEqual(['writer', 'reader']);
	});

	it('leaves a command stage’s run alone when an agent of that name is renamed', () => {
		const d = aFlow([aStage({ id: 'a', type: 'command', agent: 'x' })], {
			agents: [{ name: 'x', prompt: 'p' }]
		});
		renameAgent(d, 'x', 'y');
		expect(d.stages[0].agent).toBe('x');
	});

	it('renames a port on every stage running the agent, keeping the order', () => {
		const d = doc();
		renameAgentPort(d, 'writer', 'input', 'notes', 'memo');
		expect(Object.keys(stageIn(d, 'top').input!)).toEqual(['first', 'memo', 'last']);
		expect(Object.keys(d.sequences![0].stages[0].input!)).toEqual(['memo']);
	});

	it('renames an output apart from an input, and only that agent’s stages', () => {
		const d = doc();
		renameAgentPort(d, 'writer', 'output', 'out', 'result');
		expect(Object.keys(stageIn(d, 'top').output!)).toEqual(['result']);
		expect(Object.keys(stageIn(d, 'top').input!)).toContain('notes');
		renameAgentPort(d, 'writer', 'input', 'other', 'renamed');
		expect(Object.keys(stageIn(d, 'tool').input!)).toEqual(['other']);
	});

	it.each<[string | undefined, string]>([
		[undefined, 'agent_1'],
		['planner', 'planner'],
		['Not A Slug', 'agent_1'],
		['', 'agent_1']
	])('adds an agent asked to be called %j as %s', (name, out) => {
		const d = aFlow([]);
		expect(addAgent(d, name)).toEqual({ name: out, prompt: '' });
	});

	it('takes the next free name when the one asked for is taken', () => {
		const d = aFlow([], {
			agents: [
				{ name: 'agent_1', prompt: '' },
				{ name: 'planner', prompt: '' }
			]
		});
		expect(addAgent(d, 'planner').name).toBe('agent_2');
	});

	it('lists every stage of the document', () => {
		expect(ids(allStages(doc()))).toEqual(['top', 'tool', 'inner']);
	});
});

describe('layout', () => {
	const noOverlap = (
		pos: Record<string, { x: number; y: number }>,
		keys: string[],
		w: number,
		h: number
	) => {
		for (const a of keys)
			for (const b of keys) {
				if (a === b) continue;
				const apart = Math.abs(pos[a].x - pos[b].x) >= w || Math.abs(pos[a].y - pos[b].y) >= h;
				expect(apart, `${a} overlaps ${b}`).toBe(true);
			}
	};

	it('places a chain left to right along its success', () => {
		const doc = aFlow([
			aStage({ id: 'a', on_success: 'c' }),
			aStage({ id: 'b', on_success: DONE }),
			aStage({ id: 'c', on_success: 'b' })
		]);
		const pos = autoLayout(doc);
		expect(pos.a.x).toBeLessThan(pos.c.x);
		expect(pos.c.x).toBeLessThan(pos.b.x);
		expect(pos.a.y).toBe(pos.b.y);
	});

	it('drops stages off the chain into a lane below', () => {
		const doc = aFlow([
			aStage({ id: 'a', on_success: DONE }),
			aStage({ id: 'b', on_success: DONE })
		]);
		const pos = autoLayout(doc);
		expect(pos.b.y).toBeGreaterThan(pos.a.y);
	});

	it('gives every chain a band of its own, top to bottom, none overlapping', () => {
		const doc = threeChains();
		const pos = autoLayout(doc);
		expect(pos.a.y).toBeLessThan(pos.x.y);
		expect(pos.x.y).toBeLessThan(pos.z.y);
		noOverlap(pos, ['a', 'b', 'x', 'y', 'z'], 220, 120);
	});

	it('uses what was measured: a wider node pushes the next one', () => {
		const doc = aFlow([aStage({ id: 'a' }), aStage({ id: 'b' })]);
		const guessed = autoLayout(doc).b.x;
		const measured = autoLayout(doc, { a: { width: 500, height: 100 } }).b.x;
		expect(measured - guessed).toBe(280);
	});

	it('keeps a taller row clear of the band below', () => {
		const doc = threeChains();
		const tall = autoLayout(doc, { a: { width: 220, height: 800 } });
		expect(tall.x.y - tall.a.y).toBeGreaterThan(800);
	});

	it('lines the chains’ markers up in one column left of the stages', () => {
		const pos = autoLayout(threeChains());
		expect(pos[START].x).toBe(pos['trigger:w'].x);
		expect(pos[START].x).toBe(pos['trigger:o'].x);
		expect(pos[START].x).toBeLessThan(0);
	});

	it('gives the hub its own row, left of the markers, below the main chain', () => {
		const pos = autoLayout(threeChains());
		expect(pos[HUB].x).toBeLessThan(pos[START].x);
		expect(pos[HUB].y).toBeGreaterThan(pos.a.y);
		expect(pos[HUB].y).toBeLessThan(pos.x.y);
	});

	it('puts a watch-only flow’s start above its hub', () => {
		const pos = autoLayout(
			readDoc(`
name: f
version: 5
parameters: {}
watchers:
  - { id: w, on: manual, stages: [{ id: a, type: command, run: a }] }
`)
		);
		expect(pos[START].x).toBe(pos[HUB].x);
		expect(pos[START].y).toBeLessThan(pos[HUB].y);
	});

	it('heads each section with its label, only once there are sections', () => {
		const pos = autoLayout(threeChains());
		expect(pos[sectionKey('watcher')].y).toBeLessThan(pos['trigger:w'].y);
		expect(pos[sectionKey('outcome')].y).toBeLessThan(pos['trigger:o'].y);
		expect(autoLayout(aFlow([aStage({ id: 'a' })]))).not.toHaveProperty(sectionKey('main'));
	});

	it('closes a watcher’s row with its ends', () => {
		const doc = threeChains();
		const pos = autoLayout(doc);
		expect(pos['end:w'].x).toBeGreaterThan(pos.y.x);
		expect(pos['end:w'].y).toBe(pos.x.y);
		expect(pos['done:o'].y).toBe(pos.z.y);
	});

	it('sets a debug leaf under its stage, below the edge labels', () => {
		const doc = aFlow([aStage({ id: 'a', on_fail: { to: DEBUG } })]);
		const pos = autoLayout(doc);
		expect(pos[debugKey('a')].x).toBe(pos.a.x + 24);
		expect(pos[debugKey('a')].y).toBeGreaterThan(pos.a.y + 120);
	});

	it('stacks the main chain’s ends right of the widest chain, or of main beside a hub', () => {
		const lone = autoLayout(aFlow([aStage({ id: 'a' }), aStage({ id: 'b' })]));
		expect(lone[END].x).toBeGreaterThan(lone.b.x);
		expect(lone[DONE].y).toBeGreaterThan(lone[END].y);
		expect(lone[ABORT].y).toBeGreaterThan(lone[DONE].y);
		const doc = threeChains();
		appendStage(doc, 'command', 'w');
		const hubbed = autoLayout(doc);
		expect(hubbed[END].x).toBeLessThan(hubbed.y.x + 220 * 2);
	});

	it('lays out a chain that is only its marker without leaving a stage band', () => {
		const doc = threeChains();
		doc.phases!.push({ id: 'bare', kind: 'outcome', on: { signal: 'x' } });
		const pos = autoLayout(doc);
		expect(pos['trigger:bare'].y).toBeGreaterThan(pos.z.y);
	});

	it('lays out an empty document', () => {
		expect(autoLayout({ name: 'f', parameters: {}, stages: [] })[START].x).toBeLessThan(0);
	});

	it('survives a success loop', () => {
		const doc = aFlow([aStage({ id: 'a', on_success: 'b' }), aStage({ id: 'b', on_success: 'a' })]);
		expect(Object.keys(autoLayout(doc))).toEqual(expect.arrayContaining(['a', 'b']));
	});

	describe('placeChain', () => {
		const laidOut = () => {
			const doc = threeChains();
			return { doc, layout: autoLayout(doc) };
		};

		it('leaves a chain already placed where it is', () => {
			const { doc, layout } = laidOut();
			const before = structuredClone(layout);
			placeChain(doc, layout, 'w');
			expect(layout).toEqual(before);
		});

		it('ignores a chain the document does not have', () => {
			const { doc, layout } = laidOut();
			const before = structuredClone(layout);
			placeChain(doc, layout, 'nope');
			expect(layout).toEqual(before);
		});

		it('lands a new watcher among the watchers, the outcomes moving down', () => {
			const { doc, layout } = laidOut();
			const before = structuredClone(layout);
			const ph = addWatcher(doc);
			placeChain(doc, layout, ph.id);
			const stage = stagesOf(doc, ph.id)[0].id;
			expect(layout[`trigger:${ph.id}`].y).toBeGreaterThan(before.y.y);
			expect(layout[stage].y).toBe(layout[`trigger:${ph.id}`].y);
			expect(layout.a).toEqual(before.a);
			expect(layout.x).toEqual(before.x);
			expect(layout.z.y).toBeGreaterThan(layout[stage].y);
			expect(layout['trigger:o'].y - before['trigger:o'].y).toBe(layout.z.y - before.z.y);
			expect(layout[sectionKey('outcome')].y).toBeGreaterThan(before[sectionKey('outcome')].y);
		});

		it('lines the new chain’s stages up with the others’ first', () => {
			const { doc, layout } = laidOut();
			const ph = addWatcher(doc);
			placeChain(doc, layout, ph.id);
			expect(layout[stagesOf(doc, ph.id)[0].id].x).toBe(layout.x.x);
			expect(layout[`trigger:${ph.id}`].x).toBe(layout[START].x);
		});

		it('closes the new chain’s row with its ends', () => {
			const { doc, layout } = laidOut();
			const ph = addWatcher(doc);
			placeChain(doc, layout, ph.id);
			expect(layout[`end:${ph.id}`].x).toBeGreaterThan(layout[stagesOf(doc, ph.id)[0].id].x);
		});

		it('opens a section with its label and the hub when it is the first branch', () => {
			const doc = aFlow([aStage({ id: 'a' })]);
			const layout = autoLayout(doc);
			const ph = addOutcome(doc);
			placeChain(doc, layout, ph.id);
			expect(layout[HUB]).toBeDefined();
			expect(layout[sectionKey('outcome')].y).toBeLessThan(layout[`trigger:${ph.id}`].y);
			expect(layout[`done:${ph.id}`]).toBeDefined();
		});

		it('places a main chain added to a flow that only watched, above nothing', () => {
			const doc = threeChains();
			const layout = autoLayout(doc);
			delete layout[START];
			removePhase(doc, 'main');
			const before = layout.x.y;
			const ph = addMainChain(doc);
			placeChain(doc, layout, ph.id);
			expect(layout[START]).toBeDefined();
			expect(layout.x.y).toBeGreaterThan(before);
		});

		it('starts a first chain on an empty canvas', () => {
			const doc: FlowDoc = { name: 'f', parameters: {}, stages: [] };
			const layout = {};
			const ph = addWatcher(doc);
			placeChain(doc, layout, ph.id);
			expect(layout).toHaveProperty(`trigger:${ph.id}`);
		});
	});

	describe('columnLayout', () => {
		it('stacks the stages down, the marker above and the ways out under the last', () => {
			const doc: FlowDoc = {
				name: 'f',
				parameters: {},
				stages: [aStage({ id: 'a' }), aStage({ id: 'b' })]
			};
			const pos = columnLayout(doc);
			expect(pos.a).toEqual({ x: 0, y: 0 });
			expect(pos.b.x).toBe(0);
			expect(pos.b.y).toBeGreaterThan(120);
			expect(pos[START].y).toBeLessThan(0);
			expect(pos[END].y).toBeGreaterThan(pos.b.y);
			expect(pos[DONE].y).toBe(pos[END].y);
			expect(pos[DONE].x).toBeGreaterThan(pos[END].x);
		});

		it('sets a debug leaf right of its stage', () => {
			const doc: FlowDoc = {
				name: 'f',
				parameters: {},
				stages: [aStage({ id: 'a', on_fail: { to: DEBUG } })]
			};
			const pos = columnLayout(doc, { a: { width: 300, height: 100 } });
			expect(pos[debugKey('a')]).toEqual({ x: 356, y: 50 });
		});

		it('leaves room under a stage for its edge labels', () => {
			const plain: FlowDoc = {
				name: 'f',
				parameters: {},
				stages: [aStage({ id: 'a' }), aStage({ id: 'b' })]
			};
			const labelled: FlowDoc = {
				...plain,
				stages: [aStage({ id: 'a', on_fail: { to: 'b', max: 1 } }), aStage({ id: 'b' })]
			};
			expect(columnLayout(labelled).b.y).toBeGreaterThan(columnLayout(plain).b.y);
		});
	});
});

describe('dataflow', () => {
	const agents = [
		{
			name: 'writer',
			inputs: [
				{ name: 'ticket', description: 'the task', type: 'ticket' },
				{ name: 'notes', type: 'file', schema: 'report' }
			],
			outputs: [
				{ name: 'plan', type: 'file' },
				{ name: 'verdict', type: 'verdict' }
			]
		}
	];
	const actions = [
		{
			slug: 'push',
			inputs: [{ name: 'message', type: 'commit_message', required: true }],
			outputs: [{ name: 'sha' }]
		}
	];

	it('lists a contract’s ports in order, the unstated ones flagged, then the extras', () => {
		const s = aStage({
			id: 'a',
			type: 'agent',
			agent: 'writer',
			input: { extra: { from: 'x' }, notes: { from: 'docs/n.md' } }
		});
		expect(stageInputs(s, agents)).toEqual([
			{
				name: 'ticket',
				value: '',
				engine: false,
				type: 'ticket',
				schema: undefined,
				description: 'the task',
				unbound: true
			},
			{
				name: 'notes',
				value: 'docs/n.md',
				engine: false,
				type: 'file',
				schema: 'report',
				description: undefined,
				unbound: undefined
			},
			{
				name: 'extra',
				value: 'x',
				engine: false,
				type: undefined,
				schema: undefined,
				description: undefined,
				unbound: undefined
			}
		]);
	});

	it('lets what a stage states win over its contract', () => {
		const s = aStage({
			id: 'a',
			type: 'agent',
			agent: 'writer',
			input: { notes: { from: 'x', type: 'verdict', schema: 'mine', description: 'own' } }
		});
		expect(stageInputs(s, agents)[1]).toMatchObject({
			type: 'verdict',
			schema: 'mine',
			description: 'own'
		});
	});

	it('marks a value the engine provides', () => {
		const s = aStage({ id: 'a', input: { t: { from: 'run.ticket' } } });
		expect(stageInputs(s)[0]).toMatchObject({ engine: true, value: 'run.ticket' });
	});

	it('reads an action’s contract by its slug', () => {
		const s = aStage({ id: 'a', type: 'action', action: 'push' });
		expect(stageInputs(s, [], actions).map((p) => p.name)).toEqual(['message']);
		expect(stageOutputs(s, [], actions).map((p) => p.name)).toEqual(['sha']);
	});

	it.each<[StageKind, number, number]>([
		['command', 1, 1],
		['human_gate', 1, 0],
		['wait', 1, 0],
		['service', 0, 0],
		['sequence', 0, 0]
	])('shows a %s’s ports as written, or none', (type, inputs, outputs) => {
		const s = aStage({ id: 'a', type, input: { i: { from: 'x' } }, output: { o: { to: 'y' } } });
		expect(stageInputs(s)).toHaveLength(inputs);
		expect(stageOutputs(s)).toHaveLength(outputs);
	});

	it.each<[string, DocStage, boolean]>([
		['a gate', aStage({ id: 'a', type: 'human_gate' }), true],
		['a command', aStage({ id: 'a' }), false],
		['an agent writing a verdict', aStage({ id: 'a', type: 'agent', agent: 'writer' }), true],
		['an agent without one', aStage({ id: 'a', type: 'agent', agent: 'other' }), false],
		[
			'an agent stating one',
			aStage({ id: 'a', type: 'agent', output: { v: { to: 'x', type: 'verdict' } } }),
			true
		]
	])('says whether %s gives a verdict', (_, s, gives) => {
		expect(givesVerdict(s, agents)).toBe(gives);
	});

	it('finds an agent’s verdict output', () => {
		expect(verdictOutput(agents[0])?.name).toBe('verdict');
		expect(verdictOutput({ name: 'x' })).toBeUndefined();
		expect(verdictOutput(undefined)).toBeUndefined();
	});

	it('connects each input to the stage that writes its path', () => {
		const doc = aFlow([
			aStage({ id: 'w', output: { out: { to: 'p.md' } } }),
			aStage({ id: 'r', input: { in: { from: 'p.md' } } })
		]);
		expect(deriveDataLinks(doc)).toEqual([
			{ source: 'w', sourceHandle: 'out:out', target: 'r', targetHandle: 'in:in', value: 'p.md' }
		]);
	});

	it('points a link at the last writer of a path', () => {
		const doc = aFlow([
			aStage({ id: 'one', output: { o: { to: 'p.md' } } }),
			aStage({ id: 'two', output: { o: { to: 'p.md' } } }),
			aStage({ id: 'r', input: { i: { from: 'p.md' } } })
		]);
		expect(deriveDataLinks(doc).map((l) => l.source)).toEqual(['two']);
	});

	it('draws nothing to itself, from nobody, or for what the engine provides', () => {
		const doc = aFlow([
			aStage({
				id: 'a',
				input: {
					self: { from: 'a.md' },
					none: { from: 'nobody.md' },
					engine: { from: 'run.ticket' },
					blank: {}
				},
				output: { o: { to: 'a.md' }, ticket: { to: 'run.ticket' } }
			})
		]);
		expect(deriveDataLinks(doc)).toEqual([]);
	});

	it('finds which input reads what an edge carries', () => {
		const doc = aFlow([
			aStage({
				id: 'fix',
				input: {
					why: { from: 'edge.failure' },
					said: { from: 'edge.verdict' },
					plan: { from: 'plan.md' }
				}
			})
		]);
		expect([...feedDots(doc)]).toEqual([
			['fix|edge.failure', 'why'],
			['fix|edge.verdict', 'said']
		]);
	});
});
