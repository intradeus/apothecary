import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { SourceTicket, Ticket } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { aProject, aTicket } from '$test/fixtures';
import { anIntegration, choose } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import TicketForm from './TicketForm.svelte';

vi.mock('$lib/api', () => ({
	api: {
		tickets: { create: vi.fn(), update: vi.fn(), attachments: vi.fn() },
		integrations: { list: vi.fn(), sourceTickets: vi.fn(), sourceTicket: vi.fn() },
		files: { upload: vi.fn(), limits: vi.fn(), attachmentUrl: vi.fn() }
	}
}));

const projects = [aProject({ id: 'p1', name: 'billing' }), aProject({ id: 'p2', name: 'web' })];
const title = () => screen.getByLabelText('Title');
const goal = () => screen.getByLabelText('Goal');
const branch = () => screen.getByLabelText('Base branch');
const create = () => screen.getByRole('button', { name: 'Create ticket' });
const jira = anIntegration({ id: 'j1', name: 'Jira', port: 'ticketsource', kind: 'jira' });

const issue = (over: Partial<SourceTicket> = {}): SourceTicket => ({
	key: 'PROJ-7',
	title: 'Bad invoice',
	body: 'It rounds wrong.',
	attachments: [],
	changeType: 'fix',
	branch: 'fix/proj-7-bad-invoice',
	...over
});

function setup(over: Record<string, unknown> = {}) {
	const onsaved = vi.fn();
	render(TicketForm, { projects, onsaved, ...over });
	return { onsaved };
}
const created = (over: Partial<Ticket> = {}) => {
	const saved = aTicket(over);
	vi.mocked(api.tickets.create).mockResolvedValue(saved);
	return saved;
};
const lastBody = () => vi.mocked(api.tickets.create).mock.calls.at(-1)![0];

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.integrations.list).mockResolvedValue([]);
	vi.mocked(api.tickets.attachments).mockResolvedValue([]);
	vi.mocked(api.files.limits).mockResolvedValue(null as never);
	toasts.list = [];
});

