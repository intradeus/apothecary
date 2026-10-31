import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { aFlow, optionsOf, pick } from '$test/fixtures/flow.svelte';
import { deferred } from '$test/helpers';
import type { Flow } from '$lib/domain';
import FlowSelect from './FlowSelect.svelte';

vi.mock('$lib/api', () => ({ api: { flows: { list: vi.fn() } } }));

const LABEL = 'Default · builder';
const flows = [
	aFlow({ name: 'zeta' }),
	aFlow({ name: 'builder', shadowsBuiltin: true }),
	aFlow({ name: 'reviewer', builtin: true, operatesOn: 'merge_request' }),
	aFlow({ name: 'alpha', builtin: true })
];

const setup = (over: Partial<Parameters<typeof FlowSelect>[1]> = {}) =>
	render(FlowSelect, { projectId: 'p1', id: 'fs', fallbackLabel: LABEL, ...over });
const field = () => screen.getByRole('combobox');

describe('FlowSelect', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		vi.mocked(api.flows.list).mockResolvedValue(flows);
	});

	it('waits for the list, disabled', () => {
		vi.mocked(api.flows.list).mockReturnValue(deferred<Flow[]>().promise);
		setup();
		expect(field()).toBeDisabled();
		expect(field()).toHaveTextContent('Loading flows…');
	});

	it('asks for nothing without a project', () => {
		setup({ projectId: '' });
		expect(api.flows.list).not.toHaveBeenCalled();
	});

	it('lists the default, the workshop’s flows, then the templates, each by name', async () => {
		setup();
		await vi.waitFor(() => expect(field()).toBeEnabled());
		expect(await optionsOf(/.*/)).toEqual([
			LABEL,
			'builder (customised template)',
			'zeta',
			'alpha',
			'reviewer'
		]);
		expect(api.flows.list).toHaveBeenCalledExactlyOnceWith('p1');
	});

	it('heads each group', async () => {
		setup();
		await vi.waitFor(() => expect(field()).toBeEnabled());
		await userEvent.click(field());
		expect(await screen.findByText('Workshop flows')).toBeInTheDocument();
		expect(screen.getByText('Built-in templates')).toBeInTheDocument();
	});

	it('takes a pick', async () => {
		setup();
		await vi.waitFor(() => expect(field()).toBeEnabled());
		await pick(/.*/, 'zeta');
		expect(field()).toHaveTextContent('zeta');
	});

	it('degrades to free text when the list cannot be loaded', async () => {
		vi.mocked(api.flows.list).mockRejectedValue(new Error('down'));
		setup();
		const input = await screen.findByPlaceholderText(LABEL);
		expect(input).toHaveAttribute('id', 'fs');
		expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
	});

	it('loads again for another project, and recovers from a failure', async () => {
		vi.mocked(api.flows.list).mockRejectedValueOnce(new Error('down'));
		const { rerender } = setup();
		await screen.findByPlaceholderText(LABEL);
		await rerender({ projectId: 'p2' });
		await vi.waitFor(() => expect(field()).toBeEnabled());
		expect(api.flows.list).toHaveBeenLastCalledWith('p2');
	});

	describe('narrowed to what its runs are started on', () => {
		const narrowed = { operatesOn: 'merge_request' as const };

		it('offers only those flows', async () => {
			setup(narrowed);
			await vi.waitFor(() => expect(field()).toBeEnabled());
			expect(await optionsOf(/.*/)).toEqual([LABEL, 'reviewer']);
		});

		it('leaves the default out when its flow does not fit', async () => {
			setup({ ...narrowed, fallbackFlow: 'builder' });
			await vi.waitFor(() => expect(field()).toBeEnabled());
			expect(await optionsOf(/.*/)).toEqual(['reviewer']);
		});

		it('keeps the default when its flow fits', async () => {
			setup({ ...narrowed, fallbackFlow: 'reviewer' });
			await vi.waitFor(() => expect(field()).toBeEnabled());
			expect(await optionsOf(/.*/)).toContain(LABEL);
		});

		it('moves a choice that no longer fits to the default', async () => {
			setup({ ...narrowed, value: 'zeta' });
			await vi.waitFor(() => expect(field()).toHaveTextContent(LABEL));
		});

		it('moves it to the first that fits when the default does not', async () => {
			setup({ ...narrowed, value: 'zeta', fallbackFlow: 'builder' });
			await vi.waitFor(() => expect(field()).toHaveTextContent('reviewer'));
		});

		it('moves an empty default that does not fit', async () => {
			setup({ ...narrowed, fallbackFlow: 'builder' });
			await vi.waitFor(() => expect(field()).toHaveTextContent('reviewer'));
		});

		it('leaves a choice that fits', async () => {
			setup({ ...narrowed, value: 'reviewer' });
			await vi.waitFor(() => expect(field()).toBeEnabled());
			expect(field()).toHaveTextContent('reviewer');
		});
	});

	it('keeps to the project in hand when an older answer arrives late', async () => {
		const first = deferred<Flow[]>();
		const second = deferred<Flow[]>();
		vi.mocked(api.flows.list)
			.mockReturnValueOnce(first.promise)
			.mockReturnValueOnce(second.promise);
		const { rerender } = setup();
		await rerender({ projectId: 'p2' });

		second.resolve([aFlow({ name: 'mine' })]);
		await vi.waitFor(() => expect(field()).toBeEnabled());
		first.resolve([aFlow({ name: 'theirs' })]);
		await Promise.resolve();
		await Promise.resolve();

		const offered = await optionsOf(/.*/);
		expect(offered).toContain('mine');
		expect(offered).not.toContain('theirs');
	});

	it('takes no failure for the project in hand from an older answer', async () => {
		const first = deferred<Flow[]>();
		const second = deferred<Flow[]>();
		vi.mocked(api.flows.list)
			.mockReturnValueOnce(first.promise)
			.mockReturnValueOnce(second.promise);
		const { rerender } = setup();
		await rerender({ projectId: 'p2' });

		second.resolve([aFlow({ name: 'mine' })]);
		await vi.waitFor(() => expect(field()).toBeEnabled());
		first.reject(new Error('gone'));
		await Promise.resolve();
		await Promise.resolve();

		expect(await optionsOf(/.*/)).toContain('mine');
	});
});
