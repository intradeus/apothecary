// The run's conversation, as the chat view shows it.
//
// The engine records an agent stage as it happens — what it was sent, what it
// said, every tool call, its plan, the human's messages — as events in the
// run's log; live progress (text being written, tool calls in flight) arrives
// separately as deltas. This module folds both into one ordered list of chat
// items. Pure: the component only renders what comes out.

import type { AgentToolCall, LiveDelta, RunEvent } from '$lib/domain';
import { toAgentToolCall } from '$lib/api/mappers';

export type ChatItem =
	| {
			kind: 'stage';
			key: string;
			stageId: string;
			iteration: number;
			agent?: string;
			model?: string;
	  }
	| { kind: 'status'; key: string; text: string; tone: 'ok' | 'bad' | 'warn' | 'info' }
	| { kind: 'prompt'; key: string; promptKind: string; text: string; truncated: boolean }
	| { kind: 'message'; key: string; text: string; thought: boolean; live: boolean }
	| { kind: 'tools'; key: string; calls: AgentToolCall[]; live: boolean }
	| { kind: 'plan'; key: string; entries: { content: string; status: string }[] }
	| { kind: 'notice'; key: string; text: string }
	| {
			kind: 'user';
			key: string;
			id: string;
			text: string;
			interrupt: boolean;
			delivered: boolean;
	  }
	| {
			kind: 'question';
			key: string;
			pingId: string;
			question: string;
			options: string[];
			answer?: string;
	  };

/** Live state the view accumulates from deltas, keyed so the persisted event
 *  that follows can replace it. */
export interface LiveState {
	/** Arrival order of live keys: "m:<segment>" or "t:<toolCallId>". */
	order: string[];
	messages: Map<string, { text: string; thought: boolean }>;
	tools: Map<string, AgentToolCall>;
}

export const emptyLive = (): LiveState => ({ order: [], messages: new Map(), tools: new Map() });

/** Folds one delta into the live state (a new object, for reactivity). */
export function applyDelta(live: LiveState, d: LiveDelta): LiveState {
	if ((d.kind === 'message' || d.kind === 'thought') && d.segment) {
		const key = 'm:' + d.segment;
		const messages = new Map(live.messages);
		messages.set(d.segment, { text: d.text ?? '', thought: d.kind === 'thought' });
		return {
			...live,
			messages,
			order: live.order.includes(key) ? live.order : [...live.order, key]
		};
	}
	if (d.kind === 'tool' && d.tool?.toolCallId) {
		const key = 't:' + d.tool.toolCallId;
		const tools = new Map(live.tools);
		tools.set(d.tool.toolCallId, d.tool);
		return { ...live, tools, order: live.order.includes(key) ? live.order : [...live.order, key] };
	}
	return live;
}

/** Drops live entries that a persisted event has now superseded. */
export function pruneLive(live: LiveState, ev: RunEvent): LiveState {
	if (ev.type === 'agent_message') {
		const seg = String(ev.payload.segment ?? '');
		if (!live.messages.has(seg)) return live;
		const messages = new Map(live.messages);
		messages.delete(seg);
		return { ...live, messages, order: live.order.filter((k) => k !== 'm:' + seg) };
	}
	if (ev.type === 'agent_tool') {
		const id = String(ev.payload.tool_call_id ?? '');
		if (!live.tools.has(id)) return live;
		const tools = new Map(live.tools);
		tools.delete(id);
		return { ...live, tools, order: live.order.filter((k) => k !== 't:' + id) };
	}
	if (ev.type === 'stage_completed' || ev.type === 'stage_failed' || ev.type === 'stage_paused') {
		// Whatever was in flight when the attempt ended is final now (or gone).
		return emptyLive();
	}
	return live;
}

const promptLabels: Record<string, string> = {
	stage: 'Stage prompt',
	answer: 'Your answers, delivered',
	restart: 'Engine restarted — continue',
	messages: 'Your messages'
};
export const promptLabel = (k: string) => promptLabels[k] ?? k;

