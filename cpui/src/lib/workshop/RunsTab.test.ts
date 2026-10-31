import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import type { Run } from '$lib/domain';
import { aRun } from '$test/fixtures';
import { aWorkshop } from '$test/fixtures/app.svelte';
import WithHeader from '$test/fixtures/WithHeader.svelte';
import RunsTab from './RunsTab.svelte';

vi.mock('$lib/api', () => ({
	api: { tickets: { list: vi.fn() }, runs: { get: vi.fn() }, flows: { version: vi.fn() } }
}));

const project = aWorkshop({ id: 'p1' });
const setup = (runs: Run[] = []) =>
	render(WithHeader, { props: { page: RunsTab, props: { project, runs } } });
const live = aRun({ id: '01LIVERUNABCD', title: 'Live one', branch: 'feat/live' });
const done = aRun({ id: '01DONERUNWXYZ', title: 'Done one', state: 'completed' });
const finishedTab = () => screen.getByRole('radio', { name: /Finished/ });
const search = () => screen.getByPlaceholderText('Search title, id, branch');

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.tickets.list).mockResolvedValue([]);
	fleet.reset();
});

describe('RunsTab', () => {
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

	it('says none have finished', async () => {
		setup();
		await userEvent.click(finishedTab());
		expect(screen.getByText('No finished runs yet')).toBeInTheDocument();
	});

	it("opens the new-run dialog from the page header, on this workshop's tickets", async () => {
		setup();
		await userEvent.click(screen.getByRole('button', { name: /New run/ }));
		await waitFor(() => expect(api.tickets.list).toHaveBeenCalledOnce());
		expect(screen.getByRole('dialog')).toBeInTheDocument();
	});
});
