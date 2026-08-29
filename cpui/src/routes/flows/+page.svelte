<script lang="ts">
  import { api } from '$lib/api';
  import type { Flow } from '$lib/domain';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import { CURRENT_VERSION } from '$lib/flowdoc';
  import { ago } from '$lib/format';

  let flows = $state<Flow[]>([]);
  let loaded = $state(false);
  let error = $state('');
  let showNew = $state(false);
  let newName = $state('');
  let busy = $state(false);

  // A minimal valid v2 document: the builder gets the ticket (always implicit)
  // and the flow completes. Everything else is added in the editor.
  const template = (name: string) => `name: ${name}
version: ${CURRENT_VERSION}
description: describe what this flow does
defaults: { timeout: 30m, harness: opencode, model: anthropic/claude-sonnet-4-6, max_turns: 50 }
budget: { max_cost_usd: 10.0, max_total_turns: 300 }
stages:
  # Every stage declares what it reads and writes; $FAILURE is what the edge
  # that sent the run back here carried.
  - { id: build, type: agent, agent: builder, on_success: done,
      input: { failure: $FAILURE },
      on_fail: { to: build, max: 2 } }
`;

  onMount(async () => {
    try {
      flows = await api.flows.list();
      loaded = true;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  });

  async function create(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      const name = newName.trim();
      await api.flows.create(name, template(name));
      await goto(`/flows/${encodeURIComponent(name)}`);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

<div class="row spread" style="margin-bottom:16px">
  <h1 style="margin:0">Flows</h1>
  <button class="primary" onclick={() => (showNew = !showNew)}>{showNew ? 'Cancel' : '+ New flow'}</button>
</div>

{#if showNew}
  <form class="card" onsubmit={create}>
    <h2>New flow</h2>
    <label for="f-name">Name <span class="muted">— an immutable id (lowercase slug); starts from a minimal skeleton</span></label>
    <input id="f-name" bind:value={newName} required pattern="[a-z][a-z0-9_-]*" class="mono" placeholder="v2-fast" />
    {#if error}<div class="error">{error}</div>{/if}
    <div style="margin-top:14px">
      <button class="primary" type="submit" disabled={busy || !newName.trim()}>Create & edit</button>
    </div>
  </form>
{:else if error}
  <div class="error">{error}</div>
{/if}

{#each flows as f (f.name)}
  <a class="card row spread cardlink" href={`/flows/${encodeURIComponent(f.name)}`}>
    <div>
      <strong class="mono">{f.name}</strong>
      {#if f.builtin}<span class="chip" title="Ships with the binary — saving in the editor creates your editable copy">built-in</span>{/if}
      {#if f.shadowsBuiltin}<span class="chip" title="Your copy overrides the built-in of the same name; deleting it resets to the default">overrides built-in</span>{/if}
      <div class="small muted">hash {f.hash.slice(0, 12)}{f.builtin ? ' · from the binary' : ` · updated ${ago(f.updatedAt)}`}</div>
    </div>
    <span class="muted">→</span>
  </a>
{:else}
  <div class="empty">{loaded ? 'No flows.' : 'Loading…'}</div>
{/each}
