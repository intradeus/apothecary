import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Automation, Project, TicketRecord, TicketRow } from '$lib/domain';
import { confirmState } from '$lib/ui/confirm.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { aTicket, aTicketRow } from '$test/fixtures';
import { aFlow, anIntegration, aWorkshop, choose, confirmed } from '$test/fixtures/app.svelte';
import WithHeader from '$test/fixtures/WithHeader.svelte';
import AutomationsTab from './AutomationsTab.svelte';

vi.mock('$lib/api', () => ({
	api: {
		automations: { list: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn() },
		flows: { list: vi.fn(), get: vi.fn(), binding: vi.fn() },
		projects: { get: vi.fn(), tickets: vi.fn() },
		tickets: { get: vi.fn() },
		integrations: { list: vi.fn(), models: vi.fn() },
		skillsets: { list: vi.fn(), versions: vi.fn() }
	}
}));

const project = aWorkshop({
	id: 'p1',
	defaultFlow: 'builder',
	repos: [{ name: 'api', url: 'x', defaultBranch: 'main' }] as Project['repos'],
	ticketSourceId: 'j1'
});
const jira = anIntegration({ id: 'j1', name: 'Jira', kind: 'jira', port: 'ticketsource' });
const auto = (over: Partial<Automation> = {}): Automation =>
	({
		id: 'a1',
		projectId: 'p1',
		name: 'Nightly',
		kind: 'tracker_query',
		query: 'label=ai',
		flow: 'builder',
		params: {},
		enabled: true,
		...over
	}) as Automation;
const record = (id: string, key: string, title: string): TicketRecord => ({
	ticket: aTicket({ id, sourceKey: key, title }),
	key,
	workshops: []
});

interface Over {
	tickets?: TicketRow[];
	integrations?: ReturnType<typeof anIntegration>[] | null;
}
function setup(rows: Automation[] = [], { tickets = [], ...over }: Over = {}) {
	vi.mocked(api.automations.list).mockResolvedValue(rows);
	vi.mocked(api.projects.tickets).mockResolvedValue({ tickets, next: '' });
	return render(WithHeader, {
		props: {
			page: AutomationsTab,
			props: { project, integrations: [jira], ...over }
		}
	});
}
const openNew = async () => {
	await userEvent.click(screen.getAllByRole('button', { name: /New automation/ })[0]);
	return screen.findByRole('dialog', { name: 'New automation' });
};
const create = () => screen.getByRole('button', { name: 'Create automation' });
const sent = () => vi.mocked(api.automations.create).mock.calls.at(-1)![1];
const edited = () => vi.mocked(api.automations.update).mock.calls.at(-1)![2];
const ADVANCED = /Advanced · parameters/;
const kind = (name: string) => userEvent.click(screen.getByRole('radio', { name }));

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.automations.create).mockImplementation(async (_, d) => auto({ name: d.name }));
	vi.mocked(api.automations.update).mockImplementation(async (_, __, d) => auto({ name: d.name }));
	vi.mocked(api.flows.list).mockResolvedValue([
		aFlow({ name: 'builder', operatesOn: 'ticket' }),
		aFlow({ name: 'other', operatesOn: 'ticket' }),
		aFlow({ name: 'reviewer', operatesOn: 'merge_request' }),
		aFlow({ name: 'workspace-only', operatesOn: 'nothing', builtin: true })
	]);
	vi.mocked(api.flows.get).mockResolvedValue(aFlow());
	vi.mocked(api.flows.binding).mockResolvedValue(null as never);
	vi.mocked(api.projects.get).mockResolvedValue(project);
	vi.mocked(api.integrations.list).mockResolvedValue([]);
	vi.mocked(api.skillsets.list).mockResolvedValue([]);
	vi.mocked(api.projects.tickets).mockResolvedValue({ tickets: [], next: '' });
	vi.mocked(api.tickets.get).mockRejectedValue(new Error('not found'));
	toasts.list = [];
	confirmState.current = null;
});

