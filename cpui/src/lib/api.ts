// Typed client for the apothecary engine API (DESIGN.md §8). The CPUI talks
// only to this API — same origin (served by apothecaryd, or vite-proxied).

export interface RunView {
  run_id: string;
  project_id?: string;
  state: 'pending' | 'provisioning' | 'running' | 'waiting_on_human' | 'completed' | 'abandoned' | 'failed' | 'archived';
  ticket_title: string;
  flow_name: string;
  flow_hash: string;
  current_stage?: string;
  mr_url?: string;
  env_id?: string;
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
  harness_id: string;
  repo: { url: string; locator: string; default_branch: string; agents_md_path: string };
  commands: Record<string, string>;
  workspace: { dockerfile_fragment?: string; extra_domains?: string[]; template?: string };
  default_flow: string;
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
}

export interface AgentVersion {
  hash: string;
  name: string;
  spec: { name: string; description: string; read_only: boolean; allow_bash: boolean; prompt: string };
  created_at: string;
}

export interface AgentImportReport {
  imported: string[];
  skipped: { file: string; error: string }[];
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

export interface Preset {
  name: string;
  label: string;
  lines: string[];
  domains: string[];
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
  question: string;
  context?: string;
  payload_ref?: string;
  raised_at: string;
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
  runs: (project?: string) => req<RunView[]>(`/api/runs${project ? `?project=${encodeURIComponent(project)}` : ''}`),
  run: (id: string) => req<{ run: RunView; events: EventEnvelope[] }>(`/api/runs/${id}`),
  createTicket: (title: string, goal: string, projectId: string, flow?: string) =>
    post('/api/tickets', { title, goal, project_id: projectId, ...(flow ? { flow } : {}) }) as Promise<{ run_id: string }>,
  abort: (id: string) => post(`/api/runs/${id}/abort`),
  archive: (id: string) => post(`/api/runs/${id}/archive`),
  log: async (id: string, stage: string, n = 65536): Promise<string> => {
    const res = await fetch(`/api/runs/${id}/logs/${stage}?n=${n}`);
    if (!res.ok) throw new Error(`${res.status}`);
    return res.text();
  },
  pendingPings: () => req<Ping[]>('/api/pings?status=pending'),
  answerPing: (pingId: string, answer: string, feedback?: string) =>
    post(`/api/pings/${pingId}/answer`, { answer, ...(feedback ? { feedback } : {}) }),
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
  presets: () => req<Preset[]>('/api/presets'),
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
  importAgents: (projectId: string) =>
    post(`/api/projects/${projectId}/agents/import-repo`) as Promise<AgentImportReport>,

  flows: () => req<FlowRow[]>('/api/flows'),
  flow: (name: string) => req<FlowRow>(`/api/flows/${encodeURIComponent(name)}`),
  createFlow: (name: string, yaml: string) => post('/api/flows', { name, yaml }) as Promise<FlowRow>,
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

export const stateColors: Record<RunView['state'], string> = {
  pending: 'var(--muted)',
  provisioning: 'var(--blue)',
  running: 'var(--blue)',
  waiting_on_human: 'var(--amber)',
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
