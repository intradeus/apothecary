<script lang="ts">
  import { page } from '$app/state';
  import { api, stateColors, fmtTime, fmtCost, type EventEnvelope, type RunView } from '$lib/api';
  import { onMount } from 'svelte';

  const runId = page.params.id ?? '';

  let run = $state<RunView | null>(null);
  let events = $state<EventEnvelope[]>([]);
  let logStage = $state('');
  let logText = $state('');
  let error = $state('');
  let answering = $state(false);
  let feedback = $state('');

  // Live projection niceties derived from the stream.
  const links = $derived(
    (events.findLast((e) => e.type === 'env_ready')?.payload.links as { kind: string; url: string }[] | undefined) ?? []
  );
  const pendingPing = $derived.by(() => {
    const answered = new Set(events.filter((e) => e.type === 'ping_answered').map((e) => e.payload.ping_id));
    return events.findLast((e) => e.type === 'ping_raised' && !answered.has(e.payload.ping_id));
  });
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

  const refreshRun = async () => {
    try {
      run = (await api.run(runId)).run;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  };

  onMount(() => {
    refreshRun();
    const stop = api.events(runId, 0, (ev) => {
      events = [...events, ev];
      // State-changing events refresh the header chip.
      if (ev.type.startsWith('run_') || ev.type.startsWith('ping_') || ev.type.startsWith('env_')) refreshRun();
    });
    return stop;
  });

  async function answer(ans: string) {
    if (!pendingPing) return;
    answering = true;
    error = '';
    try {
      await api.answerPing(pendingPing.payload.ping_id as string, ans, feedback.trim() || undefined);
      feedback = '';
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
      case 'edge_taken': return { text: `↩ ${p.edge} ${p.from} → ${p.to} (${p.count}/${p.max})`, cls: 'warn' };
      case 'ping_raised': return { text: `ping [${p.type}]: ${p.question}`, cls: 'warn' };
      case 'ping_answered': return { text: `answered: ${p.answer}${p.feedback ? ` — ${p.feedback}` : ''}`, cls: 'ok' };
      case 'verdict_received': return { text: p.approve ? 'review verdict: APPROVE' : `review verdict: REJECT (${p.n_comments} comments)`, cls: p.approve ? 'ok' : 'warn' };
      case 'mr_created': return { text: `merge request created: ${p.mr_url}`, cls: 'ok' };
      case 'run_completed': return { text: `run completed — ${fmtCost(p.total_cost_usd)} · ${p.total_turns} turns · ${p.unwanted_pings} unwanted pings`, cls: 'ok' };
      case 'run_abandoned': return { text: `abandoned at ${p.stalled_at}: ${p.reason}`, cls: 'bad' };
      case 'run_failed': return { text: `run failed: ${p.reason}`, cls: 'bad' };
      case 'run_archived': return { text: 'archived', cls: '' };
      case 'env_destroyed': return { text: 'workspace destroyed', cls: '' };
      default: return { text: e.type, cls: '' };
    }
  }

  const gateTypes = ['plan_approval', 'final_review'];
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
      <button class="danger" onclick={() => act(() => api.abort(runId))}>Abort</button>
    {:else if run.state !== 'archived'}
      <button onclick={() => act(() => api.archive(runId))}>Archive</button>
    {/if}
  </div>

  {#if pendingPing}
    {@const ptype = pendingPing.payload.type as string}
    <div class="card" style="border-color:var(--amber)">
      <div class="pingtype {gateTypes.includes(ptype) ? 'wanted' : 'unwanted'}">{ptype}</div>
      <p style="margin:6px 0 10px">{pendingPing.payload.question}</p>
      {#if pendingPing.payload.payload_ref}
        <p class="mono small muted">
          {#if String(pendingPing.payload.payload_ref).startsWith('http')}
            <a href={String(pendingPing.payload.payload_ref)} target="_blank" rel="noreferrer">{pendingPing.payload.payload_ref} ↗</a>
          {:else}{pendingPing.payload.payload_ref}{/if}
        </p>
      {/if}
      {#if gateTypes.includes(ptype)}
        <textarea bind:value={feedback} placeholder="Feedback (required when rejecting)"></textarea>
        <div class="row" style="margin-top:10px">
          <button class="approve" disabled={answering} onclick={() => answer('approve')}>Approve</button>
          <button class="reject" disabled={answering || !feedback.trim()} onclick={() => answer('reject')}>Reject with feedback</button>
        </div>
      {:else}
        <textarea bind:value={feedback} placeholder="Your answer to the agent…"></textarea>
        <div class="row" style="margin-top:10px">
          <button class="primary" disabled={answering || !feedback.trim()} onclick={() => answer(feedback.trim())}>Send answer</button>
        </div>
      {/if}
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
