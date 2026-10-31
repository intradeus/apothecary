import { render, screen } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { MergeRequestChecks, MergeRequestLive } from '$lib/domain';
import { aLiveMr } from '$test/fixtures/run-components';
import MrStatus from './MrStatus.svelte';

const NOW = new Date('2026-10-31T12:00:00Z');
const checks = (over: Partial<MergeRequestChecks> = {}): MergeRequestChecks => ({
	head: 'abc',
	state: 'success',
	items: [],
	...over
});
const show = (
	over: Partial<MergeRequestLive> = {},
	props: { long?: boolean; repo?: boolean } = {}
) => render(MrStatus, { mr: aLiveMr(over), ...props });
const hasClass = (text: string, cls: string) => expect(screen.getByText(text)).toHaveClass(cls);

describe('MrStatus', () => {
	beforeEach(() => {
		vi.useFakeTimers({ toFake: ['Date'] });
		vi.setSystemTime(NOW);
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	describe('the merge state', () => {
		it.each([
			['ready to merge', { mergeableNow: true }, 'hue-success'],
			['no conflicts', { mergeable: 'clean' }, 'hue-success'],
			['conflicts', { mergeable: 'conflicts' }, 'hue-danger'],
			['checking…', { mergeable: 'unknown' }, 'faint'],
			['checking…', { mergeable: '' }, 'faint'],
			['merged', { state: 'merged' }, 'hue-success'],
			['repository unreachable', { error: 'boom' }, 'faint']
		] as const)('reads "%s" for %o', (word, over, hue) => {
			show(over);
			hasClass(word, hue);
		});

		it('puts an unreachable repository before everything else', () => {
			show({ error: 'boom', state: 'merged', mergeable: 'conflicts', mergeableNow: true });

			expect(screen.getByText('repository unreachable')).toBeInTheDocument();
			expect(screen.queryByText('merged')).not.toBeInTheDocument();
			expect(screen.queryByText('conflicts')).not.toBeInTheDocument();
		});

		it('puts merged before a stale conflict', () => {
			show({ state: 'merged', mergeable: 'conflicts' });

			expect(screen.getByText('merged')).toBeInTheDocument();
			expect(screen.queryByText('conflicts')).not.toBeInTheDocument();
		});

		it('never calls a conflicted one ready', () => {
			show({ mergeable: 'conflicts', mergeableNow: true });

			expect(screen.getByText('conflicts')).toBeInTheDocument();
			expect(screen.queryByText('ready to merge')).not.toBeInTheDocument();
		});

		it('spells conflicts and a pending answer out when long', () => {
			const { unmount } = show({ mergeable: 'conflicts', target: 'develop' }, { long: true });
			expect(screen.getByText('conflicts with develop')).toBeInTheDocument();
			unmount();

			const again = show({ mergeable: 'conflicts', target: undefined }, { long: true });
			expect(screen.getByText('conflicts with its target')).toBeInTheDocument();
			again.unmount();

			show({ mergeable: 'unknown' }, { long: true });
			expect(screen.getByText('mergeability pending')).toBeInTheDocument();
		});

		// The hosts report a closed one as mergeable (github.go leaves it clean).
		it('does not offer a closed merge request as mergeable', () => {
			show({ state: 'closed', mergeable: 'clean' });
			expect(screen.queryByText('no conflicts')).not.toBeInTheDocument();
		});

		it('says a merge request is closed, whatever else the host reports', () => {
			show({ state: 'closed', mergeable: 'conflicts', mergeableNow: true });
			expect(screen.getByText('closed')).toBeInTheDocument();
			expect(screen.queryByText('conflicts')).not.toBeInTheDocument();
		});
	});

	describe('the tooltip', () => {
		it('names the branch and where it lands', () => {
			const { container } = show({ branch: 'feat/a', target: 'main' });
			expect(container.querySelector('[title]')).toHaveAttribute('title', 'feat/a → main');
		});

		it('leaves the target out when there is none', () => {
			const { container } = show({ branch: 'feat/a', target: undefined });
			expect(container.querySelector('[title]')).toHaveAttribute('title', 'feat/a');
		});

		it('says it is merged', () => {
			const { container } = show({ branch: 'feat/a', target: 'main', state: 'merged' });
			expect(container.querySelector('[title]')).toHaveAttribute('title', 'feat/a → main merged');
		});

		it('carries the error when the repository cannot be reached', () => {
			const { container } = show({ error: 'gateway timeout' });
			expect(container.querySelector('[title]')).toHaveAttribute('title', 'gateway timeout');
		});
	});

	it('prefixes the repo name on a multi-repo run', () => {
		const { unmount } = show({ repo: 'docs' }, { repo: true });
		expect(screen.getByText('docs')).toBeInTheDocument();
		unmount();

		show({ repo: 'docs' });
		expect(screen.queryByText('docs')).not.toBeInTheDocument();
	});

	it('marks a draft while it is open', () => {
		const { unmount } = show({ draft: true });
		expect(screen.getByText('draft')).toBeInTheDocument();
		unmount();

		show({ draft: true, state: 'merged' });
		expect(screen.queryByText('draft')).not.toBeInTheDocument();
	});

	describe('stacked on another', () => {
		const stackedOn = { iid: 18, url: 'https://git.test/18', title: 'Base', branch: 'feat/base' };

		it('links the one underneath', () => {
			show({ stackedOn });

			const link = screen.getByRole('link', { name: 'stacked on !18' });
			expect(link).toHaveAttribute('href', 'https://git.test/18');
			expect(link).toHaveAttribute('target', '_blank');
			expect(link.getAttribute('title')).toContain('Base');
			expect(link.getAttribute('title')).toContain('feat/base');
		});

		it('says what to do first when long', () => {
			show({ stackedOn }, { long: true });

			expect(
				screen.getByRole('link', {
					name: 'stacked on !18 — merges into feat/base, merge !18 first'
				})
			).toBeInTheDocument();
		});

		it('drops it once merged', () => {
			show({ stackedOn, state: 'merged' });
			expect(screen.queryByRole('link')).not.toBeInTheDocument();
		});
	});

	describe('CI', () => {
		it.each([
			['CI passed', 'success', 'hue-success'],
			['CI running', 'pending', 'faint'],
			['CI failed', 'failure', 'hue-danger']
		] as const)('reads "%s" for %s', (word, state, hue) => {
			show({ checks: checks({ state }) });
			hasClass(word, hue);
		});

		it.each(['', 'neutral'] as const)('says nothing for a head whose state is "%s"', (state) => {
			show({ checks: checks({ state }) });
			expect(screen.queryByText(/^CI/)).not.toBeInTheDocument();
		});

		it('says nothing once it is not open', () => {
			show({ state: 'merged', checks: checks({ state: 'failure' }) });
			expect(screen.queryByText(/^CI/)).not.toBeInTheDocument();
		});

		it('says nothing when there are no checks', () => {
			show({ checks: undefined });
			expect(screen.queryByText(/^CI/)).not.toBeInTheDocument();
		});

		it('names the failed checks when long, and links the first of them', () => {
			show(
				{
					checks: checks({
						state: 'failure',
						items: [
							{ name: 'lint', state: 'success', url: 'https://ci.test/lint' },
							{ name: 'unit', state: 'failure', url: 'https://ci.test/unit' },
							{ name: 'e2e', state: 'failure' }
						]
					})
				},
				{ long: true }
			);

			const link = screen.getByRole('link', { name: 'CI failed: unit, e2e' });
			expect(link).toHaveAttribute('href', 'https://ci.test/unit');
			expect(link).toHaveAttribute('title', 'lint: success\nunit: failure\ne2e: failure');
		});

		it('falls back to any check with a link', () => {
			show(
				{
					checks: checks({
						state: 'failure',
						items: [
							{ name: 'unit', state: 'failure' },
							{ name: 'lint', state: 'success', url: 'https://ci.test/lint' }
						]
					})
				},
				{ long: true }
			);

			expect(screen.getByRole('link', { name: 'CI failed: unit' })).toHaveAttribute(
				'href',
				'https://ci.test/lint'
			);
		});

		it('is plain text when no check has a link, and always when short', () => {
			const items = [{ name: 'unit', state: 'success' as const, url: 'https://ci.test/unit' }];
			const { unmount } = show({ checks: checks({ items }) });
			expect(screen.getByText('CI passed')).toHaveAttribute('title', 'unit: success');
			expect(screen.queryByRole('link')).not.toBeInTheDocument();
			unmount();

			show({ checks: checks({ items: [{ name: 'unit', state: 'success' }] }) }, { long: true });
			expect(screen.getByText('CI passed')).toBeInTheDocument();
			expect(screen.queryByRole('link')).not.toBeInTheDocument();
		});
	});

	describe('approval', () => {
		it('says approved, with who when long', () => {
			const approval = { approved: true, by: ['ada', 'grace'] };
			const { unmount } = show({ approval });
			expect(screen.getByText('approved')).toHaveAttribute('title', 'ada, grace');
			unmount();

			show({ approval }, { long: true });
			expect(screen.getByText('approved by ada, grace')).toBeInTheDocument();
		});

		it('keeps it short when nobody is named', () => {
			show({ approval: { approved: true, by: [] } }, { long: true });
			expect(screen.getByText('approved')).toBeInTheDocument();
		});

		it('says nothing when not approved, or no longer open', () => {
			const { unmount } = show({ approval: { approved: false, by: ['ada'] } });
			expect(screen.queryByText(/approved/)).not.toBeInTheDocument();
			unmount();

			show({ approval: { approved: true, by: ['ada'] }, state: 'merged' });
			expect(screen.queryByText(/approved/)).not.toBeInTheDocument();
		});
	});

	describe('threads', () => {
		const lastCommentAt = new Date(NOW.getTime() - 5 * 60_000);

		it('counts them, singular and plural', () => {
			const { unmount } = show({ pendingThreads: 1 });
			expect(screen.getByText('· 1 thread')).toBeInTheDocument();
			unmount();

			show({ pendingThreads: 3 });
			expect(screen.getByText('· 3 threads')).toBeInTheDocument();
		});

		it('adds the last comment', () => {
			show({ pendingThreads: 2, lastCommentAt });
			expect(screen.getByText('· 2 threads · 5m ago')).toBeInTheDocument();
		});

		it('spells it out when long', () => {
			show({ pendingThreads: 1, lastCommentAt }, { long: true });
			expect(screen.getByText('1 unresolved thread · last 5m ago')).toBeInTheDocument();
		});

		it('says nothing with none, or once merged', () => {
			const { unmount } = show({ pendingThreads: 0, lastCommentAt });
			expect(screen.queryByText(/thread/)).not.toBeInTheDocument();
			unmount();

			show({ pendingThreads: 2, state: 'merged' });
			expect(screen.queryByText(/thread/)).not.toBeInTheDocument();
		});
	});
});
