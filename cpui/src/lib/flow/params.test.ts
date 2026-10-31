import { describe, expect, it } from 'vitest';
import type { DocParam, FlowDoc } from '$lib/domain/flowdoc';
import { readDoc } from '$test/fixtures/logic';
import {
	boolParams,
	customParams,
	defaultText,
	durationParams,
	enumParamsWithin,
	intParams,
	isKnown,
	isParamRef,
	paramKind,
	paramRefs,
	paramType,
	parseDefaultFor,
	parseValue,
	policyParams,
	renameParam,
	stringListParams,
	stringParams,
	type ParamKind,
	type ParamType
} from './params';

const flow = (): FlowDoc =>
	readDoc(`
name: f
version: 5
ask: { harness: $h }
parameters:
  model: { description: d }
  timeout: 5m
  h: { type: string, default: codex }
  gate: { type: bool, default: true }
  turns: { type: int, default: 3 }
  quiet: { type: duration, default: 1m }
  labels: { type: string, list: true, default: [a] }
  policy: { type: string, default: auto, enum: [auto, manual] }
  colour: { type: string, default: red, enum: [red, blue] }
  app: { type: string, default: web }
  ref: { type: string, default: $h }
  gateway: { type: bool, default: false }
sequences:
  - id: q
    stages:
      - { id: inner, type: command, run_ref: $app, timeout: $quiet }
main:
  - id: top
    type: agent
    agent: a
    model: $h
    harness: $h
    timeout: $quiet
    max_turns: $turns
    run_if: not $gate
    input: { url: { from: run.service.$app }, plain: { from: run.service.app } }
  - id: serve
    type: service
    service: $app
    run_if: $gate
  - id: act
    type: action
    action: x
    with: { title: $h, labels: $labels, literal: h }
watchers:
  - id: w
    on: { signal: x, start: $policy, quiet_for: $quiet, max: $turns }
    stages: [{ id: s, type: command, run: r }]
`);

describe('isParamRef', () => {
	it.each([
		['$name', true],
		['$name_2', true],
		['$a', true],
		['$NAME', false],
		['$FAILURE', false],
		['name', false],
		['$', false],
		['$2name', false],
		['$na-me', false],
		['${secrets.x}', false],
		[3, false],
		[undefined, false]
	])('reads %j', (v, out) => {
		expect(isParamRef(v)).toBe(out);
	});
});

describe('paramType', () => {
	it.each<[string, DocParam, ParamType]>([
		['harness', { type: 'int' }, 'string'],
		['model', {}, 'string'],
		['timeout', {}, 'duration'],
		['max_turns', {}, 'int'],
		['max_cost_usd', {}, 'float'],
		['max_total_turns', {}, 'int'],
		['mine', { type: 'duration' }, 'duration'],
		['mine', { default: true }, 'bool'],
		['mine', { default: 3 }, 'int'],
		['mine', { default: 0.5 }, 'float'],
		['mine', { default: 'x' }, 'string'],
		['mine', {}, 'string'],
		['mine', { default: [false] }, 'bool'],
		['mine', { default: [] }, 'string']
	])('reads %s %j as %s', (name, p, type) => {
		expect(paramType(name, p)).toBe(type);
	});
});

describe('paramKind', () => {
	it.each<[string, DocParam, ParamKind]>([
		['mine', { type: 'string', list: true }, 'list'],
		['mine', { type: 'bool', list: true }, 'list'],
		['mine', { type: 'bool' }, 'bool'],
		['mine', { type: 'int', enum: ['1'] }, 'int'],
		['mine', { type: 'string' }, 'string'],
		['mine', { type: 'string', enum: [] }, 'string'],
		['mine', { type: 'string', enum: ['auto', 'confirm'] }, 'policy'],
		['mine', { type: 'string', enum: ['auto', 'other'] }, 'enum'],
		['timeout', {}, 'duration']
	])('edits %s %j with %s', (name, p, kind) => {
		expect(paramKind(name, p)).toBe(kind);
	});
});

