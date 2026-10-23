// Facts about a run the list endpoint does not carry: what it has cost, which
// attempt its current stage is on, and what TYPE that stage is. They are
// derived from the run's event log and its flow document, fetched lazily and
// only for the runs a page actually shows (desk cards, the active runs list,
// the inbox's selected run). Cached per run and refetched when the run's
// updatedAt moves; the flow document is cached per (workshop, flow hash),
// which never changes.
//
// Read-only GETs. A failed fetch degrades to "unknown" — the cells that read
// these show nothing rather than a wrong number.

import { SvelteMap } from 'svelte/reactivity';
import { parse } from 'yaml';
import { api } from '$lib/api';
import { expandDoc, normalizeDoc, type StageKind } from '$lib/flowdoc';
import type { Run, RunEvent, RunState } from '$lib/domain';
import type { StageStatus } from '$lib/ui/StageFlask.svelte';

export interface RunFacts {
	/** USD spent so far (every completed or paused stage leg). */
	cost: number;
	turns: number;
	/** The current stage's attempt number, when a stage_started names it. */
	attempt?: number;
	/** When the current stage's latest attempt started. */
	stageStartedAt?: string;
}

const facts = new SvelteMap<string, RunFacts & { at: string }>();
const inflight = new Set<string>();

function derive(run: Run, events: RunEvent[]): RunFacts {
	let cost = 0;
	let turns = 0;
	let attempt: number | undefined;
	let stageStartedAt: string | undefined;
	for (const e of events) {
		if (e.type === 'stage_completed' || e.type === 'stage_paused') {
			const u = e.payload.usage as { cost_usd?: number } | undefined;
			cost += u?.cost_usd ?? 0;
			turns += (e.payload.turns_used as number | undefined) ?? 0;
		} else if (e.type === 'stage_started' && e.payload.stage_id === run.currentStage) {
			const it = e.payload.iteration;
			attempt = typeof it === 'number' ? it : attempt;
			stageStartedAt = e.ts;
		}
	}
	return { cost, turns, attempt, stageStartedAt };
}

async function load(run: Run) {
	inflight.add(run.id);
	try {
		const { events } = await api.runs.get(run.id);
		facts.set(run.id, { ...derive(run, events), at: run.updatedAt });
	} catch {
		facts.set(run.id, { cost: 0, turns: 0, at: run.updatedAt });
	} finally {
		inflight.delete(run.id);
	}
}

/** The run's facts, or undefined while they load. Reading it schedules the
 *  fetch (async, so it is safe inside templates and deriveds). */
export function runFacts(run: Run): RunFacts | undefined {
	const f = facts.get(run.id);
	if ((!f || f.at !== run.updatedAt) && !inflight.has(run.id)) queueMicrotask(() => load(run));
	return f;
}

// --- stage types, from the run's pinned flow version -------------------------

const docs = new SvelteMap<string, Record<string, StageKind>>();
const docsInflight = new Set<string>();

async function loadDoc(key: string, projectId: string, hash: string) {
	docsInflight.add(key);
	try {
		const v = await api.flows.version(projectId, hash);
		const doc = expandDoc(normalizeDoc(parse(v.yaml)));
		docs.set(key, Object.fromEntries(doc.stages.map((s) => [s.id, s.type])));
	} catch {
		docs.set(key, {});
	} finally {
		docsInflight.delete(key);
	}
}

/** The type of the run's current stage (agent, command, human_gate…), or
 *  undefined when unknown. A run waiting on a human is at a gate or an
 *  agent's question; the flow document says which. */
export function stageType(run: Run): StageKind | undefined {
	if (!run.currentStage || !run.projectId || !run.flowHash) return undefined;
	const key = `${run.projectId}@${run.flowHash}`;
	const m = docs.get(key);
	if (!m && !docsInflight.has(key)) {
		const { projectId, flowHash } = run;
		queueMicrotask(() => loadDoc(key, projectId, flowHash));
	}
	return m?.[run.currentStage];
}

/** The flask a run's CURRENT stage shows in lists (DESIGN 3.4), from the run
 *  state alone. */
export function stageStatus(state: RunState): StageStatus {
	switch (state) {
		case 'running':
			return 'running';
		case 'provisioning':
		case 'pending':
			return 'pending';
		case 'waiting_on_human':
			return 'waiting';
		case 'failed':
			return 'failed';
		case 'completed':
			return 'done';
		default:
			// paused, in review, abandoned, archived: parked, not progressing.
			return 'paused';
	}
}
