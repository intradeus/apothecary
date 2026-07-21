<script lang="ts">
  import { api, ago, type Ping } from '$lib/api';
  import PingCard from '$lib/PingCard.svelte';
  import { GATE_TYPES } from '$lib/ping';
  import { onMount } from 'svelte';

  let pings = $state<Ping[]>([]);
  let loaded = $state(false);
  let tab = $state<'pending' | 'answered'>('pending');
  let feedback = $state<Record<string, string>>({});
  let busy = $state<Record<string, boolean>>({});
  let error = $state('');

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
        <span class="pingtype {GATE_TYPES.includes(p.type) ? 'wanted' : 'unwanted'}">{p.type}</span>
        <span class="muted small">
          {#if p.stage_id}<span class="mono">{p.stage_id}</span> · {/if}
          {ago(p.raised_at)} · <a href={`/runs/${p.run_id}`}>view run</a>
        </span>
      </div>
      <PingCard
        ping={p}
        busy={busy[p.ping_id] ?? false}
        bind:feedback={feedback[p.ping_id]}
        onanswer={(a) => answer(p, a)}
      />
    </div>
  {/each}
{/if}
