// Typed client for the apothecary engine API (DESIGN.md §8). The CPUI talks
// only to this API — same origin (served by apothecaryd, or vite-proxied).

export interface RunView {
  run_id: string;
  project_id?: string;
  state: 'pending' | 'provisioning' | 'running' | 'waiting_on_human' | 'paused' | 'completed' | 'abandoned' | 'failed' | 'archived';
  ticket_title: string;
  flow_name: string;
  flow_hash: string;
  current_stage?: string;
  ticket_id?: string; // parent ticket (a ticket owns many runs)
  run_goal?: string; // extra direction for THIS attempt (additive to the ticket's goal)
  branch?: string;
  source_branch?: string; // branch this run forked off from (origin/<source_branch>)
  mr_url?: string;
  env_id?: string;
  // Reconciled live container status. 'unknown' = the provisioner couldn't
  // be reached (expired session, deleted project). Field absent = not yet
  // reconciled; the badge falls back to env_id.
  // The reconciled docker status, or an in-flight-op overlay
  // (provisioning/rebuilding/destroying) / recently-failed op (error).
  env_status?:
    | 'running'
    | 'starting'
    | 'stopped'
    | 'absent'
    | 'unknown'
    | 'provisioning'
    | 'rebuilding'
    | 'destroying'
    | 'error';
  created_at: string;
  updated_at: string;
}

// --- config plane (DESIGN2 §10) ---------------------------------------------

// Model auth is harness-owned (post-M8 amendment) — no provider port.
export type Port = 'forge' | 'provisioner' | 'harness';

export interface KindField {
  name: string;
  label: string;
  placeholder?: string;
  default?: string;
  secret: boolean;
  required: boolean;
}

export interface KindDescriptor {
  port: Port;
  kind: string;
  label: string;
  fields: KindField[] | null;
}

export interface Integration {
  id: string;
  port: Port;
  kind: string;
  name: string;
  config: Record<string, unknown>;
  secrets: Record<string, string>; // masks only — values never round-trip
  created_at: string;
  updated_at: string;
}

export interface RepoInfo {
  name: string;
  url: string;
  locator: string;
  default_branch: string;
}

// ModelInfo.id is the exact `provider/model` string flows use.
export interface ModelInfo {
  id: string;
  name: string;
}

export interface Project {
  id: string;
  name: string;
  forge_id: string;
  provisioner_id: string;
  // The project's set of available harness integrations (no default — the
  // flow names the harness kind). One integration per kind.
  harness_ids: string[];
  repo: { url: string; locator: string; default_branch: string; agents_md_path: string };
  commands: Record<string, string>;
  workspace: {
    extra_domains?: string[];
    code_server_extensions?: string[];
    // code-server UI defaults, applied as VS Code user settings when the
    // browser editor opens (no image rebuild — unlike the extensions above).
    editor?: {
      theme?: string;
      font_size?: number;
      tab_size?: number;
      format_on_save?: boolean;
      word_wrap?: boolean;
    };
    // Egress gateway posture. 'deny' = allow-all + denied_domains; anything
    // else (incl. empty) = deny-all + the derived allowlist ∪ extra_domains.
    egress_mode?: string;
    denied_domains?: string[];
    // Force-deny override that wins in BOTH modes (managed live from the run
    // page); carried through the form untouched.
    blocked_domains?: string[];
  };
  default_flow: string;
  created_at: string;
  updated_at: string;
}

// Per-attempt input when starting a run from a ticket or restarting one.
// All optional: flow overrides the default, goal/artifacts add direction for
// THIS attempt on top of the ticket's (they never modify the ticket).
export interface RunInput {
  flow?: string;
  goal?: string;
  source_branch?: string; // branch this run forks off from; empty = project default
  artifact_ids?: string[];
}

const runInputBody = (o?: RunInput) => ({
  ...(o?.flow ? { flow: o.flow } : {}),
  ...(o?.goal ? { goal: o.goal } : {}),
  ...(o?.source_branch ? { source_branch: o.source_branch } : {}),
  ...(o?.artifact_ids?.length ? { artifact_ids: o.artifact_ids } : {})
});

// A ticket is a unit of work that owns many runs (each run is one attempt).
// Flow is NOT here — it's chosen per run when starting one.
export interface Ticket {
  id: string;
  project_id: string;
  title: string;
  goal: string;
  base_branch: string; // stem; each run gets base_branch + a per-run suffix
  artifact_ids: string[];
  created_at: string;
  updated_at: string;
}

