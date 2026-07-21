// The flow document model the graph editor edits (DESIGN3 §2.3): the raw
// YAML schema shape, mirrored from the Go side's flow.Doc (ParseDoc hands
// it out via /api/flows/validate). The editor mutates this object; docToYaml
// re-emits canonical YAML through the same save/validate/version pipeline —
// the graph is a view, never a second format.
import { stringify } from 'yaml';

export const DONE = 'done';
// ABORT is the reserved edge target that deliberately stops the run
// (projected as abandoned) — the explicit twin of DONE.
export const ABORT = 'abort';
export const EDGE_KINDS = ['on_fail', 'on_reject', 'on_timeout'] as const;
export type EdgeKind = (typeof EDGE_KINDS)[number];

export type StageKind = 'agent' | 'command' | 'human_gate' | 'forge_mr';

// An edge is a chain: when a link's max is exhausted the next failure
// falls through to `then` (retry-then-escalate). A link with to: abort
// stops the run and carries no max/feed/then.
export interface DocEdge {
  to: string;
  max?: number;
  feed?: string;
  then?: DocEdge;
}

// chainLinks flattens an edge chain for editing/rendering (index = depth).
export function chainLinks(e: DocEdge | undefined): DocEdge[] {
  const out: DocEdge[] = [];
  for (let cur = e; cur; cur = cur.then) out.push(cur);
  return out;
}

// Which edge kinds a stage type may declare (mirror of the Go registry).
export function edgeKindsFor(type: StageKind): EdgeKind[] {
  const out: EdgeKind[] = [];
  if (type === 'agent' || type === 'command') out.push('on_fail', 'on_timeout');
  if (type === 'agent' || type === 'human_gate') out.push('on_reject');
  return out;
}

export interface DocStage {
  id: string;
  type: StageKind;
  agent?: string;
  model?: string;
  harness?: string;
  max_turns?: number;
  timeout?: string;
  input?: string[];
  output?: string;
  verdict?: boolean;
  run?: string;
  run_ref?: string;
  ping?: string;
  payload?: string;
  auto_complete?: string;
  body?: string;
  on_success?: string;
  on_fail?: DocEdge;
  on_reject?: DocEdge;
  on_timeout?: DocEdge;
}

export interface FlowDoc {
  name: string;
  description?: string;
  defaults: { timeout?: string; harness?: string; model: string; max_turns?: number };
  budget: { max_cost_usd?: number; max_total_turns?: number };
  stages: DocStage[];
}

// effectiveSuccess resolves the YAML sugar for display: explicit target, or
// next stage in document order, or done.
export function effectiveSuccess(doc: FlowDoc, i: number): string {
  const s = doc.stages[i];
  if (s.on_success) return s.on_success;
  return i === doc.stages.length - 1 ? DONE : doc.stages[i + 1].id;
}

export function edgeOf(s: DocStage, kind: EdgeKind): DocEdge | undefined {
  return kind === 'on_fail' ? s.on_fail : kind === 'on_reject' ? s.on_reject : s.on_timeout;
}

export function setEdge(s: DocStage, kind: EdgeKind, e: DocEdge | undefined) {
  if (kind === 'on_fail') s.on_fail = e;
  else if (kind === 'on_reject') s.on_reject = e;
  else s.on_timeout = e;
}

// pruneEdgeTarget drops chain links that target a deleted stage id,
// splicing their fallbacks up; returns the surviving chain head.
export function pruneEdgeTarget(e: DocEdge | undefined, id: string): DocEdge | undefined {
  if (!e) return undefined;
  const rest = pruneEdgeTarget(e.then, id);
  if (e.to === id) return rest;
  e.then = rest;
  return e;
}

// successOrder returns stages ordered by the success chain from the entry
// (doc.stages[0]) followed by the remaining stages in document order — the
// serialization order (DESIGN3 §2.3). Entry stays first by construction.
export function successOrder(doc: FlowDoc): DocStage[] {
  if (doc.stages.length === 0) return [];
  const byId = new Map(doc.stages.map((s) => [s.id, s]));
  const seen = new Set<string>();
  const out: DocStage[] = [];
  let cur: DocStage | undefined = doc.stages[0];
  let i = doc.stages.indexOf(cur);
  while (cur && !seen.has(cur.id)) {
    seen.add(cur.id);
    out.push(cur);
    const next = effectiveSuccess(doc, i);
    if (next === DONE) break;
    cur = byId.get(next);
    i = cur ? doc.stages.indexOf(cur) : -1;
  }
  for (const s of doc.stages) if (!seen.has(s.id)) out.push(s);
  return out;
}

