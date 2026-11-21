import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { session } from '$lib/app/session.svelte';
import type { DashboardVersion, Me } from '$lib/domain';
import { confirmDialog } from '$lib/ui/confirm.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import {
	aDashboard,
	aDashboardVersion,
	aDashboardVersionSummary,
	aPanel
} from '$test/fixtures/analytics';
import DashboardHistory from './DashboardHistory.svelte';

vi.mock('$lib/api', () => ({
	api: { analytics: { dashboards: { versions: vi.fn(), version: vi.fn(), restore: vi.fn() } } }
}));
vi.mock('$lib/ui/confirm.svelte', () => ({ confirmDialog: vi.fn() }));

const { versions, version, restore } = vi.mocked(api.analytics.dashboards);
const current = aDashboard({
	name: 'Mine',
	filters: ['flow_name', 'model'],
	panels: [
		aPanel({ id: 'k', title: 'Kept' }),
		aPanel({ id: 'c', title: 'Cost now', viz: 'bar', width: 12 }),
		aPanel({ id: 'n', title: 'Newcomer' })
	]
});
const NOW = Date.now();
const minutesAgo = (m: number) => new Date(NOW - m * 60_000).toISOString();

function setup() {
	const onrestored = vi.fn();
	render(DashboardHistory, { dashboard: current, onrestored });
	return onrestored;
}

const row = (seq: number) => screen.findByRole('button', { name: new RegExp(`^v${seq}\\b`) });

beforeEach(() => {
	vi.resetAllMocks();
	toasts.list = [];
	session.me = { user: { id: 'u1', handle: 'lu', name: 'Lu', role: 'member' } } as Me;
});
afterEach(() => {
	session.me = null;
});

