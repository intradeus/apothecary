import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type {
	AssistantFrame,
	AssistantMessage,
	AssistantSession,
	AssistantStatus
} from '$lib/domain';
import { Conversation, WARM_EVERY_MS, type FillHook } from './conversation.svelte';

vi.mock('$lib/api', () => ({
	api: {
		assistant: {
			status: vi.fn(),
			open: vi.fn(),
			session: vi.fn(),
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
	pages: { ticket: { presets: ['Turn my notes into a ticket.'], repos: true } },
	...over
});
const session = {
	id: 's1',
	kind: 'ticket' as const,
	workshop: 'w1',
	subject: 'T-1',
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

const request = {
	why: 'Start a run.',
	state: 'pending' as const,
	acts: [{ tool: 'start_run', arguments: {} }]
};

function hook(): FillHook & { offered: unknown[] } {
	const h = {
		asking: false,
		offered: [] as unknown[],
		ask: vi.fn(() => void (h.asking = true)),
		answered: vi.fn(() => void (h.asking = false)),
		offer: vi.fn((f: unknown) => (h.offered.push(f), true))
	};
	return h;
}

async function loaded(
	opts: ConstructorParameters<typeof Conversation>[0] = {
		kind: 'ticket',
		subject: 'T-1',
		workshop: 'w1'
	}
) {
	const c = new Conversation(opts);
	await c.load();
	return c;
}

/** A frame of this session's. */
const f = (frame: Record<string, unknown>) =>
	({ seq: 1, sessionId: 's1', ...frame }) as AssistantFrame;

// typing's clock is module-wide: each test starts well past the last warm.
let now = Date.UTC(2026, 0, 1);

beforeEach(() => {
	vi.resetAllMocks();
	vi.useFakeTimers({ toFake: ['Date'] });
	now += 10 * WARM_EVERY_MS;
	vi.setSystemTime(now);
	vi.mocked(api.assistant.status).mockResolvedValue(status());
	vi.mocked(api.assistant.open).mockResolvedValue({ session, messages: [], busy: false });
	vi.mocked(api.assistant.warm).mockResolvedValue(undefined as never);
});
afterEach(() => vi.useRealTimers());

describe('Conversation.load', () => {
	it('reads the status, then opens the session of its kind and subject', async () => {
		const c = await loaded();
		expect(api.assistant.status).toHaveBeenCalledWith('w1');
		expect(api.assistant.open).toHaveBeenCalledWith('ticket', 'T-1', 'w1', { fresh: undefined });
		expect(c.session).toEqual(session);
		expect(c.page?.presets).toEqual(['Turn my notes into a ticket.']);
	});

	it('opens a fresh session when asked, with no workshop for none', async () => {
		const c = new Conversation({ kind: 'general', subject: 'palette' });
		await c.load({ fresh: true });
		expect(api.assistant.status).toHaveBeenCalledWith(undefined);
		expect(api.assistant.open).toHaveBeenCalledWith('general', 'palette', undefined, {
			fresh: true
		});
	});

	it('opens nothing while the assistant is off, and says why it cannot load', async () => {
		vi.mocked(api.assistant.status).mockResolvedValueOnce(status({ enabled: false }));
		const c = await loaded();
		expect(c.status?.enabled).toBe(false);
		expect(api.assistant.open).not.toHaveBeenCalled();
		vi.mocked(api.assistant.status).mockRejectedValueOnce(new Error('engine down'));
		await c.load();
		expect(c.loadError).toBe('engine down');
	});

	it('keeps only the latest load’s answer', async () => {
		const first = Promise.withResolvers<AssistantStatus>();
		vi.mocked(api.assistant.status).mockReturnValueOnce(first.promise);
		const c = new Conversation({ kind: 'ticket', subject: 'T-1', workshop: 'w1' });
		const a = c.load();
		await c.load({ fresh: true });
		first.resolve(status({ enabled: false }));
		await a;
		expect(c.status?.enabled).toBe(true);
		expect(api.assistant.open).toHaveBeenCalledTimes(1);
	});

	it('knows a session answering when it opens', async () => {
		vi.mocked(api.assistant.open).mockResolvedValue({ session, messages: [], busy: true });
		const c = await loaded();
		expect(c.busy).toBe(true);
	});
});

describe('Conversation.retarget', () => {
	it('opens another workshop’s conversation, and stays for the same one', async () => {
		const c = await loaded();
		c.messages = [msg(1, 'person', 'hi')];
		await c.retarget('w1');
		expect(api.assistant.open).toHaveBeenCalledTimes(1);
		vi.mocked(api.assistant.open).mockResolvedValue({
			session: { ...session, id: 's2', workshop: 'w2' },
			messages: [],
			busy: false
		});
		await c.retarget('w2');
		expect(api.assistant.status).toHaveBeenLastCalledWith('w2');
		expect(c.session?.id).toBe('s2');
		expect(c.messages).toEqual([]);
	});
});

describe('Conversation frames', () => {
	it('follows the stream once, until disconnected', () => {
		const stop = vi.fn();
		vi.mocked(api.assistant.stream).mockReturnValue(stop);
		const c = new Conversation({ kind: 'ticket', subject: 'T-1' });
		c.connect();
		c.connect();
		expect(api.assistant.stream).toHaveBeenCalledTimes(1);
		c.disconnect();
		c.disconnect();
		expect(stop).toHaveBeenCalledTimes(1);
	});

	it('streams the answer, its tool and the engine’s line, then the message', async () => {
		const c = await loaded();
		c.onFrame(f({ kind: 'state', busy: true }));
		c.onFrame(f({ kind: 'status', text: 'starting your assistant' }));
		expect(c.note).toBe('starting your assistant');
		c.onFrame(f({ kind: 'delta', text: 'Reading', tool: 'Read README' }));
		expect([c.live, c.tool]).toEqual(['Reading', 'Read README']);
		c.onFrame(f({ kind: 'delta', text: 'Reading on' }));
		expect(c.tool).toBe('');
		c.onFrame(f({ kind: 'message', message: msg(2, 'assistant', 'Done.') }));
		c.onFrame(f({ kind: 'message', message: msg(2, 'assistant', 'Done.') }));
		expect(c.messages).toHaveLength(1);
		expect(c.live).toBe('');
		c.onFrame(f({ kind: 'state', busy: false }));
		expect([c.busy, c.note]).toEqual([false, '']);
	});

	it('keeps a person’s message without ending the answer being written', async () => {
		const c = await loaded();
		c.onFrame(f({ kind: 'delta', text: 'Reading' }));
		c.onFrame(f({ kind: 'message', message: msg(1, 'person', 'go') }));
		expect(c.live).toBe('Reading');
	});

	it('ignores another session’s frames, and every frame before a session', async () => {
		const c = new Conversation({ kind: 'ticket', subject: 'T-1', workshop: 'w1' });
		c.onFrame(f({ kind: 'delta', text: 'early' }));
		expect(c.live).toBe('');
		await c.load();
		c.onFrame(f({ kind: 'delta', sessionId: 's9', text: 'theirs' }));
		c.onFrame(f({ kind: 'message', sessionId: 's9', message: msg(1, 'assistant', 'theirs') }));
		expect(c.live).toBe('');
		expect(c.messages).toEqual([]);
	});

	it('shows a request as it is proposed, then the one decided elsewhere', async () => {
		vi.mocked(api.assistant.open).mockResolvedValue({
			session,
			messages: [msg(2, 'assistant', 'Shall I?', { request })],
			busy: false
		});
		const c = await loaded();
		c.onFrame(f({ kind: 'request', message: msg(0, 'assistant', '', { request }) }));
		expect(c.liveRequest).toEqual(request);
		c.onFrame(f({ kind: 'message', message: msg(3, 'assistant', 'Asked.') }));
		expect(c.liveRequest).toBeNull();
		const done = { ...request, state: 'done' as const };
		c.onFrame(f({ kind: 'request', message: msg(2, 'assistant', 'Shall I?', { request: done }) }));
		expect(c.messages[0].request?.state).toBe('done');
		c.onFrame(f({ kind: 'request', message: msg(0, 'assistant', '') }));
		expect(c.liveRequest).toBeNull();
	});

	it('follows its workshop’s repositories being fetched, not another’s', async () => {
		const c = await loaded();
		c.onFrame({ kind: 'repos', seq: 2, workshop: 'w1', text: 'fetching' } as AssistantFrame);
		expect(c.fetching).toBe(true);
		const repos = [{ workshop: 'w1', name: 'web', ok: true, head: 'x', at: 'T' }];
		c.onFrame({ kind: 'repos', seq: 3, workshop: 'other', repos: [] } as AssistantFrame);
		expect(c.fetching).toBe(true);
		c.onFrame({ kind: 'repos', seq: 4, workshop: 'w1', repos } as AssistantFrame);
		expect(c.fetching).toBe(false);
		expect(c.status?.repos).toEqual(repos);
	});

	it('reads the record again after a gap, dropping a proposed request', async () => {
		const c = await loaded();
		c.onFrame(f({ kind: 'state', busy: true }));
		c.onFrame(f({ kind: 'delta', text: 'half' }));
		c.onFrame(f({ kind: 'request', message: msg(0, 'assistant', '', { request }) }));
		vi.mocked(api.assistant.session).mockResolvedValue({
			session,
			messages: [msg(1, 'assistant', 'from the record')],
			busy: false
		});
		c.onFrame({ kind: 'gap' } as AssistantFrame);
		await vi.waitFor(() => expect(c.messages[0]?.text).toBe('from the record'));
		expect(api.assistant.session).toHaveBeenLastCalledWith('s1');
		expect([c.busy, c.live, c.liveRequest]).toEqual([false, '', null]);
	});

	it('reads again the session it holds after a gap, not a newer one about its subject', async () => {
		const c = await loaded();
		// Another tab opened s2 about T-1 since: a plain open would answer it.
		vi.mocked(api.assistant.open).mockResolvedValue({
			session: { ...session, id: 's2' },
			messages: [msg(1, 'person', 'the other tab’s', { sessionId: 's2' })],
			busy: true
		});
		vi.mocked(api.assistant.session).mockResolvedValue({
			session,
			messages: [msg(1, 'person', 'mine')],
			busy: false
		});
		await c.reopen();
		expect(api.assistant.open).toHaveBeenCalledTimes(1);
		expect([c.session?.id, c.messages[0]?.text, c.busy]).toEqual(['s1', 'mine', false]);
		vi.mocked(api.assistant.send).mockResolvedValue(msg(2, 'person', 'go'));
		await c.ask('go');
		expect(api.assistant.send).toHaveBeenCalledWith('s1', 'go', undefined);
	});

	it('drops a reread that another session overtook', async () => {
		const c = await loaded();
		const late = Promise.withResolvers<{
			session: AssistantSession;
			messages: AssistantMessage[];
			busy: boolean;
		}>();
		vi.mocked(api.assistant.session).mockReturnValueOnce(late.promise);
		const p = c.reopen();
		vi.mocked(api.assistant.open).mockResolvedValue({
			session: { ...session, id: 's2' },
			messages: [],
			busy: false
		});
		await c.load({ fresh: true });
		late.resolve({ session, messages: [msg(1, 'person', 'old')], busy: true });
		await p;
		expect([c.session?.id, c.messages, c.busy]).toEqual(['s2', [], false]);
		// Nor does its failure speak for the new one.
		vi.mocked(api.assistant.session).mockImplementationOnce(async () => {
			await c.load({ fresh: true });
			throw new Error('gone');
		});
		vi.mocked(api.assistant.open).mockResolvedValue({
			session: { ...session, id: 's3' },
			messages: [],
			busy: false
		});
		await c.reopen();
		expect([c.session?.id, c.error]).toEqual(['s3', '']);
	});

	it('ignores a gap before a session, and says when the record cannot be read', async () => {
		const c = new Conversation({ kind: 'ticket', subject: 'T-1' });
		c.onFrame({ kind: 'gap' } as AssistantFrame);
		await c.reopen();
		expect(api.assistant.session).not.toHaveBeenCalled();
		await c.load();
		vi.mocked(api.assistant.session).mockRejectedValueOnce(new Error('gone'));
		await c.reopen();
		expect(c.error).toBe('gone');
	});
});

describe('Conversation switching sessions', () => {
	type View = { session: AssistantSession; messages: AssistantMessage[]; busy: boolean };
	const view = (id: string, messages: AssistantMessage[] = []): View => ({
		session: { ...session, id },
		messages,
		busy: false
	});

	it('takes no question, decision or model while a new session opens', async () => {
		const m = msg(2, 'assistant', 'Shall I?', { request });
		vi.mocked(api.assistant.open).mockResolvedValue(view('s1', [m]));
		const c = await loaded();
		expect(c.switching).toBe(false);
		const opening = Promise.withResolvers<View>();
		vi.mocked(api.assistant.open).mockReturnValueOnce(opening.promise);
		const p = c.load({ fresh: true });
		expect(c.switching).toBe(true);
		await vi.waitFor(() => expect(api.assistant.open).toHaveBeenCalledTimes(2));
		// The old thread still shows; nothing goes to its session.
		expect(await c.ask('to the old one?')).toBe(false);
		await c.decide(m, true);
		await c.pickModel('model-b');
		expect(api.assistant.send).not.toHaveBeenCalled();
		expect(api.assistant.confirm).not.toHaveBeenCalled();
		expect(api.assistant.setModel).not.toHaveBeenCalled();
		opening.resolve(view('s2'));
		await p;
		expect([c.switching, c.session?.id, c.messages]).toEqual([false, 's2', []]);
		vi.mocked(api.assistant.send).mockResolvedValue(msg(1, 'person', 'now', { sessionId: 's2' }));
		expect(await c.ask('now')).toBe(true);
		expect(api.assistant.send).toHaveBeenCalledWith('s2', 'now', undefined);
	});

	it('ends switching when the session cannot be opened', async () => {
		const c = await loaded();
		vi.mocked(api.assistant.open).mockRejectedValueOnce(new Error('down'));
		await c.load({ fresh: true });
		expect([c.switching, c.loadError]).toEqual([false, 'down']);
	});

	it('drops what the old session answered once another is shown', async () => {
		const m = msg(2, 'assistant', 'Shall I?', { request });
		vi.mocked(api.assistant.open).mockResolvedValue(view('s1', [m]));
		const c = await loaded();
		vi.mocked(api.assistant.open).mockResolvedValue(view('s2', [m]));
		const sent = Promise.withResolvers<AssistantMessage>();
		vi.mocked(api.assistant.send).mockReturnValueOnce(sent.promise);
		const asked = c.ask('to s1');
		const decided = Promise.withResolvers<AssistantMessage>();
		vi.mocked(api.assistant.confirm).mockReturnValueOnce(decided.promise);
		const deciding = c.decide(m, true);
		const model = Promise.withResolvers<AssistantSession>();
		vi.mocked(api.assistant.setModel).mockReturnValueOnce(model.promise);
		const picking = c.pickModel('model-b');
		await c.retarget('w2');
		sent.resolve(msg(3, 'person', 'to s1'));
		decided.resolve({ ...m, request: { ...request, state: 'done' } });
		model.resolve({ ...session, model: 'model-b' });
		await Promise.all([asked, deciding, picking]);
		expect([c.session?.id, c.session?.model, c.messages]).toEqual(['s2', 'model-a', [m]]);
		// Nor do its refusals speak for the new one: each a session of its own.
		vi.mocked(api.assistant.open)
			.mockResolvedValueOnce(view('s3'))
			.mockResolvedValueOnce(view('s4'))
			.mockResolvedValueOnce(view('s5'));
		vi.mocked(api.assistant.send).mockImplementationOnce(async () => {
			await c.retarget('w3');
			throw new Error('busy');
		});
		vi.mocked(api.assistant.confirm).mockImplementationOnce(async () => {
			await c.retarget('w4');
			throw new Error('decided');
		});
		vi.mocked(api.assistant.setModel).mockImplementationOnce(async () => {
			await c.retarget('w5');
			throw new Error('unknown');
		});
		expect(await c.ask('again')).toBe(false);
		await c.decide(m, true);
		await c.pickModel('x');
		expect(c.error).toBe('');
	});
});

describe('Conversation questions', () => {
	it('asks with the page as it stands', async () => {
		const pageState = vi.fn(() => ({ title: 'mine' }));
		vi.mocked(api.assistant.send).mockResolvedValue(msg(1, 'person', 'fill it'));
		const c = await loaded({ kind: 'ticket', subject: 'T-1', workshop: 'w1', pageState });
		expect(await c.ask('  fill it ')).toBe(true);
		expect(api.assistant.send).toHaveBeenCalledWith('s1', 'fill it', { title: 'mine' });
		expect(c.busy).toBe(true);
		expect(c.messages).toEqual([msg(1, 'person', 'fill it')]);
		// The stream had it first: kept once.
		vi.mocked(api.assistant.send).mockResolvedValue(msg(1, 'person', 'fill it'));
		c.busy = false;
		await c.ask('again');
		expect(c.messages).toHaveLength(1);
	});

	it('asks nothing empty, before a session, or while answering', async () => {
		const c = new Conversation({ kind: 'ticket', subject: 'T-1' });
		expect(await c.ask('x')).toBe(false);
		await c.load();
		expect(await c.ask('   ')).toBe(false);
		c.busy = true;
		expect(await c.ask('x')).toBe(false);
		expect(api.assistant.send).not.toHaveBeenCalled();
	});

	it('gives back a refused question, its page no longer asking', async () => {
		vi.mocked(api.assistant.send).mockRejectedValue(new Error('the session is answering'));
		const fill = hook();
		const c = await loaded({ kind: 'ticket', subject: 'T-1', fill });
		expect(await c.ask('go')).toBe(false);
		expect([c.busy, c.error, fill.asking]).toEqual([false, 'the session is answering', false]);
	});

	it('fills its page only for its own question, until the answer ends', async () => {
		vi.mocked(api.assistant.send).mockResolvedValue(msg(1, 'person', 'go'));
		const fill = hook();
		const c = await loaded({ kind: 'ticket', subject: 'T-1', fill });
		// Replayed, or another tab's: not this page's.
		c.onFrame(f({ kind: 'fill', fill: { title: 'old' } }));
		expect(fill.offered).toEqual([]);
		await c.ask('go');
		c.onFrame(f({ kind: 'fill', fill: { title: 'new' } }));
		expect(fill.offered).toEqual([{ title: 'new' }]);
		c.onFrame(f({ kind: 'state', busy: false }));
		expect(fill.asking).toBe(false);
		c.onFrame(f({ kind: 'fill', fill: { title: 'late' } }));
		expect(fill.offered).toHaveLength(1);
	});

	it('takes the fill of an answer a gap lost from the record, once', async () => {
		const before = msg(2, 'assistant', '', { fill: { title: 'an earlier answer' } });
		vi.mocked(api.assistant.open).mockResolvedValue({ session, messages: [before], busy: false });
		vi.mocked(api.assistant.send).mockResolvedValue(msg(3, 'person', 'go'));
		const fill = hook();
		const c = await loaded({ kind: 'ticket', subject: 'T-1', fill });
		const reread = (messages: AssistantMessage[], busy: boolean) =>
			vi.mocked(api.assistant.session).mockResolvedValueOnce({ session, messages, busy });

		// Still answering: nothing in the record is this question's yet,
		// not the earlier answer's fill.
		await c.ask('go');
		reread([before, msg(3, 'person', 'go')], true);
		await c.reopen();
		expect(fill.offered).toEqual([]);
		expect(fill.asking).toBe(true);

		// Its fill frame lost, the answer is in the record: the page takes it,
		// then its question has ended.
		const answer = msg(4, 'assistant', 'Done.', { fill: { title: 'new' } });
		reread([before, msg(3, 'person', 'go'), answer], false);
		await c.reopen();
		expect(fill.offered).toEqual([{ title: 'new' }]);
		expect(fill.asking).toBe(false);
		// A frame retained from before the gap, or another reread: not again.
		c.onFrame(f({ kind: 'fill', fill: { title: 'new' } }));
		reread([before, msg(3, 'person', 'go'), answer], false);
		await c.reopen();
		expect(fill.offered).toHaveLength(1);

		// A fill its frame already brought is not offered again from the
		// record; a later one of the same answer, whose frame was lost, is.
		vi.mocked(api.assistant.send).mockResolvedValue(msg(5, 'person', 'again'));
		await c.ask('again');
		c.onFrame(f({ kind: 'fill', fill: { title: 'first' } }));
		reread([msg(6, 'assistant', '', { fill: { title: 'first' } })], false);
		await c.reopen();
		expect(fill.offered).toEqual([{ title: 'new' }, { title: 'first' }]);
		vi.mocked(api.assistant.send).mockResolvedValue(msg(7, 'person', 'more'));
		await c.ask('more');
		c.onFrame(f({ kind: 'fill', fill: { title: 'draft' } }));
		reread([msg(8, 'assistant', '', { fill: { title: 'final' } })], false);
		await c.reopen();
		expect(fill.offered.at(-1)).toEqual({ title: 'final' });
		expect(fill.offered).toHaveLength(4);
	});

	it('stops an answer, and says when it could not', async () => {
		const c = new Conversation({ kind: 'ticket', subject: 'T-1' });
		await c.cancel();
		expect(api.assistant.cancel).not.toHaveBeenCalled();
		await c.load();
		await c.cancel();
		expect(api.assistant.cancel).toHaveBeenCalledWith('s1');
		vi.mocked(api.assistant.cancel).mockRejectedValueOnce(new Error('no'));
		await c.cancel();
		expect(c.error).toBe('no');
	});

	it('confirms and declines a request, one at a time', async () => {
		const m = msg(2, 'assistant', 'Shall I?', { request });
		vi.mocked(api.assistant.open).mockResolvedValue({ session, messages: [m], busy: false });
		const c = await loaded();
		const done = msg(2, 'assistant', 'Shall I?', { request: { ...request, state: 'done' } });
		const finish = Promise.withResolvers<AssistantMessage>();
		vi.mocked(api.assistant.confirm).mockReturnValueOnce(finish.promise);
		const p = c.decide(m, true);
		expect(c.deciding).toBe(2);
		await c.decide(m, false);
		expect(api.assistant.decline).not.toHaveBeenCalled();
		finish.resolve(done);
		await p;
		expect(api.assistant.confirm).toHaveBeenCalledWith('s1', 2);
		expect(c.messages[0].request?.state).toBe('done');
		expect(c.deciding).toBeNull();
		vi.mocked(api.assistant.decline).mockRejectedValueOnce(new Error('decided already'));
		await c.decide(m, false);
		expect(api.assistant.decline).toHaveBeenCalledWith('s1', 2);
		expect(c.error).toBe('decided already');
	});

	it('decides nothing before a session', async () => {
		const c = new Conversation({ kind: 'ticket', subject: 'T-1' });
		await c.decide(msg(2, 'assistant', ''), true);
		expect(api.assistant.confirm).not.toHaveBeenCalled();
	});

	it('picks the session’s model', async () => {
		const c = new Conversation({ kind: 'ticket', subject: 'T-1' });
		await c.pickModel('model-b');
		expect(api.assistant.setModel).not.toHaveBeenCalled();
		await c.load();
		vi.mocked(api.assistant.setModel).mockResolvedValue({ ...session, model: 'model-b' });
		await c.pickModel('model-b');
		expect(c.session?.model).toBe('model-b');
		vi.mocked(api.assistant.setModel).mockRejectedValueOnce(new Error('unknown model'));
		await c.pickModel('x');
		expect(c.error).toBe('unknown model');
	});

	it('refreshes its workshop’s repositories', async () => {
		const none = new Conversation({ kind: 'ticket', subject: 'T-1' });
		await none.refresh();
		expect(api.assistant.refresh).not.toHaveBeenCalled();
		const c = await loaded();
		const repos = [{ workshop: 'w1', name: 'web', ok: false, error: '404', at: 'T' }];
		vi.mocked(api.assistant.refresh).mockResolvedValue(repos);
		await c.refresh();
		expect(api.assistant.refresh).toHaveBeenCalledWith('w1');
		expect([c.status?.repos, c.fetching]).toEqual([repos, false]);
		vi.mocked(api.assistant.refresh).mockRejectedValueOnce(new Error('no token'));
		await c.refresh();
		expect([c.error, c.fetching]).toEqual(['no token', false]);
	});
});

describe('typing', () => {
	it('starts the pod on the first words, then at most once a minute', async () => {
		const c = await loaded();
		c.typing('   ');
		expect(api.assistant.warm).not.toHaveBeenCalled();
		c.typing('h');
		c.typing('he');
		expect(api.assistant.warm).toHaveBeenCalledTimes(1);
		vi.setSystemTime(now + WARM_EVERY_MS - 1);
		c.typing('hel');
		expect(api.assistant.warm).toHaveBeenCalledTimes(1);
		vi.setSystemTime(now + WARM_EVERY_MS);
		c.typing('hell');
		expect(api.assistant.warm).toHaveBeenCalledTimes(2);
	});

	it('never starts the pod while the assistant is off or unknown', async () => {
		new Conversation({ kind: 'ticket', subject: 'T-1' }).typing('hi');
		vi.mocked(api.assistant.status).mockResolvedValueOnce(status({ enabled: false }));
		const c = await loaded();
		c.typing('hi');
		expect(api.assistant.warm).not.toHaveBeenCalled();
	});

	it('shares one clock between the drawer and a page’s modal', async () => {
		const drawer = await loaded({ kind: 'general', subject: 'palette' });
		const modal = await loaded();
		drawer.typing('a');
		modal.typing('b');
		expect(api.assistant.warm).toHaveBeenCalledTimes(1);
	});

	it('shrugs off a warm that failed', async () => {
		vi.mocked(api.assistant.warm).mockRejectedValue(new Error('down'));
		const c = await loaded();
		c.typing('hi');
		await Promise.resolve();
		expect(c.error).toBe('');
	});
});
