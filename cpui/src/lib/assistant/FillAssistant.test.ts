import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { createRawSnippet, type Snippet } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import {
	runFill,
	ticketFill,
	type AssistantFrame,
	type AssistantMessage,
	type AssistantStatus,
	type TicketFields
} from '$lib/domain';
import FillAssistant from './FillAssistant.svelte';
import { FillGuard, HandoffGuard, type PageGuard } from './fillGuard.svelte';

vi.mock('$lib/api', () => ({
	api: {
		assistant: {
			status: vi.fn(),
			open: vi.fn(),
			session: vi.fn(),
			warm: vi.fn(),
			send: vi.fn(),
			cancel: vi.fn(),
			stream: vi.fn()
		}
	}
}));

const status = (over: Partial<AssistantStatus> = {}): AssistantStatus => ({
	enabled: true,
	choice: { integration: 'int-1', model: '', skillsets: [] },
	integrations: [{ id: 'int-1', name: 'mine', kind: 'claude-code' }],
	models: [],
	pod: { state: 'running' },
	repos: [],
	pages: { ticket: { presets: ['Turn my notes into a ticket.'], repos: true } },
	...over
});
const session = (id = 's1') => ({
	id,
	kind: 'ticket' as const,
	workshop: 'w1',
	subject: 'draft-1',
	createdAt: 'T',
	updatedAt: 'T'
});
const msg = (
	seq: number,
	role: 'person' | 'assistant',
	text: string,
	over: Partial<AssistantMessage> = {}
): AssistantMessage => ({ sessionId: 's1', seq, role, text, costUsd: 0, at: 'T', ...over });

let push: (f: AssistantFrame) => void = () => {};
// typing warms at most once a minute on one module-level clock: each test
// starts well past the last one's warm.
let now = Date.UTC(2026, 0, 1);

type Props = {
	open: boolean;
	kind: 'flow' | 'ticket' | 'skill' | 'dashboard' | 'run';
	subject: string;
	workshop?: string;
	label?: string;
	guard: PageGuard;
	pageState: () => unknown;
	handoff?: Snippet;
	workshopPicker?: Snippet;
};

function setup(over: Partial<Props> = {}) {
	let form: TicketFields = { title: '', goal: 'notes', changeType: 'feat', typeId: '' };
	const guard = new FillGuard<TicketFields>(
		() => ({ ...form }),
		(v) => (form = { ...v }),
		ticketFill
	);
	const r = render(FillAssistant, {
		open: true,
		kind: 'ticket',
		subject: 'draft-1',
		workshop: 'w1',
		guard,
		pageState: () => ({ ...form }),
		...over
	});
	return { guard, form: () => form, ...r };
}

const dialog = () => screen.queryByRole('dialog');
const gone = () => waitFor(() => expect(dialog()).not.toBeInTheDocument());
const textbox = () => screen.getByRole('textbox', { name: 'What should this page hold?' });
const frame = (f: Record<string, unknown>) =>
	push({ seq: 1, sessionId: 's1', ...f } as AssistantFrame);

beforeEach(() => {
	vi.resetAllMocks();
	vi.useFakeTimers({ toFake: ['Date'] });
	vi.setSystemTime((now += 10 * 60_000));
	vi.mocked(api.assistant.status).mockResolvedValue(status());
	vi.mocked(api.assistant.open).mockResolvedValue({
		session: session(),
		messages: [],
		busy: false
	});
	vi.mocked(api.assistant.warm).mockResolvedValue(undefined as never);
	vi.mocked(api.assistant.cancel).mockResolvedValue(undefined as never);
	vi.mocked(api.assistant.send).mockResolvedValue(msg(1, 'person', 'q'));
	vi.mocked(api.assistant.stream).mockImplementation((fn) => {
		push = fn;
		return () => {};
	});
});
afterEach(() => vi.useRealTimers());