export function buildTranscript(events: RunEvent[], live: LiveState): ChatItem[] {
	const items: ChatItem[] = [];
	const delivered = new Set<string>();
	const answers = new Map<string, string>();
	const persistedSegments = new Set<string>();
	const persistedTools = new Set<string>();
	for (const e of events) {
		if (e.type === 'message_delivered') {
			for (const id of (e.payload.message_ids as string[] | undefined) ?? []) delivered.add(id);
		} else if (e.type === 'ping_answered') {
			const fb = e.payload.feedback as string | undefined;
			const a = String(e.payload.answer ?? '');
			answers.set(String(e.payload.ping_id), a === 'text' ? (fb ?? '') : fb ? `${a} — ${fb}` : a);
		} else if (e.type === 'agent_message') {
			persistedSegments.add(String(e.payload.segment ?? ''));
		} else if (e.type === 'agent_tool') {
			persistedTools.add(String(e.payload.tool_call_id ?? ''));
		}
	}

	const agentStages = new Set<string>();
	let planAt = -1; // index of the current attempt's plan item (one per attempt)
	for (const e of events) {
		const p = e.payload;
		const key = `e${e.seq}`;
		switch (e.type) {
			case 'stage_started':
				if (!p.agent && !p.harness) break; // commands, gates: the timeline has them
				agentStages.add(String(p.stage_id));
				planAt = -1;
				items.push({
					kind: 'stage',
					key,
					stageId: String(p.stage_id),
					iteration: Number(p.iteration ?? 1),
					agent: p.agent as string | undefined,
					model: p.model as string | undefined
				});
				break;
			case 'stage_completed':
				if (agentStages.has(String(p.stage_id)))
					items.push({ kind: 'status', key, text: `${p.stage_id} completed`, tone: 'ok' });
				break;
			case 'stage_failed':
				if (agentStages.has(String(p.stage_id)))
					items.push({
						kind: 'status',
						key,
						text: `${p.stage_id} failed: ${p.reason}${p.error ? ` — ${String(p.error).slice(0, 300)}` : ''}`,
						tone: 'bad'
					});
				break;
			case 'stage_paused':
				items.push({
					kind: 'status',
					key,
					text: 'Parked — waiting for your answer. The session resumes with it.',
					tone: 'warn'
				});
				break;
			case 'stage_resumed':
				items.push({
					kind: 'status',
					key,
					text:
						p.reason === 'engine_restart'
							? 'Engine restarted — picked the session back up'
							: 'Resumed with your answer',
					tone: 'info'
				});
				break;
			case 'agent_prompted':
				items.push({
					kind: 'prompt',
					key,
					promptKind: String(p.kind ?? ''),
					text: String(p.text ?? ''),
					truncated: Boolean(p.truncated)
				});
				break;
			case 'agent_message':
				items.push({
					kind: 'message',
					key,
					text: String(p.text ?? ''),
					thought: Boolean(p.thought),
					live: false
				});
				break;
			case 'agent_tool': {
				const call = toAgentToolCall(p);
				const last = items[items.length - 1];
				if (last?.kind === 'tools' && !last.live) last.calls.push(call);
				else items.push({ kind: 'tools', key, calls: [call], live: false });
				break;
			}
			case 'agent_plan': {
				const entries = ((p.entries as Record<string, unknown>[] | undefined) ?? []).map((x) => ({
					content: String(x.content ?? ''),
					status: String(x.status ?? '')
				}));
				// The latest plan only, where it was last updated.
				if (planAt >= 0) items.splice(planAt, 1);
				items.push({ kind: 'plan', key, entries });
				planAt = items.length - 1;
				break;
			}
			case 'agent_notice':
				items.push({ kind: 'notice', key, text: String(p.text ?? '') });
				break;
			case 'message_queued':
				items.push({
					kind: 'user',
					key,
					id: String(p.message_id),
					text: String(p.text ?? ''),
					interrupt: Boolean(p.interrupt),
					delivered: delivered.has(String(p.message_id))
				});
				break;
			case 'ping_raised':
				if (p.type === 'gate') break;
				items.push({
					kind: 'question',
					key,
					pingId: String(p.ping_id),
					question: String(p.question ?? ''),
					options: (p.options as string[] | undefined) ?? [],
					answer: answers.get(String(p.ping_id))
				});
				break;
		}
	}

	// What is happening right now, after everything recorded.
	let liveTools: AgentToolCall[] | null = null;
	for (const k of live.order) {
		if (k.startsWith('m:')) {
			const seg = k.slice(2);
			const m = live.messages.get(seg);
			if (!m || persistedSegments.has(seg)) continue;
			liveTools = null;
			items.push({
				kind: 'message',
				key: 'live-' + k,
				text: m.text,
				thought: m.thought,
				live: true
			});
		} else {
			const id = k.slice(2);
			const t = live.tools.get(id);
			if (!t || persistedTools.has(id)) continue;
			if (!liveTools) {
				liveTools = [];
				items.push({ kind: 'tools', key: 'live-' + k, calls: liveTools, live: true });
			}
			liveTools.push(t);
		}
	}
	return items;
}
