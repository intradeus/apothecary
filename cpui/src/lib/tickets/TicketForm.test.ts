import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { TicketDetail, TrackerType } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { aProject, aTicketDetail, aViewerProject } from '$test/fixtures';
import { choose } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import TicketForm from './TicketForm.svelte';

vi.mock('$lib/api', () => ({
	api: {
		tickets: { create: vi.fn(), update: vi.fn(), attachments: vi.fn() },
		projects: { ticketTypes: vi.fn() },
		files: { upload: vi.fn(), limits: vi.fn(), attachmentUrl: vi.fn() }
	}
}));
vi.mock(
	'$lib/assistant/FillAssistant.svelte',
	() => import('$test/fixtures/FillAssistantStub.svelte')
);

const project = aProject({ id: 'p1', ticketSourceId: 'own' });
const title = () => screen.getByLabelText('Title');
const goal = () => screen.getByLabelText('Goal');
const branch = () => screen.getByLabelText(/Base branch/);
const create = () => screen.getByRole('button', { name: 'Create ticket' });
const type = (id: string, name: string, changeType: TrackerType['changeType'], position = 0) =>
	({ id, integrationId: 'own', remoteId: '', name, changeType, position }) as TrackerType;

function setup(over: Record<string, unknown> = {}) {
	const onsaved = vi.fn();
	const r = render(TicketForm, { project, onsaved, ...over });
	return { onsaved, ...r };
}
const created = (over: Partial<TicketDetail> = {}) => {
	const saved = aTicketDetail({ internal: true, ...over });
	vi.mocked(api.tickets.create).mockResolvedValue(saved);
	return saved;
};
const lastBody = () => vi.mocked(api.tickets.create).mock.calls.at(-1)![1];

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.projects.ticketTypes).mockResolvedValue([]);
	vi.mocked(api.tickets.attachments).mockResolvedValue({ attachments: [], remote: [] });
	vi.mocked(api.files.limits).mockResolvedValue(null as never);
	toasts.list = [];
});

describe('TicketForm, without ticket:write', () => {
	it('cannot create, and says why', async () => {
		setup({ project: aViewerProject({ id: 'p1', ticketSourceId: 'own' }) });
		await userEvent.type(title(), 'T');
		await userEvent.type(goal(), 'G');
		expect(create()).toBeDisabled();
		expect(create()).toHaveAttribute('title', 'Your role in this workshop does not allow this');
	});
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

	it("creates the ticket in the workshop's source, trimmed, and hands it to the page", async () => {
		const saved = created();
		const { onsaved } = setup();
		await userEvent.type(title(), ' Add keys ');
		await userEvent.type(goal(), ' Safe retries ');
		await userEvent.click(create());

		expect(api.tickets.create).toHaveBeenCalledWith('p1', {
			title: 'Add keys',
			goal: 'Safe retries',
			baseBranch: '',
			changeType: 'feat',
			typeId: undefined,
			attachmentIds: [],
			assisted: false
		});
		expect(onsaved).toHaveBeenCalledWith(saved);
		expect(toasts.list.at(-1)?.text).toBe('Ticket created');
	});

	it('uploads files first and attaches them', async () => {
		created();
		vi.mocked(api.files.upload).mockResolvedValue([{ id: 'f1' }] as never);
		const { container } = setup();
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
		const { container, onsaved } = setup();
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
		vi.mocked(api.tickets.create).mockRejectedValue(new Error('remote source'));
		const { onsaved } = setup();
		await userEvent.type(title(), 'Add keys');
		await userEvent.type(goal(), 'Safe retries');
		await userEvent.click(create());

		expect(await screen.findByText('remote source')).toBeInTheDocument();
		expect(title()).toHaveValue('Add keys');
		expect(onsaved).not.toHaveBeenCalled();
	});

	it('sends once on a double submit', async () => {
		const save = deferred<TicketDetail>();
		vi.mocked(api.tickets.create).mockReturnValue(save.promise);
		setup();
		await userEvent.type(title(), 'Add keys');
		await userEvent.type(goal(), 'Safe retries');
		await userEvent.click(create());
		await userEvent.type(title(), '{Enter}');

		expect(api.tickets.create).toHaveBeenCalledOnce();
		save.resolve(aTicketDetail());
	});

	it('shows Cancel only when the page wants one', async () => {
		const oncancel = vi.fn();
		setup({ oncancel });
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(oncancel).toHaveBeenCalledOnce();
	});
});

