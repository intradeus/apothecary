import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { page } from '$app/state';
import { api } from '$lib/api';
import type {
	AssistantFrame,
	AssistantMessage,
	AssistantSession,
	AssistantStatus
} from '$lib/domain';
import { drawer } from './drawer.svelte';
import GlobalAssistant from './GlobalAssistant.svelte';

vi.mock('$lib/api', () => ({
	api: {
		assistant: {
			status: vi.fn(),
			open: vi.fn(),
			warm: vi.fn(),
			send: vi.fn(),
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
const sheet = () => screen.queryByRole('dialog', { name: 'Assistant' });

beforeEach(() => {
	vi.resetAllMocks();
	drawer.open = false;
	page.params.workshop = 'p1';
	vi.mocked(api.assistant.status).mockResolvedValue(status());
	vi.mocked(api.assistant.warm).mockResolvedValue(undefined as never);
	vi.mocked(api.assistant.open).mockResolvedValue({
		session: session(),
		messages: [message(1, 'Hello there.')],
		busy: false
	});
	vi.mocked(api.assistant.stream).mockImplementation((fn) => {
		push = fn;
		return stop;
	});
});

describe('GlobalAssistant', () => {
	it('is a named button with a tooltip, opened from the keyboard', async () => {
		render(GlobalAssistant);
		expect(button()).toHaveAttribute('title', expect.stringMatching(/acts once you confirm/));
		expect(sheet()).not.toBeInTheDocument();
		await userEvent.tab();
		expect(button()).toHaveFocus();
		await userEvent.keyboard('{Enter}');
		expect(sheet()).toBeInTheDocument();
	});

	it('opens the person’s general conversation about the workshop on screen', async () => {
		render(GlobalAssistant);
		await userEvent.click(button());
		expect(await screen.findByText('Hello there.')).toBeInTheDocument();
		expect(api.assistant.status).toHaveBeenCalledWith('p1');
		expect(api.assistant.open).toHaveBeenCalledWith('general', 'palette', 'p1', {
			fresh: undefined
		});
	});

	it('reads no workshop off a workshop’s pages, and follows the route to another', async () => {
		page.params.workshop = undefined;
		render(GlobalAssistant);
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
		render(GlobalAssistant);
		await userEvent.click(button());
		await screen.findByText('Hello there.');
		await userEvent.type(screen.getByRole('textbox'), 'retry it{Enter}');
		expect(api.assistant.send).toHaveBeenCalledWith('s1', 'retry it', { page: '/w/p1/runs' });
	});

	it('keeps the conversation while closed, and what arrived meanwhile', async () => {
		render(GlobalAssistant);
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
		render(GlobalAssistant);
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
		render(GlobalAssistant);
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
		render(GlobalAssistant);
		await userEvent.click(button());
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'New conversation' })).toBeDisabled()
		);
		push({ kind: 'state', seq: 2, sessionId: 's1', busy: false });
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'New conversation' })).toBeEnabled()
		);
	});

	it('goes to the assistant’s settings, closing the drawer', async () => {
		render(GlobalAssistant);
		await userEvent.click(button());
		await userEvent.click(screen.getByRole('button', { name: 'Assistant settings' }));
		expect(goto).toHaveBeenCalledWith('/system/assistant');
		expect(drawer.open).toBe(false);
	});

	it('looks off while the assistant is off, and still opens to say why', async () => {
		vi.mocked(api.assistant.status).mockResolvedValue(
			status({ enabled: false, reason: 'choose the harness' })
		);
		render(GlobalAssistant);
		await waitFor(() => expect(button()).toHaveClass('off'));
		expect(button()).toHaveAccessibleDescription('The assistant is off: open it to see why');
		expect(button()).toHaveAttribute('title', 'The assistant is off: open it to see why');
		await userEvent.click(button());
		expect(await screen.findByText('choose the harness')).toBeInTheDocument();
	});

	it('stops following the stream when it goes', () => {
		const { unmount } = render(GlobalAssistant);
		unmount();
		expect(stop).toHaveBeenCalled();
	});
});
