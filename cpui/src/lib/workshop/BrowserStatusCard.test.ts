import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { BrowserStatus } from '$lib/domain';
import BrowserStatusCard from './BrowserStatusCard.svelte';

vi.mock('$lib/api', () => ({ api: { projects: { browser: vi.fn() } } }));

const status = (over: Partial<BrowserStatus> = {}): BrowserStatus => ({
	enabled: true,
	available: true,
	state: 'running',
	sessions: 2,
	...over
});
const setup = (over: Partial<BrowserStatus> = {}, props: Record<string, unknown> = {}) => {
	vi.mocked(api.projects.browser).mockResolvedValue(status(over));
	return render(BrowserStatusCard, { projectId: 'p1', enabled: true, savedAt: null, ...props });
};

beforeEach(() => {
	vi.resetAllMocks();
});

describe('BrowserStatusCard', () => {
	it('reads the workshop it was given', async () => {
		setup();
		await screen.findByText('running');
		expect(api.projects.browser).toHaveBeenCalledWith('p1');
	});

	it('shows a running container and its sessions', async () => {
		setup({ state: 'running', sessions: 1 });
		expect(await screen.findByText('running')).toHaveClass('tint-success');
		expect(screen.getByText('1 session')).toBeInTheDocument();
	});

	it.each([
		['stopped', 'stopped'],
		['absent', 'not created']
	] as const)('shows a %s container, no sessions', async (state, label) => {
		setup({ state });
		expect(await screen.findByText(label)).toHaveClass('tint-muted');
		expect(screen.queryByText(/^\d+ sessions?$/)).not.toBeInTheDocument();
	});

	it('says when this apothecary has no browsers', async () => {
		setup({ available: false });
		await screen.findByText('running');
		expect(screen.queryByText('Image')).not.toBeInTheDocument();
		expect(screen.queryByText(/takes effect once/)).not.toBeInTheDocument();
	});

	it('says a toggle takes effect on save', async () => {
		setup({ enabled: false }, { enabled: true });
		expect(await screen.findByText(/Turned on/)).toBeInTheDocument();
	});

	it('says nothing when toggle and engine agree', async () => {
		setup();
		await screen.findByText('running');
		expect(screen.queryByText(/takes effect once/)).not.toBeInTheDocument();
	});

	it('shows the image and container', async () => {
		setup({ image: 'chrome:1', current: 'chrome:1', container: 'apo-browser' });
		expect(await screen.findByText('chrome:1')).toBeInTheDocument();
		expect(screen.getByText('apo-browser')).toBeInTheDocument();
		expect(screen.queryByText('older image')).not.toBeInTheDocument();
	});

	it('flags a container on an older image', async () => {
		setup({ image: 'chrome:1', current: 'chrome:2' });
		expect(await screen.findByText('older image')).toBeInTheDocument();
		expect(screen.getByText('chrome:1')).toBeInTheDocument();
	});

	it('falls back to the current image with no container yet', async () => {
		setup({ current: 'chrome:2', state: 'absent' });
		expect(await screen.findByText('chrome:2')).toBeInTheDocument();
		expect(screen.queryByText('older image')).not.toBeInTheDocument();
	});

	it("shows the engine's own error", async () => {
		setup({ error: 'docker down' });
		expect(await screen.findByText('docker down')).toBeInTheDocument();
	});

	it('offers a retry when the read fails', async () => {
		vi.mocked(api.projects.browser).mockRejectedValueOnce(new Error('down'));
		vi.mocked(api.projects.browser).mockResolvedValue(status());
		render(BrowserStatusCard, { projectId: 'p1', enabled: true, savedAt: null });

		expect(await screen.findByText('Couldn’t read the browser’s state')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('running')).toBeInTheDocument();
	});

	it('reads again on demand', async () => {
		setup();
		await userEvent.click(await screen.findByRole('button', { name: 'Read its state again' }));
		expect(api.projects.browser).toHaveBeenCalledTimes(2);
	});

	it('reads again after each save', async () => {
		const { rerender } = setup();
		await screen.findByText('running');
		await rerender({ savedAt: 123 });
		await waitFor(() => expect(api.projects.browser).toHaveBeenCalledTimes(2));
	});
});
