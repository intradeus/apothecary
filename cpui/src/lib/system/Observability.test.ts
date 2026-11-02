import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import Observability from './Observability.svelte';

vi.mock('$lib/api', () => ({
	api: { system: { observability: vi.fn() } }
}));

beforeEach(() => {
	vi.resetAllMocks();
});

describe('Observability', () => {
	it('links out to the operator’s Grafana in a new tab', async () => {
		vi.mocked(api.system.observability).mockResolvedValue('https://grafana.example/d/x');
		render(Observability);

		const link = await screen.findByRole('link', { name: /Open Grafana/ });
		expect(link).toHaveAttribute('href', 'https://grafana.example/d/x');
		expect(link).toHaveAttribute('target', '_blank');
		expect(link).toHaveAttribute('rel', 'noopener');
		expect(screen.getByText('https://grafana.example/d/x')).toBeInTheDocument();
	});

	it('names the variable to set when there is no link', async () => {
		vi.mocked(api.system.observability).mockResolvedValue('');
		render(Observability);

		expect(await screen.findByText('Grafana is not configured')).toBeInTheDocument();
		expect(screen.getByText(/APOTHECARY_GRAFANA_URL/)).toBeInTheDocument();
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});

	it('shows nothing but a placeholder until it knows', () => {
		vi.mocked(api.system.observability).mockReturnValue(Promise.withResolvers<string>().promise);
		render(Observability);
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
		expect(screen.queryByText('Grafana is not configured')).not.toBeInTheDocument();
	});

	it('offers a retry when loading fails', async () => {
		vi.mocked(api.system.observability)
			.mockRejectedValueOnce(new Error('down'))
			.mockResolvedValueOnce('https://g');
		render(Observability);

		expect(await screen.findByText('Couldn’t load the observability settings')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByRole('link', { name: /Open Grafana/ })).toHaveAttribute(
			'href',
			'https://g'
		);
	});
});
