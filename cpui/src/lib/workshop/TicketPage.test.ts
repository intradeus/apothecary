import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import type { TicketDetail } from '$lib/domain';
import { confirmState } from '$lib/ui/confirm.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { aRun, aTicketDetail, aViewerProject } from '$test/fixtures';
import { aFlow, aWorkshop, confirmed } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import TicketPage from './TicketPage.svelte';

vi.mock('$lib/api', () => ({
	api: {
		projects: {
			ticket: vi.fn(),
			branches: vi.fn(),
			get: vi.fn(),
			mergeRequests: vi.fn(),
			ticketStatuses: vi.fn(),
			ticketTypes: vi.fn()
		},
		tickets: {
			attachments: vi.fn(),
			startRun: vi.fn(),
			remove: vi.fn(),
			comments: vi.fn(),
			addComment: vi.fn(),
			setStatus: vi.fn(),
			setLabels: vi.fn(),
			update: vi.fn(),
			versions: vi.fn(),
			version: vi.fn(),
			restore: vi.fn()
		},
		runs: { abort: vi.fn() },
		files: { upload: vi.fn(), attachmentUrl: vi.fn(), limits: vi.fn() },
		flows: { list: vi.fn(), get: vi.fn(), binding: vi.fn() },
		integrations: { list: vi.fn(), models: vi.fn() },
		skillsets: { list: vi.fn(), versions: vi.fn() }
	},
	busyRunOf: (e: unknown) =>
		e && typeof e === 'object' && 'busy' in e && typeof e.busy === 'string' ? e.busy : undefined
}));

const TICKET_ID = '01TESTTICK000000000000ABCD';

function setup(tk: TicketDetail | Promise<TicketDetail> = aTicketDetail()) {
	vi.mocked(api.projects.ticket).mockImplementation(() => Promise.resolve(tk));
	return render(TicketPage, { projectId: 'p1', ticketKey: 'PROJ-7' });
}
const startBtn = () => screen.findByRole('button', { name: /Start run/ });

beforeEach(() => {
	vi.resetAllMocks();
	toasts.list = [];
	confirmState.current = null;
	fleet.projects = [aWorkshop({ id: 'p1', name: 'shop', defaultFlow: 'builder' })];
	fleet.runs = [];
	vi.spyOn(fleet, 'refresh').mockResolvedValue();
	vi.mocked(api.projects.branches).mockResolvedValue([
		{ name: 'develop', isDefault: false },
		{ name: 'main', isDefault: true }
	]);
	vi.mocked(api.tickets.attachments).mockResolvedValue({ attachments: [], remote: [] });
	vi.mocked(api.tickets.comments).mockResolvedValue([]);
	vi.mocked(api.projects.ticketStatuses).mockResolvedValue({ statuses: [], readOnly: true });
	vi.mocked(api.flows.list).mockResolvedValue([aFlow({ name: 'builder' })]);
	vi.mocked(api.flows.get).mockResolvedValue(aFlow({ name: 'builder' }));
	vi.mocked(api.flows.binding).mockResolvedValue({
		projectId: 'p1',
		flowName: 'builder',
		params: {},
		updatedAt: ''
	});
	vi.mocked(api.integrations.list).mockResolvedValue([]);
	vi.mocked(api.projects.get).mockResolvedValue(aWorkshop({ id: 'p1' }));
	vi.mocked(api.projects.mergeRequests).mockResolvedValue([]);
	vi.mocked(api.skillsets.list).mockResolvedValue([]);
	vi.mocked(api.projects.ticketTypes).mockResolvedValue([]);
	vi.mocked(api.files.limits).mockRejectedValue(new Error('no policy'));
	vi.mocked(api.tickets.versions).mockResolvedValue([]);
});
afterEach(() => {
	vi.restoreAllMocks();
	fleet.projects = [];
	fleet.runs = [];
});

