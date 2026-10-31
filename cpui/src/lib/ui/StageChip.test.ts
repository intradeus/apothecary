import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import StageChip from './StageChip.svelte';

describe('StageChip', () => {
	it('names the stage type in its hue', () => {
		render(StageChip, { type: 'agent' });
		expect(screen.getByText('agent')).toHaveClass('tint-running');
	});

	it('reads human_gate as "gate"', () => {
		render(StageChip, { type: 'human_gate' });
		expect(screen.getByText('gate')).toHaveClass('tint-warning');
		expect(screen.queryByText('human_gate')).toBeNull();
	});

	it('shows an unknown type as it is, in the muted hue', () => {
		render(StageChip, { type: 'teleport' });
		expect(screen.getByText('teleport')).toHaveClass('tint-muted');
	});
});
