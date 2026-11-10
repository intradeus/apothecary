import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { MergeRequestRow } from '$lib/domain';
import { anOpenMr } from '$test/fixtures/run-components';
import MrMeta from './MrMeta.svelte';

const row = (over: Partial<MergeRequestRow> = {}, mr = anOpenMr()): MergeRequestRow => ({
	mr,
	runs: 0,
	...over
});

describe('MrMeta', () => {
	it('names the author and nothing else for a plain one', () => {
		render(MrMeta, { row: row() });
		expect(screen.getByText('ada')).toBeInTheDocument();
		expect(screen.queryByText(/processed/)).not.toBeInTheDocument();
	});

	it('counts the runs it went through', () => {
		const { rerender } = render(MrMeta, { row: row({ runs: 1 }) });
		expect(screen.getByText('processed by apothecary once')).toHaveAttribute(
			'title',
			expect.stringContaining('started on it')
		);
		void rerender({ row: row({ runs: 3 }) });
		return Promise.resolve().then(() =>
			expect(screen.getByText('processed by apothecary 3 times')).toBeInTheDocument()
		);
	});

	it('marks a draft and a fork', () => {
		render(MrMeta, { row: row({}, anOpenMr({ draft: true, fork: true })) });
		expect(screen.getByText('draft')).toBeInTheDocument();
		expect(screen.getByText('from a fork')).toBeInTheDocument();
	});

	it('links the one it is stacked on, saying where it lands', () => {
		const stackedOn = { iid: 1234, url: 'https://x/9', title: 'Base', branch: 'feat/base' };
		render(MrMeta, { row: row({ stackedOn }) });
		const pill = screen.getByRole('link', { name: 'stacked on !1234' });
		expect(pill).toHaveAttribute('href', 'https://x/9');
		expect(pill.getAttribute('title')).toContain('Merge !1234 first.');
	});
});
