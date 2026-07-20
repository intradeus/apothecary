<script lang="ts">
  import type { Selection } from '$lib/FlowGraph.svelte';
  import {
    DONE,
    EDGE_KINDS,
    edgeOf,
    setEdge,
    freshStage,
    type EdgeKind,
    type FlowDoc,
    type StageKind
  } from '$lib/flowdoc';
  import type { Agent, ModelInfo } from '$lib/api';

  let {
    doc,
    selection,
    agents,
    models,
    commands,
    onchange,
    onselect
  }: {
    doc: FlowDoc;
    selection: Selection;
    agents: Agent[];
    models: ModelInfo[];
    commands: string[];
    onchange: () => void;
    onselect: (sel: Selection) => void;
  } = $props();

  const stage = $derived(
    selection?.kind === 'stage' ? doc.stages.find((s) => s.id === selection.id) : undefined
  );
  const edgeSel = $derived(
    selection?.kind === 'edge'
      ? { stage: doc.stages.find((s) => s.id === selection.stage), kind: selection.edge as EdgeKind }
      : undefined
  );
  const stageIds = $derived(doc.stages.map((s) => s.id));

  function addStage(type: StageKind) {
    const s = freshStage(doc, type);
    doc.stages.push(s);
    onchange();
    onselect({ kind: 'stage', id: s.id });
  }

  function renameStage(oldId: string, newId: string) {
    newId = newId.trim();
    if (!newId || newId === oldId || newId === DONE) return;
    if (doc.stages.some((s) => s.id === newId)) return;
    for (const s of doc.stages) {
      if (s.id === oldId) s.id = newId;
      if (s.on_success === oldId) s.on_success = newId;
      for (const kind of EDGE_KINDS) {
        const e = edgeOf(s, kind);
        if (e && e.to === oldId) e.to = newId;
      }
    }
    onchange();
    onselect({ kind: 'stage', id: newId });
  }

  function deleteStage(id: string) {
    if (!confirm(`Delete stage "${id}"?`)) return;
    doc.stages = doc.stages.filter((s) => s.id !== id);
    for (const s of doc.stages) {
      if (s.on_success === id) s.on_success = DONE;
      for (const kind of EDGE_KINDS) {
        const e = edgeOf(s, kind);
        if (e && e.to === id) setEdge(s, kind, undefined);
      }
    }
    onchange();
    onselect(null);
  }

  function addEdge(s: NonNullable<typeof stage>, kind: EdgeKind) {
    setEdge(s, kind, { to: doc.stages[0]?.id ?? '', max: 3 });
    onchange();
    onselect({ kind: 'edge', stage: s.id, edge: kind });
  }

  function feedOptions(kind: EdgeKind, type: StageKind): string[] {
    if (kind === 'on_fail') return ['', 'output'];
    return type === 'agent' ? ['', 'comments'] : type === 'human_gate' ? ['', 'feedback'] : [''];
  }
</script>

