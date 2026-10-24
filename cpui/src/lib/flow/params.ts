// Parameters in the editor (FLOWLANG §1): a parameter's type as the server
// infers it from its default, which control edits it, which parameters a
// site may read, and where a parameter is read.
import { allStages, KNOWN_PARAMS, START_POLICIES, type DocParam, type FlowDoc } from '$lib/flowdoc';

export type ParamType = 'bool' | 'int' | 'float' | 'string';
export type ParamKind = ParamType | 'enum' | 'policy' | 'duration';

/** "$name" — a reference to a parameter (sentinels are $UPPER). */
export function isParamRef(v: unknown): v is string {
	return typeof v === 'string' && /^\$[a-z][a-z0-9_]*$/.test(v);
}

/** The parameter's type, as the server infers it from the default. */
export function paramType(p: DocParam): ParamType {
	if (typeof p.default === 'boolean') return 'bool';
	if (typeof p.default === 'number') return Number.isInteger(p.default) ? 'int' : 'float';
	return 'string';
}

/** What control edits it: an enum within the start policies is a policy, a
 *  string that reads as a duration a duration, the rest by type. */
export function paramKind(name: string, p: DocParam): ParamKind {
	const t = paramType(p);
	if (t !== 'string') return t;
	if (p.enum?.length)
		return p.enum.every((v) => (START_POLICIES as readonly string[]).includes(v))
			? 'policy'
			: 'enum';
	if (name === 'timeout' || /^\d+(ms|s|m|h)$/.test(String(p.default))) return 'duration';
	return 'string';
}

/** Parse what a person typed as a default into the typed value the YAML
 *  carries: true/false, a number, else text. */
export function parseDefault(text: string): boolean | number | string {
	const v = text.trim();
	if (v === 'true' || v === 'false') return v === 'true';
	if (/^-?\d+(\.\d+)?$/.test(v)) return Number(v);
	return text;
}

export const isKnown = (name: string) => (KNOWN_PARAMS as readonly string[]).includes(name);

/** The flow's own parameters, in document order. */
export function customParams(doc: FlowDoc): [string, DocParam][] {
	return Object.entries(doc.parameters ?? {}).filter(([n]) => !isKnown(n));
}

const ofType = (doc: FlowDoc, t: ParamType) =>
	Object.entries(doc.parameters ?? {})
		.filter(([, p]) => paramType(p) === t)
		.map(([n]) => n);

export const boolParams = (doc: FlowDoc) => ofType(doc, 'bool');
export const intParams = (doc: FlowDoc) => ofType(doc, 'int');
export const stringParams = (doc: FlowDoc) => ofType(doc, 'string');

/** Enum parameters whose values all fit the site's. */
export function enumParamsWithin(doc: FlowDoc, values: readonly string[]): string[] {
	return Object.entries(doc.parameters ?? {})
		.filter(([, p]) => p.enum?.length && p.enum.every((v) => values.includes(v)))
		.map(([n]) => n);
}
export const policyParams = (doc: FlowDoc) => enumParamsWithin(doc, START_POLICIES);

/** Where a parameter is read: "stage.field", "stage.input", "phase.start". */
export function paramRefs(doc: FlowDoc, name: string): string[] {
	const ref = '$' + name;
	const out: string[] = [];
	for (const s of allStages(doc)) {
		for (const f of ['model', 'harness', 'run_ref', 'timeout', 'max_turns'] as const) {
			if (s[f] === ref) out.push(`${s.id}.${f}`);
		}
		if (s.when && s.when.replace(/^not\s+/, '') === ref) out.push(`${s.id}.when`);
		for (const [k, v] of Object.entries((s.input ?? {}) as Record<string, string>))
			if (v === ref) out.push(`${s.id}.${k}`);
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
		for (const f of ['model', 'harness', 'run_ref', 'timeout', 'max_turns'] as const)
			if (s[f] === a) s[f] = b;
		if (s.when) s.when = s.when.replace(a, b);
		if (s.input)
			for (const k of Object.keys(s.input))
				if ((s.input as Record<string, string>)[k] === a)
					(s.input as Record<string, string>)[k] = b;
	}
	for (const ph of doc.phases ?? []) {
		if (ph.on.start === a) ph.on.start = b;
		if (ph.on.max === a) ph.on.max = b;
		if (ph.on.quiet_for === a) ph.on.quiet_for = b;
	}
}
