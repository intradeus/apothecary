import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { MergeRequestLive } from '$lib/domain';
import { aLiveMr } from '$test/fixtures/run-components';
import MrIsland, { mrHue, worstHue, type MrRow } from './MrIsland.svelte';

const row = (n: number, live?: Partial<MergeRequestLive>, over: Partial<MrRow> = {}): MrRow => ({
	key: `mr-${n}`,
	label: `!${n}`,
	title: `Change ${n}`,
	url: `https://git.test/mr/${n}`,
	live: live ? aLiveMr(live) : undefined,
	...over
});
const slot = (name: string): MrRow => ({ key: name, label: '—', title: `${name}: not opened yet` });

const pill = () => screen.getByRole('link', { name: /^!1/ }).closest('.pill-mr') as HTMLElement;
const panel = () => screen.queryByText('Merge requests', { selector: 'b' });

describe('mrHue', () => {
	it.each([
		['no status yet', undefined, 'muted'],
		['an unreachable repository', aLiveMr({ error: 'boom', mergeable: 'conflicts' }), 'muted'],
		['merged', aLiveMr({ state: 'merged', mergeable: 'conflicts' }), 'attention'],
		['conflicts', aLiveMr({ mergeable: 'conflicts' }), 'danger'],
		['failed CI', aLiveMr({ checks: { head: 'a', state: 'failure', items: [] } }), 'danger'],
		['conflicts on a draft', aLiveMr({ mergeable: 'conflicts', draft: true }), 'danger'],
		['a draft', aLiveMr({ draft: true }), 'muted'],
		[
			'a draft with CI running',
			aLiveMr({ draft: true, checks: { head: 'a', state: 'pending', items: [] } }),
			'muted'
		],
		['CI running', aLiveMr({ checks: { head: 'a', state: 'pending', items: [] } }), 'warning'],
		['all clear', aLiveMr(), 'success']
	])('%s', (_, mr, hue) => {
		expect(mrHue(mr)).toBe(hue);
	});

	// repoport.MRClosed comes through with mergeable clean.
	it('does not show a closed merge request as all clear', () => {
		expect(mrHue(aLiveMr({ state: 'closed' }))).not.toBe('success');
	});
});

describe('worstHue', () => {
	it('is muted for nothing', () => {
		expect(worstHue([])).toBe('muted');
	});

	it('ranks conflict over CI running over draft over merged over clear', () => {
		const clear = row(1, {});
		const merged = row(2, { state: 'merged' });
		const draft = row(3, { draft: true });
		const running = row(4, { checks: { head: 'a', state: 'pending', items: [] } });
		const conflict = row(5, { mergeable: 'conflicts' });

		expect(worstHue([clear])).toBe('success');
		expect(worstHue([clear, merged])).toBe('attention');
		expect(worstHue([clear, merged, draft])).toBe('muted');
		expect(worstHue([clear, merged, draft, running])).toBe('warning');
		expect(worstHue([clear, merged, draft, running, conflict])).toBe('danger');
	});
});

