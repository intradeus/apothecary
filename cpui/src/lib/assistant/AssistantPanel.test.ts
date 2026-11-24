import { tick } from 'svelte';
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { aProject, aRun } from '$test/fixtures';
import type { AssistantFrame, AssistantMessage, AssistantStatus } from '$lib/domain';
import AssistantPanel from './AssistantPanel.svelte';
import { Conversation, WARM_EVERY_MS } from './conversation.svelte';

vi.mock('$lib/api', () => ({
	api: {
		assistant: {
			status: vi.fn(),
			open: vi.fn(),
			warm: vi.fn(),
			send: vi.fn(),
			cancel: vi.fn(),
			setModel: vi.fn(),
			refresh: vi.fn(),
			stream: vi.fn(),
			confirm: vi.fn(),
			decline: vi.fn()
		}
	}
}));

const status = (over: Partial<AssistantStatus> = {}): AssistantStatus => ({
	enabled: true,
	choice: { integration: 'int-1', model: '', skillsets: [] },
	integrations: [{ id: 'int-1', name: 'mine', kind: 'claude-code' }],
	models: ['model-a', 'model-b'],
	pod: { state: 'running' },
	repos: [{ workshop: 'w1', name: 'api', ok: true, head: 'abc', at: 'T' }],
	pages: { general: { presets: ['Retry the failed run.'], repos: true } },
	...over
});
const session = {
	id: 's1',
	kind: 'general' as const,
	workshop: 'w1',
	subject: 'palette',
	integration: 'int-1',
	model: 'model-a',
	createdAt: 'T',
	updatedAt: 'T'
};
const msg = (
	seq: number,
	role: 'person' | 'assistant',
	text: string,
	over: Partial<AssistantMessage> = {}
): AssistantMessage => ({ sessionId: 's1', seq, role, text, costUsd: 0, at: 'T', ...over });
const f = (frame: Record<string, unknown>) =>
	({ seq: 1, sessionId: 's1', ...frame }) as AssistantFrame;
const request = {
	why: 'Start a run on T-1.',
	state: 'pending' as const,
	acts: [{ tool: 'start_run', title: 'Start one attempt', arguments: { key: 'T-1' } }]
};

async function setup(messages: AssistantMessage[] = []) {
	vi.mocked(api.assistant.open).mockResolvedValue({ session, messages, busy: false });
	const convo = new Conversation({ kind: 'general', subject: 'palette', workshop: 'w1' });
	await convo.load();
	render(AssistantPanel, { convo });
	return convo;
}

const textbox = () => screen.getByRole('textbox', { name: 'Ask the assistant…' });

// typing's warm clock is module-wide: each test starts well past the last.
let now = Date.UTC(2026, 0, 1);

beforeEach(() => {
	vi.resetAllMocks();
	now += 10 * WARM_EVERY_MS;
	vi.setSystemTime(now);
	vi.mocked(api.assistant.status).mockResolvedValue(status());
	vi.mocked(api.assistant.warm).mockResolvedValue(undefined as never);
});