describe('TicketForm, the type', () => {
	it("offers the workshop source's types, and a type picked brings its change type", async () => {
		created();
		vi.mocked(api.projects.ticketTypes).mockResolvedValue([
			type('t2', 'Chore', 'chore', 1),
			type('t1', 'Bug', 'fix', 0)
		]);
		setup();
		// Read through the workshop, not its owner's source integration.
		await waitFor(() => expect(api.projects.ticketTypes).toHaveBeenCalledWith('p1'));
		await choose('Type', 'Bug');
		expect(screen.getByRole('combobox', { name: 'Change type' })).toHaveTextContent('fix');
		await userEvent.type(title(), 'A');
		await userEvent.type(goal(), 'B');
		await userEvent.click(create());
		expect(lastBody()).toMatchObject({ typeId: 't1', changeType: 'fix' });
	});

	it('keeps the change type when no type is picked', async () => {
		vi.mocked(api.projects.ticketTypes).mockResolvedValue([type('t1', 'Bug', 'fix')]);
		setup();
		await waitFor(() => expect(api.projects.ticketTypes).toHaveBeenCalled());
		await choose('Type', 'No type');
		expect(screen.getByRole('combobox', { name: 'Change type' })).toHaveTextContent('feat');
	});

	it('still works without the type list', async () => {
		vi.mocked(api.projects.ticketTypes).mockRejectedValue(new Error('down'));
		setup();
		await waitFor(() => expect(api.projects.ticketTypes).toHaveBeenCalled());
		expect(screen.getByRole('combobox', { name: 'Type' })).toHaveTextContent('No type');
	});

	it('takes a change type by hand', async () => {
		created();
		setup();
		await choose('Change type', 'docs');
		await userEvent.type(title(), 'A');
		await userEvent.type(goal(), 'B');
		await userEvent.click(create());
		expect(lastBody().changeType).toBe('docs');
	});
});

describe('TicketForm, the branch', () => {
	it("is optional: the workshop's format only names its placeholder", async () => {
		setup({
			project: aProject({ repo: { ...project.repo, branchFormat: '{ticket_type}/{ticket_title}' } })
		});
		expect(branch()).toHaveValue('');
		await userEvent.type(title(), 'Add Keys');
		expect(branch()).toHaveAttribute('placeholder', 'feat/add-keys');
		expect(branch()).toHaveValue('');
	});

	it('sends the branch typed', async () => {
		created();
		setup();
		await userEvent.type(title(), 'A');
		await userEvent.type(goal(), 'B');
		await userEvent.type(branch(), ' feat/mine ');
		await userEvent.click(create());
		expect(lastBody().baseBranch).toBe('feat/mine');
	});
});

describe('TicketForm, editing', () => {
	const existing = aTicketDetail({
		key: 't1',
		internal: true,
		title: 'Old title',
		body: 'Old goal',
		baseBranch: 'feat/old',
		changeType: 'fix',
		typeId: 'ty',
		attachmentIds: ['a1']
	});

	it('starts from the ticket', () => {
		setup({ existing });
		expect(title()).toHaveValue('Old title');
		expect(goal()).toHaveValue('Old goal');
		expect(branch()).toHaveValue('feat/old');
		expect(screen.getByRole('combobox', { name: 'Change type' })).toHaveTextContent('fix');
	});

	it('shows what is already attached', async () => {
		vi.mocked(api.tickets.attachments).mockResolvedValue({
			attachments: [{ id: 'a1', filename: 'spec.md' }] as never,
			remote: []
		});
		setup({ existing });
		expect(api.tickets.attachments).toHaveBeenCalledWith('p1', 't1');
		expect(await screen.findByText(/spec\.md/)).toBeInTheDocument();
	});

	it('survives failing to list what is attached', async () => {
		vi.mocked(api.tickets.attachments).mockRejectedValue(new Error('down'));
		setup({ existing });
		await waitFor(() => expect(api.tickets.attachments).toHaveBeenCalled());
		expect(title()).toHaveValue('Old title');
	});

	it('saves over the ticket, keeping its attachments', async () => {
		const saved = aTicketDetail({ key: 't1' });
		vi.mocked(api.tickets.update).mockResolvedValue(saved);
		vi.mocked(api.files.upload).mockResolvedValue([{ id: 'a2' }] as never);
		const { container, onsaved } = setup({ existing });
		await userEvent.upload(
			container.querySelector('input[type=file]') as HTMLInputElement,
			new File(['x'], 'more.md')
		);
		await userEvent.click(screen.getByRole('button', { name: 'Save ticket' }));

		expect(api.tickets.update).toHaveBeenCalledWith('p1', 't1', {
			title: 'Old title',
			goal: 'Old goal',
			baseBranch: 'feat/old',
			changeType: 'fix',
			typeId: 'ty',
			attachmentIds: ['a1', 'a2'],
			assisted: false
		});
		expect(api.tickets.create).not.toHaveBeenCalled();
		expect(onsaved).toHaveBeenCalledWith(saved);
		expect(toasts.list.at(-1)?.text).toBe('Ticket saved');
	});
});

