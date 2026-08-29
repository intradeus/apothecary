<script lang="ts">
  import { api } from '$lib/api';
  import type { Flow, Integration } from '$lib/domain';
  import ProjectForm from '$lib/ProjectForm.svelte';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';

  let integrations = $state<Integration[]>([]);
  let flows = $state<Flow[]>([]);
  let loaded = $state(false);
  let error = $state('');

  onMount(async () => {
    try {
      [integrations, flows] = await Promise.all([api.integrations.list(), api.flows.list()]);
      loaded = true;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  });
</script>

<h1>New project</h1>

{#if error}<div class="error">{error}</div>{/if}
{#if loaded}
  <ProjectForm {integrations} {flows} onsaved={(p) => goto(`/projects/${p.id}`)} />
{:else}
  <div class="empty">Loading…</div>
{/if}