// Agent library (DESIGN3 §1): per-project, versioned, copiable.
export interface Agent {
  project_id: string;
  name: string;
  description: string;
  read_only: boolean;
  allow_bash: boolean;
  prompt: string;
  hash: string;
  updated_at: string;
  // Served from the binary's defaults (not authored in this project);
  // saving under the same name creates the project's own copy.
  builtin?: boolean;
}

export interface AgentVersion {
  hash: string;
  name: string;
  spec: { name: string; description: string; read_only: boolean; allow_bash: boolean; prompt: string };
  created_at: string;
}

export interface FlowLayout {
  nodes: Record<string, { x: number; y: number }>;
}

export interface FlowRow {
  name: string;
  yaml: string;
  hash: string;
  layout?: FlowLayout | null;
  updated_at: string;
  // Built-in layering: builtin = served from the binary (no DB row; saving
  // creates your editable override); shadows_builtin = your row overrides a
  // built-in name (deleting it resets to the compiled-in default).
  builtin?: boolean;
  shadows_builtin?: boolean;
}

export interface FlowVersion {
  hash: string;
  name: string;
  yaml: string;
  created_at: string;
}

export interface Settings {
  max_concurrent_runs: number;
  max_environments: number;
  destroy_completed_after: string;
  destroy_abandoned: string;
}

export interface PreflightStep {
  id: string;
  ok: boolean;
  error?: string;
}

export interface CheckStep {
  id: string;
  status: 'pass' | 'fail' | 'skip';
  detail?: string;
}

export interface CheckRun {
  id: string;
  project_id: string;
  status: 'running' | 'pass' | 'fail';
  keep: boolean;
  steps: CheckStep[] | null;
  started_at: string;
  finished_at?: string;
}

export interface EgressDenial {
  host: string;
  count: number;
  last: string;
}

export interface EgressAuditRow {
  project: string;
  run: string;
  host: string;
  dst_ip: string;
  port: number;
  proto: string; // tls | http | other
  verdict: string; // allow | deny
  count: number;
  bytes_out: number;
  bytes_in: number;
  first_seen: string;
  last_seen: string;
}

export interface EventEnvelope {
  run_id: string;
  seq: number;
  ts: string;
  type: string;
  payload: Record<string, unknown>;
}

export interface Ping {
  ping_id: string;
  run_id: string;
  type: 'plan_approval' | 'final_review' | 'clarification' | 'blocker' | 'permission';
  stage_id?: string;
  question: string;
  context?: string;
  payload_ref?: string;
  payload_refs?: string[]; // full ask_human attachment set (agent→human multi)
  options?: string[];
  status: 'pending' | 'answered';
  answer?: string;
  feedback?: string;
  raised_at: string;
  answered_at?: string;
}

export interface ArtifactMeta {
  id: string;
  filename: string;
  mime: string;
  size: number;
}

export interface ChannelEntry {
  id: string;
  kind: string;
  config: Record<string, unknown>;
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init);
  if (!res.ok) {
    let msg = `${res.status}`;
    try {
      const body = await res.json();
      if (body.error) msg = body.error;
    } catch {
      /* not json */
    }
    throw new Error(msg);
  }
  if (res.status === 204) return undefined as T;
  // 202s may carry a body (e.g. envcheck's {check_id}); parse whatever is there.
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

const post = (path: string, body?: unknown): Promise<unknown> =>
  req(path, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) });
const put = (path: string, body: unknown): Promise<unknown> =>
  req(path, { method: 'PUT', body: JSON.stringify(body) });

