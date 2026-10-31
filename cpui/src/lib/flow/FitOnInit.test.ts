import { render } from '@testing-library/svelte';
import { flushSync, tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flowApi, measured, store } from '$test/fixtures/xyflow.svelte';
import FitOnInit from './FitOnInit.svelte';

vi.mock('@xyflow/svelte', () => import('$test/fixtures/xyflow.svelte'));

const settle = async () => {
	flushSync();
	for (let i = 0; i < 6; i++) await tick();
};
const initial = async (props = {}) => {
	measured.current = true;
	const view = render(FitOnInit, props);
	await settle();
	return view;
};
const zoomedOut = (zoom = 0.2) => {
	flowApi.getViewport.mockReturnValue({ x: 5, y: 7, zoom });
	flowApi.getNodesBounds.mockReturnValue({ x: 100, y: 50, width: 4000, height: 200 });
};

describe('FitOnInit', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		flowApi.getViewport.mockReturnValue({ x: 0, y: 0, zoom: 1 });
		store.height = 600;
	});
	afterEach(() => {
		measured.current = false;
	});

	it('waits for the nodes to be measured', async () => {
		measured.current = false;
		const onmeasured = vi.fn();
		render(FitOnInit, { onmeasured });
		await settle();
		expect(flowApi.fitView).not.toHaveBeenCalled();

		measured.current = true;
		await settle();
		expect(onmeasured).toHaveBeenCalledOnce();
		expect(flowApi.fitView).toHaveBeenCalledOnce();
	});

	it('reports the sizes before it fits, so the fit sees the new layout', async () => {
		const order: string[] = [];
		flowApi.fitView.mockImplementationOnce(async () => {
			order.push('fit');
			return true;
		});
		await initial({ onmeasured: () => order.push('measured') });
		expect(order).toEqual(['measured', 'fit']);
	});

	it('fits with the padding and zoom it is given', async () => {
		await initial({ padding: 0.3, maxZoom: 2 });
		expect(flowApi.fitView).toHaveBeenCalledExactlyOnceWith({
			padding: 0.3,
			maxZoom: 2,
			duration: undefined
		});
	});

	it('leaves a legible fit alone', async () => {
		await initial();
		expect(flowApi.setViewport).not.toHaveBeenCalled();
	});

	it('fits once only', async () => {
		const { rerender } = await initial({ padding: 0.1 });
		await rerender({ padding: 0.2 });
		await settle();
		expect(flowApi.fitView).toHaveBeenCalledOnce();
	});

	it('anchors a long flow on its entry at a legible zoom, keeping the vertical centre', async () => {
		zoomedOut();
		await initial();
		const midY = 50 + 200 / 2;
		expect(flowApi.setViewport).toHaveBeenCalledExactlyOnceWith(
			{ x: 40 - 100 * 0.55, y: 7 + midY * 0.2 - midY * 0.55, zoom: 0.55 },
			{ duration: undefined }
		);
	});

	it('goes to the top of a flow too tall to centre', async () => {
		zoomedOut();
		flowApi.getNodesBounds.mockReturnValue({ x: 100, y: 50, width: 4000, height: 2000 });
		await initial();
		expect(flowApi.setViewport.mock.calls[0][0]).toMatchObject({ y: 40 - 50 * 0.55 });
	});

	it('takes the legible zoom it is given', async () => {
		zoomedOut(0.1);
		await initial({ legible: 0.2 });
		expect(flowApi.setViewport.mock.calls[0][0]).toMatchObject({ zoom: 0.2 });
	});

	describe('fitting again', () => {
		beforeEach(() => {
			vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
				cb(0);
				return 0;
			});
		});
		afterEach(() => {
			vi.unstubAllGlobals();
		});

		it('fits again, animated, when the canvas changes size', async () => {
			const { rerender } = await initial({ refit: 0 });
			flowApi.fitView.mockClear();
			await rerender({ refit: 1 });
			await settle();
			expect(flowApi.fitView).toHaveBeenCalledExactlyOnceWith(
				expect.objectContaining({ duration: 450 })
			);
		});

		it('does not fit twice on the same refit', async () => {
			const { rerender } = await initial({ refit: 3 });
			flowApi.fitView.mockClear();
			await rerender({ refit: 3 });
			await settle();
			expect(flowApi.fitView).not.toHaveBeenCalled();
		});
	});
});
