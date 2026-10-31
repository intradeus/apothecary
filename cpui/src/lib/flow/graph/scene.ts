// What building the canvas needs to know, read once per build: the document
// and the view of it, and the chain facts both the nodes and the edges ask.
import {
	ABORT,
	END,
	hasHub,
	MAIN,
	type ActionContract,
	type AgentContract,
	type DocPhase,
	type FlowDoc
} from '$lib/domain/flowdoc';
import type { SequenceView } from '$lib/flow/graph/sequence';

export type Layout = Record<string, { x: number; y: number }>;

export interface SceneInput {
	doc: FlowDoc;
	layout: Layout;
	agents: AgentContract[];
	actions: ActionContract[];
	/** Draw this sequence instead of a flow. */
	sequence?: SequenceView;
	/** The watchers and outcomes folded to their marker. */
	collapsed: Set<string>;
}

export interface Scene extends SceneInput {
	/** The flow has watchers or outcomes: its main chain ends on the hub. */
	hub: boolean;
	mainPhase?: DocPhase;
	/** The watchers and outcomes. */
	triggers: DocPhase[];
	/** A flow that only watches: it begins at the hub. */
	watchesOnly: boolean;
	/** The watcher or outcome a stage of this phase belongs to, if any. */
	branch: (phase: string | undefined) => DocPhase | undefined;
	/** The node a route to a terminal lands on. */
	termNode: (phase: string | undefined, to: string) => string;
	/** Which chain a node belongs to (data.band). */
	bandOf: (phase: string | undefined) => string;
	/** Whether this phase's chain is folded away. */
	hidden: (phase: string | undefined) => boolean;
}

/** A watcher's hue, or an outcome's: the hue of the run's end it says. */
export const chainHue = (ph: DocPhase) =>
	ph.kind !== 'outcome' ? 'accent' : ph.then === ABORT ? 'danger' : 'success';

export function sceneOf(input: SceneInput): Scene {
	const { doc, sequence, collapsed } = input;
	// The hub: once the flow has watchers or outcomes, the main chain's end
	// is the run waiting on its merge requests, and each of them branches
	// from there with ends of its own — nothing crosses the canvas to a
	// shared terminal.
	const hub = !sequence && hasHub(doc);
	const phaseById = new Map((doc.phases ?? []).map((p) => [p.id, p]));
	const branch = (phase: string | undefined) => {
		const ph = !sequence && phase ? phaseById.get(phase) : undefined;
		return ph && ph.kind !== 'main' ? ph : undefined;
	};
	const mainPhase = doc.phases?.find((p) => p.kind === 'main');
	return {
		...input,
		hub,
		mainPhase,
		triggers: (doc.phases ?? []).filter((p) => p.kind !== 'main'),
		watchesOnly: hub && !mainPhase,
		branch,
		// A watcher's or an outcome's own, the main chain's shared ones (its
		// end the hub, once there is one), a sequence's.
		termNode: (phase, to) => {
			const ph = branch(phase);
			if (ph) return `__${to}:${ph.id}`;
			return to === END && hub ? '__hub' : `__${to}`;
		},
		// A node moves with its chain's marker, and a watcher's or an
		// outcome's frame wraps it.
		bandOf: (phase) => (phase ?? '') || MAIN,
		hidden: (phase) => !!branch(phase) && collapsed.has(phase!)
	};
}