export const api = {
  runs: (filter?: { project?: string; ticket?: string }) => {
    const q = new URLSearchParams();
    if (filter?.project) q.set('project', filter.project);
    if (filter?.ticket) q.set('ticket', filter.ticket);
    const s = q.toString();
    return req<RunView[]>(`/api/runs${s ? `?${s}` : ''}`);
  },
  run: (id: string) => req<{ run: RunView; events: EventEnvelope[] }>(`/api/runs/${id}`),

  // Tickets: a ticket owns many runs. Creating a ticket does NOT start a run;
  // runs are started from the ticket (flow chosen per run).
  tickets: (project?: string) =>
    req<Ticket[]>(`/api/tickets${project ? `?project=${encodeURIComponent(project)}` : ''}`),
  ticket: (id: string) => req<Ticket>(`/api/tickets/${id}`),
  createTicket: (body: {
    title: string;
    goal: string;
    project_id: string;
    branch?: string;
    artifact_ids?: string[];
  }) => post('/api/tickets', body) as Promise<Ticket>,
  updateTicket: (
    id: string,
    body: { title: string; goal: string; project_id: string; branch?: string; artifact_ids?: string[] }
  ) => put(`/api/tickets/${id}`, body) as Promise<Ticket>,
  deleteTicket: (id: string) => req<void>(`/api/tickets/${id}`, { method: 'DELETE' }),
  // Per-attempt input: a flow override plus this run's own direction and
  // attachments (additive to the ticket's — they never modify the ticket).
  startTicketRun: (id: string, opts?: RunInput) =>
    post(`/api/tickets/${id}/runs`, runInputBody(opts)) as Promise<{ run_id: string }>,
  abort: (id: string) => post(`/api/runs/${id}/abort`),
  // Pause parks the run (workspace kept, nothing recovered until play);
  // play re-enters mid-stage like crash recovery, budgets intact.
  pause: (id: string) => post(`/api/runs/${id}/pause`),
  play: (id: string) => post(`/api/runs/${id}/play`),
  archive: (id: string) => post(`/api/runs/${id}/archive`),
  restart: (id: string, opts?: RunInput) =>
    post(`/api/runs/${id}/restart`, runInputBody(opts)) as Promise<{ run_id: string }>,
  resume: (id: string) => post(`/api/runs/${id}/resume`),
  // Recreate the workspace container in place (keeps the run + commits,
  // re-runs bootstrap). Blocks until done; the run page shows progress via
  // the workspace-log tail and the rebuilding badge.
  rebuild: (id: string) => post(`/api/runs/${id}/rebuild`),
  // Lazily start code-server in the workspace and get its browser URL (VS Code
  // in the browser). Blocks while code-server boots on first open.
  openEditor: (id: string) => post(`/api/runs/${id}/open-editor`) as Promise<{ url: string }>,
  log: async (id: string, stage: string, n = 65536): Promise<string> => {
    const res = await fetch(`/api/runs/${id}/logs/${stage}?n=${n}`);
    if (!res.ok) throw new Error(`${res.status}`);
    return res.text();
  },
  // Live tail of the workspace's exec output (optional provisioner
  // capability); null when unavailable (no env yet / unsupported).
  workspaceLog: async (id: string, n = 65536): Promise<string | null> => {
    const res = await fetch(`/api/runs/${id}/workspace-log?n=${n}`);
    if (res.status === 404) return null;
    if (!res.ok) throw new Error(`${res.status}`);
    return res.text();
  },
  // Compiled-in "sensible defaults" allowlist (registries, OS mirrors, dev-tool
  // CDNs). Offered by the project form's "Add sensible defaults" button — no
  // longer force-seeded into every project.
  egressDefaults: () => req<{ domains: string[] }>(`/api/egress-defaults`),
  // Hosts the egress proxy blocked for this run (M18) — the visible
  // diagnosis when a model/tool can't reach its endpoint.
  egressDenials: (id: string) => req<EgressDenial[]>(`/api/runs/${id}/egress-denials`),
  // Allow a host on this run's egress instantly (no restart) and persist
  // it to the project's extra domains.
  egressAllow: (id: string, host: string) =>
    post(`/api/runs/${id}/egress-allow`, { host }) as Promise<{ allowed: string; persisted: boolean }>,
  // Live-toggle a host's egress rule: allow=true permits it, allow=false
  // force-blocks it (overrides base allowlist defaults). Persisted to the project.
  egressRule: (id: string, host: string, allow: boolean) =>
    post(`/api/runs/${id}/egress-rule`, { host, allow }) as Promise<{
      host: string;
      allow: boolean;
      persisted: boolean;
    }>,
  // Full egress audit trail for this run: every host reached/blocked, with
  // counts, bytes and timestamps. Served live from the gateway (durable store
  // as fallback). Filterable by verdict (allow|deny) and host substring.
  egressAudit: (id: string, opts?: { verdict?: string; host?: string; limit?: number }) => {
    const q = new URLSearchParams();
    if (opts?.verdict) q.set('verdict', opts.verdict);
    if (opts?.host) q.set('host', opts.host);
    if (opts?.limit) q.set('limit', String(opts.limit));
    const qs = q.toString();
    return req<EgressAuditRow[]>(`/api/runs/${id}/egress-audit${qs ? `?${qs}` : ''}`);
  },
  // Live workspace-port exposures (engine-process-scoped; the registry,
  // not the env_ready event, is authoritative after restarts), plus
  // runtime-discovered listeners not yet exposed (observed) for
  // click-to-expose. service names the compose sibling a port belongs to.
  runPorts: (id: string) =>
    req<{
      ports: { port: number; url: string }[];
      observed?: { port: number; process?: string; service?: string }[];
    }>(`/api/runs/${id}/ports`),
  // Publish a workspace port and get its browser URL (idempotent per port).
  exposePort: (id: string, port: number) =>
    post(`/api/runs/${id}/expose-port`, { port }) as Promise<{ port: number; url: string }>,
  pendingPings: () => req<Ping[]>('/api/pings?status=pending'),
  pings: (status: 'pending' | 'answered' | 'all', limit = 100) =>
    req<Ping[]>(`/api/pings?status=${status}&limit=${limit}`),
  ping: (pingId: string) => req<Ping>(`/api/pings/${pingId}`),
  pingArtifact: (pingId: string) =>
    req<{ path: string; content: string }>(`/api/pings/${pingId}/artifact`),
  // Agent-attached workspace files for a ping (multi ask_human): list them,
  // plus a per-file URL to stream one (image thumbnail / download).
  pingArtifacts: (pingId: string) =>
    req<{ files: { path: string; mime: string }[] }>(`/api/pings/${pingId}/artifacts`),
  pingArtifactFileUrl: (pingId: string, path: string) =>
    `/api/pings/${pingId}/artifacts?path=${encodeURIComponent(path)}`,
  answerPing: (pingId: string, answer: string, feedback?: string, artifactIds?: string[]) =>
    post(`/api/pings/${pingId}/answer`, {
      answer,
      ...(feedback ? { feedback } : {}),
      ...(artifactIds?.length ? { artifact_ids: artifactIds } : {})
    }),
  // Human-uploaded artifacts (documents/images) for goals + ping answers.
  uploadArtifacts: (files: File[]): Promise<ArtifactMeta[]> => {
    const fd = new FormData();
    for (const f of files) fd.append('files', f);
    // No JSON.stringify / Content-Type: the browser sets the multipart boundary.
    return req<ArtifactMeta[]>('/api/artifacts', { method: 'POST', body: fd });
  },
  artifactUrl: (id: string) => `/api/artifacts/${id}`,
  runArtifacts: (id: string) =>
    req<{ goal: ArtifactMeta[]; answers: Record<string, ArtifactMeta[]> }>(
      `/api/runs/${id}/artifacts`
    ),
  channels: () => req<ChannelEntry[]>('/api/channels'),
  addChannel: (kind: string, config: Record<string, string>) =>
    post('/api/channels', { kind, ...config }) as Promise<ChannelEntry>,
  deleteChannel: (id: string) => req<void>(`/api/channels/${id}`, { method: 'DELETE' }),

  // config plane (DESIGN2 §10)
  kinds: () => req<KindDescriptor[]>('/api/kinds'),
  integrations: () => req<Integration[]>('/api/integrations'),
  createIntegration: (body: {
    port: Port;
    kind: string;
    name: string;
    config: Record<string, unknown>;
    secrets: Record<string, string>;
  }) => post('/api/integrations', body) as Promise<Integration>,
  updateIntegration: (
    id: string,
    body: { name?: string; config?: Record<string, unknown>; secrets?: Record<string, string> }
  ) => put(`/api/integrations/${id}`, body) as Promise<Integration>,
  deleteIntegration: (id: string) => req<void>(`/api/integrations/${id}`, { method: 'DELETE' }),
  testIntegration: (id: string) =>
    post(`/api/integrations/${id}/test`) as Promise<{ ok: boolean; error?: string }>,
  repos: (integrationId: string) => req<RepoInfo[]>(`/api/integrations/${integrationId}/repos`),
  models: (integrationId: string) => req<ModelInfo[]>(`/api/integrations/${integrationId}/models`),

  projects: () => req<Project[]>('/api/projects'),
  project: (id: string) => req<Project>(`/api/projects/${id}`),
  createProject: (body: Omit<Project, 'id' | 'created_at' | 'updated_at'>) =>
    post('/api/projects', body) as Promise<Project>,
  updateProject: (id: string, body: Omit<Project, 'id' | 'created_at' | 'updated_at'>) =>
    put(`/api/projects/${id}`, body) as Promise<Project>,
  deleteProject: (id: string) => req<void>(`/api/projects/${id}`, { method: 'DELETE' }),
  preflight: (id: string) =>
    post(`/api/projects/${id}/preflight`) as Promise<{ ok: boolean; steps: PreflightStep[] }>,
  startEnvCheck: (projectId: string, keep: boolean) =>
    post(`/api/projects/${projectId}/envcheck`, { keep }) as Promise<{ check_id: string }>,
  envCheck: (checkId: string) => req<CheckRun>(`/api/envchecks/${checkId}`),

  // agent library (DESIGN3 §1)
  agents: (projectId: string) => req<Agent[]>(`/api/projects/${projectId}/agents`),
  putAgent: (projectId: string, name: string, body: { description: string; read_only: boolean; allow_bash: boolean; prompt: string }) =>
    put(`/api/projects/${projectId}/agents/${encodeURIComponent(name)}`, body) as Promise<Agent>,
  deleteAgent: (projectId: string, name: string) =>
    req<void>(`/api/projects/${projectId}/agents/${encodeURIComponent(name)}`, { method: 'DELETE' }),
  agentVersions: (projectId: string, name: string) =>
    req<AgentVersion[]>(`/api/projects/${projectId}/agents/${encodeURIComponent(name)}/versions`),
  copyAgents: (projectId: string, fromProjectId: string, names?: string[]) =>
    post(`/api/projects/${projectId}/agents/copy`, { from_project_id: fromProjectId, ...(names?.length ? { names } : {}) }) as Promise<{ copied: Agent[] }>,
  flows: () => req<FlowRow[]>('/api/flows'),
  flow: (name: string) => req<FlowRow>(`/api/flows/${encodeURIComponent(name)}`),
  createFlow: (name: string, yaml: string, layout?: FlowLayout) =>
    post('/api/flows', { name, yaml, ...(layout ? { layout } : {}) }) as Promise<FlowRow>,
  saveFlow: (name: string, yaml: string, layout?: FlowLayout) =>
    put(`/api/flows/${encodeURIComponent(name)}`, { yaml, ...(layout ? { layout } : {}) }) as Promise<FlowRow>,
  deleteFlow: (name: string) => req<void>(`/api/flows/${encodeURIComponent(name)}`, { method: 'DELETE' }),
  validateFlow: (yaml: string) =>
    post('/api/flows/validate', { yaml }) as Promise<{
      ok: boolean;
      errors?: string[];
      name?: string;
      hash?: string;
      stages?: number;
      doc?: import('./flowdoc').FlowDoc;
    }>,
  flowVersions: (name: string) => req<FlowVersion[]>(`/api/flows/${encodeURIComponent(name)}/versions`),
  flowVersion: (hash: string) => req<FlowVersion>(`/api/flow-versions/${hash}`),

  settings: () => req<Settings>('/api/settings'),
  saveSettings: (s: Settings) => put('/api/settings', s) as Promise<Settings>,

  /** SSE stream of a run's events; replays everything, then follows live. */
  events(id: string, afterSeq: number, onEvent: (e: EventEnvelope) => void): () => void {
    const es = new EventSource(`/api/runs/${id}/events?after_seq=${afterSeq}`);
    es.onmessage = (m) => onEvent(JSON.parse(m.data) as EventEnvelope);
    return () => es.close();
  }
};

