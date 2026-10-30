// What the run page reads out of a run's event log. Pure functions: the
// store holds the events, the views render what comes out of here.

import type { Run, RunEvent, ServiceStatus } from '$lib/domain';
import { MAIN } from '$lib/flowdoc';
import { fmtClockDuration, fmtCost, fmtDuration, isTerminal } from '$lib/format';
import { t } from '$lib/i18n/index.svelte';
import {
	answerKindOf,
	toCuratedMap,
	toFormContent,
	toPingForm,
	toReviewComment,
	type PingLike
} from '$lib/ping';
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
			const fileTypes = p.file_types as Record<string, string> | undefined;
			return {
				id: String(p.ping_id),
				type: String(p.type ?? ''),
				answerKind: answerKindOf(p.answer_kind, String(p.type ?? '')),
				question: String(p.question ?? ''),
				context: p.context as string | undefined,
				files,
				fileTypes: fileTypes && typeof fileTypes === 'object' ? fileTypes : undefined,
				link: p.link as string | undefined,
				// The engine's derivation for the inbox projection: the link,
				// else the first file. With files, PingCard offers review.
				primaryRef: (p.link as string | undefined) || files?.[0],
				form: toPingForm(p.form),
				phase: (p.phase as string | undefined) || undefined,
				endsPhase: p.ends_phase === true,
				stageId: p.stage_id as string | undefined,
				raisedAt: e.ts,
				seq: e.seq,
				status: a ? 'answered' : over ? 'withdrawn' : 'pending',
				answer: a ? String(a.payload.answer ?? '') : undefined,
				feedback: a ? (a.payload.feedback as string | undefined) : undefined,
				content: a ? toFormContent(a.payload.content) : undefined,
				review: ((a?.payload.review as unknown[] | undefined) ?? []).map(toReviewComment),
				curated: a ? toCuratedMap(a.payload.curated) : undefined,
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
	/** The chain it belongs to: main, or a watcher's or an outcome's id. */
	phase?: string;
}

/** The stages a run's strip shows: the main chain, what the run starts with,
 *  then every other chain in the order it first started — a flow that only
 *  watches shows its first chain until one does. */
