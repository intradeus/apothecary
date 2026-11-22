import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { session } from '$lib/app/session.svelte';
import type { Me, TicketDetail, TicketVersion, TicketVersionSummary } from '$lib/domain';
import { confirmDialog } from '$lib/ui/confirm.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { aTicketDetail } from '$test/fixtures';
import TicketHistory from './TicketHistory.svelte';

vi.mock('$lib/api', () => ({
	api: {
		tickets: { versions: vi.fn(), version: vi.fn(), restore: vi.fn() },
		projects: { ticketTypes: vi.fn() }
	}
}));
vi.mock('$lib/ui/confirm.svelte', () => ({ confirmDialog: vi.fn() }));

const { versions, version, restore } = vi.mocked(api.tickets);
const NOW = Date.now();
const minutesAgo = (m: number) => new Date(NOW - m * 60_000).toISOString();

const internal = aTicketDetail({
	internal: true,
	title: 'Now title',
	body: 'line one\nline two',
	labels: ['api'],
	source: { id: 'o', kind: 'internal', name: 'Apothecary' }
});

const summary = (seq: number, over: Partial<TicketVersionSummary> = {}): TicketVersionSummary => ({
	seq,
	title: 'T',
	author: { kind: 'user', id: 'u1', handle: 'lu' },
	createdAt: minutesAgo(seq),
	changed: [],
	oldest: false,
	...over
});

const old: TicketVersion = {
	...summary(1),
	title: 'Old title',
	goal: 'line one\nline zero',
	baseBranch: '',
	changeType: 'fix',
	typeId: 't-bug',
	labels: []
};

function setup(over: { ticket?: TicketDetail; canWrite?: boolean } = {}) {
	const onrestored = vi.fn();
	render(TicketHistory, {
		projectId: 'p1',
		ticket: over.ticket ?? internal,
		canWrite: over.canWrite ?? true,
		onrestored
	});
	return onrestored;
}

const row = (seq: number) => screen.findByRole('button', { name: new RegExp(`^v${seq}\\b`) });

beforeEach(() => {
	vi.resetAllMocks();
	toasts.list = [];
	session.me = { user: { id: 'u1', handle: 'lu', name: 'Lu', role: 'member' } } as Me;
	vi.mocked(api.projects.ticketTypes).mockResolvedValue([
		{
			id: 't-bug',
			integrationId: 'o',
			remoteId: '',
			name: 'Bug',
			changeType: 'fix',
			position: 0
		}
	]);
});
afterEach(() => {
	session.me = null;
});

describe('TicketHistory, its states', () => {
	it('shows a skeleton while loading', () => {
		versions.mockReturnValue(new Promise(() => {}));
		setup();
		expect(screen.getByLabelText('Loading')).toBeInTheDocument();
		expect(versions).toHaveBeenCalledWith('p1', 'PROJ-7');
	});

	it('says when there are no versions', async () => {
		versions.mockResolvedValue([]);
		setup();
		expect(await screen.findByText('No history yet')).toBeInTheDocument();
	});

	it('shows a failed load, and loads again on retry', async () => {
		versions.mockRejectedValueOnce(new Error('store down')).mockResolvedValue([summary(1)]);
		setup();
		expect(await screen.findByText('Could not load the history')).toBeInTheDocument();
		expect(screen.getByText('store down')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await row(1)).toBeInTheDocument();
	});
});

