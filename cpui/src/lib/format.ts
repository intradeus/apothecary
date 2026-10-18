// Presentation helpers: how values are shown, not what they are.
//
// These used to live in the API client, which meant every component that
// wanted to format a timestamp imported the transport layer to get it.

import type { RunState } from '$lib/domain';

/** Tab grouping for the runs list. Exhaustive over RunState, so adding a
 *  state without classifying it is a type error. Active = still moving or
 *  waiting on someone; finished = terminal (completed and abandoned runs keep
 *  their workspace until archived, but the run itself is done). */
export type RunTab = 'active' | 'finished';

export const stateTab: Record<RunState, RunTab> = {
	pending: 'active',
	provisioning: 'active',
	running: 'active',
	waiting_on_human: 'active',
	in_review: 'active',
	paused: 'active',
	completed: 'finished',
	abandoned: 'finished',
	failed: 'finished',
	archived: 'finished'
};

export const stateColors: Record<RunState, string> = {
	pending: 'var(--muted)',
	provisioning: 'var(--blue)',
	running: 'var(--blue)',
	waiting_on_human: 'var(--amber)',
	in_review: 'var(--accent)',
	paused: 'var(--muted)',
	completed: 'var(--green)',
	abandoned: 'var(--amber)',
	failed: 'var(--red)',
	archived: 'var(--muted)'
};

export function fmtTime(ts: string): string {
	return new Date(ts).toLocaleTimeString(undefined, { hour12: false });
}

export function fmtBytes(n?: number): string {
	if (!n && n !== 0) return '';
	if (n < 1024) return `${n} B`;
	if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} kB`;
	return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

export function fmtCost(v: unknown): string {
	return typeof v === 'number' ? `$${v.toFixed(3)}` : '';
}

export function ago(ts: string): string {
	const s = Math.max(0, (Date.now() - new Date(ts).getTime()) / 1000);
	if (s < 60) return `${Math.floor(s)}s ago`;
	if (s < 3600) return `${Math.floor(s / 60)}m ago`;
	if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
	return `${Math.floor(s / 86400)}d ago`;
}
