<script lang="ts">
  import { api, stateColors, ago, type RunView } from '$lib/api';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';

  let runs = $state<RunView[]>([]);
  let loaded = $state(false);
  let showForm = $state(false);
  let title = $state('');
  let goal = $state('');
  let flow = $state('v1');
  let submitting = $state(false);
  let error = $state('');

  const refresh = async () => {
    try {
      runs = await api.runs();
      loaded = true;
    } catch {
      /* keep last */
    }
  };

  onMount(() => {
    refresh();
    const t = setInterval(refresh, 4000);
    return () => clearInterval(t);
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    submitting = true;
    error = '';
    try {
      const { run_id } = await api.createTicket(title.trim(), goal.trim(), flow.trim() || undefined);
      title = goal = '';
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
  <h1 style="margin:0">Runs</h1>
  <button class="primary" onclick={() => (showForm = !showForm)}>
    {showForm ? 'Cancel' : '+ New ticket'}
  </button>
</div>

{#if showForm}
  <form class="card" onsubmit={submit}>
    <h2>New ticket</h2>
    <label for="t-title">Title</label>
    <input id="t-title" bind:value={title} required placeholder="Add a Farewell function" />
    <label for="t-goal">Goal</label>
    <textarea id="t-goal" bind:value={goal} required placeholder="What should the factory build, and what does done look like?"></textarea>
    <label for="t-flow">Flow</label>
    <input id="t-flow" bind:value={flow} />
    {#if error}<div class="error">{error}</div>{/if}
    <div style="margin-top:14px">
      <button class="primary" type="submit" disabled={submitting || !title.trim() || !goal.trim()}>
        {submitting ? 'Creating…' : 'Create run'}
      </button>
    </div>
  </form>
{/if}

{#if runs.length === 0}
  <div class="empty">{loaded ? 'No runs yet — create the first ticket.' : 'Loading…'}</div>
{:else}
  <div class="card" style="padding:4px 0">
    <table>
      <thead>
        <tr><th>Ticket</th><th>State</th><th>Stage</th><th>Flow</th><th>MR</th><th>Updated</th></tr>
      </thead>
      <tbody>
        {#each runs as r (r.run_id)}
          <tr class="click" onclick={() => goto(`/runs/${r.run_id}`)}>
            <td>{r.ticket_title}<div class="mono muted">{r.run_id}</div></td>
            <td>
              <span class="chip"><span class="dot" style="background:{stateColors[r.state]}"></span>{r.state}</span>
            </td>
            <td class="mono">{r.current_stage ?? ''}</td>
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
