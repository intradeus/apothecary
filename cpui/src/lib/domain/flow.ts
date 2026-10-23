// Flows: the documents a run executes, and the registries their stages may
// name.

import type { FlowDoc } from '$lib/flowdoc';

export interface FlowLayout {
	nodes: Record<string, { x: number; y: number }>;
}

export interface Flow {
	/** The project that owns it. A flow's stages name that project's commands
	 *  and its agents know that repository, so the same name in two projects is
	 *  two different documents. Empty on a built-in template, which belongs to
	 *  no project until one adopts it. */
	projectId: string;
	name: string;
	yaml: string;
	hash: string;
	layout?: FlowLayout;
	updatedAt: string;
	/** A compiled-in template this project has not adopted. Saving it creates
	 *  the project's own copy. */
	builtin: boolean;
	/** This project's own copy of a template's name; deleting it resets to the
	 *  template. */
	shadowsBuiltin: boolean;
	/** The document's parameters — what a project may bind (the engine-known
	 *  six always present) — and its phases, where a run may begin. Empty when
	 *  the document no longer parses. */
	parameters: ParamDecl[];
	phases: PhaseSummary[];
}

export interface FlowVersion {
	hash: string;
	name: string;
	yaml: string;
	createdAt: string;
}

export interface FlowValidation {
	ok: boolean;
	errors: string[];
	warnings: string[];
	name?: string;
	hash?: string;
	stages?: number;
	agents?: number;
	doc?: FlowDoc;
	/** The document's parameters (the engine-known six always present) and
	 *  where they are read; its phases with the wait point's effective values. */
	parameters: ParamDecl[];
	refs: ParamRef[];
	phases: PhaseSummary[];
}

/** A project's values for a flow's parameters: laid over the document's
 *  defaults at run creation (and under a run's own overrides), never written
 *  into the document. A template is bindable without being copied. */
export interface FlowBinding {
	projectId: string;
	flowName: string;
	params: Record<string, string>;
	updatedAt: string;
}

/** One declared parameter of a flow (FLOWLANG §1). */
export interface ParamDecl {
	name: string;
	type: 'bool' | 'int' | 'float' | 'string';
	default: string;
	description?: string;
	enum?: string[];
	/** One of the six the engine reads itself. */
	known: boolean;
}

/** One place a document reads a parameter. */
export interface ParamRef {
	param: string;
	phase?: string;
	stage?: string;
	field: string;
	input?: string;
}

/** One phase of a flow with its wait point's effective values. */
export interface PhaseSummary {
	id: string;
	description?: string;
	signal: string;
	start: 'auto' | 'confirm' | 'manual';
	quietFor?: string;
	max: number;
	/** A run may begin here (on: run_start). */
	entry: boolean;
	/** It ends the run (on: mr_merged / mr_closed); `then` is what a
	 *  stage-less outcome does. */
	outcome: boolean;
	then?: 'done' | 'abort';
	/** Its runs act on one merge request. */
	subject: boolean;
	stages: number;
}

/** One input or output in a built-in action's signature. A 'file' carries an
 *  artifact path; a 'value' is engine-held and reaches later stages through
 *  its $SENTINEL. */
export interface ActionIO {
	name: string;
	description?: string;
	/** enum: a literal the stage writes, one of `values` (sync_target's
	 *  strategy); `defaultValue` applies when the stage leaves it unbound.
	 *  text: a free literal the stage writes (a tracker's status or field
	 *  name) — the author's choice, not dataflow. */
	kind: 'file' | 'value' | 'enum' | 'text';
	required: boolean;
	defaultPath?: string;
	sentinel?: string;
	values?: string[];
	defaultValue?: string;
	/** A file output's path. */
	path?: string;
}

/** What an `action` stage may name. Compiled in, not project data. */
export interface ActionSpec {
	slug: string;
	title: string;
	description?: string;
	/** Integration port the project must have configured (e.g. 'forge'). */
	requires?: string;
	/** At most one stage per phase may use it. */
	single: boolean;
	/** Inputs of which a stage binds exactly one (ticket_transition's
	 *  to / status, ticket_set_field's value / text). */
	exactlyOne?: string[];
	/** Acts on an external service only (the ticket tracker): needs no
	 *  workspace unless a file input is bound. */
	remote: boolean;
	/** A run fact the action depends on ('merge_request' for ticket_link). */
	needs?: string;
	inputs: ActionIO[];
	outputs: ActionIO[];
}

/** What a `wait` stage may block on, or a gate's auto-answer may name. */
export interface SignalSpec {
	slug: string;
	title: string;
	description?: string;
	requires?: string;
	/** A run fact the signal depends on ('merge_request'), so a flow cannot
	 *  wait on something that can never happen. */
	needs?: string;
	/** Begins a reaction phase (`on:`) rather than ending a wait or answering
	 *  a gate — the two uses are exclusive. */
	trigger: boolean;
	/** May begin the run: allowed as the first phase's `on:`. */
	entry: boolean;
	/** Ends the run: a phase on it is an OUTCOME — it opens once, when the
	 *  signal holds, and every route in it ends the run. Still waitable. */
	outcome: boolean;
	/** A trigger that fires on activity, so its phase may take a quiet_for. */
	debounced: boolean;
}
