import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { bindable, choose } from '$test/fixtures/app.svelte';
import EditorDefaults, { type EditorFields } from './EditorDefaults.svelte';

const editor = (over: Partial<EditorFields> = {}): EditorFields => ({
	theme: '',
	fontSize: '',
	tabSize: '',
	formatOnSave: false,
	wordWrap: false,
	trustWorkspace: false,
	...over
});

describe('EditorDefaults', () => {
	it('shows the stored defaults', () => {
		render(EditorDefaults, {
			editor: editor({ theme: 'Monokai', fontSize: 14, tabSize: 2, wordWrap: true })
		});
		expect(screen.getByRole('combobox', { name: 'Color theme' })).toHaveTextContent('Monokai');
		expect(screen.getByLabelText('Font size')).toHaveValue(14);
		expect(screen.getByLabelText('Tab size')).toHaveValue(2);
		expect(screen.getByRole('switch', { name: 'Word wrap' })).toBeChecked();
	});

	it('reads an empty theme as the code-server default', () => {
		render(EditorDefaults, { editor: editor() });
		expect(screen.getByRole('combobox', { name: 'Color theme' })).toHaveTextContent(
			'code-server default'
		);
	});

	it('edits through to the bound object, field by field', async () => {
		const props = bindable({ editor: editor() });
		render(EditorDefaults, props);

		await choose('Color theme', 'Solarized Dark');
		await userEvent.type(screen.getByLabelText('Font size'), '13');
		await userEvent.type(screen.getByLabelText('Tab size'), '4');
		await userEvent.click(screen.getByRole('switch', { name: 'Format on save' }));
		await userEvent.click(screen.getByRole('switch', { name: 'Word wrap' }));
		await userEvent.click(screen.getByRole('switch', { name: 'Trust workspace' }));

		expect(props.editor).toEqual({
			theme: 'Solarized Dark',
			fontSize: 13,
			tabSize: 4,
			formatOnSave: true,
			wordWrap: true,
			trustWorkspace: true
		});
	});
});
