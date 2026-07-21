<script lang="ts">
  import { api, stateColors, stateTab, ago, type Project, type RunView, type RunTab } from '$lib/api';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';

  let runs = $state<RunView[]>([]);
  let projects = $state<Project[]>([]);
  let loaded = $state(false);
  let showForm = $state(false);
  // Native <dialog>: modal over the list (backdrop, focus trap, Escape).
  let dialogEl = $state<HTMLDialogElement | null>(null);
  $effect(() => {
    if (!dialogEl) return;
    if (showForm && !dialogEl.open) dialogEl.showModal();
    else if (!showForm && dialogEl.open) dialogEl.close();
  });
  let title = $state('');
  let goal = $state('');
  let projectId = $state('');
  let flow = $state(''); // empty = the project's default flow
  let branch = $state(''); // empty = generated (apothecary/<id>-<slug>)
  // Prefill the branch with a slug of the title (same shape the engine
  // generates) until the user edits the field — then their text wins.
  let branchEdited = $state(false);
  const slugify = (s: string) =>
    s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 24).replace(/-+$/, '');
  $effect(() => {
    if (!branchEdited) branch = slugify(title);
  });
  let submitting = $state(false);
  let error = $state('');

  // /?project=<id> filters the list (linked from a project's detail page).
  const projectFilter = $derived(page.url.searchParams.get('project') ?? '');

  const refresh = async () => {
    try {
      runs = await api.runs(projectFilter || undefined);
      loaded = true;
    } catch {
      /* keep last */
    }
  };

  onMount(() => {
    refresh();
    api
      .projects()
      .then((ps) => {
        projects = ps;
        if (!projectId && ps.length > 0) projectId = ps[ps.length - 1].id;
      })
      .catch(() => {});
    const t = setInterval(refresh, 4000);
    return () => clearInterval(t);
  });

  $effect(() => {
    void projectFilter;
    refresh();
  });

  let tab = $state<RunTab>('active');
  const activeRuns = $derived(runs.filter((r) => stateTab[r.state] === 'active'));
  const finishedRuns = $derived(runs.filter((r) => stateTab[r.state] === 'finished'));
  const shownRuns = $derived(tab === 'active' ? activeRuns : finishedRuns);

  const selectedProject = $derived(projects.find((p) => p.id === projectId));
  const projectName = (id?: string) => projects.find((p) => p.id === id)?.name;

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    submitting = true;
    error = '';
    try {
      const { run_id } = await api.createTicket(
        title.trim(),
        goal.trim(),
        projectId,
        flow.trim() || undefined,
        branch.trim() || undefined
      );
      title = goal = branch = '';
      branchEdited = false;
      showForm = false;
      await goto(`/runs/${run_id}`);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      submitting = false;
    }
  }
</script>

<div class="row spread" style="margin-bottom:16px">
  <h1 style="margin:0">
    Runs{#if projectFilter && projectName(projectFilter)}
      <span class="muted"> · {projectName(projectFilter)}</span>
      <a class="small" href="/">all</a>{/if}
  </h1>
  <button class="primary" onclick={() => (showForm = true)}>+ New ticket</button>
</div>

<dialog
  class="ticket-modal"
  bind:this={dialogEl}
  onclose={() => (showForm = false)}
  onmousedown={(e) => {
    // A click on the ::backdrop reports the dialog itself as target.
    if (e.target === dialogEl) showForm = false;
  }}
>
  <form class="card" onsubmit={submit}>
    <h2>New ticket</h2>
    {#if projects.length === 0}
      <div class="error">
        No projects configured — set up <a href="/integrations">integrations</a> and create a
        <a href="/projects/new">project</a> first.
      </div>
    {/if}
    <label for="t-project">Project</label>
    <select id="t-project" bind:value={projectId} required>
      {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
    </select>
    <label for="t-title">Title</label>
    <input id="t-title" bind:value={title} required placeholder="Add a Farewell function" />
    <label for="t-goal">Goal</label>
    <textarea id="t-goal" bind:value={goal} required placeholder="What should the factory build, and what does done look like?"></textarea>
    <label for="t-flow">Flow <span class="muted">— empty = project default{selectedProject ? ` (${selectedProject.default_flow})` : ''}</span></label>
    <input id="t-flow" bind:value={flow} placeholder={selectedProject?.default_flow ?? 'v1'} class="mono" />
    <label for="t-branch">Branch <span class="muted">— prefilled from the title; empty = generated</span></label>
    <input id="t-branch" bind:value={branch} oninput={() => (branchEdited = true)}
      placeholder="feat/PROJ-123-short-description" class="mono" />
    {#if error}<div class="error">{error}</div>{/if}
    <div class="row" style="margin-top:14px; gap:8px">
      <button class="primary" type="submit" disabled={submitting || !title.trim() || !goal.trim() || !projectId}>
        {submitting ? 'Creating…' : 'Create run'}
      </button>
      <button type="button" onclick={() => (showForm = false)}>Cancel</button>
    </div>
  </form>
</dialog>

<div class="tabs row" style="gap:0; margin-bottom:12px">
  <button class:primary={tab === 'active'} onclick={() => (tab = 'active')}>
    Active ({activeRuns.length})
  </button>
  <button class:primary={tab === 'finished'} onclick={() => (tab = 'finished')}>
    Finished ({finishedRuns.length})
  </button>
</div>

{#if shownRuns.length === 0}
  <div class="empty">
    {#if !loaded}Loading…{:else if runs.length === 0}No runs yet — create the first ticket.
    {:else if tab === 'active'}No active runs.{:else}No finished runs.{/if}
  </div>
{:else}
  <div class="card" style="padding:4px 0">
    <table>
      <thead>
        <tr><th>Ticket</th><th>State</th><th>Stage</th><th>Project</th><th>Flow</th><th>MR</th><th>Updated</th></tr>
      </thead>
      <tbody>
        {#each shownRuns as r (r.run_id)}
          <tr class="click" onclick={() => goto(`/runs/${r.run_id}`)}>
            <td>{r.ticket_title}<div class="mono muted">{r.run_id}</div></td>
            <td>
              <span class="chip"><span class="dot" style="background:{stateColors[r.state]}"></span>{r.state}</span>
            </td>
            <td class="mono">{r.current_stage ?? ''}</td>
            <td class="small">{projectName(r.project_id) ?? (r.project_id ? r.project_id.slice(-6) : '—')}</td>
            <td class="mono">{r.flow_name}</td>
            <td>
              {#if r.mr_url}<a href={r.mr_url} target="_blank" rel="noreferrer" onclick={(e) => e.stopPropagation()}>MR ↗</a>{/if}
            </td>
            <td class="muted small">{ago(r.updated_at)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<style>
  .ticket-modal {
    width: min(560px, 92vw);
    padding: 0;
    border: 1px solid var(--border, #333);
    border-radius: 10px;
    background: transparent;
    color: inherit;
  }
  .ticket-modal::backdrop {
    background: rgba(0, 0, 0, 0.55);
  }
  .ticket-modal form {
    margin: 0;
  }
</style>
