import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { t } from '$lib/i18n/index.svelte';
import CodeView, { type CodeLine } from './CodeView.svelte';

const lines: CodeLine[] = [
	{ kind: 'hunk', text: '@@ -1,2 +1,2 @@' },
	{ kind: 'ctx', n: 1, old: 1, text: 'keep' },
	{ kind: 'del', old: 2, text: 'gone' },
	{ kind: 'add', n: 2, text: 'fresh' }
];
const row = (text: string) => screen.getByText(text).parentElement!;
const gutters = (text: string) => [...row(text).querySelectorAll('.ln')].map((g) => g.textContent);

describe('CodeView', () => {
	it('shows each line after its number', () => {
		render(CodeView, { lines: [{ n: 7, text: 'const a = 1;' }] });
		expect(gutters('const a = 1;')).toEqual(['7']);
	});

	it('leaves the gutter blank for a line with no number', () => {
		render(CodeView, { lines });
		expect(gutters('@@ -1,2 +1,2 @@')).toEqual(['']);
	});

	it('gives a side-aware diff two gutters, old then new', () => {
		render(CodeView, { lines, gutters: 2 });
		expect(gutters('keep')).toEqual(['1', '1']);
		expect(gutters('gone')).toEqual(['2', '']);
		expect(gutters('fresh')).toEqual(['', '2']);
	});

	it('tints each line by its kind', () => {
		render(CodeView, { lines });
		expect(row('fresh')).toHaveClass('add');
		expect(row('gone')).toHaveClass('del');
		expect(row('@@ -1,2 +1,2 @@')).toHaveClass('hunk');
	});

	it('puts the truncation under the code', () => {
		render(CodeView, { lines, truncated: 'showing 400 of 1,204 lines' });
		expect(screen.getByText('fresh')).toBeInTheDocument();
		expect(screen.getByText('truncated · showing 400 of 1,204 lines')).toBeInTheDocument();
	});

	it('takes the words around the truncation from the catalogue', () => {
		render(CodeView, { lines, truncated: 'showing 1 of 2' });
		expect(screen.getByText(t('ui.code.truncated', { detail: 'showing 1 of 2' }))).toBeVisible();
	});

	it('claims no truncation by default', () => {
		render(CodeView, { lines });
		expect(screen.queryByText(/truncated/)).not.toBeInTheDocument();
	});

	it('says binary instead of showing code', () => {
		render(CodeView, { lines, binary: 'Binary file · 24 KB', truncated: 'showing 1 of 2' });
		expect(screen.getByText('Binary file · 24 KB')).toBeInTheDocument();
		expect(screen.queryByText('fresh')).not.toBeInTheDocument();
		expect(screen.queryByText(/truncated/)).not.toBeInTheDocument();
	});

	it('caps its height only when told to', async () => {
		const { rerender } = render(CodeView, { lines });
		expect(row('fresh').parentElement!.style.maxHeight).toBe('');

		await rerender({ maxHeight: 320 });
		expect(row('fresh').parentElement!.style.maxHeight).toBe('320px');
	});
});
