import { render, screen } from '@testing-library/svelte';
import type { NodeProps } from '@xyflow/svelte';
import { tick } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { aSequence, aStage, anAgent, anAction, live } from '$test/fixtures/flow.svelte';
import { updateNodeInternals } from '$test/fixtures/xyflow.svelte';
import type { DocStage, StageKind } from '$lib/domain/flowdoc';
import FlowStageNode from './FlowStageNode.svelte';

vi.mock('@xyflow/svelte', () => import('$test/fixtures/xyflow.svelte'));

const setup = (stage: DocStage, data: Record<string, unknown> = {}, selected = false) =>
	render(FlowStageNode, {
		id: stage.id,
		data: { stage, ...data },
		selected
	} as unknown as NodeProps);
const stageOf = (type: StageKind, over: Partial<DocStage> = {}) => aStage('s', type, over);
const handleIds = () => screen.getAllByTestId('handle').map((h) => h.dataset.id);

describe('FlowStageNode', () => {
	it.each<[string, DocStage, string, boolean]>([
		['an agent', stageOf('agent', { agent: 'planner' }), 'planner', false],
		['no agent', stageOf('agent'), '(no agent)', true],
		['a project command', stageOf('command', { run_ref: 'verify' }), 'verify', false],
		['a literal script', stageOf('command', { run: 'make' }), 'script', false],
		['no command', stageOf('command'), '(no command)', true],
		['an action', stageOf('action', { action: 'commit' }), 'commit', false],
		['no action', stageOf('action'), '(no action)', true],
		['a signal', stageOf('wait', { for: 'ci' }), 'for ci', false],
		['no signal', stageOf('wait'), 'for (no signal)', true],
		['a service', stageOf('service', { service: '$app' }), 'start $app', false],
		['a service stopped', stageOf('service', { service: 'db', do: 'stop' }), 'stop db', false],
		['no service', stageOf('service'), 'start (no service)', true],
		['no sequence', stageOf('sequence'), 'no sequence chosen', true],
		['a gate’s question', stageOf('human_gate', { question: 'Ship?' }), 'Ship?', false],
		['a gate with none', stageOf('human_gate'), 'asks a human', false]
	])('says %s', (_, stage, text, unset) => {
		setup(stage);
		const line = screen.getByText(text);
		expect(line.classList.contains('unset')).toBe(unset);
	});

	describe('a sequence stage', () => {
		const sequences = [
			aSequence('lint', [aStage('fmt'), aStage('vet')], { description: 'Tidies.' })
		];

		it('shows what it runs and how to open it', () => {
			setup(stageOf('sequence', { sequence: 'lint' }), { sequences });
			const line = screen.getByText('lint: fmt → vet');
			expect(line).toHaveAttribute('title', 'Tidies. — click to open the sequence beside the flow');
		});

		it('flags one the flow does not have', () => {
			setup(stageOf('sequence', { sequence: 'gone' }), { sequences });
			expect(screen.getByText('runs sequence gone (not in this flow)')).toHaveClass('unset');
		});
	});

	it('marks the stage its chain begins at', () => {
		setup(stageOf('command'), { entry: true });
		expect(screen.getByText('first')).toHaveAttribute('title', 'Its chain begins here');
	});

	it.each([
		['selected', {}, true],
		['materialize', { fresh: true }, false],
		['leaving', { leaving: true }, false]
	])('takes the %s look', (cls, data, selected) => {
		const { container } = setup(stageOf('command'), data, selected);
		expect(container.firstElementChild).toHaveClass(cls);
	});

	it('wears its kind’s hue', () => {
		const { container } = setup(stageOf('human_gate'));
		expect(container.firstElementChild).toHaveClass('tone-warning');
	});

	describe('its control handles', () => {
		it.each<[StageKind, (string | undefined)[]]>([
			['agent', ['on_fail', 'on_timeout', 'on_reject']],
			['command', ['on_fail', 'on_timeout']],
			['wait', ['on_fail', 'on_timeout']],
			['service', ['on_fail', 'on_timeout']],
			['action', ['on_fail']],
			['sequence', ['on_fail']],
			['human_gate', ['on_reject']]
		])('a %s stage has its routes', (type, routes) => {
			setup(stageOf(type));
			expect(handleIds()).toEqual(['', 'success', ...routes]);
		});
	});

	describe('its data ports', () => {
		const agents = [
			anAgent('planner', {
				inputs: [{ name: 'ticket' }, { name: 'plan' }],
				outputs: [{ name: 'out' }]
			})
		];

		it('draws a dot per bound file, which nothing rewires', () => {
			setup(
				stageOf('command', {
					input: { plan: { from: 'artifacts/plan.md' } },
					output: { report: { to: 'artifacts/report.md' } }
				})
			);
			expect(screen.getByText('artifacts/plan.md')).toBeInTheDocument();
			expect(screen.getByText('artifacts/report.md')).toBeInTheDocument();
			const dots = screen.getAllByTestId('handle').filter((h) => /^(in|out):/.test(h.dataset.id!));
			expect(dots.map((h) => h.dataset.id)).toEqual(['in:plan', 'out:report']);
			expect(dots.every((h) => h.dataset.connectable === 'false')).toBe(true);
		});

		it('shows a contract’s port nothing binds as unbound, with no dot', () => {
			setup(stageOf('agent', { agent: 'planner' }), { agents });
			expect(screen.getAllByText('unbound')).toHaveLength(3);
			expect(handleIds().some((h) => /^(in|out):/.test(h ?? ''))).toBe(false);
		});

		it('names the source of a value the engine provides, with no dot', () => {
			setup(stageOf('command', { input: { t: { from: 'run.ticket' } } }));
			expect(screen.getByText('run.ticket')).toBeInTheDocument();
			expect(handleIds()).not.toContain('in:t');
		});

		it('gives an edge’s feed a dot, for the edge that carries it to land on', () => {
			setup(stageOf('command', { input: { why: { from: 'edge.failure' } } }));
			expect(handleIds()).toContain('in:why');
		});

		it('takes an action’s contract', () => {
			const commit = anAction('commit', {
				outputs: [{ name: 'sha', type: 'file', required: true }]
			});
			setup(stageOf('action', { action: 'commit' }), { actions: [commit] });
			expect(screen.getByText('sha')).toBeInTheDocument();
		});

		it('draws no port row for a stage with none', () => {
			const { container } = setup(stageOf('command'));
			expect(container.querySelector('.io')).toBeNull();
		});
	});

	// xyflow caches a node's handles: a dot that comes or goes must be measured.
	describe('measuring its dots', () => {
		beforeEach(() => updateNodeInternals.mockClear());

		it('tells xyflow when the dots change', async () => {
			const stage = live(stageOf('command'));
			render(FlowStageNode, {
				id: 's',
				data: { stage },
				selected: false
			} as unknown as NodeProps);
			expect(updateNodeInternals).toHaveBeenCalledExactlyOnceWith('s');

			stage.output = { r: { to: 'x' } };
			await tick();
			expect(updateNodeInternals).toHaveBeenCalledTimes(2);
		});

		it('leaves it be when the dots stay', async () => {
			const stage = live(stageOf('command', { output: { r: { to: 'x' } } }));
			render(FlowStageNode, {
				id: 's',
				data: { stage },
				selected: false
			} as unknown as NodeProps);
			stage.description = 'Not a dot';
			await tick();
			expect(updateNodeInternals).toHaveBeenCalledOnce();
		});
	});
});
