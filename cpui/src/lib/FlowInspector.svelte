<script lang="ts">
  import type { Selection } from '$lib/FlowGraph.svelte';
  import {
    ABORT,
    DONE,
    EDGE_KINDS,
    chainLinks,
    edgeKindsFor,
    edgeOf,
    pruneEdgeTarget,
    setEdge,
    freshStage,
    type DocEdge,
    type EdgeKind,
    type FlowDoc,
    type StageKind
  } from '$lib/flowdoc';
  import type { Agent, ModelInfo } from '$lib/api';

  let {
    doc,
    selection,
    agents,
    modelsByKind,
    harnessKinds,
    defaultHarnessKind,
    commands,
    onchange,
    onselect
  }: {
    doc: FlowDoc;
    selection: Selection;
    agents: Agent[];
    // Models per harness kind (per-step harness): a stage's model list scopes
    // to the harness it pins, or the project default when unpinned.
    modelsByKind: Record<string, ModelInfo[]>;
    harnessKinds: string[];
    defaultHarnessKind: string;
    commands: string[];
    onchange: () => void;
    onselect: (sel: Selection) => void;
  } = $props();

  // The kind whose models a stage should draw from: its pinned harness, else
  // the project default.
  const stageKind = (harness: string | undefined) => harness || defaultHarnessKind;
  // The datalist id a model input points at. Kinds with no fetched models
  // still get an (empty) list — free text always works.
  const modelListId = (harness: string | undefined) => `fl-models-${stageKind(harness) || 'default'}`;
  const allKinds = $derived([...new Set([...(harnessKinds ?? []), defaultHarnessKind].filter(Boolean))]);

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
    if (!newId || newId === oldId || newId === DONE || newId === ABORT) return;
    if (doc.stages.some((s) => s.id === newId)) return;
    for (const s of doc.stages) {
      if (s.id === oldId) s.id = newId;
      if (s.on_success === oldId) s.on_success = newId;
      for (const kind of EDGE_KINDS) {
        for (const link of chainLinks(edgeOf(s, kind))) {
          if (link.to === oldId) link.to = newId;
        }
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
        setEdge(s, kind, pruneEdgeTarget(edgeOf(s, kind), id));
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
    if (kind === 'on_fail' || kind === 'on_timeout') return ['', 'output'];
    return type === 'agent' ? ['', 'comments'] : type === 'human_gate' ? ['', 'feedback'] : [''];
  }

  const targetsGate = (id: string) => doc.stages.find((x) => x.id === id)?.type === 'human_gate';
  // Unlimited (-1) needs a human on the link: gate target (each firing
  // parks on a decision) or gate source (the link only fires on reject).
  const unlimitedOK = (sourceType: StageKind, to: string) => sourceType === 'human_gate' || targetsGate(to);

  // retarget switches one chain link's target, normalizing the link's other
  // fields: abort links carry no max/feed/then; stage links need a max, and
  // -1 survives only while a gate remains on the link.
  function retarget(sourceType: StageKind, link: DocEdge, to: string) {
    link.to = to;
    if (to === ABORT) {
      link.max = undefined;
      link.feed = undefined;
      link.then = undefined;
    } else if (link.max == null || (link.max === -1 && !unlimitedOK(sourceType, to))) {
      link.max = 3;
    }
    onchange();
  }

  // appendLink adds a fallback ("then") after the chain's last link.
  function appendLink(e: DocEdge) {
    let last = e;
    while (last.then) last = last.then;
    if (last.to === ABORT || last.max === -1) return; // nothing runs after abort/unlimited
    last.then = { to: doc.stages[0]?.id ?? '', max: 3 };
    onchange();
  }

  // dropLink removes the chain's last link (the whole edge if it is alone).
  function dropLink(s: NonNullable<typeof stage>, kind: EdgeKind, e: DocEdge) {
    const links = chainLinks(e);
    if (links.length === 1) {
      setEdge(s, kind, undefined);
      onselect({ kind: 'stage', id: s.id });
    } else {
      links[links.length - 2].then = undefined;
    }
    onchange();
  }
</script>

{#each allKinds as k (k)}
  <datalist id="fl-models-{k}">
    {#each modelsByKind[k] ?? [] as m (m.id)}<option value={m.id}>{m.name}</option>{/each}
  </datalist>
{/each}
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
      <label for="st-harness">Harness <span class="muted">— empty = project default ({defaultHarnessKind || 'project harness'})</span></label>
      {#if harnessKinds.length > 0}
        <select id="st-harness" bind:value={s.harness} onchange={onchange}>
          <option value="">default{defaultHarnessKind ? ` — ${defaultHarnessKind}` : ''}</option>
          {#each harnessKinds as k (k)}<option value={k}>{k}</option>{/each}
          {#if s.harness && !harnessKinds.includes(s.harness)}
            <option value={s.harness}>{s.harness} — not in context project</option>
          {/if}
        </select>
      {:else}
        <input id="st-harness" bind:value={s.harness} oninput={onchange} placeholder="project harness" />
      {/if}
      <label for="st-model">Model <span class="muted">— empty = flow default; suggestions from {stageKind(s.harness) || 'the harness'}</span></label>
      <input id="st-model" list={modelListId(s.harness)} bind:value={s.model} oninput={onchange} placeholder={doc.defaults.model} />
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
        <option value="manual_debug">manual_debug — human inspects the workspace</option>
      </select>
      <label for="st-payload">Payload <span class="muted">— artifact path, mr_url or workspace (terminal/IDE link)</span></label>
      <input id="st-payload" bind:value={s.payload} oninput={onchange}
        placeholder={s.ping === 'manual_debug' ? 'workspace' : 'artifacts/plan.md'} />
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

    <div class="row" style="margin-top:10px; gap:8px; flex-wrap:wrap">
      {#each edgeKindsFor(s.type) as kind (kind)}
        {#if !edgeOf(s, kind)}
          <button onclick={() => addEdge(s, kind)}>+ {kind}</button>
        {:else}
          <button onclick={() => onselect({ kind: 'edge', stage: s.id, edge: kind })}>{kind} →</button>
        {/if}
      {/each}
    </div>
    {#if s.type === 'agent' || s.type === 'command'}
      <p class="muted small" style="margin:6px 0 0">
        Timeouts route via on_timeout when declared, else fall back to on_fail.
      </p>
    {/if}
  {:else if edgeSel?.stage}
    {@const s = edgeSel.stage}
    {@const kind = edgeSel.kind}
    {@const e = edgeOf(s, kind)}
    {#if e}
      {@const links = chainLinks(e)}
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
      {#each links as link, depth (depth)}
        <div class="link" class:fallback={depth > 0}>
          {#if depth > 0}<div class="small muted" style="margin-bottom:2px">then (once the link above is exhausted)</div>{/if}
          <label for="ed-to-{depth}">To</label>
          <select id="ed-to-{depth}" value={link.to} onchange={(ev) => retarget(s.type, link, ev.currentTarget.value)}>
            {#each stageIds as id (id)}<option value={id}>{id}</option>{/each}
            <option value={ABORT}>abort — stop the run deliberately</option>
          </select>
          {#if link.to !== ABORT}
            {@const canUnlimit = unlimitedOK(s.type, link.to)}
            {#if canUnlimit}
              <label class="row small" style="gap:6px; margin-top:6px">
                <input
                  type="checkbox"
                  style="width:auto"
                  checked={link.max === -1}
                  onchange={(ev) => {
                    link.max = ev.currentTarget.checked ? -1 : 3;
                    if (link.max === -1) link.then = undefined;
                    onchange();
                  }}
                />
                ∞ no limit — every firing costs a human decision{targetsGate(link.to) ? '; rejecting the gate ends the run' : ''}
              </label>
            {/if}
            <label for="ed-max-{depth}">Max <span class="muted">— times this link may fire{depth === 0 && !link.then ? ' before the run is abandoned' : ''}{canUnlimit ? ' · -1 = ∞ (unlimited)' : ''}</span></label>
            <input
              id="ed-max-{depth}"
              type="number"
              min={canUnlimit ? -1 : 1}
              value={link.max ?? 1}
              oninput={(ev) => {
                const v = Math.floor(Number(ev.currentTarget.value));
                link.max = canUnlimit && v === -1 ? -1 : Math.max(1, v || 1);
                if (link.max === -1) link.then = undefined;
                onchange();
              }}
            />
            <label for="ed-feed-{depth}">Feed</label>
            <select
              id="ed-feed-{depth}"
              value={link.feed ?? ''}
              onchange={(ev) => {
                link.feed = ev.currentTarget.value || undefined;
                onchange();
              }}
            >
              {#each feedOptions(kind, s.type) as f (f)}<option value={f}>{f === '' ? '— nothing' : f}</option>{/each}
            </select>
          {/if}
        </div>
      {/each}
      <div class="row" style="margin-top:10px; gap:8px">
        {#if links[links.length - 1].to !== ABORT && links[links.length - 1].max !== -1}
          <button onclick={() => appendLink(e)}>+ then (fallback)</button>
        {/if}
        <button onclick={() => dropLink(s, kind, e)}>− remove last link</button>
      </div>
      <p class="muted small" style="margin:8px 0 0">
        Links fire top-down: each keeps its own counter; when one is exhausted the next failure
        falls through. An exhausted chain abandons the run.
      </p>
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
    <input id="fl-model" list={modelListId(undefined)} bind:value={doc.defaults.model} oninput={onchange} placeholder="openrouter/anthropic/claude-sonnet-4.6" />
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
      red = on_fail, cyan = on_timeout, orange = on_reject. Edges may target the abort terminal to
      stop the run deliberately.
    </p>
  {/if}
</div>

<style>
  .inspector {
    min-width: 280px;
  }
  .link.fallback {
    border-left: 2px solid var(--border, #444);
    padding-left: 10px;
    margin-top: 10px;
  }
</style>
