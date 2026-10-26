// Flows: the documents a run executes, and the registries their stages may
// name.

import type { FlowDoc, PhaseKind, OperatesOn } from '$lib/flowdoc';

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
	/** What its runs are started on, the document's parameters — what a
	 *  project may bind (the engine-known six always present) — and its
	 *  phases: the main chain, the watchers, the outcomes. Empty (operatesOn
	 *  undefined) when the document no longer parses. */
	operatesOn?: OperatesOn;
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
	/** What its runs are started on, the document's parameters (the
	 *  engine-known six always present) and where they are read; its phases
	 *  with the wait point's effective values. */
	operatesOn?: OperatesOn;
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

/** One phase of a flow — its main chain, a watcher or an outcome — with its
 *  wait point's effective values. */
export interface PhaseSummary {
	id: string;
	description?: string;
	kind: PhaseKind;
	signal: string;
	start: 'auto' | 'confirm' | 'manual';
	quietFor?: string;
	max: number;
	/** How an outcome ends the run. */
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
	/** Integration port the project must have configured (e.g. 'repository'). */
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

/** A signal (FLOWLANG §1): what it is, never what it does — the section
 *  that names it decides that. */
export interface SignalSpec {
	slug: string;
	title: string;
	description?: string;
	requires?: string;
	/** A run fact the signal depends on ('merge_request'), so a flow cannot
	 *  wait on something that can never happen. */
	needs?: string;
	/** hand: manual, a person's start of a watcher; observed: a fact about
	 *  the run's merge requests — which any watcher, outcome, wait or gate
	 *  may name. */
	kind: 'hand' | 'observed';
	/** It fires on activity, so a watcher on it may take a quiet_for. */
	debounced: boolean;
	/** As an outcome or a wait on the whole run, it holds once every merge
	 *  request has it; otherwise as soon as one does. */
	all: boolean;
}
