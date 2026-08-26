<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api, stateColors, fmtTime, fmtCost, type Attachment, type EgressDenial, type EgressAuditRow, type EgressPolicy, type EventEnvelope, type ResourceSample, type RunView, type Snapshot } from '$lib/api';
  import PingCard from '$lib/PingCard.svelte';
  import ResourceGraphs from '$lib/ResourceGraphs.svelte';
  import WorkspaceBadge from '$lib/WorkspaceBadge.svelte';
  import AttachFiles from '$lib/AttachFiles.svelte';
  import { isGatePing, type PingLike } from '$lib/ping';
  import { onMount } from 'svelte';

  const runId = page.params.id ?? '';

  let run = $state<RunView | null>(null);
  let events = $state<EventEnvelope[]>([]);
  let logStage = $state('');
  let logText = $state('');
  let error = $state('');
  let answering = $state(false);

  // Live projection niceties derived from the stream. Links carry a url
  // (browser), a command (copy-paste, e.g. ssh), or both. Exposed app
  // ports come from the LIVE registry (polled): the env_ready copies go
  // stale when the engine restarts, so live exposures replace any 'app'
  // links the event carried.
  let livePorts = $state<{ port: number; url: string }[]>([]);
  // Runtime-discovered listeners not yet exposed (the shim's in-workspace
  // scan + engine-side compose-sibling scan): one-click exposes them.
  let observedPorts = $state<{ port: number; process?: string; service?: string }[]>([]);
  // code-server has its own "Open editor" button — don't also list its port
  // as a generic app link/chip.
  const CODE_SERVER_PORT = 8471;
  const links = $derived.by(() => {
    const fromEvent =
      (events.findLast((e) => e.type === 'env_ready')?.payload.links as
        | { kind: string; url: string; command?: string }[]
        | undefined) ?? [];
    const base = livePorts.length ? fromEvent.filter((l) => l.kind !== 'app') : fromEvent;
    return [
      ...base,
      ...livePorts
        .filter((p) => p.port !== CODE_SERVER_PORT)
        .map((p) => ({ kind: `app :${p.port}`, url: p.url, command: undefined }))
    ];
  });
  let copiedLink = $state('');
  async function copyCommand(l: { kind: string; command?: string }) {
    if (!l.command) return;
    await navigator.clipboard.writeText(l.command);
    copiedLink = l.kind;
    setTimeout(() => (copiedLink = ''), 1500);
  }
  // findLast ⇒ the LATEST unanswered ping: the operator answers a run's
  // pings newest-first, and each answer reveals the next-earlier one.
  const pendingPing = $derived.by(() => {
    const answered = new Set(events.filter((e) => e.type === 'ping_answered').map((e) => e.payload.ping_id));
    return events.findLast((e) => e.type === 'ping_raised' && !answered.has(e.payload.ping_id));
  });
  const pendingCount = $derived.by(() => {
    const answered = new Set(events.filter((e) => e.type === 'ping_answered').map((e) => e.payload.ping_id));
    return events.filter((e) => e.type === 'ping_raised' && !answered.has(e.payload.ping_id)).length;
  });
  // Map the ping_raised event payload onto the shared PingCard shape (the
  // inbox feeds it the durable Ping projection instead).
  const pendingPingView = $derived<PingLike | null>(
    pendingPing
      ? {
          ping_id: pendingPing.payload.ping_id as string,
          type: pendingPing.payload.type as string,
          question: pendingPing.payload.question as string,
          context: pendingPing.payload.context as string | undefined,
          payload_ref: pendingPing.payload.payload_ref as string | undefined,
          options: pendingPing.payload.options as string[] | undefined
        }
      : null
  );
  const totals = $derived.by(() => {
    let cost = 0, turns = 0;
    for (const e of events) {
      // A stage that paused on a question banks what it spent up to there;
      // the run's total is every leg, not just the ones that completed.
      if (e.type === 'stage_completed' || e.type === 'stage_paused') {
        const u = e.payload.usage as { cost_usd?: number } | undefined;
        cost += u?.cost_usd ?? 0;
        turns += (e.payload.turns_used as number | undefined) ?? 0;
      }
    }
    return { cost, turns };
  });
  const terminal = $derived(run !== null && ['completed', 'abandoned', 'failed', 'archived'].includes(run.state));

  // Live "is it actually doing something" feedback (working card):
  // current stage + ticking elapsed from the latest stage_started, plus a
  // polled tail of the workspace's exec output.
  let now = $state(Date.now());
  let liveTail = $state<string | null>(null);
  let denials = $state<EgressDenial[]>([]);
  let dismissedDenials = $state<Set<string>>(new Set());
  const newDenials = $derived(denials.filter((d) => !dismissedDenials.has(d.host)));
  let audit = $state<EgressAuditRow[]>([]);
  // The firewall policy in force right now — the rule toggles render from it.
  // null until fetched (or when the run has no live gateway), in which case
  // the rule column falls back to the row's historical verdict.
  let policy = $state<EgressPolicy | null>(null);
  let showAudit = $state(false);
  let auditVerdict = $state(''); // '' | 'allow' | 'deny'
  let auditHost = $state('');
  let toggling = $state<Record<string, boolean>>({});
  let showTail = $state(true);
  let tailEl = $state<HTMLPreElement | null>(null);
  // Workspace resource graphs (cpu/ram/net), from the engine's ~10s sampler.
  // Fetched only while the section is open, like the egress audit.
  let showResources = $state(false);
  let resources = $state<ResourceSample[]>([]);
  async function toggleResources() {
    showResources = !showResources;
    if (showResources) await refreshResources();
  }
  async function refreshResources() {
    try {
      resources = (await api.runResources(runId)).samples;
    } catch {
      /* transient */
    }
  }
  const currentStage = $derived.by(() => {
    const started = events.findLast((e) => e.type === 'stage_started');
    if (!started) return null;
    // A completion/failure for that stage after it means nothing is running —
    // and so does a pause, until the matching resume starts it working again.
    const idx = events.lastIndexOf(started);
    const last = events
      .slice(idx + 1)
      .findLast(
        (e) =>
          ['stage_completed', 'stage_failed', 'stage_paused', 'stage_resumed'].includes(e.type) &&
          e.payload.stage_id === started.payload.stage_id
      );
    if (!last || last.type === 'stage_resumed') return started;
    return null;
  });
  // Where the elapsed clock starts: a stage that paused on a question and
  // resumed is working again as of the resume, not as of the stage start —
  // the human's thinking time is not the agent's runtime.
  const workingSince = $derived.by(() => {
    if (!currentStage) return null;
    const idx = events.lastIndexOf(currentStage);
    const resumed = events
      .slice(idx + 1)
      .findLast((e) => e.type === 'stage_resumed' && e.payload.stage_id === currentStage.payload.stage_id);
    return (resumed ?? currentStage).ts;
  });
  const elapsed = $derived.by(() => {
    if (!workingSince) return '';
    const ms = now - new Date(workingSince).getTime();
    if (ms < 0) return '0s';
    const s = Math.floor(ms / 1000);
    return s >= 60 ? `${Math.floor(s / 60)}m ${s % 60}s` : `${s}s`;
  });

  // --- files ----------------------------------------------------------------
  // Everything this run put on the record: the engine's snapshots (each stage
  // attempt's declared outputs + its log, captured on success AND failure)
  // and the human's attachments. Snapshots outlive the run, so this list
  // stays complete long after the workspace is destroyed.
  let snapshots = $state<Snapshot[]>([]);
  let attachments = $state<Attachment[]>([]);
  let fileKind = $state<'' | 'output' | 'log' | 'attachment'>('');
  let showFiles = $state(true);

  interface FileRow {
    id: string;
    filename: string;
    kind: '' | 'output' | 'log' | 'attachment';
    snapshot: boolean; // which byte route serves it
    badge: string; // what it is: the declared output's role, a log, an upload
    where: string; // the attempt (or the human moment) that produced it
    href?: string; // where `where` points, when it points anywhere
    size: number;
    at: string;
  }

  const fileTotal = $derived(snapshots.length + attachments.length);

  const fileRows = $derived.by<FileRow[]>(() => {
    const rows: FileRow[] = snapshots.map((sn) => ({
      id: sn.id,
      filename: sn.filename,
      kind: sn.kind === 'log' ? 'log' : 'output',
      snapshot: true,
      badge: sn.kind === 'log' ? 'log' : `output${sn.name ? ` · ${sn.name}` : ''}`,
      where: `${sn.stage_id} · attempt ${sn.iteration ?? 0}`,
      size: sn.size,
      at: sn.created_at
    }));
    for (const a of attachments) {
      rows.push({
        id: a.id,
        filename: a.filename,
        kind: 'attachment',
        snapshot: false,
        badge: 'you attached',
        where: a.ping_id ? 'a ping answer' : 'the run goal',
        href: a.ping_id ? `/pings/${a.ping_id}` : undefined,
        size: a.size,
        at: a.created_at
      });
    }
    rows.sort((a, b) => (a.at < b.at ? -1 : a.at > b.at ? 1 : 0));
    return fileKind ? rows.filter((r) => r.kind === fileKind) : rows;
  });

  async function refreshFiles() {
    try {
      const f = await api.runFiles(runId);
      snapshots = f.snapshots;
      attachments = f.attachments;
    } catch {
      /* transient — keep what we have */
    }
  }

  const refreshRun = async () => {
    try {
      run = (await api.run(runId)).run;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  };

  onMount(() => {
    const tick = setInterval(() => (now = Date.now()), 1000);
    const poll = setInterval(async () => {
      if (terminal || document.hidden) return;
      try {
        const t = await api.workspaceLog(runId);
        liveTail = t;
        if (t !== null && showTail && tailEl) {
          requestAnimationFrame(() => tailEl && (tailEl.scrollTop = tailEl.scrollHeight));
        }
        denials = await api.egressDenials(runId);
        if (showAudit) await refreshAudit();
        if (showResources) await refreshResources();
        const pr = await api.runPorts(runId);
        livePorts = pr.ports;
        observedPorts = pr.observed ?? [];
      } catch {
        /* transient; keep last tail */
      }
    }, 2500);
    refreshRun();
    refreshFiles();
    const stop = api.events(runId, 0, (ev) => {
      events = [...events, ev];
      // State-changing events refresh the header chip.
      if (ev.type.startsWith('run_') || ev.type.startsWith('ping_') || ev.type.startsWith('env_')) refreshRun();
      // A capture (or an answered ping, which may carry attachments) adds a
      // row: the event carries the reference, the list carries the metadata.
      if (ev.type === 'snapshot_captured' || ev.type === 'ping_answered') refreshFiles();
    });
    return () => {
      clearInterval(tick);
      clearInterval(poll);
      stop();
    };
  });

  async function answer(ans: string, note?: string, attachmentIds?: string[]) {
    if (!pendingPing) return;
    answering = true;
    error = '';
    try {
      await api.answerPing(pendingPing.payload.ping_id as string, ans, note?.trim() || undefined, attachmentIds);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      answering = false;
    }
  }

  async function showLog(stage: string) {
    if (logStage === stage) {
      logStage = '';
      return;
    }
    logStage = stage;
    logText = 'loading…';
    try {
      logText = await api.log(runId, stage);
    } catch (e) {
      logText = `no log: ${e instanceof Error ? e.message : e}`;
    }
  }

  // Dismiss the new-denial banner without changing the rule (stays denied).
  function dismissDenials() {
    dismissedDenials = new Set(denials.map((d) => d.host));
  }

  // matchesList mirrors the gateway's hostMatches: exact entry, or a
  // "*.suffix" wildcard matched by suffix (never the bare apex itself).
  function matchesList(host: string, list: string[]): boolean {
    const h = host.toLowerCase();
    return list.some((d) => {
      const e = d.toLowerCase();
      return e.startsWith('*.') ? h.endsWith(e.slice(1)) : h === e;
    });
  }

  // The CURRENT rule for a host, mirroring the gateway's verdict(): a
  // force-block wins in both modes, then mode + list membership decide.
  // null while the policy is unknown (not yet fetched / no gateway).
  function ruleFor(host: string): 'allow' | 'deny' | null {
    if (!policy) return null;
    if (matchesList(host, policy.blocked_domains)) return 'deny';
    const m = matchesList(host, policy.domains);
    if (policy.mode === 'deny') return m ? 'deny' : 'allow';
    return m ? 'allow' : 'deny';
  }

  // Live-toggle a host's rule (allow ⇄ block). Persists to the project. The
  // audit rows are history and deliberately untouched — the rule column
  // re-renders from the refreshed policy.
  async function toggleRule(host: string, allow: boolean) {
    toggling[host] = true;
    error = '';
    try {
      await api.egressRule(runId, host, allow);
      policy = await api.egressPolicy(runId);
      denials = await api.egressDenials(runId);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      toggling[host] = false;
    }
  }

  async function refreshAudit() {
    try {
      audit = await api.egressAudit(runId, {
        verdict: auditVerdict || undefined,
        host: auditHost.trim() || undefined
      });
      policy = await api.egressPolicy(runId);
    } catch {
      /* transient */
    }
  }
  async function toggleAudit() {
    showAudit = !showAudit;
    if (showAudit) await refreshAudit();
  }
  function fmtBytes(n: number): string {
    if (!n) return '0';
    const u = ['B', 'KB', 'MB', 'GB'];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) {
      n /= 1024;
      i++;
    }
    return `${n < 10 && i > 0 ? n.toFixed(1) : Math.round(n)}${u[i]}`;
  }

  // Expose a workspace port on demand (the "browse the app" path); the
  // resulting link joins the row via the livePorts poll.
  let exposeInput = $state('');
  let exposing = $state(false);
  async function doExpose(port: number) {
    if (!port || port < 1 || port > 65535) return;
    exposing = true;
    error = '';
    try {
      const res = await api.exposePort(runId, port);
      const pr = await api.runPorts(runId);
      livePorts = pr.ports;
      observedPorts = pr.observed ?? []; // the now-exposed port drops off
      window.open(res.url, '_blank');
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      exposing = false;
    }
  }
  async function exposePort() {
    const port = parseInt(exposeInput, 10);
    if (!port || port < 1 || port > 65535) return;
    await doExpose(port);
    exposeInput = '';
  }

  let resuming = $state(false);
  async function resume() {
    resuming = true;
    error = '';
    try {
      await api.resume(runId);
      // Same run, now continuing — hard reload so the live streams re-init
      // (this page keys off the route param captured at mount).
      location.reload();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
      resuming = false;
    }
  }

  // Rebuild recreates the container in place; the request blocks for the whole
  // build + bootstrap, so a dedicated flag drives the button's busy state. The
  // rebuilding badge + workspace-log tail show live progress meanwhile.
  let rebuilding = $state(false);
  async function rebuild() {
    rebuilding = true;
    error = '';
    try {
      await api.rebuild(runId);
      await refreshRun();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      rebuilding = false;
    }
  }

  // Lazily start code-server + open it. First open blocks a few seconds while
  // code-server boots; opened in a new tab so this page keeps streaming.
  let opening = $state(false);
  async function openEditor() {
    opening = true;
    error = '';
    try {
      const { url } = await api.openEditor(runId);
      window.open(url, '_blank');
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      opening = false;
    }
  }

  // Restart opens a small form first: retry-after-failure is exactly when a
  // human wants to steer the next attempt (direction + fresh files).
  let showRestart = $state(false);
  let restartGoal = $state('');
  let restartFlow = $state('');
  let restartFiles = $state<File[]>([]);
  let restarting = $state(false);

  async function restart() {
    error = '';
    restarting = true;
    try {
      let attachmentIds: string[] | undefined;
      if (restartFiles.length) attachmentIds = (await api.uploadAttachments(restartFiles)).map((a) => a.id);
      const { run_id } = await api.restart(runId, {
        flow: restartFlow.trim() || undefined,
        goal: restartGoal.trim() || undefined,
        attachment_ids: attachmentIds
      });
      await goto(`/runs/${run_id}`);
      // Full reload semantics: this page keys its state off the route param
      // captured at mount, so navigate hard to re-init streams.
      location.reload();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      restarting = false;
    }
  }

  async function act(fn: () => Promise<unknown>) {
    error = '';
    try {
      await fn();
      await refreshRun();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  // The provisioning log (clone + container build + bootstrap) is filed under
  // a pseudo-stage id the flow grammar can't produce (stage ids must start
  // with a letter) — served by the same /logs/{stage} endpoint.
  const PROVISION_STAGE = '_provision';
  // Show the provisioning log on the env rows once it exists: env_ready /
  // env_failed always (the engine captures it right before appending them);
  // env_requested only for a run that ended with neither (aborted mid-build —
  // that row is then the only place to hang the log on).
  const envConcluded = $derived(events.some((e) => e.type === 'env_ready' || e.type === 'env_failed'));
  function hasLog(e: EventEnvelope, stage?: string): boolean {
    if (!stage) return false;
    if (e.type === 'stage_completed' || e.type === 'stage_failed' || e.type === 'stage_paused') return true;
    if (e.type === 'env_ready' || e.type === 'env_failed') return true;
    return e.type === 'env_requested' && terminal && !envConcluded;
  }

  function describe(e: EventEnvelope): { text: string; cls: string; stage?: string; snapshot?: string } {
    const p = e.payload;
    switch (e.type) {
      case 'snapshot_captured': {
        if (p.kind === 'ping_file') {
          return { text: `📄 captured ${p.path} for a ping (as shown to you)`, cls: '', snapshot: p.snapshot_id as string };
        }
        const what = p.kind === 'log' ? 'log' : `output ${p.name || ''}`.trim();
        const where = p.path ? ` — ${p.path}` : '';
        return {
          text: `📄 ${p.stage_id} (attempt ${p.iteration ?? 0}) captured ${what}${where}`,
          cls: '',
          snapshot: p.snapshot_id as string
        };
      }
      case 'run_created': return { text: `run created — “${p.ticket_title}” (flow ${p.flow_name})`, cls: 'info' };
      case 'env_requested': return { text: 'provisioning workspace…', cls: '', stage: PROVISION_STAGE };
      case 'env_ready': return { text: `workspace ready (${p.env_id})`, cls: 'ok', stage: PROVISION_STAGE };
      case 'env_failed': return { text: `environment failed: ${p.error}`, cls: 'bad', stage: PROVISION_STAGE };
      case 'stage_started': return { text: `${p.stage_id} started (attempt ${p.iteration})${p.model ? ` · ${p.model}` : ''}`, cls: 'info', stage: p.stage_id as string };
      case 'stage_completed': {
        const u = p.usage as { cost_usd?: number } | undefined;
        const bits = [
          p.turns_used ? `${p.turns_used} turns` : '',
          u?.cost_usd ? fmtCost(u.cost_usd) : '',
          `${Math.round((p.duration_ms as number) / 1000)}s`
        ].filter(Boolean).join(' · ');
        return { text: `${p.stage_id} completed (${bits})`, cls: 'ok', stage: p.stage_id as string };
      }
      case 'stage_paused': {
        const u = p.usage as { cost_usd?: number } | undefined;
        const spent = [p.turns_used ? `${p.turns_used} turns` : '', u?.cost_usd ? fmtCost(u.cost_usd) : '']
          .filter(Boolean).join(' · ');
        const why = p.reason === 'ask_human' ? 'waiting on its own question' : String(p.reason ?? '');
        return {
          text: `⏸ ${p.stage_id} paused — ${why}${spent ? ` (${spent} so far)` : ''}`,
          cls: 'warn',
          stage: p.stage_id as string
        };
      }
      case 'stage_resumed':
        return {
          text: `▶ ${p.stage_id} resumed — ${p.reason === 'engine_restart' ? 'engine restarted, picking the session back up' : 'the answer landed'}`,
          cls: 'info',
          stage: p.stage_id as string
        };
      case 'stage_failed': return { text: `${p.stage_id} failed: ${p.reason}${p.error ? ` — ${String(p.error).slice(0, 200)}` : ''}`, cls: 'bad', stage: p.stage_id as string };
      case 'edge_taken': return { text: `↩ ${p.edge}${p.depth ? ` (then #${p.depth})` : ''} ${p.from} → ${p.to} (${p.count}/${p.max === -1 ? '∞' : p.max})`, cls: 'warn' };
      case 'ping_raised': return { text: `ping [${p.type}]: ${p.question}`, cls: 'warn' };
      case 'ping_answered': return { text: `answered: ${p.answer}${p.feedback ? ` — ${p.feedback}` : ''}`, cls: 'ok' };
      case 'verdict_received': return { text: p.approve ? 'review verdict: APPROVE' : `review verdict: REJECT (${p.n_comments} comments)`, cls: p.approve ? 'ok' : 'warn' };
      case 'mr_created': return { text: `merge request created: ${p.mr_url}`, cls: 'ok' };
      case 'run_completed': return { text: `run completed — ${fmtCost(p.total_cost_usd)} · ${p.total_turns} turns · ${p.unwanted_pings} unwanted pings`, cls: 'ok' };
      case 'run_abandoned': return { text: `abandoned at ${p.stalled_at}: ${p.reason}`, cls: 'bad' };
      case 'run_failed': return { text: `run failed: ${p.reason}`, cls: 'bad' };
      case 'run_paused':
        // Parked by the engine on a human is not the same as paused by one:
        // the first releases the slot and comes back on its own.
        return p.reason === 'waiting_on_human'
          ? { text: `⏸ parked at ${p.stage_id} — waiting for you (the run released its slot)`, cls: '' }
          : { text: `⏸ paused${p.stage_id ? ` at ${p.stage_id}` : ''}`, cls: '' };
      case 'run_unpaused':
        return p.reason === 'answered'
          ? { text: '▶ picked back up — your answer landed', cls: 'info' }
          : { text: '▶ resumed (play)', cls: 'info' };
      case 'run_archived': return { text: 'archived', cls: '' };
      case 'env_destroyed': return { text: 'workspace destroyed', cls: '' };
      default: return { text: e.type, cls: '' };
    }
  }

</script>

{#if run}
  <div class="row spread" style="margin-bottom:6px">
    <h1 style="margin:0">{run.ticket_title}</h1>
    <span class="chip"><span class="dot" style="background:{stateColors[run.state]}"></span>{run.state}</span>
    <WorkspaceBadge state={run.state} envId={run.env_id} envStatus={run.env_status} />
  </div>
  <div class="row small muted" style="margin-bottom:16px">
    <span class="mono">{run.run_id}</span>
    {#if run.ticket_id}<a href={`/tickets/${run.ticket_id}`} title="This run's ticket">ticket ↗</a>{/if}
    <span>flow {run.flow_name}</span>
    {#if run.branch}<span title="Git branch for this run">branch <span class="mono">{run.branch}</span></span>{/if}
    {#if run.source_branch}<span title="Branch this run forked off from">from <span class="mono">{run.source_branch}</span></span>{/if}
    {#if run.env_id}<span title="Workspace / container name">workspace <span class="mono">{run.env_id}</span></span>{/if}
    {#if totals.turns > 0}<span>{fmtCost(totals.cost)} · {totals.turns} turns</span>{/if}
    {#each links as l (l.kind + (l.url || l.command))}
      {#if l.url}
        <a href={l.url} target="_blank" rel="noreferrer">{l.kind} ↗</a>
      {:else if l.command}
        <button class="linklike" title={`Copy: ${l.command}`} onclick={() => copyCommand(l)}>
          {copiedLink === l.kind ? 'copied ✓' : `${l.kind} ⧉`}
        </button>
      {/if}
    {/each}
    {#if run.mr_url}<a href={run.mr_url} target="_blank" rel="noreferrer">merge request ↗</a>{/if}
    {#if run.env_id}
      <button class="linklike" onclick={openEditor} disabled={opening}
        title="Open VS Code (code-server) in the browser on this workspace — the repo at $HOME/repo, the agent's commits, an in-container terminal.">
        {opening ? 'opening editor…' : 'Open editor ↗'}
      </button>
    {/if}
    {#each observedPorts as p (p.port)}
      <button
        class="linklike"
        disabled={exposing}
        onclick={() => doExpose(p.port)}
        title={`Detected listener${p.service ? ` in service ${p.service}` : p.process ? ` (${p.process})` : ''} — expose and open`}
      >
        :{p.port}{p.service ? ` ${p.service}` : p.process ? ` ${p.process}` : ''} ↗
      </button>
    {/each}
    {#if !terminal && run.env_id}
      <span class="row" style="gap:4px" title="Publish a port the app inside the workspace listens on — opens it on an engine-host URL (per-run dynamic port, safe across concurrent runs)">
        <input
          class="mono small"
          style="width:5.5em"
          placeholder="port"
          bind:value={exposeInput}
          onkeydown={(e) => e.key === 'Enter' && exposePort()}
        />
        <button class="linklike" disabled={exposing || !exposeInput} onclick={exposePort}>
          {exposing ? 'exposing…' : 'expose ↗'}
        </button>
      </span>
    {/if}
    {#if !terminal}
      {#if run.state === 'paused'}
        <button class="primary" onclick={() => act(() => api.play(runId))}
          title="Continue where it left off — the in-flight stage re-enters (agent sessions resume); retry budgets are untouched">
          ▶ Play
        </button>
      {:else if run.state !== 'pending'}
        <button onclick={() => act(() => api.pause(runId))}
          title="Park the run: the current attempt is stopped, the workspace stays up for inspection, and nothing moves until Play">
          ⏸ Pause
        </button>
      {/if}
      <button class="danger" onclick={() => act(() => api.abort(runId))}>Abort</button>
    {:else}
      {#if run.state === 'abandoned' || run.state === 'failed'}
        <button class="primary" onclick={resume} disabled={resuming}
          title="Re-run the failed stage in the same workspace — keeps the plan, build and commits; only retries what failed">
          {resuming ? 'Resuming…' : `Try again${run.current_stage ? ` (${run.current_stage})` : ''}`}
        </button>
      {/if}
      <button onclick={() => (showRestart = !showRestart)}
        title="Start over as a fresh run: same ticket and project — new workspace, new branch. You can add direction for the new attempt.">
        {showRestart ? 'Cancel restart' : 'Restart'}
      </button>
      {#if run.env_id}
        <button onclick={rebuild} disabled={rebuilding}
          title="Recreate this run's container in place — keeps the run and its commits, re-runs setup (e.g. to apply devcontainer.json changes). Same run, same branch.">
          {rebuilding ? 'Rebuilding…' : 'Rebuild workspace'}
        </button>
      {/if}
      {#if run.state !== 'archived'}
        <button onclick={() => act(() => api.archive(runId))}>Archive</button>
      {/if}
    {/if}
  </div>

  {#if showRestart}
    <div class="card">
      <h2 style="margin:0 0 10px">Restart as a new run</h2>
      <label for="rs-goal">Direction for the new attempt <span class="muted">— optional; added to the ticket's goal</span></label>
      <textarea id="rs-goal" bind:value={restartGoal} rows="2"
        placeholder="e.g. This attempt broke the router — use the existing middleware."></textarea>
      <AttachFiles bind:files={restartFiles} label="Attach files for the new run" />
      <div class="row" style="gap:8px; align-items:flex-end; margin-top:10px">
        <div style="flex:1">
          <label for="rs-flow">Flow <span class="muted">— empty = same as this run ({run.flow_name})</span></label>
          <input id="rs-flow" bind:value={restartFlow} placeholder={run.flow_name} class="mono" />
        </div>
        <button class="primary" onclick={restart} disabled={restarting}>
          {restarting ? 'Restarting…' : '↻ Restart'}
        </button>
      </div>
    </div>
  {/if}

  {#if run.run_goal}
    <div class="card" style="border-color:var(--blue)">
      <strong class="small">Direction for this run</strong>
      <div style="white-space:pre-wrap; margin-top:4px">{run.run_goal}</div>
    </div>
  {/if}

  {#if run.state === 'paused'}
    <div class="card" style="border-color:var(--muted)">
      <strong>⏸ Paused</strong>
      <span class="muted small">
        — workspace kept for inspection{run.current_stage ? `; will re-enter “${run.current_stage}”` : ''}.
        Play continues, Abort ends the run.
      </span>
    </div>
  {/if}

  {#if !terminal && run.state !== 'pending' && run.state !== 'paused'}
    <div class="card working">
      <div class="row spread">
        <div>
          <span class="pulse"></span>
          {#if currentStage}
            <strong class="mono">{currentStage.payload.stage_id}</strong>
            <span class="muted">running for {elapsed}</span>
            {#if currentStage.payload.agent}<span class="muted small"> · agent {currentStage.payload.agent}</span>{/if}
          {:else}
            <span class="muted">between stages…</span>
          {/if}
        </div>
        <button onclick={() => (showTail = !showTail)}>{showTail ? 'Hide output' : 'Live output'}</button>
      </div>
      {#if newDenials.length > 0}
        <div class="deny-banner">
          <span
            >⚠ egress blocked{newDenials.length > 1 ? ` ×${newDenials.length}` : ''}:
            <span class="mono">{newDenials.slice(0, 3).map((d) => d.host).join(', ')}</span
            >{newDenials.length > 3 ? ` +${newDenials.length - 3} more` : ''}</span>
          <span class="deny-banner-actions">
            <button
              class="linklike"
              onclick={() => {
                showAudit = true;
                auditVerdict = 'deny';
                refreshAudit();
              }}>review &amp; allow</button>
            <button class="linklike" title="Dismiss (leave blocked)" onclick={dismissDenials}
              >✕</button>
          </span>
        </div>
      {/if}
      <div class="audit">
        <button class="linklike" onclick={toggleAudit}>
          {showAudit ? '▾' : '▸'} Egress audit{audit.length ? ` (${audit.length})` : ''}
        </button>
        {#if showAudit}
          <div class="audit-controls">
            <select bind:value={auditVerdict} onchange={refreshAudit}>
              <option value="">all</option>
              <option value="allow">allowed</option>
              <option value="deny">denied</option>
            </select>
            <input
              placeholder="filter host…"
              bind:value={auditHost}
              oninput={refreshAudit}
              class="mono small" />
            <span class="muted small">every host this workspace reached or was blocked from</span>
          </div>
          {#if audit.length === 0}
            <p class="muted small" style="margin:6px 0 0">No egress recorded yet for this filter.</p>
          {:else}
            <div class="audit-scroll">
              <table class="audit-table mono small">
                <thead>
                  <tr><th>host</th><th>seen</th><th>rule now</th><th>proto</th><th class="num">×</th><th class="num">out</th><th class="num">in</th><th>last</th></tr>
                </thead>
                <tbody>
                  {#each audit as r (r.host + ':' + r.port + ':' + r.verdict)}
                    {@const rule = ruleFor(r.host) ?? r.verdict}
                    <tr>
                      <td>{r.host}{r.port && r.port !== 443 ? `:${r.port}` : ''}</td>
                      <td>
                        <span class="verdict hist {r.verdict}" title="What happened to this traffic when it occurred — history, never changed by rule edits">
                          {r.verdict === 'allow' ? 'allowed' : 'blocked'}
                        </span>
                      </td>
                      <td>
                        <button
                          class="verdict {rule}"
                          disabled={toggling[r.host]}
                          title={rule === 'allow' ? 'The firewall allows this host now — click to block it' : 'The firewall blocks this host now — click to allow it'}
                          onclick={() => toggleRule(r.host, rule !== 'allow')}>
                          {toggling[r.host] ? '…' : rule === 'allow' ? 'allowed' : 'blocked'}
                        </button>
                      </td>
                      <td class="muted">{r.proto}</td>
                      <td class="num">{r.count}</td>
                      <td class="num">{fmtBytes(r.bytes_out)}</td>
                      <td class="num">{fmtBytes(r.bytes_in)}</td>
                      <td class="muted">{fmtTime(r.last_seen)}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
              <p class="muted small" style="margin:6px 0 0">
                “seen” is what happened to past traffic; “rule now” is what the firewall does today —
                click it to allow/block the host live (applies in ~1s, persists to the project).
              </p>
            </div>
          {/if}
        {/if}
      </div>
      <div class="audit">
        <button class="linklike" onclick={toggleResources}>
          {showResources ? '▾' : '▸'} Resources
        </button>
        {#if showResources}
          <ResourceGraphs samples={resources} />
        {/if}
      </div>
      {#if showTail}
        {#if liveTail}
          <pre class="mono small tail" bind:this={tailEl}>{liveTail}</pre>
        {:else}
          <p class="muted small" style="margin:8px 0 0">
            No live output yet — it appears here as the workspace works (bootstrap, commands, agent
            events). Some provisioners don't support live output.
          </p>
        {/if}
      {/if}
    </div>
  {/if}

  {#if pendingPingView}
    {#key pendingPingView.ping_id}
      <div class="card" style="border-color:var(--amber)">
        <div class="pingtype {isGatePing(pendingPingView.type) ? 'wanted' : 'unwanted'}">{pendingPingView.type}</div>
        {#if pendingCount > 1}
          <div class="small" style="color:var(--amber);opacity:0.85;margin:6px 0 2px">
            answer this first — {pendingCount - 1} earlier ping{pendingCount - 1 > 1 ? 's' : ''} queued for this run
          </div>
        {/if}
        <PingCard ping={pendingPingView} busy={answering} onanswer={answer} />
      </div>
    {/key}
  {/if}

  {#if error}<div class="error">{error}</div>{/if}

  <div class="card">
    <div class="row spread">
      <h2 style="margin:0">
        <button class="linklike" onclick={() => (showFiles = !showFiles)}>
          {showFiles ? '▾' : '▸'} Files{fileTotal ? ` (${fileTotal})` : ''}
        </button>
      </h2>
      {#if showFiles && fileTotal > 0}
        <select bind:value={fileKind} class="small" style="width:auto">
          <option value="">everything</option>
          <option value="output">outputs</option>
          <option value="log">logs</option>
          <option value="attachment">your attachments</option>
        </select>
      {/if}
    </div>
    {#if showFiles}
      {#if fileRows.length === 0}
        <p class="muted small" style="margin:8px 0 0">
          {fileTotal === 0
            ? 'Nothing on the record yet — each stage attempt files its declared outputs and its log here as it finishes, whether it passed or failed.'
            : 'No files of that kind.'}
        </p>
      {:else}
        <div class="audit-scroll" style="margin-top:8px">
          <table class="audit-table mono small">
            <thead>
              <tr><th>name</th><th>kind</th><th>from</th><th class="num">size</th><th>captured</th><th></th></tr>
            </thead>
            <tbody>
              {#each fileRows as f (f.id)}
                {@const url = f.snapshot ? api.snapshotUrl(f.id) : api.attachmentUrl(f.id)}
                {@const dl = f.snapshot ? api.snapshotUrl(f.id, true) : api.attachmentUrl(f.id, true)}
                <tr>
                  <td>
                    <a href={url} target="_blank" rel="noreferrer">{f.filename}</a>
                  </td>
                  <td class="muted">{f.badge}</td>
                  <td class="muted">
                    {#if f.href}<a href={f.href}>{f.where}</a>{:else}{f.where}{/if}
                  </td>
                  <td class="num">{fmtBytes(f.size)}</td>
                  <td class="muted">{f.at ? fmtTime(f.at) : '—'}</td>
                  <td>
                    <a href={dl} title="Download {f.filename}">↓</a>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        <p class="muted small" style="margin:6px 0 0">
          Snapshots are kept after the workspace is destroyed — this stays readable for a finished
          run.
        </p>
      {/if}
    {/if}
  </div>

  <div class="card">
    <h2>Timeline</h2>
    <div class="timeline">
      {#each events as e (e.seq)}
        {@const d = describe(e)}
        <div class="tl-item {d.cls}">
          <span class="muted mono small">{fmtTime(e.ts)}</span>
          {d.text}
          {#if hasLog(e, d.stage)}
            <button class="small" style="padding:1px 8px;margin-left:6px" onclick={() => showLog(d.stage!)}>log</button>
          {/if}
          {#if d.snapshot}
            <a class="small" style="margin-left:6px" href={api.snapshotUrl(d.snapshot)} target="_blank" rel="noreferrer">open ↗</a>
          {/if}
        </div>
        {#if logStage && d.stage === logStage && hasLog(e, d.stage)}
          <pre class="log">{logText}</pre>
        {/if}
      {/each}
      {#if events.length === 0}<div class="muted">waiting for events…</div>{/if}
    </div>
  </div>
{:else}
  <div class="empty">{error || 'Loading…'}</div>
{/if}

<style>
  .linklike {
    background: none;
    border: none;
    padding: 0;
    color: var(--accent, #6ea8fe);
    cursor: pointer;
    font: inherit;
  }
  .linklike:hover { text-decoration: underline; border: none; }
  .working .pulse { display:inline-block; width:8px; height:8px; border-radius:50%; background: var(--green, #4caf7d); margin-right:8px; animation: pulse 1.6s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity: 0.25; } }
  .tail { max-height:280px; overflow-y:auto; white-space:pre-wrap; margin:10px 0 0; border:1px solid var(--border, #333); border-radius:6px; padding:8px; }
  .audit { margin-top: 10px; }
  .audit-controls { display:flex; gap:8px; align-items:center; flex-wrap:wrap; margin:8px 0; }
  .audit-controls input { flex: 1; min-width: 120px; padding:2px 6px; }
  .audit-scroll { max-height: 320px; overflow:auto; border:1px solid var(--border, #333); border-radius:6px; }
  .audit-table { width:100%; border-collapse: collapse; }
  .audit-table th, .audit-table td { text-align:left; padding:3px 8px; border-bottom:1px solid var(--border, #2a2a2a); white-space:nowrap; }
  .audit-table thead th { position:sticky; top:0; background: var(--bg, #1a1a1a); color: var(--muted, #888); font-weight:normal; }
  .audit-table td.num, .audit-table th.num { text-align:right; }
  .verdict { padding:1px 7px; border-radius:4px; font-size:11px; border:1px solid transparent; cursor:pointer; font:inherit; }
  .verdict:hover:not(:disabled) { border-color: currentColor; }
  .verdict:disabled { cursor: default; opacity: 0.6; }
  .verdict.allow { color: var(--green, #4caf7d); background: color-mix(in srgb, var(--green, #4caf7d) 12%, transparent); }
  .verdict.deny  { color: var(--amber, #e0af68); background: color-mix(in srgb, var(--amber, #e0af68) 14%, transparent); }
  .verdict.hist { cursor: default; opacity: 0.75; background: transparent; }
  .verdict.hist:hover { border-color: transparent; }
  .deny-banner {
    display:flex; align-items:center; justify-content:space-between; gap:10px;
    margin-top:10px; padding:5px 10px; font-size:12px;
    border:1px solid var(--amber, #e0af68); border-radius:6px;
    background: color-mix(in srgb, var(--amber, #e0af68) 8%, transparent);
  }
  .deny-banner-actions { display:flex; gap:10px; align-items:center; white-space:nowrap; }
</style>
