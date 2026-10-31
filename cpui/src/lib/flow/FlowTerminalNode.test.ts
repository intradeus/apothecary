import { render, screen } from '@testing-library/svelte';
import type { NodeProps } from '@xyflow/svelte';
import { describe, expect, it, vi } from 'vitest';
import FlowTerminalNode from './FlowTerminalNode.svelte';

vi.mock('@xyflow/svelte', () => import('$test/fixtures/xyflow.svelte'));

const setup = (type: string, data: Record<string, unknown> = {}) =>
	render(FlowTerminalNode, { type, data } as unknown as NodeProps).container.firstElementChild!;
const handles = () =>
	screen.queryAllByTestId('handle').map((h) => h.getAttribute('data-type') + ':' + h.dataset.id);

describe('FlowTerminalNode', () => {
	it.each([
		['the main chain’s beginning', 'start', { entry: true }, 'tone-brand', 'start'],
		['a watcher’s beginning', 'start', {}, 'tone-accent', 'start'],
		['an outcome that completes', 'start', { outcome: true }, 'tone-success', 'start'],
		['an outcome that aborts', 'start', { outcome: true, then: 'abort' }, 'tone-danger', 'start'],
		['a chain’s end', 'end', {}, 'tone-accent-2', 'end'],
		['the run’s done', 'done', {}, 'tone-success', 'done'],
		['an abort', 'abort', {}, 'tone-danger', 'abort'],
		['a debug stop', 'debug', {}, 'tone-attention', 'debug'],
		['a sequence’s way out', 'exit', {}, 'tone-success', 'exit'],
		['a sequence’s failure', 'failexit', {}, 'tone-danger', 'failexit']
	])('wears the hue of %s', (_, type, data, hue, label) => {
		expect(setup(type, data)).toHaveClass(hue);
		expect(screen.getByText(label)).toBeInTheDocument();
	});

	it('names what it was told to', () => {
		setup('start', { entry: true, label: 'main · operates on a ticket' });
		expect(screen.getByText('main · operates on a ticket')).toBeInTheDocument();
	});

	it.each([
		['start', { entry: true }, /Where the chain begins/],
		['end', {}, /This chain is over/],
		['done', {}, 'The run is done'],
		['abort', {}, 'The run stops deliberately'],
		['exit', {}, /The sequence is over/],
		['failexit', {}, /fail: the sequence leaves/]
	])('explains a %s on hover', (type, data, title) => {
		expect((setup(type, data) as HTMLElement).title).toMatch(title);
	});

	it('takes the title it is given over its own', () => {
		expect((setup('end', { title: 'Back to the hub' }) as HTMLElement).title).toBe(
			'Back to the hub'
		);
	});

	describe('its handles', () => {
		it('starts a chain from the right', () => {
			setup('start', { entry: true });
			expect(handles()).toEqual(['source:']);
		});

		it('takes the hub’s edge on the left, which nothing else connects to', () => {
			setup('start', { hub: true });
			expect(handles()).toEqual(['target:hub', 'source:']);
			expect(screen.getAllByTestId('handle')[0]).toHaveAttribute('data-connectable', 'false');
		});

		it('ends a chain: in only', () => {
			setup('done');
			expect(handles()).toEqual(['target:']);
		});

		it('debug comes back: in and out', () => {
			setup('debug');
			expect(handles()).toEqual(['target:', 'source:']);
		});
	});
});
