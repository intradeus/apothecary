async function req(path, init) {
  const res = await fetch(path, init);
  if (!res.ok) {
    let msg = `${res.status}`;
    try {
      const body = await res.json();
      if (body.error) msg = body.error;
    } catch {
    }
    throw new Error(msg);
  }
  if (res.status === 204 || res.status === 202) return void 0;
  return res.json();
}
const post = (path, body) => req(path, { method: "POST", body: body === void 0 ? void 0 : JSON.stringify(body) });
const put = (path, body) => req(path, { method: "PUT", body: JSON.stringify(body) });
const api = {
  runs: (project) => req(`/api/runs${project ? `?project=${encodeURIComponent(project)}` : ""}`),
  run: (id) => req(`/api/runs/${id}`),
  createTicket: (title, goal, projectId, flow) => post("/api/tickets", { title, goal, project_id: projectId, ...flow ? { flow } : {} }),
  abort: (id) => post(`/api/runs/${id}/abort`),
  archive: (id) => post(`/api/runs/${id}/archive`),
  log: async (id, stage, n = 65536) => {
    const res = await fetch(`/api/runs/${id}/logs/${stage}?n=${n}`);
    if (!res.ok) throw new Error(`${res.status}`);
    return res.text();
  },
  pendingPings: () => req("/api/pings?status=pending"),
  answerPing: (pingId, answer, feedback) => post(`/api/pings/${pingId}/answer`, { answer, ...feedback ? { feedback } : {} }),
  channels: () => req("/api/channels"),
  addChannel: (kind, config) => post("/api/channels", { kind, ...config }),
  deleteChannel: (id) => req(`/api/channels/${id}`, { method: "DELETE" }),
  // config plane (DESIGN2 §10)
  kinds: () => req("/api/kinds"),
  integrations: () => req("/api/integrations"),
  createIntegration: (body) => post("/api/integrations", body),
  updateIntegration: (id, body) => put(`/api/integrations/${id}`, body),
  deleteIntegration: (id) => req(`/api/integrations/${id}`, { method: "DELETE" }),
  testIntegration: (id) => post(`/api/integrations/${id}/test`),
  repos: (integrationId) => req(`/api/integrations/${integrationId}/repos`),
  models: (integrationId) => req(`/api/integrations/${integrationId}/models`),
  projects: () => req("/api/projects"),
  project: (id) => req(`/api/projects/${id}`),
  createProject: (body) => post("/api/projects", body),
  updateProject: (id, body) => put(`/api/projects/${id}`, body),
  deleteProject: (id) => req(`/api/projects/${id}`, { method: "DELETE" }),
  preflight: (id) => post(`/api/projects/${id}/preflight`),
  presets: () => req("/api/presets"),
  startEnvCheck: (projectId, keep) => post(`/api/projects/${projectId}/envcheck`, { keep }),
  envCheck: (checkId) => req(`/api/envchecks/${checkId}`),
  flows: () => req("/api/flows"),
  flow: (name) => req(`/api/flows/${encodeURIComponent(name)}`),
  createFlow: (name, yaml) => post("/api/flows", { name, yaml }),
  saveFlow: (name, yaml) => put(`/api/flows/${encodeURIComponent(name)}`, { yaml }),
  deleteFlow: (name) => req(`/api/flows/${encodeURIComponent(name)}`, { method: "DELETE" }),
  validateFlow: (yaml) => post("/api/flows/validate", { yaml }),
  flowVersions: (name) => req(`/api/flows/${encodeURIComponent(name)}/versions`),
  flowVersion: (hash) => req(`/api/flow-versions/${hash}`),
  settings: () => req("/api/settings"),
  saveSettings: (s) => put("/api/settings", s),
  /** SSE stream of a run's events; replays everything, then follows live. */
  events(id, afterSeq, onEvent) {
    const es = new EventSource(`/api/runs/${id}/events?after_seq=${afterSeq}`);
    es.onmessage = (m) => onEvent(JSON.parse(m.data));
    return () => es.close();
  }
};
const stateColors = {
  pending: "var(--muted)",
  provisioning: "var(--blue)",
  running: "var(--blue)",
  waiting_on_human: "var(--amber)",
  completed: "var(--green)",
  abandoned: "var(--amber)",
  failed: "var(--red)",
  archived: "var(--muted)"
};
function ago(ts) {
  const s = Math.max(0, (Date.now() - new Date(ts).getTime()) / 1e3);
  if (s < 60) return `${Math.floor(s)}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}
export {
  ago as a,
  api as b,
  stateColors as s
};
