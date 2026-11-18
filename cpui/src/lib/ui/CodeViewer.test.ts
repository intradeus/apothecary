import { LanguageDescription, language, type LanguageSupport } from '@codemirror/language';
import type { StateEffect, TransactionSpec } from '@codemirror/state';
import { EditorView } from '@codemirror/view';
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { bindable } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import { languageFor } from './codeLanguages';
import CodeViewer from './CodeViewer.svelte';

type Langs = { languageFor: typeof languageFor };
vi.mock('./codeLanguages', async (actual) => ({
	languageFor: vi.fn((await actual<Langs>()).languageFor)
}));
const { languageFor: realLanguageFor } = await vi.importActual<Langs>('./codeLanguages');

type Lines = { from: number; to: number };
const TEXT = 'one\ntwo\nthree\nfour\nfive';

const setup = (over: { content?: string; path?: string; docKey?: string; line?: Lines } = {}) => {
	const props = bindable({
		content: TEXT,
		path: 'notes.unknownext',
		label: 'notes',
		docKey: undefined as string | undefined,
		line: null as Lines | null,
		onrevealed: vi.fn(),
		onselect: vi.fn(),
		...over
	});
	const r = render(CodeViewer, props);
	const content = screen.getByRole('textbox', { name: 'notes' });
	return {
		props,
		component: r.component,
		container: r.container,
		content,
		view: EditorView.findFromDOM(content)!
	};
};

const lineText = (view: EditorView) => {
	const { from, to } = view.state.selection.main;
	return view.state.sliceDoc(from, to);
};

// Every transaction the viewer dispatches from here on.
const spyDispatch = (view: EditorView) => {
	const specs: TransactionSpec[] = [];
	const real = view.dispatch.bind(view);
	vi.spyOn(view, 'dispatch').mockImplementation(((...args: TransactionSpec[]) => {
		specs.push(...args);
		real(...args);
	}) as typeof view.dispatch);
	return specs;
};
const effectsOf = (specs: TransactionSpec[]) =>
	specs.flatMap((s) => [s.effects ?? []].flat() as StateEffect<unknown>[]);

// jsdom has no layout: a gutter click lands on whichever line the test names.
const gutterClick = (view: EditorView, container: HTMLElement, n: number, init: MouseEventInit) => {
	vi.spyOn(view, 'lineBlockAtHeight').mockReturnValue(
		view.lineBlockAt(view.state.doc.line(n).from)
	);
	const number = container.querySelector('.cm-lineNumbers .cm-gutterElement:last-child')!;
	fireEvent.mouseDown(number, { button: 0, ...init });
};

beforeAll(() => {
	// CodeMirror measures text; jsdom lays nothing out.
	Range.prototype.getClientRects = () => [] as unknown as DOMRectList;
	Range.prototype.getBoundingClientRect = () => new DOMRect();
});

afterEach(() => {
	vi.mocked(languageFor).mockImplementation(realLanguageFor);
});