describe('AutomationsTab, listing', () => {
	it('shows a skeleton first', () => {
		vi.mocked(api.automations.list).mockReturnValue(new Promise(() => {}));
		render(WithHeader, {
			props: {
				page: AutomationsTab,
				props: { project, integrations: [] }
			}
		});
		expect(screen.queryByText('No automations yet')).not.toBeInTheDocument();
	});

	it('invites the first one', async () => {
		setup();
		expect(await screen.findByText('No automations yet')).toBeInTheDocument();
		expect(api.automations.list).toHaveBeenCalledWith('p1');
	});

	it('offers a retry when loading fails', async () => {
		vi.mocked(api.automations.list).mockRejectedValueOnce(new Error('down'));
		vi.mocked(api.automations.list).mockResolvedValueOnce([]);
		render(WithHeader, {
			props: { page: AutomationsTab, props: { project, integrations: [] } }
		});

		expect(await screen.findByText('Couldn’t load automations')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('No automations yet')).toBeInTheDocument();
	});

	it('sums up a tracker rule', async () => {
		setup([auto()]);
		expect(await screen.findByText('Nightly')).toBeInTheDocument();
		expect(screen.getByText('tracker query')).toBeInTheDocument();
		expect(screen.getByText(/Jira \(jira\) · label=ai/)).toBeInTheDocument();
	});

	it('names the built-in tracker, or the source by role while integrations load', async () => {
		const own = anIntegration({
			id: 'j1',
			name: 'Apothecary',
			kind: 'internal',
			port: 'ticketsource'
		});
		const { unmount } = setup([auto({ query: '' })], { integrations: [own] });
		expect(await screen.findByText('Apothecary (built-in tracker)')).toBeInTheDocument();
		unmount();
		setup([auto({ query: '' })], { integrations: null });
		expect(await screen.findByText('the ticket source')).toBeInTheDocument();
	});

	it('sums up a schedule, with the ticket by its record', async () => {
		vi.mocked(api.tickets.get).mockResolvedValue(record('t1', 'PROJ-1', 'Daily sweep'));
		setup([auto({ kind: 'cron', schedule: '0 9 * * *', ticketId: 't1' })]);
		expect(await screen.findByText(/0 9 \* \* \* · Daily sweep/)).toBeInTheDocument();
		expect(api.tickets.get).toHaveBeenCalledWith('t1');
		expect(api.projects.tickets).toHaveBeenCalledWith('p1', { done: 'open', limit: 100 });
	});

	it('sums up a schedule on nothing as one with no ticket', async () => {
		setup([
			auto({ kind: 'cron', schedule: '0 3 * * *', ticketId: undefined, flow: 'workspace-only' })
		]);
		expect(await screen.findByText(/0 3 \* \* \* · no ticket/)).toBeInTheDocument();
		expect(api.tickets.get).not.toHaveBeenCalled();
	});

	it('sums up a schedule by the ticket id when its record cannot be read', async () => {
		vi.mocked(api.automations.list).mockResolvedValue([
			auto({ kind: 'cron', schedule: '0 9 * * *', ticketId: 't9' })
		]);
		vi.mocked(api.projects.tickets).mockRejectedValue(new Error('rate limited'));
		render(WithHeader, {
			props: { page: AutomationsTab, props: { project, integrations: [jira] } }
		});
		expect(await screen.findByText(/0 9 \* \* \* · t9/)).toBeInTheDocument();
		await waitFor(() => expect(api.tickets.get).toHaveBeenCalledWith('t9'));
	});

	it('sums up a merge request rule', async () => {
		setup([auto({ kind: 'merge_request' })]);
		expect(
			await screen.findByText(/merge requests others open in the primary repository/)
		).toBeInTheDocument();
	});

	it('counts parameter overrides', async () => {
		setup([auto({ params: { a: '1', b: '2' } })]);
		expect(await screen.findByText(/2 overrides/)).toBeInTheDocument();
	});

	it('says what the watcher last did', async () => {
		vi.useFakeTimers({ now: new Date('2026-10-31T12:00:00Z'), toFake: ['Date'] });
		setup([
			auto({ id: 'a', lastFiredAt: '2026-10-31T10:00:00Z', lastError: 'jira 500' }),
			auto({ id: 'b', name: 'Polled', lastPolledAt: '2026-10-31T11:00:00Z' }),
			auto({ id: 'c', name: 'Idle' })
		]);
		expect(await screen.findByText('fired 2h ago')).toBeInTheDocument();
		expect(screen.getByText('polled 1h ago')).toBeInTheDocument();
		expect(screen.getByText('never ran')).toBeInTheDocument();
		expect(screen.getByText('jira 500')).toBeInTheDocument();
		vi.useRealTimers();
	});
});