describe('TicketForm, creating', () => {
	it('needs a title and a goal before it can be created', async () => {
		setup();
		expect(create()).toBeDisabled();
		await userEvent.type(title(), 'Add keys');
		expect(create()).toBeDisabled();
		await userEvent.type(goal(), 'Safe retries');
		expect(create()).toBeEnabled();
	});

	it('will not take whitespace for a title', async () => {
		setup();
		await userEvent.type(title(), '   ');
		await userEvent.type(goal(), 'Safe retries');
		expect(create()).toBeDisabled();
	});

	it('warns and stays disabled with no workshop at all', async () => {
		setup({ projects: [] });
		expect(screen.getByRole('link', { name: /workshop/i })).toHaveAttribute('href', '/w/new');
		await userEvent.type(title(), 'Add keys');
		await userEvent.type(goal(), 'Safe retries');
		expect(create()).toBeDisabled();
	});

	it('creates the ticket, trimmed, and hands it to the page', async () => {
		const saved = created();
		const { onsaved } = setup();
		await userEvent.type(title(), ' Add keys ');
		await userEvent.type(goal(), ' Safe retries ');
		await userEvent.click(create());

		expect(lastBody()).toEqual({
			title: 'Add keys',
			goal: 'Safe retries',
			projectId: 'p2',
			branch: 'feat/add-keys',
			changeType: 'feat',
			attachmentIds: []
		});
		expect(onsaved).toHaveBeenCalledWith(saved);
		expect(toasts.list.at(-1)?.text).toBe('Ticket created');
	});

	it('defaults to the last workshop', () => {
		setup();
		expect(screen.getByRole('combobox', { name: 'Workshop' })).toHaveTextContent('web');
	});

	it('preselects initialProject when it exists', () => {
		setup({ initialProject: 'p1' });
		expect(screen.getByRole('combobox', { name: 'Workshop' })).toHaveTextContent('billing');
	});

	it('ignores an initialProject that does not exist', () => {
		setup({ initialProject: 'nope' });
		expect(screen.getByRole('combobox', { name: 'Workshop' })).toHaveTextContent('web');
	});

	it('sends the workshop picked', async () => {
		created();
		setup();
		await choose('Workshop', 'billing');
		await userEvent.type(title(), 'A');
		await userEvent.type(goal(), 'B');
		await userEvent.click(create());
		expect(lastBody().projectId).toBe('p1');
	});

	it('uploads files first and attaches them', async () => {
		created();
		vi.mocked(api.files.upload).mockResolvedValue([{ id: 'f1' }] as never);
		const { container } = render(TicketForm, { projects, onsaved: vi.fn() });
		await userEvent.type(title(), 'A');
		await userEvent.type(goal(), 'B');
		await userEvent.upload(
			container.querySelector('input[type=file]') as HTMLInputElement,
			new File(['x'], 'spec.md')
		);
		await userEvent.click(create());

		expect(api.files.upload).toHaveBeenCalledOnce();
		expect(lastBody().attachmentIds).toEqual(['f1']);
	});

	it('does not create when an upload fails', async () => {
		vi.mocked(api.files.upload).mockRejectedValue(new Error('too big'));
		const onsaved = vi.fn();
		const { container } = render(TicketForm, { projects, onsaved });
		await userEvent.type(title(), 'A');
		await userEvent.type(goal(), 'B');
		await userEvent.upload(
			container.querySelector('input[type=file]') as HTMLInputElement,
			new File(['x'], 'spec.md')
		);
		await userEvent.click(create());

		expect(await screen.findByText('too big')).toBeInTheDocument();
		expect(api.tickets.create).not.toHaveBeenCalled();
		expect(onsaved).not.toHaveBeenCalled();
	});

	it('shows a refusal and keeps what was typed', async () => {
		vi.mocked(api.tickets.create).mockRejectedValue(new Error('branch taken'));
		const { onsaved } = setup();
		await userEvent.type(title(), 'Add keys');
		await userEvent.type(goal(), 'Safe retries');
		await userEvent.click(create());

		expect(await screen.findByText('branch taken')).toBeInTheDocument();
		expect(title()).toHaveValue('Add keys');
		expect(onsaved).not.toHaveBeenCalled();
	});

	it('sends once on a double submit', async () => {
		const save = deferred<Ticket>();
		vi.mocked(api.tickets.create).mockReturnValue(save.promise);
		setup();
		await userEvent.type(title(), 'Add keys');
		await userEvent.type(goal(), 'Safe retries');
		await userEvent.click(create());
		await userEvent.type(title(), '{Enter}');

		expect(api.tickets.create).toHaveBeenCalledOnce();
		save.resolve(aTicket());
	});

	it('shows Cancel only when the page wants one', async () => {
		const oncancel = vi.fn();
		setup({ oncancel });
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(oncancel).toHaveBeenCalledOnce();
	});
});

describe('TicketForm, the branch', () => {
	it('names the branch from the type and the whole title', async () => {
		setup();
		await userEvent.type(title(), 'Add Idempotency  Keys!');
		expect(branch()).toHaveValue('feat/add-idempotency-keys');
	});

	it('leaves the branch empty while the title slugs to nothing', async () => {
		setup();
		await userEvent.type(title(), '!!!');
		expect(branch()).toHaveValue('');
	});

	it('follows the change type', async () => {
		setup();
		await userEvent.type(title(), 'Add keys');
		await choose('Change type', 'hotfix');
		expect(branch()).toHaveValue('hotfix/add-keys');
	});

	it('stops following the title once edited, and says so', async () => {
		setup();
		await userEvent.type(title(), 'Add keys');
		await userEvent.clear(branch());
		await userEvent.type(branch(), 'feat/mine');
		await userEvent.type(title(), ' more');

		expect(branch()).toHaveValue('feat/mine');
		expect(screen.getByText('edited')).toBeInTheDocument();
	});

	it('swaps only the leading type of an edited branch', async () => {
		setup();
		await userEvent.clear(branch());
		await userEvent.type(branch(), 'feat/deep/mine');
		await choose('Change type', 'fix');
		expect(branch()).toHaveValue('fix/deep/mine');
	});

	it('leaves any other edited name alone on a type change', async () => {
		setup();
		await userEvent.type(branch(), 'release/1.2');
		await choose('Change type', 'fix');
		expect(branch()).toHaveValue('release/1.2');
	});

	it('goes back to the automatic name', async () => {
		setup();
		await userEvent.type(title(), 'Add keys');
		await userEvent.type(branch(), 'x');
		await userEvent.click(screen.getByRole('button', { name: /auto/ }));

		expect(branch()).toHaveValue('feat/add-keys');
		expect(screen.queryByText('edited')).not.toBeInTheDocument();
	});

	it('sends no branch when it is empty', async () => {
		created();
		setup();
		await userEvent.type(title(), '!!!');
		await userEvent.type(goal(), 'B');
		await userEvent.click(create());
		expect(lastBody().branch).toBeUndefined();
	});
});

