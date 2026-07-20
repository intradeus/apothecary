<script lang="ts">
  import { api, ago, type Ping } from '$lib/api';
  import { onMount } from 'svelte';

  let pings = $state<Ping[]>([]);
  let loaded = $state(false);
  let feedback = $state<Record<string, string>>({});
  let busy = $state<Record<string, boolean>>({});
  let error = $state('');

  const gateTypes = ['plan_approval', 'final_review'];

  const refresh = async () => {
    try {
      pings = await api.pendingPings();
      loaded = true;
    } catch {
      /* retry next tick */
    }
  };

  onMount(() => {
    refresh();
    const t = setInterval(refresh, 4000);
    return () => clearInterval(t);
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

<h1>Inbox</h1>

{#if error}<div class="error">{error}</div>{/if}

{#if pings.length === 0}
  <div class="empty">{loaded ? 'No pending pings — the apothecary needs nothing from you.' : 'Loading…'}</div>
{:else}
  {#each pings as p (p.ping_id)}
    <div class="card">
      <div class="row spread">
        <span class="pingtype {gateTypes.includes(p.type) ? 'wanted' : 'unwanted'}">{p.type}</span>
        <span class="muted small">{ago(p.raised_at)} · <a href={`/runs/${p.run_id}`}>view run</a></span>
      </div>
      <p style="margin:8px 0">{p.question}</p>
      {#if p.context}<p class="muted small" style="white-space:pre-wrap">{p.context}</p>{/if}
      {#if p.payload_ref}
        <p class="mono small">
          {#if p.payload_ref.startsWith('http')}
            <a href={p.payload_ref} target="_blank" rel="noreferrer">{p.payload_ref} ↗</a>
          {:else}{p.payload_ref}{/if}
        </p>
      {/if}
      {#if gateTypes.includes(p.type)}
        <textarea bind:value={feedback[p.ping_id]} placeholder="Feedback (required when rejecting)"></textarea>
        <div class="row" style="margin-top:10px">
          <button class="approve" disabled={busy[p.ping_id]} onclick={() => answer(p, 'approve')}>Approve</button>
          <button class="reject" disabled={busy[p.ping_id] || !feedback[p.ping_id]?.trim()} onclick={() => answer(p, 'reject')}>
            Reject with feedback
          </button>
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
