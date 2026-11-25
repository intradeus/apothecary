import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page } from '$app/state';
import { goto } from '$app/navigation';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { prefs } from '$lib/app/prefs.svelte';
import { session } from '$lib/app/session.svelte';
import type { Me } from '$lib/domain';
import { aPing, aProject, aRun, aTicketRow } from '$test/fixtures';
import { bindable } from '$test/fixtures/app.svelte';
import { aHold, anOpenMr } from '$test/fixtures/run-components';
import { assistant } from '$lib/assistant/assistant.svelte';
import CommandPalette from './CommandPalette.svelte';

vi.mock('$lib/api', () => ({
	api: {
		projects: { tickets: vi.fn(), mergeRequests: vi.fn() },
		integrations: { list: vi.fn() }
	}
}));

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
	vi.mocked(api.projects.tickets).mockResolvedValue({ tickets: [], next: '' });
	vi.mocked(api.projects.mergeRequests).mockResolvedValue([]);
	vi.mocked(api.integrations.list).mockResolvedValue([]);
	fleet.reset();
	session.me = null;
	page.params = {};
	prefs.theme = 'ember-sky';
	prefs.collapsed = false;
});

describe('CommandPalette, opening', () => {
	it('is nothing while closed, and fetches nothing', () => {
		setup(false);
		expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
		expect(api.projects.tickets).not.toHaveBeenCalled();
	});

	it('opens focused on the search, on the first row', async () => {
		setup();
		expect(screen.getByRole('dialog', { name: 'Command palette' })).toBeInTheDocument();
		await waitFor(() => expect(search()).toHaveFocus());
		expect(selected()).toHaveTextContent('Desk');
	});

	it('starts clean each time', async () => {
		const { props } = setup();
		await userEvent.type(search(), 'xyz');

		props.open = false;
		await tick();
		await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
		props.open = true;
		await tick();

		expect(await screen.findByRole('textbox', { name: 'Search' })).toHaveValue('');
	});

	it('works without tickets when they fail to load', async () => {
		fleet.projects = [aProject({ id: 'p1' })];
		vi.mocked(api.projects.tickets).mockRejectedValue(new Error('down'));
		setup();
		await userEvent.type(search(), 'desk');
		expect(selected()).toHaveTextContent('Desk');
	});
});

