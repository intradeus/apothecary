<script lang="ts">
	// A file to read in CodeMirror 6: line numbers, find (Ctrl/Cmd+F), the
	// grammar loaded on first use (codeLanguages) and coloured in the theme's
	// role hues. Read-only, but still a text field: you can select, move
	// with the keyboard and search. Each document key keeps its own editor
	// state and scroll snapshot, so documents swap without losing their
	// place, and new content for the same key lands as one minimal change.
	import { onMount, untrack } from 'svelte';
	import { minimalSetup } from 'codemirror';
	import {
		Compartment,
		EditorSelection,
		EditorState,
		type Extension,
		type StateEffect
	} from '@codemirror/state';
	import { EditorView, keymap, lineNumbers, type BlockInfo } from '@codemirror/view';
	import { HighlightStyle, syntaxHighlighting } from '@codemirror/language';
	import { highlightSelectionMatches, search, searchKeymap } from '@codemirror/search';
	import { tags } from '@lezer/highlight';
	import { t } from '$lib/i18n/index.svelte';
	import { languageFor } from './codeLanguages';
	import { minimalChange } from './textChange';

	type Lines = { from: number; to: number };

	let {
		content,
		path,
		label,
		docKey,
		line = null,
		onrevealed,
		onselect
	}: {
		content: string;
		/** Picks the language: the file name or extension, else a shebang on the first line. */
		path: string;
		/** The accessible name. */
		label: string;
		/** The document's identity (default: path); each keeps its own state and scroll. */
		docKey?: string;
		/** 1-based, inclusive: revealed and selected once the content is in. */
		line?: Lines | null;
		/** After `line` was revealed. */
		onrevealed?: () => void;
		/** The selected lines (1-based, inclusive), null when nothing is selected. */
		onselect?: (range: Lines | null) => void;
	} = $props();

	const key = $derived(docKey ?? path);

	let host: HTMLDivElement;
	let view: EditorView | undefined;
	const lang = new Compartment();
	const local = new Compartment();

	// A document put away: its state, where it was scrolled, and which path's
	// language its state carries.
	type Doc = { state: EditorState; scroll: StateEffect<unknown>; lang: string | null };
	const docs = new Map<string, Doc>();
	/** The document on screen; null after it was forgotten (its state is not kept when left). */
	let shown: string | null = null;
	let shownLang: string | null = null;
	let wanted = '';
	/** The last selection reported, with its document: what the caller holds. */
	let reported: string | null = null;
	/** The selection was last set by search (a match, not lines to talk about). */
	let bySearch = false;

	const hues = HighlightStyle.define([
		{
			tag: [
				tags.keyword,
				tags.controlKeyword,
				tags.definitionKeyword,
				tags.moduleKeyword,
				tags.operatorKeyword,
				tags.modifier,
				tags.self
			],
			color: 'var(--accent-text)'
		},
		{
			tag: [
				tags.string,
				tags.special(tags.string),
				tags.character,
				tags.regexp,
				tags.attributeValue
			],
			color: 'var(--success-text)'
		},
		{
			tag: [tags.number, tags.bool, tags.null, tags.atom, tags.unit, tags.escape],
			color: 'var(--warning-text)'
		},
		{ tag: [tags.comment, tags.lineComment, tags.blockComment], color: 'var(--text-faint)' },
		{ tag: tags.docComment, color: 'var(--text-faint)', fontStyle: 'italic' },
		{
			tag: [
				tags.typeName,
				tags.className,
				tags.namespace,
				tags.tagName,
				tags.labelName,
				// $HOME in a shell script; a name where it is declared
				tags.special(tags.variableName),
				tags.definition(tags.variableName)
			],
			color: 'var(--accent-2-text)'
		},
		{
			tag: [
				tags.function(tags.variableName),
				tags.function(tags.propertyName),
				// a shell builtin (echo)
				tags.standard(tags.variableName),
				tags.macroName,
				tags.attributeName
			],
			color: 'var(--brand-text)'
		},
		{
			tag: [tags.meta, tags.processingInstruction, tags.annotation, tags.documentMeta],
			color: 'var(--text-muted)'
		},
		{
			tag: [tags.heading, tags.heading1, tags.heading2, tags.heading3],
			color: 'var(--brand-text)',
			fontWeight: '600'
		},
		{ tag: tags.emphasis, fontStyle: 'italic' },
		{ tag: tags.strong, fontWeight: '600' },
		{ tag: [tags.link, tags.url], color: 'var(--brand-text)' },
		{ tag: tags.monospace, color: 'var(--warning-text)' },
		{ tag: [tags.list, tags.quote], color: 'var(--accent-text)' },
		{ tag: tags.inserted, color: 'var(--success-text)' },
		{ tag: [tags.deleted, tags.invalid], color: 'var(--danger-text)' },
		{ tag: tags.changed, color: 'var(--warning-text)' }
	]);

	const theme = EditorView.theme({
		'&': { height: '100%', color: 'var(--text)', backgroundColor: 'transparent' },
		'&.cm-focused': { outline: 'none' },
		'.cm-scroller': {
			fontFamily: 'var(--font-code)',
			fontSize: 'var(--fs-ui)',
			lineHeight: '1.6'
		},
		'.cm-content': { padding: '10px 0', caretColor: 'var(--brand)' },
		'.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--brand)' },
		'.cm-gutters': { backgroundColor: 'transparent', color: 'var(--text-faint)', border: 'none' },
		'.cm-lineNumbers .cm-gutterElement': { padding: '0 10px 0 14px', cursor: 'pointer' },
		'.cm-lineNumbers .cm-gutterElement:hover': { color: 'var(--text)' },
		'&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground':
			{ backgroundColor: 'var(--brand-bg)' },
		// The page's ::selection sets an on-brand ink for a brand fill; over the
		// drawn selection the code keeps its own colours.
		'.cm-content ::selection, .cm-content::selection': { color: 'currentColor' },
		'.cm-selectionMatch': { backgroundColor: 'var(--fill-2)' },
		'.cm-searchMatch': {
			backgroundColor: 'var(--warning-bg)',
			outline: '1px solid var(--warning-line)'
		},
		'.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'var(--warning-glow)' },
		'.cm-panels': { backgroundColor: 'var(--island-bg)', color: 'var(--text)' },
		'.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--line)' },
		// The page's form defaults (full-width inputs, block labels) are undone here.
		'.cm-panel.cm-search': {
			display: 'flex',
			flexWrap: 'wrap',
			alignItems: 'center',
			gap: '4px 6px',
			padding: '6px 34px 6px 10px',
			fontSize: 'var(--fs-label)'
		},
		'.cm-panel.cm-search br': { display: 'none' },
		'.cm-panel.cm-search label': {
			display: 'inline-flex',
			alignItems: 'center',
			gap: '4px',
			margin: '0',
			fontSize: 'var(--fs-label)',
			fontWeight: 'normal',
			color: 'var(--text-muted)'
		},
		'.cm-panel.cm-search [name=close]': { color: 'var(--text-muted)', top: '6px', right: '8px' },
		'.cm-textfield': {
			width: 'auto',
			margin: '0',
			padding: '2px 8px',
			fontSize: 'var(--fs-label)',
			backgroundColor: 'var(--fill-1)',
			border: '1px solid var(--line)',
			borderRadius: 'var(--r-xs)',
			color: 'var(--text)'
		},
		'.cm-button': {
			margin: '0',
			padding: '2px 8px',
			fontSize: 'var(--fs-label)',
			backgroundImage: 'none',
			backgroundColor: 'var(--fill-2)',
			border: '1px solid var(--line)',
			borderRadius: 'var(--r-xs)',
			color: 'var(--text)'
		}
	});

	// The accessible name and the search panel's words, in the reader's language.
	const localised = (): Extension => [
		EditorView.contentAttributes.of({ 'aria-label': label }),
		EditorState.phrases.of({
			Find: t('ui.codeViewer.find'),
			next: t('ui.codeViewer.next'),
			previous: t('ui.codeViewer.previous'),
			all: t('ui.codeViewer.all'),
			'match case': t('ui.codeViewer.matchCase'),
			regexp: t('ui.codeViewer.regexp'),
			'by word': t('ui.codeViewer.byWord'),
			close: t('ui.codeViewer.close'),
			'Go to line': t('ui.codeViewer.goToLine'),
			go: t('ui.codeViewer.go'),
			'current match': t('ui.codeViewer.currentMatch'),
			'on line': t('ui.codeViewer.onLine')
		})
	];

	// The lines the main selection covers; one ending at a line's start
	// (a drag down to the next line's edge) stops on the line before. A
	// search match is a selection too, but not lines picked to talk about.
	function report(state: EditorState, searched = false) {
		const sel = state.selection.main;
		let range: Lines | null = null;
		if (!sel.empty && !searched) {
			const last = state.doc.lineAt(sel.to);
			range = {
				from: state.doc.lineAt(sel.from).number,
				to: last.from === sel.to ? last.number - 1 : last.number
			};
		}
		const said = range && `${shown}:${range.from}-${range.to}`;
		if (said === reported) return;
		reported = said;
		onselect?.(range);
	}

	// A click on a line number selects the line; with shift, every line
	// from the selection's anchor to it.
	function gutterDown(v: EditorView, block: BlockInfo, event: Event) {
		const e = event as MouseEvent;
		if (e.button !== 0) return false;
		const doc = v.state.doc;
		const target = doc.lineAt(block.from);
		const anchor = e.shiftKey ? doc.lineAt(v.state.selection.main.anchor) : target;
		v.dispatch({
			selection:
				target.number < anchor.number
					? EditorSelection.single(anchor.to, target.from)
					: EditorSelection.single(anchor.from, target.to),
			userEvent: 'select.pointer'
		});
		v.focus();
		return true;
	}

	const fixed: Extension = [
		minimalSetup,
		lineNumbers({ domEventHandlers: { mousedown: gutterDown } }),
		EditorState.readOnly.of(true),
		search({ top: true }),
		keymap.of(searchKeymap),
		highlightSelectionMatches(),
		syntaxHighlighting(hues),
		theme,
		EditorView.updateListener.of((u) => {
			if (u.selectionSet) bySearch = u.transactions.some((tr) => tr.isUserEvent('select.search'));
			if (u.selectionSet || u.docChanged) report(u.state, bySearch);
		})
	];

	function setLang(v: EditorView, p: string, support: Extension) {
		shownLang = p;
		v.dispatch({ effects: lang.reconfigure(support) });
	}

	// The shown document's grammar, once. A load that lands after the
	// document or its path moved on is dropped.
	function language(v: EditorView, k: string, p: string, text: string) {
		wanted = p;
		if (shownLang === p) return;
		const desc = languageFor(p, text.split('\n', 1)[0]);
		if (!desc) return setLang(v, p, []);
		if (desc.support) return setLang(v, p, desc.support);
		desc.load().then(
			(support) => {
				if (shown === k && wanted === p && shownLang !== p) setLang(v, p, support);
			},
			// The grammar's chunk failed to load: the file reads as plain text.
			() => {
				if (shown === k && wanted === p) setLang(v, p, []);
			}
		);
	}

	// Same text: nothing. Else one change between the common prefix and
	// suffix, so what lies outside it keeps its place.
	function patch(v: EditorView, text: string) {
		const change = minimalChange(v.state.doc.toString(), v.state.toText(text).toString());
		if (change) v.dispatch({ changes: change });
	}

	function show(k: string, text: string, p: string): EditorView {
		const v = (view ??= new EditorView({ parent: host }));
		if (k === shown) patch(v, text);
		else {
			if (shown !== null)
				docs.set(shown, { state: v.state, scroll: v.scrollSnapshot(), lang: shownLang });
			shown = k;
			const doc = docs.get(k);
			if (doc) {
				v.setState(doc.state);
				shownLang = doc.lang;
				v.dispatch({ effects: [doc.scroll, local.reconfigure(localised())] });
				patch(v, text);
			} else {
				shownLang = null;
				v.setState(
					EditorState.create({
						doc: text,
						extensions: [fixed, lang.of([]), local.of(localised())]
					})
				);
			}
			report(v.state);
		}
		language(v, k, p, text);
		return v;
	}

	function reveal(v: EditorView, lines: Lines) {
		const doc = v.state.doc;
		const at = (n: number) => doc.line(Math.min(Math.max(n, 1), doc.lines));
		const first = at(lines.from);
		v.dispatch({
			selection: EditorSelection.single(first.from, at(Math.max(lines.to, lines.from)).to),
			effects: EditorView.scrollIntoView(first.from, { y: 'center' })
		});
		onrevealed?.();
	}

	/** Collapse the selection to its head. */
	export function clearSelection() {
		view?.dispatch({ selection: EditorSelection.cursor(view.state.selection.main.head) });
	}

	/** Drop a document's kept state (its tab closed); the shown one is not kept, nor reused, when shown next. */
	export function forget(k: string) {
		docs.delete(k);
		if (k === shown) shown = null;
	}

	onMount(() => () => view?.destroy());

	$effect(() => {
		const k = key;
		const text = content;
		const p = path;
		const lines = line;
		untrack(() => {
			const v = show(k, text, p);
			if (lines) reveal(v, lines);
		});
	});

	// Another label or locale: the shown state's name and words (a state put
	// away catches up when it is shown again).
	$effect(() => {
		const ext = localised();
		untrack(() => view?.dispatch({ effects: local.reconfigure(ext) }));
	});
</script>

<div class="cv" bind:this={host}></div>

<style>
	.cv {
		width: 100%;
		height: 100%;
		min-height: 0;
		overflow: hidden;
	}
	.cv :global(.cm-editor) {
		height: 100%;
	}
</style>
