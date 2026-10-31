import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { prefs } from '$lib/app/prefs.svelte';
import { aPing, aProject, aRun, aTicket } from '$test/fixtures';
import { bindable } from '$test/fixtures/app.svelte';
import CommandPalette from './CommandPalette.svelte';

vi.mock('$lib/api', () => ({ api: { tickets: { list: vi.fn() } } }));

const setup = (open = true) => {
	const props = bindable({ open });
	const view = render(CommandPalette, props);
	return { props, ...view };
};
const search = () => screen.getByRole('textbox', { name: 'Search' });
const labels = () => screen.getAllByRole('option').map((o) => o.textContent);
const selected = () => screen.getByRole('option', { selected: true });

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.tickets.list).mockResolvedValue([]);
	fleet.reset();
	prefs.theme = 'ember-sky';
	prefs.collapsed = false;
});

describe('CommandPalette, opening', () => {
	it('is nothing while closed, and fetches nothing', () => {
		setup(false);
		expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
		expect(api.tickets.list).not.toHaveBeenCalled();
	});

	it('opens focused on the search, on the first row', async () => {
		setup();
		expect(screen.getByRole('dialog', { name: 'Command palette' })).toBeInTheDocument();
		await waitFor(() => expect(search()).toHaveFocus());
		expect(selected()).toHaveTextContent('Desk');
	});

	it('starts clean each time, with the tickets fresh', async () => {
		const { props } = setup();
		await userEvent.type(search(), 'xyz');

		props.open = false;
		await tick();
		await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
		props.open = true;
		await tick();

		expect(await screen.findByRole('textbox', { name: 'Search' })).toHaveValue('');
		expect(api.tickets.list).toHaveBeenCalledTimes(2);
	});

	it('works without tickets when they fail to load', async () => {
		vi.mocked(api.tickets.list).mockRejectedValue(new Error('down'));
		setup();
		await userEvent.type(search(), 'desk');
		expect(selected()).toHaveTextContent('Desk');
	});
});

describe('CommandPalette, listing', () => {
	it('offers pages, actions and runs before any search, not workshops or tickets', async () => {
		fleet.projects = [aProject({ name: 'billing' })];
		fleet.runs = [aRun({ title: 'Fix the invoice' })];
		vi.mocked(api.tickets.list).mockResolvedValue([aTicket({ title: 'A ticket' })]);
		setup();
		await tick();

		const all = labels().join('|');
		expect(all).toContain('New ticket');
		expect(all).toContain('Fix the invoice');
		expect(all).not.toContain('billing');
		expect(all).not.toContain('A ticket');
	});

	it('shows the pending count on the inbox and the chord on the others', () => {
		fleet.pending = [aPing({ id: '1' }), aPing({ id: '2' })];
		setup();
		expect(screen.getByRole('option', { name: /Inbox/ })).toHaveTextContent('2 pending');
		expect(screen.getByRole('option', { name: /Runs/ })).toHaveTextContent('G R');
	});

	it('leaves the current theme out of the actions', () => {
		prefs.theme = 'forest';
		setup();
		expect(screen.queryByRole('option', { name: /Use the forest theme/ })).not.toBeInTheDocument();
		expect(screen.getByRole('option', { name: /Use the light theme/ })).toBeInTheDocument();
	});

	it('lists live runs before finished ones, skips archived ones', async () => {
		fleet.runs = [
			aRun({ id: 'a', title: 'Done', state: 'completed', updatedAt: '2026-10-31T12:00:00Z' }),
			aRun({ id: 'b', title: 'Older live', updatedAt: '2026-10-01T12:00:00Z' }),
			aRun({ id: 'c', title: 'Newer live', updatedAt: '2026-10-02T12:00:00Z' }),
			aRun({ id: 'd', title: 'Shelved', state: 'archived' })
		];
		setup();
		await userEvent.type(search(), 'runs');

		const runs = labels().filter((l) => /Done|live|Shelved/.test(l ?? ''));
		expect(runs.map((l) => l?.match(/Done|Older live|Newer live|Shelved/)?.[0])).toEqual([
			'Newer live',
			'Older live',
			'Done'
		]);
	});
});

describe('CommandPalette, filtering', () => {
	it('filters on every word, in any order, across label and detail', async () => {
		const repo = (url: string) => ({ url }) as never;
		fleet.projects = [
			aProject({ id: 'a', name: 'billing', repo: repo('https://git.test/acme/pay') }),
			aProject({ id: 'b', name: 'web', repo: repo('https://git.test/acme/web') }),
			aProject({ id: 'c', name: 'billing-old', repo: repo('https://git.test/other/pay') })
		];
		setup();
		await userEvent.type(search(), 'acme billing');
		expect(labels()).toHaveLength(1);
		expect(selected()).toHaveTextContent('billing');
	});

	it('finds tickets by title and by their tracker key', async () => {
		vi.mocked(api.tickets.list).mockResolvedValue([
			aTicket({ title: 'Imported', source: { key: 'PROJ-7' } as never })
		]);
		setup();
		await userEvent.type(search(), 'proj-7');
		expect(selected()).toHaveTextContent('Imported');
	});

	it('reaches a whole group by its heading', async () => {
		fleet.projects = [aProject({ id: 'a', name: 'one' }), aProject({ id: 'b', name: 'two' })];
		setup();
		await userEvent.type(search(), 'workshops');
		const shown = labels().join('|');
		expect(shown).toContain('one');
		expect(shown).toContain('two');
	});

	it('says so when nothing matches', async () => {
		setup();
		await userEvent.type(search(), 'zzz');
		expect(screen.getByText('Nothing matches “zzz”.')).toBeInTheDocument();
		await userEvent.keyboard('{Enter}');
		expect(goto).not.toHaveBeenCalled();
	});
});