describe('parseValue', () => {
	it.each<[string, ParamType, boolean | number | string]>([
		['true', 'bool', true],
		[' false ', 'bool', false],
		['maybe', 'bool', 'maybe'],
		['3', 'int', 3],
		['-3', 'int', -3],
		[' 3 ', 'int', 3],
		['1.5', 'float', 1.5],
		['1.5', 'int', 1.5],
		['abc', 'int', 'abc'],
		['1e3', 'float', '1e3'],
		['$param', 'int', '$param'],
		['  keep spaces  ', 'string', '  keep spaces  '],
		['true', 'string', 'true'],
		['3', 'string', '3'],
		[' 5m ', 'duration', '5m']
	])('reads %j as a %s', (text, type, value) => {
		expect(parseValue(text, type)).toBe(value);
	});
});

describe('defaults', () => {
	it.each<[DocParam, string]>([
		[{}, ''],
		[{ default: 'x' }, 'x'],
		[{ default: 0 }, '0'],
		[{ default: false }, 'false'],
		[{ default: ['a', 'b'] }, 'a, b'],
		[{ default: [] }, '']
	])('shows %j', (p, text) => {
		expect(defaultText(p)).toBe(text);
	});

	it.each<[string, ParamType, boolean, DocParam['default']]>([
		['3', 'int', false, 3],
		['x', 'string', false, 'x'],
		['a, b ,, c', 'string', true, ['a', 'b', 'c']],
		['1, 2', 'int', true, [1, 2]],
		['true,false', 'bool', true, [true, false]],
		['', 'string', true, []]
	])('reads %j as %s (list: %s)', (text, type, list, value) => {
		expect(parseDefaultFor(text, type, list)).toEqual(value);
	});

	it('takes the list default off by default', () => {
		expect(parseDefaultFor('a, b', 'string')).toBe('a, b');
	});
});

describe('listing parameters', () => {
	it.each(['harness', 'model', 'timeout', 'max_turns', 'max_cost_usd', 'max_total_turns'])(
		'knows %s is the engine’s',
		(name) => {
			expect(isKnown(name)).toBe(true);
		}
	);

	it('knows the flow’s own', () => {
		expect(isKnown('mine')).toBe(false);
		expect(customParams(flow()).map(([n]) => n)).not.toContain('timeout');
		expect(customParams(flow()).map(([n]) => n)[0]).toBe('h');
		expect(customParams({ name: 'f' } as FlowDoc)).toEqual([]);
	});

	it('lists them by what they hold', () => {
		const doc = flow();
		expect(boolParams(doc)).toEqual(['gate', 'gateway']);
		expect(intParams(doc)).toEqual(['turns']);
		expect(durationParams(doc)).toEqual(['timeout', 'quiet']);
		expect(stringParams(doc)).toEqual(['model', 'h', 'policy', 'colour', 'app', 'ref']);
		expect(stringListParams(doc)).toEqual(['labels']);
	});

	it('finds the enums that fit a site', () => {
		expect(enumParamsWithin(flow(), ['red', 'blue', 'green'])).toEqual(['colour']);
		expect(enumParamsWithin(flow(), ['red'])).toEqual([]);
		expect(policyParams(flow())).toEqual(['policy']);
	});

	it('takes no list for an enum', () => {
		const doc = flow();
		doc.parameters.colour.list = true;
		expect(enumParamsWithin(doc, ['red', 'blue'])).toEqual([]);
	});
});

