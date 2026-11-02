// What the inspector's sections share and the panel around them asks: what
// a selection is called, what a stage may name, and the question a delete
// asks. Nothing here marks the document changed: the caller does, as with
// the agents' operations (agents.ts).
//
// A confirm* only asks, and removes nothing: the document may have been
// replaced by the time it is answered, so the caller removes from the
// document it holds then.
import type { Selection } from '$lib/flow/FlowGraph.svelte';
import { CHANGE_TYPES, type SignalSpec } from '$lib/domain';
import { confirmDialog } from '$lib/ui/confirm.svelte';
import { t, th } from '$lib/i18n/index.svelte';
import { boolParams } from '$lib/flow/params';
import {
	ABORT,
	END,
	EDGE_KINDS,
	MAIN,
	allStages,
	chainLinks,
	edgeOf,
	hasMain,
	mcpUsers,
	pruneEdgeTarget,
	schemaUsers,
	sequenceUsers,
	setEdge,
	type DocPhase,
	type DocSequence,
	type DocStage,
	type FlowDoc,
	type StageKind
} from '$lib/domain/flowdoc';

// The field of a rename the document refused shows the name in force.
export const renamed = (el: HTMLInputElement, current: string, took: boolean) => {
	if (!took) el.value = current;
};

export function setOperatesOn(doc: FlowDoc, v: string) {
	doc.operates_on = v === 'merge_request' ? 'merge_request' : undefined;
}

// A chain's kind in the marker's hue: main brand, watchers violet, an
// outcome the end it says.
export const kindHue = (ph: DocPhase) =>
	ph.kind === 'main'
		? 'brand'
		: ph.kind === 'watcher'
			? 'accent'
			: ph.then === ABORT
				? 'danger'
				: 'success';

// Any signal observed on the run's merge requests may end a wait, open
// a watcher or fire an outcome: the section decides what it
// does (FLOWLANG §1). manual — a person's start — opens a watcher only.
export const observedSignals = (signals: SignalSpec[]) =>
	signals.filter((g) => g.kind === 'observed');

// The run facts a condition names by path (flow.CondTracker, CondSubject,
// CondChangeType compared with a flow.CondChangeTypes entry).
const RUN_IF_FACTS = () => [
	{ value: 'run.ticket.tracker', label: t('flow.insp.runIf.tracker') },
	{ value: 'not run.ticket.tracker', label: t('flow.insp.runIf.notTracker') },
	{ value: 'run.subject', label: t('flow.insp.runIf.subject') },
	{ value: 'not run.subject', label: t('flow.insp.runIf.notSubject') },
	...CHANGE_TYPES.flatMap((c) => [
		{ value: 'run.ticket.change_type == ' + c, label: t('flow.insp.runIf.change', { type: c }) },
		{
			value: 'run.ticket.change_type != ' + c,
			label: t('flow.insp.runIf.notChange', { type: c })
		}
	])
];

/** What a stage's run_if may say: nothing, a run fact, a boolean parameter
 *  or its negation, or what it says already when it is none of those. */
export const runIfOptions = (doc: FlowDoc, current?: string) => [
	{ value: '', label: t('flow.insp.runIf.none') },
	...RUN_IF_FACTS(),
	...boolParams(doc).flatMap((p) => [
		{ value: '$' + p, label: '$' + p },
		{ value: 'not $' + p, label: 'not $' + p }
	]),
	...(current &&
	!RUN_IF_FACTS().some((o) => o.value === current) &&
	!boolParams(doc).some((p) => current === '$' + p || current === 'not $' + p)
		? [{ value: current, label: current }]
		: [])
];

// The stages — in the chains and in sequences — that match, each as the
// selection that opens it.
export const runners = (
	doc: FlowDoc,
	match: (s: DocStage) => boolean
): { label: string; sel: Selection }[] => [
	...doc.stages
		.filter(match)
		.map((s) => ({ label: s.id, sel: { kind: 'stage', id: s.id } as const })),
	...(doc.sequences ?? []).flatMap((q) =>
		q.stages.filter(match).map((s) => ({
			label: `${q.id} › ${s.id}`,
			sel: { kind: 'stage', id: s.id, sequence: q.id } as const
		}))
	)
];

// What an input may read besides the engine's sources: every path a stage
// of the flow writes.
export const writtenPathsOf = (doc: FlowDoc) =>
	[
		...new Set(
			allStages(doc).flatMap((x) =>
				Object.values(x.output ?? {})
					.map((p) => p.to ?? '')
					.filter(Boolean)
			)
		)
	].sort();

// What each written path holds, by path: a row picking one takes its
// writer's type — so a gate shown a review curates it, rather than reading
// it as a plain file.
export const writtenTypesOf = (doc: FlowDoc) =>
	Object.fromEntries(
		allStages(doc).flatMap((x) =>
			Object.values(x.output ?? {})
				.filter((p) => p.to && p.type)
				.map((p) => [p.to!, p.type!] as const)
		)
	) as Record<string, string>;

export const schemaNamesOf = (doc: FlowDoc) => (doc.schemas ?? []).map((q) => q.name);

// The stages a chain or a sequence may add: a sequence stage once there
// is something for it to run — another sequence, or the main chain again
// (from anywhere but the main chain itself).
export const addKinds = (
	doc: FlowDoc,
	where: { phase?: string; sequence?: string }
): StageKind[] => {
	const runs =
		(doc.sequences ?? []).some((q) => q.id !== where.sequence) ||
		(hasMain(doc) && where.phase !== MAIN);
	return [
		'agent',
		'command',
		'human_gate',
		'manual',
		'action',
		'wait',
		'service',
		...(runs ? ['sequence' as const] : [])
	];
};

