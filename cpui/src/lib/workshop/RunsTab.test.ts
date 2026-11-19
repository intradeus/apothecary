import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import type { Project, Run } from '$lib/domain';
import { aRun, aViewerProject } from '$test/fixtures';
import { aWorkshop, choose } from '$test/fixtures/app.svelte';
import WithHeader from '$test/fixtures/WithHeader.svelte';
import RunsTab from './RunsTab.svelte';

vi.mock('$lib/api', () => ({
	api: {
		projects: { tickets: vi.fn() },
		integrations: { list: vi.fn() },
		runs: { get: vi.fn() },
		flows: { version: vi.fn() }
	}
}));

const project = aWorkshop({ id: 'p1' });
const setup = (runs: Run[] = [], p: Project = project) =>
	render(WithHeader, { props: { page: RunsTab, props: { project: p, runs } } });
const live = aRun({ id: '01LIVERUNABCD', title: 'Live one', branch: 'feat/live' });
const done = aRun({ id: '01DONERUNWXYZ', title: 'Done one', state: 'completed' });
const finishedTab = () => screen.getByRole('radio', { name: /Finished/ });
const search = () => screen.getByPlaceholderText('Search title, id, branch');

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.projects.tickets).mockResolvedValue({ tickets: [], next: '' });
	vi.mocked(api.integrations.list).mockResolvedValue([]);
	fleet.reset();
});

describe('RunsTab', () => {
	it('holds New run where the role may not start one (run:operate)', () => {
		setup([], aViewerProject({ id: 'p1' }));
		const btn = screen.getByRole('button', { name: /New run/ });
		expect(btn).toBeDisabled();
		expect(btn).toHaveAttribute('title', 'Your role in this workshop does not allow this');
	});

	it('shows active runs, counting both tabs', () => {
		setup([live, done]);
		expect(screen.getByRole('link', { name: /Live one/ })).toBeInTheDocument();
		expect(screen.queryByRole('link', { name: /Done one/ })).not.toBeInTheDocument();
		expect(screen.getByRole('radio', { name: /Active/ })).toHaveTextContent('1');
		expect(finishedTab()).toHaveTextContent('1');
	});

	it('switches to finished runs, and the link follows', async () => {
		setup([live, done]);
		await userEvent.click(finishedTab());
		expect(screen.getByRole('link', { name: /Done one/ })).toBeInTheDocument();
		expect(screen.getByRole('link', { name: /Open in Runs/ })).toHaveAttribute(
			'href',
			'/runs?project=p1&tab=finished'
		);
	});

	it('puts the most recently updated first', () => {
		const { container } = setup([
			aRun({ id: 'a', title: 'Older', updatedAt: '2026-10-01T00:00:00Z' }),
			aRun({ id: 'b', title: 'Newer', updatedAt: '2026-10-02T00:00:00Z' })
		]);
		const text = container.textContent ?? '';
		expect(text.indexOf('Newer')).toBeLessThan(text.indexOf('Older'));
	});

	it.each([
		['title', 'LIVE ONE'],
		['id', '01liverun'],
		['short id', 'r_abcd'],
		['branch', 'feat/live']
	])('finds a run by %s', async (_, q) => {
		setup([live, aRun({ id: 'other', title: 'Other' })]);
		await userEvent.type(search(), q);
		expect(screen.getByRole('link', { name: /Live one/ })).toBeInTheDocument();
		expect(screen.queryByRole('link', { name: /Other/ })).not.toBeInTheDocument();
	});

	it('says nothing matched, by tab', async () => {
		setup([live]);
		await userEvent.type(search(), 'zzz');
		expect(screen.getByText('No active run matches “zzz”.')).toBeInTheDocument();

		await userEvent.click(finishedTab());
		expect(screen.getByText('No finished run matches “zzz”.')).toBeInTheDocument();
	});

	it('points to tickets with no active run', () => {
		setup();
		expect(screen.getByText('All still on the shelf')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: /New ticket/ })).toHaveAttribute(
			'href',
			'/tickets/new?project=p1'
		);
		expect(screen.getByRole('link', { name: 'Tickets' })).toHaveAttribute(
			'href',
			'/w/p1?tab=tickets'
		);
	});

	it('offers no New ticket on a remote source', () => {
		setup([], aWorkshop({ id: 'p1', ticketSource: { kind: 'jira', name: 'Jira', home: '' } }));
		expect(screen.queryByRole('link', { name: /New ticket/ })).not.toBeInTheDocument();
	});

	describe('the finished filters', () => {
		const AT = '2026-10-31T12:00:00Z';
		const runs = [
			aRun({ id: 'c', title: 'Kept done', state: 'completed' }),
			aRun({ id: 'f', title: 'Shelved failure', state: 'failed', archivedAt: AT }),
			aRun({ id: 'a', title: 'Kept abandon', state: 'abandoned' })
		];
		const titles = () =>
			screen
				.getAllByRole('link', { name: /Kept|Shelved/ })
				.map((l) => l.textContent?.match(/Kept done|Shelved failure|Kept abandon/)?.[0])
				.sort();

		it('shows every finished run, archived ones too, until narrowed', async () => {
			setup([live, ...runs]);
			expect(screen.queryByRole('combobox', { name: 'Outcome' })).not.toBeInTheDocument();
			await userEvent.click(finishedTab());
			expect(titles()).toEqual(['Kept abandon', 'Kept done', 'Shelved failure']);
			expect(screen.getByText('archived')).toBeInTheDocument();
		});

		it('narrows by outcome and by archiving, independently, and the link follows', async () => {
			setup(runs);
			await userEvent.click(finishedTab());

			await choose('Archived', 'Not archived');
			expect(titles()).toEqual(['Kept abandon', 'Kept done']);
			expect(finishedTab()).toHaveTextContent('2');

			await choose('Outcome', 'Failed');
			expect(screen.getByText('No finished run matches')).toBeInTheDocument();

			await choose('Archived', 'Archived only');
			expect(titles()).toEqual(['Shelved failure']);
			expect(screen.getByRole('link', { name: /Open in Runs/ })).toHaveAttribute(
				'href',
				'/runs?project=p1&tab=finished&outcome=failed&archived=yes'
			);

			await choose('Archived', 'Archived or not');
			await choose('Outcome', 'Completed');
			expect(titles()).toEqual(['Kept done']);
		});

		it('clears the filters when nothing passes them', async () => {
			setup(runs);
			await userEvent.click(finishedTab());
			await choose('Outcome', 'Abandoned');
			await choose('Archived', 'Archived only');
			await userEvent.click(screen.getByRole('button', { name: 'Clear filters' }));
			expect(titles()).toHaveLength(3);
		});
	});

	it('says none have finished', async () => {
		setup();
		await userEvent.click(finishedTab());
		expect(screen.getByText('No finished runs yet')).toBeInTheDocument();
	});

	it("opens the new-run dialog from the page header, on this workshop's tickets", async () => {
		setup();
		await userEvent.click(screen.getByRole('button', { name: /New run/ }));
		await waitFor(() =>
			expect(api.projects.tickets).toHaveBeenCalledExactlyOnceWith('p1', { limit: 50 })
		);
		expect(screen.getByRole('dialog')).toBeInTheDocument();
	});
});
