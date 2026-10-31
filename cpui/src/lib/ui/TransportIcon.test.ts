import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import TransportIcon from './TransportIcon.svelte';

describe('TransportIcon', () => {
	it('is decoration: the button around it carries the name', () => {
		const { container } = render(TransportIcon, { kind: 'play' });
		expect(container.querySelector('svg')).toHaveAttribute('aria-hidden', 'true');
	});

	it('draws play as a single triangle', () => {
		const { container } = render(TransportIcon, { kind: 'play' });
		expect(container.querySelectorAll('path')).toHaveLength(1);
		expect(container.querySelector('rect')).toBeNull();
	});

	it('draws pause as two bars', () => {
		const { container } = render(TransportIcon, { kind: 'pause' });
		expect(container.querySelectorAll('rect')).toHaveLength(2);
		expect(container.querySelector('path')).toBeNull();
	});

	it('draws stop as one square', () => {
		const { container } = render(TransportIcon, { kind: 'stop' });
		const rects = container.querySelectorAll('rect');
		expect(rects).toHaveLength(1);
		expect(rects[0].getAttribute('width')).toBe(rects[0].getAttribute('height'));
	});
});
