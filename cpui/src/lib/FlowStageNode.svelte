<script lang="ts">
  import { Handle, Position, useUpdateNodeInternals, type NodeProps } from '@xyflow/svelte';
  import {
    isFeedSentinel,
    stageInputs,
    stageOutputs,
    type ActionContract,
    type AgentContract,
    type DocStage
  } from '$lib/flowdoc';

  let { id, data, selected }: NodeProps = $props();
  const stage = $derived(data.stage as DocStage);
  const entry = $derived(Boolean(data.entry));
  const agents = $derived((data.agents ?? []) as AgentContract[]);
  const actions = $derived((data.actions ?? []) as ActionContract[]);

  // Dataflow ports: one dot per artifact the stage reads (left) and writes
  // (right). Distinct from the colored control handles: these are NOT
  // rewireable — the wiring IS the artifact paths, so you change it by editing
  // an input or output, and the data edges follow.
  const inputs = $derived(stageInputs(stage, agents, actions));
  const outputs = $derived(stageOutputs(stage, agents, actions));

  // xyflow caches handle geometry per node id, and a node's id is stable while
  // its dots come and go (bindings change, the agent library arrives async).
  // Telling it to re-measure is what keeps the data edges attached.
  const updateNodeInternals = useUpdateNodeInternals();
  const dotSignature = $derived(
    [...inputs.map((p) => `i:${p.name}:${p.value}`), ...outputs.map((p) => `o:${p.name}:${p.value}`)].join('|')
  );
  $effect(() => {
    void dotSignature;
    updateNodeInternals(id);
  });

  const typeColors: Record<string, string> = {
    agent: '#7aa2f7',
    command: '#9ece6a',
    human_gate: '#e0af68',
    action: '#bb9af7',
    wait: '#7dcfff'
  };
  const detail = $derived(
    stage.type === 'agent'
      ? stage.agent || '(no agent)'
      : stage.type === 'command'
        ? stage.run_ref || (stage.run ? 'script' : '(no command)')
        : stage.type === 'action'
          ? stage.action || '(no action)'
          : stage.type === 'wait'
            ? `for ${stage.for || '(no signal)'}`
            : 'gate'
  );
</script>

<div class="stage-node" class:selected style={`border-left: 4px solid ${typeColors[stage.type] ?? '#888'}`}>
  <div class="head">
    <!-- The un-id'd target: every control edge lands here unless the target
         bound the value it carries, in which case it lands on that dot. -->
    <Handle type="target" position={Position.Left} style="top:14px" />
    <span class="mono id">{stage.id}</span>
    {#if entry}<span class="entry">entry</span>{/if}
    <Handle id="success" type="source" position={Position.Right} style="top:14px; background:#4caf7d" />
  </div>
  <div class="meta">{stage.type} · {detail}</div>

  {#each inputs as p (p.name)}
    <!-- A feed sentinel ($DECISION/$FAILURE) gets a dot too: the control edge
         that carries it lands there, so the retry path is visible. -->
    <div class="io in" class:unbound={p.unbound} class:fed={isFeedSentinel(p.value)}>
      {#if !p.unbound && (!p.sentinel || isFeedSentinel(p.value))}
        <Handle
          id={`in:${p.name}`}
          type="target"
          position={Position.Left}
          isConnectable={false}
          class="io-dot"
        />
      {/if}
      <span class="pname mono">{p.name}</span>
      {#if p.unbound}<span class="unset">unbound</span>
      {:else if p.sentinel}<span class="sent mono">{p.value}</span>
      {:else if p.value !== p.name}<span class="path mono">{p.value}</span>{/if}
    </div>
  {/each}

  {#each outputs as p (p.name)}
    <div class="io out">
      <span class="pname mono">{p.name}</span>
      {#if p.value && p.value !== p.name}<span class="path mono" class:sent={p.sentinel}>{p.value}</span>{/if}
      {#if p.value}
        <Handle
          id={`out:${p.name}`}
          type="source"
          position={Position.Right}
          isConnectable={false}
          class="io-dot"
        />
      {/if}
    </div>
  {/each}

  {#if stage.type === 'agent' || stage.type === 'command' || stage.type === 'wait'}
    <Handle id="on_fail" type="source" position={Position.Bottom} style="left:25%; background:#e06c75" />
    <Handle id="on_timeout" type="source" position={Position.Bottom} style="left:50%; background:#7dcfff" />
  {/if}
  {#if stage.type === 'agent' || stage.type === 'human_gate'}
    <Handle id="on_reject" type="source" position={Position.Bottom} style="left:75%; background:#e0af68" />
  {/if}
</div>

<style>
  .stage-node {
    background: var(--bg2, #1f2430);
    border: 1px solid var(--border, #333);
    border-radius: 6px;
    padding: 8px 12px;
    min-width: 170px;
    font-size: 13px;
    color: inherit;
  }
  .stage-node.selected {
    outline: 2px solid #7aa2f7;
  }
  .head {
    display: flex;
    gap: 6px;
    align-items: baseline;
    position: relative;
  }
  .id {
    font-weight: 600;
  }
  .entry {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    opacity: 0.7;
  }
  .meta {
    font-size: 11px;
    opacity: 0.7;
    margin-top: 2px;
  }
  /* One io row per port; the dot is absolutely positioned on the node's edge,
     and xyflow measures handle geometry from the DOM. */
  .io {
    position: relative;
    display: flex;
    gap: 5px;
    align-items: baseline;
    font-size: 10px;
    margin-top: 3px;
    max-width: 260px;
    white-space: nowrap;
  }
  .io.in {
    padding-left: 2px;
  }
  .io.out {
    justify-content: flex-end;
    padding-right: 2px;
  }
  .io.unbound {
    opacity: 0.5;
  }
  /* An edge-carried input only fills on a retry: present, but not the
     stage's main business. */
  .io.fed {
    opacity: 0.75;
  }
  .pname {
    font-weight: 600;
    opacity: 0.85;
  }
  .path,
  .unset,
  .sent {
    opacity: 0.6;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .sent {
    color: #7dcfff;
    opacity: 0.85;
  }
  .unset {
    font-style: italic;
  }
  /* Data dots read as plumbing, not as something you can grab: smaller,
     muted, and not connectable. */
  .stage-node :global(.io-dot) {
    width: 6px;
    height: 6px;
    min-width: 6px;
    min-height: 6px;
    background: #565f89;
    border: 1px solid var(--bg2, #1f2430);
    cursor: default;
  }
  .io.in :global(.io-dot) {
    left: -13px;
    top: 50%;
  }
  .io.out :global(.io-dot) {
    right: -13px;
    top: 50%;
  }
</style>
