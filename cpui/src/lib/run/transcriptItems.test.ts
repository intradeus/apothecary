import { describe, expect, it } from 'vitest';
import type { AgentToolCall, LiveDelta, RunEvent } from '$lib/domain';
import { I } from '$lib/design/icons';
import { anEvent, aRaise, anAnswer } from '$test/fixtures/run-views';
import {
	applyDelta,
	buildTranscript,
	emptyLive,
	promptLabel,
	pruneLive,
	type ChatItem,
	type LiveState
} from './transcriptItems';

const tool = (id: string, over: Partial<AgentToolCall> = {}): AgentToolCall => ({
	toolCallId: id,
	title: `Tool ${id}`,
	kind: 'read',
	status: 'in_progress',
	locations: [],
	diffs: [],
	...over
});
const said = (segment: string, text = 'Hello', thought = false): LiveDelta =>
	({ kind: thought ? 'thought' : 'message', segment, text }) as LiveDelta;
const called = (id: string, over: Partial<AgentToolCall> = {}): LiveDelta =>
	({ kind: 'tool', tool: tool(id, over) }) as LiveDelta;
const fold = (...deltas: LiveDelta[]): LiveState => deltas.reduce(applyDelta, emptyLive());

const agentTool = (seq: number, id: string) =>
	anEvent(seq, 'agent_tool', { tool_call_id: id, title: `Tool ${id}`, kind: 'read' });
const agentMessage = (seq: number, segment: string, text = 'Done') =>
	anEvent(seq, 'agent_message', { segment, text });

const only = (events: RunEvent[], live = emptyLive()): ChatItem => {
	const items = buildTranscript(events, live);
	expect(items).toHaveLength(1);
	return items[0];
};

describe('applyDelta', () => {
	it('adds a message under its segment', () => {
		const live = fold(said('s1', 'Hi'));
		expect(live.messages.get('s1')).toEqual({ text: 'Hi', thought: false });
		expect(live.order).toEqual(['m:s1']);
	});

	it('marks a thought', () => {
		expect(fold(said('s1', 'Hmm', true)).messages.get('s1')?.thought).toBe(true);
	});

	it('replaces a segment’s text in place: each delta carries it whole', () => {
		const live = fold(said('s1', 'He'), said('s2', 'Other'), said('s1', 'Hello'));
		expect(live.messages.get('s1')?.text).toBe('Hello');
		expect(live.order).toEqual(['m:s1', 'm:s2']);
	});

	it('updates a tool call in place, keeping its turn in the order', () => {
		const live = fold(called('t1'), said('s1'), called('t1', { status: 'completed' }));
		expect(live.tools.get('t1')?.status).toBe('completed');
		expect(live.order).toEqual(['t:t1', 'm:s1']);
	});

	it('leaves the state it was given alone, so a store write is seen', () => {
		const before = fold(said('s1', 'He'));
		const after = applyDelta(before, said('s1', 'Hello'));
		expect(after).not.toBe(before);
		expect(after.messages).not.toBe(before.messages);
		expect(before.messages.get('s1')?.text).toBe('He');
	});

	it.each<[string, LiveDelta]>([
		['a message with no segment', { kind: 'message', text: 'x' } as LiveDelta],
		['a tool with no id', { kind: 'tool', tool: tool('') } as LiveDelta],
		['a tool with no call', { kind: 'tool' } as LiveDelta],
		['a turn marker', { kind: 'turn_start' } as LiveDelta]
	])('returns the same state for %s', (_, delta) => {
		const live = emptyLive();
		expect(applyDelta(live, delta)).toBe(live);
	});

	it('takes a missing text as empty', () => {
		const live = applyDelta(emptyLive(), { kind: 'message', segment: 's1' } as LiveDelta);
		expect(live.messages.get('s1')?.text).toBe('');
	});
});