describe('AutomationsTab, a new rule', () => {
	it("opens from the page header, saying it polls the workshop's source", async () => {
		setup();
		await screen.findByText('No automations yet');
		const dialog = await openNew();
		expect(
			within(dialog).getByText(/Polls Jira \(jira\), the workshop’s ticket source\./)
		).toBeInTheDocument();
		expect(within(dialog).queryByRole('combobox', { name: 'Tracker' })).not.toBeInTheDocument();
	});

	it('waits for a name', async () => {
		setup();
		await screen.findByText('No automations yet');
		await openNew();
		expect(create()).toBeDisabled();
		await userEvent.type(screen.getByLabelText('Name'), '   ');
		expect(create()).toBeDisabled();
	});

	it('creates a tracker rule on the default flow, enabled', async () => {
		setup();
		await screen.findByText('No automations yet');
		await openNew();
		await userEvent.type(screen.getByLabelText('Name'), 'Triage');
		await userEvent.type(screen.getByLabelText('Query'), 'label=ai');
		await userEvent.click(create());

		expect(sent()).toMatchObject({
			name: 'Triage',
			kind: 'tracker_query',
			query: 'label=ai',
			flow: 'builder',
			enabled: true
		});
		await waitFor(() => expect(toasts.list.at(-1)?.text).toBe('Automation Triage saved'));
		expect(api.automations.list).toHaveBeenCalledTimes(2);
		await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
	});

	it('sends only what a schedule takes, the ticket by its key', async () => {
		setup([], {
			tickets: [
				aTicketRow({ key: 'PROJ-1', ticketId: 't1', title: 'Sweep' }),
				aTicketRow({ key: 'PROJ-2', ticketId: '', title: 'Never run' })
			]
		});
		await screen.findByText('No automations yet');
		await openNew();
		await userEvent.type(screen.getByLabelText('Name'), 'Daily');
		await userEvent.type(screen.getByLabelText('Query'), 'left over');
		await kind('schedule');
		await userEvent.type(screen.getByLabelText('Schedule'), '0 9 * * *');
		// An issue with no record yet is pickable: saving makes its record.
		await choose('Ticket', 'PROJ-2 · Never run');
		await userEvent.click(create());

		expect(sent()).toMatchObject({ kind: 'cron', schedule: '0 9 * * *', ticketKey: 'PROJ-2' });
		expect(sent().ticketId).toBeUndefined();
		expect(sent().query).toBeUndefined();
	});

	it('schedules a flow whose runs are started on nothing, with no ticket', async () => {
		setup([], { tickets: [aTicketRow({ key: 'PROJ-1', ticketId: 't1', title: 'Sweep' })] });
		await screen.findByText('No automations yet');
		await openNew();
		await userEvent.type(screen.getByLabelText('Name'), 'Nightly chore');
		await kind('schedule');
		await userEvent.type(screen.getByLabelText('Schedule'), '0 3 * * *');
		await choose('Ticket', 'PROJ-1 · Sweep');
		await kind('Nothing');

		// No ticket to pick, and only the flows that start on nothing.
		expect(screen.queryByRole('combobox', { name: 'Ticket' })).not.toBeInTheDocument();
		expect(
			screen.getByText(/A slot is skipped while the previous run is still going/)
		).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.getByRole('combobox', { name: 'Flow' })).toHaveTextContent('workspace-only')
		);
		await userEvent.click(create());

		expect(sent()).toMatchObject({ kind: 'cron', schedule: '0 3 * * *', flow: 'workspace-only' });
		// The ticket picked before the switch does not travel.
		expect(sent().ticketKey).toBeUndefined();
		expect(sent().ticketId).toBeUndefined();
	});

	it('sends only what a merge request rule takes', async () => {
		setup();
		await screen.findByText('No automations yet');
		await openNew();
		await userEvent.type(screen.getByLabelText('Name'), 'Review');
		await kind('schedule');
		await userEvent.type(screen.getByLabelText('Schedule'), '0 9 * * *');
		await kind('merge requests');
		await choose('Repository', 'api');
		await waitFor(() => expect(create()).toBeEnabled());
		await userEvent.click(create());

		expect(sent()).toMatchObject({ kind: 'merge_request', repo: 'api' });
		expect(sent().schedule).toBeUndefined();
	});

	it('sends the primary repository as none', async () => {
		setup();
		await screen.findByText('No automations yet');
		await openNew();
		await userEvent.type(screen.getByLabelText('Name'), 'Review');
		await kind('merge requests');
		await userEvent.click(create());
		expect(sent().repo).toBeUndefined();
	});

	it('says when there is no ticket to schedule', async () => {
		setup([], { tickets: [] });
		await screen.findByText('No automations yet');
		await openNew();
		await kind('schedule');
		expect(await screen.findByText(/No ticket to pick/)).toBeInTheDocument();
	});

	it('copes with integrations and tickets still loading', async () => {
		vi.mocked(api.automations.list).mockResolvedValue([]);
		vi.mocked(api.projects.tickets).mockReturnValue(new Promise(() => {}));
		render(WithHeader, {
			props: { page: AutomationsTab, props: { project, integrations: null } }
		});
		await screen.findByText('No automations yet');
		await openNew();
		expect(screen.getByRole('dialog')).toBeInTheDocument();
	});

	it('starts paused when asked', async () => {
		setup();
		await screen.findByText('No automations yet');
		await openNew();
		await userEvent.type(screen.getByLabelText('Name'), 'Triage');
		await userEvent.click(screen.getByRole('switch', { name: /fires from the next pass/ }));
		await userEvent.click(create());
		expect(sent().enabled).toBe(false);
	});

	it('runs the flow picked, not the default', async () => {
		setup();
		await screen.findByText('No automations yet');
		await openNew();
		await userEvent.type(screen.getByLabelText('Name'), 'Triage');
		await waitFor(() => expect(api.flows.list).toHaveBeenCalled());
		await screen.findByRole('combobox', { name: 'Flow' });
		await choose('Flow', 'other');
		await userEvent.click(create());
		expect(sent().flow).toBe('other');
	});

	it('shows a refusal in the form, and stays open', async () => {
		vi.mocked(api.automations.create).mockRejectedValue(new Error('bad cron'));
		setup();
		await screen.findByText('No automations yet');
		await openNew();
		await userEvent.type(screen.getByLabelText('Name'), 'Triage');
		await userEvent.click(create());

		expect(await screen.findByText('bad cron')).toBeInTheDocument();
		expect(screen.getByRole('dialog')).toBeInTheDocument();
		expect(create()).toBeEnabled();
	});

	it('cancels', async () => {
		setup();
		await screen.findByText('No automations yet');
		await openNew();
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
		expect(api.automations.create).not.toHaveBeenCalled();
	});

	// AutomationDraft has no skillsets, and the engine's Automation neither.
	it('does not offer skillsets it cannot keep', async () => {
		setup();
		await screen.findByText('No automations yet');
		await openNew();
		await screen.findByText(ADVANCED);
		expect(screen.queryByText('Skillsets')).not.toBeInTheDocument();
	});
});

