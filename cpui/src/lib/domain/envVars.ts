// A workshop's workspace environment: variables every process in its
// workspaces gets, over the repository's own and the engine's size hints.
// Plain configuration, shown back — never a secret. The engine refuses what
// these checks refuse (confstore validateEnv); they say so before a save.

/** One row of the editor: a name and its value, as typed. */
export interface EnvVarRow {
	name: string;
	value: string;
}

/** The engine's size hints, which a value may name as ${APO_CPUS}. */
export const ENV_HINTS = ['APO_CPUS', 'APO_MEMORY_MIB'] as const;

export const MAX_ENV_VARS = 64;
export const MAX_ENV_VALUE_BYTES = 4096;

const NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;
const RESERVED_PREFIXES = ['APOTHECARY_', 'APO_'];
/** What the engine sets in every workspace itself (provider keys aside,
 *  which the engine names when it refuses one). */
const RESERVED_NAMES = new Set([
	'PATH',
	'HOME',
	'TERM',
	'COLORTERM',
	'DEFAULT_AUTH_REQUEST',
	'OMP_AUTH_BROKER_URL',
	'OMP_AUTH_BROKER_TOKEN'
]);

/** Why a row would be refused. */
export type EnvVarIssue =
	| { kind: 'name' }
	| { kind: 'reservedPrefix'; prefix: string }
	| { kind: 'reservedName' }
	| { kind: 'duplicate' }
	| { kind: 'tooLong' };

/** The rows a map stores, sorted by name. */
export function envRows(env: Record<string, string> | undefined): EnvVarRow[] {
	return Object.keys(env ?? {})
		.sort()
		.map((name) => ({ name, value: env![name] }));
}

/** The map the rows save as: names trimmed, rows with no name dropped. */
export function envFromRows(rows: EnvVarRow[]): Record<string, string> {
	const out: Record<string, string> = {};
	for (const r of rows) {
		const name = r.name.trim();
		if (name) out[name] = r.value;
	}
	return out;
}

/** Each row's issue (undefined when it is fine), in row order. A row with
 *  neither name nor value is an unfinished one and never an issue. */
export function envIssues(rows: EnvVarRow[]): (EnvVarIssue | undefined)[] {
	const seen = new Set<string>();
	return rows.map((r) => {
		const name = r.name.trim();
		if (!name && !r.value) return undefined;
		if (!NAME.test(name)) return { kind: 'name' };
		const prefix = RESERVED_PREFIXES.find((p) => name.toUpperCase().startsWith(p));
		if (prefix) return { kind: 'reservedPrefix', prefix };
		if (RESERVED_NAMES.has(name)) return { kind: 'reservedName' };
		if (seen.has(name)) return { kind: 'duplicate' };
		seen.add(name);
		if (new TextEncoder().encode(r.value).length > MAX_ENV_VALUE_BYTES) return { kind: 'tooLong' };
		return undefined;
	});
}

/** More rows than the engine keeps. */
export function tooManyEnvVars(rows: EnvVarRow[]): boolean {
	return Object.keys(envFromRows(rows)).length > MAX_ENV_VARS;
}
