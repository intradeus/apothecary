import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { goto, replaceState } from '$app/navigation';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { aPing, aProject, aRun, aTicket } from '$test/fixtures';
import { deferred } from '$test/helpers';
import Inbox from './Inbox.svelte';

vi.mock('$lib/api', () => ({
	api: {
		notices: { list: vi.fn() },
		pings: { list: vi.fn(), answer: vi.fn(), pending: vi.fn() },
		tickets: { list: vi.fn() },
		runs: { get: vi.fn(), list: vi.fn() },
		projects: { list: vi.fn() },
		system: { limits: vi.fn() },
		files: { upload: vi.fn() }
	}
}));

const at = (day: number) => `2026-10-${String(day).padStart(2, '0')}T12:00:00Z`;
const ping = (id: string, runId: string, day: number, over: Parameters<typeof aPing>[0] = {}) =>
	aPing({ id, runId, raisedAt: at(day), question: `Question ${id}?`, ...over });

/** The fleet as a poll left it. */
function loaded(
	pings: ReturnType<typeof aPing>[],
	runs = [aRun({ id: 'r1' }), aRun({ id: 'r2' })]
) {
	fleet.runs = runs;
	fleet.pending = pings;
	fleet.updatedAt = Date.now();
	vi.mocked(api.pings.pending).mockResolvedValue(pings);
	vi.mocked(api.runs.list).mockResolvedValue(runs);
}
const cards = () => screen.getAllByRole('button', { name: /Question/ });
const selected = () => cards().find((c) => c.getAttribute('aria-current') === 'true');
const approve = () => screen.getByRole('button', { name: 'Approve' });

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.tickets.list).mockResolvedValue([]);
	vi.mocked(api.pings.list).mockResolvedValue([]);
	vi.mocked(api.projects.list).mockResolvedValue([]);
	vi.mocked(api.system.limits).mockResolvedValue(null as never);
	vi.mocked(api.runs.list).mockResolvedValue([]);
	vi.mocked(api.pings.pending).mockResolvedValue([]);
	vi.mocked(api.notices.list).mockResolvedValue([]);
	fleet.reset();
	toasts.list = [];
	history.replaceState(null, '', '/inbox');
});