// The inspector cross-fades between selections instead of jumping: the
// outgoing and incoming views share one grid cell for the moment. An edge
// is its stage's view: selecting one keeps the stage open.
export const viewKeyOf = (selection: Selection) =>
	selection?.kind === 'stage'
		? `s:${selection.sequence ?? ''}:${selection.id}`
		: selection?.kind === 'edge'
			? `s:${selection.sequence ?? ''}:${selection.stage}`
			: selection?.kind === 'phase'
				? `p:${selection.id}`
				: selection?.kind === 'sequence'
					? `q:${selection.id}`
					: selection?.kind === 'schema'
						? `j:${selection.name}`
						: selection?.kind === 'mcp'
							? `m:${selection.name}`
							: 'flow';

// What the back button names: the panel `back` opens.
export function backLabelOf(doc: FlowDoc, b: Selection | undefined): string {
	if (b === undefined) return '';
	if (b === null) return t('flow.insp.formula');
	switch (b.kind) {
		case 'stage':
			return t('flow.insp.backStage', { id: b.sequence ? `${b.sequence} › ${b.id}` : b.id });
		case 'edge':
			return t('flow.insp.backStage', {
				id: b.sequence ? `${b.sequence} › ${b.stage}` : b.stage
			});
		case 'phase': {
			const ph = doc.phases?.find((p) => p.id === b.id);
			return ph?.kind === 'main'
				? t('flow.insp.chainMain')
				: t(ph?.kind === 'outcome' ? 'flow.insp.backOutcome' : 'flow.insp.backWatcher', {
						id: b.id
					});
		}
		case 'agent':
			return t('flow.insp.backAgent', { name: b.name });
		case 'sequence':
			return t('flow.insp.backSequence', { id: b.id });
		case 'schema':
			return t('flow.insp.backSchema', { name: b.name });
		case 'mcp':
			return t('flow.insp.backMCP', { name: b.name });
	}
}

export function confirmDeleteStage(stages: DocStage[], id: string) {
	const refs = stages.filter(
		(s) =>
			s.id !== id &&
			(s.on_success === id ||
				EDGE_KINDS.some((k) => chainLinks(edgeOf(s, k)).some((l) => l.to === id)))
	).length;
	return confirmDialog({
		title: t('flow.insp.deleteStageTitle', { id }),
		body: [
			th('flow.insp.deleteBody', { id }),
			refs ? th('flow.insp.deleteRewired', { count: refs }) : '',
			t('flow.insp.deleteNote')
		]
			.filter(Boolean)
			.join(' '),
		action: t('flow.insp.deleteAction')
	});
}

/** dropStage takes a stage out of its chains or its sequence, and out of
 *  every route that led there. */
export function dropStage(doc: FlowDoc, seq: DocSequence | undefined, id: string) {
	const rest = (seq ? seq.stages : doc.stages).filter((s) => s.id !== id);
	for (const s of rest) {
		// A success that led there ends the chain — in a sequence it goes
		// on to the next stage, or out.
		if (s.on_success === id) s.on_success = seq ? undefined : END;
		for (const kind of EDGE_KINDS) {
			setEdge(s, kind, pruneEdgeTarget(edgeOf(s, kind), id));
		}
	}
	if (seq) seq.stages = rest;
	else doc.stages = rest;
}

export function confirmDeletePhase(doc: FlowDoc, ph: DocPhase) {
	const count = doc.stages.filter((x) => x.phase === ph.id).length;
	return confirmDialog({
		title: t(
			ph.kind === 'outcome' ? 'flow.insp.deleteOutcomeTitle' : 'flow.insp.deleteWatcherTitle',
			{
				id: ph.id
			}
		),
		body: [
			count
				? th('flow.insp.deleteChainBody', { id: ph.id, count })
				: th('flow.insp.deleteChainEmpty', { id: ph.id }),
			t('flow.insp.deleteNote')
		].join(' '),
		action: t(ph.kind === 'outcome' ? 'flow.insp.deleteOutcome' : 'flow.insp.deleteWatcher')
	});
}

/** False at once while a stage still runs the sequence. */
export async function confirmDeleteSequence(doc: FlowDoc, q: DocSequence) {
	if (sequenceUsers(doc, q.id).length) return false;
	return confirmDialog({
		title: t('flow.insp.deleteSequenceTitle', { id: q.id }),
		body: [
			th('flow.insp.deleteSequenceBody', { id: q.id, count: q.stages.length }),
			t('flow.insp.deleteNote')
		].join(' '),
		action: t('flow.insp.deleteSequence')
	});
}

/** False at once while a port still names the schema. */
export async function confirmDeleteSchema(doc: FlowDoc, name: string) {
	if (schemaUsers(doc, name).length) return false;
	return confirmDialog({
		title: t('flow.insp.deleteSchemaTitle', { name }),
		body: [th('flow.insp.deleteSchemaBody', { name }), t('flow.insp.deleteNote')].join(' '),
		action: t('flow.insp.deleteSchema')
	});
}

export function confirmDeleteMCP(doc: FlowDoc, name: string) {
	const users = mcpUsers(doc, name);
	return confirmDialog({
		title: t('flow.insp.deleteMCPTitle', { name }),
		body: [
			users.length
				? th('flow.insp.deleteMCPUsed', { name, count: users.length, agents: users.join(', ') })
				: th('flow.insp.deleteMCPBody', { name }),
			t('flow.insp.deleteNote')
		].join(' '),
		action: t('flow.insp.deleteMCP')
	});
}
