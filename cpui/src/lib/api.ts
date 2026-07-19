// Typed client for the factory engine API (DESIGN.md §8). The CPUI talks
// only to this API — same origin (served by factoryd, or vite-proxied).

export interface RunView {
  run_id: string;
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
  if (res.status === 204 || res.status === 202) return undefined as T;
  return res.json() as Promise<T>;
}

const post = (path: string, body?: unknown): Promise<unknown> =>
  req(path, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) });

export const api = {
  runs: () => req<RunView[]>('/api/runs'),
  run: (id: string) => req<{ run: RunView; events: EventEnvelope[] }>(`/api/runs/${id}`),
  createTicket: (title: string, goal: string, flow?: string) =>
    post('/api/tickets', { title, goal, ...(flow ? { flow } : {}) }) as Promise<{ run_id: string }>,
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
