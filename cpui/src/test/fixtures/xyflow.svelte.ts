// What stands in for @xyflow/svelte, which needs a real layout:
//   vi.mock('@xyflow/svelte', () => import('$test/fixtures/xyflow.svelte'));
// The canvas draws its nodes with the real node components, one button each,
// so a test clicks a stage the way a person does; `canvas` is the rest of what
// Svelte Flow would call back on.
import { vi } from 'vitest';
import BaseEdge from './xyflow/BaseEdge.svelte';
import ControlButton from './xyflow/ControlButton.svelte';
import Controls from './xyflow/Controls.svelte';
import EdgeLabel from './xyflow/EdgeLabel.svelte';
import Handle from './xyflow/Handle.svelte';
import Nothing from './xyflow/Nothing.svelte';
import SvelteFlow from './xyflow/SvelteFlow.svelte';
import ViewportPortal from './xyflow/ViewportPortal.svelte';

export { canvas } from './xyflow/canvas';
export { BaseEdge, ControlButton, Controls, EdgeLabel, Handle, SvelteFlow, ViewportPortal };
export const Background = Nothing;

export const Position = { Left: 'left', Right: 'right', Top: 'top', Bottom: 'bottom' };
export const BackgroundVariant = { Dots: 'dots' };

export const getSmoothStepPath = ({
	sourceX,
	sourceY,
	targetX,
	targetY
}: Record<string, number>) => [`M${sourceX} ${sourceY} L${targetX} ${targetY}`];

/** useNodesInitialized's answer: reactive, as the real one is. */
export const measured = $state({ current: false });
export const useNodesInitialized = () => measured;

/** useStore's flow height, for a test to set. */
export const store = $state({ height: 600 });
export const useStore = () => store;

export const updateNodeInternals = vi.fn();
export const useUpdateNodeInternals = () => updateNodeInternals;

export const flowApi = {
	fitView: vi.fn(async () => true),
	getViewport: vi.fn(() => ({ x: 0, y: 0, zoom: 1 })),
	getNodes: vi.fn(() => []),
	getNodesBounds: vi.fn(() => ({ x: 0, y: 0, width: 100, height: 100 })),
	setViewport: vi.fn(async (_viewport: unknown, _options?: unknown) => true)
};
export const useSvelteFlow = () => flowApi;
