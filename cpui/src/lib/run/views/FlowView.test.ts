import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { StageDef } from '$lib/run/derive';
import { aRun } from '$test/fixtures';
import { NOW, anEvent, aViewStore } from '$test/fixtures/run-views';
import FlowView from './FlowView.svelte';

const WIDE = { w: 1000, h: 100 };
const TALL = { w: 300, h: 600 };
const stages: StageDef[] = [
	{ id: 'plan', type: 'agent', agent: 'planner' },
	{ id: 'approve', type: 'human_gate' },
	{ id: 'build', type: 'agent', agent: 'dev' }
];
const stageBtn = (id: string) => screen.getByRole('button', { name: new RegExp(id) });

function show(
	size: { w: number; h: number },
	over: Parameters<typeof aViewStore>[1] = {},
	run: Parameters<typeof aViewStore>[0] = aRun()
) {
	const onstage = vi.fn();
	const rs = aViewStore(run, { stageDefs: stages, ...over });
	render(FlowView, { rs, ...size, onstage });
	return { onstage, rs };
}

describe('FlowView', () => {
	describe('states', () => {
		it('says it is reading the recipe until it lands', () => {
			show(WIDE, { stageDefs: null });
			expect(screen.getByRole('status')).toHaveTextContent('Reading the flow…');
		});

		it('says there are no stages when the recipe is empty', () => {
			show(WIDE, { stageDefs: [] });
			expect(screen.getByText('No stages yet.')).toBeInTheDocument();
			expect(screen.queryByRole('status')).not.toBeInTheDocument();
		});

		it('does not read forever when there is no run', () => {
			show(WIDE, { stageDefs: null }, null);
			expect(screen.getByText('No stages yet.')).toBeInTheDocument();
		});

		it('falls back to what the log shows when the recipe is unknown', () => {
			show(WIDE, {
				stageDefs: null,
				events: [anEvent(1, 'stage_started', { stage_id: 'build', iteration: 1 })]
			});
			expect(stageBtn('build')).toBeInTheDocument();
		});
	});

	describe('its shape', () => {
		it('lays out as a strip when wide and short', () => {
			show(WIDE);
			expect(screen.queryByText('The formula')).not.toBeInTheDocument();
			expect(screen.getAllByRole('button')).toHaveLength(3);
		});

		it('lays out as the recipe when tall and narrow', () => {
			show(TALL);
			expect(screen.getByText('The formula')).toBeInTheDocument();
			expect(screen.getAllByRole('button')).toHaveLength(3);
		});

		it('stays the recipe at exactly the 2.2 ratio', () => {
			show({ w: 22, h: 10 });
			expect(screen.getByText('The formula')).toBeInTheDocument();
		});

		it('switches shape as the window is resized', async () => {
			const rs = aViewStore(aRun(), { stageDefs: stages });
			const { rerender } = render(FlowView, { rs, ...WIDE, onstage: vi.fn() });
			await rerender(TALL);
			expect(screen.getByText('The formula')).toBeInTheDocument();
		});

		it('names the flow and its pinned version on the recipe', () => {
			show(TALL);
			expect(screen.getByText('builder @e7c90c')).toBeInTheDocument();
		});

		it('names the flow alone when no version was pinned', () => {
			show(TALL, {}, aRun({ flowHash: '' }));
			expect(screen.getByText('builder')).toBeInTheDocument();
		});
	});

	describe('a stage', () => {
		it('opens its log from the strip', async () => {
			const { onstage } = show(WIDE);
			await userEvent.click(stageBtn('build'));
			expect(onstage).toHaveBeenCalledExactlyOnceWith('build');
		});

		it('opens its log from the recipe', async () => {
			const { onstage } = show(TALL);
			await userEvent.click(stageBtn('plan'));
			expect(onstage).toHaveBeenCalledExactlyOnceWith('plan');
		});

		it('reads as pending until it starts', () => {
			show(WIDE);
			expect(stageBtn('build')).toHaveTextContent('agent');
			expect(stageBtn('build')).toHaveTextContent('pending');
		});

		it('reads a gate as a gate', () => {
			show(WIDE);
			expect(stageBtn('approve')).toHaveTextContent('gate');
			expect(stageBtn('approve').title).toBe('approve · gate — open its log');
		});

		it('shows what finished, with what it cost and how long it took', () => {
			show(WIDE, {
				events: [
					anEvent(1, 'stage_started', { stage_id: 'plan', iteration: 1 }),
					anEvent(2, 'stage_completed', {
						stage_id: 'plan',
						usage: { cost_usd: 0.25 },
						duration_ms: 41_000
					})
				]
			});
			expect(stageBtn('plan')).toHaveTextContent('✓ agent');
			expect(stageBtn('plan')).toHaveTextContent('done · $0.250 · 41s');
			expect(stageBtn('plan')).not.toHaveTextContent('attempt');
		});

		it('shows what failed and why', () => {
			show(WIDE, {
				events: [
					anEvent(1, 'stage_started', { stage_id: 'plan', iteration: 1 }),
					anEvent(2, 'stage_failed', { stage_id: 'plan', reason: 'budget_exceeded' })
				]
			});
			expect(stageBtn('plan')).toHaveTextContent('✕ agent');
			expect(stageBtn('plan')).toHaveTextContent('budget exceeded');
		});

		it('shows the attempt and clock of what is running', () => {
			show(WIDE, {
				now: NOW + 66_000,
				events: [anEvent(1, 'stage_started', { stage_id: 'plan', iteration: 2 })]
			});
			expect(stageBtn('plan')).toHaveTextContent('● agent · attempt 2');
			expect(stageBtn('plan')).toHaveTextContent('attempt 2 · 1:05');
		});

		it('says when a stage waits on you', () => {
			show(WIDE, {
				events: [
					anEvent(1, 'stage_started', { stage_id: 'plan', iteration: 1 }),
					anEvent(2, 'stage_paused', { stage_id: 'plan', reason: 'ask_human' })
				]
			});
			expect(stageBtn('plan')).toHaveTextContent('waiting on you');
		});

		it('tells the tooltip the agent and the attempt', () => {
			show(WIDE, {
				events: [anEvent(1, 'stage_started', { stage_id: 'plan', iteration: 3 })]
			});
			expect(stageBtn('plan').title).toBe('plan · agent · planner · attempt 3 — open its log');
		});

		it('says on the recipe that a looped-back stage runs again', () => {
			show(TALL, {
				events: [
					anEvent(1, 'stage_started', { stage_id: 'plan', iteration: 1 }),
					anEvent(2, 'stage_completed', { stage_id: 'plan', duration_ms: 1000 }),
					anEvent(3, 'stage_started', { stage_id: 'build', iteration: 1 }),
					anEvent(4, 'stage_failed', { stage_id: 'build', reason: 'exit' }),
					anEvent(5, 'edge_taken', { from: 'build', to: 'plan', edge: 'on_fail' })
				]
			});
			expect(stageBtn('plan')).toHaveTextContent('↩ runs again');
		});

		it('shows the stage type on a pending recipe row', () => {
			show(TALL);
			expect(stageBtn('approve')).toHaveTextContent('gate');
			expect(stageBtn('build')).toHaveTextContent('agent');
		});
	});

	describe('phases', () => {
		const chains: StageDef[] = [
			{ id: 'plan', type: 'agent', phase: 'main' },
			{ id: 'build', type: 'agent', phase: 'main' },
			{ id: 'fix', type: 'agent', phase: 'fix-ci' }
		];
		const started = anEvent(1, 'phase_started', { phase: 'fix-ci' });

		it('shows no headings while only one chain has run', () => {
			show(WIDE, { stageDefs: chains });
			expect(screen.queryByTitle('phase')).not.toBeInTheDocument();
			expect(screen.queryByRole('button', { name: /fix/ })).not.toBeInTheDocument();
		});

		it('heads each chain once in the strip when several have run', () => {
			show(WIDE, { stageDefs: chains, events: [started] });
			expect(screen.getAllByTitle('phase').map((e) => e.textContent?.trim())).toEqual([
				'main',
				'fix-ci'
			]);
		});

		it('heads each chain once in the recipe when several have run', () => {
			show(TALL, { stageDefs: chains, events: [started] });
			expect(screen.getAllByTitle('phase').map((e) => e.textContent?.trim())).toEqual([
				'main',
				'fix-ci'
			]);
		});
	});

	describe('the finish', () => {
		it.each([
			['strip', WIDE],
			['recipe', TALL]
		])('lights up when the run completed, on the %s', (_, size) => {
			show(size, {}, aRun({ state: 'completed' }));
			expect(screen.getByText('done')).toHaveClass('ok');
		});

		it.each([
			['strip', WIDE],
			['recipe', TALL]
		])('stays unlit on a run that ended otherwise, on the %s', (_, size) => {
			show(size, {}, aRun({ state: 'failed' }));
			expect(screen.getByText('done')).not.toHaveClass('ok');
		});
	});
});
