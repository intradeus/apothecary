<script lang="ts">
  import { api, ago, type Ping, type PingFile } from '$lib/api';
  import PlanReview from '$lib/PlanReview.svelte';
  import { mdToHtml, planBlocks } from '$lib/planreview';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';

  let ping = $state<Ping | null>(null);
  let gone = $state(false);

  // The answered view renders the REVIEWED document from its raise-time
  // snapshot: "what did I approve" stays answerable long after the workspace
  // is gone (DESIGN-FILES §9 — this page used to apologize instead).
  let reviewed = $state<{ path: string; html: string } | null>(null);
  let answeredFiles = $state<PingFile[]>([]);

  async function loadAnswered(pid: string) {
    try {
      const { files } = await api.pingFiles(pid);
      answeredFiles = files;
      const primary = files[0];
      if (primary?.captured && primary.snapshot_id) {
        const res = await fetch(api.snapshotUrl(primary.snapshot_id));
        if (res.ok) {
          const text = await res.text();
          reviewed = {
            path: primary.path,
            html: planBlocks(text).map((b) => b.html).join('')
          };
        }
      }
    } catch {
      /* no files — the answer alone renders */
    }
  }

  onMount(async () => {
    try {
      ping = await api.ping(page.params.id!);
      if (ping.status === 'answered') await loadAnswered(ping.ping_id);
    } catch {
      gone = true;
    }
  });
</script>

{#if ping}
  <div class="row spread" style="margin-bottom:12px">
    <h1 style="margin:0">Review</h1>
    <span class="muted small">
      {#if ping.stage_id}<span class="mono">{ping.stage_id}</span> · {/if}
      {ago(ping.raised_at)} · <a href={`/runs/${ping.run_id}`}>view run</a> · <a href="/inbox">inbox</a>
    </span>
  </div>
  <div class="md muted" style="margin-top:-6px">{@html mdToHtml(ping.question)}</div>

  {#if ping.status === 'answered'}
    <div class="card">
      <h2 style="margin-top:0">
        Answered {ping.answered_at ? ago(ping.answered_at) : ''} —
        <span class={ping.answer === 'approve' ? 'ok' : ping.answer === 'reject' ? 'bad' : ''}>{ping.answer}</span>
      </h2>
      {#if ping.feedback}<div class="md">{@html mdToHtml(ping.feedback)}</div>{/if}
    </div>
    {#if reviewed}
      <div class="card">
        <div class="row spread" style="margin-bottom:8px">
          <span class="mono small muted">{reviewed.path}</span>
          <span class="small muted">as reviewed — the snapshot taken when this was shown to you</span>
        </div>
        <div class="md">{@html reviewed.html}</div>
      </div>
    {/if}
    {#if answeredFiles.length > 1}
      <div class="card">
        <div class="small muted" style="margin-bottom:6px">Also attached by the agent:</div>
        <div style="display:flex; flex-wrap:wrap; gap:8px">
          {#each answeredFiles.slice(1) as f (f.path)}
            {#if f.captured && f.snapshot_id}
              <a class="small mono" href={api.snapshotUrl(f.snapshot_id)} target="_blank" rel="noreferrer">📎 {f.path}</a>
            {:else}
              <span class="small mono muted" title="not captured when the ping was raised">📎 {f.path} (not captured)</span>
            {/if}
          {/each}
        </div>
      </div>
    {/if}
  {:else}
    <PlanReview {ping} onanswered={() => goto('/inbox')} />
  {/if}
{:else if gone}
  <div class="empty">Unknown ping. <a href="/inbox">Back to the inbox</a>.</div>
{:else}
  <div class="empty">Loading…</div>
{/if}

<style>
  .ok {
    color: var(--green, #4caf7d);
  }
  .bad {
    color: var(--red, #e06c75);
  }
  .md :global(pre) {
    overflow-x: auto;
  }
</style>