describe('TicketPage, reading the ticket', () => {
	it('shows a skeleton until the source answers', () => {
		setup(new Promise(() => {}));
		expect(screen.queryByRole('heading', { name: 'Add idempotency keys' })).toBeNull();
	});

	it('reads the issue live through the workshop, with its branches and files', async () => {
		vi.mocked(api.tickets.attachments).mockResolvedValue({
			attachments: [],
			remote: [{ filename: 'trace.png', size: 10 }]
		});
		setup();
		expect(
			await screen.findByRole('heading', { name: 'Add idempotency keys' })
		).toBeInTheDocument();
		expect(api.projects.ticket).toHaveBeenCalledWith('p1', 'PROJ-7');
		// The header and the tracker pane both link the issue.
		for (const link of screen.getAllByRole('link', { name: /Open in Jira/ }))
			expect(link).toHaveAttribute('href', 'https://tracker.test/browse/PROJ-7');
		expect(screen.getByText('Make the webhook safe to retry.')).toBeInTheDocument();
		expect(await screen.findByText('trace.png')).toBeInTheDocument();
		expect(api.tickets.attachments).toHaveBeenCalledWith('p1', 'PROJ-7');
		expect(api.projects.branches).toHaveBeenCalledWith('p1');
		expect(await screen.findByRole('combobox', { name: 'Source branch' })).toHaveTextContent(
			'main'
		);
	});

	it('says when the source does not have it', async () => {
		vi.mocked(api.projects.ticket).mockRejectedValue(new Error('no issue PROJ-7'));
		render(TicketPage, { projectId: 'p1', ticketKey: 'PROJ-7' });
		expect(await screen.findByText('No such ticket')).toBeInTheDocument();
		expect(screen.getByText('no issue PROJ-7')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'All tickets' })).toHaveAttribute(
			'href',
			'/w/p1?tab=tickets'
		);
	});

	it('rereads the tracker on refresh: the ticket, its thread and its files', async () => {
		setup();
		await screen.findByRole('heading', { name: 'Add idempotency keys' });
		await waitFor(() => expect(api.tickets.comments).toHaveBeenCalledOnce());
		vi.mocked(api.projects.ticket).mockResolvedValue(aTicketDetail({ title: 'Idempotency, v2' }));

		await userEvent.click(screen.getByRole('button', { name: /Refresh/ }));

		expect(await screen.findByRole('heading', { name: 'Idempotency, v2' })).toBeInTheDocument();
		expect(api.projects.ticket).toHaveBeenLastCalledWith('p1', 'PROJ-7', { refresh: true });
		await waitFor(() =>
			expect(api.tickets.comments).toHaveBeenLastCalledWith('p1', 'PROJ-7', { refresh: true })
		);
		expect(api.tickets.attachments).toHaveBeenCalledTimes(2);
	});

	it('falls back to the default branch when the repository host cannot list them', async () => {
		vi.mocked(api.projects.branches).mockRejectedValue(new Error('host down'));
		setup();
		const input = await screen.findByPlaceholderText('branches unavailable');
		expect(input).toBeDisabled();
	});

	it("lists the ticket's runs from the shared poll", async () => {
		fleet.runs = [aRun({ id: '01RUNONIT0000000000000AAAA', projectId: 'p1', ticketId: TICKET_ID })];
		setup();
		await screen.findByRole('heading', { name: 'Add idempotency keys' });
		expect(screen.getAllByRole('link', { name: /aaaa/i }).length).toBeGreaterThan(0);
		expect(screen.queryByText('No runs of this ticket yet. Start one above.')).toBeNull();
	});

	it('says when none ran yet, as for an issue with no record', async () => {
		fleet.runs = [aRun({ projectId: 'p1', ticketId: TICKET_ID })];
		setup(aTicketDetail({ ticketId: '' }));
		expect(
			await screen.findByText('No runs of this ticket yet. Start one above.')
		).toBeInTheDocument();
	});
});

describe('TicketPage, what may be done to it', () => {
	it('neither edits nor deletes a remote issue', async () => {
		setup();
		await screen.findByRole('heading', { name: 'Add idempotency keys' });
		expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull();
		expect(screen.queryByRole('button', { name: /Delete/ })).toBeNull();
	});

	it('edits an internal ticket in place, and puts the form away again', async () => {
		setup(
			aTicketDetail({
				internal: true,
				url: '',
				source: { id: 'o', kind: 'internal', name: 'Apothecary' }
			})
		);
		await userEvent.click(await screen.findByRole('button', { name: 'Edit' }));
		expect(screen.getByRole('button', { name: 'Cancel edit' })).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: /Start run/ })).toBeNull();
		expect(screen.queryByRole('link', { name: /Open in/ })).toBeNull();

		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(await screen.findByRole('button', { name: 'Edit' })).toBeInTheDocument();
		expect(await startBtn()).toBeInTheDocument();
	});

	it('deletes an internal ticket once confirmed, back to the workshop list', async () => {
		vi.mocked(api.tickets.remove).mockResolvedValue(undefined as never);
		setup(aTicketDetail({ internal: true }));
		await userEvent.click(await screen.findByRole('button', { name: /Delete/ }));
		await confirmed(true);
		await waitFor(() => expect(goto).toHaveBeenCalledWith('/w/p1?tab=tickets'));
		expect(api.tickets.remove).toHaveBeenCalledWith('p1', 'PROJ-7');
		expect(toasts.list.at(-1)?.text).toBe('Ticket “Add idempotency keys” deleted');
	});

	it('keeps it when the delete is called off, and shows a refused one', async () => {
		setup(aTicketDetail({ internal: true }));
		await userEvent.click(await screen.findByRole('button', { name: /Delete/ }));
		await confirmed(false);
		expect(api.tickets.remove).not.toHaveBeenCalled();

		vi.mocked(api.tickets.remove).mockRejectedValue(new Error('it has an active run'));
		await userEvent.click(screen.getByRole('button', { name: /Delete/ }));
		await confirmed(true);
		expect(await screen.findByText('it has an active run')).toBeInTheDocument();
		expect(goto).not.toHaveBeenCalled();
	});

	it('without ticket:write or run:operate, edits, deletes, comments and starts nothing', async () => {
		fleet.projects = [aViewerProject({ id: 'p1', name: 'shop', defaultFlow: 'builder' })];
		setup(
			aTicketDetail({
				internal: true,
				url: '',
				labels: ['api'],
				source: { id: 'o', kind: 'internal', name: 'Apothecary' }
			})
		);
		const edit = await screen.findByRole('button', { name: 'Edit' });
		expect(edit).toBeDisabled();
		expect(edit).toHaveAttribute('title', 'Your role in this workshop does not allow this');
		expect(screen.getByRole('button', { name: /Delete/ })).toBeDisabled();
		expect(screen.getByRole('button', { name: 'Remove label api' })).toBeDisabled();
		expect(screen.queryByRole('button', { name: 'Comment' })).toBeNull();
		expect(await startBtn()).toBeDisabled();
	});
});

