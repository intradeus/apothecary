<script lang="ts">
  // Ticket create/edit form. A ticket is a unit of work; flow is NOT chosen
  // here — it's picked per run when a run is started.
  //
  // A new ticket can also be COPIED from a tracker (Jira): pick a source,
  // search it, and the chosen issue prefills this form. The copy is taken on
  // submit — title/goal are whatever the fields say by then, and the issue's
  // attachments come across as ticket attachments.
  import {
    api,
    fmtBytes,
    type ArtifactMeta,
    type Integration,
    type Project,
    type SourceTicket,
    type Ticket
  } from '$lib/api';
  import ArtifactList from '$lib/ArtifactList.svelte';
  import AttachFiles from '$lib/AttachFiles.svelte';
  import { onMount } from 'svelte';

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

  // --- import from a tracker (create only) ---
  let sources = $state<Integration[]>([]);
  let sourceId = $state(''); // '' = write it by hand
  let query = $state('');
  let results = $state<SourceTicket[]>([]);
  let searching = $state(false);
  let searchErr = $state('');
  let picked = $state<SourceTicket | null>(null); // the issue being copied

  // Already-attached files, shown when editing so the list isn't invisible
  // state the form silently round-trips.
  let attached = $state<ArtifactMeta[]>([]);

  onMount(async () => {
    if (init) {
      // Provenance is immutable, so editing never re-imports — but it should
      // still show what the ticket carries.
      api.ticketArtifacts(init.id).then((a) => (attached = a)).catch(() => {});
      return;
    }
    try {
      sources = (await api.integrations()).filter((i) => i.port === 'ticketsource');
    } catch {
      /* no tracker configured is the normal case */
    }
  });

  async function search() {
    if (!sourceId) return;
    searching = true;
    searchErr = '';
    try {
      results = await api.sourceTickets(sourceId, query.trim());
    } catch (e) {
      searchErr = e instanceof Error ? e.message : String(e);
      results = [];
    } finally {
      searching = false;
    }
  }

  function openSource(id: string) {
    sourceId = id;
    results = [];
    searchErr = '';
    search(); // the empty query is the tracker's own "what's on my plate"
  }

  // Picking an issue fills the form from the full issue (search results carry
  // no attachments, so this re-fetches). Everything stays editable — the
  // import is a starting point, not a lock.
  async function pick(t: SourceTicket) {
    searchErr = '';
    try {
      picked = await api.sourceTicket(sourceId, t.key);
    } catch (e) {
      searchErr = e instanceof Error ? e.message : String(e);
      return;
    }
    title = picked.title ?? '';
    goal = picked.body ?? '';
    if (!branchEdited) branch = `${picked.key.toLowerCase()}-${slugify(picked.title ?? '')}`;
    results = [];
    query = '';
  }

  function clearImport() {
    picked = null;
    sourceId = '';
    results = [];
    query = '';
  }

  const sourceName = (id: string) => sources.find((s) => s.id === id)?.name ?? id;

  // Prefill the branch from a slug of the title until the user edits it.
  let branchEdited = $state(init != null);
  // The WHOLE title: a git branch has no meaningful length limit, and the one
  // thing that does — the run's container name — is derived and truncated
  // separately by the engine (engine.workspaceName), so nothing is gained by
  // cutting the readable name short here.
  const slugify = (s: string) =>
    s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
  $effect(() => {
    if (!branchEdited && !picked) branch = slugify(title);
  });

  let submitting = $state(false);
  let error = $state('');
  // An import that dropped an attachment stops here with the reason rather
  // than navigating away as if the copy were complete; the ticket exists
  // either way, so the operator moves on when they've read it.
  let warnings = $state<string[]>([]);
  let created = $state<Ticket | null>(null);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    submitting = true;
    error = '';
    warnings = [];
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
        artifact_ids: artifactIds,
        ...(picked ? { source_id: sourceId, source_key: picked.key } : {})
      };
      const t = init
        ? await api.updateTicket(init.id, body)
        : await api.createTicket(body);
      if (t.warnings?.length) {
        warnings = t.warnings;
        created = t;
        return;
      }
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

  {#if !init && sources.length}
    <div class="import">
      {#if picked}
        <div class="row spread">
          <div>
            <span class="chip">{picked.key}</span>
            <span class="small muted">from {sourceName(sourceId)}</span>
            {#if picked.url}
              <a class="small" href={picked.url} target="_blank" rel="noreferrer">open in tracker ↗</a>
            {/if}
          </div>
          <button type="button" class="small" onclick={clearImport}>Clear</button>
        </div>
        <!-- Name every file that will be copied — and say so plainly when the
             issue has none, so "where are my attachments?" is answered before
             the ticket is created rather than after. -->
        {#if picked.attachments?.length}
          <div class="small muted">
            {picked.attachments.length} file{picked.attachments.length > 1 ? 's' : ''} will be copied into the ticket:
          </div>
          <div class="chips">
            {#each picked.attachments as a (a.filename)}
              <span class="chip" title={a.mime ?? ''}>
                📄 {a.filename}
                {#if a.size}<span class="muted">· {fmtBytes(a.size)}</span>{/if}
              </span>
            {/each}
          </div>
        {:else}
          <div class="small muted">This issue has no attachments.</div>
        {/if}
      {:else if sourceId}
        <div class="row" style="gap:8px">
          <input
            bind:value={query}
            placeholder="Search the tracker — 2624, PROJ-2624, words, or jql:…"
            onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); search(); } }}
          />
          <button type="button" onclick={search} disabled={searching}>
            {searching ? 'Searching…' : 'Search'}
          </button>
          <button type="button" class="small" onclick={clearImport}>Cancel</button>
        </div>
        {#if searchErr}<div class="error">{searchErr}</div>{/if}
        {#if results.length}
          <div class="results">
            {#each results as r (r.key)}
              <button type="button" class="result" onclick={() => pick(r)}>
                <span class="chip mono">{r.key}</span>
                <span class="rtitle">{r.title}</span>
                <span class="small muted">
                  {r.type ?? ''}{r.status ? ` · ${r.status}` : ''}{r.assignee ? ` · ${r.assignee}` : ''}
                </span>
              </button>
            {/each}
          </div>
        {:else if !searching && !searchErr}
          <div class="muted small">No matching issues.</div>
        {/if}
      {:else}
        <div class="row" style="gap:8px; align-items:center">
          <span class="small muted">Start from a tracker issue:</span>
          {#each sources as s (s.id)}
            <button type="button" class="small" onclick={() => openSource(s.id)}>
              ⇩ {s.name} <span class="muted">({s.kind})</span>
            </button>
          {/each}
        </div>
      {/if}
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
  {#if attached.length}
    <ArtifactList items={attached} label="Already attached — delivered into every run's workspace" />
  {/if}
  <AttachFiles bind:files label="Attach documents / images for the agent to read" />
  <label for="t-branch">Base branch <span class="muted">— prefilled from the title; each run appends a short unique suffix</span></label>
  <input id="t-branch" bind:value={branch} oninput={() => (branchEdited = true)}
    placeholder="feat/PROJ-123-short-description" class="mono" />
  {#if error}<div class="error">{error}</div>{/if}
  {#if warnings.length && created}
    <div class="error">
      Ticket created, but some attachments did not come across:
      <ul>{#each warnings as w (w)}<li>{w}</li>{/each}</ul>
    </div>
    <div class="row" style="margin-top:14px">
      <button class="primary" type="button" onclick={() => onsaved(created!)}>Continue to the ticket</button>
    </div>
  {:else}
    <div class="row" style="margin-top:14px; gap:8px">
      <button class="primary" type="submit" disabled={submitting || !title.trim() || !goal.trim() || !projectId}>
        {submitting ? 'Saving…' : init ? 'Save ticket' : picked ? `Create from ${picked.key}` : 'Create ticket'}
      </button>
    </div>
  {/if}
</form>

<style>
  .import {
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 10px;
    margin-bottom: 14px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .results {
    display: flex;
    flex-direction: column;
    max-height: 260px;
    overflow-y: auto;
  }
  .result {
    display: flex;
    align-items: baseline;
    gap: 8px;
    text-align: left;
    background: none;
    border: none;
    border-bottom: 1px solid var(--border);
    padding: 6px 2px;
    cursor: pointer;
  }
  .result:hover {
    background: var(--hover, rgba(127, 127, 127, 0.12));
  }
  .rtitle {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
