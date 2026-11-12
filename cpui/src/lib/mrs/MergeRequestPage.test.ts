import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import type { MergeRequestRow } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { aRun } from '$test/fixtures';
import { aFlow, aWorkshop } from '$test/fixtures/app.svelte';
import { anOpenMr } from '$test/fixtures/run-components';
import { deferred } from '$test/helpers';
import MergeRequestPage from './MergeRequestPage.svelte';

vi.mock('$lib/api', () => ({
	api: {
		projects: {
			mergeRequest: vi.fn(),
			startRunOnMR: vi.fn(),
			mergeRequests: vi.fn(),
			get: vi.fn()
		},
		runs: { abort: vi.fn() },
		flows: { list: vi.fn(), get: vi.fn(), binding: vi.fn() },
		integrations: { list: vi.fn(), models: vi.fn() },
		skillsets: { list: vi.fn(), versions: vi.fn() }
	},
	busyRunOf: (e: unknown) => (e as { busy?: string }).busy
}));

const reviewer = aFlow({ name: 'reviewer', operatesOn: 'merge_request' });
const row = (over: Partial<MergeRequestRow['mr']> = {}): MergeRequestRow => ({
	mr: anOpenMr({ iid: 12, title: 'Add keys', body: 'Why **this**', ...over }),
	runs: 0
});

function setup(r: MergeRequestRow | Promise<MergeRequestRow> = row(), repo = '') {
	vi.mocked(api.projects.mergeRequest).mockImplementation(() => Promise.resolve(r));
	return render(MergeRequestPage, { projectId: 'p1', iid: 12, repo });
}
const startBtn = () => screen.findByRole('button', { name: /Start run/ });

beforeEach(() => {
	vi.resetAllMocks();
	toasts.list = [];
	fleet.projects = [aWorkshop({ id: 'p1', name: 'shop', defaultFlow: 'builder' })];
	fleet.runs = [];
	vi.mocked(api.flows.list).mockResolvedValue([reviewer, aFlow({ name: 'builder' })]);
	vi.mocked(api.flows.get).mockResolvedValue(reviewer);
	vi.mocked(api.flows.binding).mockResolvedValue({
		projectId: 'p1',
		flowName: 'reviewer',
		params: {},
		updatedAt: ''
	});
	vi.mocked(api.integrations.list).mockResolvedValue([]);
	vi.mocked(api.projects.get).mockResolvedValue(aWorkshop({ id: 'p1' }));
	vi.mocked(api.skillsets.list).mockResolvedValue([]);
});
afterEach(() => {
	fleet.projects = [];
	fleet.runs = [];
});

describe('MergeRequestPage', () => {
	it('shows a skeleton until it is read', () => {
		setup(new Promise(() => {}));
		expect(screen.queryByRole('heading', { name: 'Add keys' })).not.toBeInTheDocument();
	});

	it('shows the merge request and its description, asking for it by repository', async () => {
		setup(row(), 'docs');
		expect(await screen.findByRole('heading', { name: 'Add keys' })).toBeInTheDocument();
		expect(api.projects.mergeRequest).toHaveBeenCalledWith('p1', 12, 'docs');
		expect(screen.getByText('this')).toBeInTheDocument();
		expect(screen.getByText(/feat\/idempotency → main/)).toBeInTheDocument();
		expect(screen.getByTitle('Open on the repository host')).toHaveAttribute(
			'href',
			anOpenMr().url
		);
		expect(screen.getByText(/no ticket is made/)).toBeInTheDocument();
	});

	it('says when it has no description', async () => {
		setup(row({ body: '  ' }));
		expect(await screen.findByText('No description.')).toBeInTheDocument();
	});

	it('says when it cannot be found', async () => {
		vi.mocked(api.projects.mergeRequest).mockRejectedValue(new Error('no such merge request'));
		render(MergeRequestPage, { projectId: 'p1', iid: 12 });
		expect(await screen.findByText('Merge request not found')).toBeInTheDocument();
		expect(screen.getByText('no such merge request')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'All merge requests' })).toHaveAttribute(
			'href',
			'/merge-requests'
		);
	});

	it('lists the runs that went through it, from the shared poll', async () => {
		fleet.runs = [
			aRun({ id: '01RUNONIT0000000000000AAAA', projectId: 'p1', mrs: [] }),
			aRun({
				id: '01RUNONIT0000000000000BBBB',
				projectId: 'p1',
				adopted: { repo: 'repo', iid: 12, url: 'u' }
			})
		];
		setup();
		await screen.findByRole('heading', { name: 'Add keys' });
		expect(screen.getAllByRole('link', { name: /bbbb/i }).length).toBeGreaterThan(0);
		expect(screen.queryByText('No run has been through this merge request yet.')).toBeNull();
	});

	it('says when no run has been through it', async () => {
		setup();
		expect(
			await screen.findByText('No run has been through this merge request yet.')
		).toBeInTheDocument();
	});

	it('offers only the flows that start on a merge request', async () => {
		setup();
		await screen.findByRole('heading', { name: 'Add keys' });
		await userEvent.click(screen.getByRole('combobox', { name: 'Flow' }));
		expect(await screen.findByRole('option', { name: /reviewer/ })).toBeInTheDocument();
		expect(screen.queryByRole('option', { name: /builder/ })).not.toBeInTheDocument();
	});

	it('starts a run on it and opens the run', async () => {
		vi.mocked(api.projects.startRunOnMR).mockResolvedValue({ runId: '01NEWRUN00000000000000ZZZZ' });
		setup(row(), 'docs');
		await userEvent.click(await startBtn());
		await waitFor(() => expect(goto).toHaveBeenCalledWith('/runs/01NEWRUN00000000000000ZZZZ'));
		expect(api.projects.startRunOnMR).toHaveBeenCalledWith(
			'p1',
			12,
			expect.objectContaining({ repo: 'docs', flow: 'reviewer' })
		);
		expect(toasts.list.at(-1)?.text).toMatch(/started/);
	});

	it('holds the button while the start is on its way', async () => {
		const pending = deferred<{ runId: string }>();
		vi.mocked(api.projects.startRunOnMR).mockReturnValue(pending.promise);
		setup();
		const btn = await startBtn();
		await userEvent.click(btn);
		expect(btn).toHaveAttribute('aria-busy', 'true');
		await userEvent.click(btn);
		expect(api.projects.startRunOnMR).toHaveBeenCalledOnce();
		pending.resolve({ runId: 'r1' });
	});

	it('names the run in the way, and stops it on request', async () => {
		vi.mocked(api.projects.startRunOnMR).mockRejectedValue({ busy: '01BUSY0000000000000000BUSY' });
		vi.mocked(api.runs.abort).mockResolvedValue(undefined as never);
		setup();
		await userEvent.click(await startBtn());
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent(/still working on this merge request/);
		expect(screen.getByRole('link', { name: /Open run/ })).toHaveAttribute(
			'href',
			'/runs/01BUSY0000000000000000BUSY'
		);
	});

	it('shows any other refusal', async () => {
		vi.mocked(api.projects.startRunOnMR).mockRejectedValue(new Error('preflight: no model'));
		setup();
		await userEvent.click(await startBtn());
		expect(await screen.findByText('preflight: no model')).toBeInTheDocument();
	});

	it('starts nothing on one that is no longer open', async () => {
		setup(row({ state: 'merged' }));
		expect(await startBtn()).toBeDisabled();
		expect(screen.getByText(/This merge request is merged/)).toBeInTheDocument();
	});
});
