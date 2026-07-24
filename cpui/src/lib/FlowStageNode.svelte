<script lang="ts">
  import { Handle, Position, type NodeProps } from '@xyflow/svelte';
  import { isMapInputs, isMapOutputs, type DocStage } from '$lib/flowdoc';

  let { data, selected }: NodeProps = $props();
  const stage = $derived(data.stage as DocStage);
  const entry = $derived(Boolean(data.entry));

  // Compact I/O summary for agent stages (paths are the rendezvous, so we show
  // them; there are no graph ports for bindings — a port implies a rewireable
  // edge, and input/output binding is by path, not by edge).
  const ioSummary = $derived.by(() => {
    if (stage.type !== 'agent') return '';
    const parts: string[] = [];
    const inp = stage.input;
    if (isMapInputs(inp) && Object.keys(inp).length) parts.push('in: ' + Object.keys(inp).join(', '));
    else if (Array.isArray(inp) && inp.length) parts.push('in: ' + inp.join(', '));
    const out = stage.output;
    if (isMapOutputs(out) && Object.keys(out).length) parts.push('out: ' + Object.values(out).join(', '));
    else if (typeof out === 'string' && out) parts.push('out: ' + out);
    return parts.join(' · ');
  });

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
  {#if ioSummary}<div class="io mono">{ioSummary}</div>{/if}
  <Handle id="success" type="source" position={Position.Right} style="background:#4caf7d" />
  {#if stage.type === 'agent' || stage.type === 'command'}
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
  .io {
    font-size: 10px;
    opacity: 0.6;
    margin-top: 2px;
    max-width: 260px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
