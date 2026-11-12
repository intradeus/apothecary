import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { createRawSnippet } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { confirmDialog } from '$lib/ui/confirm.svelte';
import { aFlow, aWorkshop, bindable } from '$test/fixtures/app.svelte';
import StartRunPanel from './StartRunPanel.svelte';

vi.mock('$lib/api', () => ({
	api: {
		runs: { abort: vi.fn() },
		flows: { list: vi.fn(), get: vi.fn(), binding: vi.fn() },
		projects: { get: vi.fn(), mergeRequests: vi.fn() },
		integrations: { list: vi.fn(), models: vi.fn() },
		skillsets: { list: vi.fn(), versions: vi.fn() }
	}
}));
vi.mock('$lib/ui/confirm.svelte', () => ({ confirmDialog: vi.fn() }));

const reviewer = aFlow({ name: 'reviewer', operatesOn: 'merge_request' });
const builder = aFlow({ name: 'builder' });
const snippet = (text: string) => createRawSnippet(() => ({ render: () => `<p>${text}</p>` }));

function setup(over: Record<string, unknown> = {}) {
	const props = bindable({
		projectId: 'p1',
		busyRun: '',
		busyText: (id: string) => `run ${id} is in the way`,
		starting: false,
		onstart: vi.fn(),
		onerror: vi.fn(),
		idPrefix: 'x',
		...over
	});
	render(StartRunPanel, { props });
	return props;
}

beforeEach(() => {
	vi.resetAllMocks();
	fleet.projects = [aWorkshop({ id: 'p1', defaultFlow: 'builder' })];
	vi.mocked(api.flows.list).mockResolvedValue([reviewer, builder]);
	vi.mocked(api.flows.get).mockResolvedValue(builder);
	vi.mocked(api.flows.binding).mockResolvedValue({
		projectId: 'p1',
		flowName: 'builder',
		params: {},
		updatedAt: ''
	});
	vi.mocked(api.projects.get).mockResolvedValue(aWorkshop({ id: 'p1' }));
	vi.mocked(api.integrations.list).mockResolvedValue([]);
	vi.mocked(api.skillsets.list).mockResolvedValue([]);
});
afterEach(() => {
	fleet.projects = [];
});

describe('StartRunPanel', () => {
	it('puts the item’s own fields around the flow, and starts on Start', async () => {
		const props = setup({ before: snippet('above'), after: snippet('below') });
		expect(screen.getByText('above')).toBeInTheDocument();
		expect(screen.getByText('below')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Start run/ }));
		expect(props.onstart).toHaveBeenCalledOnce();
	});

	it('offers every flow, the workshop’s default first', async () => {
		setup();
		await userEvent.click(screen.getByRole('combobox', { name: 'Flow' }));
		expect(await screen.findByRole('option', { name: /Default · builder/ })).toBeInTheDocument();
		expect(screen.getByRole('option', { name: /reviewer/ })).toBeInTheDocument();
	});

	it('keeps to the flows of one kind of work item', async () => {
		setup({ operatesOn: 'merge_request' });
		await userEvent.click(screen.getByRole('combobox', { name: 'Flow' }));
		expect(await screen.findByRole('option', { name: /reviewer/ })).toBeInTheDocument();
		expect(screen.queryByRole('option', { name: /builder/ })).not.toBeInTheDocument();
	});

	it('holds Start while disabled, and says when it is starting', () => {
		setup({ disabled: true, starting: true });
		const start = screen.getByRole('button', { name: /Start run/ });
		expect(start).toBeDisabled();
		expect(start).toHaveAttribute('aria-busy', 'true');
	});

	describe('a run in the way', () => {
		const busy = '01BUSY0000000000000000WXYZ';

		it('names it and links it', () => {
			setup({ busyRun: busy });
			expect(screen.getByText(/is in the way/)).toHaveTextContent(/^run \S+ is in the way$/);
			expect(screen.getByRole('link', { name: /Open run/ })).toHaveAttribute(
				'href',
				`/runs/${busy}`
			);
		});

		it('stops it once confirmed, and clears the alert', async () => {
			vi.mocked(confirmDialog).mockResolvedValue(true);
			vi.mocked(api.runs.abort).mockResolvedValue(undefined as never);
			const props = setup({ busyRun: busy });
			await userEvent.click(screen.getByRole('button', { name: 'Abort run' }));
			expect(api.runs.abort).toHaveBeenCalledWith(busy);
			await waitFor(() => expect(props.busyRun).toBe(''));
		});

		it('leaves it when the stop is not confirmed', async () => {
			vi.mocked(confirmDialog).mockResolvedValue(false);
			setup({ busyRun: busy });
			await userEvent.click(screen.getByRole('button', { name: 'Abort run' }));
			expect(api.runs.abort).not.toHaveBeenCalled();
		});

		it('says why a stop failed', async () => {
			vi.mocked(confirmDialog).mockResolvedValue(true);
			vi.mocked(api.runs.abort).mockRejectedValue(new Error('gone'));
			const props = setup({ busyRun: busy });
			await userEvent.click(screen.getByRole('button', { name: 'Abort run' }));
			await waitFor(() => expect(props.onerror).toHaveBeenCalledWith('gone'));
			expect(props.busyRun).toBe(busy);
		});
	});
});