describe('FillAssistant', () => {
	it.each([
		['flow', 'Fill with the assistant · Flow'],
		['ticket', 'Fill with the assistant · Ticket'],
		['skill', 'Fill with the assistant · Skill'],
		['dashboard', 'Fill with the assistant · Dashboard'],
		['run', 'Fill with the assistant · Run']
	] as const)('names the %s page in its title', (kind, title) => {
		setup({ kind });
		expect(screen.getByRole('dialog', { name: title })).toBeInTheDocument();
	});

	it('names the page by its label when given', () => {
		setup({ label: 'Bug report' });
		expect(
			screen.getByRole('dialog', { name: 'Fill with the assistant · Bug report' })
		).toBeInTheDocument();
	});

	it('starts a fresh conversation on each opening', async () => {
		vi.mocked(api.assistant.open)
			.mockResolvedValueOnce({ session: session('s1'), messages: [], busy: false })
			.mockResolvedValueOnce({ session: session('s2'), messages: [], busy: false });
		const p = setup();
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.keyboard('{Escape}');
		await gone();
		// The page closes it as the modal did, then opens it again.
		await p.rerender({ open: false });
		await p.rerender({ open: true });
		await screen.findByText('Turn my notes into a ticket.');
		expect(api.assistant.open).toHaveBeenCalledTimes(2);
		expect(api.assistant.open).toHaveBeenNthCalledWith(1, 'ticket', 'draft-1', 'w1', {
			fresh: true
		});
		expect(api.assistant.open).toHaveBeenNthCalledWith(2, 'ticket', 'draft-1', 'w1', {
			fresh: true
		});
		await userEvent.type(textbox(), 'go{Enter}');
		expect(api.assistant.send).toHaveBeenCalledWith('s2', 'go', expect.anything());
	});

	it('introduces itself, and asks a preset with the page as it stands', async () => {
		setup();
		expect(await screen.findByText(/Say what this page should hold/)).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: 'Turn my notes into a ticket.' }));
		expect(api.assistant.send).toHaveBeenCalledWith('s1', 'Turn my notes into a ticket.', {
			title: '',
			goal: 'notes',
			changeType: 'feat',
			typeId: ''
		});
	});

	it('warms the pod as the person types, and Shift+Enter is a new line', async () => {
		setup();
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.type(textbox(), 'the login{Shift>}{Enter}{/Shift}page');
		expect(api.assistant.warm).toHaveBeenCalledTimes(1);
		expect(api.assistant.send).not.toHaveBeenCalled();
		expect(textbox()).toHaveValue('the login\npage');
		await userEvent.click(screen.getByRole('button', { name: 'Ask' }));
		expect(api.assistant.send).toHaveBeenCalledWith('s1', 'the login\npage', expect.anything());
	});

	it('shows only that it is brewing while the answer is written, and Stop stops it', async () => {
		setup();
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.type(textbox(), 'go{Enter}');
		frame({ kind: 'state', busy: true });
		frame({ kind: 'delta', text: 'Reading the code', tool: 'Read README' });
		expect(await screen.findByText('Brewing your ticket…')).toBeInTheDocument();
		expect(screen.queryByText(/Reading the code/)).not.toBeInTheDocument();
		expect(screen.queryByText(/Read README/)).not.toBeInTheDocument();
		expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Ask' })).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: 'Stop' }));
		expect(api.assistant.cancel).toHaveBeenCalledWith('s1');
	});

	it('closes once the answer filled the page: the page holds the fill', async () => {
		const p = setup();
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.type(textbox(), 'the login page fails{Enter}');
		frame({ kind: 'state', busy: true });
		frame({
			kind: 'fill',
			page: 'ticket',
			subject: 'draft-1',
			fill: { title: 'Fix the login page' }
		});
		expect(dialog()).toBeInTheDocument();
		frame({ kind: 'state', busy: false });
		await gone();
		expect(p.form().title).toBe('Fix the login page');
		expect(p.guard.changed).toEqual(['title']);
	});

	it('fills the page from the record when a gap lost the fill, once, with Undo', async () => {
		const p = setup();
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.type(textbox(), 'the login page fails{Enter}');
		frame({ kind: 'state', busy: true });
		// The engine restarted: the answer is recorded, its fill frame gone.
		vi.mocked(api.assistant.session).mockResolvedValue({
			session: session(),
			messages: [
				msg(1, 'person', 'the login page fails'),
				msg(2, 'assistant', 'Done.', { fill: { title: 'Fix the login page' } })
			],
			busy: false
		});
		push({ kind: 'gap' } as AssistantFrame);
		await gone();
		expect(api.assistant.session).toHaveBeenCalledWith('s1');
		expect(p.form().title).toBe('Fix the login page');
		expect(p.guard.changed).toEqual(['title']);
		// A frame of the same answer arriving late fills nothing again.
		p.guard.revert();
		frame({ kind: 'fill', page: 'ticket', subject: 'draft-1', fill: { title: 'Fix it again' } });
		expect(p.form()).toEqual({ title: '', goal: 'notes', changeType: 'feat', typeId: '' });
		expect(p.guard.changed).toEqual([]);
	});

	it('shows an answer that filled nothing, and keeps the input for a reply', async () => {
		const p = setup();
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.type(textbox(), 'go{Enter}');
		frame({ kind: 'state', busy: true });
		frame({ kind: 'message', message: msg(2, 'assistant', 'Which page fails?') });
		frame({ kind: 'state', busy: false });
		expect(await screen.findByText('Which page fails?')).toBeInTheDocument();
		expect(dialog()).toBeInTheDocument();
		expect(screen.queryByText(/Say what this page should hold/)).not.toBeInTheDocument();
		await userEvent.type(textbox(), 'the login{Enter}');
		expect(api.assistant.send).toHaveBeenLastCalledWith('s1', 'the login', expect.anything());
		expect(p.form().title).toBe('');
	});

	it('shows why an answer failed', async () => {
		setup();
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.type(textbox(), 'go{Enter}');
		frame({ kind: 'message', message: msg(2, 'assistant', '', { error: 'harness crashed' }) });
		frame({ kind: 'state', busy: false });
		expect(await screen.findByText('harness crashed')).toBeInTheDocument();
	});

	it('keeps the question when it could not be asked', async () => {
		vi.mocked(api.assistant.send).mockRejectedValue(new Error('engine down'));
		setup();
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.type(textbox(), 'go{Enter}');
		expect(await screen.findByText('engine down')).toBeInTheDocument();
		expect(textbox()).toHaveValue('go');
	});

	it('stops the answer when closed while brewing, and drops its late fill', async () => {
		const p = setup();
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.type(textbox(), 'go{Enter}');
		frame({ kind: 'state', busy: true });
		await screen.findByText('Brewing your ticket…');
		await userEvent.keyboard('{Escape}');
		await gone();
		expect(api.assistant.cancel).toHaveBeenCalledWith('s1');
		frame({ kind: 'fill', page: 'ticket', subject: 'draft-1', fill: { title: 'late' } });
		frame({ kind: 'state', busy: false });
		expect(p.form().title).toBe('');
		expect(p.guard.changed).toEqual([]);
	});

	it('drops the old page’s conversation when another page takes the component', async () => {
		vi.mocked(api.assistant.open)
			.mockResolvedValueOnce({ session: session('s1'), messages: [], busy: false })
			.mockResolvedValueOnce({
				session: { ...session('s2'), subject: 'T-7' },
				messages: [],
				busy: false
			});
		const p = setup();
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.type(textbox(), 'go{Enter}');
		frame({ kind: 'state', busy: true });
		await screen.findByText('Brewing your ticket…');
		const old = push;
		// Back to another ticket: the same component, another subject and page.
		const other = { title: '', goal: 'the other ticket', changeType: 'feat', typeId: '' };
		await p.rerender({ subject: 'T-7', pageState: () => ({ ...other }) });
		expect(api.assistant.cancel).toHaveBeenCalledWith('s1');
		await waitFor(() =>
			expect(api.assistant.open).toHaveBeenLastCalledWith('ticket', 'T-7', 'w1', { fresh: true })
		);
		await screen.findByText('Turn my notes into a ticket.');
		await userEvent.type(textbox(), 'tidy it{Enter}');
		expect(api.assistant.send).toHaveBeenLastCalledWith('s2', 'tidy it', other);
		expect(p.guard.asking).toBe(true);
		// The old session's late answer neither fills the new page nor ends
		// its question.
		old({ seq: 5, sessionId: 's1', kind: 'state', busy: false } as AssistantFrame);
		old({
			seq: 6,
			sessionId: 's1',
			kind: 'fill',
			page: 'ticket',
			subject: 'draft-1',
			fill: { title: 'the old ticket' }
		} as AssistantFrame);
		expect(p.form().title).toBe('');
		expect(p.guard.asking).toBe(true);
	});

	it('stays open after a fill that belongs elsewhere, to hand it off', async () => {
		const guard = new HandoffGuard(runFill);
		const handoff = createRawSnippet(() => ({
			render: () => '<p>Take it to the flow editor</p>'
		}));
		setup({ kind: 'run', guard, handoff });
		await screen.findByText(/Say what this page should hold/);
		expect(screen.queryByText('Take it to the flow editor')).not.toBeInTheDocument();
		await userEvent.type(textbox(), 'fix the flow{Enter}');
		frame({ kind: 'state', busy: true });
		frame({ kind: 'fill', page: 'run', subject: 'draft-1', fill: { flow: 'f', yaml: 'a: 1' } });
		frame({ kind: 'state', busy: false });
		expect(await screen.findByText('Take it to the flow editor')).toBeInTheDocument();
		expect(dialog()).toBeInTheDocument();
		expect(guard.handoff).toEqual({ flow: 'f', yaml: 'a: 1' });
	});

	it('says the assistant is off, why, and where to turn it on', async () => {
		vi.mocked(api.assistant.status).mockResolvedValue(
			status({ enabled: false, reason: 'No harness chosen' })
		);
		setup();
		expect(await screen.findByText('The assistant is off')).toBeInTheDocument();
		expect(screen.getByText('No harness chosen')).toBeInTheDocument();
		expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
		expect(api.assistant.open).not.toHaveBeenCalled();
		const link = screen.getByRole('link', { name: 'Choose its harness' });
		expect(link).toHaveAttribute('href', '/system/assistant');
		link.addEventListener('click', (e) => e.preventDefault());
		await userEvent.click(link);
		await gone();
	});

	it('shows the page’s workshop choice above the conversation', async () => {
		const workshopPicker = createRawSnippet(() => ({
			render: () => '<label>Read a workshop’s repositories<select></select></label>'
		}));
		setup({ kind: 'skill', workshopPicker });
		expect(
			within(screen.getByRole('dialog')).getByLabelText('Read a workshop’s repositories')
		).toBeInTheDocument();
	});

	it('starts afresh on a workshop chosen in the open modal, keeping the question', async () => {
		vi.mocked(api.assistant.open)
			.mockResolvedValueOnce({ session: session('s1'), messages: [], busy: false })
			.mockResolvedValueOnce({ session: session('s2'), messages: [], busy: false });
		const p = setup({ kind: 'skill', workshop: '' });
		await userEvent.type(await screen.findByRole('textbox'), 'notes');
		expect(api.assistant.status).toHaveBeenLastCalledWith(undefined);
		expect(api.assistant.open).toHaveBeenLastCalledWith('skill', 'draft-1', undefined, {
			fresh: true
		});
		await p.rerender({ workshop: 'w2' });
		await waitFor(() => expect(api.assistant.open).toHaveBeenCalledTimes(2));
		expect(api.assistant.status).toHaveBeenLastCalledWith('w2');
		expect(api.assistant.open).toHaveBeenLastCalledWith('skill', 'draft-1', 'w2', {
			fresh: true
		});
		expect(await screen.findByRole('textbox')).toHaveValue('notes');
		await userEvent.type(textbox(), '{Enter}');
		expect(api.assistant.send).toHaveBeenCalledWith('s2', 'notes', expect.anything());
	});

	it('hides the workshop choice while brewing; a workshop changed then stops the answer', async () => {
		const workshopPicker = createRawSnippet(() => ({
			render: () => '<label>Read a workshop’s repositories<select></select></label>'
		}));
		const p = setup({ kind: 'skill', workshopPicker });
		await screen.findByRole('textbox');
		expect(screen.getByLabelText('Read a workshop’s repositories')).toBeInTheDocument();
		await userEvent.type(textbox(), 'go{Enter}');
		frame({ kind: 'state', busy: true });
		await screen.findByText('Brewing your skill…');
		expect(screen.queryByLabelText('Read a workshop’s repositories')).not.toBeInTheDocument();
		await p.rerender({ workshop: 'w2' });
		expect(api.assistant.cancel).toHaveBeenCalledWith('s1');
		expect(await screen.findByLabelText('Read a workshop’s repositories')).toBeInTheDocument();
		expect(api.assistant.open).toHaveBeenLastCalledWith('skill', 'draft-1', 'w2', {
			fresh: true
		});
	});

	it('says when it could not start, and retries', async () => {
		vi.mocked(api.assistant.status).mockRejectedValueOnce(new Error('engine down'));
		setup();
		expect(await screen.findByText('engine down')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('Turn my notes into a ticket.')).toBeInTheDocument();
		expect(api.assistant.open).toHaveBeenLastCalledWith('ticket', 'draft-1', 'w1', {
			fresh: true
		});
	});
});
