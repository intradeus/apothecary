// Parameters in the editor (FLOWLANG §1): a parameter's declared type (the
// engine's own for its six), which control edits it, which parameters a site
// may read, and where a parameter is read.
import {
	allStages,
	KNOWN_PARAMS,
	serviceSource,
	START_POLICIES,
	type DocParam,
	type FlowDoc
} from '$lib/flowdoc';

// The stage fields a parameter may stand for, whole.
const PARAM_FIELDS = ['model', 'harness', 'run_ref', 'timeout', 'max_turns', 'service'] as const;

export type ParamType = 'bool' | 'int' | 'float' | 'string' | 'duration';
export type ParamKind = ParamType | 'enum' | 'policy' | 'list';

/** The types a parameter may declare, in display order (mirror of Go). */
export const PARAM_TYPES: ParamType[] = ['bool', 'int', 'float', 'string', 'duration'];

/** The engine's six and their types: theirs, not the document's. */
export const KNOWN_TYPES: Record<string, ParamType> = {
	harness: 'string',
	model: 'string',
	timeout: 'duration',
	max_turns: 'int',
	max_cost_usd: 'float',
	max_total_turns: 'int'
};

/** "$name" — a reference to a parameter (sentinels are $UPPER). */
export function isParamRef(v: unknown): v is string {
	return typeof v === 'string' && /^\$[a-z][a-z0-9_]*$/.test(v);
}

/** The parameter's type: the engine's for its six, else as declared. A
 *  document from before types were declared reads as its default looks —
 *  the server refuses it, and the editor's Type control fixes it. */
export function paramType(name: string, p: DocParam): ParamType {
	if (KNOWN_TYPES[name]) return KNOWN_TYPES[name];
	if (p.type) return p.type;
	const d = Array.isArray(p.default) ? p.default[0] : p.default;
	if (typeof d === 'boolean') return 'bool';
	if (typeof d === 'number') return Number.isInteger(d) ? 'int' : 'float';
	return 'string';
}

/** What control edits it: a list, an enum within the start policies (a
 *  policy), another enum, else its type. */
export function paramKind(name: string, p: DocParam): ParamKind {
	if (p.list) return 'list';
	const t = paramType(name, p);
	if (t !== 'string') return t;
	if (p.enum?.length)
		return p.enum.every((v) => (START_POLICIES as readonly string[]).includes(v))
			? 'policy'
			: 'enum';
	return 'string';
}

/** Parse one value a person typed into what the YAML carries for its type:
 *  true/false and numbers bare, everything else text. */
export function parseValue(text: string, type: ParamType): boolean | number | string {
	const v = text.trim();
	if (type === 'bool' && (v === 'true' || v === 'false')) return v === 'true';
	if ((type === 'int' || type === 'float') && /^-?\d+(\.\d+)?$/.test(v)) return Number(v);
	return type === 'string' ? text : v;
}

/** A default as it is shown in a field: a list's items joined. */
export function defaultText(p: DocParam): string {
	return Array.isArray(p.default) ? p.default.join(', ') : String(p.default);
}

/** Parse a default for a parameter: a list's from comma-separated items. */
export function parseDefaultFor(text: string, type: ParamType, list = false): DocParam['default'] {
	if (!list) return parseValue(text, type);
	return text
		.split(',')
		.map((v) => v.trim())
		.filter(Boolean)
		.map((v) => parseValue(v, type) as boolean | number | string);
}

export const isKnown = (name: string) => (KNOWN_PARAMS as readonly string[]).includes(name);

/** The flow's own parameters, in document order. */
export function customParams(doc: FlowDoc): [string, DocParam][] {
	return Object.entries(doc.parameters ?? {}).filter(([n]) => !isKnown(n));
}

const ofType = (doc: FlowDoc, t: ParamType, list = false) =>
	Object.entries(doc.parameters ?? {})
		.filter(([n, p]) => paramType(n, p) === t && !!p.list === list)
		.map(([n]) => n);

export const boolParams = (doc: FlowDoc) => ofType(doc, 'bool');
export const intParams = (doc: FlowDoc) => ofType(doc, 'int');
export const stringParams = (doc: FlowDoc) => ofType(doc, 'string');
export const durationParams = (doc: FlowDoc) => ofType(doc, 'duration');
/** List parameters of strings: what a list setting (labels) may read. */
export const stringListParams = (doc: FlowDoc) => ofType(doc, 'string', true);

/** Enum parameters whose values all fit the site's. */
export function enumParamsWithin(doc: FlowDoc, values: readonly string[]): string[] {
	return Object.entries(doc.parameters ?? {})
		.filter(([, p]) => !p.list && p.enum?.length && p.enum.every((v) => values.includes(v)))
		.map(([n]) => n);
}
export const policyParams = (doc: FlowDoc) => enumParamsWithin(doc, START_POLICIES);

/** Where a parameter is read: "stage.field", "stage.setting", "stage.input"
 *  (a run.service.$name source), "phase.start". */
export function paramRefs(doc: FlowDoc, name: string): string[] {
	const ref = '$' + name;
	const out: string[] = [];
	for (const s of allStages(doc)) {
		for (const f of PARAM_FIELDS) {
			if (s[f] === ref) out.push(`${s.id}.${f}`);
		}
		if (s.run_if && s.run_if.replace(/^not\s+/, '') === ref) out.push(`${s.id}.run_if`);
		for (const [k, v] of Object.entries(s.with ?? {})) if (v === ref) out.push(`${s.id}.${k}`);
		for (const [k, p] of Object.entries(s.input ?? {}))
			if (p.from === serviceSource(ref)) out.push(`${s.id}.${k}`);
	}
	for (const ph of doc.phases ?? []) {
		if (ph.on.start === ref) out.push(`${ph.id}.start`);
		if (ph.on.max === ref) out.push(`${ph.id}.max`);
		if (ph.on.quiet_for === ref) out.push(`${ph.id}.quiet_for`);
	}
	return out;
}

/** Rename a parameter in place, keeping document order, and every reference. */
export function renameParam(doc: FlowDoc, from: string, to: string) {
	doc.parameters = Object.fromEntries(
		Object.entries(doc.parameters).map(([k, v]) => [k === from ? to : k, v])
	);
	const a = '$' + from;
	const b = '$' + to;
	for (const s of allStages(doc)) {
		for (const f of PARAM_FIELDS) if (s[f] === a) s[f] = b;
		if (s.run_if) s.run_if = s.run_if.replace(a, b);
		for (const k of Object.keys(s.with ?? {})) if (s.with![k] === a) s.with![k] = b;
		for (const p of Object.values(s.input ?? {}))
			if (p.from === serviceSource(a)) p.from = serviceSource(b);
	}
	for (const ph of doc.phases ?? []) {
		if (ph.on.start === a) ph.on.start = b;
		if (ph.on.max === a) ph.on.max = b;
		if (ph.on.quiet_for === a) ph.on.quiet_for = b;
	}
}
