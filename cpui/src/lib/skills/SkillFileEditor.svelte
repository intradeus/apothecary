<script lang="ts">
	// One skill file in CodeMirror 6: markdown highlighting for SKILL.md and
	// its references, plain text for scripts and the rest, in the theme's
	// hues like the flow editor's YAML tab.
	import { onMount, untrack } from 'svelte';
	import { basicSetup } from 'codemirror';
	import { Compartment, EditorState } from '@codemirror/state';
	import { EditorView } from '@codemirror/view';
	import { HighlightStyle, syntaxHighlighting, indentUnit } from '@codemirror/language';
	import { tags } from '@lezer/highlight';
	import { markdown } from '@codemirror/lang-markdown';
	import { languageFor } from './files';

	let {
		value = $bindable(''),
		path,
		label
	}: {
		value: string;
		/** Picks the language (markdown for .md). */
		path: string;
		label: string;
	} = $props();

	let host: HTMLDivElement;
	let view: EditorView | undefined;
	const lang = new Compartment();

	const hues = HighlightStyle.define([
		{
			tag: [tags.heading, tags.heading1, tags.heading2, tags.heading3],
			color: 'var(--az)',
			fontWeight: '600'
		},
		{ tag: [tags.emphasis], fontStyle: 'italic' },
		{ tag: [tags.strong], fontWeight: '600' },
		{ tag: [tags.link, tags.url], color: 'var(--brand)' },
		{ tag: [tags.monospace, tags.string], color: 'var(--amb)' },
		{ tag: [tags.comment, tags.meta, tags.processingInstruction], color: 'var(--fnt)' },
		{ tag: [tags.list, tags.quote], color: 'var(--vio)' }
	]);

	const theme = EditorView.theme({
		'&': { height: '100%', color: 'var(--tx)', backgroundColor: 'transparent' },
		'&.cm-focused': { outline: 'none' },
		'.cm-scroller': { fontFamily: 'var(--mono)', fontSize: 'var(--fs-ui)', lineHeight: '1.65' },
		'.cm-content': { padding: '14px 0', caretColor: 'var(--brand)' },
		'.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--brand)' },
		'.cm-gutters': { backgroundColor: 'transparent', color: 'var(--fnt)', border: 'none' },
		'.cm-activeLine, .cm-activeLineGutter': { backgroundColor: 'var(--p1)' },
		'&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground':
			{ backgroundColor: 'var(--brandBg)' },
		'.cm-matchingBracket': { backgroundColor: 'var(--p3)', outline: 'none' },
		'.cm-panels': { backgroundColor: 'var(--popBg)', color: 'var(--tx)' }
	});

	const langFor = (p: string) => (languageFor(p) === 'markdown' ? markdown() : []);

	onMount(() => {
		view = new EditorView({
			parent: host,
			state: EditorState.create({
				doc: untrack(() => value),
				extensions: [
					basicSetup,
					indentUnit.of('  '),
					EditorState.tabSize.of(2),
					lang.of(langFor(untrack(() => path))),
					syntaxHighlighting(hues),
					EditorView.lineWrapping,
					theme,
					EditorView.contentAttributes.of({ 'aria-label': label, spellcheck: 'true' }),
					EditorView.updateListener.of((u) => {
						if (u.docChanged) value = u.state.doc.toString();
					})
				]
			})
		});
		return () => view?.destroy();
	});

	// Another file picked: its language.
	$effect(() => {
		const p = path;
		if (view) view.dispatch({ effects: lang.reconfigure(langFor(p)) });
	});

	// Kept in step with `value` set from outside (another file, a refresh).
	$effect(() => {
		const v = value;
		if (view && v !== view.state.doc.toString())
			view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: v } });
	});
</script>

<div class="sfe" bind:this={host}></div>

<style>
	.sfe {
		width: 100%;
		height: 100%;
		min-height: 0;
		overflow: hidden;
	}
	.sfe :global(.cm-editor) {
		height: 100%;
	}
</style>