// docToYaml emits canonical YAML: stages in success order with a fully
// explicit on_success on every stage (the editor never relies on sugar).
export function docToYaml(doc: FlowDoc): string {
  const ordered = successOrder(doc);
  const idx = new Map(doc.stages.map((s, i) => [s.id, i]));
  const obj: Record<string, unknown> = { name: doc.name };
  if (doc.description) obj.description = doc.description;
  const defaults: Record<string, unknown> = {};
  if (doc.defaults.timeout) defaults.timeout = doc.defaults.timeout;
  if (doc.defaults.harness) defaults.harness = doc.defaults.harness;
  defaults.model = doc.defaults.model;
  if (doc.defaults.max_turns != null) defaults.max_turns = doc.defaults.max_turns;
  obj.defaults = defaults;
  const budget: Record<string, unknown> = {};
  if (doc.budget?.max_cost_usd != null) budget.max_cost_usd = doc.budget.max_cost_usd;
  if (doc.budget?.max_total_turns != null) budget.max_total_turns = doc.budget.max_total_turns;
  if (Object.keys(budget).length > 0) obj.budget = budget;
  obj.stages = ordered.map((s) => {
    const o: Record<string, unknown> = { id: s.id, type: s.type };
    if (s.agent) o.agent = s.agent;
    if (s.model) o.model = s.model;
    if (s.harness) o.harness = s.harness;
    if (s.max_turns != null) o.max_turns = s.max_turns;
    if (s.timeout) o.timeout = s.timeout;
    if (s.input?.length) o.input = s.input;
    if (s.output) o.output = s.output;
    if (s.verdict) o.verdict = true;
    if (s.run) o.run = s.run;
    if (s.run_ref) o.run_ref = s.run_ref;
    if (s.ping) o.ping = s.ping;
    if (s.payload) o.payload = s.payload;
    if (s.auto_complete) o.auto_complete = s.auto_complete;
    if (s.body) o.body = s.body;
    o.on_success = s.on_success ?? effectiveSuccess(doc, idx.get(s.id)!);
    for (const kind of EDGE_KINDS) {
      const e = edgeOf(s, kind);
      if (e) o[kind] = edgeToYamlValue(e);
    }
    return o;
  });
  return stringify(obj, { lineWidth: 0 });
}

// edgeToYamlValue serializes one chain link (recursing into then). An
// abort link uses the parser's scalar sugar: `on_reject: abort`.
function edgeToYamlValue(e: DocEdge): unknown {
  if (e.to === ABORT) return ABORT;
  const eo: Record<string, unknown> = { to: e.to, max: e.max };
  if (e.feed) eo.feed = e.feed;
  if (e.then) eo.then = edgeToYamlValue(e.then);
  return eo;
}

// autoLayout places nodes left-to-right along the success chain, dropping
// off-chain stages into lanes below (used when no stored layout exists).
export function autoLayout(doc: FlowDoc): Record<string, { x: number; y: number }> {
  const pos: Record<string, { x: number; y: number }> = {};
  const ordered = successOrder(doc);
  const chain = new Set<string>();
  let cur = doc.stages[0]?.id;
  const byId = new Map(doc.stages.map((s, i) => [s.id, i]));
  while (cur && cur !== DONE && !chain.has(cur)) {
    chain.add(cur);
    cur = effectiveSuccess(doc, byId.get(cur)!);
  }
  let cx = 0;
  let ox = 0;
  for (const s of ordered) {
    if (chain.has(s.id)) {
      pos[s.id] = { x: cx * 240, y: 0 };
      cx++;
    } else {
      pos[s.id] = { x: ox * 240 + 120, y: 160 };
      ox++;
    }
  }
  return pos;
}

// freshStage builds a new stage of the given type with a unique id.
export function freshStage(doc: FlowDoc, type: StageKind): DocStage {
  const base = { agent: 'agent', command: 'cmd', human_gate: 'gate', forge_mr: 'mr' }[type];
  let n = 1;
  let id = `${base}_${n}`;
  const ids = new Set(doc.stages.map((s) => s.id));
  while (ids.has(id)) id = `${base}_${++n}`;
  const s: DocStage = { id, type, on_success: DONE };
  if (type === 'agent') s.agent = '';
  if (type === 'command') s.run_ref = '';
  if (type === 'human_gate') s.ping = 'plan_approval';
  if (type === 'forge_mr') s.body = 'artifacts/mr.md';
  return s;
}