describe('CommandPalette, the keyboard', () => {
	it('moves through the rows, stopping at both ends', async () => {
		setup();
		const count = labels().length;
		await userEvent.click(search());

		await userEvent.keyboard('{ArrowUp}');
		expect(selected()).toHaveTextContent('Desk');

		await userEvent.keyboard('{ArrowDown}');
		expect(selected()).toHaveTextContent('Inbox');

		await userEvent.keyboard(`{ArrowDown>${count + 3}/}`);
		expect(selected()).toBe(screen.getAllByRole('option').at(-1));
	});

	it('goes back to the top on a new query', async () => {
		setup();
		await userEvent.click(search());
		await userEvent.keyboard('{ArrowDown}{ArrowDown}');
		await userEvent.keyboard('e');
		expect(selected()).toBe(screen.getAllByRole('option')[0]);
	});

	it('keeps its place when a poll changes the list, clamped to what is left', async () => {
		fleet.runs = [aRun({ id: 'a', title: 'Run one' }), aRun({ id: 'b', title: 'Run two' })];
		setup();
		await userEvent.type(search(), 'run');
		await userEvent.keyboard('{ArrowDown>30/}');
		const last = labels().length - 1;
		expect(screen.getAllByRole('option')[last]).toHaveAttribute('aria-selected', 'true');

		fleet.runs = [aRun({ id: 'a', title: 'Run one' })];
		await tick();
		await waitFor(() =>
			expect(screen.getAllByRole('option').at(-1)).toHaveAttribute('aria-selected', 'true')
		);
	});

	it('follows Enter to the selected row and closes', async () => {
		const { props } = setup();
		await userEvent.click(search());
		await userEvent.keyboard('{ArrowDown}{Enter}');
		expect(goto).toHaveBeenCalledWith('/inbox');
		expect(props.open).toBe(false);
	});

	it('closes on Escape', async () => {
		const { props } = setup();
		await waitFor(() => expect(search()).toHaveFocus());
		await userEvent.keyboard('{Escape}');
		expect(props.open).toBe(false);
	});
});

describe('CommandPalette, doing things', () => {
	it.each([
		['workshop', 'billing', '/w/p1'],
		['run', 'Fix the invoice', '/runs/r1'],
		['ticket', 'Write docs', '/tickets/t1']
	])('jumps to a %s', async (_, title, href) => {
		fleet.projects = [aProject({ id: 'p1', name: 'billing' })];
		fleet.runs = [aRun({ id: 'r1', title: 'Fix the invoice' })];
		vi.mocked(api.tickets.list).mockResolvedValue([aTicket({ id: 't1', title: 'Write docs' })]);
		setup();
		await userEvent.type(search(), title);
		await userEvent.click(screen.getByRole('option', { name: new RegExp(title) }));
		expect(goto).toHaveBeenCalledWith(href);
	});

	it('names a ticket by its workshop when it has no tracker key', async () => {
		fleet.projects = [aProject({ id: 'p1', name: 'billing' })];
		vi.mocked(api.tickets.list).mockResolvedValue([
			aTicket({ title: 'Write docs', projectId: 'p1' })
		]);
		setup();
		await userEvent.type(search(), 'write');
		expect(selected()).toHaveTextContent('billing');
	});

	it('switches theme from a row, with no navigation', async () => {
		const { props } = setup();
		await userEvent.type(search(), 'forest');
		await userEvent.keyboard('{Enter}');
		expect(prefs.theme).toBe('forest');
		expect(goto).not.toHaveBeenCalled();
		expect(props.open).toBe(false);
	});

	it('offers to collapse or expand, whichever applies', async () => {
		setup();
		await userEvent.type(search(), 'sidebar');
		expect(selected()).toHaveTextContent('Collapse sidebar');
		await userEvent.keyboard('{Enter}');
		expect(prefs.collapsed).toBe(true);
	});

	it('moves the selection with the pointer', async () => {
		setup();
		await userEvent.hover(screen.getByRole('option', { name: /Tickets/ }));
		expect(selected()).toHaveTextContent('Tickets');
	});

	it('closes on a click beside it', async () => {
		const { props, container } = setup();
		await userEvent.pointer({ keys: '[MouseLeft>]', target: container.querySelector('.cp-wrap')! });
		expect(props.open).toBe(false);
	});
});