describe('DashboardHistory', () => {
	it('shows a skeleton while loading', () => {
		versions.mockReturnValue(new Promise(() => {}));
		setup();
		expect(screen.getByLabelText('Loading')).toBeInTheDocument();
	});

	it('says when there are no versions', async () => {
		versions.mockResolvedValue([]);
		setup();
		expect(await screen.findByText('No versions yet')).toBeInTheDocument();
		expect(versions).toHaveBeenCalledWith('d1');
	});

	it('shows a failure in place and retries', async () => {
		versions.mockRejectedValueOnce(new Error('boom')).mockResolvedValue([]);
		setup();
		expect(await screen.findByText('boom')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('No versions yet')).toBeInTheDocument();
	});

	it('lists the versions: author, time, what changed, restores', async () => {
		versions.mockResolvedValue([
			aDashboardVersionSummary({
				seq: 7,
				createdAt: minutesAgo(3),
				changed: ['name', 'filters', 'panels'],
				panelChanges: { added: 2, removed: 1, changed: 3 }
			}),
			aDashboardVersionSummary({
				seq: 6,
				author: { kind: 'assistant', id: 'u1' },
				changed: ['panels'],
				panelChanges: { added: 0, removed: 0, changed: 0, reordered: true }
			}),
			aDashboardVersionSummary({ seq: 5, author: { kind: 'user', id: 'u2', handle: 'mo' } }),
			aDashboardVersionSummary({ seq: 4, author: { kind: 'user', id: 'u3' }, restoredFrom: 2 }),
			aDashboardVersionSummary({ seq: 3, author: { kind: 'run', id: 'r' } }),
			aDashboardVersionSummary({ seq: 2, author: { kind: 'sync', id: 's' } }),
			aDashboardVersionSummary({ seq: 1, author: { kind: 'backfill', id: '' }, oldest: true })
		]);
		setup();
		expect(await row(7)).toHaveTextContent(
			/^v7\s*you\s*3m ago\s*current\s*name\s*filters\s*\+2 panels\s*−1\s*~3 changed$/
		);
		expect(await row(6)).toHaveTextContent(
			/^v6\s*assistant \(saved by you\)\s*\S+ ago\s*panels reordered$/
		);
		expect(await row(5)).toHaveTextContent(/^v5\s*mo\s/);
		expect(await row(4)).toHaveTextContent(/^v4\s*u3\s.*restored from v2$/);
		expect(await row(3)).toHaveTextContent(/^v3\s*a run\s/);
		expect(await row(2)).toHaveTextContent(/^v2\s*sync\s/);
		expect(await row(1)).toHaveTextContent(/^v1\s*history began\s/);
	});

	describe('a version picked', () => {
		const old: DashboardVersion = aDashboardVersion({
			seq: 1,
			name: 'Old',
			filters: ['flow_name', 'stage_id'],
			panels: [
				aPanel({ id: 'g', title: 'Gone' }),
				aPanel({ id: 'k', title: 'Kept' }),
				aPanel({ id: 'c', title: 'Cost then', viz: 'line' })
			]
		});

		beforeEach(() => {
			versions.mockResolvedValue([
				aDashboardVersionSummary({ seq: 2 }),
				aDashboardVersionSummary({ seq: 1 })
			]);
		});

		it('reads it against the current dashboard', async () => {
			version.mockResolvedValue(old);
			setup();
			await userEvent.click(await row(1));
			expect(version).toHaveBeenCalledWith('d1', 1);
			const cmp = await screen.findByRole('heading', { name: 'v1 against the current dashboard' });
			const items = within(cmp.parentElement!)
				.getAllByRole('listitem')
				.map((li) => li.textContent?.trim());
			expect(items).toEqual([
				'Name: “Old” here, “Mine” now',
				'Filters only here: stage_id',
				'Filters only now: model',
				'Panels only here: Gone',
				'Panels only now: Newcomer',
				'Cost then: title, chart, width differ'
			]);
			expect(await row(1)).toHaveAttribute('aria-pressed', 'true');
		});

		it('names the order a restore brings back, even when it is all that differs', async () => {
			version.mockResolvedValue(
				aDashboardVersion({
					...current,
					seq: 1,
					filters: [...current.filters].reverse(),
					panels: [...current.panels].reverse()
				})
			);
			setup();
			await userEvent.click(await row(1));
			const cmp = await screen.findByRole('heading', { name: 'v1 against the current dashboard' });
			const items = within(cmp.parentElement!)
				.getAllByRole('listitem')
				.map((li) => li.textContent?.trim());
			expect(items).toEqual([
				'Filter order here: model, flow_name',
				'Panel order here: Newcomer, Cost now, Kept'
			]);
			expect(screen.queryByText('Same as the current dashboard')).toBeNull();
		});

		it('says when it equals the current dashboard, with no restore on the newest', async () => {
			version.mockResolvedValue(aDashboardVersion({ ...current, seq: 2 }));
			setup();
			await userEvent.click(await row(2));
			expect(await screen.findByText('Same as the current dashboard')).toBeInTheDocument();
			expect(
				screen.queryByRole('button', { name: 'Restore this version' })
			).not.toBeInTheDocument();
		});

		it('shows a skeleton while it loads, keeping only the last one picked', async () => {
			const first = Promise.withResolvers<DashboardVersion>();
			version
				.mockReturnValueOnce(first.promise)
				.mockResolvedValueOnce(aDashboardVersion({ ...current, seq: 2 }));
			setup();
			await userEvent.click(await row(1));
			expect(screen.getByLabelText('Loading')).toBeInTheDocument();
			await userEvent.click(await row(2));
			await screen.findByText('Same as the current dashboard');
			first.resolve(old);
			await waitFor(() =>
				expect(screen.getByRole('heading', { level: 3 })).toHaveTextContent('v2')
			);
		});

		it('ignores an earlier pick’s failure that answers late', async () => {
			const first = Promise.withResolvers<DashboardVersion>();
			version
				.mockReturnValueOnce(first.promise)
				.mockResolvedValueOnce(aDashboardVersion({ ...current, seq: 2 }));
			setup();
			await userEvent.click(await row(1));
			await userEvent.click(await row(2));
			await screen.findByText('Same as the current dashboard');
			first.reject(new Error('gone'));
			await first.promise.catch(() => {});
			await tick();
			expect(screen.queryByText('gone')).toBeNull();
			expect(screen.getByText('Same as the current dashboard')).toBeInTheDocument();
		});

		it('shows why it could not load and retries', async () => {
			version.mockRejectedValueOnce(new Error('gone')).mockResolvedValue(old);
			setup();
			await userEvent.click(await row(1));
			expect(await screen.findByText('gone')).toBeInTheDocument();
			await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
			expect(await screen.findByText('Panels only here: Gone')).toBeInTheDocument();
		});

		it('restores it once confirmed', async () => {
			version.mockResolvedValue(old);
			const back = aDashboard({ name: 'Old' });
			restore.mockResolvedValue(back);
			vi.mocked(confirmDialog).mockResolvedValue(true);
			const onrestored = setup();
			await userEvent.click(await row(1));
			await userEvent.click(await screen.findByRole('button', { name: 'Restore this version' }));
			expect(confirmDialog).toHaveBeenCalledWith(
				expect.objectContaining({
					title: 'Restore v1?',
					body: expect.stringContaining('<b>Mine</b> gets the name, filters and panels of v1'),
					action: 'Restore this version'
				})
			);
			expect(restore).toHaveBeenCalledWith('d1', 1);
			await waitFor(() => expect(onrestored).toHaveBeenCalledWith(back));
			expect(toasts.list.at(-1)?.text).toBe('Version restored');
		});

		it('does nothing when declined', async () => {
			version.mockResolvedValue(old);
			vi.mocked(confirmDialog).mockResolvedValue(false);
			const onrestored = setup();
			await userEvent.click(await row(1));
			await userEvent.click(await screen.findByRole('button', { name: 'Restore this version' }));
			expect(restore).not.toHaveBeenCalled();
			expect(onrestored).not.toHaveBeenCalled();
		});

		it('shows why the restore failed beside the button', async () => {
			version.mockResolvedValue(old);
			restore.mockRejectedValue(new Error('a built-in dashboard is read-only'));
			vi.mocked(confirmDialog).mockResolvedValue(true);
			const onrestored = setup();
			await userEvent.click(await row(1));
			await userEvent.click(await screen.findByRole('button', { name: 'Restore this version' }));
			expect(await screen.findByText('Couldn’t restore this version')).toBeInTheDocument();
			expect(screen.getByText('a built-in dashboard is read-only')).toBeInTheDocument();
			expect(onrestored).not.toHaveBeenCalled();
			expect(screen.getByRole('button', { name: 'Restore this version' })).toBeEnabled();
			await userEvent.click(screen.getByRole('button', { name: /Dismiss/ }));
			expect(screen.queryByText('Couldn’t restore this version')).not.toBeInTheDocument();
		});
	});
});
