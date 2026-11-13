import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { aCrew, aSample, aUnit } from '$test/fixtures/swarm';
import Unit from './Unit.svelte';

const AT = { x: 10, y: 20 };

const mount = (unit = aUnit(), over: { selected?: boolean } = {}) => {
	const onselect = vi.fn();
	const r = render(Unit, { unit, at: AT, onselect, ...over });
	return { ...r, onselect, button: screen.getByRole('button') };
};

const crewed = (counts: number[]) =>
	aUnit({
		crew: aCrew({
			agent: 'builder',
			subagents: counts.map((count, i) => ({
				toolCallId: `c${i}`,
				title: `scout ${i}`,
				count,
				since: '2026-10-31T12:00:00Z'
			}))
		})
	});

describe('Unit', () => {
	it('names the run, its state, its agent and its drones', () => {
		const { button } = mount(crewed([2, 1]));
		const label = button.getAttribute('aria-label');
		expect(label).toContain('Add idempotency keys');
		expect(label).toContain('Running');
		expect(label).toContain('agent builder');
		expect(label).toContain('3 drones');
	});

	it('says a unit with no agent has no drones', () => {
		const { button } = mount();
		expect(button.getAttribute('aria-label')).toBe('Add idempotency keys, Running, 0 drones');
	});

	it('flags a unit that needs a human', () => {
		const { button, container } = mount(aUnit({ state: 'failed' }));
		expect(button.getAttribute('aria-label')).toContain('needs attention');
		expect(container.querySelector('.alert.tone-danger')).not.toBeNull();
	});

	it('marks an open question in the attention hue', () => {
		const { container } = mount(aUnit({ openPings: 1 }));
		expect(container.querySelector('.alert.tone-attention')).not.toBeNull();
	});

	it('draws one drone per subagent the spawns run', () => {
		const { container } = mount(crewed([2, 1]));
		expect(container.querySelectorAll('.drone')).toHaveLength(3);
	});

	it('draws no orbit without drones', () => {
		const { container } = mount();
		expect(container.querySelector('.orbit')).toBeNull();
	});

	it.each([
		['running', 'tone-running'],
		['waiting_on_human', 'tone-attention'],
		['failed', 'tone-danger'],
		['completed', 'tone-success']
	] as const)('rings a %s unit in %s', (state, tone) => {
		const { button } = mount(aUnit({ state }));
		expect(button).toHaveClass(tone);
	});

	it('pulses while the run is live, not once it is over', () => {
		const live = mount(aUnit({ state: 'provisioning' }));
		expect(live.button).toHaveClass('live');
		live.unmount();
		expect(mount(aUnit({ state: 'completed' })).button).not.toHaveClass('live');
	});

	it('shows a pilot badge and the stage only with an agent on station', () => {
		const { container, unmount } = mount(crewed([]));
		expect(container.querySelector('.pilot')?.textContent).toBe('OB');
		expect(screen.getByText('build')).toBeInTheDocument();
		unmount();
		const bare = mount();
		expect(bare.container.querySelector('.pilot')).toBeNull();
		expect(screen.queryByText('build')).not.toBeInTheDocument();
	});

	it('fills its bars from the sample', () => {
		const { container } = mount(aUnit({ resources: aSample({ memLimit: 4 * 2 ** 30 }) }));
		expect(container.querySelector('.bar.cpu')).not.toBeNull();
		expect(container.querySelector('.bar.mem')).not.toBeNull();
		expect(container.querySelectorAll('.track.empty')).toHaveLength(0);
	});

	it('leaves dashed empty tracks without a sample, not empty bars', () => {
		const { container } = mount();
		expect(container.querySelectorAll('.track.empty')).toHaveLength(2);
		expect(container.querySelector('.bar')).toBeNull();
	});

	it('shortens a long title under the unit', () => {
		mount(aUnit({ title: 'A very long run title indeed' }));
		expect(screen.getByText('A very long…')).toBeInTheDocument();
	});

	it('shows a selected unit as pressed', () => {
		const { button } = mount(aUnit(), { selected: true });
		expect(button).toHaveClass('selected');
		expect(button).toHaveAttribute('aria-pressed', 'true');
	});

	it('selects on a click', async () => {
		const { button, onselect } = mount();
		await userEvent.click(button);
		expect(onselect).toHaveBeenCalledExactlyOnceWith('01TESTRUN0000000000000ABCD');
	});

	it.each(['{Enter}', ' '])('selects on %s', async (key) => {
		const { button, onselect } = mount();
		button.focus();
		await userEvent.keyboard(key);
		expect(onselect).toHaveBeenCalledExactlyOnceWith('01TESTRUN0000000000000ABCD');
	});

	it('ignores other keys', async () => {
		const { button, onselect } = mount();
		button.focus();
		await userEvent.keyboard('x');
		expect(onselect).not.toHaveBeenCalled();
	});
});
