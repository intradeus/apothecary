import { render, screen, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { aCrew, aSample, aUnit } from '$test/fixtures/swarm';
import { hudStats, type HudStats } from './stats';
import Hud from './Hud.svelte';

const GB = 2 ** 30;
const capacity = { runs: { used: 2, max: 4 }, envs: { used: 3, max: 0 } };

const mount = (stats: HudStats, cap = capacity) => render(Hud, { stats, capacity: cap });
const value = (caption: string) => screen.getByText(caption).nextElementSibling?.textContent;

const board = () =>
	hudStats([
		aUnit({
			runId: 'a',
			resources: aSample({ cpuPerc: 150, memUsed: GB, memLimit: 4 * GB }),
			crew: aCrew({
				subagents: [{ toolCallId: 'c', title: 'scouts', count: 3, since: '2026-10-31T12:00:00Z' }]
			})
		}),
		aUnit({ runId: 'b', state: 'waiting_on_human' }),
		aUnit({ runId: 'c', state: 'completed' })
	]);

describe('Hud', () => {
	it('counts the board', () => {
		mount(board());
		expect(value('Sandboxes online')).toBe('2/3');
		expect(value('Cores')).toBe('1.5');
		expect(value('RAM')).toBe('1.00 GB / 4.00 GB');
		expect(value('Agents')).toBe('1');
		expect(value('Drones')).toBe('3');
		expect(value('Alerts')).toBe('1');
	});

	it('lights the alerts when there are any', () => {
		mount(board());
		expect(screen.getByText('Alerts').parentElement).toHaveClass('tone-danger');
	});

	it('keeps the alerts quiet at zero', () => {
		mount(hudStats([aUnit()]));
		expect(screen.getByText('Alerts').parentElement).toHaveClass('tone-muted');
	});

	it('says the load is unknown without a sample, not zero', () => {
		mount(hudStats([aUnit()]));
		expect(value('Cores')).toBe('–');
		expect(value('RAM')).toBe('–');
	});

	it('shows the memory used alone when no limit is known', () => {
		mount(hudStats([aUnit({ resources: aSample({ memUsed: GB, memLimit: 0 }) })]));
		expect(value('RAM')).toBe('1.00 GB');
	});

	it('meters the run slots as cells against the cap', () => {
		mount(board());
		const meter = screen.getByRole('meter', { name: 'Run slots' });
		expect(meter).toHaveAttribute('aria-valuenow', '2');
		expect(meter).toHaveAttribute('aria-valuemax', '4');
		expect(meter.querySelectorAll('.cell')).toHaveLength(4);
		expect(meter.querySelectorAll('.cell.on')).toHaveLength(2);
		expect(screen.getByText('2/4')).toBeInTheDocument();
	});

	it('shows the count alone when there is no cap', () => {
		mount(board());
		expect(screen.queryByRole('meter', { name: 'Workspaces' })).not.toBeInTheDocument();
		expect(
			within(screen.getByText('Workspaces').parentElement!).getByText('3')
		).toBeInTheDocument();
	});

	it('scales a big cap onto its cells', () => {
		mount(board(), { runs: { used: 32, max: 64 }, envs: { used: 0, max: 0 } });
		const meter = screen.getByRole('meter', { name: 'Run slots' });
		expect(meter.querySelectorAll('.cell')).toHaveLength(16);
		expect(meter.querySelectorAll('.cell.on')).toHaveLength(8);
	});

	it('warns when the slots are full', () => {
		mount(board(), { runs: { used: 4, max: 4 }, envs: { used: 0, max: 0 } });
		expect(screen.getByRole('meter', { name: 'Run slots' })).toHaveClass('full');
	});
});
