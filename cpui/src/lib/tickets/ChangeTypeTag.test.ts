import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { ChangeType } from '$lib/domain';
import ChangeTypeTag from './ChangeTypeTag.svelte';

describe('ChangeTypeTag', () => {
	it.each<[ChangeType, string]>([
		['feat', 'tint-attention'],
		['fix', 'tint-danger'],
		['hotfix', 'tint-hot'],
		['perf', 'tint-warning'],
		['docs', 'tint-accent-2'],
		['chore', 'tint-muted']
	])('tints %s with %s', (type, tint) => {
		render(ChangeTypeTag, { type });
		expect(screen.getByText(type)).toHaveClass(tint);
	});

	it('says what the type is', () => {
		render(ChangeTypeTag, { type: 'fix' });
		expect(screen.getByText('fix')).toHaveAttribute('title', 'Change type: fix');
	});

	it("carries the tracker's own word for an imported issue", () => {
		render(ChangeTypeTag, { type: 'fix', sourceType: 'Bug' });
		expect(screen.getByText('fix')).toHaveAttribute(
			'title',
			'Change type: fix — the tracker calls it “Bug”'
		);
	});
});
