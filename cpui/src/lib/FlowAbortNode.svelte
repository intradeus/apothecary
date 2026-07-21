<script lang="ts">
  import { Handle, Position, type NodeProps } from '@xyflow/svelte';

  let {}: NodeProps = $props();
</script>

<div class="abort-node">
  <Handle type="target" position={Position.Left} />
  abort ✕
</div>

<style>
  .abort-node {
    background: var(--bg2, #1f2430);
    border: 1px dashed #e06c75;
    border-radius: 16px;
    padding: 6px 14px;
    font-size: 12px;
    color: #e06c75;
  }
</style>