<datalist id="fl-models">
  {#each models as m (m.id)}<option value={m.id}>{m.name}</option>{/each}
</datalist>
<datalist id="fl-agents">
  {#each agents as a (a.name)}<option value={a.name}>{a.description}</option>{/each}
</datalist>
<datalist id="fl-commands">
  {#each commands as c (c)}<option value={c}></option>{/each}
</datalist>

<div class="card inspector">
  {#if stage}
    {@const s = stage}
    <div class="row spread">
      <h2 style="margin:0">Stage</h2>
      <button class="danger" onclick={() => deleteStage(s.id)}>Delete</button>
    </div>
    <label for="st-id">Id</label>
    <input id="st-id" value={s.id} onchange={(e) => renameStage(s.id, e.currentTarget.value)} />
    <div class="muted small">type: <span class="mono">{s.type}</span></div>

    {#if s.type === 'agent'}
      <label for="st-agent">Agent <span class="muted">— from the context project's library</span></label>
      <input id="st-agent" list="fl-agents" bind:value={s.agent} oninput={onchange} placeholder="builder" />
      <label for="st-model">Model <span class="muted">— empty = flow default</span></label>
      <input id="st-model" list="fl-models" bind:value={s.model} oninput={onchange} placeholder={doc.defaults.model} />
      <label for="st-harness">Harness pin <span class="muted">— usually empty (project's harness)</span></label>
      <input id="st-harness" bind:value={s.harness} oninput={onchange} placeholder="" />
      <div class="row" style="gap:12px">
        <div style="flex:1">
          <label for="st-turns">Max turns</label>
          <input
            id="st-turns"
            type="number"
            value={s.max_turns ?? ''}
            oninput={(e) => {
              s.max_turns = e.currentTarget.value === '' ? undefined : Number(e.currentTarget.value);
              onchange();
            }}
          />
        </div>
        <div style="flex:1">
          <label for="st-timeout">Timeout</label>
          <input id="st-timeout" bind:value={s.timeout} oninput={onchange} placeholder="30m" />
        </div>
      </div>
      <label for="st-input">Input <span class="muted">— comma-separated: ticket, artifact path, $CHANGED</span></label>
      <input
        id="st-input"
        value={(s.input ?? []).join(', ')}
        oninput={(e) => {
          const v = e.currentTarget.value
            .split(',')
            .map((x) => x.trim())
            .filter(Boolean);
          s.input = v.length ? v : undefined;
          onchange();
        }}
      />
      <label for="st-output">Output artifact</label>
      <input id="st-output" bind:value={s.output} oninput={onchange} placeholder="artifacts/plan.md" />
      <label class="row small" style="gap:6px; margin-top:6px">
        <input
          type="checkbox"
          style="width:auto"
          checked={s.verdict ?? false}
          onchange={(e) => {
            s.verdict = e.currentTarget.checked || undefined;
            onchange();
          }}
        />
        verdict (writes artifacts/verdict.json; REJECT routes on_reject)
      </label>
    {:else if s.type === 'command'}
      <label for="st-runref">run_ref <span class="muted">— from the context project's commands</span></label>
      <input id="st-runref" list="fl-commands" bind:value={s.run_ref} oninput={onchange} placeholder="verify" />
      <label for="st-run">…or literal script</label>
      <textarea id="st-run" bind:value={s.run} oninput={onchange} rows="3" placeholder="make test"></textarea>
    {:else if s.type === 'human_gate'}
      <label for="st-ping">Ping</label>
      <select id="st-ping" bind:value={s.ping} onchange={onchange}>
        <option value="plan_approval">plan_approval</option>
        <option value="final_review">final_review</option>
      </select>
      <label for="st-payload">Payload <span class="muted">— artifact path or mr_url</span></label>
      <input id="st-payload" bind:value={s.payload} oninput={onchange} placeholder="artifacts/plan.md" />
      <label for="st-auto">Auto-complete</label>
      <select
        id="st-auto"
        value={s.auto_complete ?? ''}
        onchange={(e) => {
          s.auto_complete = e.currentTarget.value || undefined;
          onchange();
        }}
      >
        <option value="">—</option>
        <option value="mr_merged">mr_merged</option>
      </select>
    {:else if s.type === 'forge_mr'}
      <label for="st-body">Body artifact</label>
      <input id="st-body" bind:value={s.body} oninput={onchange} placeholder="artifacts/mr.md" />
    {/if}

    <label for="st-succ">On success</label>
    <select id="st-succ" bind:value={s.on_success} onchange={onchange}>
      <option value={DONE}>done (complete the run)</option>
      {#each stageIds.filter((id) => id !== s.id) as id (id)}<option value={id}>{id}</option>{/each}
    </select>

    <div class="row" style="margin-top:10px; gap:8px">
      {#if (s.type === 'agent' || s.type === 'command') && !s.on_fail}
        <button onclick={() => addEdge(s, 'on_fail')}>+ on_fail</button>
      {/if}
      {#if (s.type === 'agent' || s.type === 'human_gate') && !s.on_reject}
        <button onclick={() => addEdge(s, 'on_reject')}>+ on_reject</button>
      {/if}
      {#each EDGE_KINDS as kind (kind)}
        {#if edgeOf(s, kind)}
          <button onclick={() => onselect({ kind: 'edge', stage: s.id, edge: kind })}>{kind} →</button>
        {/if}
      {/each}
    </div>
  {:else if edgeSel?.stage}
    {@const s = edgeSel.stage}
    {@const kind = edgeSel.kind}
    {@const e = edgeOf(s, kind)}
    {#if e}
      <div class="row spread">
        <h2 style="margin:0"><span class="mono">{kind}</span> from <span class="mono">{s.id}</span></h2>
        <button
          class="danger"
          onclick={() => {
            setEdge(s, kind, undefined);
            onchange();
            onselect({ kind: 'stage', id: s.id });
          }}>Remove</button
        >
      </div>
      <label for="ed-to">To</label>
      <select id="ed-to" bind:value={e.to} onchange={onchange}>
        {#each stageIds as id (id)}<option value={id}>{id}</option>{/each}
      </select>
      <label for="ed-max">Max <span class="muted">— attempts before the run is abandoned</span></label>
      <input
        id="ed-max"
        type="number"
        min="1"
        value={e.max ?? 1}
        oninput={(ev) => {
          e.max = Number(ev.currentTarget.value) || 1;
          onchange();
        }}
      />
      <label for="ed-feed">Feed</label>
      <select
        id="ed-feed"
        value={e.feed ?? ''}
        onchange={(ev) => {
          e.feed = ev.currentTarget.value || undefined;
          onchange();
        }}
      >
        {#each feedOptions(kind, s.type) as f (f)}<option value={f}>{f === '' ? '— nothing' : f}</option>{/each}
      </select>
    {/if}
  {:else}
    <h2 style="margin-top:0">Flow</h2>
    <label for="fl-desc">Description</label>
    <input
      id="fl-desc"
      value={doc.description ?? ''}
      oninput={(e) => {
        doc.description = e.currentTarget.value || undefined;
        onchange();
      }}
    />
    <label for="fl-model">Default model</label>
    <input id="fl-model" list="fl-models" bind:value={doc.defaults.model} oninput={onchange} placeholder="openrouter/anthropic/claude-sonnet-4.6" />
    <div class="row" style="gap:12px">
      <div style="flex:1">
        <label for="fl-timeout">Default timeout</label>
        <input id="fl-timeout" bind:value={doc.defaults.timeout} oninput={onchange} placeholder="30m" />
      </div>
      <div style="flex:1">
        <label for="fl-turns">Default max turns</label>
        <input
          id="fl-turns"
          type="number"
          value={doc.defaults.max_turns ?? ''}
          oninput={(e) => {
            doc.defaults.max_turns = e.currentTarget.value === '' ? undefined : Number(e.currentTarget.value);
            onchange();
          }}
        />
      </div>
    </div>
    <div class="row" style="gap:12px">
      <div style="flex:1">
        <label for="fl-cost">Budget: max cost (USD)</label>
        <input
          id="fl-cost"
          type="number"
          step="0.5"
          value={doc.budget?.max_cost_usd ?? ''}
          oninput={(e) => {
            doc.budget.max_cost_usd = e.currentTarget.value === '' ? undefined : Number(e.currentTarget.value);
            onchange();
          }}
        />
      </div>
      <div style="flex:1">
        <label for="fl-tturns">Budget: max total turns</label>
        <input
          id="fl-tturns"
          type="number"
          value={doc.budget?.max_total_turns ?? ''}
          oninput={(e) => {
            doc.budget.max_total_turns = e.currentTarget.value === '' ? undefined : Number(e.currentTarget.value);
            onchange();
          }}
        />
      </div>
    </div>
    <h2>Add stage</h2>
    <div class="row" style="gap:8px; flex-wrap:wrap">
      <button onclick={() => addStage('agent')}>+ agent</button>
      <button onclick={() => addStage('command')}>+ command</button>
      <button onclick={() => addStage('human_gate')}>+ human gate</button>
      <button onclick={() => addStage('forge_mr')}>+ forge MR</button>
    </div>
    <p class="muted small" style="margin-top:10px">
      Click a node or edge to edit it. Drag from a node's colored handles to rewire: green = success,
      red = on_fail, orange = on_reject.
    </p>
  {/if}
</div>

<style>
  .inspector {
    min-width: 280px;
  }
</style>
