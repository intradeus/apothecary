<script lang="ts">
  import { SvelteFlow, Background, Controls, type Node, type Edge, type Connection } from '@xyflow/svelte';
  import '@xyflow/svelte/dist/style.css';
  import FlowStageNode from '$lib/FlowStageNode.svelte';
  import FlowDoneNode from '$lib/FlowDoneNode.svelte';
  import { DONE, EDGE_KINDS, effectiveSuccess, edgeOf, type FlowDoc } from '$lib/flowdoc';

  export type Selection = { kind: 'stage'; id: string } | { kind: 'edge'; stage: string; edge: string } | null;

  let {
    doc,
    layout,
    rev,
    onselect,
    onmove,
    onlink
  }: {
    doc: FlowDoc;
    layout: Record<string, { x: number; y: number }>;
    rev: number;
    onselect: (sel: Selection) => void;
    onmove: (id: string, x: number, y: number) => void;
    onlink: (source: string, handle: string, target: string) => void;
  } = $props();

  const nodeTypes = { stage: FlowStageNode, done: FlowDoneNode };

  let nodes = $state.raw<Node[]>([]);
  let edges = $state.raw<Edge[]>([]);

  function rebuild() {
    const ns: Node[] = doc.stages.map((s, i) => ({
      id: s.id,
      type: 'stage',
      position: layout[s.id] ?? { x: i * 240, y: 0 },
      data: { stage: s, entry: i === 0 }
    }));
    ns.push({
      id: '__done',
      type: 'done',
      position: layout[DONE] ?? { x: doc.stages.length * 240, y: 0 },
      data: {}
    });
    const es: Edge[] = [];
    doc.stages.forEach((s, i) => {
      const target = effectiveSuccess(doc, i);
      es.push({
        id: `success|${s.id}`,
        source: s.id,
        sourceHandle: 'success',
        target: target === DONE ? '__done' : target,
        style: 'stroke:#4caf7d; stroke-width:2'
      });
      for (const kind of EDGE_KINDS) {
        const e = edgeOf(s, kind);
        if (!e) continue;
        es.push({
          id: `${kind}|${s.id}`,
          source: s.id,
          sourceHandle: kind,
          target: e.to,
          label: `${kind} ×${e.max ?? '?'}${e.feed ? ` (${e.feed})` : ''}`,
          animated: true,
          style: kind === 'on_fail' ? 'stroke:#e06c75; stroke-dasharray:6 3' : 'stroke:#e0af68; stroke-dasharray:6 3'
        });
      }
    });
    nodes = ns;
    edges = es;
  }
  $effect(() => {
    void rev;
    rebuild();
  });

  function handleConnect(conn: Connection) {
    if (!conn.source || !conn.target) return;
    const target = conn.target === '__done' ? DONE : conn.target;
    onlink(conn.source, conn.sourceHandle ?? 'success', target);
  }
</script>

<div class="graph-wrap">
  <SvelteFlow
    bind:nodes
    bind:edges
    {nodeTypes}
    fitView
    deleteKey={null}
    onconnect={handleConnect}
    onnodedragstop={({ targetNode }) => {
      if (targetNode) onmove(targetNode.id === '__done' ? DONE : targetNode.id, targetNode.position.x, targetNode.position.y);
    }}
    onnodeclick={({ node }) => onselect(node.id === '__done' ? null : { kind: 'stage', id: node.id })}
    onedgeclick={({ edge }) => {
      const [kind, stage] = edge.id.split('|');
      onselect(kind === 'success' ? { kind: 'stage', id: stage } : { kind: 'edge', stage, edge: kind });
    }}
    onpaneclick={() => onselect(null)}
  >
    <Background />
    <Controls />
  </SvelteFlow>
</div>

<style>
  .graph-wrap {
    height: 480px;
    border: 1px solid var(--border, #333);
    border-radius: 6px;
    overflow: hidden;
  }
</style>