describe('TicketHistory, the list', () => {
	it('names each author, when, and what changed', async () => {
		versions.mockResolvedValue([
			summary(8, { changed: ['title', 'goal'] }),
			summary(7, { author: { kind: 'user', id: 'u2', handle: 'ann' }, changed: ['labels'] }),
			summary(6, { author: { kind: 'user', id: 'u3' }, changed: ['mystery'] }),
			summary(5, { author: { kind: 'assistant', id: 'u1' }, changed: ['change_type', 'type'] }),
			summary(4, { author: { kind: 'run', id: '01RUNID000000000000000ABCD' } }),
			summary(3, { author: { kind: 'sync', id: 's' }, changed: ['base_branch'] }),
			summary(2, { author: { kind: 'user', id: 'u1' }, restoredFrom: 1 }),
			summary(1, { author: { kind: 'backfill', id: '' }, oldest: true })
		]);
		setup();
		expect(await row(8)).toHaveTextContent(/you.*8m ago.*title.*goal/);
		expect(await row(7)).toHaveTextContent(/@ann.*labels/);
		expect(await row(6)).toHaveTextContent(/someone.*mystery/);
		expect(await row(5)).toHaveTextContent(/assistant \(saved by you\).*change type.*type/);
		expect(await row(4)).toHaveTextContent('run');
		expect(screen.getByRole('link', { name: /ABCD/i })).toHaveAttribute(
			'href',
			'/runs/01RUNID000000000000000ABCD'
		);
		expect(await row(3)).toHaveTextContent(/tracker sync.*base branch/);
		expect(await row(2)).toHaveTextContent('restored from v1');
		expect(await row(1)).toHaveTextContent(/history began.*oldest kept/);
	});

	it('names a type by its id when the source’s types cannot be read', async () => {
		vi.mocked(api.projects.ticketTypes).mockRejectedValue(new Error('nope'));
		versions.mockResolvedValue([summary(2), summary(1)]);
		version.mockResolvedValue(old);
		setup();
		await userEvent.click(await row(1));
		const cmp = await screen.findByLabelText('v1 against the ticket now');
		expect(cmp).toHaveTextContent('t-bug');
	});
});

describe('TicketHistory, a version against the ticket now', () => {
	it('shows title and goal as a line diff, the rest as before/after rows', async () => {
		versions.mockResolvedValue([summary(2), summary(1)]);
		version.mockResolvedValue(old);
		setup();
		await userEvent.click(await row(1));
		expect(version).toHaveBeenCalledWith('p1', 'PROJ-7', 1);
		const cmp = await screen.findByLabelText('v1 against the ticket now');
		expect(cmp).toHaveTextContent('−Now title');
		expect(cmp).toHaveTextContent('+Old title');
		expect(cmp).toHaveTextContent('−line two');
		expect(cmp).toHaveTextContent('+line zero');
		expect(cmp).toHaveTextContent(/change type\s*now\s*feat\s*v1\s*fix/);
		expect(cmp).toHaveTextContent(/type\s*now\s*Story\s*v1\s*Bug/);
		expect(cmp).toHaveTextContent(/labels\s*now\s*api\s*v1\s*—/);
		expect(cmp).toHaveTextContent(/base branch\s*now\s*feat\/idempotency\s*v1\s*—/);
		expect(screen.getByRole('button', { name: /^v1/ })).toHaveAttribute('aria-pressed', 'true');
	});

	it('says when a version holds what the ticket holds now', async () => {
		versions.mockResolvedValue([summary(2), summary(1)]);
		version.mockResolvedValue({
			...old,
			title: internal.title,
			goal: internal.body,
			baseBranch: internal.baseBranch,
			changeType: internal.changeType,
			typeId: internal.typeId,
			labels: internal.labels
		});
		setup();
		await userEvent.click(await row(1));
		expect(await screen.findByText('Same content as the ticket now.')).toBeInTheDocument();
	});

	it('offers no restore on the newest version', async () => {
		versions.mockResolvedValue([summary(2), summary(1)]);
		version.mockResolvedValue({ ...old, seq: 2 });
		setup();
		await userEvent.click(await row(2));
		expect(await screen.findByText('This is the ticket now.')).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Restore this version' })).toBeNull();
	});

	it('shows a version that fails to load, with a retry', async () => {
		versions.mockResolvedValue([summary(2), summary(1)]);
		version.mockRejectedValueOnce(new Error('gone')).mockResolvedValue(old);
		setup();
		await userEvent.click(await row(1));
		expect(await screen.findByText('Could not load this version')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByLabelText('v1 against the ticket now')).toBeInTheDocument();
		expect(version).toHaveBeenCalledTimes(2);
	});

	it('shows a skeleton while the version loads', async () => {
		versions.mockResolvedValue([summary(2), summary(1)]);
		version.mockReturnValue(new Promise(() => {}));
		setup();
		await userEvent.click(await row(1));
		expect(screen.getByLabelText('Loading')).toBeInTheDocument();
	});

	it('keeps only the last one picked: restore posts the version shown', async () => {
		versions.mockResolvedValue([summary(3), summary(2), summary(1)]);
		const first = Promise.withResolvers<TicketVersion>();
		version
			.mockReturnValueOnce(first.promise)
			.mockResolvedValueOnce({ ...old, seq: 2, title: 'Middle title' });
		restore.mockResolvedValue(internal);
		vi.mocked(confirmDialog).mockResolvedValue(true);
		setup();
		await userEvent.click(await row(1));
		await userEvent.click(await row(2));
		await screen.findByLabelText('v2 against the ticket now');
		first.resolve(old);
		await first.promise;
		await tick();
		const cmp = screen.getByLabelText('v2 against the ticket now');
		expect(cmp).toHaveTextContent('+Middle title');
		expect(cmp).not.toHaveTextContent('+Old title');
		await userEvent.click(screen.getByRole('button', { name: 'Restore this version' }));
		await waitFor(() => expect(restore).toHaveBeenCalledWith('p1', 'PROJ-7', 2));
	});

	it('ignores an earlier pick’s failure that answers late', async () => {
		versions.mockResolvedValue([summary(3), summary(2), summary(1)]);
		const first = Promise.withResolvers<TicketVersion>();
		version.mockReturnValueOnce(first.promise).mockResolvedValueOnce({ ...old, seq: 2 });
		setup();
		await userEvent.click(await row(1));
		await userEvent.click(await row(2));
		await screen.findByLabelText('v2 against the ticket now');
		first.reject(new Error('gone'));
		await first.promise.catch(() => {});
		await tick();
		expect(screen.queryByText('Could not load this version')).toBeNull();
		expect(screen.getByLabelText('v2 against the ticket now')).toBeInTheDocument();
	});
});

