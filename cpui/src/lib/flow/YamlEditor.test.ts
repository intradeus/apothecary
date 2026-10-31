import { render } from '@testing-library/svelte';
import { tick } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Text } from '@codemirror/state';
import { yamlCompletion } from 'codemirror-json-schema/yaml';
import { updateSchema } from 'codemirror-json-schema';
import YamlEditor from './YamlEditor.svelte';

// CodeMirror measures a layout jsdom does not have. Only @codemirror/state is
// real, so the text and the transactions are CodeMirror's own; everything that
// needs a page stands in, keeping what this file gave it.
const cm = vi.hoisted(() => ({
	view: null as null | {
		state: { doc: Text };
		parent: HTMLElement;
		destroyed: boolean;
		dispatch: (spec: unknown) => void;
	},
	onUpdate: null as null | ((u: { docChanged: boolean; state: unknown }) => void),
	attributes: {} as Record<string, string>,
	lint: null as null | ((view: unknown) => Promise<unknown[]>),
	lintOptions: {} as Record<string, unknown>,
	sources: [] as unknown[],
	decorate: null as null | ((m: string[]) => { class: string }),
	schemaLint: vi.fn((_: unknown): { from: number; to: number; message: string }[] => [])
}));

vi.mock('codemirror', () => ({ basicSetup: [] }));
vi.mock('@codemirror/view', () => {
	class EditorView {
		static theme = () => [];
		static contentAttributes = {
			of: (a: Record<string, string>) => ((cm.attributes = a), [])
		};
		static updateListener = {
			of: (fn: typeof cm.onUpdate) => ((cm.onUpdate = fn), [])
		};
		state: { doc: Text; update: (s: unknown) => { state: EditorView['state'] } };
		parent: HTMLElement;
		destroyed = false;
		constructor(c: { parent: HTMLElement; state: EditorView['state'] }) {
			this.state = c.state;
			this.parent = c.parent;
			c.parent.append(Object.assign(document.createElement('div'), { role: 'textbox' }));
			cm.view = this;
		}
		dispatch(spec: unknown) {
			this.state = this.state.update(spec).state;
			cm.onUpdate?.({ docChanged: true, state: this.state });
		}
		destroy() {
			this.destroyed = true;
		}
	}
	class MatchDecorator {
		constructor(c: { decoration: typeof cm.decorate }) {
			cm.decorate = c.decoration;
		}
	}
	return {
		EditorView,
		MatchDecorator,
		Decoration: { mark: (spec: { class: string }) => spec },
		ViewPlugin: { fromClass: () => [] },
		hoverTooltip: () => []
	};
});
vi.mock('@codemirror/language', () => ({
	HighlightStyle: { define: () => [] },
	syntaxHighlighting: () => [],
	indentUnit: { of: () => [] }
}));
vi.mock('@codemirror/lint', () => ({
	linter: (fn: typeof cm.lint, options: Record<string, unknown>) => {
		cm.lint = fn;
		cm.lintOptions = options;
		return [];
	},
	lintGutter: () => []
}));
vi.mock('@codemirror/lang-yaml', () => ({
	yaml: () => [],
	yamlLanguage: {
		data: { of: (o: { autocomplete: unknown }) => (cm.sources.push(o.autocomplete), []) }
	}
}));
vi.mock('codemirror-json-schema/yaml', () => ({
	yamlCompletion: vi.fn(() => () => null),
	yamlSchemaHover: () => () => null,
	yamlSchemaLinter: () => cm.schemaLint
}));
vi.mock('codemirror-json-schema', () => ({
	handleRefresh: () => true,
	stateExtensions: () => [],
	updateSchema: vi.fn()
}));
vi.mock('./yamlValues', () => ({ yamlValues: () => null }));

const TEXT = 'name: a\nstages:\n  - id: b\n';

/** Renders it with `value` bound: what the editor writes lands in `bound`. */
function setup(over: Record<string, unknown> = {}) {
	const bound = { value: TEXT };
	const view = render(YamlEditor, {
		props: {
			get value() {
				return bound.value;
			},
			set value(v: string) {
				bound.value = v;
			},
			label: 'Flow YAML',
			...over
		}
	});
	return { bound, ...view };
}

const doc = () => cm.view!.state.doc.toString();
const lint = () => cm.lint!(cm.view) as Promise<{ from: number; to: number; message: string }[]>;
const lineOf = (n: number) => cm.view!.state.doc.line(n);

