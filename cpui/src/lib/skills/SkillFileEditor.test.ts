import { language } from '@codemirror/language';
import { EditorView } from '@codemirror/view';
import { render, screen, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import { beforeAll, describe, expect, it } from 'vitest';
import { bindable } from '$test/fixtures/app.svelte';
import SkillFileEditor from './SkillFileEditor.svelte';

const setup = (value: string, path = 'SKILL.md') => {
	const props = bindable({ value, path, label: 'SKILL.md' });
	render(SkillFileEditor, props);
	const content = screen.getByRole('textbox', { name: 'SKILL.md' });
	return { props, content, view: EditorView.findFromDOM(content)! };
};
const typed = (view: EditorView, text: string) =>
	view.dispatch({ changes: { from: view.state.doc.length, insert: text } });

beforeAll(() => {
	// CodeMirror measures text; jsdom lays nothing out.
	Range.prototype.getClientRects = () => [] as unknown as DOMRectList;
	Range.prototype.getBoundingClientRect = () => new DOMRect();
});

describe('SkillFileEditor', () => {
	it('shows the file, named for assistive tech', () => {
		const { content } = setup('# Hello');
		expect(content).toHaveTextContent('# Hello');
	});

	it('writes edits back through value', () => {
		const { props, view } = setup('# Hello');
		typed(view, ' world');
		expect(props.value).toBe('# Hello world');
	});

	it('takes a value set from outside', async () => {
		const { props, view } = setup('one');
		props.value = 'two';
		await tick();
		await waitFor(() => expect(view.state.doc.toString()).toBe('two'));
	});

	it('highlights markdown for a .md file and nothing else', () => {
		const { view } = setup('x', 'references/guide.md');
		expect(view.state.facet(language)?.name).toBe('markdown');
	});

	it('reads a script as plain text', () => {
		const { view } = setup('x', 'run.sh');
		expect(view.state.facet(language)).toBeNull();
	});

	it('changes language with the file', async () => {
		const { props, view } = setup('x', 'SKILL.md');
		props.path = 'run.sh';
		await tick();
		await waitFor(() => expect(view.state.facet(language)).toBeNull());
	});

	it('destroys the editor on unmount', () => {
		const props = bindable({ value: 'x', path: 'a.md', label: 'file' });
		const { unmount } = render(SkillFileEditor, props);
		const content = screen.getByRole('textbox', { name: 'file' });
		unmount();
		expect(content).not.toBeInTheDocument();
	});
});
