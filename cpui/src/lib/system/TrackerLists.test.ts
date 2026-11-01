import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api, isInUse } from '$lib/api';
import type { TrackerStatus, TrackerType } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { anIntegration, choose } from '$test/fixtures/app.svelte';
import TrackerLists from './TrackerLists.svelte';

vi.mock('$lib/api', () => ({
	api: {
		integrations: {
			statuses: vi.fn(),
			types: vi.fn(),
			saveStatuses: vi.fn(),
			saveTypes: vi.fn(),
			sync: vi.fn()
		}
	},
	isInUse: vi.fn()
}));

const status = (over: Partial<TrackerStatus>): TrackerStatus => ({
	id: 's1',
	integrationId: 'i1',
	remoteId: '1',
	name: 'To Do',
	remoteCategory: 'new',
	position: 0,
	isDone: false,
	...over
});
const type = (over: Partial<TrackerType>): TrackerType => ({
	id: 'y1',
	integrationId: 'i1',
	remoteId: '1',
	name: 'Bug',
	changeType: 'fix',
	position: 0,
	...over
});

const jira = anIntegration({ id: 'i1', name: 'Jira', port: 'ticketsource', kind: 'jira' });
const own = anIntegration({ id: 'i1', name: 'Apothecary', port: 'ticketsource', kind: 'internal' });

const statuses = () => within(screen.getByRole('region', { name: 'Statuses' }));
const types = () => within(screen.getByRole('region', { name: 'Types' }));
/** Renders the lists and waits for them to load. */
const setup = async (integration: typeof jira) => {
	render(TrackerLists, { integration });
	await screen.findByRole('region', { name: 'Statuses' });
};

beforeEach(() => {
	vi.resetAllMocks();
	toasts.list = [];
	vi.mocked(api.integrations.statuses).mockResolvedValue({
		statuses: [
			status({ id: 's1', name: 'To Do', flowStep: 'todo' }),
			status({ id: 's2', name: 'Done', position: 1 })
		],
		warnings: []
	});
	vi.mocked(api.integrations.types).mockResolvedValue([type({})]);
});

describe('TrackerLists, a remote tracker', () => {
	it("shows the tracker's rows with their names fixed", async () => {
		await setup(jira);
		expect(await statuses().findByText('To Do')).toBeInTheDocument();
		expect(statuses().queryByRole('textbox')).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: '+ Status' })).not.toBeInTheDocument();
		expect(types().getByText('Bug')).toBeInTheDocument();
	});

	it('saves Done and the flow step', async () => {
		vi.mocked(api.integrations.saveStatuses).mockImplementation(async (_, rows) => ({
			statuses: rows,
			warnings: []
		}));
		await setup(jira);
		await userEvent.click(screen.getByRole('checkbox', { name: 'Done is done' }));
		await choose('Flow step of Done', 'done');
		await userEvent.click(screen.getByRole('button', { name: 'Save statuses' }));

		expect(api.integrations.saveStatuses).toHaveBeenCalledWith(
			'i1',
			[
				expect.objectContaining({ id: 's1', flowStep: 'todo' }),
				expect.objectContaining({ id: 's2', isDone: true, flowStep: 'done' })
			],
			undefined
		);
		await waitFor(() => expect(toasts.list.at(-1)?.text).toBe('Jira saved'));
	});

	it('offers a step another status holds, but disabled', async () => {
		await setup(jira);
		await userEvent.click(screen.getByRole('combobox', { name: 'Flow step of Done' }));
		expect(await screen.findByRole('option', { name: 'to do' })).toHaveAttribute(
			'aria-disabled',
			'true'
		);
	});

	it('saves a change type', async () => {
		vi.mocked(api.integrations.saveTypes).mockImplementation(async (_, rows) => rows);
		await setup(jira);
		await types().findByText('Bug');
		await choose('Change type of Bug', 'chore');
		await userEvent.click(screen.getByRole('button', { name: 'Save types' }));
		expect(api.integrations.saveTypes).toHaveBeenCalledWith(
			'i1',
			[expect.objectContaining({ id: 'y1', changeType: 'chore' })],
			undefined
		);
	});

	it('refreshes the lists from the tracker, showing its warnings', async () => {
		await setup(jira);
		vi.mocked(api.integrations.statuses).mockResolvedValue({
			statuses: [],
			warnings: ['status_map entry “Old” matched nothing']
		});
		await userEvent.click(screen.getByRole('button', { name: 'Refresh from tracker' }));

		expect(api.integrations.statuses).toHaveBeenLastCalledWith('i1', true);
		expect(api.integrations.types).toHaveBeenLastCalledWith('i1', true);
		expect(await screen.findByText('status_map entry “Old” matched nothing')).toBeInTheDocument();
	});

	it('syncs its tickets now', async () => {
		vi.mocked(api.integrations.sync).mockResolvedValue({
			tickets: 5,
			synced: 4,
			gone: 1,
			failed: 0,
			busy: 0,
			rateLimited: 0
		});
		render(TrackerLists, { integration: jira });
		await userEvent.click(screen.getByRole('button', { name: 'Sync now' }));
		await waitFor(() =>
			expect(toasts.list.at(-1)?.text).toMatch(/^Synced 4 of 5 tickets · 1 gone/)
		);
	});
});

