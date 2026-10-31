// Presentation helpers: how values are shown, not what they are.
//
// These used to live in the API client, which meant every component that
// wanted to format a timestamp imported the transport layer to get it.

import type { RunState } from '$lib/domain';
import { i18n, t, type MessageKey } from '$lib/i18n/index.svelte';

/** The word for an integration port (or a spec's `requires`) in UI text. */
export const portLabel = (port: string) => (port in PORT_WORDS ? t(PORT_WORDS[port]) : port);
const PORT_WORDS: Record<string, MessageKey> = {
	repository: 'port.repository',
	provisioner: 'port.provisioner',
	harness: 'port.harness',
	ticketsource: 'port.ticketsource',
	tracker: 'port.tracker'
};

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

export const isTerminal = (s: RunState) => stateTab[s] === 'finished';

/** The hue token of each state (DESIGN 3.1), for dots and accents. */
export const stateHue: Record<RunState, string> = {
	pending: 'var(--mut)',
	provisioning: 'var(--emb)',
	running: 'var(--emb)',
	waiting_on_human: 'var(--sky)',
	in_review: 'var(--vio)',
	paused: 'var(--mut)',
	completed: 'var(--grn)',
	abandoned: 'var(--gold)',
	failed: 'var(--ros)',
	archived: 'var(--fnt)'
};
/** The same, as a text-colour class (`.hue-*`; `.fnt` for archived). */
export const stateHueClass: Record<RunState, string> = {
	pending: 'hue-mut',
	provisioning: 'hue-emb',
	running: 'hue-emb',
	waiting_on_human: 'hue-sky',
	in_review: 'hue-vio',
	paused: 'hue-mut',
	completed: 'hue-grn',
	abandoned: 'hue-gold',
	failed: 'hue-ros',
	archived: 'fnt'
};
/** @deprecated use stateHue. */
export const stateColors = stateHue;

/** The words a state chip shows. */
export const stateLabel: Record<RunState, string> = {
	get pending() {
		return t('state.pending');
	},
	get provisioning() {
		return t('state.provisioning');
	},
	get running() {
		return t('state.running');
	},
	get waiting_on_human() {
		return t('state.waiting_on_human');
	},
	get in_review() {
		return t('state.in_review');
	},
	get paused() {
		return t('state.paused');
	},
	get completed() {
		return t('state.completed');
	},
	get abandoned() {
		return t('state.abandoned');
	},
	get failed() {
		return t('state.failed');
	},
	get archived() {
		return t('state.archived');
	}
};

/** A run's short id: "r_" + the ULID's last four (random) characters. */
export const shortId = (id: string, prefix = 'r_') => `${prefix}${id.slice(-4).toLowerCase()}`;

/** 24-hour clock, seconds included. */
export function fmtTime(ts: string | number | Date): string {
	return new Date(ts).toLocaleTimeString(i18n.tag, { hour12: false });
}

/** 24-hour clock, minutes only ("13:21"). */
export function fmtClock(ts: string | number | Date): string {
	return new Date(ts).toLocaleTimeString(i18n.tag, {
		hour12: false,
		hour: '2-digit',
		minute: '2-digit'
	});
}

/** Full absolute timestamp for tooltips behind relative times. */
export function fmtAbs(ts: string | number | Date): string {
	const d = new Date(ts);
	return `${d.toLocaleDateString(i18n.tag, { year: 'numeric', month: 'short', day: 'numeric' })} ${fmtTime(d)}`;
}

export function fmtBytes(n?: number): string {
	if (!n && n !== 0) return '';
	if (n < 1024) return `${n} B`;
	if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
	if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
	return `${(n / (1024 * 1024 * 1024)).toFixed(2)} GB`;
}

/** Cost as $0.000 (three decimals). Non-numbers render empty. */
export function fmtCost(v: unknown): string {
	return typeof v === 'number' ? `$${v.toFixed(3)}` : '';
}

/** "4:12", "1:02:09" — a live stage clock. */
export function fmtClockDuration(ms: number): string {
	const s = Math.max(0, Math.floor(ms / 1000));
	const h = Math.floor(s / 3600);
	const m = Math.floor((s % 3600) / 60);
	const ss = String(s % 60).padStart(2, '0');
	return h ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`;
}

/** "0:04", "1:12", "26:05" — an elapsed span as hours:minutes. */
export function fmtHoursMinutes(ms: number): string {
	const m = Math.max(0, Math.floor(ms / 60000));
	return `${Math.floor(m / 60)}:${String(m % 60).padStart(2, '0')}`;
}

/** "14m 08s", "2h 05m", "41s" — an elapsed span. */
export function fmtDuration(ms: number): string {
	const s = Math.max(0, Math.floor(ms / 1000));
	if (s < 60) return `${s}s`;
	if (s < 3600) return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`;
	if (s < 86400)
		return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}m`;
	return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

/** Relative time: "just now", "3m ago", "2h ago", "4d ago". Pass `now` from a
 *  ticking clock to keep it live. */
export function ago(ts: string | number | Date, now = Date.now()): string {
	const s = Math.max(0, (now - new Date(ts).getTime()) / 1000);
	if (s < 10) return t('fmt.justNow');
	if (s < 60) return t('fmt.agoS', { n: Math.floor(s) });
	if (s < 3600) return t('fmt.agoM', { n: Math.floor(s / 60) });
	if (s < 86400) return t('fmt.agoH', { n: Math.floor(s / 3600) });
	return t('fmt.agoD', { n: Math.floor(s / 86400) });
}

/** Age without the "ago" ("38m", "2h") for dense columns. */
export function age(ts: string | number | Date): string {
	const s = Math.max(0, (Date.now() - new Date(ts).getTime()) / 1000);
	if (s < 10) return t('fmt.now');
	if (s < 60) return t('fmt.ageS', { n: Math.floor(s) });
	if (s < 3600) return t('fmt.ageM', { n: Math.floor(s / 60) });
	if (s < 86400) return t('fmt.ageH', { n: Math.floor(s / 3600) });
	return t('fmt.ageD', { n: Math.floor(s / 86400) });
}

/** "×3" for repeat counts in alerts. */
export const times = (n: number) => `×${n}`;

export const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** A byte rate ("12 KB/s"); fractions of a byte round away. */
export const fmtRate = (bytesPerSec: number): string =>
	`${fmtBytes(Math.round(bytesPerSec)) || '0 B'}/s`;

/** A percentage with one decimal under 10 ("4.2%", "37%"). */
export const fmtPct = (v: number): string => `${v >= 10 ? Math.round(v) : v.toFixed(1)}%`;

/** Per-second rates from a cumulative counter sampled at `times` (ms). The
 *  first point has no rate (null); a negative delta is a counter reset
 *  (container restart) and reads 0; a non-increasing time is a gap (null). */
export function counterRates(cum: number[], times: number[]): (number | null)[] {
	return cum.map((v, i) => {
		if (i === 0) return null;
		const dt = (times[i] - times[i - 1]) / 1000;
		if (!(dt > 0)) return null;
		return Math.max(0, (v - cum[i - 1]) / dt);
	});
}
