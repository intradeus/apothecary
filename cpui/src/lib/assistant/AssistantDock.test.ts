import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { page } from '$app/state';
import { api } from '$lib/api';
import type {
	AssistantFrame,
	AssistantMessage,
	AssistantSession,
	AssistantStatus
} from '$lib/domain';
import { ticketFill, type TicketFields } from '$lib/domain';
import { assistant, type PageAssist } from './assistant.svelte';
import AssistantDock from './AssistantDock.svelte';
import { FillGuard } from './fillGuard.svelte';

vi.mock('$lib/api', () => ({
	api: {
		assistant: {
			status: vi.fn(),
			open: vi.fn(),
			warm: vi.fn(),
			send: vi.fn(),
			cancel: vi.fn(),
			stream: vi.fn()
		}
	}
}));
vi.mock('$app/state', async () => {
	const { bindable } = await import('$test/fixtures/app.svelte');
	return {
		page: bindable({
			params: { workshop: 'p1' } as Record<string, string | undefined>,
			url: new URL('http://localhost/w/p1/runs')
		})
	};
});

const status = (over: Partial<AssistantStatus> = {}): AssistantStatus => ({
	enabled: true,
	choice: { integration: 'int-1', model: '', skillsets: [] },
	integrations: [{ id: 'int-1', name: 'mine', kind: 'claude-code' }],
	models: [],
	pod: { state: 'running' },
	repos: [],
	pages: { general: { presets: [], repos: false } },
	...over
});
const session = (id = 's1') => ({
	id,
	kind: 'general' as const,
	workshop: 'p1',
	subject: 'palette',
	createdAt: 'T',
	updatedAt: 'T'
});
const message = (seq: number, text: string, sessionId = 's1') => ({
	sessionId,
	seq,
	role: 'assistant' as const,
	text,
	costUsd: 0,
	at: 'T'
});

let push: (f: AssistantFrame) => void = () => {};
const stop = vi.fn();

const button = () => screen.getByRole('button', { name: 'Ask the assistant' });
const sheet = () => screen.queryByRole('complementary', { name: 'Assistant' });

beforeEach(() => {
	vi.resetAllMocks();
	assistant.close();
	assistant.chosen = 'general';
	page.params.workshop = 'p1';
	vi.mocked(api.assistant.status).mockResolvedValue(status());
	vi.mocked(api.assistant.warm).mockResolvedValue(undefined as never);
	vi.mocked(api.assistant.open).mockResolvedValue({
		session: session(),
		messages: [message(1, 'Hello there.')],
		busy: false
	});
	vi.mocked(api.assistant.stream).mockImplementation((fn, ready) => {
		push = fn;
		ready?.();
		return stop;
	});
});

