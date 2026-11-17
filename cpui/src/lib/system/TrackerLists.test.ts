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
			saveTypes: vi.fn()
		}
	},
	isInUse: vi.fn()
}));

const status = (over: Partial<TrackerStatus>): TrackerStatus => ({
	id: 's1',
	integrationId: 'i1',
	remoteId: '1',
	name: 'To Do',
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

const jira = anIntegration({ id: 'remote', name: 'Jira', port: 'ticketsource', kind: 'jira' });
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
	vi.mocked(api.integrations.statuses).mockImplementation(async (id) => ({
		statuses: [
			status({ id: 's1', name: 'To Do' }),
			status({ id: 's2', name: 'Done', position: 1 })
		],
		readOnly: id === 'remote'
	}));
	vi.mocked(api.integrations.types).mockResolvedValue([type({})]);
});

describe('TrackerLists, a remote tracker', () => {
	it("shows the tracker's statuses read-only", async () => {
		await setup(jira);
		expect(await statuses().findByText('To Do')).toBeInTheDocument();
		expect(statuses().getByText(/read live/)).toBeInTheDocument();
		expect(statuses().queryByRole('textbox')).not.toBeInTheDocument();
		expect(statuses().queryByRole('button', { name: 'Move up' })).not.toBeInTheDocument();
		expect(statuses().queryByRole('button', { name: 'Delete row' })).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: '+ Status' })).not.toBeInTheDocument();
		expect(screen.getByRole('checkbox', { name: 'Done is done' })).toBeDisabled();
		expect(screen.queryByRole('button', { name: 'Save statuses' })).not.toBeInTheDocument();
		expect(types().getByText('Bug')).toBeInTheDocument();
		expect(types().queryByRole('textbox')).not.toBeInTheDocument();
	});

	it('saves a change type', async () => {
		vi.mocked(api.integrations.saveTypes).mockImplementation(async (_, rows) => rows);
		await setup(jira);
		await types().findByText('Bug');
		await choose('Change type of Bug', 'chore');
		await userEvent.click(screen.getByRole('button', { name: 'Save types' }));
		expect(api.integrations.saveTypes).toHaveBeenCalledWith(
			'remote',
			[expect.objectContaining({ id: 'y1', changeType: 'chore' })],
			undefined
		);
		await waitFor(() => expect(toasts.list.at(-1)?.text).toBe('Jira saved'));
	});

	it('refreshes the lists from the tracker', async () => {
		await setup(jira);
		vi.mocked(api.integrations.statuses).mockResolvedValue({
			statuses: [status({ id: 's3', name: 'Shipped' })],
			readOnly: true
		});
		await userEvent.click(screen.getByRole('button', { name: 'Refresh from tracker' }));

		expect(api.integrations.statuses).toHaveBeenLastCalledWith('remote', true);
		expect(api.integrations.types).toHaveBeenLastCalledWith('remote', true);
		expect(await statuses().findByText('Shipped')).toBeInTheDocument();
	});

	it('shows a list that would not load', async () => {
		vi.mocked(api.integrations.statuses).mockRejectedValue(new Error('rate limited'));
		render(TrackerLists, { integration: jira });
		expect(await screen.findByText('rate limited')).toBeInTheDocument();
	});
});

describe('TrackerLists, the internal tracker', () => {
	it('has no tracker to refresh from', async () => {
		await setup(own);
		await statuses().findByDisplayValue('To Do');
		expect(screen.queryByRole('button', { name: 'Refresh from tracker' })).not.toBeInTheDocument();
	});

	it('adds, renames and reorders statuses', async () => {
		vi.mocked(api.integrations.saveStatuses).mockImplementation(async (_, rows) => ({
			statuses: rows,
			readOnly: false
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
			.mockImplementation(async (_, rows) => ({ statuses: rows, readOnly: false }));
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
