<script lang="ts">
  import { api, ago, type Ping } from '$lib/api';
  import PlanReview from '$lib/PlanReview.svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';

  let ping = $state<Ping | null>(null);
  let gone = $state(false);

  onMount(async () => {
    try {
      const pending = await api.pendingPings();
      ping = pending.find((p) => p.ping_id === page.params.id) ?? null;
      gone = ping === null;
    } catch {
      gone = true;
    }
  });
</script>

{#if ping}
  <div class="row spread" style="margin-bottom:12px">
    <h1 style="margin:0">{ping.type === 'plan_approval' ? 'Plan review' : 'Review'}</h1>
    <span class="muted small">{ago(ping.raised_at)} · <a href={`/runs/${ping.run_id}`}>view run</a> · <a href="/inbox">inbox</a></span>
  </div>
  <p class="muted" style="margin-top:-6px">{ping.question}</p>
  <PlanReview {ping} onanswered={() => goto('/inbox')} />
{:else if gone}
  <div class="empty">This ping is gone — already answered, or its run has ended. <a href="/inbox">Back to the inbox</a>.</div>
{:else}
  <div class="empty">Loading…</div>
{/if}
