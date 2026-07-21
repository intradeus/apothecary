<script lang="ts">
  import { api, ago, type Agent, type FlowRow, type FlowVersion, type Integration, type ModelInfo, type Project } from '$lib/api';
  import FlowGraph, { type Selection } from '$lib/FlowGraph.svelte';
  import FlowInspector from '$lib/FlowInspector.svelte';
  import { autoLayout, docToYaml, DONE, edgeOf, setEdge, type EdgeKind, type FlowDoc } from '$lib/flowdoc';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';

  let flow = $state<FlowRow | null>(null);
  let yaml = $state('');
  let doc = $state<FlowDoc | null>(null);
  let layout = $state<Record<string, { x: number; y: number }>>({});
  let rev = $state(0);
  let tab = $state<'graph' | 'yaml'>('graph');
  let graphDirty = $state(false);
  let selection = $state<Selection>(null);
  let versions = $state<FlowVersion[]>([]);
  let error = $state('');
  let busy = $state(false);
  let validation = $state<{ ok: boolean; detail: string } | null>(null);

  // Editing context (DESIGN3 §2.4): a project supplies the dropdowns —
  // agents from its library, models from its harness auth, run_refs from
  // its commands. Free text always works; flows stay portable.
  let projects = $state<Project[]>([]);
  let integrations = $state<Integration[]>([]);
  let contextId = $state('');
  let agents = $state<Agent[]>([]);
  // Per-step harness: models are fetched per harness integration and keyed by
  // kind, so a stage's model list scopes to the harness it pins.
  let modelsByKind = $state<Record<string, ModelInfo[]>>({});
  const contextProject = $derived(projects.find((p) => p.id === contextId));
  const commands = $derived(Object.keys(contextProject?.commands ?? {}));
  const harnessIdsOf = (p: Project | undefined) =>
    p ? (p.harness_ids?.length ? p.harness_ids : p.harness_id ? [p.harness_id] : []) : [];
  const defaultHarnessKind = $derived(
    integrations.find((i) => i.id === contextProject?.harness_id)?.kind ?? ''
  );
  const harnessKinds = $derived.by(() => {
    const ints = integrations;
    const kinds = harnessIdsOf(contextProject)
      .map((id) => ints.find((i) => i.id === id)?.kind)
      .filter((k): k is string => !!k);
    return [...new Set(kinds)];
  });

  const dirty = $derived(flow !== null && (graphDirty || yaml !== flow.yaml));

  const load = async () => {
    try {
      flow = await api.flow(page.params.name!);
      yaml = flow.yaml;
      versions = await api.flowVersions(flow.name);
      const r = await api.validateFlow(flow.yaml);
      if (r.ok && r.doc) {
        doc = r.doc;
        layout = flow.layout?.nodes ?? autoLayout(doc);
        rev++;
      } else {
        tab = 'yaml';
        validation = { ok: false, detail: (r.errors ?? []).join('\n') };
      }
      projects = await api.projects();
      integrations = await api.integrations().catch(() => []);
      const def = projects.find((p) => p.default_flow === flow!.name) ?? projects[0];
      if (def) {
        contextId = def.id;
      }
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  };
  onMount(load);

  // Context loads are best-effort: dropdown assistance, not a gate.
  $effect(() => {
    const p = projects.find((x) => x.id === contextId);
    const ints = integrations; // read synchronously so this re-runs once loaded
    if (!p) return;
    api.agents(p.id).then((a) => (agents = a)).catch(() => (agents = []));
    // Fetch each harness's live model list, keyed by kind (per-step harness).
    const ids = harnessIdsOf(p);
    const map: Record<string, ModelInfo[]> = {};
    Promise.all(
      ids.map(async (id) => {
        const kind = ints.find((i) => i.id === id)?.kind;
        if (!kind) return;
        try {
          map[kind] = await api.models(id);
        } catch {
          map[kind] = [];
        }
      })
    ).then(() => (modelsByKind = map));
  });

  function currentYAML(): string {
    if (doc && (tab === 'graph' || graphDirty)) return docToYaml(doc);
    return yaml;
  }

  function markChanged() {
    graphDirty = true;
    rev++;
  }

  function switchTab(to: 'graph' | 'yaml') {
    if (to === tab) return;
    if (to === 'yaml') {
      if (doc && graphDirty) yaml = docToYaml(doc);
      tab = 'yaml';
      return;
    }
    // yaml -> graph: re-parse through the server (single validation source).
    api
      .validateFlow(yaml)
      .then((r) => {
        if (r.ok && r.doc) {
          doc = r.doc;
          for (const s of doc.stages) if (!(s.id in layout)) layout[s.id] = { x: 120, y: 240 };
          graphDirty = false;
          validation = null;
          rev++;
          tab = 'graph';
        } else {
          validation = { ok: false, detail: (r.errors ?? []).join('\n') || 'invalid flow' };
        }
      })
      .catch((e) => (validation = { ok: false, detail: String(e) }));
  }

  function onlink(source: string, handle: string, target: string) {
    if (!doc) return;
    const s = doc.stages.find((x) => x.id === source);
    if (!s) return;
    if (handle === 'success') {
      if (target !== s.id) s.on_success = target;
    } else {
      const kind = handle as EdgeKind;
      if (target === DONE) return; // recoverable edges point at stages, not done
      const existing = edgeOf(s, kind);
      setEdge(s, kind, { to: target, max: existing?.max ?? 3, feed: existing?.feed });
    }
    markChanged();
  }

  let layoutTimer: ReturnType<typeof setTimeout> | undefined;
  function onmove(id: string, x: number, y: number) {
    layout[id] = { x, y };
    // Layout-only autosave (DESIGN3 §2.2): same YAML = same hash = no new
    // version; deferred while real edits are pending (they save together).
    clearTimeout(layoutTimer);
    layoutTimer = setTimeout(async () => {
      if (!flow || dirty) return;
      try {
        flow = await api.saveFlow(flow.name, flow.yaml, { nodes: layout });
      } catch {
        /* next explicit save carries it */
      }
    }, 800);
  }

  async function validate() {
    validation = null;
    try {
      const r = await api.validateFlow(currentYAML());
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
      const y = currentYAML();
      flow = await api.saveFlow(flow.name, y, { nodes: layout });
      yaml = flow.yaml;
      const r = await api.validateFlow(flow.yaml);
      if (r.ok && r.doc) {
        doc = r.doc;
        rev++;
      }
      graphDirty = false;
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
      yaml = v.yaml; // into the YAML tab; Save makes it current again
      tab = 'yaml';
      graphDirty = false;
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
      <div class="tabs row" style="gap:0; margin-right:8px">
        <button class:primary={tab === 'graph'} onclick={() => switchTab('graph')}>Graph</button>
        <button class:primary={tab === 'yaml'} onclick={() => switchTab('yaml')}>YAML</button>
      </div>
      <button onclick={validate}>Validate</button>
      <button class="primary" onclick={save} disabled={busy || !dirty}>{busy ? 'Saving…' : 'Save'}</button>
      <button class="danger" onclick={remove}>Delete</button>
    </div>
  </div>

  {#if error}<div class="error">{error}</div>{/if}

  {#if tab === 'graph' && doc}
    <div class="row" style="margin-bottom:8px; gap:8px; align-items:center">
      <span class="muted small">Editing context:</span>
      <select bind:value={contextId} style="width:auto">
        {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{:else}<option value="">no projects</option>{/each}
      </select>
      <span class="muted small">
        fills the agent / model / run_ref dropdowns — free text always works
      </span>
    </div>
    <div class="row" style="align-items:stretch; gap:14px">
      <div style="flex:1; min-width:0">
        <FlowGraph {doc} {layout} {rev} onselect={(s) => (selection = s)} {onmove} {onlink} />
      </div>
      <FlowInspector
        {doc}
        {selection}
        {agents}
        {modelsByKind}
        {harnessKinds}
        {defaultHarnessKind}
        {commands}
        onchange={markChanged}
        onselect={(s) => (selection = s)}
      />
    </div>
  {:else}
    <textarea class="mono" style="min-height:380px; font-size:12.5px; line-height:1.6" bind:value={yaml}
      spellcheck="false"></textarea>
  {/if}

  {#if validation}
    <div class="card" style="margin-top:10px; border-color:{validation.ok ? 'var(--green)' : 'var(--red)'}">
      <pre style="margin:0; white-space:pre-wrap" class="small {validation.ok ? '' : 'error'}">{validation.detail}</pre>
    </div>
  {/if}
  {#if dirty}<div class="muted small" style="margin-top:6px">Unsaved changes — runs keep using the last saved version.</div>{/if}

  <h2 style="margin-top:24px">Versions</h2>
  <p class="muted small" style="margin-top:-6px">
    Content-addressed and immutable; every run pins the hash it was created under. Node positions are
    display metadata and never create versions.
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
