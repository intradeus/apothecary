import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { aStage } from '$test/fixtures/flow.svelte';
import SeqRow from './SeqRow.svelte';

it('is a way into the stage when it is given one', async () => {
	const onclick = vi.fn();
	render(SeqRow, { stage: aStage('fmt'), label: 'fmt', onclick });
	await userEvent.click(screen.getByRole('button', { name: /fmt/ }));
	expect(onclick).toHaveBeenCalledOnce();
});

it('reads only, without one', () => {
	render(SeqRow, { stage: aStage('fmt', 'agent'), label: 'build.fmt' });
	expect(screen.queryByRole('button')).not.toBeInTheDocument();
	expect(screen.getByText('build.fmt')).toBeInTheDocument();
	expect(screen.getByText('agent')).toBeInTheDocument();
});

it('says what a sequence stage runs', () => {
	render(SeqRow, { stage: aStage('run', 'sequence', { sequence: 'lint' }), label: 'run' });
	expect(screen.getByText('→ lint')).toBeInTheDocument();
});
