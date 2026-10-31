// What the editor has selected, and what keeps a remembered selection honest:
// whether it still names something, and what a rename makes of it.
import type { FlowDoc } from '$lib/domain/flowdoc';

/** What the editor has selected. A stage or an edge inside a sequence
 *  names the sequence; one in the chains does not. */
export type Selection =
	| { kind: 'stage'; id: string; sequence?: string }
	| { kind: 'edge'; stage: string; edge: string; sequence?: string }
	| { kind: 'phase'; id: string }
	| { kind: 'agent'; name: string }
	| { kind: 'sequence'; id: string }
	| { kind: 'schema'; name: string }
	| { kind: 'mcp'; name: string }
	| null;

/** Whether a selection still names something (a delete leaves the trail
 *  pointing at nothing). */
export function selectionExists(doc: FlowDoc | null, sel: Selection): boolean {
	if (!sel || !doc) return true;
	const inScope = (seq?: string) =>
		seq ? doc.sequences?.find((q) => q.id === seq)?.stages : doc.stages;
	switch (sel.kind) {
		case 'stage':
			return !!inScope(sel.sequence)?.some((s) => s.id === sel.id);
		case 'edge':
			return !!inScope(sel.sequence)?.some((s) => s.id === sel.stage);
		case 'phase':
			return !!doc.phases?.some((p) => p.id === sel.id);
		case 'agent':
			return !!doc.agents?.some((a) => a.name === sel.name);
		case 'sequence':
			return !!doc.sequences?.some((q) => q.id === sel.id);
		case 'schema':
			return !!doc.schemas?.some((q) => q.name === sel.name);
		case 'mcp':
			return !!doc.mcp_servers?.some((m) => m.name === sel.name);
	}
}

/** A rename renames what the trail remembers too: the panel renamed, and
 *  a sequence's stages under its new name. */
export function renameInTrail(trail: Selection[], from: Selection, to: Selection): Selection[] {
	if (!from || !to || from.kind !== to.kind) return trail;
	return trail.map((t): Selection => {
		if (!t) return t;
		if (from.kind === 'sequence' && to.kind === 'sequence') {
			if (t.kind === 'sequence' && t.id === from.id) return { ...t, id: to.id };
			if ((t.kind === 'stage' || t.kind === 'edge') && t.sequence === from.id)
				return { ...t, sequence: to.id };
		}
		if (from.kind === 'agent' && to.kind === 'agent' && t.kind === 'agent' && t.name === from.name)
			return { ...t, name: to.name };
		if (from.kind === 'phase' && to.kind === 'phase' && t.kind === 'phase' && t.id === from.id)
			return { ...t, id: to.id };
		if (
			from.kind === 'schema' &&
			to.kind === 'schema' &&
			t.kind === 'schema' &&
			t.name === from.name
		)
			return { ...t, name: to.name };
		if (from.kind === 'mcp' && to.kind === 'mcp' && t.kind === 'mcp' && t.name === from.name)
			return { ...t, name: to.name };
		if (from.kind === 'stage' && to.kind === 'stage') {
			if (t.kind === 'stage' && t.sequence === from.sequence && t.id === from.id)
				return { ...t, id: to.id };
			if (t.kind === 'edge' && t.sequence === from.sequence && t.stage === from.id)
				return { ...t, stage: to.id };
		}
		return t;
	});
}
