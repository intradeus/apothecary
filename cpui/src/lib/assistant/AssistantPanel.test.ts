import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import {
	ticketFill,
	type AssistantFrame,
	type AssistantMessage,
	type AssistantStatus,
	type TicketFields
} from '$lib/domain';
import AssistantPanel from './AssistantPanel.svelte';
import { FillGuard } from './fillGuard.svelte';

vi.mock('$lib/api', () => ({
	api: {
		assistant: {
			status: vi.fn(),
			open: vi.fn(),
			send: vi.fn(),
			cancel: vi.fn(),
			setModel: vi.fn(),
			setSubject: vi.fn(),
			setChoice: vi.fn(),
			refresh: vi.fn(),
			messages: vi.fn(),
			stream: vi.fn()
		}
	}
}));

const status = (over: Partial<AssistantStatus> = {}): AssistantStatus => ({
	enabled: true,
	choice: { integration: 'int-1', model: '' },
	integrations: [{ id: 'int-1', name: 'mine', kind: 'claude-code' }],
	models: ['acme/one', 'acme/two'],
	pod: { state: 'running' },
	repos: [{ workshop: 'w1', name: 'api', ok: true, head: 'abc', at: 'T' }],
	pages: { ticket: { presets: ['Turn my notes into a ticket.'], repos: true } },
	...over
});
const session = {
	id: 's1',
	kind: 'ticket' as const,
	workshop: 'w1',
	subject: 'draft-1',
	model: 'acme/one',
	createdAt: 'T',
	updatedAt: 'T'
};
const msg = (
	seq: number,
	role: 'person' | 'assistant',
	text: string,
	over: Partial<AssistantMessage> = {}
): AssistantMessage => ({
	sessionId: 's1',
	seq,
	role,
	text,
	costUsd: 0,
	at: 'T',
	...over
});

let push: (f: AssistantFrame) => void = () => {};

function setup(
	init: TicketFields = { title: '', goal: 'notes', changeType: 'feat', typeId: '' },
	subject = 'draft-1'
) {
	let form = { ...init };
	const guard = new FillGuard<TicketFields>(
		() => ({ ...form }),
		(v) => (form = { ...v }),
		ticketFill
	);
	const context = vi.fn(() => ({ ...form }));
	const onsession = vi.fn();
	const r = render(AssistantPanel, {
		kind: 'ticket',
		subject,
		workshop: 'w1',
		guard,
		pageState: context,
		onsession
	});
	return {
		guard,
		context,
		onsession,
		form: () => form,
		edit: (f: Partial<TicketFields>) => (form = { ...form, ...f }),
		...r
	};
}

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.assistant.status).mockResolvedValue(status());
	vi.mocked(api.assistant.open).mockResolvedValue({ session, messages: [], busy: false });
	vi.mocked(api.assistant.stream).mockImplementation((fn) => {
		push = fn;
		return () => {};
	});
});

const textbox = () => screen.getByRole('textbox', { name: 'Ask the assistant…' });