describe('TicketForm, editing', () => {
	const existing = aTicket({
		id: 't1',
		projectId: 'p1',
		title: 'Old title',
		goal: 'Old goal',
		baseBranch: 'feat/old',
		changeType: 'fix',
		attachmentIds: ['a1']
	});

	it('starts from the ticket, its workshop locked and its branch left alone', async () => {
		setup({ existing });
		expect(title()).toHaveValue('Old title');
		expect(goal()).toHaveValue('Old goal');
		expect(branch()).toHaveValue('feat/old');
		expect(screen.getByRole('combobox', { name: 'Workshop' })).toBeDisabled();
		expect(screen.getByText(/Locked/)).toBeInTheDocument();

		await userEvent.type(title(), ' new');
		expect(branch()).toHaveValue('feat/old');
	});

	it('does not offer the import', () => {
		setup({ existing });
		expect(screen.queryByRole('heading', { name: 'Import from tracker' })).not.toBeInTheDocument();
		expect(api.integrations.list).not.toHaveBeenCalled();
	});

	it('shows what is already attached', async () => {
		vi.mocked(api.tickets.attachments).mockResolvedValue([
			{ id: 'a1', filename: 'spec.md' }
		] as never);
		setup({ existing });
		expect(await screen.findByText(/spec\.md/)).toBeInTheDocument();
	});

	it('saves over the ticket, keeping its attachments', async () => {
		const saved = aTicket({ id: 't1' });
		vi.mocked(api.tickets.update).mockResolvedValue(saved);
		vi.mocked(api.files.upload).mockResolvedValue([{ id: 'a2' }] as never);
		const onsaved = vi.fn();
		const { container } = render(TicketForm, { projects, existing, onsaved });
		await userEvent.upload(
			container.querySelector('input[type=file]') as HTMLInputElement,
			new File(['x'], 'more.md')
		);
		await userEvent.click(screen.getByRole('button', { name: 'Save ticket' }));

		expect(api.tickets.update).toHaveBeenCalledWith(
			't1',
			expect.objectContaining({ attachmentIds: ['a1', 'a2'], projectId: 'p1', branch: 'feat/old' })
		);
		expect(api.tickets.create).not.toHaveBeenCalled();
		expect(onsaved).toHaveBeenCalledWith(saved);
		expect(toasts.list.at(-1)?.text).toBe('Ticket saved');
	});
});

