<script lang="ts">
  // Ticket create/edit form. A ticket is a unit of work; flow is NOT chosen
  // here — it's picked per run when a run is started from the ticket.
  import { api, type Project, type Ticket } from '$lib/api';
  import AttachFiles from '$lib/AttachFiles.svelte';

  let {
    projects,
    existing = null,
    onsaved
  }: {
    projects: Project[];
    existing?: Ticket | null;
    onsaved: (t: Ticket) => void;
  } = $props();

  const init = $state.snapshot(existing) as Ticket | null;

  let projectId = $state(init?.project_id ?? projects[projects.length - 1]?.id ?? '');
  let title = $state(init?.title ?? '');
  let goal = $state(init?.goal ?? '');
  let branch = $state(init?.base_branch ?? '');
  let files = $state<File[]>([]);

  // Prefill the branch from a slug of the title until the user edits it.
  let branchEdited = $state(init != null);
  const slugify = (s: string) =>
    s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 24).replace(/-+$/, '');
  $effect(() => {
    if (!branchEdited) branch = slugify(title);
  });

  let submitting = $state(false);
  let error = $state('');

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    submitting = true;
    error = '';
    try {
      let artifactIds = init?.artifact_ids ?? [];
      if (files.length) {
        const uploaded = (await api.uploadArtifacts(files)).map((a) => a.id);
        artifactIds = [...artifactIds, ...uploaded];
      }
      const body = {
        title: title.trim(),
        goal: goal.trim(),
        project_id: projectId,
        branch: branch.trim() || undefined,
        artifact_ids: artifactIds
      };
      const t = init
        ? await api.updateTicket(init.id, body)
        : await api.createTicket(body);
      onsaved(t);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      submitting = false;
    }
  }
</script>

<form class="card" onsubmit={submit}>
  {#if projects.length === 0}
    <div class="error">
      No projects configured — set up <a href="/integrations">integrations</a> and create a
      <a href="/projects/new">project</a> first.
    </div>
  {/if}
  <label for="t-project">Project</label>
  <select id="t-project" bind:value={projectId} required disabled={init != null}>
    {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
  </select>
  <label for="t-title">Title</label>
  <input id="t-title" bind:value={title} required placeholder="Add a Farewell function" />
  <label for="t-goal">Goal</label>
  <textarea id="t-goal" bind:value={goal} required
    placeholder="What should the factory build, and what does done look like?"></textarea>
  <AttachFiles bind:files label="Attach documents / images for the agent to read" />
  <label for="t-branch">Base branch <span class="muted">— prefilled from the title; each run appends a short unique suffix</span></label>
  <input id="t-branch" bind:value={branch} oninput={() => (branchEdited = true)}
    placeholder="feat/PROJ-123-short-description" class="mono" />
  {#if error}<div class="error">{error}</div>{/if}
  <div class="row" style="margin-top:14px; gap:8px">
    <button class="primary" type="submit" disabled={submitting || !title.trim() || !goal.trim() || !projectId}>
      {submitting ? 'Saving…' : init ? 'Save ticket' : 'Create ticket'}
    </button>
  </div>
</form>