describe('AssistantDock', () => {
	it('is a named button with a tooltip, opened from the keyboard', async () => {
		render(AssistantDock);
		expect(button()).toHaveAttribute('title', expect.stringMatching(/acts once you confirm/));
		expect(sheet()).not.toBeInTheDocument();
		await userEvent.tab();
		expect(button()).toHaveFocus();
		await userEvent.keyboard('{Enter}');
		expect(sheet()).toBeInTheDocument();
	});

	it('opens the person’s general conversation about the workshop on screen', async () => {
		render(AssistantDock);
		await userEvent.click(button());
		expect(await screen.findByText('Hello there.')).toBeInTheDocument();
		expect(api.assistant.status).toHaveBeenCalledWith('p1');
		expect(api.assistant.open).toHaveBeenCalledWith('general', 'palette', 'p1', {
			fresh: undefined
		});
	});

	it('reads no workshop off a workshop’s pages, and follows the route to another', async () => {
		page.params.workshop = undefined;
		render(AssistantDock);
		await waitFor(() =>
			expect(api.assistant.open).toHaveBeenCalledWith('general', 'palette', undefined, {
				fresh: undefined
			})
		);
		page.params.workshop = 'p2';
		await waitFor(() =>
			expect(api.assistant.open).toHaveBeenLastCalledWith('general', 'palette', 'p2', {
				fresh: undefined
			})
		);
	});

	it('sends the page the person is on with a question', async () => {
		vi.mocked(api.assistant.send).mockResolvedValue({
			...message(2, 'retry it'),
			role: 'person'
		});
		render(AssistantDock);
		await userEvent.click(button());
		await screen.findByText('Hello there.');
		await userEvent.type(screen.getByRole('textbox'), 'retry it{Enter}');
		expect(api.assistant.send).toHaveBeenCalledWith('s1', 'retry it', { page: '/w/p1/runs' });
	});

	it('keeps the conversation while closed, and what arrived meanwhile', async () => {
		render(AssistantDock);
		await userEvent.click(button());
		await screen.findByText('Hello there.');
		await userEvent.click(screen.getByRole('button', { name: 'Close the assistant' }));
		await waitFor(() => expect(sheet()).not.toBeInTheDocument());
		push({ kind: 'message', seq: 3, sessionId: 's1', message: message(2, 'While you were away.') });
		await userEvent.click(button());
		expect(await screen.findByText('While you were away.')).toBeInTheDocument();
		expect(screen.getByText('Hello there.')).toBeInTheDocument();
		expect(api.assistant.open).toHaveBeenCalledTimes(1);
		expect(api.assistant.stream).toHaveBeenCalledTimes(1);
	});

	it('starts a new conversation', async () => {
		render(AssistantDock);
		await userEvent.click(button());
		await screen.findByText('Hello there.');
		vi.mocked(api.assistant.open).mockResolvedValue({
			session: session('s2'),
			messages: [],
			busy: false
		});
		await userEvent.click(screen.getByRole('button', { name: 'New conversation' }));
		expect(api.assistant.open).toHaveBeenLastCalledWith('general', 'palette', 'p1', {
			fresh: true
		});
		await waitFor(() => expect(screen.queryByText('Hello there.')).not.toBeInTheDocument());
	});

	it('takes no question while the new conversation opens', async () => {
		vi.mocked(api.assistant.status).mockResolvedValue(
			status({ pages: { general: { presets: ['Retry the failed run'], repos: false } } })
		);
		vi.mocked(api.assistant.open).mockResolvedValue({
			session: session(),
			messages: [],
			busy: false
		});
		render(AssistantDock);
		await userEvent.click(button());
		await screen.findByText('Retry the failed run');
		const opening = Promise.withResolvers<{
			session: AssistantSession;
			messages: AssistantMessage[];
			busy: boolean;
		}>();
		vi.mocked(api.assistant.open).mockReturnValueOnce(opening.promise);
		await userEvent.click(screen.getByRole('button', { name: 'New conversation' }));
		await waitFor(() => expect(api.assistant.open).toHaveBeenCalledTimes(2));
		// Neither the textbox, a preset, nor the button again: all wait.
		expect(screen.getByRole('textbox')).toBeDisabled();
		expect(screen.getByRole('button', { name: 'Retry the failed run' })).toBeDisabled();
		expect(screen.getByRole('button', { name: 'New conversation' })).toBeDisabled();
		await userEvent.click(screen.getByRole('button', { name: 'Retry the failed run' }));
		expect(api.assistant.send).not.toHaveBeenCalled();
		opening.resolve({ session: session('s2'), messages: [], busy: false });
		await waitFor(() => expect(screen.getByRole('textbox')).toBeEnabled());
		vi.mocked(api.assistant.send).mockResolvedValue({
			...message(1, 'go', 's2'),
			role: 'person'
		});
		await userEvent.type(screen.getByRole('textbox'), 'go{Enter}');
		expect(api.assistant.send).toHaveBeenCalledWith('s2', 'go', { page: '/w/p1/runs' });
	});

	it('starts no new conversation while an answer is written', async () => {
		vi.mocked(api.assistant.open).mockResolvedValue({
			session: session(),
			messages: [],
			busy: true
		});
		render(AssistantDock);
		await userEvent.click(button());
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'New conversation' })).toBeDisabled()
		);
		push({ kind: 'state', seq: 2, sessionId: 's1', busy: false });
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'New conversation' })).toBeEnabled()
		);
	});

	it('opens once the stream follows, losing nothing of an answer ending meanwhile', async () => {
		let ready = () => {};
		vi.mocked(api.assistant.stream).mockImplementation((fn, r) => {
			push = fn;
			ready = r ?? ready;
			return stop;
		});
		const opening = Promise.withResolvers<{
			session: AssistantSession;
			messages: AssistantMessage[];
			busy: boolean;
		}>();
		vi.mocked(api.assistant.open).mockReturnValueOnce(opening.promise);
		render(AssistantDock);
		// The status is in, and the load past it, waiting on the stream.
		await vi.mocked(api.assistant.status).mock.results[0].value;
		// Not before: a frame between the record and the stream would be lost.
		expect(api.assistant.open).not.toHaveBeenCalled();
		ready();
		await waitFor(() => expect(api.assistant.open).toHaveBeenCalled());
		// The record says answering; the answer ends before the drawer holds it.
		push({ kind: 'message', seq: 2, sessionId: 's1', message: message(2, 'All done.') });
		push({ kind: 'state', seq: 3, sessionId: 's1', busy: false });
		opening.resolve({ session: session(), messages: [message(1, 'Hello there.')], busy: true });
		await userEvent.click(button());
		expect(await screen.findByText('All done.')).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'New conversation' })).toBeEnabled()
		);
	});

	it('goes to the assistant’s settings, closing the panel', async () => {
		render(AssistantDock);
		await userEvent.click(button());
		await userEvent.click(screen.getByRole('button', { name: 'Assistant settings' }));
		expect(goto).toHaveBeenCalledWith('/system/assistant');
		expect(assistant.open).toBe(false);
	});

	it('looks off while the assistant is off, and still opens to say why', async () => {
		vi.mocked(api.assistant.status).mockResolvedValue(
			status({ enabled: false, reason: 'choose the harness' })
		);
		render(AssistantDock);
		await waitFor(() => expect(button()).toHaveClass('off'));
		expect(button()).toHaveAccessibleDescription('The assistant is off: open it to see why');
		expect(button()).toHaveAttribute('title', 'The assistant is off: open it to see why');
		await userEvent.click(button());
		expect(await screen.findByText('choose the harness')).toBeInTheDocument();
	});

	it('stops following the stream when it goes', () => {
		const { unmount } = render(AssistantDock);
		unmount();
		expect(stop).toHaveBeenCalled();
	});

	it('folds back to the button on a click in the page, keeping what was typed', async () => {
		render(AssistantDock);
		const outsideEl = document.body.appendChild(document.createElement('main'));
		await userEvent.click(button());
		const box = await screen.findByRole('textbox', { name: 'Ask the assistant…' });
		await waitFor(() => expect(box).toBeEnabled());
		await userEvent.type(box, 'half a question');
		// A click in it, or on a list it opened over the page, keeps it open.
		await userEvent.click(screen.getByText(/never changes the page/));
		const list = document.body.appendChild(document.createElement('ul'));
		list.setAttribute('role', 'listbox');
		await userEvent.click(list);
		expect(sheet()).toBeInTheDocument();
		await userEvent.click(outsideEl);
		await waitFor(() => expect(sheet()).not.toBeInTheDocument());
		await userEvent.click(button());
		expect(screen.getByRole('textbox', { name: 'Ask the assistant…' })).toHaveValue(
			'half a question'
		);
		outsideEl.remove();
		list.remove();
	});

	it('says on the button what it is doing while closed', async () => {
		vi.mocked(api.assistant.open).mockResolvedValue({
			session: session(),
			messages: [],
			busy: true
		});
		render(AssistantDock);
		await waitFor(() => expect(button()).toHaveTextContent('Thinking…'));
		expect(button()).toHaveClass('pill');
		push({ kind: 'state', seq: 2, sessionId: 's1', busy: false });
		await waitFor(() => expect(button()).not.toHaveClass('pill'));
	});

	it('shows the latest exchange in the card, and the whole conversation expanded', async () => {
		vi.mocked(api.assistant.open).mockResolvedValue({
			session: session(),
			messages: [
				{ ...message(1, 'first question'), role: 'person' },
				message(2, 'first answer'),
				{ ...message(3, 'second question'), role: 'person' },
				message(4, 'second answer')
			],
			busy: false
		});
		render(AssistantDock);
		await userEvent.click(button());
		expect(await screen.findByText('second answer')).toBeVisible();
		expect(screen.queryByText('first answer')).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /2 earlier messages/ }));
		expect(await screen.findByText('first answer')).toBeVisible();
		expect(screen.getByRole('button', { name: 'Back to the latest' })).toHaveAttribute(
			'aria-pressed',
			'true'
		);
		await userEvent.click(screen.getByRole('button', { name: 'Back to the latest' }));
		await waitFor(() => expect(screen.queryByText('first answer')).not.toBeInTheDocument());
		await userEvent.click(screen.getByRole('button', { name: 'Show the whole conversation' }));
		expect(await screen.findByText('first answer')).toBeVisible();
		// Closed and opened again: the card, not the drawer.
		await userEvent.click(screen.getByRole('button', { name: 'Close the assistant' }));
		await userEvent.click(button());
		await waitFor(() => expect(screen.queryByText('first answer')).not.toBeInTheDocument());
	});

	describe('with a page on screen', () => {
		let form: TicketFields;
		let withdraw = () => {};
		const offer = (over: Partial<PageAssist> = {}) => {
			form = { title: '', goal: 'notes', changeType: 'feat', typeId: '' };
			const guard = new FillGuard<TicketFields>(
				() => ({ ...form }),
				(v) => (form = { ...v }),
				ticketFill
			);
			withdraw = assistant.offer({
				kind: 'ticket',
				subject: 'T-1',
				workshop: 'p1',
				name: 'T-1',
				guard,
				pageState: () => ({ ...form }),
				...over
			});
			return guard;
		};
		const ticketSession = { ...session('t1'), kind: 'ticket' as const, subject: 'T-1' };

		beforeEach(() => {
			vi.mocked(api.assistant.open).mockImplementation(async (kind) =>
				kind === 'general'
					? { session: session(), messages: [message(1, 'Hello there.')], busy: false }
					: { session: ticketSession, messages: [], busy: false }
			);
		});
		afterEach(() => withdraw());

		const tab = (name: RegExp) => screen.getByRole('radio', { name });

		it('says the page can be filled, and opens about it, its own conversation', async () => {
			offer();
			render(AssistantDock);
			expect(button()).toHaveAttribute('title', expect.stringMatching(/can fill this ticket/));
			await userEvent.click(button());
			expect(tab(/This ticket · T-1/)).toBeChecked();
			expect(screen.getByText(/Fills this ticket: nothing is saved/)).toBeInTheDocument();
			await waitFor(() =>
				expect(api.assistant.open).toHaveBeenCalledWith('ticket', 'T-1', 'p1', { fresh: undefined })
			);
			expect(
				await screen.findByRole('textbox', { name: 'What should this ticket hold?' })
			).toBeVisible();
			// Everywhere is the other conversation, the page's kept.
			await userEvent.click(tab(/Everywhere/));
			expect(await screen.findByText('Hello there.')).toBeVisible();
			expect(screen.getByText(/never changes the page/)).toBeInTheDocument();
			await userEvent.click(tab(/This ticket/));
			expect(screen.getByText('Hello there.')).not.toBeVisible();
			expect(
				vi.mocked(api.assistant.open).mock.calls.filter((c) => c[0] === 'ticket')
			).toHaveLength(1);
		});

		it('opens no page conversation until its mode shows', async () => {
			offer();
			render(AssistantDock);
			await waitFor(() => expect(api.assistant.open).toHaveBeenCalledTimes(1));
			assistant.show('general');
			await screen.findByText('Hello there.');
			expect(api.assistant.open).toHaveBeenCalledTimes(1);
		});

		it('fills the page from the panel, with the page’s Undo beside the answer', async () => {
			vi.mocked(api.assistant.send).mockResolvedValue({
				...message(1, 'a title', 't1'),
				role: 'person'
			});
			const guard = offer();
			render(AssistantDock);
			await userEvent.click(button());
			const box = await screen.findByRole('textbox', { name: 'What should this ticket hold?' });
			await waitFor(() => expect(box).toBeEnabled());
			await userEvent.type(box, 'a title{Enter}');
			expect(guard.asking).toBe(true);
			push({
				kind: 'fill',
				seq: 5,
				sessionId: 't1',
				page: 'ticket',
				subject: 'T-1',
				fill: { title: 'Fix login' }
			});
			expect(form.title).toBe('Fix login');
			push({
				kind: 'message',
				seq: 6,
				sessionId: 't1',
				message: { ...message(2, 'Done.', 't1'), fill: { title: 'Fix login' } }
			});
			push({ kind: 'state', seq: 7, sessionId: 't1', busy: false });
			const card = await screen.findByRole('generic', { name: 'Filled this ticket' });
			expect(card).toHaveTextContent('Filled: title');
			// Folded back to the button, it says what the fill changed.
			await userEvent.click(screen.getByRole('button', { name: 'Close the assistant' }));
			await waitFor(() => expect(button()).toHaveTextContent('Filled: title'));
			await userEvent.click(button());
			await userEvent.click(screen.getByRole('button', { name: 'Undo' }));
			expect(form.title).toBe('');
			// Undone: the answer can be applied again.
			await userEvent.click(await screen.findByRole('button', { name: 'Apply this again' }));
			expect(form.title).toBe('Fix login');
		});

		it('takes a question that is not about the page to Everywhere, for the person to send', async () => {
			vi.mocked(api.assistant.open).mockImplementation(async (kind) =>
				kind === 'general'
					? { session: session(), messages: [], busy: false }
					: {
							session: ticketSession,
							messages: [
								{ ...message(1, 'start a run on it', 't1'), role: 'person' as const },
								message(2, 'I can only fill this ticket.', 't1')
							],
							busy: false
						}
			);
			offer();
			render(AssistantDock);
			await userEvent.click(button());
			await userEvent.click(await screen.findByRole('button', { name: /Ask in Everywhere/ }));
			expect(tab(/Everywhere/)).toBeChecked();
			const box = screen.getByRole('textbox', { name: 'Ask the assistant…' });
			await waitFor(() => expect(box).toHaveValue('start a run on it'));
			expect(api.assistant.send).not.toHaveBeenCalled();
		});

		it('leaves page mode when the page goes, ending its conversation', async () => {
			vi.mocked(api.assistant.open).mockImplementation(async (kind) =>
				kind === 'general'
					? { session: session(), messages: [message(1, 'Hello there.')], busy: false }
					: { session: ticketSession, messages: [], busy: true }
			);
			offer();
			render(AssistantDock);
			await userEvent.click(button());
			await waitFor(() =>
				expect(api.assistant.open).toHaveBeenCalledWith('ticket', 'T-1', 'p1', { fresh: undefined })
			);
			await waitFor(() => expect(screen.getByRole('button', { name: 'Stop' })).toBeInTheDocument());
			withdraw();
			// An answer on its way has no page to land on.
			await waitFor(() => expect(api.assistant.cancel).toHaveBeenCalledWith('t1'));
			expect(screen.queryByRole('radio', { name: /This ticket/ })).not.toBeInTheDocument();
			expect(await screen.findByText('Hello there.')).toBeVisible();
		});

		it('opens with ⌘J, switches modes with it, and closes on Esc', async () => {
			offer();
			render(AssistantDock);
			await userEvent.keyboard('{Control>}j{/Control}');
			expect(tab(/This ticket/)).toBeChecked();
			await userEvent.keyboard('{Meta>}j{/Meta}');
			expect(tab(/Everywhere/)).toBeChecked();
			await userEvent.keyboard('{Control>}j{/Control}');
			expect(tab(/This ticket/)).toBeChecked();
			await userEvent.click(screen.getByText(/Fills this ticket/));
			await userEvent.keyboard('{Escape}');
			await waitFor(() => expect(sheet()).not.toBeInTheDocument());
			await waitFor(() => expect(button()).toHaveFocus());
		});

		it('closes on ⌘J with no page to switch to', async () => {
			render(AssistantDock);
			await userEvent.keyboard('{Control>}j{/Control}');
			expect(sheet()).toBeInTheDocument();
			await userEvent.keyboard('{Control>}j{/Control}');
			await waitFor(() => expect(sheet()).not.toBeInTheDocument());
		});
	});
});