export function shownStages(defs: StageDef[], events: RunEvent[]): StageDef[] {
	if (!defs.some((d) => d.phase)) return defs;
	const order = [defs.some((d) => d.phase === MAIN) ? MAIN : defs[0].phase!];
	for (const e of events) {
		const ph = e.type === 'phase_started' ? String(e.payload.phase ?? '') : '';
		if (ph && !order.includes(ph)) order.push(ph);
	}
	return order.flatMap((ph) => defs.filter((d) => d.phase === ph));
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
				end.payload.skipped ? t('run.stage.skipped') : t('run.stage.done'),
				u?.cost_usd ? fmtCost(u.cost_usd) : '',
				end.payload.duration_ms ? fmtDuration(Number(end.payload.duration_ms)) : ''
			].filter(Boolean);
			return { ...r, status: 'done' as StageStatus, line: bits.join(' · ') };
		}
		if (!over && end?.type === 'stage_failed' && parked.has(d.id))
			return {
				...r,
				status: 'waiting' as StageStatus,
				line: t('run.stage.parked')
			};
		if (end?.type === 'stage_failed')
			return {
				...r,
				status: 'failed' as StageStatus,
				line: String(end.payload.reason ?? 'failed').replace(/_/g, ' ')
			};
		if (!over && (end?.type === 'stage_paused' || state === 'waiting_on_human'))
			return { ...r, status: 'waiting' as StageStatus, line: t('run.stage.waiting') };
		if (state === 'paused')
			return { ...r, status: 'paused' as StageStatus, line: t('run.stage.pausedReenter') };
		if (over)
			return {
				...r,
				status: (state === 'failed' ? 'failed' : 'paused') as StageStatus,
				line: state === 'failed' ? t('run.stage.failed') : t('run.stage.stopped')
			};
		// Working: the clock counts from the start or the latest resume.
		const since = end?.type === 'stage_resumed' ? end.ts : start.ts;
		return {
			...r,
			status: 'running' as StageStatus,
			line: t('run.stage.working', {
				attempt,
				clock: fmtClockDuration(now - new Date(since).getTime())
			})
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
				rows[i] = { ...r, status: 'pending', line: t('run.stage.runsAgain'), looped: true };
			}
		}
		if (rows[to].startSeq > e.seq) rows[to] = { ...rows[to], looped: true };
	}
	for (const r of rows) {
		if (r.status === 'running' || r.status === 'waiting' || r.status === 'done') continue;
		if (r.status === 'pending' && !r.line)
			r.line = r.type === 'human_gate' ? t('run.stage.gate') : r.type;
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
					text: t('run.event.capturedPing', { path: String(p.path) }),
					tone: '',
					snapshot: p.snapshot_id as string
				};
			const what =
				p.kind === 'log'
					? t('run.event.log')
					: t('run.event.output', { name: String(p.name || '') }).trim();
			const where = p.path ? ` — ${p.path}` : '';
			return {
				text: t('run.event.captured', {
					stage: String(p.stage_id),
					attempt: Number(p.iteration ?? 0),
					what,
					where
				}),
				tone: '',
				snapshot: p.snapshot_id as string
			};
		}
		case 'run_created':
			return {
				text: t('run.event.runCreated', {
					title: String(p.ticket_title),
					flow: String(p.flow_name)
				}),
				tone: 'info'
			};
		case 'env_requested':
			return { text: t('run.event.provisioning'), tone: '', stage: PROVISION_STAGE };
		case 'image_built':
			return {
				text: t(p.cached ? 'run.event.imageBuiltCached' : 'run.event.imageBuilt'),
				tone: ''
			};
		case 'env_ready':
			return {
				text: t('run.event.wsReady', { env: String(p.env_id) }),
				tone: 'ok',
				stage: PROVISION_STAGE
			};
		case 'env_failed':
			return {
				text: t('run.event.envFailed', { error: String(p.error) }),
				tone: 'bad',
				stage: PROVISION_STAGE
			};
		case 'env_rebuild_started':
			return { text: t('run.event.rebuildStarted'), tone: 'info' };
		case 'env_rebuild_completed':
			return { text: t('run.event.rebuilt'), tone: 'ok' };
		case 'env_rebuild_failed':
			return {
				text: p.error
					? t('run.event.rebuildFailedWith', { error: String(p.error) })
					: t('run.event.rebuildFailed'),
				tone: 'bad'
			};
		case 'stage_started':
			return {
				text: `${t('run.event.stageStarted', { stage: String(p.stage_id), attempt: Number(p.iteration) })}${p.model ? ` · ${p.model}` : ''}`,
				tone: 'info',
				stage: p.stage_id as string
			};
		case 'stage_completed': {
			if (p.skipped)
				return {
					text: t('run.event.stageSkipped', {
						stage: String(p.stage_id),
						note: String(p.note ?? '')
					}),
					tone: 'info',
					stage: p.stage_id as string
				};
			const u = p.usage as { cost_usd?: number } | undefined;
			const bits = [
				p.turns_used ? t('run.event.turns', { count: Number(p.turns_used) }) : '',
				u?.cost_usd ? fmtCost(u.cost_usd) : '',
				`${Math.round((p.duration_ms as number) / 1000)}s`
			]
				.filter(Boolean)
				.join(' · ');
			return {
				text: t('run.event.stageCompleted', { stage: String(p.stage_id), bits }),
				tone: 'ok',
				stage: p.stage_id as string
			};
		}
		case 'stage_paused': {
			const u = p.usage as { cost_usd?: number } | undefined;
			const spent = [
				p.turns_used ? t('run.event.turns', { count: Number(p.turns_used) }) : '',
				u?.cost_usd ? fmtCost(u.cost_usd) : ''
			]
				.filter(Boolean)
				.join(' · ');
			const why =
				p.reason === 'ask_human'
					? t('run.event.ownQuestion')
					: p.reason === 'held'
						? t('run.event.quietHolder')
						: String(p.reason ?? '');
			return {
				text: spent
					? t('run.event.stagePausedSpent', { stage: String(p.stage_id), why, spent })
					: t('run.event.stagePaused', { stage: String(p.stage_id), why }),
				tone: 'warn',
				stage: p.stage_id as string
			};
		}
		case 'stage_resumed':
			return {
				text: t(
					p.reason === 'engine_restart'
						? 'run.event.resumedRestart'
						: p.reason === 'message'
							? 'run.event.resumedMessage'
							: p.reason === 'released'
								? 'run.event.resumedReleased'
								: 'run.event.resumedAnswer',
					{ stage: String(p.stage_id) }
				),
				tone: 'info',
				stage: p.stage_id as string
			};
		case 'hold_taken':
			return {
				text: t('run.event.holdTaken', { stage: String(p.stage_id) }),
				tone: 'warn',
				stage: p.stage_id as string
			};
		case 'hold_released': {
			const u = p.usage as { cost_usd?: number } | undefined;
			const bits = [
				fmtDuration(Number(p.duration_ms ?? 0)),
				p.turns ? t('run.event.turns', { count: Number(p.turns) }) : '',
				p.messages ? t('run.event.messages', { count: Number(p.messages) }) : '',
				u?.cost_usd ? fmtCost(u.cost_usd) : ''
			]
				.filter(Boolean)
				.join(' · ');
			return {
				text: t(p.reason === 'stage_ended' ? 'run.event.holdEnded' : 'run.event.holdReleased', {
					stage: String(p.stage_id),
					bits
				}),
				tone: 'info',
				stage: p.stage_id as string
			};
		}
		case 'stage_failed':
			return {
				text: `${t('run.event.stageFailed', { stage: String(p.stage_id), reason: String(p.reason) })}${p.error ? ` — ${String(p.error).slice(0, 200)}` : ''}`,
				tone: 'bad',
				stage: p.stage_id as string
			};
		case 'action_started':
			return {
				text: t('run.event.actionStarted', { stage: String(p.stage_id), action: String(p.action) }),
				tone: 'info'
			};
		case 'edge_taken':
			return {
				text: `↩ ${p.edge}${p.depth ? ` ${t('run.event.thenDepth', { depth: Number(p.depth) })}` : ''} ${p.from} → ${p.to} (${p.count}/${p.max === -1 ? '∞' : p.max})`,
				tone: 'warn'
			};
		case 'ping_raised':
			if (p.type === 'debug')
				return { text: t('run.event.parkedDebug', { stage: String(p.stage_id) }), tone: 'warn' };
			return {
				text: t('run.event.ping', { type: String(p.type), question: String(p.question) }),
				tone: 'warn'
			};
		case 'ping_answered': {
			// A form's answer reads as its values; the field names and option
			// labels live on the raise, which one event line does not see.
			const content = toFormContent(p.content);
			const answer =
				p.answer === 'accept' && content
					? Object.entries(content)
							.map(([k, v]) => `${k}: ${Array.isArray(v) ? v.join(', ') || '—' : String(v)}`)
							.join(' · ')
					: String(p.answer);
			return {
				text: `${t('run.event.answered', { answer })}${p.feedback ? ` — ${p.feedback}` : ''}`,
				tone: 'ok'
			};
		}
		case 'verdict_received':
			return {
				text: p.approve
					? t('run.event.approve')
					: t('run.event.reject', { count: Number(p.n_comments) }),
				tone: p.approve ? 'ok' : 'warn'
			};
		case 'phase_started':
			return {
				text: p.subject
					? t('run.event.phaseOn', {
							phase: String(p.phase),
							subject: p.subject === 'repo' ? t('run.event.theMr') : String(p.subject),
							how: p.by === 'human' ? t('run.event.byHand') : t('run.event.triggered')
						})
					: t('run.event.phase', { phase: String(p.phase) }),
				tone: 'info'
			};
		case 'phase_completed':
			// A phase that ended at a dead end says why; the run went back to
			// waiting either way.
			return p.reason
				? {
						text: t('run.event.phaseEnded', { phase: String(p.phase), reason: String(p.reason) }),
						tone: 'warn'
					}
				: { text: t('run.event.phaseDone', { phase: String(p.phase) }), tone: 'ok' };
		case 'phase_declined':
			return { text: t('run.event.phaseDeclined', { phase: String(p.phase) }), tone: 'warn' };
		case 'review_comments_loaded': {
			// The threads a phase run loaded: the pending ones for a fixing
			// round, every one for a reviewer reading before it writes.
			const n = Array.isArray(p.threads) ? p.threads.length : 0;
			return {
				text:
					p.scope === 'all'
						? t('run.event.threadsRead', { count: n })
						: t('run.event.threadsLoaded', { count: n }),
				tone: 'info'
			};
		}
		case 'review_replies_posted': {
			const n = Array.isArray(p.thread_ids) ? p.thread_ids.length : 0;
			const declined = Array.isArray(p.declined) ? p.declined.length : 0;
			return {
				text:
					t('run.event.threadsReplied', { count: n }) +
					(declined ? ` · ${t('run.event.repliesDeclined', { count: declined })}` : ''),
				tone: 'ok'
			};
		}
		case 'review_posted': {
			const inline = Number(p.inline ?? 0);
			const folded = Number(p.folded ?? 0);
			const replied = Array.isArray(p.replied) ? p.replied.length : 0;
			const resolved = Array.isArray(p.resolved) ? p.resolved.length : 0;
			if (p.already) return { text: t('run.event.reviewAlready'), tone: 'ok' };
			const parts: string[] = [];
			if (inline + folded > 0) parts.push(t('run.event.reviewPosted', { count: inline + folded }));
			if (folded) parts.push(t('run.event.reviewFolded', { count: folded }));
			if (replied) parts.push(t('run.event.reviewContinued', { count: replied }));
			if (resolved) parts.push(t('run.event.reviewResolved', { count: resolved }));
			if (!parts.length) parts.push(t('run.event.reviewSummaryOnly'));
			return { text: parts.join(' · '), tone: 'ok' };
		}
		case 'review_curated': {
			// What a person made of a review document at a gate: kept, dropped,
			// reworded — and whether the draft went back.
			const notes = p.type === 'review_threads';
			const review = p.review as { notes?: unknown[]; replies?: unknown[] } | undefined;
			const kept = notes
				? (review?.notes?.length ?? 0) + (review?.replies?.length ?? 0)
				: Array.isArray(p.replies)
					? p.replies.length
					: 0;
			const dropped =
				(Array.isArray(p.dropped) ? p.dropped.length : 0) +
				(Array.isArray(p.dropped_replies) ? p.dropped_replies.length : 0);
			const reworded = Array.isArray(p.rewritten) ? p.rewritten.length : 0;
			const parts = [
				notes
					? t('run.event.curatedNotes', { kept, total: kept + dropped })
					: t('run.event.curatedReplies', { kept, total: kept + dropped })
			];
			if (dropped) parts.push(t('run.event.curatedDropped', { count: dropped }));
			if (reworded) parts.push(t('run.event.curatedReworded', { count: reworded }));
			if (!p.approved) parts.push(t('run.event.curatedRejected'));
			return { text: parts.join(' · '), tone: p.approved ? 'info' : 'warn' };
		}
		case 'mr_merged':
			return {
				text: t('run.event.mrMerged', {
					method: String(p.method),
					head: String(p.head ?? '').slice(0, 8)
				}),
				tone: 'ok'
			};
		case 'branch_pushed':
			return {
				text: t('run.event.pushed', {
					branch: `${p.branch}${p.repo && p.repo !== 'repo' ? ` (${p.repo})` : ''}`,
					tip: String(p.tip ?? '').slice(0, 8)
				}),
				tone: 'ok'
			};
		case 'target_synced':
			return {
				text: t('run.event.targetSynced', {
					tip: String(p.target_tip ?? '').slice(0, 10),
					strategy: String(p.strategy)
				}),
				tone: 'ok'
			};
		case 'mr_created':
			return {
				text: `${t('run.event.mrCreated', { url: String(p.mr_url) })}${p.target ? ` (${p.branch} → ${p.target})` : ''}`,
				tone: 'ok'
			};
		case 'run_completed':
			return {
				text: t('run.event.runCompleted', {
					cost: fmtCost(p.total_cost_usd),
					turns: Number(p.total_turns),
					pings: Number(p.unwanted_pings)
				}),
				tone: 'ok'
			};
		case 'run_abandoned':
			// An outcome's `then: abort` names no stage: the flow ended the run.
			return {
				text: p.stalled_at
					? t('run.event.abandoned', { stage: String(p.stalled_at), reason: String(p.reason) })
					: t('run.event.abandonedBy', { reason: String(p.reason) }),
				tone: 'bad'
			};
		case 'run_failed':
			return { text: t('run.event.runFailed', { reason: String(p.reason) }), tone: 'bad' };
		case 'run_retried':
		case 'run_resumed': // the pre-rename spelling, still in stored history
			return { text: t('run.event.retried'), tone: 'info' };
		case 'run_paused':
			// Parked by the engine on a human is not the same as paused by one:
			// the first releases the slot and comes back on its own.
			return p.reason === 'waiting_on_human'
				? {
						text: t('run.event.parked', { stage: String(p.stage_id) }),
						tone: ''
					}
				: {
						text: p.stage_id
							? t('run.event.pausedAt', { stage: String(p.stage_id) })
							: t('run.event.paused'),
						tone: ''
					};
		case 'run_unpaused':
			if (p.reason === 'hold') return { text: t('run.event.pickedUpHold'), tone: 'info' };
			return p.reason === 'answered'
				? { text: t('run.event.pickedUp'), tone: 'info' }
				: { text: t('run.event.play'), tone: 'info' };
		case 'run_archived':
			return { text: t('run.event.archived'), tone: '' };
		case 'env_destroyed':
			return { text: t('run.event.destroyed'), tone: '' };
		case 'service_started': {
			const by =
				p.by === 'stage' && p.stage_id
					? t('run.event.serviceByStage', { stage: String(p.stage_id) })
					: t('run.event.serviceByHuman');
			return {
				text: t(p.restart ? 'run.event.serviceRestarted' : 'run.event.serviceStarted', {
					service: String(p.service),
					by
				}),
				tone: 'info'
			};
		}
		case 'service_ready':
			return {
				text: t('run.event.serviceReady', { service: String(p.service) }),
				tone: 'ok'
			};
		case 'service_stopped':
			return {
				text: p.reason
					? t('run.event.serviceStoppedWhy', {
							service: String(p.service),
							reason: String(p.reason)
						})
					: t('run.event.serviceStopped', { service: String(p.service) }),
				tone: ''
			};
		case 'service_failed':
			return {
				text: t('run.event.serviceFailed', {
					service: String(p.service),
					reason: String(p.reason ?? '')
				}),
				tone: 'bad'
			};
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
	if (e.type === 'stage_completed') return !e.payload.skipped;
	if (e.type === 'stage_failed' || e.type === 'stage_paused') return true;
	if (e.type === 'env_ready' || e.type === 'env_failed') return true;
	return (
		e.type === 'env_requested' &&
		over &&
		!events.some((x) => x.type === 'env_ready' || x.type === 'env_failed')
	);
}

