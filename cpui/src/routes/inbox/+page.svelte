<script lang="ts">
  import { api, ago, type Ping } from '$lib/api';
  import { mdToHtml } from '$lib/planreview';
  import { onMount } from 'svelte';

  let pings = $state<Ping[]>([]);
  let loaded = $state(false);
  let tab = $state<'pending' | 'answered'>('pending');
  let feedback = $state<Record<string, string>>({});
  let busy = $state<Record<string, boolean>>({});
  let error = $state('');

  const gateTypes = ['plan_approval', 'final_review'];

  const refresh = async () => {
    try {
      pings = await api.pings(tab);
      loaded = true;
    } catch {
      /* retry next tick */
    }
  };

  onMount(() => {
    const t = setInterval(refresh, 4000);
    return () => clearInterval(t);
  });
  $effect(() => {
    void tab;
    loaded = false;
    refresh();
  });

  async function answer(p: Ping, ans: string) {
    busy[p.ping_id] = true;
    error = '';
    try {
      await api.answerPing(p.ping_id, ans, feedback[p.ping_id]?.trim() || undefined);
      delete feedback[p.ping_id];
      await refresh();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy[p.ping_id] = false;
    }
  }
</script>

<div class="row spread">
  <h1 style="margin:0">Inbox</h1>
  <div class="row" style="gap:0">
    <button class:primary={tab === 'pending'} onclick={() => (tab = 'pending')}>Pending</button>
    <button class:primary={tab === 'answered'} onclick={() => (tab = 'answered')}>Answered</button>
  </div>
</div>

{#if error}<div class="error">{error}</div>{/if}

{#if pings.length === 0}
  <div class="empty">
    {#if !loaded}Loading…{:else if tab === 'pending'}No pending pings — the apothecary needs nothing from you.{:else}No answered pings yet.{/if}
  </div>
{:else}
  {#each pings as p (p.ping_id)}
    <div class="card">
      <div class="row spread">
        <span class="pingtype {gateTypes.includes(p.type) ? 'wanted' : 'unwanted'}">{p.type}</span>
        <span class="muted small">
          {#if p.stage_id}<span class="mono">{p.stage_id}</span> · {/if}
          {ago(p.raised_at)} · <a href={`/runs/${p.run_id}`}>view run</a>
        </span>
      </div>
      <div class="md" style="margin:8px 0">{@html mdToHtml(p.question)}</div>
      {#if p.context}<div class="md muted small">{@html mdToHtml(p.context)}</div>{/if}
      {#if p.payload_ref}
        <p class="mono small">
          {#if p.payload_ref.startsWith('http')}
            <a href={p.payload_ref} target="_blank" rel="noreferrer">{p.payload_ref} ↗</a>
          {:else if p.status === 'pending'}
            {p.payload_ref} —
            <a href={`/pings/${p.ping_id}`}>review &amp; comment →</a>
          {:else}{p.payload_ref}{/if}
        </p>
      {/if}

      {#if p.status === 'answered'}
        <div class="answerbox">
          <div class="small muted">
            answered {p.answered_at ? ago(p.answered_at) : ''} —
            <strong class={p.answer === 'approve' ? 'ok' : p.answer === 'reject' ? 'bad' : ''}>{p.answer}</strong>
          </div>
          {#if p.feedback}<div class="md small" style="margin-top:4px">{@html mdToHtml(p.feedback)}</div>{/if}
        </div>
      {:else if gateTypes.includes(p.type)}
        <textarea bind:value={feedback[p.ping_id]} placeholder="Feedback (required when rejecting)"></textarea>
        <div class="row" style="margin-top:10px">
          <button class="approve" disabled={busy[p.ping_id]} onclick={() => answer(p, 'approve')}>Approve</button>
          <button class="reject" disabled={busy[p.ping_id] || !feedback[p.ping_id]?.trim()} onclick={() => answer(p, 'reject')}>
            Reject with feedback
          </button>
        </div>
      {:else if p.options?.length}
        <textarea bind:value={feedback[p.ping_id]} placeholder="Optional note to send along…"></textarea>
        <div class="row" style="margin-top:10px; flex-wrap:wrap; gap:8px">
          {#each p.options as opt (opt)}
            <button class="primary" disabled={busy[p.ping_id]} onclick={() => answer(p, opt)}>{opt}</button>
          {/each}
        </div>
      {:else}
        <textarea bind:value={feedback[p.ping_id]} placeholder="Your answer to the agent…"></textarea>
        <div class="row" style="margin-top:10px">
          <button class="primary" disabled={busy[p.ping_id] || !feedback[p.ping_id]?.trim()}
            onclick={() => answer(p, feedback[p.ping_id].trim())}>Send answer</button>
        </div>
      {/if}
    </div>
  {/each}
{/if}

<style>
  .answerbox {
    border-left: 3px solid var(--border, #444);
    padding: 6px 10px;
    margin-top: 8px;
  }
  .answerbox .ok {
    color: var(--green, #4caf7d);
  }
  .answerbox .bad {
    color: var(--red, #e06c75);
  }
  .md :global(p) {
    margin: 4px 0;
  }
  .md :global(pre) {
    overflow-x: auto;
  }
</style>
