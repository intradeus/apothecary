import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { TicketDetail, TrackerStatus } from '$lib/domain';
import { aTicketDetail } from '$test/fixtures';
import { choose } from '$test/fixtures/app.svelte';
import TicketTracker from './TicketTracker.svelte';

vi.mock('$lib/api', () => ({
	api: {
		integrations: { statuses: vi.fn() },
		projects: { ticket: vi.fn() },
		tickets: { setStatus: vi.fn(), setLabels: vi.fn(), update: vi.fn() }
	}
}));

const status = (name: string, position: number, isDone = false): TrackerStatus => ({
	id: '',
	integrationId: 'src',
	remoteId: name,
	name,
	position,
	isDone
});

const setup = (over: Partial<TicketDetail> = {}, branchFormat = '') => {
	const onchange = vi.fn();
	render(TicketTracker, {
		projectId: 'p1',
		ticket: aTicketDetail({ key: 'P-1', status: 'To Do', ...over }),
		branchFormat,
		onchange
	});
	return { onchange };
};

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.integrations.statuses).mockResolvedValue({
		statuses: [status('To Do', 0), status('Done', 1, true)],
		readOnly: true
	});
});

describe('TicketTracker, read only (no ticket:write)', () => {
	it('shows status, labels and base branch but changes none of them', async () => {
		render(TicketTracker, {
			projectId: 'p1',
			ticket: aTicketDetail({ key: 'P-1', status: 'To Do', labels: ['api'] }),
			readonly: true,
			onchange: vi.fn()
		});
		await waitFor(() => expect(api.integrations.statuses).toHaveBeenCalled());
		const why = 'Your role in this workshop does not allow this';
		expect(screen.getByRole('button', { name: 'Remove label api' })).toBeDisabled();
		expect(screen.getByLabelText('Add a label…')).toBeDisabled();
		expect(screen.getByLabelText('Base branch')).toHaveAttribute('title', why);
		expect(screen.getByLabelText('Base branch')).toBeDisabled();
		expect(screen.getByRole('button', { name: /Refresh/ })).toBeEnabled();
	});
});

describe('TicketTracker, the status', () => {
	it("moves the ticket by name through its source's statuses, read-only ones too", async () => {
		const moved = aTicketDetail({ status: 'Done', done: true });
		vi.mocked(api.tickets.setStatus).mockResolvedValue(moved);
		const { onchange } = setup();
		await waitFor(() =>
			expect(api.integrations.statuses).toHaveBeenCalledWith('01TESTSRC0000000000000ABCD')
		);
		await choose('Change status', /^Done/);

		expect(api.tickets.setStatus).toHaveBeenCalledWith('p1', 'P-1', 'Done');
		await waitFor(() => expect(onchange).toHaveBeenCalledWith(moved));
	});

	it('keeps the current status pickable when the list fails', async () => {
		vi.mocked(api.integrations.statuses).mockRejectedValue(new Error('down'));
		setup({ status: 'Triage' });
		await waitFor(() => expect(api.integrations.statuses).toHaveBeenCalled());
		await choose('Change status', /^Triage/);
		expect(api.tickets.setStatus).not.toHaveBeenCalled();
	});

	it('shows a refused move next to it', async () => {
		vi.mocked(api.tickets.setStatus).mockRejectedValue(new Error('reachable: In Progress'));
		setup();
		await waitFor(() => expect(api.integrations.statuses).toHaveBeenCalled());
		await choose('Change status', /^Done/);
		expect(await screen.findByText('reachable: In Progress')).toBeInTheDocument();
	});
});