describe('AutomationsTab, editing', () => {
	const open = async () => {
		await userEvent.click(await screen.findByRole('button', { name: 'Edit' }));
		return screen.findByRole('dialog', { name: 'Edit Nightly' });
	};

	it('starts from the rule', async () => {
		setup([auto()]);
		await open();
		expect(screen.getByLabelText('Name')).toHaveValue('Nightly');
		expect(screen.getByLabelText('Query')).toHaveValue('label=ai');
		expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled();
	});

	it('saves over it, keeping its overrides', async () => {
		setup([auto({ params: { model: 'x' } })]);
		await open();
		await userEvent.type(screen.getByLabelText('Name'), ' 2');
		await userEvent.click(screen.getByRole('button', { name: 'Save' }));

		expect(api.automations.update).toHaveBeenCalledWith('p1', 'a1', expect.anything());
		expect(edited()).toMatchObject({ name: 'Nightly 2', flow: 'builder' });
		expect(api.automations.create).not.toHaveBeenCalled();
	});

	it('keeps a flow other than the default', async () => {
		setup([auto({ flow: 'other' })]);
		await open();
		await screen.findByRole('combobox', { name: 'Flow' });
		await userEvent.click(screen.getByRole('button', { name: 'Save' }));
		expect(edited().flow).toBe('other');
	});

	describe('a schedule', () => {
		const cron = () =>
			auto({ kind: 'cron', query: undefined, schedule: '0 9 * * *', ticketId: 't1' });
		const open = async () => {
			await userEvent.click(await screen.findByRole('button', { name: 'Edit' }));
			await screen.findByRole('dialog', { name: 'Edit Nightly' });
			return screen.findByRole('combobox', { name: 'Ticket' });
		};

		it('keeps its ticket, even one no longer open', async () => {
			vi.mocked(api.tickets.get).mockResolvedValue(record('t1', 'PROJ-1', 'Done already'));
			setup([cron()], { tickets: [aTicketRow({ key: 'PROJ-2', title: 'Open' })] });
			await open();
			await waitFor(() =>
				expect(screen.getByRole('combobox', { name: 'Ticket' })).toHaveTextContent('PROJ-1')
			);
			await userEvent.click(screen.getByRole('button', { name: 'Save' }));
			expect(edited()).toMatchObject({ ticketId: 't1' });
			expect(edited().ticketKey).toBeUndefined();
		});

		const two = () =>
			setup([cron()], {
				tickets: [
					aTicketRow({ key: 'PROJ-1', ticketId: 't1', title: 'Sweep' }),
					aTicketRow({ key: 'PROJ-2', ticketId: '', title: 'Other' })
				]
			});

		it('moves to another ticket by its key', async () => {
			vi.mocked(api.tickets.get).mockResolvedValue(record('t1', 'PROJ-1', 'Sweep'));
			two();
			await open();
			await choose('Ticket', 'PROJ-2 · Other');
			await userEvent.click(screen.getByRole('button', { name: 'Save' }));
			expect(edited()).toMatchObject({ ticketKey: 'PROJ-2' });
			expect(edited().ticketId).toBeUndefined();
		});

		it('picked back, keeps its own record', async () => {
			vi.mocked(api.tickets.get).mockResolvedValue(record('t1', 'PROJ-1', 'Sweep'));
			two();
			await open();
			await choose('Ticket', 'PROJ-2 · Other');
			await choose('Ticket', 'PROJ-1 · Sweep');
			await userEvent.click(screen.getByRole('button', { name: 'Save' }));
			expect(edited()).toMatchObject({ ticketId: 't1' });
			expect(edited().ticketKey).toBeUndefined();
		});

		it('opens one on nothing as such, and can be given a ticket', async () => {
			setup(
				[auto({ kind: 'cron', query: undefined, schedule: '0 3 * * *', flow: 'workspace-only' })],
				{ tickets: [aTicketRow({ key: 'PROJ-1', ticketId: 't1', title: 'Sweep' })] }
			);
			await userEvent.click(await screen.findByRole('button', { name: 'Edit' }));
			await screen.findByRole('dialog', { name: 'Edit Nightly' });
			expect(screen.getByRole('radio', { name: 'Nothing' })).toBeChecked();
			expect(screen.queryByRole('combobox', { name: 'Ticket' })).not.toBeInTheDocument();

			await kind('A ticket');
			expect(await screen.findByRole('combobox', { name: 'Ticket' })).toBeInTheDocument();
		});

		it('clears its ticket', async () => {
			vi.mocked(api.tickets.get).mockResolvedValue(record('t1', 'PROJ-1', 'Sweep'));
			setup([cron()], { tickets: [aTicketRow({ key: 'PROJ-1', ticketId: 't1', title: 'Sweep' })] });
			await open();
			await choose('Ticket', '—');
			await userEvent.click(screen.getByRole('button', { name: 'Save' }));
			expect(edited().ticketId).toBeUndefined();
			expect(edited().ticketKey).toBeUndefined();
		});
	});
});

