import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { bindable } from '$test/fixtures/app.svelte';
import BrowserSection from './BrowserSection.svelte';

vi.mock('$lib/api', () => ({ api: { projects: { browser: vi.fn() } } }));

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.projects.browser).mockResolvedValue({
		enabled: false,
		available: true,
		state: 'absent',
		sessions: 0
	});
});

describe('BrowserSection', () => {
	it('binds the toggle', async () => {
		const props = bindable({ enabled: false, savedAt: null });
		render(BrowserSection, props);
		await userEvent.click(screen.getByRole('switch', { name: 'Give this workshop a browser' }));
		expect(props.enabled).toBe(true);
	});

	it('has no status for a workshop that does not exist yet', () => {
		render(BrowserSection, { enabled: true, savedAt: null });
		expect(api.projects.browser).not.toHaveBeenCalled();
	});

	it('shows the status once it does', async () => {
		render(BrowserSection, { enabled: true, projectId: 'p1', savedAt: null });
		expect(await screen.findByText('not created')).toBeInTheDocument();
		expect(api.projects.browser).toHaveBeenCalledWith('p1');
	});
});
