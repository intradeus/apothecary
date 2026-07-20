<script lang="ts">
  import { Handle, Position, type NodeProps } from '@xyflow/svelte';
  import type { DocStage } from '$lib/flowdoc';

  let { data, selected }: NodeProps = $props();
  const stage = $derived(data.stage as DocStage);
  const entry = $derived(Boolean(data.entry));

  const typeColors: Record<string, string> = {
    agent: '#7aa2f7',
    command: '#9ece6a',
    human_gate: '#e0af68',
    forge_mr: '#bb9af7'
  };
  const detail = $derived(
    stage.type === 'agent'
      ? stage.agent || '(no agent)'
      : stage.type === 'command'
        ? stage.run_ref || (stage.run ? 'script' : '(no command)')
        : stage.type === 'human_gate'
          ? stage.ping
          : stage.body
  );
</script>

<div class="stage-node" class:selected style={`border-left: 4px solid ${typeColors[stage.type] ?? '#888'}`}>
  <Handle type="target" position={Position.Left} />
  <div class="head">
    <span class="mono id">{stage.id}</span>
    {#if entry}<span class="entry">entry</span>{/if}
  </div>
  <div class="meta">{stage.type} · {detail}</div>
  <Handle id="success" type="source" position={Position.Right} style="background:#4caf7d" />
  {#if stage.type === 'agent' || stage.type === 'command'}
    <Handle id="on_fail" type="source" position={Position.Bottom} style="left:35%; background:#e06c75" />
  {/if}
  {#if stage.type === 'agent' || stage.type === 'human_gate'}
    <Handle id="on_reject" type="source" position={Position.Bottom} style="left:65%; background:#e0af68" />
  {/if}
</div>

<style>
  .stage-node {
    background: var(--bg2, #1f2430);
    border: 1px solid var(--border, #333);
    border-radius: 6px;
    padding: 8px 12px;
    min-width: 150px;
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
</style>
