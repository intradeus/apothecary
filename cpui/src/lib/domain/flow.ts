// Flows: the documents a run executes, and the registries their stages may
// name.

import type { FlowDoc } from '$lib/flowdoc';

export interface FlowLayout {
  nodes: Record<string, { x: number; y: number }>;
}

export interface Flow {
  name: string;
  yaml: string;
  hash: string;
  layout?: FlowLayout;
  updatedAt: string;
  /** Served from the binary; saving creates an editable override. */
  builtin: boolean;
  /** This row overrides a built-in name; deleting it resets to the
   *  compiled-in default. */
  shadowsBuiltin: boolean;
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
}

/** One input or output in a built-in action's signature. A 'file' carries an
 *  artifact path; a 'value' is engine-held and reaches later stages through
 *  its $SENTINEL. */
export interface ActionIO {
  name: string;
  description?: string;
  kind: 'file' | 'value';
  required: boolean;
  defaultPath?: string;
  sentinel?: string;
}

/** What an `action` stage may name. Compiled in, not project data. */
export interface ActionSpec {
  slug: string;
  title: string;
  description?: string;
  /** Integration port the project must have configured (e.g. 'forge'). */
  requires?: string;
  /** At most one stage per flow may use it. */
  single: boolean;
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
}
