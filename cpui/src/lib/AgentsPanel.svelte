<script lang="ts">
  import { api, type Agent, type AgentVersion, type Project } from '$lib/api';
  import { onMount } from 'svelte';

  let { project }: { project: Project } = $props();

  let agents = $state<Agent[]>([]);
  let projects = $state<Project[]>([]);
  let error = $state('');
  let notice = $state('');
  let editingName = $state<string | null>(null); // null = closed, '' = new agent
  let versionsFor = $state<string | null>(null);
  let versions = $state<AgentVersion[]>([]);
  let copyFrom = $state('');
  let busy = $state(false);

  // Editor fields.
  let fName = $state('');
  let fDescription = $state('');
  let fReadOnly = $state(false);
  let fAllowBash = $state(false);
  let fPrompt = $state('');

  const refresh = async () => {
    try {
      agents = await api.agents(project.id);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  };
  onMount(async () => {
    await refresh();
    try {
      projects = (await api.projects()).filter((p) => p.id !== project.id);
    } catch {
      /* copy stays hidden */
    }
  });

  function openEditor(a?: Agent) {
    editingName = a ? a.name : '';
    fName = a?.name ?? '';
    fDescription = a?.description ?? '';
    fReadOnly = a?.read_only ?? false;
    fAllowBash = a?.allow_bash ?? false;
    fPrompt = a?.prompt ?? '';
    notice = '';
    error = '';
  }

  async function save(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      await api.putAgent(project.id, fName.trim(), {
        description: fDescription,
        read_only: fReadOnly,
        allow_bash: fAllowBash,
        prompt: fPrompt
      });
      editingName = null;
      await refresh();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  async function remove(name: string) {
    if (!confirm(`Delete agent "${name}"? Version history is kept.`)) return;
    error = '';
    try {
      await api.deleteAgent(project.id, name);
      await refresh();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function showVersions(name: string) {
    if (versionsFor === name) {
      versionsFor = null;
      return;
    }
    error = '';
    try {
      versions = await api.agentVersions(project.id, name);
      versionsFor = name;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function copy() {
    if (!copyFrom) return;
    busy = true;
    error = '';
    notice = '';
    try {
      const { copied } = await api.copyAgents(project.id, copyFrom);
      notice = `Copied ${copied.length} agent(s).`;
      await refresh();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

</script>

<div class="card">
  <div class="row spread">
    <h2 style="margin:0">Agents</h2>
    <div class="row">
      {#if projects.length > 0}
        <select bind:value={copyFrom} style="width:auto">
          <option value="">Copy from project…</option>
          {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
        </select>
        <button onclick={copy} disabled={busy || !copyFrom}>Copy</button>
      {/if}
      <button class="primary" onclick={() => openEditor()}>New agent</button>
    </div>
  </div>
  <p class="muted small" style="margin-top:2px">
    The prompts your flows run with — edit here, versioned on every save. Flows reference agents by
    name; this project defines what each name means. Built-in agents ship with the binary and apply
    until you customize them (your copy shadows the built-in; deleting the copy resets it).
  </p>
  {#if error}<div class="error">{error}</div>{/if}
  {#if notice}<div class="muted small">{notice}</div>{/if}

  {#each agents as a (a.name)}
    <div class="row spread" style="padding:6px 0; border-top:1px solid var(--border, #333)">
      <div>
        <strong class="mono">{a.name}</strong>
        {#if a.builtin}<span class="chip" title="Ships with the binary — editing creates this project's own copy">built-in</span>{/if}
        <span class="muted small">
          {a.description}
          {#if a.read_only}· read-only{/if}
          {#if a.allow_bash}· bash{/if}
          · <span class="mono">{a.hash}</span>
        </span>
      </div>
      <div class="row">
        {#if !a.builtin}
          <button onclick={() => showVersions(a.name)}>{versionsFor === a.name ? 'Hide versions' : 'Versions'}</button>
        {/if}
        <button onclick={() => openEditor(a)}>{a.builtin ? 'Customize' : 'Edit'}</button>
        {#if !a.builtin}
          <button class="danger" onclick={() => remove(a.name)}>Delete</button>
        {/if}
      </div>
    </div>
    {#if versionsFor === a.name}
      <div style="margin:4px 0 10px 12px">
        {#each versions as v (v.hash)}
          <details>
            <summary class="small">
              <span class="mono">{v.hash}</span>
              <span class="muted">— {new Date(v.created_at).toLocaleString()}{v.hash === a.hash ? ' (current)' : ''}</span>
            </summary>
            <pre class="mono small" style="white-space:pre-wrap">{v.spec.prompt}</pre>
          </details>
        {/each}
      </div>
    {/if}
  {:else}
    <div class="empty">
      No agents yet — flows with agent stages will fail preflight. Create one or copy from
      another project.
    </div>
  {/each}

  {#if editingName !== null}
    <form onsubmit={save} style="margin-top:14px">
      <h2>{editingName === '' ? 'New agent' : `Edit ${editingName}`}</h2>
      <label for="ag-name">Name <span class="muted">— lowercase slug; flows reference it</span></label>
      <input id="ag-name" bind:value={fName} required disabled={editingName !== ''} placeholder="builder" />
      <label for="ag-desc">Description</label>
      <input id="ag-desc" bind:value={fDescription} placeholder="implements the approved plan" />
      <div class="row" style="gap:16px; margin:8px 0">
        <label class="row small" style="margin:0; gap:4px">
          <input type="checkbox" bind:checked={fReadOnly} style="width:auto" /> read-only
        </label>
        <label class="row small" style="margin:0; gap:4px">
          <input type="checkbox" bind:checked={fAllowBash} style="width:auto" /> allow bash
        </label>
      </div>
      <label for="ag-prompt">Prompt</label>
      <textarea id="ag-prompt" bind:value={fPrompt} rows="14" required placeholder="You are the building agent…"></textarea>
      <div class="row" style="margin-top:10px">
        <button class="primary" type="submit" disabled={busy}>Save</button>
        <button type="button" onclick={() => (editingName = null)}>Cancel</button>
      </div>
    </form>
  {/if}
</div>