describe('paramRefs', () => {
	it.each<[string, string[]]>([
		['h', ['top.model', 'top.harness', 'act.title', 'ask.harness', 'ref.default']],
		['app', ['serve.service', 'top.url', 'inner.run_ref']],
		['quiet', ['top.timeout', 'inner.timeout', 'w.quiet_for']],
		['turns', ['top.max_turns', 'w.max']],
		['gate', ['top.run_if', 'serve.run_if']],
		['labels', ['act.labels']],
		['policy', ['w.start']],
		['colour', []]
	])('finds where %s is read', (name, refs) => {
		expect(paramRefs(flow(), name).sort()).toEqual([...refs].sort());
	});

	it('takes no prefix for a read: $gate is not $gateway', () => {
		expect(paramRefs(flow(), 'gat')).toEqual([]);
	});

	// `ask: { harness: $h }` is a parameter read (FLOWLANG §1), and the editor
	// asks before deleting a parameter this finds read.
	it('finds the Ask pane reading it', () => {
		expect(paramRefs(flow(), 'h')).toContain('ask.harness');
	});

	// `default: $h` reads h too (flow.Param.DefaultRef).
	it('finds another parameter’s default reading it', () => {
		expect(paramRefs(flow(), 'h')).toContain('ref.default');
	});
});

describe('renameParam', () => {
	const renamed = (from: string, to: string) => {
		const doc = flow();
		renameParam(doc, from, to);
		return doc;
	};

	it('keeps the parameters in document order', () => {
		const keys = Object.keys(renamed('turns', 'rounds').parameters);
		expect(keys.indexOf('rounds')).toBe(Object.keys(flow().parameters).indexOf('turns'));
		expect(keys).not.toContain('turns');
	});

	it('keeps what the parameter says', () => {
		expect(renamed('turns', 'rounds').parameters.rounds).toEqual({ type: 'int', default: 3 });
	});

	it('renames every reference: stage fields, settings, sources, phases', () => {
		const doc = renamed('quiet', 'calm');
		expect(doc.stages[0].timeout).toBe('$calm');
		expect(doc.sequences?.[0].stages[0].timeout).toBe('$calm');
		expect(doc.phases?.[1].on.quiet_for).toBe('$calm');

		const d2 = renamed('app', 'site');
		expect(d2.stages[1].service).toBe('$site');
		expect(d2.stages[0].input?.url.from).toBe('run.service.$site');
		expect(d2.sequences?.[0].stages[0].run_ref).toBe('$site');
		expect(d2.stages[0].input?.plain.from).toBe('run.service.app');

		expect(renamed('h', 'kind').stages[2].with?.title).toBe('$kind');
		expect(renamed('policy', 'how').phases?.[1].on.start).toBe('$how');
		expect(renamed('turns', 'rounds').phases?.[1].on.max).toBe('$rounds');
		expect(renamed('turns', 'rounds').stages[0].max_turns).toBe('$rounds');
		expect(renamed('labels', 'tags').stages[2].with?.labels).toBe('$tags');
	});

	it('renames a condition, negated or not', () => {
		const doc = renamed('gate', 'check');
		expect(doc.stages[0].run_if).toBe('not $check');
		expect(doc.stages[1].run_if).toBe('$check');
	});

	it('leaves a literal of the same word alone', () => {
		expect(renamed('h', 'kind').stages[2].with?.literal).toBe('h');
	});

	it('finds everything paramRefs found, under the new name', () => {
		const before = paramRefs(flow(), 'quiet').length;
		expect(paramRefs(renamed('quiet', 'calm'), 'calm')).toHaveLength(before);
		expect(paramRefs(renamed('quiet', 'calm'), 'quiet')).toEqual([]);
	});

	it('leaves a longer name that starts the same alone', () => {
		const doc = flow();
		doc.stages[1].run_if = '$gateway';
		renameParam(doc, 'gate', 'check');
		expect(doc.stages[1].run_if).toBe('$gateway');
	});

	it('leaves a negated longer name alone too', () => {
		const doc = flow();
		doc.stages[1].run_if = 'not $gateway';
		renameParam(doc, 'gate', 'check');
		expect(doc.stages[1].run_if).toBe('not $gateway');
	});

	it('renames the Ask pane’s harness', () => {
		expect(renamed('h', 'kind').ask?.harness).toBe('$kind');
	});

	it('renames another parameter’s default reading it', () => {
		expect(renamed('h', 'kind').parameters.ref.default).toBe('$kind');
	});
});