// Tab grouping for the runs list. Exhaustive over RunView['state'] so adding
// a state without classifying it is a type error. Active = still moving or
// waiting on someone; finished = every terminal state (completed/abandoned
// keep their env until archived, but the run itself is done).
export type RunTab = 'active' | 'finished';
export const stateTab: Record<RunView['state'], RunTab> = {
  pending: 'active',
  provisioning: 'active',
  running: 'active',
  waiting_on_human: 'active',
  paused: 'active',
  completed: 'finished',
  abandoned: 'finished',
  failed: 'finished',
  archived: 'finished'
};

export const stateColors: Record<RunView['state'], string> = {
  pending: 'var(--muted)',
  provisioning: 'var(--blue)',
  running: 'var(--blue)',
  waiting_on_human: 'var(--amber)',
  paused: 'var(--muted)',
  completed: 'var(--green)',
  abandoned: 'var(--amber)',
  failed: 'var(--red)',
  archived: 'var(--muted)'
};

export function fmtTime(ts: string): string {
  return new Date(ts).toLocaleTimeString(undefined, { hour12: false });
}

export function fmtCost(v: unknown): string {
  return typeof v === 'number' ? `$${v.toFixed(3)}` : '';
}

export function ago(ts: string): string {
  const s = Math.max(0, (Date.now() - new Date(ts).getTime()) / 1000);
  if (s < 60) return `${Math.floor(s)}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}