describe('AutomationsTab, the row', () => {
	const toggle = () => screen.findByRole('switch');

	it('pauses a rule, sending the whole rule back', async () => {
		setup([auto({ params: { model: 'x' } })]);
		await userEvent.click(await toggle());

		expect(edited()).toMatchObject({
			name: 'Nightly',
			kind: 'tracker_query',
			query: 'label=ai',
			params: { model: 'x' },
			enabled: false
		});
		expect(api.automations.list).toHaveBeenCalledTimes(2);
	});

	it('says what the toggle will do', async () => {
		setup([auto({ enabled: false })]);
		expect(await toggle()).toHaveAttribute('title', 'Paused — click to enable');
	});

	it('shows a failure to toggle as a toast', async () => {
		vi.mocked(api.automations.update).mockRejectedValue(new Error('locked'));
		setup([auto()]);
		await userEvent.click(await toggle());
		await waitFor(() => expect(toasts.list.at(-1)?.text).toBe('locked'));
	});

	it('does nothing when deletion is declined', async () => {
		setup([auto()]);
		await userEvent.click(await screen.findByRole('button', { name: 'Delete…' }));
		await confirmed(false);
		await Promise.resolve();
		expect(api.automations.remove).not.toHaveBeenCalled();
	});

	it('deletes and reloads', async () => {
		vi.mocked(api.automations.remove).mockResolvedValue(undefined as never);
		setup([auto()]);
		await userEvent.click(await screen.findByRole('button', { name: 'Delete…' }));
		const asked = await confirmed(true);

		expect(asked.title).toBe('Delete automation Nightly?');
		await waitFor(() => expect(api.automations.remove).toHaveBeenCalledWith('p1', 'a1'));
		await waitFor(() => expect(toasts.list.at(-1)?.text).toBe('Automation Nightly deleted'));
	});

	it('shows a failure to delete as a toast', async () => {
		vi.mocked(api.automations.remove).mockRejectedValue(new Error('locked'));
		setup([auto()]);
		await userEvent.click(await screen.findByRole('button', { name: 'Delete…' }));
		await confirmed(true);
		await waitFor(() => expect(toasts.list.at(-1)?.text).toBe('locked'));
	});
});
