import { CompletionContext } from '@codemirror/autocomplete';
import { EditorState } from '@codemirror/state';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { parse } from 'yaml';
import { yamlValues } from './yamlValues';

// The package's build does not load under Node: its three calls stand in here,
// each answering what the test says the document is. What is under test is
// finding the key, and resolving its schema.
const editor = vi.hoisted(() => ({
	schema: undefined as object | undefined,
	pointer: '',
	unreadable: false
}));
vi.mock('codemirror-json-schema', () => ({
	getJSONSchema: () => editor.schema,
	jsonPointerForPosition: () => editor.pointer
}));
vi.mock('codemirror-json-schema/yaml', () => ({
	parseYAMLDocumentState: (state: EditorState) => {
		if (editor.unreadable) throw new Error('half typed');
		return { data: parse(state.doc.toString()) };
	}
}));

const SCHEMA = {
	type: 'object',
	properties: {
		kind: { enum: ['one', 'two', 3] },
		pin: { const: 'fixed' },
		named: { examples: ['$harness', 'codex', 9] },
		loose: { type: 'string' },
		ref: { $ref: '#/$defs/color' },
		either: { anyOf: [{ enum: ['a'] }, { $ref: '#/$defs/color' }] },
		one: { oneOf: [{ enum: ['b'] }, { enum: ['c'] }] },
		all: { allOf: [{ enum: ['d'] }] },
		stages: { type: 'array', items: { $ref: '#/$defs/stage' } },
		byName: { type: 'object', additionalProperties: { enum: ['free'] } }
	},
	$defs: {
		color: { enum: ['red', 'blue'] },
		stage: {
			type: 'object',
			properties: { type: { enum: ['agent', 'action'] } },
			allOf: [
				{
					if: { required: ['type'], properties: { type: { const: 'action' } } },
					then: { properties: { action: { enum: ['push', 'merge'] } } }
				},
				{
					if: { required: ['type'], properties: { type: { const: 'agent' } } },
					then: { properties: { action: { enum: ['never'] } } }
				},
				{
					if: { required: ['run'] },
					then: { properties: { shell: { enum: ['bash'] } } }
				}
			]
		}
	}
};

interface At {
	explicit?: boolean;
	/** The JSON pointer of where the key stands; its name alone by default. */
	pointer?: string;
	schema?: object;
}

/** Completion at the `|` in `text`. */
function complete(text: string, { explicit = false, pointer, schema = SCHEMA }: At = {}) {
	const pos = text.indexOf('|');
	const doc = text.replace('|', '');
	editor.schema = schema;
	editor.pointer = pointer ?? `/${/([\w-]+):[^:]*$/.exec(text.slice(0, pos))?.[1] ?? ''}`;
	return yamlValues(new CompletionContext(EditorState.create({ doc }), pos, explicit));
}
const labels = (text: string, at?: At) => complete(text, at)?.options.map((o) => o.label);

beforeEach(() => {
	editor.unreadable = false;
});

describe('yamlValues', () => {
	it('offers nothing with no schema', () => {
		editor.schema = undefined;
		const state = EditorState.create({ doc: 'kind: ' });
		expect(yamlValues(new CompletionContext(state, 6, true))).toBeNull();
	});

	it('offers a key’s enum, numbers as text', () => {
		expect(labels('kind: |')).toEqual(['one', 'two', '3']);
	});

	it('marks them as enum values, replacing what is typed', () => {
		const out = complete('kind: tw|');
		expect(out?.from).toBe('kind: '.length);
		expect(out?.options[0].type).toBe('enum');
		expect('$a.b-c').toMatch(out?.validFor as RegExp);
	});

	it('offers every value, whatever the prefix', () => {
		expect(labels('kind: zz|')).toEqual(['one', 'two', '3']);
	});

	it('offers a const', () => {
		expect(labels('pin: |')).toEqual(['fixed']);
	});

	it('offers the string examples', () => {
		expect(labels('named: |')).toEqual(['$harness', 'codex']);
	});

	it('offers nothing for a key with no values', () => {
		expect(complete('loose: |')).toBeNull();
	});

	it('offers nothing for an unknown key', () => {
		expect(complete('nope: |')).toBeNull();
	});

	it('offers nothing away from a value', () => {
		expect(complete('kin|')).toBeNull();
	});

	it('waits for the space after the colon unless asked', () => {
		expect(complete('kind:|')).toBeNull();
		expect(labels('kind:|', { explicit: true })).toEqual(['one', 'two', '3']);
	});

	it.each<[string, string, string[]]>([
		['a $ref', 'ref: |', ['red', 'blue']],
		['anyOf', 'either: |', ['a', 'red', 'blue']],
		['oneOf', 'one: |', ['b', 'c']],
		['allOf', 'all: |', ['d']]
	])('resolves %s', (_, text, values) => {
		expect(labels(text)).toEqual(values);
	});

	it('resolves additionalProperties', () => {
		expect(labels('byName:\n  anything: |', { pointer: '/byName/anything' })).toEqual(['free']);
	});

	it('reaches into a list', () => {
		expect(labels('stages:\n  - type: |', { pointer: '/stages/0/type' })).toEqual([
			'agent',
			'action'
		]);
	});

	it('reads a flow mapping', () => {
		expect(labels('stages:\n  - { type: |', { pointer: '/stages/0/type' })).toEqual([
			'agent',
			'action'
		]);
	});

	it('names the key itself when the pointer stops at the mapping around it', () => {
		expect(labels('stages:\n  - type: |', { pointer: '/stages/0' })).toEqual(['agent', 'action']);
	});

	it.each([
		['action', ['push', 'merge']],
		['agent', ['never']]
	])('takes the branch of an if the value holds: %s', (type, values) => {
		const text = `stages:\n  - type: ${type}\n    action: |`;
		expect(labels(text, { pointer: '/stages/0/action' })).toEqual(values);
	});

	it('takes no branch before the value says which', () => {
		expect(complete('stages:\n  - action: |', { pointer: '/stages/0/action' })).toBeNull();
	});

	it('takes a branch that asks only for a key to be there', () => {
		const at = { pointer: '/stages/0/shell' };
		expect(labels('stages:\n  - run: x\n    shell: |', at)).toEqual(['bash']);
		expect(complete('stages:\n  - type: agent\n    shell: |', at)).toBeNull();
	});

	it('reads no branch from a document that does not parse yet', () => {
		editor.unreadable = true;
		const text = 'stages:\n  - type: action\n    action: |';
		expect(complete(text, { pointer: '/stages/0/action' })).toBeNull();
	});

	it('does not chase a schema that refers to itself for ever', () => {
		const loop = {
			type: 'object',
			properties: { a: { $ref: '#/$defs/x' } },
			$defs: { x: { $ref: '#/$defs/x' } }
		};
		expect(complete('a: |', { schema: loop })).toBeNull();
	});
});
