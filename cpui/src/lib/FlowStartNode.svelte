<script lang="ts">
  import { Handle, Position, type NodeProps } from '@xyflow/svelte';

  let {}: NodeProps = $props();
</script>

<div class="start-node" title="The entry — drag this handle onto a stage to start the flow there">
  start ▶
  <Handle type="source" position={Position.Right} style="background:#7aa2f7" />
</div>

<style>
  .start-node {
    background: var(--bg2, #1f2430);
    border: 1px dashed #7aa2f7;
    border-radius: 16px;
    padding: 6px 14px;
    font-size: 12px;
    color: #7aa2f7;
  }
</style>