describe('AssistantPanel', () => {
	it('opens the session about its subject, and offers the page’s presets', async () => {
		const p = setup();
		expect(await screen.findByText('Turn my notes into a ticket.')).toBeInTheDocument();
		expect(api.assistant.status).toHaveBeenCalledWith('w1');
		expect(api.assistant.open).toHaveBeenCalledWith('ticket', 'draft-1', 'w1');
		expect(screen.getByText('api')).toHaveAttribute('title', 'abc');
		expect(p.onsession).toHaveBeenCalledWith('s1');
	});

	it('asks with the page as it stands, streams the answer, and fills the page once', async () => {
		vi.mocked(api.assistant.send).mockResolvedValue(msg(1, 'person', 'the login page fails'));
		const p = setup();
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.type(textbox(), 'the login page fails{Enter}');
		expect(api.assistant.send).toHaveBeenCalledWith('s1', 'the login page fails', {
			title: '',
			goal: 'notes',
			changeType: 'feat',
			typeId: ''
		});
		expect(await screen.findByText('the login page fails')).toBeInTheDocument();
		push({ kind: 'state', seq: 1, sessionId: 's1', busy: true });
		push({ kind: 'delta', seq: 2, sessionId: 's1', text: 'Reading', tool: 'Read README' });
		expect(await screen.findByText('Read README')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Stop' })).toBeInTheDocument();
		push({
			kind: 'fill',
			seq: 3,
			sessionId: 's1',
			page: 'ticket',
			subject: 'draft-1',
			fill: { title: 'Fix the login page' }
		});
		// Another session's frames are not this panel's.
		push({
			kind: 'fill',
			seq: 4,
			sessionId: 's9',
			page: 'ticket',
			subject: 'x',
			fill: { title: 'theirs' }
		});
		push({
			kind: 'message',
			seq: 5,
			sessionId: 's1',
			message: msg(2, 'assistant', 'Filled the title.', { fill: { title: 'Fix the login page' } })
		});
		push({ kind: 'state', seq: 6, sessionId: 's1', busy: false });
		expect(await screen.findByText('Filled the title.')).toBeInTheDocument();
		expect(p.form().title).toBe('Fix the login page');
		expect(screen.getByRole('status')).toHaveTextContent('Filled: title');
		await userEvent.click(screen.getByRole('button', { name: 'Undo' }));
		expect(p.form().title).toBe('');
		expect(screen.queryByRole('status')).not.toBeInTheDocument();
	});

	it('holds a fill when the page changed since the question', async () => {
		vi.mocked(api.assistant.send).mockResolvedValue(msg(1, 'person', 'go'));
		const p = setup();
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.click(screen.getByRole('button', { name: 'Turn my notes into a ticket.' }));
		p.edit({ goal: 'newer notes' });
		push({
			kind: 'fill',
			seq: 3,
			sessionId: 's1',
			page: 'ticket',
			subject: 'draft-1',
			fill: { title: 'T', goal: 'G' }
		});
		expect(await screen.findByText(/You changed the page since you asked/)).toBeInTheDocument();
		expect(p.form().goal).toBe('newer notes');
		await userEvent.click(screen.getByRole('button', { name: 'Keep mine' }));
		expect(p.form().goal).toBe('newer notes');
		push({
			kind: 'fill',
			seq: 4,
			sessionId: 's1',
			page: 'ticket',
			subject: 'draft-1',
			fill: { title: 'T' }
		});
		await userEvent.click(await screen.findByRole('button', { name: 'Fill it anyway' }));
		expect(p.form()).toMatchObject({ title: 'T', goal: 'newer notes' });
	});

	it('gives a refused question back, and stops an answer', async () => {
		vi.mocked(api.assistant.send).mockRejectedValue(new Error('the session is answering'));
		setup();
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.type(textbox(), 'again{Enter}');
		expect(await screen.findByText('the session is answering')).toBeInTheDocument();
		expect(textbox()).toHaveValue('again');
		push({ kind: 'state', seq: 1, sessionId: 's1', busy: true });
		await userEvent.click(await screen.findByRole('button', { name: 'Stop' }));
		expect(api.assistant.cancel).toHaveBeenCalledWith('s1');
	});

	it('picks a model, refreshes the repositories, reloads after a gap', async () => {
		vi.mocked(api.assistant.setModel).mockResolvedValue({ ...session, model: 'acme/two' });
		vi.mocked(api.assistant.refresh).mockResolvedValue([
			{ workshop: 'w1', name: 'web', ok: false, error: '404', at: 'T' }
		]);
		setup();
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.click(screen.getByRole('combobox', { name: 'Model' }));
		await userEvent.click(screen.getByRole('option', { name: 'acme/two' }));
		expect(api.assistant.setModel).toHaveBeenCalledWith('s1', 'acme/two');
		await userEvent.click(screen.getByRole('button', { name: /Refresh/ }));
		expect(await screen.findByText('web')).toHaveAttribute('title', 'web: 404');
		push({ kind: 'repos', seq: 2, workshop: 'w1', text: 'fetching' });
		expect(await screen.findByText('fetching…')).toBeInTheDocument();
		push({ kind: 'repos', seq: 3, workshop: 'other', repos: [] });
		// Frames lost: the record says where the session is.
		vi.mocked(api.assistant.open).mockResolvedValue({
			session,
			messages: [msg(1, 'assistant', 'from the record')],
			busy: false
		});
		push({ kind: 'gap' });
		expect(await screen.findByText('from the record')).toBeInTheDocument();
		push({ kind: 'status', seq: 4, sessionId: 's1', text: 'starting your assistant' });
		push({ kind: 'state', seq: 5, sessionId: 's1', busy: true });
		expect(await screen.findByText('starting your assistant')).toBeInTheDocument();
	});

	it('fills only for a question it asked; an earlier fill on a click', async () => {
		vi.mocked(api.assistant.open).mockResolvedValue({
			session,
			messages: [msg(2, 'assistant', 'Filled the title.', { fill: { title: 'From before' } })],
			busy: false
		});
		const p = setup();
		await screen.findByText('Filled the title.');
		// A fill this panel did not ask for (replayed, another tab's): ignored.
		push({
			kind: 'fill',
			seq: 9,
			sessionId: 's1',
			page: 'ticket',
			subject: 'draft-1',
			fill: { title: 'x' }
		});
		expect(p.form().title).toBe('');
		await userEvent.click(screen.getByRole('button', { name: 'Fill the page with this' }));
		expect(p.form().title).toBe('From before');
		await userEvent.click(screen.getByRole('button', { name: 'Undo' }));
		expect(p.form().title).toBe('');
	});

	it('knows a session answering when it opens, and that it ended after a gap', async () => {
		vi.mocked(api.assistant.open).mockResolvedValue({ session, messages: [], busy: true });
		setup();
		expect(await screen.findByRole('button', { name: 'Stop' })).toBeInTheDocument();
		vi.mocked(api.assistant.open).mockResolvedValue({ session, messages: [], busy: false });
		push({ kind: 'gap' });
		expect(await screen.findByRole('button', { name: 'Ask' })).toBeInTheDocument();
	});

	it('opens the conversation of another subject rather than moving this one', async () => {
		const p = setup(undefined, 'APO-3');
		vi.mocked(api.assistant.open).mockResolvedValue({
			session: { ...session, subject: 'APO-3' },
			messages: [],
			busy: false
		});
		await screen.findByText('Turn my notes into a ticket.');
		await waitFor(() => expect(api.assistant.open).toHaveBeenCalledTimes(1));
		vi.mocked(api.assistant.open).mockResolvedValue({
			session: { ...session, id: 's2', subject: 'APO-4' },
			messages: [],
			busy: false
		});
		await p.rerender({
			kind: 'ticket',
			subject: 'APO-4',
			workshop: 'w1',
			guard: p.guard,
			pageState: p.context
		});
		await waitFor(() =>
			expect(api.assistant.open).toHaveBeenLastCalledWith('ticket', 'APO-4', 'w1')
		);
		expect(api.assistant.setSubject).not.toHaveBeenCalled();
	});

	it('moves a draft’s session to what it was saved as', async () => {
		vi.mocked(api.assistant.setSubject).mockResolvedValue({ ...session, subject: 'APO-12' });
		const p = setup();
		await screen.findByText('Turn my notes into a ticket.');
		await p.rerender({
			kind: 'ticket',
			subject: 'APO-12',
			workshop: 'w1',
			guard: p.guard,
			pageState: p.context
		});
		await waitFor(() => expect(api.assistant.setSubject).toHaveBeenCalledWith('s1', 'APO-12'));
	});

	it('says why it is off, and takes a harness choice', async () => {
		vi.mocked(api.assistant.status)
			.mockResolvedValueOnce(
				status({
					enabled: false,
					reason: 'choose the harness',
					choice: { integration: '', model: '' }
				})
			)
			.mockResolvedValueOnce(status());
		setup();
		expect(await screen.findByText('choose the harness')).toBeInTheDocument();
		expect(api.assistant.open).not.toHaveBeenCalled();
		await userEvent.click(screen.getByRole('button', { name: 'Use this harness' }));
		expect(api.assistant.setChoice).toHaveBeenCalledWith('int-1');
		expect(await screen.findByText('Turn my notes into a ticket.')).toBeInTheDocument();
	});

	it('says when there is no harness to run on, and when it cannot load', async () => {
		vi.mocked(api.assistant.status).mockResolvedValueOnce(
			status({ enabled: false, reason: 'no harness', integrations: [], pod: { state: 'absent' } })
		);
		setup();
		expect(await screen.findByText(/Add a Claude Code or opencode harness/)).toBeInTheDocument();
		vi.mocked(api.assistant.status).mockRejectedValueOnce(new Error('engine down'));
		setup();
		expect(await screen.findByText('engine down')).toBeInTheDocument();
	});
});
