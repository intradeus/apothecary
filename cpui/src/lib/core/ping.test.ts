import { describe, expect, it } from 'vitest';
import { I } from '$lib/design/icons';
import type { PingField, PingForm } from '$lib/domain';
import {
	answerKindOf,
	answerMeta,
	answerSummary,
	curatable,
	fieldAnswerText,
	fieldLabel,
	formContent,
	formDefaults,
	formMissing,
	formShape,
	isAgentPing,
	isDebug,
	isDocumentPing,
	isPhaseConfirm,
	isWantedPing,
	pingCta,
	pingHeadline,
	primaryFile,
	toCuratedMap,
	toCuration,
	toFormContent,
	toPingForm,
	toPingOption,
	toReviewComment
} from './ping';

const aField = (over: Partial<PingField> = {}): PingField => ({
	id: 'f',
	type: 'string',
	required: false,
	options: [],
	...over
});
const form = (...fields: Partial<PingField>[]): PingForm => ({ fields: fields.map(aField) });
const options = [
	{ id: 'a', label: 'Option A' },
	{ id: 'b', label: 'Option B' }
];

describe('what a ping is', () => {
	it.each<[string, boolean, boolean, boolean, boolean]>([
		['gate', false, false, false, true],
		['debug', true, false, false, true],
		['phase_confirm', false, true, false, true],
		['select', false, false, false, true],
		['clarification', false, false, true, false],
		['blocker', false, false, true, false],
		['permission', false, false, true, false]
	])('classifies %s', (type, debug, confirm, agent, wanted) => {
		expect(isDebug(type)).toBe(debug);
		expect(isPhaseConfirm(type)).toBe(confirm);
		expect(isAgentPing(type)).toBe(agent);
		expect(isWantedPing(type)).toBe(wanted);
	});

	it.each<[unknown, string, string]>([
		['yes_no', 'clarification', 'yes_no'],
		['form', 'gate', 'form'],
		[undefined, 'gate', 'yes_no'],
		[undefined, 'phase_confirm', 'yes_no'],
		[undefined, 'debug', 'yes_no'],
		[undefined, 'select', 'form'],
		[undefined, 'clarification', 'form'],
		['nonsense', 'permission', 'form']
	])('reads answer kind %j on a %s as %s', (kind, type, out) => {
		expect(answerKindOf(kind, type)).toBe(out);
	});
});

