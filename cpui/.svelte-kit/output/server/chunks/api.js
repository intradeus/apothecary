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
  stateColors as s
};
