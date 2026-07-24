<script lang="ts">
  import { api, stateColors, ago, type Project, type RunView, type Ticket } from '$lib/api';
  import WorkspaceBadge from '$lib/WorkspaceBadge.svelte';
  import TicketForm from '$lib/TicketForm.svelte';
  import AttachFiles from '$lib/AttachFiles.svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';

  const ticketId = $derived(page.params.id ?? '');

  let ticket = $state<Ticket | null>(null);
  let projects = $state<Project[]>([]);
  let runs = $state<RunView[]>([]);
  let loaded = $state(false);
  let error = $state('');
  let editing = $state(false);

  let flow = $state(''); // per-run flow; empty = project default
  let sourceBranch = $state(''); // branch to fork this run off from; empty = project default
  let runGoal = $state(''); // direction for THIS attempt (additive to the ticket goal)
  let runFiles = $state<File[]>([]);
  let starting = $state(false);

  const project = $derived(projects.find((p) => p.id === ticket?.project_id));

  const load = async () => {
    try {
      [ticket, runs] = await Promise.all([api.ticket(ticketId), api.runs({ ticket: ticketId })]);
      loaded = true;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
      loaded = true;
    }
  };

  onMount(() => {
    api.projects().then((ps) => (projects = ps)).catch(() => {});
    load();
    const t = setInterval(() => api.runs({ ticket: ticketId }).then((r) => (runs = r)).catch(() => {}), 4000);
    return () => clearInterval(t);
  });

  async function startRun() {
    starting = true;
    error = '';
    try {
      let artifactIds: string[] | undefined;
      if (runFiles.length) artifactIds = (await api.uploadArtifacts(runFiles)).map((a) => a.id);
      const { run_id } = await api.startTicketRun(ticketId, {
        flow: flow.trim() || undefined,
        goal: runGoal.trim() || undefined,
        source_branch: sourceBranch.trim() || undefined,
        artifact_ids: artifactIds
      });
      await goto(`/runs/${run_id}`);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      starting = false;
    }
  }

  async function del() {
    if (!confirm('Delete this ticket? Its runs keep their history.')) return;
    try {
      await api.deleteTicket(ticketId);
      await goto('/tickets');
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }
</script>

{#if ticket}
  <div class="row spread" style="margin-bottom:6px">
    <h1 style="margin:0">{ticket.title}</h1>
    <div class="row" style="gap:8px">
      <button onclick={() => (editing = !editing)}>{editing ? 'Cancel' : 'Edit'}</button>
      <button class="danger" onclick={del}>Delete</button>
    </div>
  </div>
  <div class="row small muted" style="margin-bottom:16px; gap:12px">
    <a href={`/?project=${ticket.project_id}`}>{project?.name ?? ticket.project_id.slice(-6)} ↗</a>
    {#if ticket.base_branch}<span>base branch <span class="mono">{ticket.base_branch}</span></span>{/if}
    <span>{ago(ticket.created_at)}</span>
  </div>

  {#if editing}
    <TicketForm {projects} existing={ticket} onsaved={(t) => { ticket = t; editing = false; }} />
  {:else}
    {#if ticket.goal}<div class="card" style="white-space:pre-wrap">{ticket.goal}</div>{/if}

    <div class="card">
      <h2 style="margin:0 0 10px">Start a run</h2>
      <label for="run-goal">Direction for this run <span class="muted">— optional; added to the ticket's goal, never replaces it</span></label>
      <textarea id="run-goal" bind:value={runGoal} rows="2"
        placeholder="e.g. Last attempt broke the router — use the existing middleware."></textarea>
      <AttachFiles bind:files={runFiles} label="Attach files for this run only" />
      <div class="row" style="gap:8px; align-items:flex-end; margin-top:10px">
        <div style="flex:1">
          <label for="flow">Flow <span class="muted">— empty = project default{project ? ` (${project.default_flow})` : ''}</span></label>
          <input id="flow" bind:value={flow} placeholder={project?.default_flow ?? 'default'} class="mono" />
        </div>
        <div style="flex:1">
          <label for="source-branch">Source branch <span class="muted">— branch off from; empty = project default{project ? ` (${project.repo.default_branch})` : ''}</span></label>
          <input id="source-branch" bind:value={sourceBranch} placeholder={project?.repo.default_branch ?? 'main'} class="mono" />
        </div>
        <button class="primary" onclick={startRun} disabled={starting}>
          {starting ? 'Starting…' : '▶ Start run'}
        </button>
      </div>
    </div>
  {/if}

  {#if error}<div class="error">{error}</div>{/if}

  <h2 style="margin:18px 0 8px">Runs ({runs.length})</h2>
  {#if runs.length === 0}
    <div class="empty">No runs yet — start one above.</div>
  {:else}
    <div class="card" style="padding:4px 0">
      <table>
        <thead>
          <tr><th>Run</th><th>State</th><th>Workspace</th><th>Branch</th><th>Flow</th><th>Updated</th></tr>
        </thead>
        <tbody>
          {#each runs as r (r.run_id)}
            <tr class="click" onclick={() => goto(`/runs/${r.run_id}`)}>
              <td class="mono small">{r.run_id}</td>
              <td><span class="chip"><span class="dot" style="background:{stateColors[r.state]}"></span>{r.state}</span></td>
              <td><WorkspaceBadge state={r.state} envId={r.env_id} envStatus={r.env_status} compact /></td>
              <td class="mono small">{r.branch ?? ''}</td>
              <td class="mono">{r.flow_name}</td>
              <td class="muted small">{ago(r.updated_at)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{:else if loaded}
  <div class="error">{error || 'Ticket not found.'}</div>
{:else}
  <div class="empty">Loading…</div>
{/if}