describe('pruneLive', () => {
	const live = fold(said('s1'), called('t1'), said('s2'));

	it('drops the message a persisted one supersedes', () => {
		const next = pruneLive(live, agentMessage(1, 's1'));
		expect(next.messages.has('s1')).toBe(false);
		expect(next.messages.has('s2')).toBe(true);
		expect(next.order).toEqual(['t:t1', 'm:s2']);
	});

	it('drops the tool call a persisted one supersedes', () => {
		const next = pruneLive(live, agentTool(1, 't1'));
		expect(next.tools.size).toBe(0);
		expect(next.order).toEqual(['m:s1', 'm:s2']);
	});

	it.each([
		['a message it does not hold', agentMessage(1, 'other')],
		['a tool it does not hold', agentTool(1, 'other')],
		['an unrelated event', anEvent(1, 'ping_raised')]
	])('returns the same state for %s', (_, ev) => {
		expect(pruneLive(live, ev)).toBe(live);
	});

	it.each(['stage_completed', 'stage_failed', 'stage_paused'])(
		'clears everything in flight on %s',
		(type) => {
			const next = pruneLive(live, anEvent(1, type));
			expect(next.order).toEqual([]);
			expect(next.messages.size).toBe(0);
			expect(next.tools.size).toBe(0);
		}
	);
});

describe('promptLabel', () => {
	it.each([
		['stage', 'Stage prompt'],
		['answer', 'Your answers, delivered'],
		['restart', 'Engine restarted — continue'],
		['messages', 'Your messages'],
		['handback', 'Handed back — finish the task'],
		['something_new', 'something_new']
	])('names %s', (kind, label) => {
		expect(promptLabel(kind)).toBe(label);
	});
});