describe('CommandPalette, listing', () => {
	it('offers pages, actions and runs before any search, not workshops or tickets', async () => {
		fleet.projects = [aProject({ name: 'billing' })];
		fleet.runs = [aRun({ title: 'Fix the invoice' })];
		setup();
		await tick();

		const all = labels().join('|');
		expect(all).toContain('New run');
		expect(all).toContain('New workshop');
		expect(all).toContain('Fix the invoice');
		expect(all).not.toContain('billing');
		expect(all).not.toContain('A ticket');
	});

	it('offers a guest no New workshop: it creates none', async () => {
		session.me = { user: { id: 'u', handle: 'lu', name: 'Lu', role: 'guest' } } as Me;
		setup();
		await tick();
		expect(labels().join('|')).not.toContain('New workshop');
	});

	it('offers New ticket only where the workshop’s source is the internal tracker', async () => {
		fleet.projects = [aProject({ id: 'p1' })];
		setup();
		await userEvent.type(search(), 'new ticket');
		await userEvent.keyboard('{Enter}');
		expect(goto).toHaveBeenCalledWith('/tickets/new?project=p1');
	});

	it('offers no New ticket on a remote source', async () => {
		fleet.projects = [
			aProject({ id: 'p1', ticketSource: { kind: 'jira', name: 'Jira', home: '' } })
		];
		setup();
		await tick();
		expect(labels().join('|')).not.toContain('New ticket');
	});

	it('offers no New ticket when the source is gone', async () => {
		fleet.projects = [aProject({ id: 'p1', ticketSource: undefined })];
		setup();
		await tick();
		expect(labels().join('|')).not.toContain('New ticket');
	});

	it('shows the pending count, held runs included, on the inbox and the chord on the others', () => {
		fleet.runs = [aRun({ id: 'h', hold: aHold({ stageId: 'plan' }) })];
		fleet.pending = [aPing({ id: '1' }), aPing({ id: '2' })];
		setup();
		expect(screen.getByRole('option', { name: /Inbox/ })).toHaveTextContent('3 pending');
		expect(screen.getByRole('option', { name: /Runs/ })).toHaveTextContent('G R');
	});

	it('offers no theme switching: the user card does that', async () => {
		setup();
		await userEvent.type(search(), 'theme');
		expect(screen.queryByRole('option', { name: /theme/i })).not.toBeInTheDocument();
	});

	it('follows the mouse only when it moves, not a list scrolling under it', async () => {
		setup();
		const rows = screen.getAllByRole('option');
		const at = { screenX: 40, screenY: 300, bubbles: true };
		rows[1].dispatchEvent(new PointerEvent('pointermove', at));
		await tick();
		expect(selected()).toBe(rows[1]);
		await userEvent.keyboard('{ArrowDown}{ArrowDown}');
		expect(selected()).toBe(rows[3]);
		// The list scrolled: another row is under the still pointer.
		rows[2].dispatchEvent(new PointerEvent('pointermove', at));
		await tick();
		expect(selected()).toBe(rows[3]);
		rows[2].dispatchEvent(new PointerEvent('pointermove', { ...at, screenY: 310 }));
		await tick();
		expect(selected()).toBe(rows[2]);
	});

	it('lists live runs before finished ones, skips archived ones', async () => {
		fleet.runs = [
			aRun({ id: 'a', title: 'Done', state: 'completed', updatedAt: '2026-10-31T12:00:00Z' }),
			aRun({ id: 'b', title: 'Older live', updatedAt: '2026-10-01T12:00:00Z' }),
			aRun({ id: 'c', title: 'Newer live', updatedAt: '2026-10-02T12:00:00Z' }),
			aRun({
				id: 'd',
				title: 'Shelved',
				state: 'completed',
				updatedAt: '2026-11-01T12:00:00Z',
				archivedAt: '2026-11-01T12:00:00Z'
			})
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

	it('asks the first workshop’s source for tickets once typing pauses', async () => {
		fleet.projects = [aProject({ id: 'p1', name: 'billing' }), aProject({ id: 'p2' })];
		vi.mocked(api.projects.tickets).mockResolvedValue({
			tickets: [aTicketRow({ key: 'PROJ-7', title: 'Imported' })],
			next: ''
		});
		setup();
		await userEvent.type(search(), 'proj-7');
		await waitFor(() => expect(selected()).toHaveTextContent('Imported'));
		expect(selected()).toHaveTextContent('PROJ-7 · billing');
		expect(api.projects.tickets).toHaveBeenCalledExactlyOnceWith('p1', { q: 'proj-7', limit: 20 });
	});

	it('searches the workshop whose page it is opened on', async () => {
		fleet.projects = [aProject({ id: 'p1' }), aProject({ id: 'p2' })];
		page.params = { workshop: 'p2' };
		setup();
		await userEvent.type(search(), 'x');
		await waitFor(() =>
			expect(api.projects.tickets).toHaveBeenCalledWith('p2', { q: 'x', limit: 20 })
		);
	});

	it('asks for no tickets with no workshop to ask', async () => {
		setup();
		await userEvent.type(search(), 'desk');
		await new Promise((r) => setTimeout(r, 300));
		expect(api.projects.tickets).not.toHaveBeenCalled();
	});

	it('drops an answer the query has moved past', async () => {
		fleet.projects = [aProject({ id: 'p1' })];
		let answer: (v: { tickets: ReturnType<typeof aTicketRow>[]; next: string }) => void = () => {};
		vi.mocked(api.projects.tickets).mockReturnValueOnce(new Promise((r) => (answer = r)));
		setup();
		await userEvent.type(search(), 'old');
		await waitFor(() => expect(api.projects.tickets).toHaveBeenCalledTimes(1));
		await userEvent.type(search(), 'er');
		answer({ tickets: [aTicketRow({ title: 'Stale old' })], next: '' });
		await waitFor(() => expect(api.projects.tickets).toHaveBeenCalledTimes(2));
		expect(screen.queryByRole('option', { name: /Stale/ })).not.toBeInTheDocument();
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
		['ticket', 'Write docs', '/w/p1/tickets/PROJ-1']
	])('jumps to a %s', async (_, title, href) => {
		fleet.projects = [aProject({ id: 'p1', name: 'billing' })];
		fleet.runs = [aRun({ id: 'r1', title: 'Fix the invoice' })];
		vi.mocked(api.projects.tickets).mockImplementation((_, query) =>
			Promise.resolve({
				tickets:
					query?.q === 'Write docs' ? [aTicketRow({ key: 'PROJ-1', title: 'Write docs' })] : [],
				next: ''
			})
		);
		setup();
		await userEvent.type(search(), title);
		await userEvent.click(await screen.findByRole('option', { name: new RegExp(title) }));
		expect(goto).toHaveBeenCalledWith(href);
	});

	it('opens the New run dialog, closing itself', async () => {
		const { props } = setup();
		await userEvent.type(search(), 'new run');
		expect(selected()).toHaveTextContent('New run');
		await userEvent.keyboard('{Enter}');
		expect(props.open).toBe(false);
		expect(await screen.findByRole('dialog', { name: 'New run' })).toBeInTheDocument();
		expect(goto).not.toHaveBeenCalled();
	});

	it('opens the assistant, closing itself', async () => {
		assistant.close();
		const { props } = setup();
		await userEvent.type(search(), 'ask the');
		expect(selected()).toHaveTextContent('Ask the assistant');
		await userEvent.keyboard('{Enter}');
		expect(props.open).toBe(false);
		expect(assistant.open).toBe(true);
		assistant.close();
	});

	it('jumps to a merge request of any workshop, reading them once a search is typed', async () => {
		fleet.projects = [
			aProject({ id: 'p1', name: 'billing' }),
			aProject({ id: 'p2', name: 'docs' })
		];
		vi.mocked(api.projects.mergeRequests).mockImplementation((id) =>
			id === 'p2'
				? Promise.reject(new Error('no token'))
				: Promise.resolve([{ mr: anOpenMr({ iid: 7, title: 'Add keys', author: 'ada' }), runs: 0 }])
		);
		setup();
		await tick();
		expect(api.projects.mergeRequests).not.toHaveBeenCalled();

		await userEvent.type(search(), 'add keys');
		const row = await screen.findByRole('option', { name: /!7 Add keys/ });
		expect(row).toHaveTextContent('ada · billing');
		expect(api.projects.mergeRequests).toHaveBeenCalledTimes(2);
		await userEvent.click(row);
		expect(goto).toHaveBeenCalledWith('/merge-requests/p1/7');
	});

	it('offers the merge requests page among the places to go', async () => {
		setup();
		await userEvent.type(search(), 'merge');
		expect(screen.getByRole('option', { name: /Merge requests.*G M/ })).toBeInTheDocument();
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