describe('TicketForm, importing', () => {
	async function open(results: SourceTicket[] = [issue()]) {
		vi.mocked(api.integrations.list).mockResolvedValue([
			jira,
			anIntegration({ id: 'other', port: 'repository' })
		]);
		vi.mocked(api.integrations.sourceTickets).mockResolvedValue(results);
		vi.mocked(api.integrations.sourceTicket).mockResolvedValue(issue());
		setup();
		await screen.findByRole('combobox', { name: 'Tracker' });
		await choose('Tracker', 'Jira (jira)');
	}
	const result = () => screen.findByRole('button', { name: /PROJ-7/ });

	it('says how to connect a tracker when none is', async () => {
		setup();
		expect(await screen.findByText(/No ticket source connected/)).toBeInTheDocument();
	});

	it('survives failing to list trackers', async () => {
		vi.mocked(api.integrations.list).mockRejectedValue(new Error('down'));
		setup();
		expect(await screen.findByText(/No ticket source connected/)).toBeInTheDocument();
	});

	it("asks the tracker for one's plate as soon as it is picked", async () => {
		await open();
		expect(api.integrations.sourceTickets).toHaveBeenCalledWith('j1', '');
		expect(await result()).toBeInTheDocument();
	});

	it('searches on Enter, without submitting the ticket', async () => {
		await open();
		await userEvent.type(
			screen.getByRole('searchbox', { name: 'Search the tracker' }),
			' 2624 {Enter}'
		);
		expect(api.integrations.sourceTickets).toHaveBeenLastCalledWith('j1', '2624');
		expect(api.tickets.create).not.toHaveBeenCalled();
	});

	it('says when nothing matched', async () => {
		await open([]);
		expect(await screen.findByText('No matching issues.')).toBeInTheDocument();
	});

	it('shows a failed search', async () => {
		vi.mocked(api.integrations.list).mockResolvedValue([jira]);
		vi.mocked(api.integrations.sourceTickets).mockRejectedValue(new Error('bad jql'));
		setup();
		await screen.findByRole('combobox', { name: 'Tracker' });
		await choose('Tracker', 'Jira (jira)');
		expect(await screen.findByText('bad jql')).toBeInTheDocument();
	});

	it('fills the form from the full issue', async () => {
		await open();
		await userEvent.click(await result());

		await waitFor(() => expect(title()).toHaveValue('Bad invoice'));
		expect(goal()).toHaveValue('It rounds wrong.');
		expect(branch()).toHaveValue('fix/proj-7-bad-invoice');
		expect(screen.getByRole('combobox', { name: 'Change type' })).toHaveTextContent('fix');
		expect(screen.getByRole('button', { name: 'Create from PROJ-7' })).toBeInTheDocument();
	});

	it('records where the ticket came from', async () => {
		created();
		await open();
		await userEvent.click(await result());
		await userEvent.click(await screen.findByRole('button', { name: 'Create from PROJ-7' }));

		expect(lastBody()).toMatchObject({ sourceIntegrationId: 'j1', sourceKey: 'PROJ-7' });
	});

	it('keeps the import branch through a type change', async () => {
		await open();
		await userEvent.click(await result());
		await waitFor(() => expect(branch()).toHaveValue('fix/proj-7-bad-invoice'));

		await choose('Change type', 'chore');
		expect(branch()).toHaveValue('chore/proj-7-bad-invoice');
	});

	it('names the files that will be copied', async () => {
		await open();
		vi.mocked(api.integrations.sourceTicket).mockResolvedValue(
			issue({ attachments: [{ filename: 'trace.log', size: 100 }] })
		);
		await userEvent.click(await result());

		expect(await screen.findByText('1 file will be copied into the ticket:')).toBeInTheDocument();
		expect(screen.getByText(/trace\.log/)).toBeInTheDocument();
	});

	it('says plainly when the issue has no files', async () => {
		await open();
		await userEvent.click(await result());
		expect(await screen.findByText('This issue has no attachments.')).toBeInTheDocument();
	});

	it('stays put when the full issue cannot be read', async () => {
		await open();
		vi.mocked(api.integrations.sourceTicket).mockRejectedValue(new Error('gone'));
		await userEvent.click(await result());

		expect(await screen.findByText('gone')).toBeInTheDocument();
		expect(title()).toHaveValue('');
		expect(create()).toBeInTheDocument();
	});

	it('clears the import, keeping what the fields say', async () => {
		created();
		await open();
		await userEvent.click(await result());
		await userEvent.click(await screen.findByRole('button', { name: 'Clear' }));

		expect(title()).toHaveValue('Bad invoice');
		await userEvent.click(create());
		expect(lastBody()).not.toHaveProperty('sourceKey');
	});

	it('goes back to writing by hand on the first choice', async () => {
		await open();
		await choose('Tracker', 'Choose a tracker…');
		expect(screen.queryByRole('searchbox')).not.toBeInTheDocument();
	});

	it('stops at the warnings of an import that lost a file', async () => {
		const saved = created({ warnings: ['trace.log was too large'] });
		const onsaved = vi.fn();
		vi.mocked(api.integrations.list).mockResolvedValue([]);
		render(TicketForm, { projects, onsaved });
		await userEvent.type(title(), 'A');
		await userEvent.type(goal(), 'B');
		await userEvent.click(create());

		const warned = await screen.findByText('trace.log was too large');
		expect(warned).toBeInTheDocument();
		expect(onsaved).not.toHaveBeenCalled();
		expect(within(document.body).queryByRole('button', { name: 'Create ticket' })).toBeNull();

		await userEvent.click(screen.getByRole('button', { name: 'Continue to the ticket' }));
		expect(onsaved).toHaveBeenCalledWith(saved);
	});
});