export interface EnvLink {
	kind: string;
	url: string;
}

/** code-server has its own Editor button: never list its port as an app. */
export const CODE_SERVER_PORT = 8471;

/** The workspace's links — its app ports and a healthy service's previews.
 *  The LIVE registry is the source for exposed ports; env_ready's copies
 *  stand in only until it has any (they go stale when the engine restarts).
 *  Getting into the workspace is no link here: the SSH door's rows and the
 *  Editor button are live facts the header fetches on their own. */
export function envLinks(
	events: RunEvent[],
	exposed: { port: number; url: string }[],
	services: ServiceStatus[] = []
): EnvLink[] {
	const fromEvent =
		(events.findLast((e) => e.type === 'env_ready')?.payload.links as EnvLink[] | undefined) ?? [];
	const base = exposed.length ? [] : fromEvent.filter((l) => !!l.url);
	// A healthy service's preview links, named for it; its port may also be
	// on the exposed list — the service's name wins.
	const fromServices = services.flatMap((s) =>
		s.ports.flatMap((p) =>
			p.url ? [{ kind: `${s.name}${p.label ? ` · ${p.label}` : ''} :${p.port}`, url: p.url }] : []
		)
	);
	const taken = new Set(fromServices.map((l) => l.url));
	return [
		...base,
		...fromServices,
		...exposed
			.filter((p) => p.port !== CODE_SERVER_PORT && !taken.has(p.url))
			.map((p) => ({ kind: `app :${p.port}`, url: p.url }))
	];
}

/** The settings.json lines that let a desktop's extensions follow it into
 *  workspaces over SSH: VS Code installs on connection the listed ids it
 *  also has locally, and downloading on the desktop spares the workspace a
 *  trip to Microsoft's marketplace, which the project's egress may not
 *  allow. The list is what the workspace's own editor runs. */
export function vscodeRemoteSettings(extensions: string[] = []): string {
	return [
		`"remote.defaultExtensionsIfInstalledLocally": ${JSON.stringify(extensions)},`,
		`"remote.downloadExtensionsLocally": true`
	].join('\n');
}

/** Split a title into plain words and ONE serif accent phrase: the last two
 *  or three words. */
export function accentSplit(title: string): [string, string] {
	const words = title.trim().split(/\s+/);
	const n = words.length >= 7 ? 3 : words.length >= 3 ? 2 : words.length === 2 ? 1 : 0;
	if (!n) return [title, ''];
	return [words.slice(0, -n).join(' '), words.slice(-n).join(' ')];
}