describe('CodeViewer', () => {
	it('shows the content with line numbers, read-only but selectable', () => {
		const { content, container } = setup();
		expect(content).toHaveTextContent('one');
		expect(content).toHaveTextContent('five');
		const numbers = [...container.querySelectorAll('.cm-lineNumbers .cm-gutterElement')].map(
			(n) => n.textContent
		);
		expect(numbers).toEqual(expect.arrayContaining(['1', '2', '3', '4', '5']));
		expect(content).toHaveAttribute('aria-readonly', 'true');
		expect(content).toHaveAttribute('contenteditable', 'true');
	});

	it('changes nothing when typed into or pasted on', async () => {
		const { content, view } = setup();
		view.focus();
		await userEvent.type(content, 'x{Enter}{Backspace}{Delete}');
		fireEvent.paste(content, { clipboardData: { getData: () => 'pasted' } });
		await tick();
		expect(view.state.doc.toString()).toBe(TEXT);
	});

	it('opens find on Ctrl+F, without replace', async () => {
		const { content } = setup();
		await fireEvent.keyDown(content, { key: 'f', ctrlKey: true });
		expect(await screen.findByRole('textbox', { name: 'Find' })).toBeInTheDocument();
		expect(screen.queryByRole('textbox', { name: 'Replace' })).not.toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'close' })).toBeInTheDocument();
	});

	it('takes a new label', async () => {
		const { props } = setup();
		props.label = 'other';
		await tick();
		expect(screen.getByRole('textbox', { name: 'other' })).toBeInTheDocument();
	});

	describe('language', () => {
		it('loads the grammar for the path', async () => {
			const { view } = setup({ path: 'README.md', content: '# Hi' });
			await waitFor(() => expect(view.state.facet(language)?.name).toBe('markdown'));
		});

		it('uses a grammar already loaded at once', async () => {
			await realLanguageFor('a.go')!.load();
			const { view } = setup({ path: 'main.go', content: 'package main' });
			expect(view.state.facet(language)?.name).toBe('go');
		});

		it('reads an extensionless script by its shebang', async () => {
			const { view } = setup({ path: 'bin/run', content: '#!/bin/sh\necho hi' });
			await waitFor(() => expect(view.state.facet(language)?.name).toBe('shell'));
			expect(languageFor).toHaveBeenLastCalledWith('bin/run', '#!/bin/sh');
		});

		it('follows another path for the same document, and drops a load that lands late', async () => {
			const slow = deferred<LanguageSupport>();
			vi.mocked(languageFor).mockImplementation((p, first) =>
				p.endsWith('.slow')
					? LanguageDescription.of({ name: 'Slow', load: () => slow.promise })
					: realLanguageFor(p, first)
			);
			const { props, view } = setup({ path: 'a.slow', docKey: 'doc' });
			props.path = 'a.md';
			await tick();
			await waitFor(() => expect(view.state.facet(language)?.name).toBe('markdown'));
			slow.resolve(await realLanguageFor('x.go')!.load());
			await slow.promise;
			await tick();
			expect(view.state.facet(language)?.name).toBe('markdown');
		});

		it('drops a load that lands after the document changed', async () => {
			const slow = deferred<LanguageSupport>();
			vi.mocked(languageFor).mockImplementation((p, first) =>
				p.endsWith('.slow')
					? LanguageDescription.of({ name: 'Slow', load: () => slow.promise })
					: realLanguageFor(p, first)
			);
			const { props, view } = setup({ path: 'a.slow' });
			props.path = 'b.unknownext';
			await tick();
			slow.resolve(await realLanguageFor('x.go')!.load());
			await slow.promise;
			await tick();
			expect(view.state.facet(language)).toBeNull();
		});

		it('applies a slow load that lands in time, and reads plain text when one fails', async () => {
			const slow = deferred<LanguageSupport>();
			const broken = deferred<LanguageSupport>();
			vi.mocked(languageFor).mockImplementation((p) =>
				LanguageDescription.of({
					name: p,
					load: () => (p.endsWith('.slow') ? slow.promise : broken.promise)
				})
			);
			const { props, view } = setup({ path: 'a.slow' });
			slow.resolve(await realLanguageFor('x.go')!.load());
			await waitFor(() => expect(view.state.facet(language)?.name).toBe('go'));
			props.path = 'b.broken';
			await tick();
			broken.reject(new Error('chunk failed'));
			await waitFor(() => expect(view.state.facet(language)).toBeNull());
		});
	});

	describe('content', () => {
		it('lands new content as one minimal change, keeping scroll and selection', async () => {
			const { props, view } = setup();
			view.dispatch({ selection: { anchor: TEXT.indexOf('four'), head: TEXT.indexOf('five') } });
			view.scrollDOM.scrollTop = 120;
			const specs = spyDispatch(view);
			props.content = TEXT.replace('two', 'TWO!');
			await tick();
			expect(specs).toEqual([{ changes: { from: 4, to: 7, insert: 'TWO!' } }]);
			expect(view.state.doc.toString()).toBe(props.content);
			expect(lineText(view)).toBe('four\n');
			expect(view.scrollDOM.scrollTop).toBe(120);
		});

		it('dispatches nothing for the same content, line endings aside', async () => {
			const { props, view } = setup();
			const specs = spyDispatch(view);
			props.content = TEXT.replaceAll('\n', '\r\n');
			await tick();
			expect(specs).toEqual([]);
		});

		it('keeps each document, its selection and its scroll', async () => {
			const { props, view, container } = setup({ docKey: 'A', content: 'a1\na2\na3' });
			const snaps: StateEffect<unknown>[] = [];
			const snapshot = view.scrollSnapshot.bind(view);
			vi.spyOn(view, 'scrollSnapshot').mockImplementation(() => {
				const s = snapshot();
				snaps.push(s);
				return s;
			});
			gutterClick(view, container, 2, {});
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 2, to: 2 });

			props.docKey = 'B';
			props.content = 'b1\nb2';
			await tick();
			expect(view.state.doc.toString()).toBe('b1\nb2');
			expect(props.onselect).toHaveBeenLastCalledWith(null);
			expect(snaps).toHaveLength(1);

			const specs = spyDispatch(view);
			props.docKey = 'A';
			props.content = 'a1\na2\na3\na4';
			await tick();
			expect(view.state.doc.toString()).toBe('a1\na2\na3\na4');
			expect(lineText(view)).toBe('a2');
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 2, to: 2 });
			expect(effectsOf(specs)).toContain(snaps[0]);

			props.docKey = 'B';
			props.content = 'b1\nb2';
			await tick();
			expect(effectsOf(specs)).toContain(snaps[1]);
			expect(view.state.doc.toString()).toBe('b1\nb2');
		});

		it('forgets a closed document, the shown one once it is left or shown again', async () => {
			const { props, view, component } = setup({ docKey: 'A', content: 'a1\na2' });
			view.dispatch({ selection: { anchor: 0, head: 2 } });
			props.docKey = 'B';
			await tick();
			component.forget('A');
			props.docKey = 'A';
			await tick();
			expect(view.state.selection.main.empty).toBe(true);

			view.dispatch({ selection: { anchor: 0, head: 2 } });
			component.forget('A');
			props.docKey = 'B';
			await tick();
			props.docKey = 'A';
			await tick();
			expect(view.state.selection.main.empty).toBe(true);

			// Forgotten while shown, then opened again without leaving it: anew.
			view.dispatch({ selection: { anchor: 0, head: 2 } });
			component.forget('A');
			props.content = 'a1\na2\na3';
			await tick();
			expect(view.state.selection.main.empty).toBe(true);
			expect(view.state.doc.toString()).toBe('a1\na2\na3');
		});
	});

	describe('selection', () => {
		it('selects a line from the gutter, and extends it with shift', () => {
			const { props, view, container } = setup();
			gutterClick(view, container, 2, {});
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 2, to: 2 });
			expect(lineText(view)).toBe('two');
			gutterClick(view, container, 4, { shiftKey: true });
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 2, to: 4 });
			gutterClick(view, container, 1, { shiftKey: true });
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 1, to: 2 });
			expect(lineText(view)).toBe('one\ntwo');
			gutterClick(view, container, 3, {});
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 3, to: 3 });
		});

		it('picks a blank line from the gutter, inside the file and at its end', () => {
			const { props, view, container } = setup({ content: 'one\n\nthree\n' });
			const marked = () =>
				[...container.querySelectorAll('.cm-line.cm-picked')].map((l) => l.textContent);
			gutterClick(view, container, 2, {});
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 2, to: 2 });
			expect(marked()).toEqual(['']);
			gutterClick(view, container, 4, {});
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 4, to: 4 });
			expect(marked()).toEqual(['']);
			gutterClick(view, container, 1, { shiftKey: true });
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 1, to: 4 });
			// Down to a blank last line, which the selection draws nothing on.
			gutterClick(view, container, 3, {});
			gutterClick(view, container, 4, { shiftKey: true });
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 3, to: 4 });
			expect(marked()).toEqual(['']);
			// A line with text is drawn by the selection alone.
			gutterClick(view, container, 3, {});
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 3, to: 3 });
			expect(marked()).toEqual([]);
		});

		it('keeps a picked blank line through new content, and drops it for another selection', async () => {
			const { props, view, container } = setup({ content: 'one\n\nthree' });
			gutterClick(view, container, 2, {});
			props.content = 'zero\none\n\nthree';
			await tick();
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 3, to: 3 });
			view.dispatch({ selection: { anchor: 0 } });
			expect(props.onselect).toHaveBeenLastCalledWith(null);
			expect(container.querySelector('.cm-picked')).toBeNull();
		});

		it('ignores another button on the gutter', () => {
			const { props, view, container } = setup();
			gutterClick(view, container, 2, { button: 2 });
			expect(props.onselect).not.toHaveBeenCalled();
		});

		it('reports a drag by the lines it covers, once per change', () => {
			const { props, view } = setup();
			// From line 1 to the start of line 3: lines 1 and 2.
			view.dispatch({ selection: { anchor: 1, head: TEXT.indexOf('three') } });
			view.dispatch({ selection: { anchor: 2, head: TEXT.indexOf('three') } });
			expect(props.onselect).toHaveBeenCalledTimes(1);
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 1, to: 2 });
			view.dispatch({ selection: { anchor: 3 } });
			expect(props.onselect).toHaveBeenLastCalledWith(null);
		});

		it('reports the same lines again when another document shows them', async () => {
			const { props, view } = setup({ docKey: 'A', content: 'a1\na2' });
			view.dispatch({ selection: { anchor: 0, head: 2 } });
			props.docKey = 'B';
			await tick();
			view.dispatch({ selection: { anchor: 0, head: 2 } });
			props.onselect.mockClear();

			props.docKey = 'A';
			await tick();

			expect(props.onselect).toHaveBeenCalledExactlyOnceWith({ from: 1, to: 1 });
		});

		it('does not report a search match as lines to talk about', () => {
			const { props, view } = setup();
			const at = TEXT.indexOf('three');
			view.dispatch({ selection: { anchor: at, head: at + 5 }, userEvent: 'select.search' });
			// New content keeps the match selected, and still unreported.
			view.dispatch({ changes: { from: 0, insert: '0' } });
			expect(props.onselect).not.toHaveBeenCalled();
			view.dispatch({ selection: { anchor: 1, head: 3 }, userEvent: 'select.pointer' });
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 1, to: 1 });
		});

		it("keeps each document's search match apart from another's selection", async () => {
			const { props, view } = setup({ docKey: 'A', content: 'a1\na2\na3' });
			view.dispatch({ selection: { anchor: 3, head: 5 }, userEvent: 'select.search' });
			props.docKey = 'B';
			props.content = 'b1\nb2\nb3';
			await tick();
			view.dispatch({ selection: { anchor: 0, head: 2 }, userEvent: 'select.pointer' });
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 1, to: 1 });

			// Back on A, its match is still a match, not lines.
			props.docKey = 'A';
			props.content = 'a1\na2\na3';
			await tick();
			expect(lineText(view)).toBe('a2');
			expect(props.onselect).toHaveBeenLastCalledWith(null);

			// Back on B, a live refresh keeps its lines picked.
			props.docKey = 'B';
			props.content = 'b1\nb2\nb3';
			await tick();
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 1, to: 1 });
			props.content = 'b1\nb2\nb3\nb4';
			await tick();
			expect(view.state.doc.toString()).toBe('b1\nb2\nb3\nb4');
			expect(lineText(view)).toBe('b1');
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 1, to: 1 });
		});

		it('clears the selection', () => {
			const { props, view, component } = setup();
			view.dispatch({ selection: { anchor: 0, head: 6 } });
			component.clearSelection();
			expect(view.state.selection.main.empty).toBe(true);
			expect(props.onselect).toHaveBeenLastCalledWith(null);
		});
	});

	describe('line', () => {
		it('reveals and selects the lines once the content is in', () => {
			const { props, view } = setup({ line: { from: 2, to: 3 } });
			expect(lineText(view)).toBe('two\nthree');
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 2, to: 3 });
			expect(props.onrevealed).toHaveBeenCalledTimes(1);
		});

		it('reveals a blank line as a line to talk about', () => {
			const { props } = setup({ content: 'one\n\nthree', line: { from: 2, to: 2 } });
			expect(props.onselect).toHaveBeenLastCalledWith({ from: 2, to: 2 });
		});

		it('reveals in the document it arrives with, clamped to it', async () => {
			const { props, view } = setup({ docKey: 'A' });
			const specs = spyDispatch(view);
			props.docKey = 'B';
			props.content = 'b1\nb2\nb3';
			props.line = { from: 3, to: 1 };
			await tick();
			expect(lineText(view)).toBe('b3');
			expect(props.onrevealed).toHaveBeenCalledTimes(1);
			expect(effectsOf(specs).length).toBeGreaterThan(0);
			props.line = { from: 0, to: 99 };
			await tick();
			expect(lineText(view)).toBe('b1\nb2\nb3');
			expect(props.onrevealed).toHaveBeenCalledTimes(2);
		});
	});

	it('destroys the editor on unmount', () => {
		const props = bindable({ content: 'x', path: 'a.md', label: 'file' });
		const { unmount } = render(CodeViewer, props);
		const content = screen.getByRole('textbox', { name: 'file' });
		unmount();
		expect(content).not.toBeInTheDocument();
	});
});
