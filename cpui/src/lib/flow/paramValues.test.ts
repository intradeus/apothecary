import { describe, expect, it } from 'vitest';
import type { ParamDecl } from '$lib/domain';
import { harnessKindOf, modelHarnessKinds, paramValue } from './paramValues';

const decl = (name: string, over: Partial<ParamDecl> = {}): ParamDecl => ({
	name,
	type: 'string',
	default: '',
	known: false,
	...over
});

describe('paramValue', () => {
	const all = [
		decl('a', { default: 'declared' }),
		decl('alias', { defaultRef: 'a' }),
		decl('deep', { defaultRef: 'alias' }),
		decl('bound', { required: true, default: 'never read' })
	];

	it.each<[string, string, (Record<string, string> | undefined)[], string | undefined]>([
		['a default', 'a', [], 'declared'],
		['the first layer that sets it', 'a', [{ a: 'one' }, { a: 'two' }], 'one'],
		['past a layer that does not', 'a', [{}, { a: 'two' }], 'two'],
		['past an absent layer', 'a', [undefined, { a: 'two' }], 'two'],
		['past a blank', 'a', [{ a: '' }, { a: 'two' }], 'two'],
		['the default past blanks alone', 'a', [{ a: '' }], 'declared'],
		['a parameter another’s default reads', 'alias', [], 'declared'],
		['what that parameter is bound to', 'alias', [{ a: 'bound' }], 'bound'],
		['a chain of them', 'deep', [{ a: 'bound' }], 'bound'],
		['its own value over what it reads', 'alias', [{ alias: 'mine', a: 'bound' }], 'mine'],
		['nothing for a required one', 'bound', [], undefined],
		['a required one’s binding', 'bound', [{ bound: 'x' }], 'x'],
		['nothing for an unknown one', 'nope', [], undefined]
	])('finds %s', (_, name, layers, value) => {
		expect(paramValue(name, all, layers)).toBe(value);
	});

	it('gives up on a loop instead of hanging', () => {
		const loop = [decl('a', { defaultRef: 'b' }), decl('b', { defaultRef: 'a' })];
		expect(paramValue('a', loop, [])).toBeUndefined();
	});
});

describe('harnessKindOf', () => {
	const harness = decl('h', { default: 'opencode' });

	it.each<[string, string | undefined, (Record<string, string> | undefined)[], string]>([
		['no harness', undefined, [], ''],
		['a harness written at the stages', 'codex', [], 'codex'],
		['the parameter’s default', '$h', [], 'opencode'],
		['the parameter as the form stands', '$h', [{ h: 'omp' }], 'omp'],
		['an unknown parameter', '$nope', [], '']
	])('reads %s', (_, h, layers, kind) => {
		expect(harnessKindOf(decl('m', { harness: h }), [harness], layers)).toBe(kind);
	});
});

describe('modelHarnessKinds', () => {
	it('gives one kind per model parameter, distinct, in declaration order', () => {
		const all = [
			decl('h', { role: 'harness', default: 'codex' }),
			decl('m1', { role: 'model', harness: 'omp' }),
			decl('m2', { role: 'model', harness: '$h' }),
			decl('m3', { role: 'model', harness: 'omp' }),
			decl('m4', { role: 'model' }),
			decl('other', { harness: 'ignored' })
		];
		expect(modelHarnessKinds(all, [])).toEqual(['omp', 'codex']);
		expect(modelHarnessKinds(all, [{ h: 'opencode' }])).toEqual(['omp', 'opencode']);
	});
});
