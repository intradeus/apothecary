// The terminal session each run's Terminal pane last showed, so coming back
// to the run (another tab, a refresh, the next day) shows the same shell.
//
// As in openFiles.svelte.ts: one localStorage key for every run, pruned to
// the most recent runs on each write. It is a convenience, not a record:
// every read and write tolerates blocked or corrupt storage, and an id the
// workspace no longer lists is simply not picked.

export const STORAGE_KEY = 'apothecary.terminals.v1';
/** Runs whose last session is kept; older ones are dropped on each write. */
export const KEPT_RUNS = 20;

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

function readAll(): Obj {
	try {
		const all: unknown = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '{}');
		return isObj(all) ? all : {};
	} catch {
		return {};
	}
}

function writeAll(all: Obj) {
	try {
		localStorage.setItem(STORAGE_KEY, JSON.stringify(all));
	} catch {
		/* storage blocked: the choice lasts this page only */
	}
}

const savedAt = (v: unknown) => (isObj(v) && typeof v.at === 'number' ? v.at : 0);

/** The session last shown for the run; '' when none was kept. */
export function rememberedSession(runId: string): string {
	const entry = readAll()[runId];
	return isObj(entry) && typeof entry.id === 'string' ? entry.id : '';
}

/** Keep `id` as the run's shown session. */
export function rememberSession(runId: string, id: string) {
	const all = readAll();
	all[runId] = { at: Date.now(), id };
	const kept = Object.entries(all)
		.filter(([, v]) => isObj(v))
		.sort(([, a], [, b]) => savedAt(b) - savedAt(a))
		.slice(0, KEPT_RUNS);
	writeAll(Object.fromEntries(kept));
}
