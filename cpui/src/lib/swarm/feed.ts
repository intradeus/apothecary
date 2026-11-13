// The kill feed: what changed between two snapshots of the swarm, as the
// ticker's lines. Pure — the poller keeps the previous snapshot and the
// list.

import type { RunState, Swarm, SwarmUnit } from '$lib/domain';
import { isTerminal } from '$lib/core/format';

export type FeedKind =
	| 'deployed' // a unit appeared
	| 'stage' // it cleared a stage (detail: the stage it left)
	| 'needs_you' // it is waiting on a human
	| 'in_review'
	| 'completed'
	| 'failed'
	| 'abandoned'
	| 'drone'; // its agent launched a subagent (detail: the spawn's title)

export interface FeedEntry {
	/** Unique within a feed. */
	id: string;
	at: string;
	kind: FeedKind;
	runId: string;
	projectId: string;
	title: string;
	detail?: string;
}

/** The line a run's arrival in a state earns; none for the others. */
const ARRIVAL: Partial<Record<RunState, FeedKind>> = {
	waiting_on_human: 'needs_you',
	in_review: 'in_review',
	completed: 'completed',
	failed: 'failed',
	abandoned: 'abandoned'
};

/** The feed lines between prev and next, in next's unit order. The first
 *  snapshot (prev null) is the board as found, not news: no lines. A unit
 *  that leaves says nothing — how it ended was its line when it ended. */
export function diffSwarm(prev: Swarm | null, next: Swarm): FeedEntry[] {
	if (!prev) return [];
	const before = new Map(prev.units.map((u) => [u.runId, u]));
	const out: FeedEntry[] = [];
	const line = (u: SwarmUnit, kind: FeedKind, detail?: string) =>
		out.push({
			id: `${next.at}|${u.runId}|${kind}|${detail ?? ''}`,
			at: next.at,
			kind,
			runId: u.runId,
			projectId: u.projectId,
			title: u.title,
			detail
		});
	for (const u of next.units) {
		const was = before.get(u.runId);
		if (!was) {
			// Seen for the first time already over: only how it ended.
			const kind = isTerminal(u.state) ? ARRIVAL[u.state] : 'deployed';
			if (kind) line(u, kind);
			continue;
		}
		if (was.currentStage && u.currentStage !== was.currentStage) line(u, 'stage', was.currentStage);
		const arrived = ARRIVAL[u.state];
		if (u.state !== was.state && arrived) line(u, arrived);
		const flying = new Set(was.crew?.subagents.map((s) => s.toolCallId));
		for (const s of u.crew?.subagents ?? []) {
			if (!flying.has(s.toolCallId)) line(u, 'drone', s.title);
		}
	}
	return out;
}