describe('reading the wire', () => {
	it.each<[unknown, object]>([
		['older', { id: 'older', label: 'older' }],
		[
			{ id: 'a', label: 'A', detail: 'more' },
			{ id: 'a', label: 'A', detail: 'more' }
		],
		[{ id: 'a' }, { id: 'a', label: 'a', detail: undefined }],
		[
			{ id: 'a', label: '', detail: '' },
			{ id: 'a', label: 'a', detail: undefined }
		],
		[null, { id: '', label: '', detail: undefined }]
	])('reads the option %j', (wire, out) => {
		expect(toPingOption(wire)).toEqual(out);
	});

	it('reads a review comment, absent lines staying absent', () => {
		expect(
			toReviewComment({
				path: 'a.go',
				start_line: 3,
				end_line: 5,
				heading: 'H',
				quote: 'q',
				body: 'b',
				attachment_ids: ['x']
			})
		).toEqual({
			path: 'a.go',
			startLine: 3,
			endLine: 5,
			heading: 'H',
			quote: 'q',
			body: 'b',
			attachmentIds: ['x']
		});
	});

	it.each([[null], [undefined], [{}], [{ start_line: 0, end_line: 0, attachment_ids: null }]])(
		'reads the review comment %j as an empty whole-file one',
		(wire) => {
			expect(toReviewComment(wire)).toEqual({
				path: '',
				startLine: undefined,
				endLine: undefined,
				heading: undefined,
				quote: undefined,
				body: '',
				attachmentIds: []
			});
		}
	);

	it.each([[null], [undefined], [{}], [{ fields: null }], [{ fields: 'x' }]])(
		'reads no form from %j',
		(wire) => {
			expect(toPingForm(wire)).toBeUndefined();
		}
	);

	it('reads every field of a form', () => {
		const out = toPingForm({
			fields: [
				{
					id: 'n',
					type: 'integer',
					title: 'How many',
					description: 'd',
					required: true,
					options: ['x', { id: 'y', label: 'Y' }],
					format: 'date',
					pattern: '^a',
					min_length: 1,
					max_length: 2,
					minimum: 0,
					maximum: 9,
					min_items: 1,
					max_items: 3,
					default: 4
				}
			]
		});
		expect(out?.fields[0]).toEqual({
			id: 'n',
			type: 'integer',
			title: 'How many',
			description: 'd',
			required: true,
			options: [
				{ id: 'x', label: 'x' },
				{ id: 'y', label: 'Y', detail: undefined }
			],
			format: 'date',
			pattern: '^a',
			minLength: 1,
			maxLength: 2,
			minimum: 0,
			maximum: 9,
			minItems: 1,
			maxItems: 3,
			default: 4
		});
	});

	it('reads a sparse or odd field safely', () => {
		const [odd, none] = toPingForm({
			fields: [{ id: 3, type: 'mystery', required: 'yes', min_length: '1', minimum: 0 }, null]
		})!.fields;
		expect(odd).toMatchObject({
			id: '3',
			type: 'string',
			required: false,
			options: [],
			minimum: 0
		});
		expect(odd.minLength).toBeUndefined();
		expect(none).toMatchObject({ id: '', type: 'string' });
	});

	it('keeps a false or zero default', () => {
		const out = toPingForm({ fields: [{ default: false }, { default: 0 }, { default: null }] })!;
		expect(out.fields.map((f) => f.default)).toEqual([false, 0, undefined]);
	});

	it.each<[unknown, unknown]>([
		[{ a: 1 }, { a: 1 }],
		[{}, {}],
		[null, undefined],
		['x', undefined],
		[['a'], undefined]
	])('reads the content %j', (wire, out) => {
		expect(toFormContent(wire)).toEqual(out);
	});

	it('reads a curation, an unsaid keep meaning kept', () => {
		expect(
			toCuration({
				summary: 'S',
				items: [
					{ item: 'notes/0', keep: false, body: 'b', resolve: true },
					{ item: 'notes/1' },
					{ item: 'notes/2', keep: true, body: '', resolve: 'no' },
					null
				]
			})
		).toEqual({
			summary: 'S',
			items: [
				{ item: 'notes/0', keep: false, body: 'b', resolve: true },
				{ item: 'notes/1', keep: true, body: undefined, resolve: undefined },
				{ item: 'notes/2', keep: true, body: undefined, resolve: undefined },
				{ item: '', keep: true, body: undefined, resolve: undefined }
			]
		});
	});

	it.each([[null], [undefined], [{ summary: 3 }], [{ summary: null, items: null }]])(
		'reads %j as a curation of nothing',
		(wire) => {
			expect(toCuration(wire)).toEqual({ summary: undefined, items: [] });
		}
	);

	it('keeps an emptied summary: the person cleared it', () => {
		expect(toCuration({ summary: '' }).summary).toBe('');
	});

	it('reads curations by path', () => {
		expect(toCuratedMap({ 'a.yaml': { items: [] } })).toEqual({
			'a.yaml': { summary: undefined, items: [] }
		});
	});

	it.each([[null], [undefined], ['x'], [[]], [{}]])('reads no curations from %j', (wire) => {
		expect(toCuratedMap(wire)).toBeUndefined();
	});
});

describe('curatable', () => {
	it.each<[string, string[], Record<string, string> | undefined, unknown]>([
		[
			'a review',
			['r.yaml'],
			{ 'r.yaml': 'review_threads' },
			{ path: 'r.yaml', type: 'review_threads' }
		],
		[
			'replies',
			['r.yaml'],
			{ 'r.yaml': 'review_replies' },
			{ path: 'r.yaml', type: 'review_replies' }
		],
		['threads alone', ['t.yaml'], { 't.yaml': 'mr_threads' }, undefined],
		['a plain file', ['a.md'], {}, undefined],
		['no types', ['a.md'], undefined, undefined],
		[
			'the first curatable file',
			['a.md', 't.yaml', 'r.yaml', 's.yaml'],
			{ 't.yaml': 'mr_threads', 'r.yaml': 'review_threads', 's.yaml': 'review_replies' },
			{ path: 'r.yaml', type: 'review_threads' }
		]
	])('finds %s', (_, files, fileTypes, out) => {
		expect(curatable({ files, fileTypes })).toEqual(out);
	});

	it('finds nothing with no files', () => {
		expect(curatable({})).toBeUndefined();
	});
});

