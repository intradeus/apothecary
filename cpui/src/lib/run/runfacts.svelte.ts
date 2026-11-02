// What the lists show about a run beyond its row: what it has cost, which
// attempt its current stage is on, and what TYPE that stage is. Spend and
// attempts come with the run itself (its folded facts); the stage type is
// read lazily from the run's pinned flow document, cached per (workshop,
// flow hash), which never changes.
//
// Read-only GETs. A failed fetch degrades to "unknown" — the cells that read
// these show nothing rather than a wrong number.

import { SvelteMap } from 'svelte/reactivity';
import { parse } from 'yaml';
import { api } from '$lib/api';
import { expandDoc, normalizeDoc, type StageKind } from '$lib/domain/flowdoc';
import type { Run, RunState } from '$lib/domain';
import type { StageStatus } from '$lib/ui/StageFlask.svelte';

export interface RunGlance {
	/** USD spent so far, failed attempts included. */
	cost: number;
	turns: number;
	/** The current stage's attempt number while that attempt is open. */
	attempt?: number;
	/** When the current stage's open attempt started. */
	stageStartedAt?: string;
}

/** The run's spend and its current stage's attempt, from the facts the list
 *  carries. */
export function runFacts(run: Run): RunGlance {
	const open = run.currentStage ? run.facts.open[run.currentStage] : undefined;
	return {
		cost: run.facts.costUsd,
		turns: run.facts.turns,
		attempt: open?.iteration,
		stageStartedAt: open?.startedAt
	};
}

// --- stage types, from the run's pinned flow version -------------------------

const docs = new SvelteMap<string, Record<string, StageKind>>();
const docsInflight = new Set<string>();

async function loadDoc(key: string, projectId: string, hash: string) {
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
		docsInflight.add(key);
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
