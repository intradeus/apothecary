<script lang="ts">
  import { api, stateColors, stateTab, ago, type Project, type RunView, type RunTab } from '$lib/api';
  import WorkspaceBadge from '$lib/WorkspaceBadge.svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';

  let runs = $state<RunView[]>([]);
  let projects = $state<Project[]>([]);
  let loaded = $state(false);

  // /?project=<id> filters the list (linked from a project's detail page).
  const projectFilter = $derived(page.url.searchParams.get('project') ?? '');

  const refresh = async () => {
    try {
      runs = await api.runs(projectFilter ? { project: projectFilter } : undefined);
      loaded = true;
    } catch {
      /* keep last */
    }
  };

  onMount(() => {
    refresh();
    api
      .projects()
      .then((ps) => (projects = ps))
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

  const projectName = (id?: string) => projects.find((p) => p.id === id)?.name;
</script>

<div class="row spread" style="margin-bottom:16px">
  <h1 style="margin:0">
    Runs{#if projectFilter && projectName(projectFilter)}
      <span class="muted"> · {projectName(projectFilter)}</span>
      <a class="small" href="/">all</a>{/if}
  </h1>
  <button class="primary" onclick={() => goto('/tickets/new')}>+ New ticket</button>
</div>

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
    {#if !loaded}Loading…{:else if runs.length === 0}No runs yet — create a <a href="/tickets/new">ticket</a> and start one.
    {:else if tab === 'active'}No active runs.{:else}No finished runs.{/if}
  </div>
{:else}
  <div class="card" style="padding:4px 0">
    <table>
      <thead>
        <tr><th>Ticket</th><th>State</th><th>Workspace</th><th>Stage</th><th>Project</th><th>Flow</th><th>MR</th><th>Updated</th></tr>
      </thead>
      <tbody>
        {#each shownRuns as r (r.run_id)}
          <tr class="click" onclick={() => goto(`/runs/${r.run_id}`)}>
            <td>{r.ticket_title}<div class="mono muted">{r.run_id}</div></td>
            <td>
              <span class="chip"><span class="dot" style="background:{stateColors[r.state]}"></span>{r.state}</span>
            </td>
            <td><WorkspaceBadge state={r.state} envId={r.env_id} envStatus={r.env_status} /></td>
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