describe('forms', () => {
	it.each<[string, PingForm | undefined, string]>([
		['no form', undefined, 'fields'],
		['no fields', form(), 'fields'],
		['one text field', form({}), 'text'],
		['one field of options', form({ options }), 'pick'],
		['a formatted field', form({ format: 'email' }), 'fields'],
		['a patterned field', form({ pattern: '^a' }), 'fields'],
		['a number', form({ type: 'number' }), 'fields'],
		['a boolean', form({ type: 'boolean' }), 'fields'],
		['an array of options', form({ type: 'array', options }), 'fields'],
		['two fields', form({ id: 'a' }, { id: 'b' }), 'fields']
	])('asks %s', (_, f, shape) => {
		expect(formShape(f)).toBe(shape);
	});

	it('starts each field on its default, an array on none', () => {
		const f = form(
			{ id: 'text', default: 'x' },
			{ id: 'none' },
			{ id: 'flag', type: 'boolean', default: false },
			{ id: 'n', type: 'number', default: 0 },
			{ id: 'picked', type: 'array', default: ['a'] },
			{ id: 'empty', type: 'array' }
		);
		expect(formDefaults(f)).toEqual({
			text: 'x',
			none: undefined,
			flag: false,
			n: 0,
			picked: ['a'],
			empty: []
		});
	});

	it('copies an array default, so ticking one leaves the form alone', () => {
		const def = ['a'];
		const out = formDefaults(form({ id: 'p', type: 'array', default: def }));
		expect(out.p).not.toBe(def);
	});

	it('has no values without a form', () => {
		expect(formDefaults(undefined)).toEqual({});
		expect(formContent(undefined, { a: 1 })).toEqual({});
		expect(formMissing(undefined, {})).toEqual([]);
	});

	it('sends what was answered, trimmed, the blanks dropped', () => {
		const f = form(
			{ id: 'text' },
			{ id: 'blank' },
			{ id: 'spaces' },
			{ id: 'nan', type: 'number' },
			{ id: 'zero', type: 'number' },
			{ id: 'no', type: 'boolean' },
			{ id: 'none', type: 'array' },
			{ id: 'unset' }
		);
		expect(
			formContent(f, {
				text: '  hi  ',
				blank: '',
				spaces: '   ',
				nan: NaN,
				zero: 0,
				no: false,
				none: [],
				other: 'not a field'
			})
		).toEqual({ text: 'hi', zero: 0, no: false, none: [] });
	});

	it('finds the required fields still unanswered', () => {
		const f = form(
			{ id: 'a', required: true },
			{ id: 'b', required: true },
			{ id: 'c', required: true },
			{ id: 'd', required: false },
			{ id: 'e', required: true, type: 'array' },
			{ id: 'g', required: true, type: 'boolean' }
		);
		expect(formMissing(f, { a: 'x', b: '  ', e: [], g: false }).map((x) => x.id)).toEqual([
			'b',
			'c'
		]);
	});

	it('labels a field by its title, else its id', () => {
		expect(fieldLabel(aField({ title: 'T' }))).toBe('T');
		expect(fieldLabel(aField({ title: '' }))).toBe('f');
	});

	it.each<[string, Partial<PingField>, Parameters<typeof fieldAnswerText>[1], string]>([
		['nothing', {}, undefined, '—'],
		['text', {}, 'hello', 'hello'],
		['an option', { options }, 'a', 'Option A'],
		['an unknown option', { options }, 'z', 'z'],
		['options', { options }, ['a', 'b', 'z'], 'Option A, Option B, z'],
		['no options', { options }, [], 'none selected'],
		['yes', {}, true, 'Yes'],
		['no', {}, false, 'No'],
		['a number', {}, 0, '0']
	])('reads an answer of %s', (_, field, value, out) => {
		expect(fieldAnswerText(aField(field), value)).toBe(out);
	});
});

describe('answerSummary', () => {
	it('gives a yes/no its answer', () => {
		expect(answerSummary({ answerKind: 'yes_no', answer: 'approve' })).toBe('approve');
		expect(answerSummary({ answerKind: 'yes_no' })).toBe('');
	});

	it('says a form was declined', () => {
		expect(answerSummary({ answerKind: 'form', answer: 'decline', content: {} })).toBe('Declined');
	});

	it('reads a form of one field as its answer', () => {
		const f = form({ id: 'a', options });
		expect(
			answerSummary({ answerKind: 'form', answer: 'accept', form: f, content: { a: 'b' } })
		).toBe('Option B');
	});

	it('reads several fields as label: answer, leaving out the unanswered', () => {
		const f = form(
			{ id: 'a', title: 'First' },
			{ id: 'b', title: 'Second' },
			{ id: 'c', type: 'boolean' }
		);
		expect(
			answerSummary({ answerKind: 'form', answer: 'accept', form: f, content: { a: 'x', c: true } })
		).toBe('First: x · c: Yes');
	});
});

