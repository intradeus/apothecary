import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { goto } from '$app/navigation';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { aProject, aTicketRow } from '$test/fixtures';
import { anIntegration } from '$test/fixtures/app.svelte';
import { anOpenMr, bound } from '$test/fixtures/run-components';
import { deferred } from '$test/helpers';
import NewRunDialog from './NewRunDialog.svelte';

vi.mock('$lib/api', () => ({
	api: {
		projects: { tickets: vi.fn(), mergeRequests: vi.fn() },
		integrations: { list: vi.fn() }
	}
}));

const first = aTicketRow({ key: 'PROJ-1', title: 'First ticket' });
const second = aTicketRow({ key: 'PROJ-2', title: 'Second ticket' });
const page = (tickets: ReturnType<typeof aTicketRow>[]) => ({ tickets, next: '' });

function open(over: { projectId?: string } = {}) {
	const props = bound({ open: true, projectId: '', ...over });
	render(NewRunDialog, { props });
	return props;
}
const picker = () => screen.findByRole('combobox', { name: 'Ticket' });
const continueBtn = () => screen.getByRole('button', { name: 'Continue' });

describe('NewRunDialog', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		vi.mocked(api.projects.tickets).mockImplementation((pid) =>
			Promise.resolve(page(pid === 'p1' ? [first, second] : []))
		);
		vi.mocked(api.integrations.list).mockResolvedValue([
			anIntegration({ id: 'src', kind: 'internal', port: 'ticketsource' })
		]);
		fleet.projects = [
			aProject({ id: 'p1', name: 'ledger', ticketSourceId: 'src' }),
			aProject({ id: 'p2', name: 'docs', ticketSourceId: 'src' })
		];
	});

	it('loads nothing until opened', () => {
		render(NewRunDialog, { open: false });

		expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
		expect(api.projects.tickets).not.toHaveBeenCalled();
	});

	it('shows it is loading, with nothing to continue to', () => {
		vi.mocked(api.projects.tickets).mockReturnValue(deferred<never>().promise);
		open();

		expect(screen.getByRole('dialog', { name: 'New run' })).toBeInTheDocument();
		expect(screen.getByRole('status')).toHaveTextContent('Loading…');
		expect(continueBtn()).toBeDisabled();
	});

	it('picks the first ticket of the first workshop’s live list', async () => {
		open();

		expect(await picker()).toHaveTextContent('First ticket');
		expect(api.projects.tickets).toHaveBeenCalledWith('p1', { limit: 50 });
		expect(continueBtn()).toBeEnabled();
	});

	it('takes you to the ticket in its workshop, where its run is started', async () => {
		open();
		await picker();

		await userEvent.click(continueBtn());

		expect(goto).toHaveBeenCalledExactlyOnceWith('/w/p1/tickets/PROJ-1');
	});

	it('takes you to the ticket you pick', async () => {
		open();
		await userEvent.click(await picker());
		await userEvent.click(screen.getByRole('option', { name: /Second ticket/ }));

		await userEvent.click(continueBtn());

		expect(goto).toHaveBeenCalledExactlyOnceWith('/w/p1/tickets/PROJ-2');
	});

	it('shows each ticket with its key', async () => {
		open();
		await userEvent.click(await picker());

		expect(screen.getByRole('option', { name: /First ticket.*PROJ-1/ })).toBeInTheDocument();
	});

	it('reads another workshop’s list when it is picked', async () => {
		open();
		await picker();
		await userEvent.click(screen.getByRole('combobox', { name: 'Workshop' }));
		await userEvent.click(screen.getByRole('option', { name: 'docs' }));

		expect(await screen.findByRole('link', { name: /ticket/i })).toBeInTheDocument();
		expect(api.projects.tickets).toHaveBeenLastCalledWith('p2', { limit: 50 });
	});

	it('keeps to the page’s workshop, without asking which', async () => {
		open({ projectId: 'p1' });
		await picker();

		expect(api.projects.tickets).toHaveBeenCalledExactlyOnceWith('p1', { limit: 50 });
		expect(screen.queryByRole('combobox', { name: 'Workshop' })).not.toBeInTheDocument();
	});

	describe('with no tickets', () => {
		beforeEach(() => {
			vi.mocked(api.projects.tickets).mockResolvedValue(page([]));
		});

		it('sends you to write one, the workshop carried', async () => {
			open();

			expect(
				await screen.findByText('A run works on a ticket, and there is none yet. Write one first.')
			).toBeInTheDocument();
			expect(screen.getByRole('link', { name: /ticket/i })).toHaveAttribute(
				'href',
				'/tickets/new?project=p1'
			);
			expect(screen.queryByRole('button', { name: 'Continue' })).not.toBeInTheDocument();
		});

		it('offers no New ticket on a remote source', async () => {
			vi.mocked(api.integrations.list).mockResolvedValue([
				anIntegration({ id: 'src', kind: 'jira', port: 'ticketsource' })
			]);
			open();

			expect(
				await screen.findByText('A run works on a ticket, and there is none yet. Write one first.')
			).toBeInTheDocument();
			expect(screen.queryByRole('link', { name: /ticket/i })).not.toBeInTheDocument();
		});

		it('offers no New ticket when the integrations cannot be read', async () => {
			vi.mocked(api.integrations.list).mockRejectedValue(new Error('down'));
			open();

			await screen.findByText('A run works on a ticket, and there is none yet. Write one first.');
			expect(screen.queryByRole('link', { name: /ticket/i })).not.toBeInTheDocument();
		});
	});

	describe('when loading fails', () => {
		beforeEach(() => {
			vi.mocked(api.projects.tickets).mockRejectedValueOnce(new Error('engine down'));
		});

		it('says so, with nothing to continue to', async () => {
			open();

			expect(await screen.findByRole('alert')).toHaveTextContent('Could not load tickets');
			expect(screen.getByText('engine down')).toBeInTheDocument();
			expect(continueBtn()).toBeDisabled();
		});

		it('tries again', async () => {
			open();
			await screen.findByRole('alert');

			await userEvent.click(screen.getByRole('button', { name: /Retry/ }));

			expect(await picker()).toHaveTextContent('First ticket');
			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
			expect(api.projects.tickets).toHaveBeenCalledTimes(2);
		});
	});

	it('closes on Cancel', async () => {
		const props = open();
		await picker();

		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));

		expect(props.open).toBe(false);
		await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
		expect(goto).not.toHaveBeenCalled();
	});

	it('reads the list again each time it opens', async () => {
		const props = open();
		await picker();
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

		vi.mocked(api.projects.tickets).mockResolvedValue(page([second]));
		props.open = true;
		await tick();

		expect(await picker()).toHaveTextContent('Second ticket');
		expect(api.projects.tickets).toHaveBeenCalledTimes(2);
	});

	// `picked` survives load(): until the list is back, Continue is live and
	// leads to the last dialog's ticket, with no list on screen.
	it('has nothing to continue to while a reopened dialog reloads', async () => {
		const props = open();
		await picker();
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

		vi.mocked(api.projects.tickets).mockReturnValue(deferred<never>().promise);
		props.open = true;
		await tick();

		expect(continueBtn()).toBeDisabled();
	});

	describe('on a merge request', () => {
		const mrTab = () => screen.getByRole('tab', { name: 'Merge request' });
		const mrPicker = () => screen.findByRole('combobox', { name: 'Merge request' });
		beforeEach(() => {
			vi.mocked(api.projects.mergeRequests).mockImplementation((pid) =>
				Promise.resolve(
					pid === 'p1'
						? [
								{ mr: anOpenMr({ iid: 7, title: 'Add keys' }), runs: 0 },
								{ mr: anOpenMr({ iid: 8, title: 'Fix typo' }), runs: 0 }
							]
						: []
				)
			);
		});

		it('reads nothing of the repository until the tab is chosen', async () => {
			open();
			await picker();
			expect(api.projects.mergeRequests).not.toHaveBeenCalled();
		});

		it('says the run is given the merge request, no ticket made', async () => {
			open();
			await userEvent.click(mrTab());
			expect(await screen.findByText(/with no ticket made/)).toBeInTheDocument();
		});

		it('picks the first open one of the first workshop, and goes to its page', async () => {
			open();
			await userEvent.click(mrTab());
			expect(await mrPicker()).toHaveTextContent('!7 Add keys');
			expect(api.projects.mergeRequests).toHaveBeenCalledWith('p1');

			await userEvent.click(continueBtn());
			expect(goto).toHaveBeenCalledExactlyOnceWith('/merge-requests/p1/7');
		});

		it('goes to the one picked', async () => {
			open();
			await userEvent.click(mrTab());
			await userEvent.click(await mrPicker());
			await userEvent.click(screen.getByRole('option', { name: /Fix typo/ }));
			await userEvent.click(continueBtn());
			expect(goto).toHaveBeenCalledExactlyOnceWith('/merge-requests/p1/8');
		});

		it('reads another workshop’s when it is picked', async () => {
			open();
			await userEvent.click(mrTab());
			await mrPicker();
			await userEvent.click(screen.getByRole('combobox', { name: 'Workshop' }));
			await userEvent.click(screen.getByRole('option', { name: 'docs' }));
			expect(
				await screen.findByText('This workshop’s repository has no open merge request.')
			).toBeInTheDocument();
			expect(continueBtn()).toBeDisabled();
		});

		it('keeps to the page’s workshop, without asking which', async () => {
			open({ projectId: 'p2' });
			await userEvent.click(mrTab());
			await screen.findByText('This workshop’s repository has no open merge request.');
			expect(api.projects.mergeRequests).toHaveBeenCalledWith('p2');
			expect(screen.queryByRole('combobox', { name: 'Workshop' })).not.toBeInTheDocument();
		});

		it('says when the repository will not answer, and tries again', async () => {
			vi.mocked(api.projects.mergeRequests).mockRejectedValueOnce(new Error('no token'));
			open();
			await userEvent.click(mrTab());
			expect(await screen.findByRole('alert')).toHaveTextContent(
				'Could not list the merge requests'
			);
			expect(continueBtn()).toBeDisabled();
			await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
			expect(await mrPicker()).toHaveTextContent('!7 Add keys');
		});

		it('shows it is loading', async () => {
			vi.mocked(api.projects.mergeRequests).mockReturnValue(deferred<never>().promise);
			open();
			await picker();
			await userEvent.click(mrTab());
			expect(screen.getByRole('status')).toHaveTextContent('Loading…');
		});
	});
});
