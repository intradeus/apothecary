<script lang="ts">
  import { api } from '$lib/api';
  import type { Project } from '$lib/domain';
  import TicketForm from '$lib/TicketForm.svelte';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';

  let projects = $state<Project[]>([]);
  let loaded = $state(false);

  onMount(async () => {
    try {
      projects = await api.projects.list();
    } finally {
      loaded = true;
    }
  });
</script>

<div class="row spread" style="margin-bottom:16px">
  <h1 style="margin:0">New ticket</h1>
  <a class="small" href="/tickets">← all tickets</a>
</div>

{#if loaded}
  <TicketForm {projects} onsaved={(t) => goto(`/tickets/${t.id}`)} />
{:else}
  <div class="empty">Loading…</div>
{/if}