describe('TicketTracker, the tracker', () => {
	it('opens the issue in its tracker', () => {
		setup({ url: 'https://tracker.test/browse/P-1' });
		expect(screen.getByRole('link', { name: /Open in Jira/ })).toHaveAttribute(
			'href',
			'https://tracker.test/browse/P-1'
		);
	});

	it('has no tracker link without a URL', () => {
		setup({ url: '' });
		expect(screen.queryByRole('link', { name: /Open in/ })).not.toBeInTheDocument();
	});

	it('reads the issue from the tracker again', async () => {
		const fresh = aTicketDetail({ title: 'Fresh' });
		vi.mocked(api.projects.ticket).mockResolvedValue(fresh);
		const { onchange } = setup();
		await userEvent.click(screen.getByRole('button', { name: /Refresh/ }));
		expect(api.projects.ticket).toHaveBeenCalledWith('p1', 'P-1', { refresh: true });
		await waitFor(() => expect(onchange).toHaveBeenCalledWith(fresh));
	});

	it('lists the links', () => {
		setup({ links: [{ ticketId: 't9', url: 'https://x.test/spec', title: 'Spec' }] });
		expect(screen.getByRole('link', { name: /Spec/ })).toHaveAttribute(
			'href',
			'https://x.test/spec'
		);
	});
});

describe('TicketTracker, the labels', () => {
	it('adds a label', async () => {
		vi.mocked(api.tickets.setLabels).mockResolvedValue(aTicketDetail());
		setup({ labels: ['ui'] });
		await userEvent.type(screen.getByRole('textbox', { name: 'Add a label…' }), 'api{Enter}');
		expect(api.tickets.setLabels).toHaveBeenCalledWith('p1', 'P-1', ['ui', 'api']);
	});

	it('ignores an empty or repeated label', async () => {
		setup({ labels: ['ui'] });
		const box = screen.getByRole('textbox', { name: 'Add a label…' });
		await userEvent.type(box, '{Enter}');
		await userEvent.type(box, 'ui{Enter}');
		expect(api.tickets.setLabels).not.toHaveBeenCalled();
	});

	it('removes a label', async () => {
		vi.mocked(api.tickets.setLabels).mockResolvedValue(aTicketDetail());
		setup({ labels: ['ui', 'api'] });
		await userEvent.click(screen.getByRole('button', { name: 'Remove label ui' }));
		expect(api.tickets.setLabels).toHaveBeenCalledWith('p1', 'P-1', ['api']);
	});

	it('refuses a label with a comma before sending it', async () => {
		setup();
		await userEvent.type(screen.getByRole('textbox', { name: 'Add a label…' }), 'a,b{Enter}');
		expect(screen.getByText('A label can’t contain a comma.')).toBeInTheDocument();
		expect(api.tickets.setLabels).not.toHaveBeenCalled();
	});
});

describe('TicketTracker, the base branch', () => {
	it("shows the workshop format's name as the placeholder", () => {
		setup(
			{ baseBranch: '', changeType: 'fix', title: 'Fix login' },
			'{ticket_type}/{ticket_title}'
		);
		expect(screen.getByRole('textbox', { name: 'Base branch' })).toHaveAttribute(
			'placeholder',
			'fix/fix-login'
		);
	});

	it('saves an edited branch, sending the rest of the ticket back as it is', async () => {
		const saved = aTicketDetail({ baseBranch: 'feat/x' });
		vi.mocked(api.tickets.update).mockResolvedValue(saved);
		const { onchange } = setup({
			baseBranch: '',
			internal: true,
			typeId: '',
			attachmentIds: ['a1']
		});
		expect(screen.queryByRole('button', { name: 'Save branch' })).not.toBeInTheDocument();
		await userEvent.type(screen.getByRole('textbox', { name: 'Base branch' }), 'feat/x');
		await userEvent.click(screen.getByRole('button', { name: 'Save branch' }));
		expect(api.tickets.update).toHaveBeenCalledWith('p1', 'P-1', {
			title: 'Add idempotency keys',
			goal: 'Make the webhook safe to retry.',
			baseBranch: 'feat/x',
			changeType: 'feat',
			typeId: undefined,
			attachmentIds: ['a1']
		});
		await waitFor(() => expect(onchange).toHaveBeenCalledWith(saved));
	});
});
