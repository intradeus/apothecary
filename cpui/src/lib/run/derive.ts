// What the run page reads out of a run's event log. Pure functions: the
// store holds the events, the views render what comes out of here.

import type { Run, RunEvent } from '$lib/domain';
import { fmtClockDuration, fmtCost, fmtDuration, isTerminal } from '$lib/format';
import { answerKindOf, toPingOption, type PingLike } from '$lib/ping';
import type { StageStatus } from '$lib/ui/StageFlask.svelte';

/** The provisioning log (clone + build + bootstrap) is filed under a
 *  pseudo-stage id the flow grammar can't produce, served by /logs/{stage}. */
export const PROVISION_STAGE = '_provision';

/** Cost and turns over every leg: a stage that paused on a question banks
 *  what it spent up to there. */
export function totals(events: RunEvent[]) {
	let cost = 0;
	let turns = 0;
	for (const e of events) {
		if (e.type === 'stage_completed' || e.type === 'stage_paused') {
			const u = e.payload.usage as { cost_usd?: number } | undefined;
			cost += u?.cost_usd ?? 0;
			turns += (e.payload.turns_used as number | undefined) ?? 0;
		}
	}
	return { cost, turns };
}

export interface RunPing extends PingLike {
	stageId?: string;
	raisedAt: string;
	status: 'pending' | 'answered' | 'withdrawn';
	seq: number;
	attachmentIds?: string[];
}

/** Every ping this run raised, oldest first, from ping_raised/answered. A
 *  finished run's unanswered pings were withdrawn with it. */
export function pingsOf(events: RunEvent[], run: Run | null): RunPing[] {
	const answered = new Map<string, RunEvent>();
	for (const e of events)
		if (e.type === 'ping_answered') answered.set(String(e.payload.ping_id), e);
	const over = run ? isTerminal(run.state) : false;
	return events
		.filter((e) => e.type === 'ping_raised')
		.map((e) => {
			const p = e.payload;
			const a = answered.get(String(p.ping_id));
			const files = p.files as string[] | undefined;
			return {
				id: String(p.ping_id),
				type: String(p.type ?? ''),
				answerKind: answerKindOf(p.answer_kind, String(p.type ?? ''), p.options),
				question: String(p.question ?? ''),
				context: p.context as string | undefined,
				files,
				link: p.link as string | undefined,
				// The engine's derivation for the inbox projection: the link,
				// else the first file. With a file, PingCard offers review.
				primaryRef: (p.link as string | undefined) || files?.[0],
				options: ((p.options as unknown[] | undefined) ?? []).map(toPingOption),
				stageId: p.stage_id as string | undefined,
				raisedAt: e.ts,
				seq: e.seq,
				status: a ? 'answered' : over ? 'withdrawn' : 'pending',
				answer: a ? String(a.payload.answer ?? '') : undefined,
				feedback: a ? (a.payload.feedback as string | undefined) : undefined,
				answeredAt: a?.ts,
				attachmentIds: a ? (a.payload.attachment_ids as string[] | undefined) : undefined
			} satisfies RunPing;
		});
}

/** The stage in flight: the latest start with no completion, failure or
 *  pause after it (a resume puts it back in flight). */
export function currentStage(events: RunEvent[]): RunEvent | null {
	const started = events.findLast((e) => e.type === 'stage_started');
	if (!started) return null;
	const idx = events.lastIndexOf(started);
	const last = events
		.slice(idx + 1)
		.findLast(
			(e) =>
				['stage_completed', 'stage_failed', 'stage_paused', 'stage_resumed'].includes(e.type) &&
				e.payload.stage_id === started.payload.stage_id
		);
	return !last || last.type === 'stage_resumed' ? started : null;
}

/** When the in-flight stage started working: a resume after a question
 *  counts from the resume — the human's thinking time is not the agent's. */
export function workingSince(events: RunEvent[], cur: RunEvent | null): string | null {
	if (!cur) return null;
	const idx = events.lastIndexOf(cur);
	const resumed = events
		.slice(idx + 1)
		.findLast((e) => e.type === 'stage_resumed' && e.payload.stage_id === cur.payload.stage_id);
	return (resumed ?? cur).ts;
}

export interface StageDef {
	id: string;
	/** agent | command | human_gate | action | wait (or '' when unknown). */
	type: string;
	agent?: string;
}

export interface StageRow extends StageDef {
	status: StageStatus;
	line: string;
	attempt: number;
	/** The stage was re-entered by a loop-back. */
	looped: boolean;
}

/** Each stage's status (DESIGN 3.4), in recipe order. Stages the log
 *  mentions but the recipe doesn't (an older flow version) are appended. */
