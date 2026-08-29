<script lang="ts">
  import { api } from '$lib/api';
  import { stateColors, ago } from '$lib/format';
  import type { Attachment, Branch, Project, Run, Ticket } from '$lib/domain';
  import WorkspaceBadge from '$lib/WorkspaceBadge.svelte';
  import TicketForm from '$lib/TicketForm.svelte';
  import FileChips from '$lib/FileChips.svelte';
  import AttachFiles from '$lib/AttachFiles.svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';

  const ticketId = $derived(page.params.id ?? '');

  let ticket = $state<Ticket | null>(null);
  let projects = $state<Project[]>([]);
  let runs = $state<Run[]>([]);
  let loaded = $state(false);
  let error = $state('');
  let editing = $state(false);

  let flow = $state(''); // per-run flow; empty = project default
  let sourceBranch = $state(''); // branch to fork this run off from; '' until branches load
  let branches = $state<Branch[]>([]);
  let branchesErr = $state(''); // forge unreachable ⇒ fall back to project default
  let runGoal = $state(''); // direction for THIS attempt (additive to the ticket goal)
  let runFiles = $state<File[]>([]);
  let starting = $state(false);

  const project = $derived(projects.find((p) => p.id === ticket?.projectId));

  // The ticket's attachments — hand-uploaded or copied from a tracker. They
  // are delivered into the workspace of EVERY run of this ticket, so they
  // belong on the ticket page, not on a run's.
  let attachments = $state<Attachment[]>([]);

  const loadAttachments = async () => {
    try {
      attachments = await api.tickets.attachments(ticketId);
    } catch {
      attachments = [];
    }
  };

  const load = async () => {
    try {
      [ticket, runs] = await Promise.all([api.tickets.get(ticketId), api.runs.list({ ticket: ticketId })]);
      loaded = true;
      loadBranches();
      loadAttachments();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
      loaded = true;
    }
  };

  // Fetch the repo's branches for the source-branch picker; pre-select the
  // default branch. On failure we hide the picker and let the run start on the
  // project default (source_branch left empty).
  const loadBranches = async () => {
    if (!ticket) return;
    try {
      branches = await api.projects.branches(ticket.projectId);
      const def = branches.find((b) => b.isDefault) ?? branches[0];
      if (def && !sourceBranch) sourceBranch = def.name;
    } catch (e) {
      branchesErr = e instanceof Error ? e.message : String(e);
    }
  };

  onMount(() => {
    api.projects.list().then((ps) => (projects = ps)).catch(() => {});
    load();
    const t = setInterval(() => api.runs.list({ ticket: ticketId }).then((r) => (runs = r)).catch(() => {}), 4000);
    return () => clearInterval(t);
  });

  async function startRun() {
    starting = true;
    error = '';
    try {
      let attachmentIds: string[] | undefined;
      if (runFiles.length) attachmentIds = (await api.files.upload(runFiles)).map((a) => a.id);
      const runId = await api.tickets.startRun(ticketId, {
        flow: flow.trim() || undefined,
        goal: runGoal.trim() || undefined,
        sourceBranch: sourceBranch.trim() || undefined,
        attachmentIds
      });
      await goto(`/runs/${runId}`);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      starting = false;
    }
  }

  async function del() {
    if (!confirm('Delete this ticket? Its runs keep their history.')) return;
    try {
      await api.tickets.remove(ticketId);
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
    <a href={`/?project=${ticket.projectId}`}>{project?.name ?? ticket.projectId.slice(-6)} ↗</a>
    {#if ticket.source?.key}
      <!-- Provenance: a copy taken once. Nothing here is written back. -->
      {#if ticket.source?.url}
        <a href={ticket.source?.url} target="_blank" rel="noreferrer">
          {ticket.source?.kind ?? 'source'} {ticket.source?.key} ↗
        </a>
      {:else}
        <span>{ticket.source?.kind ?? 'source'} <span class="mono">{ticket.source?.key}</span></span>
      {/if}
    {/if}
    {#if ticket.baseBranch}<span>base branch <span class="mono">{ticket.baseBranch}</span></span>{/if}
    <span>{ago(ticket.createdAt)}</span>
  </div>

  {#if editing}
    <TicketForm {projects} existing={ticket} onsaved={(t) => { ticket = t; editing = false; loadAttachments(); }} />
  {:else}
    {#if ticket.goal}<div class="card" style="white-space:pre-wrap">{ticket.goal}</div>{/if}

    {#if attachments.length}
      <div class="card">
        <div class="row spread" style="margin-bottom:8px">
          <strong>Attachments ({attachments.length})</strong>
          <span class="small muted">delivered into every run's workspace under .apothecary/attachments/</span>
        </div>
        <FileChips items={attachments.map((a) => ({ ...a, kind: 'attachment' as const }))} />
      </div>
    {/if}

    <div class="card">
      <h2 style="margin:0 0 10px">Start a run</h2>
      <label for="run-goal">Direction for this run <span class="muted">— optional; added to the ticket's goal, never replaces it</span></label>
      <textarea id="run-goal" bind:value={runGoal} rows="2"
        placeholder="e.g. Last attempt broke the router — use the existing middleware."></textarea>
      <AttachFiles bind:files={runFiles} label="Attach files for this run only" />
      <div class="row" style="gap:8px; align-items:flex-end; margin-top:10px">
        <div style="flex:1">
          <label for="flow">Flow <span class="muted">— empty = project default{project ? ` (${project.defaultFlow})` : ''}</span></label>
          <input id="flow" bind:value={flow} placeholder={project?.defaultFlow ?? 'default'} class="mono" />
        </div>
        <div style="flex:1">
          <label for="source-branch">Source branch <span class="muted">— branch this run forks off from</span></label>
          {#if branches.length}
            <select id="source-branch" bind:value={sourceBranch} class="mono">
              {#each branches as b (b.name)}
                <option value={b.name}>{b.name}{b.isDefault ? ' (default)' : ''}</option>
              {/each}
            </select>
          {:else}
            <input id="source-branch" class="mono" disabled
              placeholder={branchesErr ? 'branches unavailable — using project default' : 'loading branches…'}
              value={project?.repo.defaultBranch ?? ''} />
          {/if}
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
          {#each runs as r (r.id)}
            <tr class="click" onclick={() => goto(`/runs/${r.id}`)}>
              <td class="mono small">{r.id}</td>
              <td><span class="chip"><span class="dot" style="background:{stateColors[r.state]}"></span>{r.state}</span></td>
              <td><WorkspaceBadge state={r.state} envId={r.envId} envStatus={r.envStatus} compact /></td>
              <td class="mono small">{r.branch ?? ''}</td>
              <td class="mono">{r.flowName}</td>
              <td class="muted small">{ago(r.updatedAt)}</td>
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
