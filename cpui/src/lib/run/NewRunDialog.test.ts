import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { goto } from '$app/navigation';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { aProject, aTicket } from '$test/fixtures';
import { anOpenMr, bound } from '$test/fixtures/run-components';
import { deferred } from '$test/helpers';
import NewRunDialog from './NewRunDialog.svelte';

vi.mock('$lib/api', () => ({
	api: { tickets: { list: vi.fn() }, projects: { mergeRequests: vi.fn() } }
}));

const older = aTicket({
	id: 'old',
	projectId: 'p1',
	title: 'Older ticket',
	createdAt: '2026-10-01T00:00:00Z'
});
const newer = aTicket({
	id: 'new',
	projectId: 'p2',
	title: 'Newer ticket',
	createdAt: '2026-10-20T00:00:00Z'
});

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
		vi.mocked(api.tickets.list).mockResolvedValue([older, newer]);
		fleet.projects = [aProject({ id: 'p1', name: 'ledger' }), aProject({ id: 'p2', name: 'docs' })];
	});

	it('loads nothing until opened', () => {
		render(NewRunDialog, { open: false });

		expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
		expect(api.tickets.list).not.toHaveBeenCalled();
	});

	it('shows it is loading, with nothing to continue to', () => {
		vi.mocked(api.tickets.list).mockReturnValue(deferred<never>().promise);
		open();

		expect(screen.getByRole('dialog', { name: 'New run' })).toBeInTheDocument();
		expect(screen.getByRole('status')).toHaveTextContent('Loading…');
		expect(continueBtn()).toBeDisabled();
	});

	it('picks the newest ticket first', async () => {
		open();

		expect(await picker()).toHaveTextContent('Newer ticket');
		expect(continueBtn()).toBeEnabled();
	});

	it('takes you to the ticket, where its run is started', async () => {
		open();
		await picker();

		await userEvent.click(continueBtn());

		expect(goto).toHaveBeenCalledExactlyOnceWith('/tickets/new');
	});

	it('takes you to the ticket you pick', async () => {
		open();
		await userEvent.click(await picker());
		await userEvent.click(screen.getByRole('option', { name: /Older ticket/ }));

		await userEvent.click(continueBtn());

		expect(goto).toHaveBeenCalledExactlyOnceWith('/tickets/old');
	});

	it('lists every workshop by name when not filtered', async () => {
		open();
		await userEvent.click(await picker());

		expect(screen.getByRole('option', { name: /Older ticket.*ledger/ })).toBeInTheDocument();
		expect(screen.getByRole('option', { name: /Newer ticket.*docs/ })).toBeInTheDocument();
	});

	it('keeps to one workshop when the page is filtered', async () => {
		open({ projectId: 'p1' });
		await userEvent.click(await picker());

		const options = screen.getAllByRole('option');
		expect(options).toHaveLength(1);
		expect(options[0]).toHaveTextContent('Older ticket');
		expect(options[0]).not.toHaveTextContent('ledger');
	});

	describe('with no tickets', () => {
		beforeEach(() => {
			vi.mocked(api.tickets.list).mockResolvedValue([]);
		});

		it('sends you to write one', async () => {
			open();

			expect(
				await screen.findByText('A run works on a ticket, and there is none yet. Write one first.')
			).toBeInTheDocument();
			expect(screen.getByRole('link', { name: /ticket/i })).toHaveAttribute('href', '/tickets/new');
			expect(screen.queryByRole('button', { name: 'Continue' })).not.toBeInTheDocument();
		});

		it('carries the workshop to the new ticket', async () => {
			open({ projectId: 'p1' });

			expect(await screen.findByRole('link', { name: /ticket/i })).toHaveAttribute(
				'href',
				'/tickets/new?project=p1'
			);
		});

		it('treats tickets of other workshops as none', async () => {
			vi.mocked(api.tickets.list).mockResolvedValue([newer]);
			open({ projectId: 'p1' });

			expect(await screen.findByRole('link', { name: /ticket/i })).toBeInTheDocument();
		});
	});

	describe('when loading fails', () => {
		beforeEach(() => {
			vi.mocked(api.tickets.list).mockRejectedValueOnce(new Error('engine down'));
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

			expect(await picker()).toHaveTextContent('Newer ticket');
			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
			expect(api.tickets.list).toHaveBeenCalledTimes(2);
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

		vi.mocked(api.tickets.list).mockResolvedValue([older]);
		props.open = true;
		await tick();

		expect(await picker()).toHaveTextContent('Older ticket');
		expect(api.tickets.list).toHaveBeenCalledTimes(2);
	});

	// `picked` survives load(): until the list is back, Continue is live and
	// leads to the last dialog's ticket, with no list on screen.
	it('has nothing to continue to while a reopened dialog reloads', async () => {
		const props = open();
		await picker();
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

		vi.mocked(api.tickets.list).mockReturnValue(deferred<never>().promise);
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
