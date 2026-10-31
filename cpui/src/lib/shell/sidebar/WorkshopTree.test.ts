import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fleet } from '$lib/app/fleet.svelte';
import { aPing, aProject, aRun } from '$test/fixtures';
import WorkshopTree from './WorkshopTree.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

const billing = aProject({ id: 'p1', name: 'billing' });
const web = aProject({ id: 'p2', name: 'web' });
const run = aRun({ id: '01RUNABCD', title: 'Fix the invoice', projectId: 'p1' });

beforeEach(() => {
	fleet.reset();
	fleet.projects = [billing, web];
	fleet.runs = [run];
});

describe('WorkshopTree', () => {
	it('links each workshop', () => {
		render(WorkshopTree, { workshops: [billing, web] });
		expect(screen.getByRole('link', { name: /billing/ })).toHaveAttribute('href', '/w/p1');
		expect(screen.getByRole('link', { name: /web/ })).toHaveAttribute('href', '/w/p2');
	});

	it('counts live runs and pending pings on a row', () => {
		fleet.pending = [aPing({ id: 'a', runId: run.id }), aPing({ id: 'b', runId: run.id })];
		render(WorkshopTree, { workshops: [billing, web] });
		expect(screen.getByRole('link', { name: /billing/ })).toHaveTextContent(/billing.*1.*2/);
		expect(screen.getByRole('link', { name: /web/ })).not.toHaveTextContent(/\d/);
	});

	it('expands only the current workshop into its live runs', () => {
		const { unmount } = render(WorkshopTree, { workshops: [billing, web] });
		expect(screen.queryByRole('link', { name: /Fix the invoice/ })).not.toBeInTheDocument();
		unmount();

		render(WorkshopTree, { workshops: [billing, web], currentWorkshop: 'p1' });
		expect(screen.getByRole('link', { name: /Fix the invoice/ })).toHaveAttribute(
			'href',
			'/runs/01RUNABCD'
		);
	});

	it('leaves a current workshop with no live run unexpanded', () => {
		render(WorkshopTree, { workshops: [billing, web], currentWorkshop: 'p2' });
		expect(screen.queryByRole('link', { name: /Fix the invoice/ })).not.toBeInTheDocument();
	});

	it('invites the first workshop on an empty list', () => {
		render(WorkshopTree, { workshops: [] });
		const links = screen.getAllByRole('link');
		expect(screen.getByRole('link', { name: /Set up your first workshop/ })).toHaveAttribute(
			'href',
			'/w/new'
		);
		expect(links).toHaveLength(2);
	});

	it('tells the drawer a link was followed', async () => {
		const onnavigate = vi.fn();
		render(WorkshopTree, { workshops: [billing], currentWorkshop: 'p1', onnavigate });
		document.addEventListener('click', (e) => e.preventDefault(), { once: true });
		await userEvent.click(screen.getByRole('link', { name: /Fix the invoice/ }));
		expect(onnavigate).toHaveBeenCalledOnce();
	});
});
