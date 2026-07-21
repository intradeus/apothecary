<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api, stateColors, fmtTime, fmtCost, type EgressDenial, type EventEnvelope, type RunView } from '$lib/api';
  import PingCard from '$lib/PingCard.svelte';
  import { GATE_TYPES, type PingLike } from '$lib/ping';
  import { onMount } from 'svelte';

  const runId = page.params.id ?? '';

  let run = $state<RunView | null>(null);
  let events = $state<EventEnvelope[]>([]);
  let logStage = $state('');
  let logText = $state('');
  let error = $state('');
  let answering = $state(false);

  // Live projection niceties derived from the stream.
  const links = $derived(
    (events.findLast((e) => e.type === 'env_ready')?.payload.links as { kind: string; url: string }[] | undefined) ?? []
  );
  const pendingPing = $derived.by(() => {
    const answered = new Set(events.filter((e) => e.type === 'ping_answered').map((e) => e.payload.ping_id));
    return events.findLast((e) => e.type === 'ping_raised' && !answered.has(e.payload.ping_id));
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
      if (e.type === 'stage_completed') {
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
  let allowing = $state<Record<string, boolean>>({});
  let showTail = $state(true);
  let tailEl = $state<HTMLPreElement | null>(null);
  const currentStage = $derived.by(() => {
    const started = events.findLast((e) => e.type === 'stage_started');
    if (!started) return null;
    // A completion/failure for that stage after it means nothing is running.
    const idx = events.lastIndexOf(started);
    const doneAfter = events
      .slice(idx + 1)
      .some((e) => (e.type === 'stage_completed' || e.type === 'stage_failed') && e.payload.stage_id === started.payload.stage_id);
    return doneAfter ? null : started;
  });
  const elapsed = $derived.by(() => {
    if (!currentStage) return '';
    const ms = now - new Date(currentStage.ts).getTime();
    if (ms < 0) return '0s';
    const s = Math.floor(ms / 1000);
    return s >= 60 ? `${Math.floor(s / 60)}m ${s % 60}s` : `${s}s`;
  });

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
      } catch {
        /* transient; keep last tail */
      }
    }, 2500);
    refreshRun();
    const stop = api.events(runId, 0, (ev) => {
      events = [...events, ev];
      // State-changing events refresh the header chip.
      if (ev.type.startsWith('run_') || ev.type.startsWith('ping_') || ev.type.startsWith('env_')) refreshRun();
    });
    return () => {
      clearInterval(tick);
      clearInterval(poll);
      stop();
    };
  });

  async function answer(ans: string, note?: string) {
    if (!pendingPing) return;
    answering = true;
    error = '';
    try {
      await api.answerPing(pendingPing.payload.ping_id as string, ans, note?.trim() || undefined);
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

  async function allowEgress(host: string) {
    allowing[host] = true;
    error = '';
    try {
      await api.egressAllow(runId, host);
      denials = await api.egressDenials(runId); // host clears from the list
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      allowing[host] = false;
    }
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

  async function restart() {
    error = '';
    try {
      const { run_id } = await api.restart(runId);
      await goto(`/runs/${run_id}`);
      // Full reload semantics: this page keys its state off the route param
      // captured at mount, so navigate hard to re-init streams.
      location.reload();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
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

  function describe(e: EventEnvelope): { text: string; cls: string; stage?: string } {
    const p = e.payload;
    switch (e.type) {
      case 'run_created': return { text: `run created — “${p.ticket_title}” (flow ${p.flow_name})`, cls: 'info' };
      case 'env_requested': return { text: 'provisioning workspace…', cls: '' };
      case 'env_ready': return { text: `workspace ready (${p.env_id})`, cls: 'ok' };
      case 'env_failed': return { text: `environment failed: ${p.error}`, cls: 'bad' };
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
      case 'stage_failed': return { text: `${p.stage_id} failed: ${p.reason}${p.error ? ` — ${String(p.error).slice(0, 200)}` : ''}`, cls: 'bad', stage: p.stage_id as string };
      case 'edge_taken': return { text: `↩ ${p.edge}${p.depth ? ` (then #${p.depth})` : ''} ${p.from} → ${p.to} (${p.count}/${p.max === -1 ? '∞' : p.max})`, cls: 'warn' };
      case 'ping_raised': return { text: `ping [${p.type}]: ${p.question}`, cls: 'warn' };
      case 'ping_answered': return { text: `answered: ${p.answer}${p.feedback ? ` — ${p.feedback}` : ''}`, cls: 'ok' };
      case 'verdict_received': return { text: p.approve ? 'review verdict: APPROVE' : `review verdict: REJECT (${p.n_comments} comments)`, cls: p.approve ? 'ok' : 'warn' };
      case 'mr_created': return { text: `merge request created: ${p.mr_url}`, cls: 'ok' };
      case 'run_completed': return { text: `run completed — ${fmtCost(p.total_cost_usd)} · ${p.total_turns} turns · ${p.unwanted_pings} unwanted pings`, cls: 'ok' };
      case 'run_abandoned': return { text: `abandoned at ${p.stalled_at}: ${p.reason}`, cls: 'bad' };
      case 'run_failed': return { text: `run failed: ${p.reason}`, cls: 'bad' };
      case 'run_paused': return { text: `⏸ paused${p.stage_id ? ` at ${p.stage_id}` : ''}`, cls: '' };
      case 'run_unpaused': return { text: '▶ resumed (play)', cls: 'info' };
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
  </div>
  <div class="row small muted" style="margin-bottom:16px">
    <span class="mono">{run.run_id}</span>
    <span>flow {run.flow_name}</span>
    {#if totals.turns > 0}<span>{fmtCost(totals.cost)} · {totals.turns} turns</span>{/if}
    {#each links as l (l.url)}<a href={l.url} target="_blank" rel="noreferrer">{l.kind} ↗</a>{/each}
    {#if run.mr_url}<a href={run.mr_url} target="_blank" rel="noreferrer">merge request ↗</a>{/if}
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
      <button onclick={restart} title="Start over as a fresh run: same ticket, project and flow — new workspace, new branch">
        Restart
      </button>
      {#if run.state !== 'archived'}
        <button onclick={() => act(() => api.archive(runId))}>Archive</button>
      {/if}
    {/if}
  </div>

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
      {#if denials.length > 0}
        <div class="denials">
          <strong>⚠ egress blocked</strong> — the workspace tried to reach hosts not in this run's
          allowlist. Add them to the project's <em>extra domains</em> and restart:
          <ul>
            {#each denials as d (d.host)}
              <li>
                <span class="mono">{d.host}</span> <span class="muted small">×{d.count}</span>
                <button class="small" disabled={allowing[d.host]} onclick={() => allowEgress(d.host)}>
                  {allowing[d.host] ? 'allowing…' : "Add to project's allowlist"}
                </button>
              </li>
            {/each}
          </ul>
          <p class="muted small" style="margin:6px 0 0">
            Adds the host to the project's allowlist (every future run inherits it) and unblocks the
            running agent instantly — no restart.
          </p>
        </div>
      {/if}
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
    <div class="card" style="border-color:var(--amber)">
      <div class="pingtype {GATE_TYPES.includes(pendingPingView.type) ? 'wanted' : 'unwanted'}">{pendingPingView.type}</div>
      <PingCard ping={pendingPingView} busy={answering} onanswer={answer} />
    </div>
  {/if}

  {#if error}<div class="error">{error}</div>{/if}

  <div class="card">
    <h2>Timeline</h2>
    <div class="timeline">
      {#each events as e (e.seq)}
        {@const d = describe(e)}
        <div class="tl-item {d.cls}">
          <span class="muted mono small">{fmtTime(e.ts)}</span>
          {d.text}
          {#if d.stage && (e.type === 'stage_completed' || e.type === 'stage_failed')}
            <button class="small" style="padding:1px 8px;margin-left:6px" onclick={() => showLog(d.stage!)}>log</button>
          {/if}
        </div>
        {#if logStage && d.stage === logStage && (e.type === 'stage_completed' || e.type === 'stage_failed')}
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
  .working .pulse { display:inline-block; width:8px; height:8px; border-radius:50%; background: var(--green, #4caf7d); margin-right:8px; animation: pulse 1.6s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity: 0.25; } }
  .denials {
    margin-top: 10px;
    padding: 8px 12px;
    border: 1px solid var(--amber, #e0af68);
    border-radius: 6px;
    background: color-mix(in srgb, var(--amber, #e0af68) 8%, transparent);
    font-size: 13px;
  }
  .denials ul {
    margin: 6px 0 0;
    padding-left: 18px;
  }
  .tail { max-height:280px; overflow-y:auto; white-space:pre-wrap; margin:10px 0 0; border:1px solid var(--border, #333); border-radius:6px; padding:8px; }
</style>