describe('YamlEditor', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		cm.schemaLint.mockReturnValue([]);
		cm.sources = [];
	});

	it('opens on the text it is given, in the page', () => {
		const { container } = setup();
		expect(doc()).toBe(TEXT);
		expect(cm.view!.parent).toBe(container.querySelector('.ye'));
	});

	it('names the editing area for assistive tech', () => {
		setup();
		expect(cm.attributes).toEqual({ 'aria-label': 'Flow YAML', spellcheck: 'false' });
	});

	it('writes what is typed to the value', () => {
		const { bound } = setup();
		cm.view!.dispatch({ changes: { from: 0, insert: '# hi\n' } });
		expect(bound.value).toBe('# hi\n' + TEXT);
	});

	it('leaves the value alone on a change that is not the document’s', () => {
		const { bound } = setup();
		cm.onUpdate!({ docChanged: false, state: { doc: 'ignored' } });
		expect(bound.value).toBe(TEXT);
	});

	it('takes a value set from outside', async () => {
		const { rerender } = setup();
		await rerender({ value: 'name: loaded\n' });
		await tick();
		expect(doc()).toBe('name: loaded\n');
	});

	it('does not dispatch a value it already shows', async () => {
		const { rerender } = setup();
		const dispatch = vi.spyOn(cm.view!, 'dispatch');
		await rerender({ value: TEXT });
		await tick();
		expect(dispatch).not.toHaveBeenCalled();
	});

	it('closes the view with the component', () => {
		const { unmount } = setup();
		const view = cm.view!;
		unmount();
		expect(view.destroyed).toBe(true);
	});

	describe('its schema', () => {
		const schema = { type: 'object' };

		it('starts checking when the schema arrives', async () => {
			const { rerender } = setup();
			expect(updateSchema).not.toHaveBeenCalled();
			await rerender({ schema });
			await tick();
			expect(updateSchema).toHaveBeenCalledExactlyOnceWith(cm.view, schema);
		});

		it('checks from the start when it has one', () => {
			setup({ schema });
			expect(updateSchema).toHaveBeenCalledExactlyOnceWith(cm.view, schema);
		});
	});

	describe('completion', () => {
		const schemaSource = () => cm.sources[0] as (ctx: unknown) => unknown;

		// The library answers [] with no schema, which CodeMirror cannot read.
		it('answers null when the schema offers nothing', () => {
			const inner = vi.fn(() => []);
			vi.mocked(yamlCompletion).mockReturnValueOnce(inner as never);
			setup();
			expect(schemaSource()({})).toBeNull();
		});

		it('passes on what the schema offers', () => {
			const result = { from: 0, options: [] };
			vi.mocked(yamlCompletion).mockReturnValueOnce((() => result) as never);
			setup();
			expect(schemaSource()({})).toBe(result);
		});

		it('offers the values too', () => {
			setup();
			expect(cm.sources).toHaveLength(2);
		});
	});

	describe('its checks', () => {
		const bySchema = (from: number, message = 'unknown key') => ({ from, to: from + 1, message });

		it('gives the schema’s alone with no parser', async () => {
			const found = [bySchema(0)];
			cm.schemaLint.mockReturnValue(found);
			setup();
			expect(await lint()).toEqual(found);
		});

		it('hands the parser the text', async () => {
			const check = vi.fn(async () => []);
			setup({ check });
			await lint();
			expect(check).toHaveBeenCalledExactlyOnceWith(TEXT);
		});

		it('puts a parser’s error on the line it names, its location off the message', async () => {
			setup({ check: async () => ['flow:2: stages must not be empty'] });
			expect(await lint()).toEqual([
				{
					from: lineOf(2).from,
					to: lineOf(2).to,
					severity: 'error',
					source: 'apothecary',
					message: 'stages must not be empty'
				}
			]);
		});

		it('reads the “line N:” a YAML error gives', async () => {
			setup({ check: async () => ['yaml: line 3: mapping values are not allowed'] });
			const [d] = await lint();
			expect(d.from).toBe(lineOf(3).from);
			expect(d.message).toBe('mapping values are not allowed');
		});

		it('puts an error with no line on the first, whole', async () => {
			setup({ check: async () => ['no main chain'] });
			const [d] = await lint();
			expect(d.from).toBe(0);
			expect(d.message).toBe('no main chain');
		});

		it('keeps a line past the end inside the text', async () => {
			setup({ check: async () => ['flow:99: too far'] });
			const [d] = await lint();
			expect(d.from).toBe(cm.view!.state.doc.line(cm.view!.state.doc.lines).from);
		});

		it('says a typo once: nothing on a line the schema marks', async () => {
			setup({ check: async () => ['flow:2: unknown key stages'] });
			cm.schemaLint.mockReturnValue([bySchema(lineOf(2).from)]);
			const found = await lint();
			expect(found).toHaveLength(1);
			expect(found[0].message).toBe('unknown key');
		});

		it('adds the parser’s on other lines', async () => {
			setup({ check: async () => ['flow:3: no such agent'] });
			cm.schemaLint.mockReturnValue([bySchema(lineOf(1).from)]);
			expect(await lint()).toHaveLength(2);
		});

		it('falls back to the schema’s when the parser cannot be reached', async () => {
			const found = [bySchema(0)];
			setup({ check: async () => Promise.reject(new Error('offline')) });
			cm.schemaLint.mockReturnValue(found);
			expect(await lint()).toEqual(found);
		});

		// An answer for text that has changed would land on the wrong lines.
		it('drops the parser’s answer when the text moved on meanwhile', async () => {
			let answer!: (e: string[]) => void;
			setup({ check: () => new Promise<string[]>((r) => (answer = r)) });
			const pending = lint();
			cm.view!.dispatch({ changes: { from: 0, insert: '# edited\n' } });
			answer(['flow:1: stale']);
			expect(await pending).toEqual([]);
		});

		it('waits for a pause in the typing', () => {
			setup();
			expect(cm.lintOptions.delay).toBe(600);
		});
	});

	describe('its $references', () => {
		it.each([
			['$model', 'cm-param'],
			['${model}', 'cm-param'],
			['$WORKSPACE', 'cm-sentinel']
		])('marks %s as %s', (ref, cls) => {
			setup();
			expect(cm.decorate!([ref])).toEqual({ class: cls });
		});
	});
});