export function stageRows(
	defs: StageDef[],
	events: RunEvent[],
	run: Run | null,
	now: number
): StageRow[] {
	const order = [...defs];
	for (const e of events)
		if (e.type === 'stage_started' && !order.some((d) => d.id === e.payload.stage_id))
			order.push({
				id: String(e.payload.stage_id),
				type: e.payload.agent || e.payload.harness ? 'agent' : '',
				agent: e.payload.agent as string | undefined
			});
	const idx = new Map(order.map((d, i) => [d.id, i]));
	const state = run?.state;
	const over = state ? isTerminal(state) : false;

	// Stages parked on the debug leaf: a pending debug ping names its stage.
	const parked = new Set<string>();
	const debugStage = new Map<string, string>();
	for (const e of events) {
		if (e.type === 'ping_raised' && e.payload.type === 'debug') {
			debugStage.set(String(e.payload.ping_id), String(e.payload.stage_id));
			parked.add(String(e.payload.stage_id));
		} else if (e.type === 'ping_answered') {
			const sid = debugStage.get(String(e.payload.ping_id));
			if (sid) parked.delete(sid);
		}
	}

	const rows: (StageRow & { endSeq: number; startSeq: number })[] = order.map((d) => {
		const mine = events.filter((e) => e.payload.stage_id === d.id);
		const start = mine.findLast((e) => e.type === 'stage_started');
		const base = { ...d, attempt: 0, looped: false, endSeq: -1, startSeq: -1 };
		if (!start) return { ...base, status: 'pending' as StageStatus, line: '' };
		const attempt = Number(start.payload.iteration ?? 1);
		const end = mine
			.filter((e) => e.seq > start.seq)
			.findLast((e) =>
				['stage_completed', 'stage_failed', 'stage_paused', 'stage_resumed'].includes(e.type)
			);
		const r = { ...base, attempt, startSeq: start.seq, endSeq: end?.seq ?? start.seq };
		if (end?.type === 'stage_completed') {
			const u = end.payload.usage as { cost_usd?: number } | undefined;
			const bits = [
				'done',
				u?.cost_usd ? fmtCost(u.cost_usd) : '',
				end.payload.duration_ms ? fmtDuration(Number(end.payload.duration_ms)) : ''
			].filter(Boolean);
			return { ...r, status: 'done' as StageStatus, line: bits.join(' · ') };
		}
		if (!over && end?.type === 'stage_failed' && parked.has(d.id))
			return {
				...r,
				status: 'waiting' as StageStatus,
				line: 'failed — parked for you to check why'
			};
		if (end?.type === 'stage_failed')
			return {
				...r,
				status: 'failed' as StageStatus,
				line: String(end.payload.reason ?? 'failed').replace(/_/g, ' ')
			};
		if (!over && (end?.type === 'stage_paused' || state === 'waiting_on_human'))
			return { ...r, status: 'waiting' as StageStatus, line: 'waiting on you' };
		if (state === 'paused')
			return { ...r, status: 'paused' as StageStatus, line: 'paused — will re-enter' };
		if (over)
			return {
				...r,
				status: (state === 'failed' ? 'failed' : 'paused') as StageStatus,
				line: state === 'failed' ? 'failed' : 'stopped'
			};
		// Working: the clock counts from the start or the latest resume.
		const since = end?.type === 'stage_resumed' ? end.ts : start.ts;
		return {
			...r,
			status: 'running' as StageStatus,
			line: `attempt ${attempt} · ${fmtClockDuration(now - new Date(since).getTime())}`
		};
	});

	// A loop-back re-opens everything between its target and its source that
	// finished before it: those stages run again.
	for (const e of events) {
		if (e.type !== 'edge_taken') continue;
		const from = idx.get(String(e.payload.from));
		const to = idx.get(String(e.payload.to));
		if (from === undefined || to === undefined || to > from) continue;
		for (let i = to; i <= from; i++) {
			const r = rows[i];
			if (r.startSeq >= 0 && r.endSeq < e.seq && r.startSeq < e.seq) {
				rows[i] = { ...r, status: 'pending', line: '↩ runs again', looped: true };
			}
		}
		if (rows[to].startSeq > e.seq) rows[to] = { ...rows[to], looped: true };
	}
	for (const r of rows) {
		if (r.status === 'running' || r.status === 'waiting' || r.status === 'done') continue;
		if (r.status === 'pending' && !r.line) r.line = r.type === 'human_gate' ? 'gate' : r.type;
	}
	return rows.map(({ endSeq: _e, startSeq: _s, ...r }) => r);
}

export type Tone = 'ok' | 'bad' | 'warn' | 'info' | '';

export interface Described {
	text: string;
	tone: Tone;
	/** The stage whose log this row can show. */
	stage?: string;
	snapshot?: string;
}

