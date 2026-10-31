// Editing a flow's agents (FLOWLANG §1, §4): the operations the graph's
// sidebar and the Agents tab share, so both change the document — and so
// the YAML — the same way. Each mutates the document in place; the caller
// marks it changed.
import { confirmDialog } from '$lib/ui/confirm.svelte';
import { t, th } from '$lib/i18n/index.svelte';
import {
	AGENT_NAME,
	agentUsers,
	renameAgent,
	renameAgentPort,
	type DocAgent,
	type FlowDoc
} from '$lib/domain/flowdoc';

export type AgentField = 'input' | 'output';

/** Where an agent runs: a stage, written in a chain or in a sequence. */
export interface AgentRunner {
	label: string;
	id: string;
	sequence?: string;
}

/** runnersOf lists the stages running the agent, chains first. */
export function runnersOf(doc: FlowDoc, name: string): AgentRunner[] {
	return [
		...doc.stages
			.filter((s) => s.type === 'agent' && s.agent === name)
			.map((s) => ({ label: s.id, id: s.id })),
		...(doc.sequences ?? []).flatMap((q) =>
			q.stages
				.filter((s) => s.type === 'agent' && s.agent === name)
				.map((s) => ({ label: `${q.id} › ${s.id}`, id: s.id, sequence: q.id }))
		)
	];
}

/** renameAgentTo renames an agent and every stage running it; false when
 *  the name is not a free slug. */
export function renameAgentTo(doc: FlowDoc, from: string, to: string): boolean {
	to = to.trim();
	if (!to || to === from || !AGENT_NAME.test(to) || doc.agents?.some((a) => a.name === to))
		return false;
	renameAgent(doc, from, to);
	return true;
}

/** deleteAgent asks, then removes the agent — naming the stages that will
 *  stop validating. True when it was removed. */
export async function deleteAgent(doc: FlowDoc, name: string): Promise<boolean> {
	const used = agentUsers(doc, name);
	const ok = await confirmDialog({
		title: t('flow.insp.deleteAgentTitle', { name }),
		body: [
			used.length
				? th('flow.insp.deleteAgentUsed', { name, count: used.length, stages: used.join(', ') })
				: th('flow.insp.deleteAgentBody', { name }),
			t('flow.insp.deleteNote')
		].join(' '),
		action: t('flow.insp.deleteAgent')
	});
	if (!ok) return false;
	doc.agents = doc.agents?.filter((a) => a.name !== name);
	if (!doc.agents?.length) doc.agents = undefined;
	return true;
}

// A port is one declared input or output: a slug unique on its agent.
const freePort = (taken: { name: string }[], base: string) => {
	let n = 1;
	while (taken.some((p) => p.name === `${base}_${n}`)) n++;
	return `${base}_${n}`;
};

/** addPort declares a new input or output, a plain file to start with. */
export function addPort(a: DocAgent, field: AgentField) {
	if (field === 'input')
		a.inputs = [...(a.inputs ?? []), { name: freePort(a.inputs ?? [], 'input'), type: 'file' }];
	else
		a.outputs = [...(a.outputs ?? []), { name: freePort(a.outputs ?? [], 'output'), type: 'file' }];
}

/** removePort drops a declared input or output. */
export function removePort(a: DocAgent, field: AgentField, name: string) {
	if (field === 'input') {
		a.inputs = a.inputs?.filter((p) => p.name !== name);
		if (!a.inputs?.length) a.inputs = undefined;
	} else {
		a.outputs = a.outputs?.filter((p) => p.name !== name);
		if (!a.outputs?.length) a.outputs = undefined;
	}
}

/** renamePort renames a declared input or output, and the port every stage
 *  running the agent states under that name; false when the name is not a
 *  free slug. */
export function renamePort(
	doc: FlowDoc,
	a: DocAgent,
	field: AgentField,
	from: string,
	to: string
): boolean {
	to = to.trim();
	const list = (field === 'input' ? a.inputs : a.outputs) ?? [];
	const port = list.find((p) => p.name === from);
	if (!port || !to || to === from || !AGENT_NAME.test(to) || list.some((p) => p.name === to))
		return false;
	port.name = to;
	renameAgentPort(doc, a.name, field, from, to);
	return true;
}