describe('AssistantPanel', () => {
	it('offers the presets, and asks one', async () => {
		vi.mocked(api.assistant.send).mockResolvedValue(msg(1, 'person', 'Retry the failed run.'));
		await setup();
		expect(screen.getByText(/Ask about anything here/)).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: 'Retry the failed run.' }));
		expect(api.assistant.send).toHaveBeenCalledWith('s1', 'Retry the failed run.', undefined);
		expect(await screen.findByRole('button', { name: 'Stop' })).toBeInTheDocument();
	});

	it('starts the pod as the person types, and asks on Enter', async () => {
		vi.mocked(api.assistant.send).mockResolvedValue(msg(1, 'person', 'hello'));
		await setup();
		await userEvent.type(textbox(), 'hello');
		expect(api.assistant.warm).toHaveBeenCalledTimes(1);
		await userEvent.keyboard('{Enter}');
		expect(api.assistant.send).toHaveBeenCalledWith('s1', 'hello', undefined);
		expect(await screen.findByText('hello')).toBeInTheDocument();
	});

	it('gives a refused question back, and stops an answer', async () => {
		vi.mocked(api.assistant.send).mockRejectedValue(new Error('the session is answering'));
		const c = await setup();
		await userEvent.type(textbox(), 'again');
		await userEvent.click(screen.getByRole('button', { name: 'Ask' }));
		expect(await screen.findByText('the session is answering')).toBeInTheDocument();
		expect(textbox()).toHaveValue('again');
		c.onFrame(f({ kind: 'state', busy: true }));
		// A loader, and the engine's line on what it is doing: no step.
		expect(await screen.findByText('Thinking…')).toBeInTheDocument();
		c.onFrame(f({ kind: 'status', text: 'starting your assistant' }));
		expect(await screen.findByText('starting your assistant')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: 'Stop' }));
		expect(api.assistant.cancel).toHaveBeenCalledWith('s1');
	});

	it('shows a request once the answer is in, then confirms it and shows what came of it', async () => {
		const c = await setup();
		c.onFrame(f({ kind: 'state', busy: true }));
		c.onFrame(f({ kind: 'request', message: msg(0, 'assistant', '', { request }) }));
		await tick();
		expect(screen.queryByText('Start a run on T-1.')).not.toBeInTheDocument();
		c.onFrame(f({ kind: 'message', message: msg(2, 'assistant', 'Shall I?', { request }) }));
		c.onFrame(f({ kind: 'state', busy: false }));
		expect(await screen.findByText('Shall I?')).toBeInTheDocument();
		expect(screen.getAllByText('Start a run on T-1.')).toHaveLength(1);
		expect(screen.getByText('Start one attempt')).toBeInTheDocument();
		expect(screen.getByText(/"key": "T-1"/)).toBeInTheDocument();
		vi.mocked(api.assistant.confirm).mockResolvedValue(
			msg(2, 'assistant', 'Shall I?', {
				request: {
					...request,
					state: 'done',
					acts: [{ ...request.acts[0], status: 201, result: '{"run_id":"r1"}' }]
				}
			})
		);
		await userEvent.click(screen.getByRole('button', { name: 'Do it' }));
		expect(api.assistant.confirm).toHaveBeenCalledWith('s1', 2);
		expect(await screen.findByText('201')).not.toHaveClass('tint-danger');
		expect(screen.getByText('{"run_id":"r1"}')).toBeInTheDocument();
		expect(screen.getByText('Done')).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Do it' })).not.toBeInTheDocument();
	});

	it('declines a request, and shows one refused and one decided elsewhere', async () => {
		const two = {
			...request,
			acts: [
				{ tool: 'create_ticket', arguments: {} },
				{ tool: 'start_run', arguments: {} }
			]
		};
		const c = await setup([msg(2, 'assistant', 'Shall I?', { request: two })]);
		vi.mocked(api.assistant.decline)
			.mockRejectedValueOnce(new Error('decided already'))
			.mockResolvedValueOnce(
				msg(2, 'assistant', 'Shall I?', { request: { ...two, state: 'declined' } })
			);
		await userEvent.click(screen.getByRole('button', { name: 'Decline' }));
		expect(await screen.findByText('decided already')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: 'Decline' }));
		expect(await screen.findByText('Declined')).toBeInTheDocument();
		c.onFrame(
			f({
				kind: 'request',
				message: msg(2, 'assistant', 'Shall I?', {
					request: {
						...two,
						state: 'done',
						acts: [{ ...two.acts[0], status: 403, result: 'this needs ticket:write' }, two.acts[1]]
					}
				})
			})
		);
		expect(await screen.findByText('403')).toHaveClass('tint-danger');
		expect(screen.getByText('not run')).toBeInTheDocument();
	});

	it('names what an act’s ids are, and shows a request interrupted', async () => {
		fleet.projects = [aProject({ id: 'p1', name: 'billing' })];
		fleet.runs = [aRun({ id: 'r1', title: 'Add caching' })];
		await setup([
			msg(2, 'assistant', 'Stopping.', {
				request: {
					why: 'Stop it.',
					state: 'running',
					acts: [{ tool: 'abort_run', arguments: { id: 'r1', workshop: 'p1', other: 'x', n: 1 } }]
				}
			}),
			msg(3, 'assistant', '', { request: { ...request, acts: [{ tool: 't', arguments: null }] } })
		]);
		expect(screen.getByText('id: the run “Add caching”')).toBeInTheDocument();
		expect(screen.getByText('workshop: the workshop billing')).toBeInTheDocument();
		expect(screen.getByText(/Interrupted: check what it did/)).toBeInTheDocument();
		fleet.projects = [];
		fleet.runs = [];
	});

	it('shows an answer that failed', async () => {
		await setup([msg(2, 'assistant', '', { error: 'the pod stopped' })]);
		expect(screen.getByText('the pod stopped')).toBeInTheDocument();
	});

	it('warns of a failed start without turning the assistant off, until an answer comes', async () => {
		vi.mocked(api.assistant.status).mockResolvedValue(
			status({ pod: { state: 'absent', error: 'the gateway did not come up' } })
		);
		const c = await setup();
		expect(screen.getByText(/last start failed/)).toBeInTheDocument();
		expect(screen.getByText('the gateway did not come up')).toBeInTheDocument();
		expect(textbox()).toBeEnabled();
		// An answer that failed again keeps it; one that came puts it away.
		c.onFrame(f({ kind: 'message', message: msg(2, 'assistant', '', { error: 'again' }) }));
		expect(screen.getByText('the gateway did not come up')).toBeInTheDocument();
		c.onFrame(f({ kind: 'message', message: msg(3, 'assistant', 'hello') }));
		await vi.waitFor(() =>
			expect(screen.queryByText('the gateway did not come up')).not.toBeInTheDocument()
		);
	});

	it('puts a failed start’s warning away when dismissed', async () => {
		vi.mocked(api.assistant.status).mockResolvedValue(
			status({ pod: { state: 'running', error: 'no answer' } })
		);
		await setup();
		await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
		expect(screen.queryByText('no answer')).not.toBeInTheDocument();
	});

	it('picks the conversation’s model', async () => {
		vi.mocked(api.assistant.setModel).mockResolvedValue({ ...session, model: 'model-b' });
		await setup();
		await userEvent.click(screen.getByRole('combobox', { name: 'Model' }));
		await userEvent.click(screen.getByRole('option', { name: 'model-b' }));
		expect(api.assistant.setModel).toHaveBeenCalledWith('s1', 'model-b');
	});

	it('offers an older conversation only its own harness’s models once the harness changed', async () => {
		vi.mocked(api.assistant.status).mockResolvedValue(
			status({
				choice: { integration: 'int-2', model: '', skillsets: [] },
				integrations: [
					{ id: 'int-1', name: 'mine', kind: 'claude-code' },
					{ id: 'int-2', name: 'other', kind: 'opencode' }
				],
				models: ['other-x', 'other-y']
			})
		);
		await setup();
		const pick = screen.getByRole('combobox', { name: 'Model' });
		expect(pick).toHaveAttribute('title', expect.stringMatching(/keeps the harness it started on/));
		await userEvent.click(pick);
		expect(screen.getAllByRole('option')).toHaveLength(2);
		expect(screen.getByRole('option', { name: /model-a/ })).toBeInTheDocument();
		expect(screen.queryByRole('option', { name: /other-x/ })).not.toBeInTheDocument();
	});

	it('shows the workshop’s repositories, and refreshes them', async () => {
		vi.mocked(api.assistant.refresh).mockResolvedValue([
			{ workshop: 'w1', name: 'web', ok: false, error: '404', at: 'T' }
		]);
		const c = await setup();
		expect(screen.getByText('api')).toHaveAttribute('title', 'abc');
		await userEvent.click(screen.getByRole('button', { name: /Refresh/ }));
		expect(api.assistant.refresh).toHaveBeenCalledWith('w1');
		expect(await screen.findByText('web')).toHaveAttribute('title', 'web: 404');
		c.onFrame({ kind: 'repos', seq: 2, workshop: 'w1', text: 'fetching' } as AssistantFrame);
		expect(await screen.findByText('fetching…')).toBeInTheDocument();
	});

	it('says why it is off, and points at the settings', async () => {
		vi.mocked(api.assistant.status).mockResolvedValue(
			status({
				enabled: false,
				reason: 'choose the harness',
				choice: { integration: '', model: '', skillsets: [] }
			})
		);
		await setup();
		expect(screen.getByText('choose the harness')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Choose its harness' })).toHaveAttribute(
			'href',
			'/system/assistant'
		);
		expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
	});

	it('says when there is no harness to run on', async () => {
		vi.mocked(api.assistant.status).mockResolvedValue(
			status({
				enabled: false,
				reason: 'no harness',
				integrations: [],
				choice: { integration: 'gone', model: '', skillsets: [] }
			})
		);
		await setup();
		expect(screen.getByText(/Add a Claude Code or opencode harness/)).toBeInTheDocument();
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});

	it('says when it cannot load, and tries again', async () => {
		vi.mocked(api.assistant.status).mockRejectedValueOnce(new Error('engine down'));
		await setup();
		expect(screen.getByText('engine down')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry|Try again/ }));
		expect(await screen.findByText(/Ask about anything here/)).toBeInTheDocument();
	});
});
