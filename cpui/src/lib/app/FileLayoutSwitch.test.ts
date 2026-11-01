import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { prefs } from '$lib/app/prefs.svelte';
import FileLayoutSwitch from './FileLayoutSwitch.svelte';

describe('FileLayoutSwitch', () => {
	it('shows the layout every file list follows', () => {
		prefs.setFileLayout('list');
		render(FileLayoutSwitch);
		const group = screen.getByRole('radiogroup', { name: 'Show files as' });
		expect(within(group).getByRole('radio', { name: 'List' })).toBeChecked();
	});

	it('switches them all, and keeps the choice', async () => {
		prefs.setFileLayout('tree');
		render(FileLayoutSwitch);
		await userEvent.click(screen.getByRole('radio', { name: 'List' }));
		expect(prefs.fileLayout).toBe('list');
		expect(localStorage.getItem('apothecary.files.layout')).toBe('list');

		await userEvent.click(screen.getByRole('radio', { name: 'Tree' }));
		expect(prefs.fileLayout).toBe('tree');
		expect(localStorage.getItem('apothecary.files.layout')).toBe('tree');
	});
});
