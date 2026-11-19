import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { aRun, aRunStore } from '$test/fixtures';

import AssistantView from './AssistantView.svelte';

vi.mock(
	'$lib/assistant/AssistantPanel.svelte',
	() => import('$test/fixtures/AssistantPanelStub.svelte')
);

beforeEach(() => {
	vi.clearAllMocks();
	sessionStorage.clear();
});

async function offer(fill: unknown) {
	const box = screen.getByRole('textbox', { name: 'fill' });
	await userEvent.clear(box);
	box.focus();
	await userEvent.paste(JSON.stringify(fill));
	await userEvent.click(screen.getByRole('button', { name: 'offer the fill' }));
}

describe('AssistantView', () => {
	it('is about the run, reads it, and takes a flow change to the flow editor', async () => {
		const rs = aRunStore(
			aRun({ id: 'r1', projectId: 'p1', flowName: 'builder', state: 'failed' }),
			{ pendingPings: [] }
		);
		render(AssistantView, { rs });
		const panel = screen.getByRole('region', { name: 'Assistant' });
		expect(panel.dataset).toMatchObject({ kind: 'run', subject: 'r1', workshop: 'p1' });
		await userEvent.click(screen.getByRole('button', { name: 'read the page' }));
		const read = JSON.parse(screen.getByRole('status', { name: 'page state' }).textContent!);
		expect(read).toMatchObject({ run: 'r1', state: 'failed', flow: 'builder' });

		await offer({ flow: 'builder', yaml: 'name: builder\n' });
		expect(
			screen.getByText('The assistant proposes a change to the flow builder.')
		).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: 'Open in the flow editor' }));
		expect(sessionStorage.getItem('apothecary.flowFill.p1.builder')).toBe('name: builder\n');
		expect(goto).toHaveBeenCalledWith('/w/p1/flows/builder');
		expect(screen.queryByText(/proposes a change/)).not.toBeInTheDocument();
	});

	it('drops a proposed change, and ignores what is not one', async () => {
		render(AssistantView, {
			rs: aRunStore(aRun({ id: 'r1', projectId: 'p1' }), { pendingPings: [] })
		});
		await offer({ yaml: 'no flow' });
		expect(screen.queryByText(/proposes a change/)).not.toBeInTheDocument();
		await offer({ flow: 'ship', yaml: 'name: ship\n' });
		await userEvent.click(screen.getByRole('button', { name: /Dismiss/ }));
		expect(screen.queryByText(/proposes a change/)).not.toBeInTheDocument();
	});
});