describe('TicketHistory, restoring', () => {
	beforeEach(() => {
		versions.mockResolvedValue([summary(2), summary(1)]);
		version.mockResolvedValue(old);
	});
	const pickOld = async () => {
		await userEvent.click(await row(1));
		return screen.findByRole('button', { name: 'Restore this version' });
	};

	it('restores once confirmed, says so and hands the ticket to the page', async () => {
		const restored = { ...internal, title: 'Old title' };
		restore.mockResolvedValue(restored);
		vi.mocked(confirmDialog).mockResolvedValue(true);
		const onrestored = setup();
		await userEvent.click(await pickOld());
		expect(confirmDialog).toHaveBeenCalledWith(
			expect.objectContaining({ title: 'Restore v1?', action: 'Restore' })
		);
		await waitFor(() => expect(onrestored).toHaveBeenCalledWith(restored));
		expect(restore).toHaveBeenCalledWith('p1', 'PROJ-7', 1);
		expect(toasts.list.at(-1)?.text).toBe('Restored v1');
	});

	it('does nothing when the confirm is declined', async () => {
		vi.mocked(confirmDialog).mockResolvedValue(false);
		const onrestored = setup();
		await userEvent.click(await pickOld());
		await waitFor(() => expect(confirmDialog).toHaveBeenCalled());
		expect(restore).not.toHaveBeenCalled();
		expect(onrestored).not.toHaveBeenCalled();
	});

	it('shows a refused restore beside the button, until dismissed', async () => {
		restore.mockRejectedValue(new Error('base branch is not valid'));
		vi.mocked(confirmDialog).mockResolvedValue(true);
		const onrestored = setup();
		await userEvent.click(await pickOld());
		const card = (await screen.findByText('Could not restore v1')).closest(
			'[role=alert]'
		) as HTMLElement;
		expect(within(card).getByText('base branch is not valid')).toBeInTheDocument();
		expect(onrestored).not.toHaveBeenCalled();
		await userEvent.click(screen.getByRole('button', { name: /dismiss/i }));
		expect(screen.queryByText('Could not restore v1')).toBeNull();
	});

	it('is disabled on a remote record, naming its tracker', async () => {
		setup({ ticket: aTicketDetail() });
		const btn = await pickOld();
		expect(btn).toBeDisabled();
		const why = 'Change it on Jira: its content belongs to the tracker.';
		expect(btn).toHaveAttribute('title', why);
		expect(screen.getByText(why)).toBeInTheDocument();
	});

	it('is disabled without ticket:write', async () => {
		setup({ canWrite: false });
		const btn = await pickOld();
		expect(btn).toBeDisabled();
		expect(btn).toHaveAttribute('title', 'Your role in this workshop does not allow this');
	});
});
