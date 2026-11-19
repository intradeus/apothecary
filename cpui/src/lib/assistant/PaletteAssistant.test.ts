import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { page } from '$app/state';
import PaletteAssistant from './PaletteAssistant.svelte';

vi.mock('./AssistantPanel.svelte', () => import('$test/fixtures/AssistantPanelStub.svelte'));
vi.mock('$app/state', async () => {
	const { bindable } = await import('$test/fixtures/app.svelte');
	return {
		page: bindable({ params: { workshop: 'p1' }, url: new URL('http://localhost/w/p1/runs') })
	};
});

describe('PaletteAssistant', () => {
	it('opens a page-less session that fills nothing, reading the workshop it is opened on', async () => {
		render(PaletteAssistant, { open: true });
		const panel = screen.getByRole('region', { name: 'Assistant' });
		expect(panel.dataset).toMatchObject({ kind: 'general', subject: 'palette', workshop: 'p1' });
		await userEvent.click(screen.getByRole('button', { name: 'read the page' }));
		expect(screen.getByRole('status', { name: 'page state' })).toHaveTextContent(
			'"page":"/w/p1/runs"'
		);
		await userEvent.click(screen.getByRole('button', { name: 'offer the fill' }));
		expect(screen.getByRole('status', { name: 'changed' })).toHaveTextContent('');
	});

	it('reads no workshop off a workshop’s pages, and shows nothing closed', () => {
		page.params.workshop = undefined as unknown as string;
		const { unmount } = render(PaletteAssistant, { open: true });
		expect(screen.getByRole('region', { name: 'Assistant' }).dataset.workshop).toBe('');
		unmount();
		render(PaletteAssistant, { open: false });
		expect(screen.queryByRole('region', { name: 'Assistant' })).not.toBeInTheDocument();
	});
});
