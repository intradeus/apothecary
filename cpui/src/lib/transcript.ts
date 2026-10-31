// The run's conversation, as the chat view shows it.
//
// The engine records an agent stage as it happens — what it was sent, what it
// said, every tool call, its plan, the human's messages — as events in the
// run's log; live progress (text being written, tool calls in flight) arrives
// separately as deltas. This module folds both into one ordered list of chat
// items. Pure: the component only renders what comes out.

import type { AgentToolCall, LiveDelta, RunEvent } from '$lib/domain';
import { toAgentToolCall } from '$lib/api/mappers';
import type { Tone as AnyTone } from '$lib/ui/hues';
import { I } from '$lib/ui/icons';
import { t } from '$lib/i18n/index.svelte';
import { answerKindOf, answerSummary, fieldLabel, toFormContent, toPingForm } from '$lib/ping';

/** A status line's tone; its hue comes from TONE_HUE. */
export type Tone = Exclude<AnyTone, 'mut'>;

export type ChatItem =
	| {
			kind: 'stage';
			key: string;
			stageId: string;
			iteration: number;
			/** An agent stage (else a command, gate, action or wait). */
			agentic: boolean;
			agent?: string;
			model?: string;
			ts: string;
	  }
	| { kind: 'status'; key: string; glyph: string; text: string; meta?: string; tone: Tone }
	| {
			kind: 'failure';
			key: string;
			stageId: string;
			title: string;
			error?: string;
	  }
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
			delivered: boolean;
			ts: string;
			deliveredAt?: string;
	  }
	| {
			kind: 'question';
			key: string;
			pingId: string;
			type: string;
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

const promptLabelKeys = {
	stage: 'transcript.prompt.stage',
	answer: 'transcript.prompt.answer',
	restart: 'transcript.prompt.restart',
	messages: 'transcript.prompt.messages',
	handback: 'transcript.prompt.handback'
} as const;
export const promptLabel = (k: string) =>
	k in promptLabelKeys ? t(promptLabelKeys[k as keyof typeof promptLabelKeys]) : k;

const cost = (u: unknown) => {
	const c = (u as { cost_usd?: number } | undefined)?.cost_usd;
	return typeof c === 'number' && c > 0 ? `$${c.toFixed(3)}` : '';
};
const dur = (ms: unknown) => {
	const s = Math.round(Number(ms ?? 0) / 1000);
	if (!s) return '';
	return s >= 60 ? `${Math.floor(s / 60)}m${String(s % 60).padStart(2, '0')}s` : `${s}s`;
};

export function buildTranscript(events: RunEvent[], live: LiveState): ChatItem[] {
	const items: ChatItem[] = [];
	const delivered = new Map<string, string>();
	const answered = new Map<string, Record<string, unknown>>();
	const persistedSegments = new Set<string>();
	const persistedTools = new Set<string>();
	for (const e of events) {
		if (e.type === 'message_delivered') {
			for (const id of (e.payload.message_ids as string[] | undefined) ?? [])
				delivered.set(id, e.ts);
		} else if (e.type === 'ping_answered') {
			answered.set(String(e.payload.ping_id), e.payload);
		} else if (e.type === 'agent_message') {
			persistedSegments.add(String(e.payload.segment ?? ''));
		} else if (e.type === 'agent_tool') {
			persistedTools.add(String(e.payload.tool_call_id ?? ''));
		}
	}

	let planAt = -1; // index of the current attempt's plan item (one per attempt)
	for (const e of events) {
		const p = e.payload;
		const key = `e${e.seq}`;
		switch (e.type) {
			case 'stage_started':
				planAt = -1;
				items.push({
					kind: 'stage',
					key,
					stageId: String(p.stage_id),
					iteration: Number(p.iteration ?? 1),
					agentic: Boolean(p.agent || p.harness),
					agent: p.agent as string | undefined,
					model: p.model as string | undefined,
					ts: e.ts
				});
				break;
			case 'stage_completed':
				if (p.skipped) {
					items.push({
						kind: 'status',
						key,
						glyph: I.ok,
						text: t('transcript.stageSkipped', { stage: String(p.stage_id) }),
						meta: String(p.note ?? ''),
						tone: 'ok'
					});
					break;
				}
				items.push({
					kind: 'status',
					key,
					glyph: I.ok,
					text: t('transcript.stageCompleted', { stage: String(p.stage_id) }),
					meta: [
						p.turns_used ? t('transcript.turns', { count: Number(p.turns_used) }) : '',
						cost(p.usage),
						dur(p.duration_ms)
					]
						.filter(Boolean)
						.join(' · '),
					tone: 'ok'
				});
				break;
			case 'stage_failed':
				items.push({
					kind: 'failure',
					key,
					stageId: String(p.stage_id),
					title:
						t('transcript.stageFailed', {
							stage: String(p.stage_id),
							reason: String(p.reason ?? '').replace(/_/g, ' ')
						}) +
						(typeof p.exit_code === 'number'
							? t('transcript.exitCode', { code: p.exit_code })
							: ''),
					error: p.error ? String(p.error).slice(0, 600) : undefined
				});
				break;
			case 'env_failed':
				items.push({
					kind: 'failure',
					key,
					stageId: '_provision',
					title: t('transcript.provisionFailed'),
					error: p.error ? String(p.error).slice(0, 600) : undefined
				});
				break;
			case 'stage_paused':
				items.push({
					kind: 'status',
					key,
					glyph: I.pause,
					text: p.reason === 'held' ? t('transcript.parkedHeld') : t('transcript.parked'),
					tone: 'warn'
				});
				break;
			case 'stage_resumed':
				items.push({
					kind: 'status',
					key,
					glyph: p.reason === 'engine_restart' ? '⟳' : I.retry,
					text:
						p.reason === 'engine_restart'
							? t('transcript.resumedRestart')
							: p.reason === 'message'
								? t('transcript.resumedMessage')
								: p.reason === 'released'
									? t('transcript.resumedReleased')
									: t('transcript.resumedAnswer'),
					tone: 'info'
				});
				break;
			case 'hold_taken':
				items.push({
					kind: 'status',
					key,
					glyph: I.hold,
					text: t('transcript.holdTaken'),
					tone: 'warn'
				});
				break;
			case 'hold_released':
				items.push({
					kind: 'status',
					key,
					glyph: I.hold,
					text:
						p.reason === 'stage_ended' ? t('transcript.holdEnded') : t('transcript.holdReleased'),
					meta: [
						dur(p.duration_ms),
						p.turns ? t('transcript.turns', { count: Number(p.turns) }) : '',
						p.messages ? t('transcript.messages', { count: Number(p.messages) }) : '',
						cost(p.usage)
					]
						.filter(Boolean)
						.join(' · '),
					tone: 'info'
				});
				break;
			case 'edge_taken':
				items.push({
					kind: 'status',
					key,
					glyph: I.loop,
					text: `${p.from} → ${p.to} (${p.count}/${p.max === -1 ? '∞' : p.max})`,
					meta: String(p.edge ?? '').replace(/_/g, ' '),
					tone: 'warn'
				});
				break;
			case 'run_completed':
				items.push({
					kind: 'status',
					key,
					glyph: I.ok,
					text: t('transcript.runCompleted'),
					meta: [
						cost(p.total_cost_usd !== undefined ? { cost_usd: p.total_cost_usd } : undefined),
						p.total_turns ? t('transcript.turns', { count: Number(p.total_turns) }) : ''
					]
						.filter(Boolean)
						.join(' · '),
					tone: 'ok'
				});
				break;
			case 'run_failed':
				items.push({
					kind: 'status',
					key,
					glyph: I.bad,
					text: t('transcript.runFailed', { reason: String(p.reason ?? '') }),
					tone: 'bad'
				});
				break;
			case 'run_abandoned':
				items.push({
					kind: 'status',
					key,
					glyph: I.stop,
					text:
						t('transcript.runAbandoned') +
						(p.stalled_at ? t('transcript.abandonedAt', { stage: String(p.stalled_at) }) : '') +
						(p.reason ? ` — ${p.reason}` : ''),
					tone: 'warn'
				});
				break;
			case 'run_retried':
			case 'run_resumed': // the pre-rename spelling, still in stored history
				items.push({
					kind: 'status',
					key,
					glyph: I.retry,
					text: t('transcript.retrying'),
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
					delivered: delivered.has(String(p.message_id)),
					deliveredAt: delivered.get(String(p.message_id)),
					ts: e.ts
				});
				break;
			case 'ping_raised':
				if (p.type === 'gate' || p.type === 'phase_confirm' || p.type === 'debug') break;
				{
					const form = toPingForm(p.form);
					const a = answered.get(String(p.ping_id));
					// What it offered: a lone field's options, else the fields.
					const offered =
						form?.fields.length === 1
							? form.fields[0].options.map((o) => o.label)
							: (form?.fields ?? []).map(fieldLabel);
					let answer: string | undefined;
					if (a) {
						answer = answerSummary({
							answerKind: answerKindOf(p.answer_kind, String(p.type ?? '')),
							form,
							answer: String(a.answer ?? ''),
							content: toFormContent(a.content)
						});
						if (a.feedback) answer += ` — ${a.feedback}`;
					}
					items.push({
						kind: 'question',
						key,
						pingId: String(p.ping_id),
						type: String(p.type ?? ''),
						question: String(p.question ?? ''),
						options: offered,
						answer
					});
				}
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
			const tc = live.tools.get(id);
			if (!tc || persistedTools.has(id)) continue;
			if (!liveTools) {
				liveTools = [];
				items.push({ kind: 'tools', key: 'live-' + k, calls: liveTools, live: true });
			}
			liveTools.push(tc);
		}
	}
	return items;
}