describe('Inbox, the pending tab', () => {
	it('claims nothing until the fleet has landed', () => {
		render(Inbox);
		expect(screen.queryByText('All quiet at the bench')).not.toBeInTheDocument();
	});

	it('says all quiet with nothing pending, and links the runs', () => {
		fleet.updatedAt = 1;
		render(Inbox);
		expect(screen.getByText('All quiet at the bench')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Open runs' })).toHaveAttribute('href', '/runs');
		expect(screen.getByText('nothing pending')).toBeInTheDocument();
	});

	it('offers a retry when the first load failed', async () => {
		fleet.error = 'engine down';
		render(Inbox);
		expect(screen.getByText('Could not load pings')).toBeInTheDocument();

		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(api.runs.list).toHaveBeenCalledOnce();
	});

	it('keeps the list when a later poll fails', () => {
		loaded([ping('a', 'r1', 1)]);
		fleet.error = 'blip';
		render(Inbox);
		expect(screen.queryByText('Could not load pings')).not.toBeInTheDocument();
		expect(cards()).toHaveLength(1);
	});

	it('shows one card per run, the newest ping heading it', () => {
		loaded([ping('old', 'r1', 1), ping('new', 'r1', 2), ping('other', 'r2', 3)]);
		render(Inbox);

		expect(cards()).toHaveLength(2);
		expect(cards()[0]).toHaveTextContent('Question other?');
		expect(cards()[1]).toHaveTextContent('Question new?');
		expect(cards()[1]).toHaveTextContent('Answer this first — 1 earlier ping queued');
		expect(screen.getByText('3 pending across 2 runs')).toBeInTheDocument();
	});

	it('names the run, its tracker key and its workshop on a card', async () => {
		fleet.projects = [aProject({ id: 'p1', name: 'billing' })];
		vi.mocked(api.tickets.list).mockResolvedValue([
			aTicket({ id: 't1', source: { key: 'PROJ-7' } as never })
		]);
		loaded(
			[ping('a', 'r1', 1)],
			[aRun({ id: 'r1', title: 'Fix the invoice', projectId: 'p1', ticketId: 't1' })]
		);
		render(Inbox);

		const card = within(cards()[0]);
		expect(card.getByText('Fix the invoice')).toBeInTheDocument();
		expect(card.getByText('billing')).toBeInTheDocument();
		expect(await card.findByText('PROJ-7')).toBeInTheDocument();
	});

	it('falls back to a short id for a run the fleet does not have', () => {
		loaded([ping('a', '01GONERUNWXYZ', 1)], []);
		render(Inbox);
		expect(within(cards()[0]).getByText('r_wxyz')).toBeInTheDocument();
	});
});

describe('Inbox, filtering by type', () => {
	it('offers the filter only with more than one type', () => {
		loaded([ping('a', 'r1', 1), ping('b', 'r2', 2)]);
		render(Inbox);
		expect(screen.queryByRole('radiogroup', { name: 'Ping type' })).not.toBeInTheDocument();
	});

	it('narrows the list to one type', async () => {
		loaded([ping('a', 'r1', 1), ping('b', 'r2', 2, { type: 'blocker', answerKind: 'form' })]);
		render(Inbox);

		const filter = within(screen.getByRole('radiogroup', { name: 'Ping type' }));
		expect(filter.getByRole('radio', { name: /All/ })).toHaveTextContent('2');
		await userEvent.click(filter.getByRole('radio', { name: /Blocker/i }));

		await waitFor(() => expect(cards()).toHaveLength(1));
		expect(cards()[0]).toHaveTextContent('Question b?');
	});
});

describe('Inbox, selection', () => {
	it('starts on the first card', () => {
		loaded([ping('a', 'r1', 1), ping('b', 'r2', 2)]);
		render(Inbox);
		expect(selected()).toHaveTextContent('Question b?');
	});

	it('opens a deep link', () => {
		loaded([ping('a', 'r1', 1), ping('b', 'r2', 2)]);
		render(Inbox, { initial: 'a' });
		expect(selected()).toHaveTextContent('Question a?');
	});

	it("shows a queued ping's head when the link names the older one", () => {
		loaded([ping('old', 'r1', 1), ping('new', 'r1', 2)]);
		render(Inbox, { initial: 'old' });
		expect(selected()).toHaveTextContent('Question new?');
		expect(screen.getByText('Queued behind this one')).toBeInTheDocument();
	});

	it('falls back to the first card for a link to a ping no longer pending', () => {
		loaded([ping('a', 'r1', 1)]);
		render(Inbox, { initial: 'gone' });
		expect(selected()).toHaveTextContent('Question a?');
	});

	it('mirrors a pick in the URL, without navigating', async () => {
		loaded([ping('a', 'r1', 1), ping('b', 'r2', 2)]);
		render(Inbox);
		await userEvent.click(cards()[1]);

		expect(selected()).toHaveTextContent('Question a?');
		expect(replaceState).toHaveBeenCalledWith('/inbox?ping=a', {});
		expect(goto).not.toHaveBeenCalled();
	});

	it('holds the pick when the router is not ready', async () => {
		vi.mocked(replaceState).mockImplementation(() => {
			throw new Error('router not ready');
		});
		loaded([ping('a', 'r1', 1), ping('b', 'r2', 2)]);
		render(Inbox);
		await userEvent.click(cards()[1]);
		expect(selected()).toHaveTextContent('Question a?');
	});

	it('lands on the card that slid into place when a run has no ping left', async () => {
		loaded(
			[ping('a', 'r1', 1), ping('b', 'r2', 2), ping('c', 'r3', 3)],
			[aRun({ id: 'r1' }), aRun({ id: 'r2' }), aRun({ id: 'r3' })]
		);
		render(Inbox, { initial: 'b' });
		expect(selected()).toHaveTextContent('Question b?');

		fleet.pending = [ping('a', 'r1', 1), ping('c', 'r3', 3)];
		await waitFor(() => expect(selected()).toHaveTextContent('Question a?'));
	});
});

describe('Inbox, the selected ping', () => {
	it('shows the run strip with a link to the run', () => {
		loaded(
			[ping('a', 'r1', 1)],
			[
				aRun({
					id: 'r1',
					title: 'Fix the invoice',
					currentStage: 'build',
					state: 'waiting_on_human'
				})
			]
		);
		render(Inbox);
		const detail = screen.getByRole('link', { name: /Open run/ });
		expect(detail).toHaveAttribute('href', '/runs/r1');
		expect(screen.getAllByText('build').length).toBeGreaterThan(0);
	});

	it('lists what queues behind it', () => {
		loaded([ping('old', 'r1', 1, { type: 'blocker' }), ping('new', 'r1', 2)]);
		render(Inbox);
		expect(screen.getByText('Queued behind this one')).toBeInTheDocument();
		expect(screen.getByText('Question old?')).toBeInTheDocument();
	});
});

describe('Inbox, answering', () => {
	it('sends the answer, refreshes the fleet and offers the run', async () => {
		loaded([ping('a', 'r1', 1)]);
		vi.mocked(api.pings.answer).mockResolvedValue(undefined as never);
		render(Inbox);
		await userEvent.click(approve());

		expect(api.pings.answer).toHaveBeenCalledWith('a', {
			answer: 'approve',
			feedback: undefined,
			attachmentIds: undefined,
			content: undefined
		});
		await waitFor(() => expect(api.pings.pending).toHaveBeenCalled());
		await waitFor(() => expect(toasts.list.map((x) => x.text)).toContain('Answer sent'));

		toasts.list.at(-1)!.action!.run();
		expect(goto).toHaveBeenCalledWith('/runs/r1');
	});

	it('sends changes with their feedback, trimmed', async () => {
		loaded([ping('a', 'r1', 1)]);
		vi.mocked(api.pings.answer).mockResolvedValue(undefined as never);
		render(Inbox);

		await userEvent.click(screen.getByRole('button', { name: /Request changes/ }));
		await userEvent.type(screen.getByRole('textbox'), '  tighten it up ');
		await userEvent.click(screen.getByRole('button', { name: 'Send changes' }));

		expect(api.pings.answer).toHaveBeenCalledWith(
			'a',
			expect.objectContaining({ answer: 'reject', feedback: 'tighten it up' })
		);
	});

	it('holds the card while the answer travels', async () => {
		loaded([ping('a', 'r1', 1)]);
		const sent = deferred<never>();
		vi.mocked(api.pings.answer).mockReturnValue(sent.promise);
		render(Inbox);

		await userEvent.click(approve());
		await waitFor(() => expect(approve()).toBeDisabled());

		sent.resolve(undefined as never);
		await waitFor(() => expect(approve()).toBeEnabled());
	});

	it('says so when the answer fails, and keeps the ping', async () => {
		loaded([ping('a', 'r1', 1)]);
		vi.mocked(api.pings.answer).mockRejectedValue(new Error('already answered'));
		render(Inbox);
		await userEvent.click(approve());

		expect(await screen.findByText('The answer did not go through')).toBeInTheDocument();
		expect(screen.getByText('already answered')).toBeInTheDocument();
		expect(cards()).toHaveLength(1);
		expect(toasts.list).toEqual([]);
	});

	it('points a deep link at the inbox once its ping is answered', async () => {
		history.replaceState(null, '', '/inbox?ping=a');
		loaded([ping('a', 'r1', 1)]);
		vi.mocked(api.pings.answer).mockResolvedValue(undefined as never);
		render(Inbox, { initial: 'a' });
		await userEvent.click(approve());

		await waitFor(() => expect(replaceState).toHaveBeenCalledWith('/inbox', {}));
	});

	it("moves on to the run's next ping once the head is answered", async () => {
		loaded([ping('old', 'r1', 1), ping('new', 'r1', 2)]);
		vi.mocked(api.pings.answer).mockResolvedValue(undefined as never);
		vi.mocked(api.pings.pending).mockResolvedValue([ping('old', 'r1', 1)]);
		render(Inbox);
		await userEvent.click(approve());

		await waitFor(() => expect(selected()).toHaveTextContent('Question old?'));
	});
});

describe('Inbox, the answered tab', () => {
	const history_ = () => screen.getByRole('radio', { name: /Answered/ });

	it('opens on it, loading once, skeleton first', async () => {
		const list = deferred<ReturnType<typeof aPing>[]>();
		vi.mocked(api.pings.list).mockReturnValue(list.promise);
		render(Inbox, { initialTab: 'answered' });
		await waitFor(() => expect(api.pings.list).toHaveBeenCalledWith('answered'));

		list.resolve([]);
		expect(await screen.findByText('No answers yet')).toBeInTheDocument();
	});

	it('loads when switched to, and counts what it found', async () => {
		vi.mocked(api.pings.list).mockResolvedValue([
			aPing({ id: 'x', status: 'answered', answer: 'approve', question: 'Done?', raisedAt: at(1) })
		]);
		render(Inbox);
		expect(api.pings.list).not.toHaveBeenCalled();

		await userEvent.click(history_());
		expect(await screen.findByRole('link', { name: 'Done?' })).toHaveAttribute('href', '/inbox/x');
		expect(history_()).toHaveTextContent('1');
	});

	it('shows the answer and its feedback', async () => {
		vi.mocked(api.pings.list).mockResolvedValue([
			aPing({
				id: 'x',
				runId: 'r1',
				status: 'answered',
				answer: 'reject',
				feedback: 'not yet',
				stageId: 'plan',
				question: 'Done?',
				raisedAt: at(1)
			})
		]);
		fleet.runs = [aRun({ id: 'r1', title: 'Fix the invoice' })];
		render(Inbox, { initialTab: 'answered' });

		expect(await screen.findByText(/reject/)).toBeInTheDocument();
		expect(screen.getByText(/not yet/)).toBeInTheDocument();
		expect(screen.getByText(/Fix the invoice/)).toBeInTheDocument();
	});

	it('offers a retry when the history fails', async () => {
		vi.mocked(api.pings.list).mockRejectedValueOnce(new Error('down'));
		render(Inbox, { initialTab: 'answered' });
		expect(await screen.findByText('Could not load the history')).toBeInTheDocument();

		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('No answers yet')).toBeInTheDocument();
	});

	it('polls the history every eight seconds, only on its tab and only when visible', async () => {
		vi.useFakeTimers();
		const view = render(Inbox);
		await vi.advanceTimersByTimeAsync(8000);
		expect(api.pings.list).not.toHaveBeenCalled();
		view.unmount();

		render(Inbox, { initialTab: 'answered' });
		await vi.advanceTimersByTimeAsync(0);
		expect(api.pings.list).toHaveBeenCalledTimes(1);

		await vi.advanceTimersByTimeAsync(8000);
		expect(api.pings.list).toHaveBeenCalledTimes(2);

		const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);
		await vi.advanceTimersByTimeAsync(8000);
		expect(api.pings.list).toHaveBeenCalledTimes(2);
		hidden.mockRestore();
		vi.useRealTimers();
	});
});
