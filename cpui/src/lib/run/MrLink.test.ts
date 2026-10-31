import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { RunMergeRequest } from '$lib/domain';
import { aRun } from '$test/fixtures';
import MrLink from './MrLink.svelte';

const anMr = (over: Partial<RunMergeRequest> = {}) =>
	({
		repo: 'repo',
		url: 'https://git.test/mr/20',
		iid: 20,
		branch: 'feat/a',
		target: 'main',
		...over
	}) as RunMergeRequest;

describe('MrLink', () => {
	it('shows a dash before any merge request is opened', () => {
		render(MrLink, { run: aRun() });

		expect(screen.getByTitle('No merge request yet')).toHaveTextContent('—');
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});

	it('links one merge request, in a new tab', () => {
		render(MrLink, { run: aRun({ mrs: [anMr()] }) });

		const link = screen.getByRole('link', { name: /^MR/ });
		expect(link).toHaveAttribute('href', 'https://git.test/mr/20');
		expect(link).toHaveAttribute('target', '_blank');
		expect(link).toHaveAttribute('rel', 'noreferrer');
	});

	// Runs from before multi-repo carry only mrUrl.
	it('still links an older run that has no list', () => {
		render(MrLink, { run: aRun({ mrUrl: 'https://git.test/mr/7' }) });

		expect(screen.getByRole('link', { name: /^MR/ })).toHaveAttribute(
			'href',
			'https://git.test/mr/7'
		);
	});

	it('counts several and links the first', () => {
		const mrs = [anMr(), anMr({ url: 'https://git.test/mr/21', iid: 21 })];
		render(MrLink, { run: aRun({ mrs }) });

		expect(screen.getByRole('link', { name: /MR ×2/ })).toHaveAttribute(
			'href',
			'https://git.test/mr/20'
		);
	});

	it('gives each merge request its own link in detail', () => {
		const mrs = [
			anMr(),
			anMr({ url: 'https://git.test/mr/21', iid: 21, repo: 'docs', branch: 'feat/b', target: '' })
		];
		render(MrLink, { run: aRun({ mrs }), detail: true });

		const first = screen.getByRole('link', { name: /!20/ });
		expect(first).toHaveAttribute('href', 'https://git.test/mr/20');
		expect(first).toHaveAttribute('title', 'feat/a → main');

		const second = screen.getByRole('link', { name: /!21/ });
		expect(second).toHaveAttribute('href', 'https://git.test/mr/21');
		expect(second).toHaveAttribute('title', 'docs: feat/b');
	});

	it('keeps a single merge request to the short link in detail', () => {
		render(MrLink, { run: aRun({ mrs: [anMr()] }), detail: true });

		expect(screen.getByRole('link', { name: /^MR/ })).toBeInTheDocument();
		expect(screen.queryByRole('link', { name: /!20/ })).not.toBeInTheDocument();
	});
});
