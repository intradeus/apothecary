<script lang="ts">
  // Flows, grouped by the project that owns them.
  //
  // A flow names its project's commands and its agents know that repository,
  // so there is no such thing as "the flow called default" — there is one per
  // project. The templates below are the exception: compiled into the binary,
  // owned by nobody, and copied into a project the moment someone edits one.
  import { api } from '$lib/api';
  import type { Flow, Project } from '$lib/domain';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import { CURRENT_VERSION } from '$lib/flowdoc';
  import { ago } from '$lib/format';
  import CopyFlow from '$lib/CopyFlow.svelte';

  let projects = $state<Project[]>([]);
  let owned = $state<Flow[]>([]);
  let templates = $state<Flow[]>([]);
  let loaded = $state(false);
  let error = $state('');

  let showNew = $state(false);
  let newName = $state('');
  let newProject = $state('');
  let busy = $state(false);

  /** Which flow the copy dialog is working on; null when it is closed. */
  let copying = $state<Flow | null>(null);

  const byProject = $derived.by(() => {
    const groups = new Map<string, Flow[]>();
    for (const f of owned) {
      const list = groups.get(f.projectId) ?? [];
      list.push(f);
      groups.set(f.projectId, list);
    }
    return projects
      .map((p) => ({ project: p, flows: groups.get(p.id) ?? [] }))
      .filter((g) => g.flows.length > 0 || projects.length <= 8);
  });

  const projectName = (id: string) => projects.find((p) => p.id === id)?.name ?? id;

  // A minimal valid document: the builder gets the ticket (always implicit)
  // and the flow completes. Everything else is added in the editor.
  const skeleton = (name: string) => `name: ${name}
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

  async function load() {
    [projects, owned, templates] = await Promise.all([
      api.projects.list(),
      api.flows.listAll(),
      api.flows.templates()
    ]);
    if (!newProject && projects.length) newProject = projects[0].id;
    loaded = true;
  }

  onMount(async () => {
    try {
      await load();
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
      await api.flows.create(newProject, name, skeleton(name));
      await goto(`/flows/${newProject}/${encodeURIComponent(name)}`);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  /** Adopting a template is a copy into the project, which is what makes it
   *  editable — the binary's document itself never changes. */
  async function adopt(t: Flow, projectId: string) {
    error = '';
    try {
      await api.flows.copy('', t.name, projectId, t.name);
      await goto(`/flows/${projectId}/${encodeURIComponent(t.name)}`);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }
</script>

<div class="row spread" style="margin-bottom:16px">
  <h1 style="margin:0">Flows</h1>
  <button class="primary" onclick={() => (showNew = !showNew)} disabled={!projects.length}>
    {showNew ? 'Cancel' : '+ New flow'}
  </button>
</div>

{#if showNew}
  <form class="card" onsubmit={create}>
    <h2>New flow</h2>
    <label for="f-project">Project <span class="muted">— the flow belongs to it; its stages run that repository's commands</span></label>
    <select id="f-project" bind:value={newProject}>
      {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
    </select>
    <label for="f-name" style="margin-top:10px">Name <span class="muted">— an immutable id (lowercase slug); starts from a minimal skeleton</span></label>
    <input id="f-name" bind:value={newName} required pattern="[a-z][a-z0-9_-]*" class="mono" placeholder="v2-fast" />
    {#if error}<div class="error">{error}</div>{/if}
    <div style="margin-top:14px">
      <button class="primary" type="submit" disabled={busy || !newName.trim() || !newProject}>Create &amp; edit</button>
    </div>
  </form>
{:else if error}
  <div class="error">{error}</div>
{/if}

{#if !projects.length && loaded}
  <div class="empty">
    Flows belong to a project. <a href="/projects/new">Create a project</a> first.
  </div>
{/if}

{#each byProject as group (group.project.id)}
  <h2 class="row spread" style="margin:22px 0 8px">
    <a href={`/projects/${group.project.id}`} class="plain">{group.project.name}</a>
    <span class="small muted">{group.flows.length} flow{group.flows.length === 1 ? '' : 's'}</span>
  </h2>
  {#each group.flows as f (f.name)}
    <div class="card row spread">
      <a class="grow plain" href={`/flows/${f.projectId}/${encodeURIComponent(f.name)}`}>
        <strong class="mono">{f.name}</strong>
        {#if f.name === group.project.defaultFlow}<span class="chip" title="Runs start on this flow unless one is picked">default</span>{/if}
        {#if f.shadowsBuiltin}<span class="chip" title="This project's own copy of a built-in template; deleting it resets to the template">from template</span>{/if}
        <div class="small muted">hash {f.hash.slice(0, 12)} · updated {ago(f.updatedAt)}</div>
      </a>
      <button class="small" onclick={() => (copying = f)} title="Copy this flow into another project">⧉ copy</button>
    </div>
  {:else}
    <div class="empty small">
      No flows of its own — runs use the <span class="mono">{group.project.defaultFlow}</span> template.
    </div>
  {/each}
{/each}

{#if templates.length}
  <h2 style="margin:26px 0 8px">
    Templates
    <span class="small muted" style="font-weight:normal">— compiled in, owned by nobody; adopting one copies it into a project</span>
  </h2>
  {#each templates as t (t.name)}
    <div class="card row spread">
      <div class="grow">
        <strong class="mono">{t.name}</strong>
        <span class="chip">built-in</span>
        <div class="small muted">hash {t.hash.slice(0, 12)} · from the binary</div>
      </div>
      {#if projects.length}
        <button class="small" onclick={() => (copying = t)}>⧉ use in a project</button>
      {/if}
    </div>
  {/each}
{/if}

{#if !loaded && !error}<div class="empty">Loading…</div>{/if}

{#if copying}
  <CopyFlow
    flow={copying}
    {projects}
    sourceLabel={copying.builtin ? 'template' : projectName(copying.projectId)}
    onclose={() => (copying = null)}
    oncopied={async (projectId, name) => {
      copying = null;
      await goto(`/flows/${projectId}/${encodeURIComponent(name)}`);
    }}
  />
{/if}

<style>
  .grow {
    flex: 1;
    min-width: 0;
  }
  .plain {
    color: inherit;
    text-decoration: none;
  }
  .plain:hover strong {
    text-decoration: underline;
  }
</style>