/** One timeline row in words (DESIGN §6 Timeline). */
export function describe(e: RunEvent): Described {
	const p = e.payload;
	switch (e.type) {
		case 'snapshot_captured': {
			if (p.kind === 'ping_file')
				return {
					text: `captured ${p.path} for a ping (as shown to you)`,
					tone: '',
					snapshot: p.snapshot_id as string
				};
			const what = p.kind === 'log' ? 'log' : `output ${p.name || ''}`.trim();
			const where = p.path ? ` — ${p.path}` : '';
			return {
				text: `${p.stage_id} (attempt ${p.iteration ?? 0}) captured ${what}${where}`,
				tone: '',
				snapshot: p.snapshot_id as string
			};
		}
		case 'run_created':
			return { text: `run created — “${p.ticket_title}” (flow ${p.flow_name})`, tone: 'info' };
		case 'env_requested':
			return { text: 'provisioning workspace…', tone: '', stage: PROVISION_STAGE };
		case 'image_built':
			return { text: `image built${p.cached ? ' (from cache)' : ''}`, tone: '' };
		case 'env_ready':
			return { text: `workspace ready (${p.env_id})`, tone: 'ok', stage: PROVISION_STAGE };
		case 'env_failed':
			return { text: `environment failed: ${p.error}`, tone: 'bad', stage: PROVISION_STAGE };
		case 'env_rebuild_started':
			return { text: 'workspace rebuild started', tone: 'info' };
		case 'env_rebuild_completed':
			return { text: 'workspace rebuilt', tone: 'ok' };
		case 'env_rebuild_failed':
			return { text: `workspace rebuild failed${p.error ? `: ${p.error}` : ''}`, tone: 'bad' };
		case 'stage_started':
			return {
				text: `${p.stage_id} started (attempt ${p.iteration})${p.model ? ` · ${p.model}` : ''}`,
				tone: 'info',
				stage: p.stage_id as string
			};
		case 'stage_completed': {
			const u = p.usage as { cost_usd?: number } | undefined;
			const bits = [
				p.turns_used ? `${p.turns_used} turns` : '',
				u?.cost_usd ? fmtCost(u.cost_usd) : '',
				`${Math.round((p.duration_ms as number) / 1000)}s`
			]
				.filter(Boolean)
				.join(' · ');
			return { text: `${p.stage_id} completed (${bits})`, tone: 'ok', stage: p.stage_id as string };
		}
		case 'stage_paused': {
			const u = p.usage as { cost_usd?: number } | undefined;
			const spent = [
				p.turns_used ? `${p.turns_used} turns` : '',
				u?.cost_usd ? fmtCost(u.cost_usd) : ''
			]
				.filter(Boolean)
				.join(' · ');
			const why = p.reason === 'ask_human' ? 'waiting on its own question' : String(p.reason ?? '');
			return {
				text: `${p.stage_id} paused — ${why}${spent ? ` (${spent} so far)` : ''}`,
				tone: 'warn',
				stage: p.stage_id as string
			};
		}
		case 'stage_resumed':
			return {
				text: `${p.stage_id} resumed — ${p.reason === 'engine_restart' ? 'engine restarted, picking the session back up' : 'the answer landed'}`,
				tone: 'info',
				stage: p.stage_id as string
			};
		case 'stage_failed':
			return {
				text: `${p.stage_id} failed: ${p.reason}${p.error ? ` — ${String(p.error).slice(0, 200)}` : ''}`,
				tone: 'bad',
				stage: p.stage_id as string
			};
		case 'action_started':
			return { text: `${p.stage_id}: action ${p.action} started`, tone: 'info' };
		case 'edge_taken':
			return {
				text: `↩ ${p.edge}${p.depth ? ` (then #${p.depth})` : ''} ${p.from} → ${p.to} (${p.count}/${p.max === -1 ? '∞' : p.max})`,
				tone: 'warn'
			};
		case 'ping_raised':
			if (p.type === 'debug')
				return { text: `${p.stage_id} failed — parked for debugging`, tone: 'warn' };
			return { text: `ping [${p.type}]: ${p.question}`, tone: 'warn' };
		case 'ping_answered':
			return { text: `answered: ${p.answer}${p.feedback ? ` — ${p.feedback}` : ''}`, tone: 'ok' };
		case 'verdict_received':
			return {
				text: p.approve
					? 'review verdict: APPROVE'
					: `review verdict: REJECT (${p.n_comments} comments)`,
				tone: p.approve ? 'ok' : 'warn'
			};
		case 'phase_started':
			return {
				text: p.subject
					? `phase ${p.phase} on ${p.subject === 'repo' ? 'the merge request' : p.subject} (${p.by === 'human' ? 'started by hand' : 'triggered'})`
					: `phase ${p.phase}`,
				tone: 'info'
			};
		case 'phase_completed':
			return { text: `phase ${p.phase} done`, tone: 'ok' };
		case 'phase_declined':
			return { text: `phase ${p.phase} declined`, tone: 'warn' };
		case 'review_comments_loaded': {
			const n = Array.isArray(p.thread_ids) ? p.thread_ids.length : 0;
			return { text: `loaded ${n} review thread${n === 1 ? '' : 's'}`, tone: 'info' };
		}
		case 'review_replies_posted': {
			const n = Array.isArray(p.thread_ids) ? p.thread_ids.length : 0;
			return { text: `replied to ${n} review thread${n === 1 ? '' : 's'}`, tone: 'ok' };
		}
		case 'branch_pushed':
			return {
				text: `pushed ${p.branch}${p.repo && p.repo !== 'repo' ? ` (${p.repo})` : ''} at ${String(p.tip ?? '').slice(0, 8)}`,
				tone: 'ok'
			};
		case 'target_synced':
			return {
				text: `took ${String(p.target_tip ?? '').slice(0, 10)} in (${p.strategy})`,
				tone: 'ok'
			};
		case 'mr_created':
			return {
				text: `merge request created: ${p.mr_url}${p.target ? ` (${p.branch} → ${p.target})` : ''}`,
				tone: 'ok'
			};
		case 'run_completed':
			return {
				text: `run completed — ${fmtCost(p.total_cost_usd)} · ${p.total_turns} turns · ${p.unwanted_pings} unwanted pings`,
				tone: 'ok'
			};
		case 'run_abandoned':
			return { text: `abandoned at ${p.stalled_at}: ${p.reason}`, tone: 'bad' };
		case 'run_failed':
			return { text: `run failed: ${p.reason}`, tone: 'bad' };
		case 'run_retried':
		case 'run_resumed': // the pre-rename spelling, still in stored history
			return { text: 'retry — re-entering the failed stage in the same workspace', tone: 'info' };
		case 'run_paused':
			// Parked by the engine on a human is not the same as paused by one:
			// the first releases the slot and comes back on its own.
			return p.reason === 'waiting_on_human'
				? {
						text: `parked at ${p.stage_id} — waiting for you (the run released its slot)`,
						tone: ''
					}
				: { text: `paused${p.stage_id ? ` at ${p.stage_id}` : ''}`, tone: '' };
		case 'run_unpaused':
			return p.reason === 'answered'
				? { text: 'picked back up — your answer landed', tone: 'info' }
				: { text: 'play — continuing where it left off', tone: 'info' };
		case 'run_archived':
			return { text: 'archived', tone: '' };
		case 'env_destroyed':
			return { text: 'workspace destroyed', tone: '' };
		default:
			return { text: e.type.replace(/_/g, ' '), tone: '' };
	}
}

/** Transcript events have their own view; the timeline keeps to the run's
 *  lifecycle so it stays readable. */
export const TRANSCRIPT_TYPES = new Set([
	'agent_session',
	'agent_prompted',
	'agent_message',
	'agent_tool',
	'agent_plan',
	'agent_notice',
	'message_queued',
	'message_delivered'
]);

/** Whether a timeline row can show a log: a finished attempt always; env
 *  rows the provisioning log once it exists (env_requested only for a run
 *  that ended mid-build, the one place left to hang it). */
export function hasLog(e: RunEvent, stage: string | undefined, events: RunEvent[], over: boolean) {
	if (!stage) return false;
	if (e.type === 'stage_completed' || e.type === 'stage_failed' || e.type === 'stage_paused')
		return true;
	if (e.type === 'env_ready' || e.type === 'env_failed') return true;
	return (
		e.type === 'env_requested' &&
		over &&
		!events.some((x) => x.type === 'env_ready' || x.type === 'env_failed')
	);
}

export interface EnvLink {
	kind: string;
	url?: string;
	command?: string;
}

/** code-server has its own Editor button: never list its port as an app. */
export const CODE_SERVER_PORT = 8471;

/** The workspace's links: env_ready's, with app ports from the LIVE
 *  registry (env_ready's copies go stale when the engine restarts). */
export function envLinks(events: RunEvent[], exposed: { port: number; url: string }[]): EnvLink[] {
	const fromEvent =
		(events.findLast((e) => e.type === 'env_ready')?.payload.links as EnvLink[] | undefined) ?? [];
	const base = exposed.length ? fromEvent.filter((l) => l.kind !== 'app') : fromEvent;
	return [
		...base,
		...exposed
			.filter((p) => p.port !== CODE_SERVER_PORT)
			.map((p) => ({ kind: `app :${p.port}`, url: p.url }))
	];
}

/** Split a title into plain words and ONE serif accent phrase: the last two
 *  or three words. */
export function accentSplit(title: string): [string, string] {
	const words = title.trim().split(/\s+/);
	const n = words.length >= 7 ? 3 : words.length >= 3 ? 2 : words.length === 2 ? 1 : 0;
	if (!n) return [title, ''];
	return [words.slice(0, -n).join(' '), words.slice(-n).join(' ')];
}
