import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { session } from '$lib/app/session.svelte';
import type { Me } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import LimitsSection from './LimitsSection.svelte';

vi.mock('$lib/api', () => ({ api: { system: { setLimits: vi.fn() } } }));

const signIn = (admin: boolean) =>
	(session.me = {
		user: { id: 'u1', handle: 'lu', name: 'Lu', role: admin ? 'admin' : 'member' }
	} as Me);
const runs = () => screen.getByRole('spinbutton', { name: 'Max concurrent runs' });
const save = () => screen.getByRole('button', { name: 'Save' });
const unset = { cpus: null, memoryGiB: null };

beforeEach(() => {
	vi.resetAllMocks();
	fleet.limits = { maxConcurrentRuns: 3, maxEnvironments: null };
	fleet.runs = [];
	toasts.list = [];
	signIn(true);
});

describe('LimitsSection', () => {
	it('shows everyone else the caps, read-only', () => {
		signIn(false);
		render(LimitsSection);
		expect(screen.getByText('3')).toBeInTheDocument();
		expect(screen.getByText('no cap')).toBeInTheDocument();
		expect(screen.getByText(/Only an admin can change these/)).toBeInTheDocument();
		expect(screen.queryByRole('spinbutton')).not.toBeInTheDocument();
	});

	it('lets an admin change a cap and save it', async () => {
		vi.mocked(api.system.setLimits).mockImplementation(async (l) => l);
		render(LimitsSection);
		expect(save()).toBeDisabled();
		await userEvent.clear(runs());
		await userEvent.type(runs(), '5');
		await userEvent.click(save());

		await waitFor(() =>
			expect(api.system.setLimits).toHaveBeenCalledWith({
				maxConcurrentRuns: 5,
				maxEnvironments: null,
				sandbox: { default: unset, ceiling: unset, idleMinutes: 0 }
			})
		);
		expect(fleet.limits).toMatchObject({ maxConcurrentRuns: 5, maxEnvironments: null });
		expect(toasts.list.at(-1)?.text).toBe('Limits saved');
	});

	it('lifts a cap with its toggle, and puts the old one back', async () => {
		render(LimitsSection);
		const [runsCap, envsCap] = screen.getAllByRole('switch', { name: 'No cap' });
		await userEvent.click(runsCap);
		expect(runs()).toBeDisabled();
		await userEvent.click(runsCap);
		expect(runs()).toHaveValue(3);
		await userEvent.click(envsCap);
		expect(screen.getByRole('spinbutton', { name: 'Max environments' })).toHaveValue(1);
	});

	it('will not save a cap that is not a whole positive number', async () => {
		render(LimitsSection);
		await userEvent.clear(runs());
		expect(save()).toBeDisabled();
	});

	it('shows a refused save, and discards back to the stored caps', async () => {
		vi.mocked(api.system.setLimits).mockRejectedValue(new Error('caps locked'));
		render(LimitsSection);
		await userEvent.clear(runs());
		await userEvent.type(runs(), '7');
		await userEvent.click(save());
		expect(await screen.findByText('caps locked')).toBeInTheDocument();

		await userEvent.click(screen.getByRole('button', { name: 'Discard' }));
		expect(runs()).toHaveValue(3);
		expect(screen.queryByText('caps locked')).not.toBeInTheDocument();
	});

	describe('sandbox sizes', () => {
		// The default's fields come first, the ceiling's second.
		const field = (which: 'default' | 'ceiling', name: 'CPUs' | 'Memory') =>
			screen.getAllByLabelText(name)[which === 'default' ? 0 : 1];

		it('shows everyone else the sizes, read-only', () => {
			signIn(false);
			fleet.limits = {
				maxConcurrentRuns: 3,
				maxEnvironments: 6,
				sandbox: { default: { cpus: 2, memoryGiB: 4 }, ceiling: unset, idleMinutes: 0 }
			};
			render(LimitsSection);
			expect(screen.getByText('2 CPUs · 4 GiB')).toBeInTheDocument();
			expect(screen.getByText('not set')).toBeInTheDocument();
		});

		it('reads a field left unset as any', () => {
			signIn(false);
			fleet.limits = {
				maxConcurrentRuns: 3,
				maxEnvironments: 6,
				sandbox: { default: unset, ceiling: { cpus: null, memoryGiB: 16 }, idleMinutes: 45 }
			};
			render(LimitsSection);
			expect(screen.getByText('any CPUs · 16 GiB')).toBeInTheDocument();
			expect(screen.getByText('after 45 min')).toBeInTheDocument();
		});

		it('lets an admin set a default and a ceiling, without touching the stored ones until saved', async () => {
			vi.mocked(api.system.setLimits).mockImplementation(async (l) => l);
			render(LimitsSection);
			await userEvent.type(field('default', 'Memory'), '4');
			await userEvent.type(field('ceiling', 'CPUs'), '8');
			expect(fleet.limits?.sandbox).toBeUndefined();
			await userEvent.click(save());
			await waitFor(() =>
				expect(api.system.setLimits).toHaveBeenCalledWith({
					maxConcurrentRuns: 3,
					maxEnvironments: null,
					sandbox: {
						default: { cpus: null, memoryGiB: 4 },
						ceiling: { cpus: 8, memoryGiB: null },
						idleMinutes: 0
					}
				})
			);
		});

		it('will not save a default above the ceiling, and says so', async () => {
			render(LimitsSection);
			await userEvent.type(field('ceiling', 'Memory'), '8');
			await userEvent.type(field('default', 'Memory'), '16');
			expect(screen.getByText('The default is above the ceiling.')).toBeInTheDocument();
			expect(field('default', 'Memory')).toHaveAttribute('aria-invalid', 'true');
			expect(save()).toBeDisabled();
		});

		it('will not save a size that is not positive', async () => {
			render(LimitsSection);
			await userEvent.type(field('default', 'CPUs'), '0');
			expect(save()).toBeDisabled();
		});

		it('will not save a size out of its bounds, and says which', async () => {
			render(LimitsSection);
			await userEvent.type(field('ceiling', 'Memory'), '99999');
			expect(field('ceiling', 'Memory')).toHaveAttribute('aria-invalid', 'true');
			expect(screen.getByText(/A size is 0.1 to 1000 CPUs/)).toBeInTheDocument();
			expect(save()).toBeDisabled();
		});
	});
});
