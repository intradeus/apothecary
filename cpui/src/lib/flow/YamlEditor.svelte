<script lang="ts">
	// The flow editor's YAML tab (CodeMirror 6). The document is highlighted in
	// the theme's hues — keys, strings, numbers, comments, a $parameter, a
	// $SENTINEL — and checked two ways as you type: against the flow's JSON
	// Schema (an unknown key, a stage type, a signal, a start policy, an
	// action's input; with completion and docs on hover from the same schema),
	// and by the parser itself (`check`), whose verdict — what a schema cannot
	// say — lands under the line it names.
	import { onMount, untrack } from 'svelte';
	import { basicSetup } from 'codemirror';
	import { EditorState } from '@codemirror/state';
	import {
		Decoration,
		EditorView,
		MatchDecorator,
		ViewPlugin,
		hoverTooltip,
		type DecorationSet,
		type ViewUpdate
	} from '@codemirror/view';
	import { HighlightStyle, syntaxHighlighting, indentUnit } from '@codemirror/language';
	import { linter, lintGutter, type Diagnostic } from '@codemirror/lint';
	import { tags } from '@lezer/highlight';
	import { yaml, yamlLanguage } from '@codemirror/lang-yaml';
	import { yamlCompletion, yamlSchemaHover, yamlSchemaLinter } from 'codemirror-json-schema/yaml';
	import { handleRefresh, stateExtensions, updateSchema } from 'codemirror-json-schema';
	import { yamlValues } from './yamlValues';

	let {
		value = $bindable(''),
		schema,
		check,
		label
	}: {
		value: string;
		/** The flow's JSON Schema (GET /api/flows/schema), once fetched. */
		schema?: Record<string, unknown>;
		/** The parser's verdict on the text: its errors, each "file:line: …". */
		check?: (yaml: string) => Promise<string[]>;
		label: string;
	} = $props();

	let host: HTMLDivElement;
	let view: EditorView | undefined;

	// Syntax in the theme's hues: tokens, so light and dark both read.
	const hues = HighlightStyle.define([
		{ tag: [tags.definition(tags.propertyName), tags.propertyName], color: 'var(--az)' },
		{ tag: [tags.string, tags.content, tags.special(tags.string)], color: 'var(--tx)' },
		{ tag: [tags.number, tags.bool, tags.null, tags.atom], color: 'var(--amb)' },
		{ tag: [tags.comment, tags.lineComment], color: 'var(--fnt)', fontStyle: 'italic' },
		{ tag: [tags.labelName, tags.typeName, tags.meta], color: 'var(--vio)' },
		{ tag: [tags.punctuation, tags.separator, tags.squareBracket, tags.brace], color: 'var(--mut)' }
	]);

	// $name reads a parameter; $UPPER is a sentinel the engine fills.
	const refs = new MatchDecorator({
		regexp: /\$\{?[A-Za-z_][A-Za-z0-9_]*\}?/g,
		decoration: (m) =>
			Decoration.mark({ class: /^\$\{?[a-z]/.test(m[0]) ? 'cm-param' : 'cm-sentinel' })
	});
	const refMarks = ViewPlugin.fromClass(
		class {
			marks: DecorationSet;
			constructor(view: EditorView) {
				this.marks = refs.createDeco(view);
			}
			update(u: ViewUpdate) {
				this.marks = refs.updateDeco(u, this.marks);
			}
		},
		{ decorations: (v) => v.marks }
	);

	// One lint, two checks: the schema's, where a person mistyped, then the
	// parser's errors on their lines ("flow:14: …") — those on a line the
	// schema already marks left out, so a typo is said once.
	const schemaLint = yamlSchemaLinter();
	const lint = linter(
		async (v) => {
			const bySchema = schemaLint(v);
			if (!check) return bySchema;
			const text = v.state.doc.toString();
			let errors: string[];
			try {
				errors = await check(text);
			} catch {
				return bySchema;
			}
			if (v.state.doc.toString() !== text) return bySchema;
			const marked = new Set(bySchema.map((d) => v.state.doc.lineAt(d.from).number));
			const byParser = errors.flatMap((e): Diagnostic[] => {
				const m = /^[^:\s]*:(\d+):\s*/.exec(e) ?? /\bline (\d+):\s*/.exec(e);
				const n = m ? Math.min(Math.max(1, Number(m[1])), v.state.doc.lines) : 1;
				if (marked.has(n)) return [];
				const line = v.state.doc.line(n);
				return [
					{
						from: line.from,
						to: line.to,
						severity: 'error',
						source: 'apothecary',
						message: m ? e.slice(m.index + m[0].length) : e
					}
				];
			});
			return [...bySchema, ...byParser];
		},
		{ delay: 600, needsRefresh: handleRefresh }
	);

	const theme = EditorView.theme({
		'&': { height: '100%', color: 'var(--tx)', backgroundColor: 'transparent' },
		'&.cm-focused': { outline: 'none' },
		'.cm-scroller': {
			fontFamily: 'var(--mono)',
			fontSize: 'var(--fs-ui)',
			lineHeight: '1.65'
		},
		'.cm-content': { padding: '14px 0', caretColor: 'var(--brand)' },
		'.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--brand)' },
		'.cm-gutters': { backgroundColor: 'transparent', color: 'var(--fnt)', border: 'none' },
		'.cm-activeLine, .cm-activeLineGutter': { backgroundColor: 'var(--p1)' },
		'&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground':
			{ backgroundColor: 'var(--brandBg)' },
		'.cm-matchingBracket': { backgroundColor: 'var(--p3)', outline: 'none' },
		'.cm-foldPlaceholder': { backgroundColor: 'var(--p2)', border: 'none', color: 'var(--mut)' },
		'.cm-tooltip': {
			backgroundColor: 'var(--popBg)',
			color: 'var(--tx)',
			border: '1px solid var(--ln2)',
			borderRadius: 'var(--r-row)',
			fontFamily: 'var(--sans)',
			fontSize: 'var(--fs-label)',
			maxWidth: '480px'
		},
		'.cm-tooltip-autocomplete > ul > li[aria-selected]': {
			backgroundColor: 'var(--brandBg)',
			color: 'var(--tx)'
		},
		'.cm-diagnostic': { borderLeftWidth: '3px', padding: '4px 8px' },
		'.cm-diagnostic-error': { borderLeftColor: 'var(--ros)' },
		'.cm-diagnostic-warning': { borderLeftColor: 'var(--amb)' },
		'.cm-lintRange-error': {
			backgroundImage: 'none',
			textDecoration: 'underline wavy var(--ros)',
			textUnderlineOffset: '3px'
		},
		'.cm-panels': { backgroundColor: 'var(--popBg)', color: 'var(--tx)' },
		'.cm-param': { color: 'var(--brand)', fontWeight: '600' },
		'.cm-sentinel': { color: 'var(--vio)', fontWeight: '600' }
	});

	onMount(() => {
		view = new EditorView({
			parent: host,
			state: EditorState.create({
				doc: untrack(() => value),
				extensions: [
					basicSetup,
					indentUnit.of('  '),
					EditorState.tabSize.of(2),
					yaml(),
					yamlLanguage.data.of({ autocomplete: yamlCompletion() }),
					yamlLanguage.data.of({ autocomplete: yamlValues }),
					hoverTooltip(yamlSchemaHover()),
					stateExtensions(),
					syntaxHighlighting(hues),
					refMarks,
					lint,
					lintGutter(),
					theme,
					EditorView.contentAttributes.of({ 'aria-label': label, spellcheck: 'false' }),
					EditorView.updateListener.of((u) => {
						if (u.docChanged) value = u.state.doc.toString();
					})
				]
			})
		});
		return () => view?.destroy();
	});

	// The schema arrives after the editor: it starts checking then.
	$effect(() => {
		if (view && schema) updateSchema(view, schema as Parameters<typeof updateSchema>[1]);
	});

	// Kept in step with `value` set from outside — a version loaded into the
	// editor, the graph's document written out.
	$effect(() => {
		const v = value;
		if (view && v !== view.state.doc.toString())
			view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: v } });
	});
</script>

<div class="ye" bind:this={host}></div>

<style>
	.ye {
		width: 100%;
		height: 100%;
		min-height: 0;
		overflow: hidden;
	}
	.ye :global(.cm-editor) {
		height: 100%;
	}
</style>
