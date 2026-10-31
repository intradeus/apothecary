import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { text } from '$test/helpers';
import DocHead from './DocHead.svelte';

const props = { path: 'docs/plan.md', raisedAt: '2026-10-31T12:00:00Z' };

describe('DocHead', () => {
	it('names the file, and says it is the snapshot, not the live file', () => {
		render(DocHead, props);
		expect(screen.getByText('docs/plan.md')).toBeInTheDocument();
		expect(screen.getByText(/^snapshot from \d\d:\d\d · not the live file$/)).toBeInTheDocument();
	});

	it('says the file is as reviewed once the review is answered', () => {
		render(DocHead, { ...props, reviewed: true });
		expect(screen.getByText(/^as reviewed · snapshot from \d\d:\d\d$/)).toBeInTheDocument();
		expect(screen.queryByText(/not the live file/)).not.toBeInTheDocument();
	});

	it('gives the snapshot’s full date on hover', () => {
		render(DocHead, props);
		expect(screen.getByText(/^snapshot from/)).toHaveAttribute(
			'title',
			expect.stringContaining('2026')
		);
	});

	it('puts what can be done with the file in the head', () => {
		render(DocHead, { ...props, children: text('Comment on this file') });
		expect(screen.getByText('Comment on this file')).toBeInTheDocument();
	});
});
