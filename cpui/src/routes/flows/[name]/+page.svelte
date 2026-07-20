<script lang="ts">
  import { api, ago, type FlowRow, type FlowVersion, type Integration, type ModelInfo } from '$lib/api';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';

  let flow = $state<FlowRow | null>(null);
  let yaml = $state('');
  let versions = $state<FlowVersion[]>([]);
  let error = $state('');
  let busy = $state(false);
  let validation = $state<{ ok: boolean; detail: string } | null>(null);

  // Model browser: the harness knows which `provider/model` strings are
  // routable with its configured auth.
  let harnesses = $state<Integration[]>([]);
  let modelHarness = $state('');
  let models = $state<ModelInfo[] | null>(null);
  let modelsError = $state('');
  let modelsLoading = $state(false);
  let modelFilter = $state('');
  let copied = $state('');

  const load = async () => {
    try {
      flow = await api.flow(page.params.name!);
      yaml = flow.yaml;
      versions = await api.flowVersions(flow.name);
      const ints = await api.integrations();
      harnesses = ints.filter((i) => i.port === 'harness');
      if (harnesses.length > 0) modelHarness = harnesses[0].id;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  };
  onMount(load);

  async function loadModels() {
    modelsLoading = true;
    modelsError = '';
    models = null;
    try {
      models = await api.models(modelHarness);
    } catch (e) {
      modelsError = e instanceof Error ? e.message : String(e);
    } finally {
      modelsLoading = false;
    }
  }

  const filteredModels = $derived(
    (models ?? []).filter(
      (m) =>
        !modelFilter.trim() ||
        m.id.toLowerCase().includes(modelFilter.toLowerCase()) ||
        m.name.toLowerCase().includes(modelFilter.toLowerCase())
    )
  );

  async function copyModel(id: string) {
    try {
      await navigator.clipboard.writeText(id);
      copied = id;
      setTimeout(() => (copied = ''), 1500);
    } catch {
      /* clipboard unavailable; the string is visible to select manually */
    }
  }

  const dirty = $derived(flow !== null && yaml !== flow.yaml);

  async function validate() {
    validation = null;
    try {
      const r = await api.validateFlow(yaml);
      validation = r.ok
        ? { ok: true, detail: `valid — ${r.stages} stages, hash ${r.hash?.slice(0, 12)}` }
        : { ok: false, detail: (r.errors ?? []).join('\n') };
    } catch (e) {
      validation = { ok: false, detail: e instanceof Error ? e.message : String(e) };
    }
  }

  async function save() {
    if (!flow) return;
    busy = true;
    error = '';
    try {
      flow = await api.saveFlow(flow.name, yaml);
      yaml = flow.yaml;
      validation = { ok: true, detail: 'saved' };
      versions = await api.flowVersions(flow.name);
    } catch (e) {
      validation = { ok: false, detail: e instanceof Error ? e.message : String(e) };
    } finally {
      busy = false;
    }
  }

  async function loadVersion(hash: string) {
    try {
      const v = await api.flowVersion(hash);
      yaml = v.yaml; // into the editor; Save makes it current again
      validation = { ok: true, detail: `loaded version ${hash.slice(0, 12)} into the editor — Save to restore it` };
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function remove() {
    if (!flow) return;
    if (!confirm(`Delete flow "${flow.name}"? Versions remain retrievable by hash.`)) return;
    try {
      await api.deleteFlow(flow.name);
      await goto('/flows');
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }
</script>

{#if flow}
  <div class="row spread" style="margin-bottom:16px">
    <h1 style="margin:0" class="mono">{flow.name}</h1>
    <div class="row">
      <button onclick={validate}>Validate</button>
      <button class="primary" onclick={save} disabled={busy || !dirty}>{busy ? 'Saving…' : 'Save'}</button>
      <button class="danger" onclick={remove}>Delete</button>
    </div>
  </div>

  {#if error}<div class="error">{error}</div>{/if}

  <textarea class="mono" style="min-height:380px; font-size:12.5px; line-height:1.6" bind:value={yaml}
    spellcheck="false"></textarea>
  {#if validation}
    <div class="card" style="margin-top:10px; border-color:{validation.ok ? 'var(--green)' : 'var(--red)'}">
      <pre style="margin:0; white-space:pre-wrap" class="small {validation.ok ? '' : 'error'}">{validation.detail}</pre>
    </div>
  {/if}
  {#if dirty}<div class="muted small" style="margin-top:6px">Unsaved changes — runs keep using the last saved version.</div>{/if}

  <h2 style="margin-top:24px">Available models</h2>
  <p class="muted small" style="margin-top:-6px">
    The exact strings for <code>defaults.model</code> / stage <code>model:</code>, as routable by your
    harness's configured auth. Click a row to copy.
  </p>
  <div class="card">
    {#if harnesses.length === 0}
      <div class="muted small">No harness integration configured yet.</div>
    {:else}
      <div class="row">
        <select bind:value={modelHarness} style="width:auto">
          {#each harnesses as h (h.id)}<option value={h.id}>{h.name} ({h.kind})</option>{/each}
        </select>
        <button onclick={loadModels} disabled={modelsLoading}>
          {modelsLoading ? 'Loading…' : models ? 'Reload' : 'Load models'}
        </button>
        {#if models}
          <input placeholder="filter…" bind:value={modelFilter} style="flex:1; max-width:280px" />
          <span class="muted small">{filteredModels.length}/{models.length}</span>
        {/if}
      </div>
      {#if modelsError}<div class="error">{modelsError}</div>{/if}
      {#if models}
        <div style="max-height:260px; overflow-y:auto; margin-top:10px; border:1px solid var(--border); border-radius:8px">
          <table>
            <tbody>
              {#each filteredModels as m (m.id)}
                <tr class="click" onclick={() => copyModel(m.id)}>
                  <td class="mono">{m.id}{#if copied === m.id}<span class="small" style="color:var(--green)"> copied ✓</span>{/if}</td>
                  <td class="muted small">{m.name}</td>
                </tr>
              {:else}
                <tr><td class="muted">No models match.</td></tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    {/if}
  </div>

  <h2 style="margin-top:24px">Versions</h2>
  <p class="muted small" style="margin-top:-6px">
    Content-addressed and immutable; every run pins the hash it was created under.
  </p>
  <div class="card" style="padding:4px 0">
    <table>
      <tbody>
        {#each versions as v (v.hash)}
          <tr>
            <td class="mono">{v.hash.slice(0, 12)}
              {#if v.hash === flow.hash}<span class="chip" style="margin-left:8px">current</span>{/if}
            </td>
            <td class="muted small">{ago(v.created_at)}</td>
            <td style="text-align:right">
              <button onclick={() => loadVersion(v.hash)}>Load into editor</button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{:else if error}
  <div class="error">{error}</div>
{:else}
  <div class="empty">Loading…</div>
{/if}