describe('TicketForm, the assistant', () => {
	const openIt = () =>
		userEvent.click(screen.getByRole('button', { name: 'Fill with the assistant' }));
	const modal = () => screen.getByRole('region', { name: 'Fill assistant' });
	const offer = async (fill: unknown) => {
		const box = screen.getByRole('textbox', { name: 'fill' });
		await userEvent.clear(box);
		box.focus();
		await userEvent.paste(JSON.stringify(fill));
		await userEvent.click(screen.getByRole('button', { name: 'offer the fill' }));
	};

	it('opens on a new ticket as a draft, reads the unsaved form, and fills it unsaved', async () => {
		vi.mocked(api.projects.ticketTypes).mockResolvedValue([type('t1', 'Bug', 'fix')]);
		setup();
		expect(screen.queryByRole('region', { name: 'Fill assistant' })).not.toBeInTheDocument();
		await userEvent.type(goal(), 'my notes');
		await openIt();
		expect(modal().dataset.kind).toBe('ticket');
		expect(modal().dataset.subject).toMatch(/^draft-/);
		expect(modal().dataset.workshop).toBe('p1');

		await userEvent.click(screen.getByRole('button', { name: 'read the page' }));
		expect(JSON.parse(screen.getByRole('status', { name: 'page state' }).textContent!)).toEqual({
			title: '',
			goal: 'my notes',
			change_type: 'feat',
			type_id: '',
			base_branch: '',
			issue_types: [{ id: 't1', name: 'Bug', change_type: 'fix' }]
		});

		await offer({ title: 'Fix the login', goal: 'It works.', change_type: 'fix', type_id: 't1' });
		expect(title()).toHaveValue('Fix the login');
		expect(goal()).toHaveValue('It works.');
		expect(api.tickets.create).not.toHaveBeenCalled();
		expect(screen.getByText(/^Filled:/)).toHaveTextContent('title');
		await userEvent.click(screen.getByRole('button', { name: 'Undo' }));
		expect(title()).toHaveValue('');
		expect(goal()).toHaveValue('my notes');
		expect(screen.queryByText(/^Filled:/)).not.toBeInTheDocument();
	});

	it('creates the ticket from what was filled', async () => {
		created({ key: 'APO-7' });
		const { onsaved } = setup();
		await openIt();
		await offer({ title: 'T', goal: 'G' });
		await userEvent.click(create());
		await waitFor(() => expect(onsaved).toHaveBeenCalled());
		expect(lastBody()).toMatchObject({ title: 'T', goal: 'G', assisted: true });
		expect(screen.queryByText(/^Filled:/)).not.toBeInTheDocument();
	});

	it('is not the assistant’s when it was never opened', async () => {
		created();
		const { onsaved } = setup();
		await userEvent.type(title(), 'T');
		await userEvent.type(goal(), 'G');
		await userEvent.click(create());
		await waitFor(() => expect(onsaved).toHaveBeenCalled());
		expect(lastBody().assisted).toBe(false);
	});

	it('is about the ticket’s key when editing one, and closes', async () => {
		setup({ existing: aTicketDetail({ key: 'APO-3', internal: true }) });
		await openIt();
		expect(modal().dataset.subject).toBe('APO-3');
		await userEvent.click(screen.getByRole('button', { name: 'close the assistant' }));
		expect(screen.queryByRole('region', { name: 'Fill assistant' })).not.toBeInTheDocument();
	});
});
