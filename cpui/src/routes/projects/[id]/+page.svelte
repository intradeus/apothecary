<script lang="ts">
  import { api, type CheckRun, type FlowRow, type Integration, type PreflightStep, type Project } from '$lib/api';
  import AgentsPanel from '$lib/AgentsPanel.svelte';
  import ProjectForm from '$lib/ProjectForm.svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';

  let project = $state<Project | null>(null);
  let integrations = $state<Integration[]>([]);
  let flows = $state<FlowRow[]>([]);
  let error = $state('');
  let editing = $state(false);
  let preflight = $state<{ ok: boolean; steps: PreflightStep[] } | 'running' | null>(null);
  let check = $state<CheckRun | null>(null);
  let checkKeep = $state(false);
  let checkError = $state('');
  let checkTimer: ReturnType<typeof setInterval> | undefined;

  async function startEnvCheck() {
    checkError = '';
    try {
      const { check_id } = await api.startEnvCheck(project!.id, checkKeep);
      check = await api.envCheck(check_id);
      clearInterval(checkTimer);
      checkTimer = setInterval(async () => {
        try {
          check = await api.envCheck(check_id);
          if (check.status !== 'running') clearInterval(checkTimer);
        } catch {
          /* keep polling */
        }
      }, 2000);
    } catch (e) {
      checkError = e instanceof Error ? e.message : String(e);
    }
  }
  onMount(() => () => clearInterval(checkTimer));

  const load = async () => {
    try {
      [project, integrations, flows] = await Promise.all([
        api.project(page.params.id!),
        api.integrations(),
        api.flows()
      ]);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  };
  onMount(load);

  async function runPreflight() {
    preflight = 'running';
    try {
      preflight = await api.preflight(project!.id);
    } catch (e) {
      preflight = { ok: false, steps: [{ id: 'request', ok: false, error: e instanceof Error ? e.message : String(e) }] };
    }
  }

  async function remove() {
    if (!project) return;
    if (!confirm(`Delete project "${project.name}"? All its runs must be archived first.`)) return;
    error = '';
    try {
      await api.deleteProject(project.id);
      await goto('/projects');
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }
</script>

{#if project}
  <div class="row spread" style="margin-bottom:16px">
    <h1 style="margin:0">{project.name}</h1>
    <div class="row">
      <a class="btn" href={`/?project=${project.id}`}>Runs</a>
      <button onclick={runPreflight} disabled={preflight === 'running'}>
        {preflight === 'running' ? 'Checking…' : 'Preflight'}
      </button>
      <button onclick={startEnvCheck} disabled={check?.status === 'running'}>
        {check?.status === 'running' ? 'Envcheck running…' : 'Envcheck'}
      </button>
      <label class="row small muted" style="margin:0; gap:4px">
        <input type="checkbox" bind:checked={checkKeep} style="width:auto" /> keep workspace
      </label>
      <button onclick={() => (editing = !editing)}>{editing ? 'Close editor' : 'Edit'}</button>
      <button class="danger" onclick={remove}>Delete</button>
    </div>
  </div>

  {#if error}<div class="error">{error}</div>{/if}
  {#if checkError}<div class="error">{checkError}</div>{/if}

  {#if check}
    <div class="card">
      <h2>
        Envcheck
        {#if check.status === 'running'}— running…{:else if check.status === 'pass'}— all good ✓{:else}— failed{/if}
      </h2>
      <p class="muted small" style="margin-top:-6px">
        Provisions a throwaway workspace and proves the derived contract: image, firewall, egress,
        harness, shim, tunnel.
      </p>
      <div class="timeline">
        {#each check.steps ?? [] as s, i (i)}
          <div class="tl-item {s.status === 'pass' ? 'ok' : s.status === 'fail' ? 'bad' : 'info'}">
            <span class="mono">{s.id}</span>
            {#if s.status === 'skip'}<span class="muted small"> — skipped: {s.detail}</span>
            {:else if s.status === 'fail'}<div class="error">{s.detail}</div>
            {:else if s.detail}<span class="muted small"> — {s.detail}</span>{/if}
          </div>
        {/each}
        {#if check.status === 'running'}<div class="tl-item"><span class="muted">…</span></div>{/if}
      </div>
    </div>
  {/if}

  {#if preflight && preflight !== 'running'}
    <div class="card">
      <h2>Preflight {preflight.ok ? '— all good ✓' : '— failed'}</h2>
      <div class="timeline">
        {#each preflight.steps as s (s.id)}
          <div class="tl-item {s.ok ? 'ok' : 'bad'}">
            <span class="mono">{s.id}</span>
            {#if !s.ok}<div class="error">{s.error}</div>{/if}
          </div>
        {/each}
      </div>
    </div>
  {/if}

  {#if editing}
    <ProjectForm
      {integrations}
      {flows}
      existing={project}
      onsaved={(p) => {
        project = p;
        editing = false;
      }}
    />
  {:else}
    <AgentsPanel {project} />
    <div class="card">
      <h2>Repository</h2>
      <div class="mono small">{project.repo.url}</div>
      <div class="small muted">
        locator {project.repo.locator} · branch {project.repo.default_branch} · {project.repo.agents_md_path}
      </div>
    </div>
    <div class="card">
      <h2>Configuration</h2>
      <table>
        <tbody>
          <tr><td class="muted">Forge</td><td>{integrations.find((i) => i.id === project!.forge_id)?.name ?? project.forge_id}</td></tr>
          <tr><td class="muted">Provisioner</td><td>{integrations.find((i) => i.id === project!.provisioner_id)?.name ?? project.provisioner_id}</td></tr>
          <tr><td class="muted">Harnesses</td><td>{(project.harness_ids ?? []).map((id) => integrations.find((i) => i.id === id)?.name ?? id).join(', ') || '—'}</td></tr>
          <tr><td class="muted">Default flow</td><td class="mono">{project.default_flow}</td></tr>
          <tr><td class="muted">Extra domains</td><td class="mono">{(project.workspace.extra_domains ?? []).join(', ') || '—'}</td></tr>
        </tbody>
      </table>
    </div>
    <div class="card">
      <h2>Commands</h2>
      <table>
        <tbody>
          {#each Object.entries(project.commands) as [k, v] (k)}
            <tr><td class="mono muted" style="width:140px">{k}</td><td class="mono">{v}</td></tr>
          {:else}
            <tr><td class="muted">No commands — bootstrap setup is skipped; flows using run_ref will fail preflight.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{:else if error}
  <div class="error">{error}</div>
{:else}
  <div class="empty">Loading…</div>
{/if}
