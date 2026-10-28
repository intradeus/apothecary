// What a flow's parameters resolve to as a form stands, and which harness a
// model parameter's suggestions come from. Shared by the workshop's Configure
// sheet and the start-run form: both edit layers of values over the flow's
// declarations, and both want a model field to list what the right harness
// offers — the harness of the stages reading that parameter (FLOWLANG §1:
// stages sharing a model parameter share a harness), as the form currently
// sets it.
import type { ParamDecl } from '$lib/domain';

/** The value a parameter has: the first layer that sets it, else its
 *  default — following a default that reads another parameter — or
 *  undefined when nothing gives it one (unbound). */
export function paramValue(
	name: string,
	all: ParamDecl[],
	layers: (Record<string, string> | undefined)[],
	depth = 0
): string | undefined {
	for (const layer of layers) {
		const v = layer?.[name];
		if (v !== undefined && v !== '') return v;
	}
	const p = all.find((d) => d.name === name);
	if (!p || depth > all.length) return undefined;
	if (p.defaultRef) return paramValue(p.defaultRef, all, layers, depth + 1);
	if (p.required) return undefined;
	return p.default;
}

/** The harness kind a model parameter's stages run on, as the form stands:
 *  the kind written at the stages, or the value of the harness parameter they
 *  read. '' when the parameter is not a model's, or that harness is itself
 *  unbound. */
export function harnessKindOf(
	p: ParamDecl,
	all: ParamDecl[],
	layers: (Record<string, string> | undefined)[]
): string {
	const h = p.harness ?? '';
	if (!h) return '';
	if (!h.startsWith('$')) return h;
	return paramValue(h.slice(1), all, layers) ?? '';
}

/** The harness kinds whose models the form should have at hand: one per
 *  model parameter, distinct, in declaration order. */
export function modelHarnessKinds(
	all: ParamDecl[],
	layers: (Record<string, string> | undefined)[]
): string[] {
	const out: string[] = [];
	for (const p of all) {
		if (p.role !== 'model') continue;
		const k = harnessKindOf(p, all, layers);
		if (k && !out.includes(k)) out.push(k);
	}
	return out;
}
