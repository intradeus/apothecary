import { describe, expect, it } from 'vitest';
import { parse } from 'yaml';
import { CURRENT_VERSION, type DocStage, type FlowDoc } from '$lib/domain/flowdoc';
import { readDoc } from '$test/fixtures/logic';
import { flowSkeleton } from './skeleton';

const out = () => parse(flowSkeleton('mine'));

describe('flowSkeleton', () => {
	it('names the flow and declares the current grammar', () => {
		expect(out()).toMatchObject({ name: 'mine', version: CURRENT_VERSION });
	});

	it('leaves the model to the workshop: no model id in the tree', () => {
		expect(out().parameters).not.toHaveProperty('model');
	});

	it('runs an agent the document defines, on the flow’s harness and model', () => {
		const doc: FlowDoc = readDoc(flowSkeleton('mine'));
		const [work] = doc.stages;
		expect(work).toMatchObject({ type: 'agent', harness: '$harness', model: '$model' });
		expect(doc.agents?.map((a) => a.name)).toContain(work.agent);
	});

	it('finishes the run when the one stage succeeds', () => {
		expect(readDoc(flowSkeleton('mine')).stages[0].on_success).toBe('done');
	});

	it('retries the stage on failure', () => {
		expect(readDoc(flowSkeleton('mine')).stages[0].on_fail).toEqual({ to: 'work', max: 2 });
	});

	// The parser wants all of it: edge.failure is a `failure` and is only
	// filled when an edge carried it, so the port says the type and optional.
	it('writes its ports in the current grammar', () => {
		const [work] = out().main as DocStage[];
		expect(work.input?.failure).toEqual({
			from: 'edge.failure',
			type: 'failure',
			optional: true
		});
	});

	it('types the input its agent declares', () => {
		expect(out().agents[0].inputs[0]).toMatchObject({ name: 'failure', type: 'failure' });
	});
});