describe('TrackerLists, the internal tracker', () => {
	it('has no tracker to refresh from or sync', async () => {
		await setup(own);
		await statuses().findByDisplayValue('To Do');
		expect(screen.queryByRole('button', { name: 'Sync now' })).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Refresh from tracker' })).not.toBeInTheDocument();
	});

	it('adds, renames and reorders statuses', async () => {
		vi.mocked(api.integrations.saveStatuses).mockImplementation(async (_, rows) => ({
			statuses: rows,
			warnings: []
		}));
		await setup(own);
		const first = await statuses().findByDisplayValue('To Do');
		await userEvent.clear(first);
		await userEvent.type(first, 'Backlog');
		await userEvent.click(screen.getByRole('button', { name: '+ Status' }));
		const names = statuses().getAllByRole('textbox', { name: 'Name' });
		await userEvent.type(names[2], 'Doing');
		await userEvent.click(statuses().getAllByRole('button', { name: 'Move up' })[2]);
		await userEvent.click(screen.getByRole('button', { name: 'Save statuses' }));

		const rows = vi.mocked(api.integrations.saveStatuses).mock.calls[0][1];
		expect(rows.map((r) => [r.id, r.name, r.position])).toEqual([
			['s1', 'Backlog', 0],
			['', 'Doing', 1],
			['s2', 'Done', 2]
		]);
	});

	it('will not save an empty name', async () => {
		await setup(own);
		await statuses().findByDisplayValue('To Do');
		await userEvent.click(screen.getByRole('button', { name: '+ Status' }));
		expect(screen.getByText('Every row needs a name.')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Save statuses' })).toBeDisabled();
	});

	it('asks what the tickets of a deleted status move to, and resends with it', async () => {
		const inUse = new Error('in use');
		vi.mocked(api.integrations.saveStatuses)
			.mockRejectedValueOnce(inUse)
			.mockImplementation(async (_, rows) => ({ statuses: rows, warnings: [] }));
		vi.mocked(isInUse).mockImplementation((e) => e === inUse);
		await setup(own);
		await statuses().findByDisplayValue('To Do');
		await userEvent.click(statuses().getAllByRole('button', { name: 'Delete row' })[0]);
		await userEvent.click(screen.getByRole('button', { name: 'Save statuses' }));

		await screen.findByText(/Tickets still use what you deleted/);
		const save = screen.getByRole('button', { name: 'Move tickets and save' });
		expect(save).toBeDisabled();
		await choose('Replacement for To Do', 'Done');
		await userEvent.click(save);

		expect(api.integrations.saveStatuses).toHaveBeenLastCalledWith(
			'i1',
			[expect.objectContaining({ id: 's2' })],
			{ s1: 's2' }
		);
		await waitFor(() =>
			expect(screen.queryByText(/Tickets still use what you deleted/)).not.toBeInTheDocument()
		);
	});
});