describe('documents and calls to action', () => {
	it.each<[string, object, boolean, string]>([
		['files', { files: ['a.md'] }, true, 'a.md'],
		['files and a link', { files: ['a.md'], link: 'u', primaryRef: 'u' }, true, 'a.md'],
		['a primary ref', { primaryRef: 'a.md' }, true, 'a.md'],
		['a link', { link: 'u', primaryRef: 'u' }, false, ''],
		['nothing', {}, false, '']
	])('reads a ping with %s', (_, p, doc, file) => {
		expect(isDocumentPing(p)).toBe(doc);
		expect(primaryFile(p)).toBe(file);
	});

	it.each<[string, Parameters<typeof pingCta>[0], string]>([
		[
			'curation first',
			{
				type: 'gate',
				answerKind: 'yes_no',
				files: ['x.md', 'dir/r.yaml'],
				fileTypes: { 'dir/r.yaml': 'review_threads' }
			},
			'Curate r.yaml'
		],
		['several files', { type: 'gate', answerKind: 'yes_no', files: ['a', 'b'] }, 'Review 2 files'],
		[
			'one file, by name',
			{ type: 'gate', answerKind: 'yes_no', files: ['dir/a.md'] },
			'Review a.md'
		],
		['a primary ref', { type: 'gate', answerKind: 'yes_no', primaryRef: 'a.md' }, 'Review a.md'],
		['a yes/no', { type: 'gate', answerKind: 'yes_no' }, 'Decide'],
		['a link', { type: 'gate', answerKind: 'yes_no', link: 'u', primaryRef: 'u' }, 'Decide'],
		['a permission', { type: 'permission', answerKind: 'form' }, 'Decide'],
		['a question', { type: 'clarification', answerKind: 'form' }, 'Answer'],
		['a path ending in a slash', { type: 'gate', answerKind: 'yes_no', files: ['dir/'] }, 'Review']
	])('offers %s', (_, p, cta) => {
		expect(pingCta(p)).toBe(cta);
	});
});

describe('pingHeadline', () => {
	it.each([
		['Plain question?', 'Plain question?'],
		['  padded  ', 'padded'],
		['\n\n  second line\nthird', 'second line'],
		['# A heading', 'A heading'],
		['### Deeper', 'Deeper'],
		['- a bullet', 'a bullet'],
		['* a star', 'a star'],
		['> a quote', 'a quote'],
		['**bold** and *soft*', 'bold and soft'],
		['Run `make test` now', 'Run make test now'],
		['See [the plan](https://a.test/plan) please', 'See the plan please'],
		['```\ncode fence\n```\nthen text', 'code fence'],
		['```go', ''],
		['', '']
	])('reads %j as %j', (question, line) => {
		expect(pingHeadline(question)).toBe(line);
	});

	// An identifier in a question must not be misquoted in the inbox row.
	it.each([
		['Raise `max_turns` to 80?', 'Raise max_turns to 80?'],
		['Review docs/flow_language.md', 'Review docs/flow_language.md']
	])('keeps the underscore inside a word: %j', (question, line) => {
		expect(pingHeadline(question)).toBe(line);
	});

	it.each([
		['Call `__init__` first', 'Call __init__ first'],
		['Deref `*ptr` here', 'Deref *ptr here']
	])('keeps a code span as written: %j', (question, line) => {
		expect(pingHeadline(question)).toBe(line);
	});

	it.each([
		['_soft_ and __bold__', 'soft and bold'],
		['an unclosed ` tick', 'an unclosed  tick']
	])('still takes underscore emphasis and stray marks off: %j', (question, line) => {
		expect(pingHeadline(question)).toBe(line);
	});
});

describe('answerMeta', () => {
	it.each<[object, string, string, string]>([
		[{ status: 'withdrawn', answer: 'approve' }, I.none, 'Withdrawn', 'muted'],
		[{ answer: 'approve' }, I.ok, 'Approved', 'success'],
		[{ answer: 'reject' }, I.loop, 'Changes requested', 'warning'],
		[{ answer: 'decline' }, I.none, 'Declined', 'warning'],
		[{ answer: 'accept' }, I.ok, 'Answered', 'success'],
		[{}, I.ok, 'Answered', 'success']
	])('reads %j', (p, glyph, word, hue) => {
		expect(answerMeta(p)).toEqual({ glyph, word, hue });
	});
});