describe('MrIsland', () => {
	it('says there is none when nothing is opened', () => {
		render(MrIsland, { rows: [] });
		expect(screen.getByText('No merge request yet')).toBeInTheDocument();
	});

	it('does not expand over slots that are still empty', async () => {
		render(MrIsland, { rows: [slot('repo')] });
		const none = screen.getByText('No merge request yet');
		expect(none).toHaveAttribute('title', 'repo: not opened yet');

		await userEvent.hover(none);

		expect(panel()).not.toBeInTheDocument();
	});

	describe('the pill', () => {
		it('names one merge request and links it', () => {
			render(MrIsland, { rows: [row(1, {})] });

			expect(screen.getByText('Merge request')).toBeInTheDocument();
			const chip = screen.getByRole('link', { name: /^!1/ });
			expect(chip).toHaveAttribute('href', 'https://git.test/mr/1');
			expect(chip).toHaveAttribute('target', '_blank');
			expect(chip).toHaveAttribute('title', 'Change 1');
			expect(screen.queryByText(/^\+/)).not.toBeInTheDocument();
		});

		it('counts the rest and dots the worst of them', () => {
			const rows = [
				row(1, { mergeable: 'conflicts' }),
				row(2, { state: 'merged' }),
				row(3, { checks: { head: 'a', state: 'pending', items: [] } }),
				slot('docs')
			];
			render(MrIsland, { rows });

			expect(screen.getByText('Merge requests')).toBeInTheDocument();
			const more = screen.getByText('+3');
			// The primary's conflict is on its own chip: the dot is the rest's.
			expect(more.querySelector('.live-dot')).toHaveClass('hue-warning');
		});
	});

	describe('opening', () => {
		it('stays shut until asked', () => {
			render(MrIsland, { rows: [row(1, {})] });
			expect(panel()).not.toBeInTheDocument();
		});

		it('opens on hover', async () => {
			render(MrIsland, { rows: [row(1, {})] });
			await userEvent.hover(pill());
			expect(panel()).toBeInTheDocument();
		});

		it('opens on focus', async () => {
			render(MrIsland, { rows: [row(1, {})] });
			await userEvent.tab();
			expect(screen.getByRole('link', { name: /^!1/ })).toHaveFocus();
			expect(panel()).toBeInTheDocument();
		});

		describe('and closing', () => {
			const user = () => userEvent.setup({ advanceTimers: vi.advanceTimersByTime });

			beforeEach(() => {
				vi.useFakeTimers();
			});
			afterEach(() => {
				vi.useRealTimers();
			});

			it('pours back after the pointer leaves, then is gone', async () => {
				render(MrIsland, { rows: [row(1, {})] });
				const u = user();
				await u.hover(pill());
				await u.unhover(pill());

				expect(panel()?.closest('.mr-panel')).toHaveClass('closing');

				vi.advanceTimersByTime(330);
				await vi.waitFor(() => expect(panel()).not.toBeInTheDocument());
			});

			it('stays open when the pointer returns while it closes', async () => {
				render(MrIsland, { rows: [row(1, {})] });
				const u = user();
				await u.hover(pill());
				await u.unhover(pill());
				await u.hover(pill());

				vi.advanceTimersByTime(1000);
				await vi.advanceTimersByTimeAsync(0);

				expect(panel()).toBeInTheDocument();
				expect(panel()?.closest('.mr-panel')).not.toHaveClass('closing');
			});

			it('closes on Escape', async () => {
				render(MrIsland, { rows: [row(1, {})] });
				const u = user();
				await u.hover(pill());

				await u.keyboard('{Escape}');
				vi.advanceTimersByTime(330);

				await vi.waitFor(() => expect(panel()).not.toBeInTheDocument());
			});

			it('stops listening for Escape once closed', async () => {
				const off = vi.spyOn(window, 'removeEventListener');
				render(MrIsland, { rows: [row(1, {})] });
				const u = user();
				await u.hover(pill());
				await u.keyboard('{Escape}');

				await vi.waitFor(() => expect(off).toHaveBeenCalledWith('keydown', expect.any(Function)));
			});

			it('closes when focus leaves, and not while it moves inside', async () => {
				render(MrIsland, { rows: [row(1, {}), row(2, {})] });
				const outside = document.body.appendChild(document.createElement('button'));
				const u = user();
				await u.tab();
				await u.tab();
				expect(screen.getByRole('link', { name: /^Open ↗/ })).toHaveFocus();
				expect(panel()?.closest('.mr-panel')).not.toHaveClass('closing');

				outside.focus();
				await vi.waitFor(() => expect(panel()?.closest('.mr-panel')).toHaveClass('closing'));
				outside.remove();
			});
		});
	});

	describe('the island', () => {
		// `target` is also a mount option, so these go under `props`.
		async function open(props: { rows: MrRow[]; target?: string; multiRepo?: boolean }) {
			render(MrIsland, { props });
			await userEvent.hover(pill());
			return panel()!.closest('.mr-panel') as HTMLElement;
		}
		/** The list row showing this title. */
		const rowOf = (island: HTMLElement, title: string) =>
			within(island).getByText(title).closest('.row') as HTMLElement;

		it('heads with the count and where they land', async () => {
			const island = await open({
				rows: [row(1, {}), row(2, {})],
				target: 'ledger-api → main'
			});

			expect(within(island).getByText('2')).toBeInTheDocument();
			expect(within(island).getByText('ledger-api → main')).toBeInTheDocument();
		});

		it('warns of conflicts, and only then', async () => {
			const island = await open({
				rows: [row(1, { mergeable: 'conflicts' }), row(2, { mergeable: 'conflicts' }), row(3, {})]
			});
			expect(within(island).getByText(/^2 conflict/)).toBeInTheDocument();
		});

		it('stays quiet without conflicts', async () => {
			const island = await open({ rows: [row(1, {})] });
			expect(within(island).queryByText(/^\d+ conflict/)).not.toBeInTheDocument();
		});

		it('gives the primary Open, a conflict Resolve, the others an arrow', async () => {
			const island = await open({
				rows: [row(1, { mergeable: 'conflicts' }), row(2, { mergeable: 'conflicts' }), row(3, {})]
			});

			const primary = within(rowOf(island, 'Change 1')).getByRole('link');
			expect(primary).toHaveTextContent('Open');
			expect(primary).toHaveAttribute('href', 'https://git.test/mr/1');

			expect(
				within(rowOf(island, 'Change 2')).getByRole('link', { name: 'Resolve' })
			).toHaveAttribute('href', 'https://git.test/mr/2');
			expect(within(rowOf(island, 'Change 3')).getByRole('link', { name: 'Open' })).toHaveAttribute(
				'href',
				'https://git.test/mr/3'
			);
		});

		it('shows each one its live status', async () => {
			const island = await open({ rows: [row(1, { mergeableNow: true })] });
			expect(within(rowOf(island, 'Change 1')).getByText('ready to merge')).toBeInTheDocument();
		});

		it('says a slot is not opened yet and offers nothing to click', async () => {
			const island = await open({ rows: [row(1, {}), slot('docs')] });
			const empty = rowOf(island, 'docs: not opened yet');

			expect(within(empty).getByText('not opened yet')).toBeInTheDocument();
			expect(within(empty).queryByRole('link')).not.toBeInTheDocument();
			expect(empty.querySelector('.live-dot')).toHaveClass('hollow');
		});

		it('says nothing yet for an opened one before its status arrives', async () => {
			const island = await open({ rows: [row(1)] });
			expect(within(island).queryByText('not opened yet')).not.toBeInTheDocument();
		});

		it('dots each row in its own hue', async () => {
			const island = await open({
				rows: [row(1, { mergeable: 'conflicts' }), row(2, { state: 'merged' })]
			});

			expect(rowOf(island, 'Change 1').querySelector('.live-dot')).toHaveClass('hue-danger');
			expect(rowOf(island, 'Change 2').querySelector('.live-dot')).toHaveClass('hue-attention');
		});

		it('names the repo on a multi-repo run', async () => {
			const island = await open({ rows: [row(1, { repo: 'docs' })], multiRepo: true });
			expect(within(island).getByText('docs')).toBeInTheDocument();
		});

		it('leaves the repo out otherwise', async () => {
			const island = await open({ rows: [row(1, { repo: 'docs' })] });
			expect(within(island).queryByText('docs')).not.toBeInTheDocument();
		});
	});
});
