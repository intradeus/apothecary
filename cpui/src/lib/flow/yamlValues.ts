// Completion of a key's value in the flow YAML from its schema's enums: a
// stage's type, a signal, an action, an action's enum input, a start policy,
// an outcome's then. codemirror-json-schema completes keys, and values only
// once one is typed and only on the schema's first branch; this source finds
// the key before the cursor, resolves its schema — through $ref, anyOf and
// an action's if/then — and offers every value it allows, prefix or not.
import type { CompletionContext, CompletionResult } from '@codemirror/autocomplete';
import { getJSONSchema, jsonPointerForPosition } from 'codemirror-json-schema';
import { parseYAMLDocumentState } from 'codemirror-json-schema/yaml';

type Schema = Record<string, unknown> & {
	$ref?: string;
	properties?: Record<string, Schema>;
	additionalProperties?: Schema | boolean;
	items?: Schema;
	anyOf?: Schema[];
	oneOf?: Schema[];
	allOf?: Schema[];
	if?: Schema;
	then?: Schema;
	enum?: unknown[];
	const?: unknown;
	required?: string[];
};

const deref = (root: Schema, s: Schema | undefined): Schema | undefined => {
	for (let i = 0; s?.$ref && i < 16; i++) {
		let at: unknown = root;
		for (const seg of s.$ref.replace(/^#\//, '').split('/'))
			at = (at as Record<string, unknown>)?.[seg];
		s = at as Schema | undefined;
	}
	return s;
};

// A schema's `if` as the flow's write it: required keys holding consts.
const holds = (cond: Schema, value: unknown): boolean => {
	if (!value || typeof value !== 'object') return false;
	const v = value as Record<string, unknown>;
	if (cond.required?.some((k) => !(k in v))) return false;
	return Object.entries(cond.properties ?? {}).every(
		([k, p]) => p.const === undefined || v[k] === p.const
	);
};

// Every shape a schema may take for this value.
function variants(root: Schema, s: Schema | undefined, value: unknown, depth = 0): Schema[] {
	s = deref(root, s);
	if (!s || typeof s !== 'object' || depth > 8) return [];
	const out = [s];
	for (const b of [...(s.anyOf ?? []), ...(s.oneOf ?? [])])
		out.push(...variants(root, b, value, depth + 1));
	for (const b of s.allOf ?? []) {
		if (b.if) {
			if (b.then && holds(b.if, value)) out.push(...variants(root, b.then, value, depth + 1));
		} else out.push(...variants(root, b, value, depth + 1));
	}
	return out;
}

// The schemas for the value at a JSON pointer.
function schemasAt(root: Schema, path: string[], data: unknown): Schema[] {
	let cur: Schema[] = [root];
	let value = data;
	for (const seg of path) {
		const next: Schema[] = [];
		for (const s of cur.flatMap((c) => variants(root, c, value))) {
			if (s.properties?.[seg]) next.push(s.properties[seg]);
			else if (s.items && /^\d+$/.test(seg)) next.push(s.items);
			else if (typeof s.additionalProperties === 'object' && !s.properties?.[seg])
				next.push(s.additionalProperties);
		}
		cur = next;
		value = (value as Record<string, unknown> | undefined)?.[seg];
	}
	return cur.flatMap((c) => variants(root, c, value));
}

// The key a value is being typed for: `key: va|`, in a block or a flow
// mapping.
const KEY = /(?:^|[\s{,-])([A-Za-z_][\w-]*):\s*([\w$.-]*)$/;

export function yamlValues(ctx: CompletionContext): CompletionResult | null {
	const schema = getJSONSchema(ctx.state) as Schema | undefined;
	if (!schema) return null;
	const line = ctx.state.doc.lineAt(ctx.pos);
	const m = KEY.exec(line.text.slice(0, ctx.pos - line.from));
	if (!m) return null;
	const typed = m[2];
	if (!typed && !ctx.explicit && !/:\s$/.test(line.text.slice(0, ctx.pos - line.from))) return null;
	const keyAt = line.from + (m.index ?? 0) + m[0].indexOf(m[1]);
	let pointer = jsonPointerForPosition(ctx.state, keyAt + 1, 1, 'yaml');
	if (!pointer.endsWith('/' + m[1])) pointer = `${pointer}/${m[1]}`;
	const path = pointer.split('/').filter(Boolean);
	let data: unknown;
	try {
		data = parseYAMLDocumentState(ctx.state).data;
	} catch {
		data = undefined;
	}
	const values = new Set<string>();
	for (const s of schemasAt(schema, path, data)) {
		for (const v of s.enum ?? [])
			if (typeof v === 'string' || typeof v === 'number') values.add(String(v));
		if (typeof s.const === 'string' || typeof s.const === 'number') values.add(String(s.const));
		for (const v of (s.examples as unknown[] | undefined) ?? [])
			if (typeof v === 'string') values.add(v);
	}
	if (!values.size) return null;
	return {
		from: ctx.pos - typed.length,
		options: [...values].map((label) => ({ label, type: 'enum' })),
		validFor: /^[\w$.-]*$/
	};
}
