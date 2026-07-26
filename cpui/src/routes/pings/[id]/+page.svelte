<script lang="ts">
  import { api, ago, type Ping } from '$lib/api';
  import PlanReview from '$lib/PlanReview.svelte';
  import { mdToHtml } from '$lib/planreview';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';

  let ping = $state<Ping | null>(null);
  let gone = $state(false);

  onMount(async () => {
    try {
      ping = await api.ping(page.params.id!);
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
      <p class="muted small">
        The exchange is permanent history; the workspace artifact itself may no longer exist.
      </p>
    </div>
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