describe('TicketPage, its history', () => {
	const internal = () =>
		aTicketDetail({
			internal: true,
			url: '',
			source: { id: 'o', kind: 'internal', name: 'Apothecary' }
		});
	const summary = (seq: number, title: string) => ({
		seq,
		title,
		author: { kind: 'user' as const, id: 'u1', handle: 'ann' },
		createdAt: new Date().toISOString(),
		changed: ['title'],
		oldest: false
	});

	it('shows in the read view, not while editing', async () => {
		setup(internal());
		expect(await screen.findByRole('region', { name: 'History' })).toBeInTheDocument();
		expect(api.tickets.versions).toHaveBeenCalledWith('p1', 'PROJ-7');
		await userEvent.click(screen.getByRole('button', { name: 'Edit' }));
		expect(screen.queryByRole('region', { name: 'History' })).toBeNull();
	});

	it('takes a restored ticket as the page’s and rereads the history', async () => {
		vi.mocked(api.tickets.versions).mockResolvedValue([summary(2, 'Now'), summary(1, 'Was')]);
		vi.mocked(api.tickets.version).mockResolvedValue({
			...summary(1, 'Was'),
			goal: 'G',
			baseBranch: '',
			changeType: 'feat',
			typeId: '',
			labels: []
		});
		vi.mocked(api.tickets.restore).mockResolvedValue({ ...internal(), title: 'Was' });
		setup(internal());
		await userEvent.click(await screen.findByRole('button', { name: /^v1/ }));
		vi.mocked(api.tickets.versions).mockResolvedValue([
			summary(3, 'Was'),
			summary(2, 'Now'),
			summary(1, 'Was')
		]);
		await userEvent.click(await screen.findByRole('button', { name: 'Restore this version' }));
		await confirmed(true);
		await waitFor(() => expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Was'));
		expect(api.tickets.restore).toHaveBeenCalledWith('p1', 'PROJ-7', 1);
		expect(await screen.findByRole('button', { name: /^v3/ })).toBeInTheDocument();
	});
});

describe('TicketPage, starting a run', () => {
	it('starts one through the workshop and opens it', async () => {
		vi.mocked(api.tickets.startRun).mockResolvedValue('01NEWRUN00000000000000ZZZZ');
		setup();
		await screen.findByRole('combobox', { name: 'Source branch' });
		await userEvent.type(screen.getByLabelText(/Direction/), '  only the API  ');
		await userEvent.click(await startBtn());

		await waitFor(() => expect(goto).toHaveBeenCalledWith('/runs/01NEWRUN00000000000000ZZZZ'));
		expect(api.tickets.startRun).toHaveBeenCalledWith(
			'p1',
			'PROJ-7',
			expect.objectContaining({ goal: 'only the API', sourceBranch: 'main' })
		);
		expect(api.files.upload).not.toHaveBeenCalled();
		expect(fleet.refresh).toHaveBeenCalled();
		expect(toasts.list.at(-1)?.text).toMatch(/started/);
	});

	it('holds the button while the start is on its way', async () => {
		const pending = deferred<string>();
		vi.mocked(api.tickets.startRun).mockReturnValue(pending.promise);
		setup();
		const btn = await startBtn();
		await userEvent.click(btn);
		expect(btn).toHaveAttribute('aria-busy', 'true');
		await userEvent.click(btn);
		expect(api.tickets.startRun).toHaveBeenCalledOnce();
		pending.resolve('r1');
	});

	it('names the run in the way, in whichever workshop it is', async () => {
		vi.mocked(api.tickets.startRun).mockRejectedValue({ busy: '01BUSY0000000000000000BUSY' });
		setup();
		await userEvent.click(await startBtn());
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent(/is active — a ticket has one run at a time/);
		expect(screen.getByRole('link', { name: /Open run/ })).toHaveAttribute(
			'href',
			'/runs/01BUSY0000000000000000BUSY'
		);
		expect(goto).not.toHaveBeenCalled();
	});

	it('shows any other refusal, and starts again after it', async () => {
		vi.mocked(api.tickets.startRun).mockRejectedValueOnce(
			new Error('"in_progress_status" has no value')
		);
		setup();
		await userEvent.click(await startBtn());
		expect(await screen.findByText('"in_progress_status" has no value')).toBeInTheDocument();

		vi.mocked(api.tickets.startRun).mockResolvedValue('r2');
		await userEvent.click(await startBtn());
		await waitFor(() => expect(goto).toHaveBeenCalledWith('/runs/r2'));
	});
});