describe('buildTranscript', () => {
	it('is empty for no events and nothing live', () => {
		expect(buildTranscript([], emptyLive())).toEqual([]);
	});

	it('keeps the log order and keys each item by its seq', () => {
		const items = buildTranscript(
			[anEvent(3, 'stage_started', { stage_id: 'a' }), agentMessage(9, 's1')],
			emptyLive()
		);
		expect(items.map((i) => [i.kind, i.key])).toEqual([
			['stage', 'e3'],
			['message', 'e9']
		]);
	});

	it('skips what it has no words for', () => {
		expect(buildTranscript([anEvent(1, 'run_created')], emptyLive())).toEqual([]);
	});

	describe('stage rows', () => {
		it('opens an agent stage with what runs it', () => {
			const e = anEvent(1, 'stage_started', {
				stage_id: 'build',
				iteration: 2,
				agent: 'dev',
				model: 'fake/m'
			});
			expect(only([e])).toMatchObject({
				kind: 'stage',
				stageId: 'build',
				iteration: 2,
				agentic: true,
				agent: 'dev',
				model: 'fake/m',
				ts: e.ts
			});
		});

		it('takes a harness alone as an agent stage', () => {
			const e = anEvent(1, 'stage_started', { stage_id: 'a', harness: 'h' });
			expect(only([e])).toMatchObject({ agentic: true });
		});

		it('reads a stage with neither as a plain one, first attempt', () => {
			const e = anEvent(1, 'stage_started', { stage_id: 'a' });
			expect(only([e])).toMatchObject({ agentic: false, iteration: 1 });
		});
	});

	it.each<[string, string, Record<string, unknown>, Partial<ChatItem>]>([
		[
			'stage_completed',
			'with what it spent',
			{ stage_id: 'a', turns_used: 3, usage: { cost_usd: 0.1234 }, duration_ms: 75_000 },
			{ text: 'a completed', meta: '3 turns · $0.123 · 1m15s', tone: 'ok', glyph: I.ok }
		],
		[
			'stage_completed',
			'with one turn, seconds only',
			{ stage_id: 'a', turns_used: 1, duration_ms: 4000 },
			{ text: 'a completed', meta: '1 turn · 4s' }
		],
		[
			'stage_completed',
			'with nothing to report',
			{ stage_id: 'a', usage: { cost_usd: 0 }, duration_ms: 100 },
			{ text: 'a completed', meta: '' }
		],
		[
			'stage_completed',
			'skipped',
			{ stage_id: 'a', skipped: true, note: 'no diff' },
			{ text: 'a skipped', meta: 'no diff', tone: 'ok' }
		],
		[
			'stage_paused',
			'on a question',
			{},
			{ text: 'Parked — waiting for your answer. The session resumes with it.', tone: 'warn' }
		],
		[
			'stage_paused',
			'held',
			{ reason: 'held' },
			{
				text: 'Parked — the agent is in your hands and waits for your word. Your next message resumes it.'
			}
		],
		[
			'stage_resumed',
			'after a restart',
			{ reason: 'engine_restart' },
			{ text: 'Engine restarted — picked the session back up', glyph: '⟳', tone: 'info' }
		],
		[
			'stage_resumed',
			'with a message',
			{ reason: 'message' },
			{ text: 'Resumed with your message', glyph: I.retry }
		],
		['stage_resumed', 'handed back', { reason: 'released' }, { text: 'Resumed — handed back' }],
		['stage_resumed', 'with an answer', {}, { text: 'Resumed with your answer' }],
		[
			'hold_taken',
			'',
			{},
			{
				text: 'You took over — the agent waits for your word between turns; the stage cannot end until you release it',
				tone: 'warn'
			}
		],
		[
			'hold_released',
			'by hand',
			{ duration_ms: 90_000, turns: 2, messages: 1, usage: { cost_usd: 0.5 } },
			{
				text: 'Handed back — the agent finishes its task',
				meta: '1m30s · 2 turns · 1 message · $0.500',
				tone: 'info'
			}
		],
		[
			'hold_released',
			'with the stage',
			{ reason: 'stage_ended' },
			{ text: 'Take-over ended with the attempt', meta: '' }
		],
		[
			'edge_taken',
			'bounded',
			{ from: 'r', to: 'b', count: 1, max: 3, edge: 'changes_requested' },
			{ text: 'r → b (1/3)', meta: 'changes requested', tone: 'warn', glyph: I.loop }
		],
		['edge_taken', 'unbounded', { from: 'r', to: 'b', count: 4, max: -1 }, { text: 'r → b (4/∞)' }],
		[
			'run_completed',
			'',
			{ total_cost_usd: 1.5, total_turns: 9 },
			{ text: 'Run completed', meta: '$1.500 · 9 turns', tone: 'ok' }
		],
		['run_completed', 'with no totals', {}, { text: 'Run completed', meta: '' }],
		[
			'run_failed',
			'',
			{ reason: 'budget' },
			{ text: 'Run failed — budget', tone: 'bad', glyph: I.bad }
		],
		[
			'run_abandoned',
			'at a stage',
			{ stalled_at: 's', reason: 'gave up' },
			{ text: 'Run abandoned at s — gave up', tone: 'warn', glyph: I.stop }
		],
		['run_abandoned', 'with no detail', {}, { text: 'Run abandoned' }],
		['run_retried', '', {}, { text: 'Retrying in the same workspace', tone: 'info' }],
		[
			'run_paused',
			'over budget',
			{ reason: 'budget', note: 'budget exceeded: 12 turns > max 10' },
			{
				text: 'Paused over budget — the work is kept; Play grants another budget’s worth — budget exceeded: 12 turns > max 10',
				tone: 'warn',
				glyph: I.pause
			}
		],
		['run_paused', 'over budget, no note', { reason: 'budget' }, { tone: 'warn', glyph: I.pause }],
		['run_resumed', 'old spelling', {}, { text: 'Retrying in the same workspace' }]
	])('words %s %s', (type, _case, payload, want) => {
		expect(only([anEvent(1, type, payload)])).toMatchObject({ kind: 'status', ...want });
	});

	it('says nothing of a pause that is not the budget', () => {
		expect(buildTranscript([anEvent(1, 'run_paused', { reason: 'user' })], emptyLive())).toEqual(
			[]
		);
	});

	describe('failures', () => {
		it('names the stage, the reason in words and the exit code', () => {
			const e = anEvent(1, 'stage_failed', {
				stage_id: 'a',
				reason: 'command_failed',
				exit_code: 2,
				error: 'boom'
			});
			expect(only([e])).toMatchObject({
				kind: 'failure',
				stageId: 'a',
				title: 'a failed — command failed (exit 2)',
				error: 'boom'
			});
		});

		it('shows exit zero: it is still an exit code', () => {
			const e = anEvent(1, 'stage_failed', { stage_id: 'a', reason: 'r', exit_code: 0 });
			expect(only([e])).toMatchObject({ title: 'a failed — r (exit 0)' });
		});

		it('leaves the code out when there is none', () => {
			const e = anEvent(1, 'stage_failed', { stage_id: 'a', reason: 'r' });
			expect(only([e])).toMatchObject({ title: 'a failed — r', error: undefined });
		});

		it('cuts a long error to six hundred characters', () => {
			const e = anEvent(1, 'stage_failed', { stage_id: 'a', error: 'x'.repeat(900) });
			expect(only([e])).toMatchObject({ error: 'x'.repeat(600) });
		});

		it('files a provisioning failure under the provisioning log', () => {
			const e = anEvent(1, 'env_failed', { error: 'no image' });
			expect(only([e])).toMatchObject({
				kind: 'failure',
				stageId: '_provision',
				title: 'Workspace failed to provision',
				error: 'no image'
			});
		});
	});

	it('reads a prompt', () => {
		const e = anEvent(1, 'agent_prompted', { kind: 'stage', text: 'Do it', truncated: true });
		expect(only([e])).toMatchObject({
			kind: 'prompt',
			promptKind: 'stage',
			text: 'Do it',
			truncated: true
		});
	});

	it('reads a message and a thought', () => {
		const items = buildTranscript(
			[
				agentMessage(1, 's1', 'Said'),
				anEvent(2, 'agent_message', { segment: 's2', text: 'Mused', thought: true })
			],
			emptyLive()
		);
		expect(items).toMatchObject([
			{ kind: 'message', text: 'Said', thought: false, live: false, segment: 's1' },
			{ kind: 'message', text: 'Mused', thought: true, live: false, segment: 's2' }
		]);
	});

	it('reads a notice', () => {
		expect(only([anEvent(1, 'agent_notice', { text: 'Heads up' })])).toMatchObject({
			kind: 'notice',
			text: 'Heads up'
		});
	});

	describe('tool calls', () => {
		it('groups calls in a row into one item, keyed by the first', () => {
			const item = only([agentTool(1, 't1'), agentTool(2, 't2')]);
			expect(item).toMatchObject({ kind: 'tools', key: 'e1', live: false });
			expect((item as { calls: AgentToolCall[] }).calls.map((c) => c.toolCallId)).toEqual([
				't1',
				't2'
			]);
		});

		it('starts a new group after anything else', () => {
			const items = buildTranscript(
				[agentTool(1, 't1'), agentMessage(2, 's1'), agentTool(3, 't2')],
				emptyLive()
			);
			expect(items.map((i) => i.kind)).toEqual(['tools', 'message', 'tools']);
		});
	});

	describe('plans', () => {
		const plan = (seq: number, content: string) =>
			anEvent(seq, 'agent_plan', { entries: [{ content, status: 'pending' }] });

		it('reads the entries', () => {
			expect(only([plan(1, 'Step')])).toMatchObject({
				kind: 'plan',
				entries: [{ content: 'Step', status: 'pending' }]
			});
		});

		it('keeps only the latest, where it was last updated', () => {
			const items = buildTranscript(
				[plan(1, 'First'), agentMessage(2, 's1'), plan(3, 'Second')],
				emptyLive()
			);
			expect(items.map((i) => i.kind)).toEqual(['message', 'plan']);
			expect(items[1]).toMatchObject({ entries: [{ content: 'Second' }] });
		});

		it('keeps one plan per attempt', () => {
			const items = buildTranscript(
				[plan(1, 'First'), anEvent(2, 'stage_started', { stage_id: 'a' }), plan(3, 'Second')],
				emptyLive()
			);
			expect(items.map((i) => i.kind)).toEqual(['plan', 'stage', 'plan']);
		});

		it('survives a plan with no entries', () => {
			expect(only([anEvent(1, 'agent_plan')])).toMatchObject({ kind: 'plan', entries: [] });
		});
	});

	describe('your messages', () => {
		const queued = anEvent(1, 'message_queued', { message_id: 'm1', text: 'Try again' });

		it('is undelivered until the engine says otherwise', () => {
			expect(only([queued])).toMatchObject({
				kind: 'user',
				id: 'm1',
				text: 'Try again',
				delivered: false,
				deliveredAt: undefined,
				ts: queued.ts
			});
		});

		it('takes its delivery from any later event naming it', () => {
			const delivered = anEvent(5, 'message_delivered', { message_ids: ['m0', 'm1'] });
			const items = buildTranscript([queued, delivered], emptyLive());
			expect(items[0]).toMatchObject({ delivered: true, deliveredAt: delivered.ts });
		});

		it('survives a delivery that names nothing', () => {
			expect(only([queued, anEvent(2, 'message_delivered')])).toMatchObject({ delivered: false });
		});
	});

	describe('questions', () => {
		const form = {
			fields: [
				{
					id: 'env',
					type: 'string',
					options: [
						{ id: 'dev', label: 'Development' },
						{ id: 'prod', label: 'Production' }
					]
				}
			]
		};
		const asked = (payload = {}) =>
			aRaise(1, 'p1', { type: 'clarification', answer_kind: 'form', question: 'Q?', ...payload });

		it.each(['gate', 'phase_confirm', 'debug'])('keeps a %s out of the chat', (type) => {
			expect(buildTranscript([aRaise(1, 'p1', { type })], emptyLive())).toEqual([]);
		});

		it('offers the options of a lone field', () => {
			expect(only([asked({ form })])).toMatchObject({
				kind: 'question',
				pingId: 'p1',
				type: 'clarification',
				question: 'Q?',
				options: ['Development', 'Production'],
				answer: undefined
			});
		});

		it('offers the fields of a longer form', () => {
			const longer = {
				fields: [
					{ id: 'env', title: 'Environment', type: 'string' },
					{ id: 'count', type: 'integer' }
				]
			};
			expect(only([asked({ form: longer })])).toMatchObject({
				options: ['Environment', 'count']
			});
		});

		it('offers nothing with no form', () => {
			expect(only([asked()])).toMatchObject({ options: [] });
		});

		it("reads the answer in the option's words", () => {
			const a = anAnswer(2, 'p1', { answer: 'accept', content: { env: 'prod' } });
			expect(only([asked({ form }), a])).toMatchObject({ answer: 'Production' });
		});

		it('adds the feedback to the answer', () => {
			const a = anAnswer(2, 'p1', { answer: 'decline', feedback: 'Neither' });
			expect(only([asked({ form }), a])).toMatchObject({ answer: 'Declined — Neither' });
		});

		it("ignores another ping's answer", () => {
			expect(only([asked({ form }), anAnswer(2, 'other')])).toMatchObject({ answer: undefined });
		});
	});

	describe('what is live', () => {
		it('follows everything recorded', () => {
			const items = buildTranscript([agentMessage(1, 's1')], fold(said('s2', 'Now')));
			expect(items.map((i) => [i.key, i.kind])).toEqual([
				['e1', 'message'],
				['live-m:s2', 'message']
			]);
			expect(items[1]).toMatchObject({ text: 'Now', live: true, thought: false });
		});

		it('keeps a live thought a thought', () => {
			expect(only([], fold(said('s1', 'Hmm', true)))).toMatchObject({ thought: true, live: true });
		});

		it('groups tool calls in a row', () => {
			const item = only([], fold(called('t1'), called('t2')));
			expect(item).toMatchObject({ kind: 'tools', key: 'live-t:t1', live: true });
			expect((item as { calls: AgentToolCall[] }).calls).toHaveLength(2);
		});

		it('breaks the group at a message, in arrival order', () => {
			const items = buildTranscript([], fold(called('t1'), said('s1'), called('t2')));
			expect(items.map((i) => i.kind)).toEqual(['tools', 'message', 'tools']);
		});

		it('never merges a live call into the recorded group before it', () => {
			const items = buildTranscript([agentTool(1, 't1')], fold(called('t2')));
			expect(items.map((i) => i.key)).toEqual(['e1', 'live-t:t2']);
		});

		// A delta can land after its persisted event: the prune has already run,
		// so only the build keeps the same words from showing twice.
		it('hides a live message the log already has', () => {
			expect(only([agentMessage(1, 's1')], fold(said('s1')))).toMatchObject({ live: false });
		});

		it('hides a live tool call the log already has', () => {
			expect(only([agentTool(1, 't1')], fold(called('t1')))).toMatchObject({ live: false });
		});

		it('ignores an order entry with nothing behind it', () => {
			const live = { ...emptyLive(), order: ['m:gone', 't:gone'] };
			expect(buildTranscript([], live)).toEqual([]);
		});
	});
});
