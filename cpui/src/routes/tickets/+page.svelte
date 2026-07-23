<script lang="ts">
  import { api, ago, type Project, type Ticket } from '$lib/api';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';

  let tickets = $state<Ticket[]>([]);
  let projects = $state<Project[]>([]);
  let loaded = $state(false);
  let error = $state('');

  onMount(async () => {
    try {
      [tickets, projects] = await Promise.all([api.tickets(), api.projects()]);
      loaded = true;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  });

  const projectName = (id: string) => projects.find((p) => p.id === id)?.name ?? id.slice(-6);
</script>

<div class="row spread" style="margin-bottom:16px">
  <h1 style="margin:0">Tickets</h1>
  <button class="primary" onclick={() => goto('/tickets/new')}>+ New ticket</button>
</div>

{#if error}<div class="error">{error}</div>{/if}

{#each tickets as t (t.id)}
  <a class="card row spread cardlink" href={`/tickets/${t.id}`}>
    <div>
      <strong>{t.title}</strong>
      <div class="small muted">
        {projectName(t.project_id)}
        {#if t.base_branch}· <span class="mono">{t.base_branch}</span>{/if}
        · {ago(t.created_at)}
      </div>
    </div>
    <span class="muted">→</span>
  </a>
{:else}
  <div class="empty">
    {#if !loaded}
      Loading…
    {:else}
      No tickets yet. Create the first one to start runs against it.
    {/if}
  </div>
{/each}
