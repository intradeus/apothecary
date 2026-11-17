// Which of a flow's parameters name a tracker status: those an action
// stage reads at a setting that may wait for a binding (ticket_transition's
// to). The workshop binds them as its tracker writes its statuses, so the
// Configure sheet suggests the names of the workshop's ticket source.
import { parse } from 'yaml';
import type { ActionSpec } from '$lib/domain';
import { allStages, normalizeDoc } from '$lib/domain/flowdoc';
import { isParamRef } from '$lib/flow/params';

/** The parameters read as a tracker status, in document order; none when
 *  the document does not parse. */
export function statusParams(yaml: string, actions: ActionSpec[]): string[] {
	let stages;
	try {
		stages = allStages(normalizeDoc(parse(yaml)));
	} catch {
		return [];
	}
	const out: string[] = [];
	for (const s of stages) {
		const spec = actions.find((a) => a.slug === s.action);
		for (const set of spec?.settings ?? []) {
			const v = s.with?.[set.name];
			if (set.unbound && isParamRef(v) && !out.includes(v.slice(1))) out.push(v.slice(1));
		}
	}
	return out;
}
